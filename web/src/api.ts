import {
  cachedOwner,
  clearPrivateShoppingCache,
  localShoppingSignOut,
  explicitShoppingSignIn,
  signedOut,
  logoutPending,
  pendingLogoutIntent,
  serverLogoutCompleted,
  ShoppingStorageError,
} from "./shopping-cache";
import type { components, DeleteAccountData } from "./api.generated";
import {
  platformRequest,
  forgetPlatformSession,
  isNativeTransport,
  PlatformStorageError,
} from "./platform";
import {
  mediaPlatform,
  clearPlatformMedia,
  NativeMediaError,
} from "./platform-media";

export type Profile = components["schemas"]["Profile"];
export type Session = components["schemas"]["Session"];
export type Recipe = components["schemas"]["Recipe"];
export type MenuInput = components["schemas"]["MenuInput"];
export type Operation = components["schemas"]["Generation"];
export type Menu = components["schemas"]["Menu"];
export type RecipePage = components["schemas"]["RecipePage"];
export type ProfileUpdate = components["schemas"]["ProfileUpdate"];
export type PersonalRecipe = components["schemas"]["PersonalRecipe"];
export type PersonalContent = components["schemas"]["PersonalRecipeContent"];
export type PersonalSaveInput = components["schemas"]["PersonalSaveInput"];
export type PersonalErrorDetails = {
  fields?: Record<string, string>;
  current?: PersonalRecipe;
  changedFields?: string[];
  existingId?: string;
};

export type FeedbackInput = components["schemas"]["FeedbackInput"];
export type FeedbackReceipt = components["schemas"]["FeedbackReceipt"];
export type FeedbackRating = components["schemas"]["FeedbackRating"];
export type FeedbackAvailability =
  components["schemas"]["FeedbackAvailability"];
export type FeedbackPointer = components["schemas"]["FeedbackReceiptInput"];
export type PendingFeedback =
  | { operation: "feedback"; input: FeedbackInput }
  | {
      operation: "rating";
      input: { requestKey: string; rating: number };
      recipeId: string;
    };
const feedbackPrefix = "mychef.feedback.";
let memoryPointer: { owner: string; pointer: FeedbackPointer } | null = null;
let feedbackDraft: PendingFeedback | null = null;

export function readFeedbackDraft(
  owner: string,
  pointer: FeedbackPointer | null,
) {
  return owner === identity &&
    pointer?.operation === feedbackDraft?.operation &&
    pointer?.requestKey === feedbackDraft?.input.requestKey
    ? feedbackDraft
    : null;
}

export function writeFeedbackDraft(
  owner: string,
  draft: PendingFeedback | null,
) {
  if (owner === identity) feedbackDraft = draft;
}

function clearOtherFeedbackPointers(owner: string | null) {
  if (memoryPointer?.owner !== owner) memoryPointer = null;
  try {
    const keep = owner === null ? "" : feedbackPrefix + owner;
    for (const key of Object.keys(sessionStorage))
      if (key.startsWith(feedbackPrefix) && key !== keep)
        sessionStorage.removeItem(key);
  } catch {
    // Receipt recovery across reload is unavailable when storage is disabled.
  }
}

export function readFeedbackPointer(owner: string): FeedbackPointer | null {
  try {
    if (owner !== identity) return null;
    if (memoryPointer?.owner === owner) return memoryPointer.pointer;
    const saved = JSON.parse(
      sessionStorage.getItem(feedbackPrefix + owner) ?? "null",
    );
    if (
      (saved?.operation === "feedback" || saved?.operation === "rating") &&
      typeof saved.requestKey === "string" &&
      /^[0-9a-f]{32}$/.test(saved.requestKey)
    )
      return { operation: saved.operation, requestKey: saved.requestKey };
  } catch {
    // Treat unavailable or invalid storage as empty.
  }
  return null;
}

export function writeFeedbackPointer(
  owner: string,
  pointer: FeedbackPointer | null,
) {
  if (owner !== identity) return;
  memoryPointer = pointer ? { owner, pointer } : null;
  try {
    if (pointer)
      sessionStorage.setItem(feedbackPrefix + owner, JSON.stringify(pointer));
    else sessionStorage.removeItem(feedbackPrefix + owner);
  } catch {
    // This view still supports exact retries in memory.
  }
}

let csrf = "";
let sessionPromise: Promise<Session> | null = null;
let epoch = 0;
let identity: string | null = null;
let accountInstance: string | null = null;
let restricted = false;
let pendingPasswordChange: Promise<Session> | null = null;
let fallbackSessionLock: Promise<void> = Promise.resolve();
const passwordPath = "/account/password";

async function withSessionLock<T>(work: () => Promise<T>): Promise<T> {
  if (navigator.locks)
    return await navigator.locks.request("mychef-session-cookie", work);
  const result = fallbackSessionLock.then(work);
  fallbackSessionLock = result.then(
    () => undefined,
    () => undefined,
  );
  return result;
}
export const SESSION_LOST = "mychef-session-lost";
export const browserSignInKey = "mychef.browser-sign-in";

function notifyBrowserSignIn(): void {
  try {
    // Other tabs need only know the shared cookie changed. Keep no identity
    // or credential in storage, and remove the transient event immediately.
    localStorage.setItem(browserSignInKey, "1");
    localStorage.removeItem(browserSignInKey);
  } catch {
    // Storage can be disabled; server-side session checks still apply.
  }
}
export const sessionEpoch = (): number => epoch;
export const isStale = (error: unknown): boolean =>
  error instanceof APIError && error.code === "stale_session";
export function forgetSession(): void {
  clearPlatformMedia();
  forgetPlatformSession();
  clearPrivateShoppingCache();
  clearOtherFeedbackPointers(null);
  feedbackDraft = null;
  epoch++;
  identity = null;
  accountInstance = null;
  restricted = false;
  csrf = "";
  sessionPromise = null;
}
export class APIError extends Error {
  constructor(
    message: string,
    public code: string,
    public status: number,
    public requestId?: string,
    public details?: PersonalErrorDetails,
  ) {
    super(message);
  }
}
export function acceptSession(value: Session, rotated = false): void {
  const saved = cachedOwner();
  if (
    value.user?.accountInstance &&
    saved?.profile.id === value.user.id &&
    saved.profile.accountInstance !== value.user.accountInstance
  ) {
    // Numeric IDs can be reused. Old offline data belongs to the old account.
    localShoppingSignOut();
    const intent = pendingLogoutIntent();
    if (intent) serverLogoutCompleted(intent);
    explicitShoppingSignIn();
  }
  const verificationRequired = value.verificationRequired === true;
  clearOtherFeedbackPointers(
    verificationRequired ? null : (value.user?.id ?? null),
  );
  if (
    rotated ||
    csrf !== value.csrfToken ||
    identity !== (value.user?.id ?? null) ||
    accountInstance !== (value.user?.accountInstance ?? null) ||
    restricted !== verificationRequired
  ) {
    epoch++;
    clearPlatformMedia();
    feedbackDraft = null;
  }
  csrf = value.csrfToken;
  identity = value.user?.id ?? null;
  accountInstance = value.user?.accountInstance ?? null;
  restricted = verificationRequired;
}
const recoveryRequestPath = "/password-recovery/request";
const recoveryCompletePath = "/password-recovery/complete";

export function sessionHasAccount(): boolean {
  return identity !== null || restricted;
}

// Private actions must not run against an incomplete/legacy session or a
// superseded account scope. The server remains authoritative for permissions.
export function personalAccountReady(): boolean {
  return (
    !restricted &&
    identity !== null &&
    /^[1-9][0-9]{0,18}$/.test(identity) &&
    accountInstance !== null &&
    /^[0-9a-f]{32}$/.test(accountInstance)
  );
}

export function requestPasswordRecovery(
  input: components["schemas"]["RecoveryRequest"],
  signal: AbortSignal,
) {
  return api<components["schemas"]["EmailRequestAccepted"]>(
    recoveryRequestPath,
    {
      method: "POST",
      body: JSON.stringify(input),
      signal,
    },
  );
}

export function completePasswordRecovery(
  input: components["schemas"]["RecoveryCompletion"],
  signal: AbortSignal,
) {
  return api<components["schemas"]["RecoveryResult"]>(recoveryCompletePath, {
    method: "POST",
    body: JSON.stringify(input),
    signal,
  });
}

const publicProofPaths = [
  recoveryRequestPath,
  recoveryCompletePath,
  "/signup/inspect",
  "/signup/complete",
  "/email/change/inspect",
  "/email/change/decision",
];
const verifyPath = "/email/verification/confirm";
const sessionPaths = ["/session", "/logout", "/login", "/register"];

export async function api<T>(
  path: string,
  options: RequestInit = {},
  responseKind: "json" | "download" = "json",
): Promise<T> {
  if (signedOut() && ![...sessionPaths, ...publicProofPaths].includes(path))
    throw new APIError("Please sign in.", "unauthenticated", 401);
  if (
    logoutPending() &&
    ["/login", "/register", ...publicProofPaths].includes(path)
  )
    throw new APIError(
      "Finish signing out before signing in again.",
      "logout_pending",
      409,
    );
  const started = epoch,
    owner = identity;
  const headers = new Headers(options.headers);
  if (options.body && !(options.body instanceof FormData))
    headers.set("Content-Type", "application/json");
  if (options.method && options.method !== "GET")
    headers.set("X-CSRF-Token", csrf);
  let response: Response;
  try {
    response = await platformRequest(`/api/v1${path}`, {
      ...options,
      headers,
      credentials: "same-origin",
      cache: "no-store",
    });
  } catch (error) {
    if (started !== epoch) throw new APIError("", "stale_session", 409);
    if (
      error instanceof PlatformStorageError ||
      error instanceof ShoppingStorageError ||
      error instanceof NativeMediaError
    )
      throw new APIError(
        "Secure storage is unavailable. Unlock your device and try again.",
        "device_storage",
        503,
      );
    throw new APIError(
      "The connection was interrupted. Check the request status before starting another menu.",
      "network_uncertain",
      0,
    );
  }
  // A request issued before password rotation can get a 401 before the
  // rotation response reaches this tab. Let that response advance the epoch.
  if (
    response.status === 401 &&
    path !== passwordPath &&
    !["/session", "/login", "/register", "/logout"].includes(path) &&
    pendingPasswordChange
  ) {
    await pendingPasswordChange.catch(() => undefined);
  }
  if (started !== epoch) throw new APIError("", "stale_session", 409);
  const authEndpoint = [
    ...sessionPaths,
    ...publicProofPaths,
    verifyPath,
  ].includes(path);
  const responseOwner = response.headers.get("X-MyChef-User");
  if (
    !authEndpoint &&
    owner &&
    ((responseOwner && responseOwner !== owner) ||
      (response.ok && !responseOwner))
  ) {
    forgetSession();
    window.dispatchEvent(new Event(SESSION_LOST));
    throw new APIError("Your browser session changed.", "stale_session", 409);
  }
  if (!response.ok) {
    const data = await response.json().catch(() => null);
    if (
      response.status === 401 &&
      !authEndpoint &&
      path !== "/logout" &&
      started === epoch &&
      !signedOut()
    ) {
      forgetSession();
      window.dispatchEvent(new Event(SESSION_LOST));
    }
    throw new APIError(
      data?.error?.message ?? "The service could not complete this request.",
      data?.error?.code ?? "unavailable",
      response.status,
      data?.error?.requestId,
      data?.error,
    );
  }
  const value =
    response.status === 204
      ? undefined
      : responseKind === "download"
        ? await readAccountDownload(response)
        : await response.json();
  if (started !== epoch) throw new APIError("", "stale_session", 409);
  return value as T;
}
async function readSessionInLock(): Promise<Session> {
  const value = await api<Session>("/session");
  acceptSession(value);
  return value;
}

export function getSession(): Promise<Session> {
  if (!sessionPromise) {
    const current = withSessionLock(readSessionInLock).finally(() => {
      if (sessionPromise === current) sessionPromise = null;
    });
    sessionPromise = current;
  }
  return sessionPromise;
}
export const message = (error: unknown): string =>
  error instanceof APIError || error instanceof NativeMediaError
    ? error.message
    : "The request could not be completed. Please try again.";

async function finishLogoutInLock(): Promise<void> {
  for (
    let intent = pendingLogoutIntent();
    intent;
    intent = pendingLogoutIntent()
  ) {
    const value = await readSessionInLock();
    // A newer shared intent supersedes the old one.
    if (pendingLogoutIntent() !== intent) continue;
    if (value.user || value.verificationRequired) {
      try {
        await api<void>("/logout", { method: "POST", body: "{}" });
      } catch (err) {
        if (!(err instanceof APIError) || err.status !== 401) throw err;
      }
    }
    forgetSession();
    serverLogoutCompleted(intent);
  }
}

let logoutCompletion: Promise<void> | null = null;
export function completeLocalLogout(): Promise<void> {
  if (!logoutPending()) return Promise.resolve();
  if (logoutCompletion) return logoutCompletion;
  const completion = withSessionLock(finishLogoutInLock).finally(() => {
    if (logoutCompletion === completion) logoutCompletion = null;
  });
  logoutCompletion = completion;
  return completion;
}

export function signIn(body: string): Promise<Session> {
  return withSessionLock(async () => {
    await finishLogoutInLock();
    await readSessionInLock();
    if (logoutPending()) throw new APIError("", "stale_session", 409);
    const value = await api<Session>("/login", {
      method: "POST",
      body,
    });
    if (logoutPending()) {
      await finishLogoutInLock();
      throw new APIError("", "stale_session", 409);
    }
    const saved = cachedOwner();
    if (
      value.verificationRequired ||
      !saved ||
      saved.profile.id === value.user?.id
    ) {
      acceptSession(value, isNativeTransport());
      explicitShoppingSignIn(value.verificationRequired);
    }
    notifyBrowserSignIn();
    return value;
  });
}

export function requestSignUp(
  body: components["schemas"]["Registration"],
): Promise<components["schemas"]["EmailRequestAccepted"]> {
  return withSessionLock(async () => {
    await finishLogoutInLock();
    await readSessionInLock();
    return api("/register", { method: "POST", body: JSON.stringify(body) });
  });
}

export function confirmEmailVerification(proof: string): Promise<Session> {
  const started = epoch;
  return withSessionLock(async () => {
    if (started !== epoch || !restricted || signedOut() || logoutPending())
      throw new APIError("", "stale_session", 409);
    const value = await api<Session>(verifyPath, {
      method: "POST",
      body: JSON.stringify({ proof }),
    });
    if (logoutPending()) {
      await finishLogoutInLock();
      throw new APIError("", "stale_session", 409);
    }
    if (!value.user || started !== epoch || signedOut())
      throw new APIError("", "stale_session", 409);
    acceptSession(value, isNativeTransport());
    return value;
  });
}

export const canChangePassword = () => isNativeTransport() || !!navigator.locks;

export function changePassword(
  body: components["schemas"]["PasswordChange"],
): Promise<Session> {
  if (!canChangePassword())
    return Promise.reject(
      new APIError(
        "Open MyChef in a supported secure browser to change your password.",
        "password_browser",
        400,
      ),
    );
  const owner = identity;
  const started = epoch;
  const operation = withSessionLock(async () => {
    if (!owner || started !== epoch || signedOut() || logoutPending())
      throw new APIError("", "stale_session", 409);
    const value = await api<Session>(passwordPath, {
      method: "PUT",
      body: JSON.stringify(body),
    });
    if (
      started !== epoch ||
      signedOut() ||
      logoutPending() ||
      value.user?.id !== owner
    )
      throw new APIError("", "stale_session", 409);
    acceptSession(value, isNativeTransport());
    return value;
  }).finally(() => {
    if (pendingPasswordChange === operation) pendingPasswordChange = null;
  });
  pendingPasswordChange = operation;
  return operation;
}

export type AccountDownload = { blob: Blob; filename: string };

async function readAccountDownload(
  response: Response,
): Promise<AccountDownload> {
  const match = /^attachment; filename="(mychef-data-[0-9a-f]{32}\.zip)"$/.exec(
    response.headers.get("Content-Disposition") ?? "",
  );
  const incomplete = () =>
    new APIError(
      "The download could not be completed. Try again.",
      "incomplete_export",
      0,
    );
  if (!match || response.headers.get("Content-Type") !== "application/zip")
    throw incomplete();
  let blob: Blob;
  try {
    blob = await response.blob();
  } catch {
    throw incomplete();
  }
  if (blob.size < 22) throw incomplete();
  // The server writes the ZIP end record only after its snapshot succeeds.
  const footer = new DataView(await blob.slice(-22).arrayBuffer());
  if (footer.getUint32(0, true) !== 0x06054b50) throw incomplete();
  return { blob, filename: match[1] };
}

export function requestAccountExport(
  currentPassword: string,
  signal: AbortSignal,
): Promise<AccountDownload> {
  return api<AccountDownload>(
    "/account/export",
    { method: "POST", body: JSON.stringify({ currentPassword }), signal },
    "download",
  );
}

export async function saveAccountDownload(
  value: AccountDownload,
): Promise<boolean> {
  const port = mediaPlatform();
  if (port) return port.saveExport(value);
  const url = URL.createObjectURL(value.blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = value.filename;
  document.body.append(link);
  try {
    link.click();
  } finally {
    link.remove();
    // Allow Safari to start its download before releasing the memory-only URL.
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
  return true;
}

export function deleteOwnAccount(
  input: DeleteAccountData["body"],
  operation: string,
  signal: AbortSignal,
): Promise<void> {
  const started = epoch;
  return withSessionLock(async () => {
    if (
      started !== epoch ||
      !sessionHasAccount() ||
      signedOut() ||
      logoutPending()
    )
      throw new APIError("", "stale_session", 409);
    await api<components["schemas"]["AccountDeletionResult"]>(
      "/account/deletion",
      {
        method: "POST",
        body: JSON.stringify(input),
        signal,
        headers: { "Idempotency-Key": operation },
      },
    );
    if (started !== epoch) throw new APIError("", "stale_session", 409);
    localShoppingSignOut();
    const intent = pendingLogoutIntent();
    if (intent) serverLogoutCompleted(intent);
    forgetSession();
    notifyBrowserSignIn();
  });
}

// Personal content and search terms stay in request bodies and memory. These
// helpers share the session epoch checks used by every other account request.
export const personalApi = {
  permissions: (signal?: AbortSignal) =>
    api<components["schemas"]["PersonalPermissions"]>(
      "/personal-recipes/permissions",
      { signal },
    ),
  search: (
    input: components["schemas"]["PersonalSearch"],
    signal?: AbortSignal,
  ) =>
    api<components["schemas"]["PersonalRecipePage"]>(
      "/personal-recipes/search",
      { method: "POST", body: JSON.stringify(input), signal },
    ),
  read: (id: string, signal?: AbortSignal, version?: string) =>
    api<PersonalRecipe>(
      `/personal-recipes/${encodeURIComponent(id)}${version ? `?version=${encodeURIComponent(version)}` : ""}`,
      { signal },
    ),
  copy: (recipeId: string, signal?: AbortSignal) =>
    api<components["schemas"]["PersonalCopy"]>("/personal-recipes/copy", {
      method: "POST",
      body: JSON.stringify({ recipeId }),
      signal,
    }),
  save: (
    id: string | null,
    input: PersonalSaveInput,
    photo: Blob | null,
    signal?: AbortSignal,
  ) => {
    let body: string | FormData = JSON.stringify(input);
    if (photo) {
      body = new FormData();
      body.append("recipe", JSON.stringify(input));
      body.append("photo", photo, "recipe.jpg");
    }
    return api<PersonalRecipe>(
      `/personal-recipes${id ? `/${encodeURIComponent(id)}` : ""}`,
      { method: id ? "PUT" : "POST", body, signal },
    );
  },
  remove: (
    id: string,
    expectedVersion: string,
    requestKey: string,
    signal?: AbortSignal,
  ) =>
    api<components["schemas"]["PersonalDeleteResult"]>(
      `/personal-recipes/${encodeURIComponent(id)}`,
      {
        method: "DELETE",
        body: JSON.stringify({ expectedVersion, requestKey }),
        signal,
      },
    ),
  queue: (
    id: string,
    input: components["schemas"]["PersonalQueueInput"],
    signal?: AbortSignal,
  ) =>
    api<components["schemas"]["FavoriteWeek"]>(
      `/personal-recipes/${encodeURIComponent(id)}/queue`,
      { method: "POST", body: JSON.stringify(input), signal },
    ),
  swap: (
    id: string,
    input: components["schemas"]["PersonalSwapInput"],
    signal?: AbortSignal,
  ) =>
    api<Recipe>(`/personal-recipes/${encodeURIComponent(id)}/swap`, {
      method: "POST",
      body: JSON.stringify(input),
      signal,
    }),
  removeQueued: (id: string, requestKey: string, signal?: AbortSignal) =>
    api<components["schemas"]["FavoriteWeek"]>(
      `/personal-queue/${encodeURIComponent(id)}`,
      { method: "DELETE", body: JSON.stringify({ requestKey }), signal },
    ),
};

export type ImportedDraft = components["schemas"]["ImportedDraft"];
export type RecipeImport = components["schemas"]["RecipeImport"];
export type ImportSource = components["schemas"]["ImportSource"];

// No source address, text, original filename or image is placed in a URL or
// browser storage. Session epoch and owner checks remain in api().
export const importApi = {
  availability: (signal?: AbortSignal) =>
    api<components["schemas"]["ImportAvailability"]>(
      "/recipe-imports/availability",
      { signal },
    ),
  url: (input: components["schemas"]["UrlImportInput"], signal?: AbortSignal) =>
    api<RecipeImport>("/recipe-imports/url", {
      method: "POST",
      body: JSON.stringify(input),
      signal,
    }),
  photos: (
    input: components["schemas"]["PhotoImportMetadata"],
    pages: Blob[],
    signal?: AbortSignal,
  ) => {
    const body = new FormData();
    body.append("import", JSON.stringify(input));
    for (const page of pages)
      body.append(
        "photos",
        new File([page], "recipe-page", { type: page.type }),
      );
    return api<RecipeImport>("/recipe-imports/photos", {
      method: "POST",
      body,
      signal,
    });
  },
  read: (id: string, signal?: AbortSignal) =>
    api<RecipeImport>(`/recipe-imports/${encodeURIComponent(id)}`, { signal }),
  select: (id: string, candidateId: string, signal?: AbortSignal) =>
    api<RecipeImport>(`/recipe-imports/${encodeURIComponent(id)}/select`, {
      method: "POST",
      body: JSON.stringify({ candidateId }),
      signal,
    }),
  ai: (id: string, signal?: AbortSignal) =>
    api<RecipeImport>(
      `/recipe-imports/${encodeURIComponent(id)}/read-with-ai`,
      {
        method: "POST",
        body: JSON.stringify({ aiConsentConfirmed: true }),
        signal,
      },
    ),
  review: (id: string, content: PersonalContent, signal?: AbortSignal) =>
    api<components["schemas"]["ImportReview"]>(
      `/recipe-imports/${encodeURIComponent(id)}/review`,
      { method: "POST", body: JSON.stringify({ content }), signal },
    ),
  cancel: (id: string, signal?: AbortSignal) =>
    api<void>(`/recipe-imports/${encodeURIComponent(id)}/cancel`, {
      method: "POST",
      body: "{}",
      signal,
    }),
};
