import type { components } from "./api.generated";

type NativeTokens = components["schemas"]["NativeTokens"];
export type NativeCredentialStore = {
  read(): Promise<string | null>;
  write(refresh: string | null): Promise<void>;
};
export type PlatformTransport = {
  readonly native: boolean;
  request(path: string, options?: RequestInit): Promise<Response>;
  forget(): Promise<void>;
};
type NativeOptions = {
  platform: "ios" | "android";
  credentials: NativeCredentialStore;
  send(path: string, options: RequestInit): Promise<Response>;
  now?: () => number;
  onForget?(): Promise<void>;
  beforeAccount?(user: unknown): Promise<void>;
};

let selected: PlatformTransport | null = null;
let erasing: Promise<void> = Promise.resolve();
export function setPlatformTransport(value: PlatformTransport | null) {
  selected = value;
}
export const isNativeTransport = () => selected?.native === true;
export function platformRequest(path: string, options: RequestInit = {}) {
  return selected ? selected.request(path, options) : fetch(path, options);
}
export function forgetPlatformSession() {
  // Implementations fence old responses synchronously, before asynchronous
  // protected-store cleanup. A storage fault remains a fail-closed barrier.
  erasing = selected?.forget() ?? Promise.resolve();
  void erasing.catch(() => undefined);
}
export const completePlatformErasure = () => erasing;
export class PlatformStorageError extends Error {
  constructor() {
    super("Protected device storage is unavailable.");
  }
}
class NativeResponse extends Error {
  constructor(readonly response: Response) {
    super("Native session request was rejected.");
  }
}
const mutations = new Set([
  "/api/v1/login",
  "/api/v1/register",
  "/api/v1/account/password",
  "/api/v1/email/verification/confirm",
]);
const stale = () =>
  new DOMException("The account session changed.", "AbortError");

function nativePath(path: string): string {
  if (
    !path.startsWith("/api/v1/") ||
    /[\\\r\n#]/.test(path) ||
    /%2f|%5c|%2e/i.test(path.split("?")[0])
  )
    throw new TypeError("Only app API paths are permitted.");
  const url = new URL(path, "https://app.invalid");
  if (url.pathname !== path.split("?")[0])
    throw new TypeError("API traversal is not permitted.");
  return "/api/native/v1/" + path.slice("/api/v1/".length);
}
function readTokens(value: unknown): NativeTokens {
  const token = value as NativeTokens | null;
  if (
    !token ||
    typeof token.accessToken !== "string" ||
    typeof token.refreshToken !== "string" ||
    !/^[0-9a-f]{64}$/.test(token.accessToken) ||
    !/^[0-9a-f]{64}$/.test(token.refreshToken) ||
    token.accessToken === token.refreshToken ||
    !Number.isSafeInteger(token.accessExpiresUnix) ||
    !Number.isSafeInteger(token.sessionExpiresUnix) ||
    token.accessExpiresUnix <= 0 ||
    token.accessExpiresUnix > token.sessionExpiresUnix
  )
    throw new TypeError("The native session response is invalid.");
  return token;
}

export class NativeTransport implements PlatformTransport {
  readonly native = true;
  #access: string | null = null;
  #deadline = 0;
  #epoch = 0;
  #work: Promise<void> | null = null;
  #storage: Promise<void> = Promise.resolve();
  #barrier: Promise<void> = Promise.resolve();
  #fault = false;
  #now: () => number;
  constructor(private readonly options: NativeOptions) {
    this.#now = options.now ?? Date.now;
  }
  #serial<T>(work: () => Promise<T>): Promise<T> {
    const task = this.#storage.then(work);
    this.#storage = task.then(
      () => undefined,
      () => undefined,
    );
    return task;
  }
  async #read(): Promise<string | null> {
    try {
      return await this.options.credentials.read();
    } catch {
      this.#fault = true;
      throw new PlatformStorageError();
    }
  }
  async #write(value: string | null): Promise<void> {
    try {
      await this.options.credentials.write(value);
    } catch {
      this.#fault = true;
      throw new PlatformStorageError();
    }
  }
  #check(epoch: number) {
    if (epoch !== this.#epoch) throw stale();
  }
  #send(path: string, options: RequestInit = {}, access: string | null = null) {
    const headers = new Headers(options.headers);
    for (const name of [
      "Authorization",
      "Cookie",
      "Cookie2",
      "X-CSRF-Token",
      "Origin",
    ])
      headers.delete(name);
    if (access) headers.set("Authorization", "Bearer " + access);
    if (options.body && !(options.body instanceof FormData))
      headers.set("Content-Type", "application/json");
    return this.options.send(path, {
      ...options,
      headers,
      credentials: "omit",
      redirect: "error",
      cache: "no-store",
    });
  }
  async #accept(tokens: NativeTokens, response: Response, epoch: number) {
    this.#check(epoch);
    await this.#serial(async () => {
      this.#check(epoch);
      await this.#write(tokens.refreshToken);
      this.#check(epoch);
      this.#access = tokens.accessToken;
      const serverDate = Date.parse(response.headers.get("Date") ?? "");
      const base = Number.isFinite(serverDate) ? serverDate : this.#now();
      this.#deadline =
        this.#now() +
        Math.max(0, Math.min(300_000, tokens.accessExpiresUnix * 1000 - base));
    });
  }
  async #revoke(access: string | null, refresh: string | null) {
    try {
      if (access) {
        const response = await this.#send(
          "/api/native/v1/logout",
          { method: "POST", body: "{}" },
          access,
        );
        if (response.ok) return;
      }
      if (refresh) {
        // Expired access can still be revoked using protected refresh authority.
        // A consumed refresh revokes its family at the server instead.
        const response = await this.#send("/api/native/v1/refresh", {
          method: "POST",
          body: JSON.stringify({ refreshToken: refresh }),
        });
        if (!response.ok) return;
        const next = readTokens((await response.json()).native);
        await this.#send(
          "/api/native/v1/logout",
          { method: "POST", body: "{}" },
          next.accessToken,
        );
      }
    } catch {
      // Local erasure still completes offline. No revocation credential is
      // retained for a future background attempt or used to restore sign-in.
    }
  }
  async forget(revoke = true): Promise<void> {
    const access = this.#access;
    this.#epoch++;
    this.#access = null;
    this.#deadline = 0;
    this.#work = null;
    const erase = this.options.onForget?.() ?? Promise.resolve();
    void erase.catch(() => undefined);
    const clear = this.#serial(async () => {
      let refresh: string | null = null;
      try {
        refresh = await this.#read();
      } catch {
        /* Erasure can succeed even when reading fails. */
      }
      await this.#write(null);
      this.#fault = false;
      return refresh;
    });
    this.#barrier = Promise.all([clear, erase]).then(() => undefined);
    void this.#barrier.catch(() => undefined);
    const refresh = await clear;
    await erase;
    if (revoke) void this.#revoke(access, refresh);
  }
  async #exchange(epoch: number) {
    this.#check(epoch);
    const refresh = await this.#serial(() => this.#read());
    this.#check(epoch);
    const path = refresh ? "/api/native/v1/refresh" : "/api/native/v1/session";
    const response = await this.#send(path, {
      method: "POST",
      body: JSON.stringify(
        refresh
          ? { refreshToken: refresh }
          : { platform: this.options.platform },
      ),
    });
    const body = response.ok ? await response.clone().json() : null;
    if (epoch !== this.#epoch) {
      if (body?.native) {
        const token = readTokens(body.native);
        await this.#revoke(token.accessToken, token.refreshToken);
      }
      throw stale();
    }
    if (!response.ok) {
      if (response.status === 401) await this.forget(false);
      throw new NativeResponse(response);
    }
    await this.#accept(readTokens(body.native), response, epoch);
  }
  async #credentials(epoch: number, force = false) {
    await this.#barrier;
    this.#check(epoch);
    if (this.#fault) throw new PlatformStorageError();
    if (!force && this.#access && this.#deadline > this.#now() + 15_000) return;
    if (this.#work) return this.#work;
    const task = this.#exchange(epoch).finally(() => {
      if (this.#work === task) this.#work = null;
    });
    this.#work = task;
    await task;
  }
  async #discardLateSession(path: string, response: Response) {
    if (!response.ok || !mutations.has(path)) return;
    try {
      const value = await response.clone().json();
      if (value.native) {
        const tokens = readTokens(value.native);
        await this.#revoke(tokens.accessToken, tokens.refreshToken);
      }
    } catch {
      // No late credential is installed or exposed to the shared interface.
    }
  }
  async #request(
    path: string,
    options: RequestInit,
    recoverSession: boolean,
  ): Promise<Response> {
    const target = nativePath(path);
    const pathname = path.split("?")[0];
    const epoch = this.#epoch;
    if (options.signal?.aborted) throw stale();
    try {
      await this.#credentials(epoch);
      this.#check(epoch);
      if (options.signal?.aborted) throw stale();
      let access = this.#access;
      let response = await this.#send(target, options, access);
      if (epoch !== this.#epoch || options.signal?.aborted) {
        await this.#discardLateSession(pathname, response);
        throw stale();
      }
      if (response.status === 401 && !mutations.has(pathname)) {
        if (this.#access === access) await this.#credentials(epoch, true);
        this.#check(epoch);
        access = this.#access;
        if (options.signal?.aborted) throw stale();
        response = await this.#send(target, options, access);
      }
      if (epoch !== this.#epoch || options.signal?.aborted) {
        await this.#discardLateSession(pathname, response);
        throw stale();
      }
      if (response.ok && mutations.has(pathname)) {
        const value = await response.clone().json();
        // Profile-first registration only acknowledges the request; it does
        // not create account credentials before mailbox proof.
        if (
          pathname === "/api/v1/register" &&
          response.status === 202 &&
          value.status === "accepted"
        )
          return response;
        const tokens = readTokens(value.native);
        try {
          if (options.signal?.aborted) throw stale();
          await this.options.beforeAccount?.(value.user ?? null);
          this.#check(epoch);
          await this.#accept(tokens, response, epoch);
        } catch (error) {
          await this.#revoke(tokens.accessToken, tokens.refreshToken);
          throw error;
        }
        // The shared UI receives the public session, never the refresh secret.
        delete value.native;
        const headers = new Headers(response.headers);
        headers.delete("Content-Length");
        headers.delete("Content-Encoding");
        response = new Response(JSON.stringify(value), {
          status: response.status,
          statusText: response.statusText,
          headers,
        });
      }
      if (
        response.ok &&
        (pathname === "/api/v1/logout" ||
          pathname === "/api/v1/account/deletion")
      )
        await this.forget(false);
      return response;
    } catch (error) {
      if (error instanceof NativeResponse) {
        if (
          error.response.status === 401 &&
          pathname === "/api/v1/session" &&
          recoverSession
        )
          return this.#request(path, options, false);
        return error.response;
      }
      throw error;
    }
  }
  request(path: string, options: RequestInit = {}) {
    return this.#request(path, options, true);
  }
}
