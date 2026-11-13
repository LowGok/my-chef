import { forgetReminderDevice, setReminderBridge, syncReminderDevice, type NativeReminderBridge } from "./reminder-device";
import {
  Capacitor,
  registerPlugin,
  type PluginListenerHandle,
} from "@capacitor/core";
import { NativeTransport, setPlatformTransport } from "./platform";
import { encodeNativeBytes, decodeNativeBytes } from "./native-bytes";
import { isolateNativeBridge, type RawNativeBridge } from "./native-bridge";
import {
  installNativeRuntime,
  type NativeRuntimeBridge,
} from "./native-runtime";
import {
  clearPlatformMedia,
  createNativeMedia,
  setMediaPlatform,
  type NativeMediaBridge,
} from "./platform-media";
import {
  cachedOwner,
  forgetShoppingDevice,
  setShoppingStorage,
} from "./shopping-cache";
import {
  openNativeShoppingStorage,
  type NativeShoppingBridge,
} from "./native-shopping";

export type NativeRequest = {
  requestId: string;
  path: string;
  method: string;
  headers: Record<string, string>;
  body?: string;
};
export type NativeBridge = {
  configuration(): Promise<{ platform: string; apiOrigin: string }>;
  readRefresh(): Promise<{ value: string | null }>;
  writeRefresh(value: { value: string | null }): Promise<void>;
  request(
    value: NativeRequest,
  ): Promise<{ status: number; headers: Record<string, string>; body: string }>;
  cancelRequest(value: { requestId: string }): Promise<void>;
};
const failure = () => new TypeError("The private API request failed.");
const aborted = () =>
  new DOMException("The request was cancelled.", "AbortError");
const requestHeaders = new Set([
  "accept",
  "content-type",
  "authorization",
  "idempotency-key",
  "if-match",
  "if-none-match",
]);
const responseHeaders = new Set([
  "content-type",
  "content-disposition",
  "date",
  "etag",
  "x-mychef-user",
  "retry-after",
]);

export async function createNativeTransport(
  bridge: NativeBridge,
  platform: "ios" | "android",
) {
  let config: Awaited<ReturnType<NativeBridge["configuration"]>>;
  try {
    config = await bridge.configuration();
  } catch {
    throw failure();
  }
  const origin = new URL(config.apiOrigin);
  if (
    config.platform !== platform ||
    config.apiOrigin !== origin.origin ||
    origin.protocol !== "https:" ||
    !(
      origin.hostname === "localhost" ||
      /^[a-z0-9-]+\.tail[a-z0-9]+\.ts\.net$/.test(origin.hostname)
    )
  )
    throw failure();
  return new NativeTransport({
    platform,
    onForget() {
      return Promise.all([clearPlatformMedia(), forgetShoppingDevice(), forgetReminderDevice()]).then(
        () => undefined,
      );
    },
    async beforeAccount(user) {
      await Promise.all([clearPlatformMedia(), forgetReminderDevice()]);
      const saved = cachedOwner()?.profile;
      const next = user as { id?: unknown; accountInstance?: unknown } | null;
      if (
        saved &&
        (saved.id !== next?.id ||
          saved.accountInstance !== next?.accountInstance)
      )
        await forgetShoppingDevice();
    },
    credentials: {
      async read() {
        const result = await bridge.readRefresh();
        if (
          result.value !== null &&
          (typeof result.value !== "string" ||
            !/^[a-f0-9]{64}$/.test(result.value))
        )
          throw failure();
        return result.value;
      },
      write: (value) => bridge.writeRefresh({ value }),
    },
    async send(path, options) {
      if (options.signal?.aborted) throw aborted();
      const url = new URL(path, origin);
      if (
        !path.startsWith("/api/native/v1/") ||
        url.origin !== origin.origin ||
        url.pathname !== path.split("?")[0] ||
        /[\\#\r\n]|%2e|%2f|%5c/i.test(path)
      )
        throw failure();
      const request = new Request(url, options);
      const headers: Record<string, string> = {};
      request.headers.forEach((value, key) => {
        if (requestHeaders.has(key)) headers[key] = value;
      });
      const data =
        options.body == null
          ? null
          : new Uint8Array(await request.arrayBuffer());
      if (data && data.length > 24 * 1024 * 1024) throw failure();
      if (options.signal?.aborted) throw aborted();
      const requestId = crypto.randomUUID();
      let onAbort!: () => void;
      const interrupted = new Promise<never>((_, reject) => {
        onAbort = () => {
          void bridge.cancelRequest({ requestId }).catch(() => undefined);
          reject(aborted());
        };
        options.signal?.addEventListener("abort", onAbort, { once: true });
      });
      try {
        const raw = await Promise.race([
          bridge.request({
            requestId,
            path,
            method: request.method,
            headers,
            ...(data?.length ? { body: encodeNativeBytes(data) } : {}),
          }),
          interrupted,
        ]);
        if (options.signal?.aborted) throw aborted();
        if (
          !Number.isInteger(raw.status) ||
          raw.status < 200 ||
          raw.status > 599 ||
          (raw.status >= 300 && raw.status < 400)
        )
          throw failure();
        const safe: Record<string, string> = {};
        for (const [key, value] of Object.entries(raw.headers))
          if (responseHeaders.has(key.toLowerCase())) safe[key] = value;
        const bytes = decodeNativeBytes(raw.body, 32 * 1024 * 1024);
        return new Response(
          [204, 205, 304].includes(raw.status) ? null : bytes,
          { status: raw.status, headers: safe },
        );
      } catch (error) {
        if (options.signal?.aborted) throw aborted();
        throw failure();
      } finally {
        options.signal?.removeEventListener("abort", onAbort);
      }
    },
  });
}

const startupReferences = {
  configuration: "MYC-01",
  storage: "MYC-02",
  shopping: "MYC-03",
  device: "MYC-04",
} as const;
type StartupStep = keyof typeof startupReferences;

export class NativeStartupError extends Error {
  readonly reference: (typeof startupReferences)[StartupStep];
  constructor(step: StartupStep) {
    super("MyChef could not open securely.");
    this.reference = startupReferences[step];
  }
}

export async function runNativeStartupStep<T>(
  step: StartupStep,
  run: () => Promise<T> | T,
): Promise<T> {
  try {
    return await run();
  } catch {
    // The reference can be shared with support; native details must never surface.
    throw new NativeStartupError(step);
  }
}

export async function initializeNativePlatform() {
  if (!Capacitor.isNativePlatform()) return false;
  const platform = await runNativeStartupStep("configuration", () => {
    const platform = Capacitor.getPlatform();
    if (
      (platform !== "ios" && platform !== "android") ||
      !Capacitor.isPluginAvailable("MyChefNative")
    )
      throw failure();
    return platform;
  });
  const raw = registerPlugin<
    RawNativeBridge & {
      addListener(
        event: "runtime",
        wake: () => void,
      ): Promise<PluginListenerHandle>;
    }
  >("MyChefNative");
  const bridge = isolateNativeBridge<
    NativeBridge &
      NativeShoppingBridge &
      NativeMediaBridge &
      NativeRuntimeBridge &
      NativeReminderBridge
  >(raw);
  const transport = await runNativeStartupStep("configuration", () =>
    createNativeTransport(bridge, platform),
  );
  const stored = await runNativeStartupStep("storage", async () => {
    const stored = await bridge.readRefresh();
    if (
      stored.value !== null &&
      !/^[a-f0-9]{64}$/.test(
        typeof stored.value === "string" ? stored.value : "",
      )
    )
      throw failure();
    return stored;
  });
  await runNativeStartupStep("shopping", async () => {
    if (stored.value === null) await bridge.clearShopping();
    setShoppingStorage(await openNativeShoppingStorage(bridge));
  });
  setPlatformTransport(transport);
  setMediaPlatform(createNativeMedia(bridge));
  setReminderBridge(bridge);
  await runNativeStartupStep("device", async () =>
    installNativeRuntime(
      bridge,
      (await bridge.configuration()).apiOrigin,
      (wake) => raw.addListener("runtime", () => {
        wake();
        void syncReminderDevice().catch(() => undefined);
      }),
    ),
  );
  return true;
}
