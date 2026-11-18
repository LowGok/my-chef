import { decodeNativeBytes } from "./native-bytes";
import type { NativeBridge } from "./native-platform";

export const appOpeningLink = "https://mychef.dev/app/open";
export const appBrowserFallback = "https://mychef.dev/app/";
export function validAppOpeningLink(value: unknown, lane: unknown) {
  return lane === "app" && value === appOpeningLink;
}

export function createIdentityAuthorization(
  lane: unknown,
  read: () => Promise<unknown>,
  publish: (active: boolean) => void,
) {
  if (lane !== "app" && lane !== "preview")
    throw new Error("Unsupported native identity.");
  let allowed = lane === "preview",
    generation = 0;
  const update = (next: boolean) => {
    if (allowed !== next) {
      allowed = next;
      publish(next);
    }
  };
  return {
    allowed: () => allowed,
    suspend() {
      if (lane === "app") {
        generation++;
        update(false);
      }
    },
    async refresh() {
      if (lane === "preview") return true;
      const current = ++generation;
      let next = false;
      try {
        const value = await read();
        next =
          typeof value === "object" &&
          value !== null &&
          "active" in value &&
          value.active === true;
      } catch {
        /* An unavailable server grants no candidate authority. */
      }
      if (current === generation) update(next);
      return current === generation && allowed;
    },
  };
}

export const nativeIdentityEvent = "mychef-native-identity-policy";
export const nativeLinkFailureEvent = "mychef-native-app-link-failure";
let linkFailure = false;
export const nativeLinkFailed = () => linkFailure;
export function reportNativeLinkFailure() {
  if (identityLane === "app") {
    linkFailure = true;
    window.dispatchEvent(new Event(nativeLinkFailureEvent));
  }
}
export function clearNativeLinkFailure() {
  linkFailure = false;
  window.dispatchEvent(new Event(nativeLinkFailureEvent));
}
let identityLane: "preview" | "app" = "preview";
let authorization = createIdentityAuthorization(
  "preview",
  async () => null,
  () => {},
);
export const nativeIdentityAllowed = () => authorization.allowed();
export const nativeIdentityLane = () => identityLane;
export const refreshNativeIdentity = () => authorization.refresh();
export const suspendNativeIdentity = () => authorization.suspend();

export async function installNativeIdentity(
  bridge: NativeBridge,
  lane: unknown,
) {
  if (lane !== "preview" && lane !== "app")
    throw new Error("Unsupported native identity.");
  identityLane = lane;
  authorization = createIdentityAuthorization(
    lane,
    async () => {
      const requestId = crypto.randomUUID();
      let timeout: ReturnType<typeof setTimeout> | undefined;
      const expired = new Promise<never>((_, reject) => {
        timeout = setTimeout(() => {
          void bridge.cancelRequest({ requestId }).catch(() => undefined);
          reject(new Error("Candidate policy unavailable."));
        }, 5000);
      });
      try {
        const result = await Promise.race([
          bridge.request({
            requestId,
            path: "/api/native/v1/identity-status",
            method: "GET",
            headers: { accept: "application/json" },
          }),
          expired,
        ]);
        if (result.status !== 200 || result.body.length > 4096)
          throw new Error("Candidate policy unavailable.");
        return JSON.parse(
          new TextDecoder().decode(decodeNativeBytes(result.body, 2048)),
        );
      } finally {
        clearTimeout(timeout);
      }
    },
    () => window.dispatchEvent(new Event(nativeIdentityEvent)),
  );
  if (lane === "preview") return;
  window.addEventListener("offline", () => authorization.suspend());
  window.addEventListener("online", () => {authorization.suspend();void authorization.refresh();});
  window.addEventListener(
    "mychef-native-resume",
    () => void authorization.refresh(),
  );
  await authorization.refresh();
}
