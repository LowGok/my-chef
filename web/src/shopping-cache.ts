import type { Profile } from "./api";
import type { ShoppingList, QueuedShoppingEdit } from "./shopping-state";

const prefix = "mychef.shopping.";
export type ShoppingStorage = Pick<
  Storage,
  "length" | "key" | "getItem" | "setItem" | "removeItem"
> & {
  flush(): Promise<void>;
  erase(): Promise<void>;
};
let nativeStorage: ShoppingStorage | null = null;
let cacheEpoch = 0;
export const shoppingCacheEpoch = () => cacheEpoch;
export class ShoppingStorageError extends Error {
  constructor() {
    super("Shopping could not be saved securely on this device.");
  }
}
export function setShoppingStorage(value: ShoppingStorage | null) {
  cacheEpoch++;
  clearPrivateShoppingCache();
  nativeStorage = value;
}
const storage = () => nativeStorage ?? localStorage;
export const flushShoppingCache = () =>
  nativeStorage?.flush() ?? Promise.resolve();
export function forgetShoppingDevice() {
  cacheEpoch++;
  clearPrivateShoppingCache();
  return nativeStorage?.erase() ?? Promise.resolve();
}
export const signedOut = () => storage().getItem(prefix + "signed-out") === "1";
export const shoppingLogoutIntentKey = prefix + "logout-pending";
export const pendingLogoutIntent = () =>
  storage().getItem(shoppingLogoutIntentKey);
export const logoutPending = () => pendingLogoutIntent() !== null;
export type ShoppingCache = {
  base: ShoppingList;
  queue: QueuedShoppingEdit[];
  reviewRequired?: true;
  privateImports?: true;
};
const privateLists = new Map<
  string,
  { instance: string | undefined; value: ShoppingCache }
>();
export function clearPrivateShoppingCache() {
  privateLists.clear();
}
const privateKey = (owner: string, week: string) => `${owner}/${week}`;
function privateList(owner: string, week: string): ShoppingCache | null {
  const profile = cachedOwner()?.profile,
    saved = privateLists.get(privateKey(owner, week));
  return profile?.id === owner &&
    saved &&
    saved.instance === profile.accountInstance
    ? structuredClone(saved.value)
    : null;
}
export type CachedOwner = { profile: Profile; week: string; verified?: true };
function read<T>(key: string): T | null {
  try {
    return JSON.parse(storage().getItem(prefix + key) ?? "null") as T | null;
  } catch {
    return null;
  }
}
export function cachedOwner(): CachedOwner | null {
  const value = read<CachedOwner>("owner");
  return value &&
    typeof value.profile?.id === "string" &&
    typeof value.week === "string"
    ? value
    : null;
}
export function offlineOwner(): CachedOwner | null {
  const owner = cachedOwner();
  return !signedOut() &&
    storage().getItem(prefix + "suspended") !== "1" &&
    owner?.verified === true
    ? owner
    : null;
}
export function suspendShoppingOwner() {
  storage().setItem(prefix + "suspended", "1");
}
export function resumeShoppingOwner(owner: string) {
  const saved = cachedOwner();
  if (saved?.profile.id !== owner) return;
  if (saved.verified !== true)
    storage().setItem(
      prefix + "owner",
      JSON.stringify({ ...saved, verified: true }),
    );
  storage().removeItem(prefix + "suspended");
}
export function rememberShoppingOwner(
  profile: Profile,
  week: string,
  expectedEpoch?: number,
) {
  if (expectedEpoch !== undefined && expectedEpoch !== cacheEpoch) return;
  if (signedOut()) return;
  const current = cachedOwner();
  if (current && current.profile.id !== profile.id)
    throw new Error("Sign out before switching accounts.");
  if (
    nativeStorage &&
    current &&
    current.profile.accountInstance !== profile.accountInstance
  )
    throw new ShoppingStorageError();
  storage().setItem(
    prefix + "owner",
    JSON.stringify({ profile, week, verified: true }),
  );
  storage().removeItem(prefix + "suspended");
}
export function loadShoppingCache(
  owner: string,
  week: string,
): ShoppingCache | null {
  const value =
    privateList(owner, week) ?? read<ShoppingCache>(`list.${owner}.${week}`);
  return value?.base?.week === week &&
    Array.isArray(value.queue) &&
    Array.isArray(value.base.items)
    ? value
    : null;
}
export function saveShoppingCache(
  owner: string,
  week: string,
  value: ShoppingCache,
  accountInstance?: string,
  expectedEpoch?: number,
) {
  if (expectedEpoch !== undefined && expectedEpoch !== cacheEpoch) return;
  if (signedOut() || cachedOwner()?.profile.id !== owner) return;
  if (
    accountInstance &&
    cachedOwner()?.profile.accountInstance !== accountInstance
  )
    return;
  const key = privateKey(owner, week);
  const keepPrivate =
    value.base.privateImports ||
    value.privateImports ||
    (value.queue.length > 0 && privateList(owner, week) !== null);
  if (keepPrivate) {
    privateLists.set(key, {
      instance: cachedOwner()?.profile.accountInstance,
      value: structuredClone({ ...value, privateImports: true as const }),
    });
    storage().removeItem(prefix + `list.${owner}.${week}`);
  } else {
    privateLists.delete(key);
    storage().setItem(prefix + `list.${owner}.${week}`, JSON.stringify(value));
  }
  window.dispatchEvent(new Event("mychef-shopping-cache"));
}
export function pendingShoppingWeeks(owner: string): string[] {
  const weeks: string[] = [];
  for (const key of privateLists.keys()) {
    if (!key.startsWith(owner + "/")) continue;
    const week = key.slice(owner.length + 1);
    if (privateList(owner, week)?.queue.length) weeks.push(week);
  }
  const ownerPrefix = prefix + `list.${owner}.`;
  for (let index = 0; index < storage().length; index++) {
    const key = storage().key(index);
    if (!key?.startsWith(ownerPrefix)) continue;
    const week = key.slice(ownerPrefix.length);
    if (
      /^\d{4}-\d{2}-\d{2}$/.test(week) &&
      loadShoppingCache(owner, week)?.queue.length
    )
      weeks.push(week);
  }
  return [...new Set(weeks)].sort();
}
export function hasPendingShopping(owner: string): boolean {
  if (
    [...privateLists.keys()].some(
      (key) =>
        key.startsWith(owner + "/") &&
        privateList(owner, key.slice(owner.length + 1))?.queue.length,
    )
  )
    return true;
  for (let i = 0; i < storage().length; i++) {
    const key = storage().key(i);
    if (key?.startsWith(prefix + `list.${owner}.`)) {
      try {
        if (JSON.parse(storage().getItem(key) ?? "null")?.queue?.length)
          return true;
      } catch {
        /* A broken cache must not be silently discarded. */ return true;
      }
    }
  }
  return false;
}
export function localShoppingSignOut(preserveCache = false) {
  if (nativeStorage) {
    void forgetShoppingDevice().catch(() => undefined);
    preserveCache = false;
  }
  clearPrivateShoppingCache();
  // This marker is durable before private state disappears from the UI.
  storage().setItem(prefix + "signed-out", "1");
  const intent = Array.from(crypto.getRandomValues(new Uint32Array(4)), (n) =>
    n.toString(16).padStart(8, "0"),
  ).join("");
  storage().setItem(shoppingLogoutIntentKey, intent);
  if (preserveCache) {
    suspendShoppingOwner();
    return;
  }
  const remove: string[] = [];
  for (let i = 0; i < storage().length; i++) {
    const key = storage().key(i);
    if (key?.startsWith(prefix + "list.")) remove.push(key);
  }
  for (const key of remove) storage().removeItem(key);
  storage().removeItem(prefix + "owner");
  storage().removeItem(prefix + "suspended");
}
export function serverLogoutCompleted(intent: string) {
  if (pendingLogoutIntent() === intent)
    storage().removeItem(shoppingLogoutIntentKey);
}
export function explicitShoppingSignIn(restricted = false) {
  if (restricted) suspendShoppingOwner();
  else storage().removeItem(prefix + "suspended");
  storage().removeItem(prefix + "signed-out");
}
