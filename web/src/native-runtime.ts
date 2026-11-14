import { captureReminderOpen } from "./reminder-device";
import type { ProofEntry } from "./EmailVerification";
import type { RecoveryEntry } from "./PasswordRecovery";
export type NativeProof = ProofEntry | RecoveryEntry;
export type RuntimeSnapshot = {
  active: boolean;
  online: boolean;
  back: boolean;
  link?: string;
  invalidLink?: boolean;
  reminder?: string;
};
export type NativeRuntimeBridge = { readRuntime(): Promise<RuntimeSnapshot> };
type RuntimeEvent =
  | { type: "connection"; active: boolean; online: boolean }
  | { type: "proof"; entry: NativeProof }
  | { type: "back" | "invalid-link" | "unavailable" };
export function validateNativeProof(
  value: string,
  origin: string,
): NativeProof | null {
  try {
    if (value.length > 512) return null;
    const url = new URL(value);
    if (url.origin !== origin || url.username || url.password || url.search)
      return null;
    const kind = url.pathname.slice(5);
    if (
      !["signup", "verify-email", "change-email", "reset-password"].includes(
        kind,
      ) ||
      url.pathname !== "/app/" + kind ||
      !/^#[A-Za-z0-9_-]{43}$/.test(url.hash)
    )
      return null;
    // Require the exact unencoded shape, not URL-parser normalization of dot paths.
    if (value !== `${origin}/app/${kind}${url.hash}`) return null;
    return { kind, proof: url.hash.slice(1) } as NativeProof;
  } catch {
    return null;
  }
}
export function createNativeRuntime(
  bridge: NativeRuntimeBridge,
  origin: string,
  publish: (event: RuntimeEvent) => void,
) {
  let stopped = false,
    work: Promise<void> | null = null,
    again = false;
  let last: Pick<RuntimeSnapshot, "active" | "online"> | null = null;
  async function pull() {
    try {
      const value = await bridge.readRuntime();
      if (stopped) return;
      if (
        typeof value.active !== "boolean" ||
        typeof value.online !== "boolean" ||
        typeof value.back !== "boolean"
      )
        throw new Error();
      if (!last || value.active !== last.active || value.online !== last.online)
        publish({
          type: "connection",
          active: value.active,
          online: value.online,
        });
      last = { active: value.active, online: value.online };
      if (value.link !== undefined || value.invalidLink) {
        const entry =
          typeof value.link === "string"
            ? validateNativeProof(value.link, origin)
            : null;
        publish(entry ? { type: "proof", entry } : { type: "invalid-link" });
      }
      if (value.reminder !== undefined) captureReminderOpen(value.reminder);
      if (value.back) publish({ type: "back" });
    } catch {
      if (!stopped) publish({ type: "unavailable" });
    }
  }
  return {
    wake(): Promise<void> {
      if (stopped) return Promise.resolve();
      if (work) {
        again = true;
        return work;
      }
      work = (async () => {
        do {
          again = false;
          await pull();
        } while (again && !stopped);
      })().finally(() => {
        work = null;
      });
      return work;
    },
    stop() {
      stopped = true;
    },
  };
}
let proof: NativeProof | null = null;
let active = true;
export const nativeAppActive = () => active;
export const peekNativeProof = () => proof;
export function takeNativeProof() {
  const value = proof;
  proof = null;
  return value;
}
export function forgetNativeProof() {
  proof = null;
}
export const nativeProofEvent = "mychef-native-account-link";
export const nativeBackEvent = "mychef-native-back";
export const nativeResumeEvent = "mychef-native-resume";
export async function installNativeRuntime(
  bridge: NativeRuntimeBridge,
  origin: string,
  subscribe: (wake: () => void) => Promise<{ remove(): Promise<void> }>,
) {
  let initialFailure = false;
  const runtime = createNativeRuntime(bridge, origin, (event) => {
    if (event.type === "unavailable") initialFailure = true;
    if (event.type === "proof") {
      proof = event.entry;
      window.dispatchEvent(new Event(nativeProofEvent));
    } else if (event.type === "invalid-link")
      window.dispatchEvent(new Event("mychef-native-link-error"));
    else if (event.type === "back")
      window.dispatchEvent(new Event(nativeBackEvent));
    else if (event.type === "connection") {
      active = event.active;
      if (active) {
        window.dispatchEvent(new Event(event.online ? "online" : "offline"));
      }
    }
  });
  // Listener payloads are deliberately ignored. Pull through the nonce-bound
  // bridge so no proof can be delivered to an obsolete view's callback number.
  const listener = await subscribe(() => void runtime.wake());
  await runtime.wake();
  if (initialFailure) {
    runtime.stop();
    await listener.remove();
    throw new Error("Native app state is unavailable.");
  }
  const resume = () => {
    if (document.visibilityState === "visible") void runtime.wake();
  };
  const retry = setInterval(() => {
    if (active && document.visibilityState === "visible")
      window.dispatchEvent(new Event(nativeResumeEvent));
  }, 15000);
  document.addEventListener("visibilitychange", resume);
  window.addEventListener(
    "pagehide",
    () => {
      forgetNativeProof();
      runtime.stop();
      clearInterval(retry);
      document.removeEventListener("visibilitychange", resume);
      void listener.remove();
    },
    { once: true },
  );
}
