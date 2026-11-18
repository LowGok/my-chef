export type NativeIdentityLane = "preview" | "app";

const identities = {
  preview: {
    lane: "preview",
    appId: "com.gkldevelopment.mychef.preview",
    appName: "MyChef Preview",
  },
  app: { lane: "app", appId: "dev.mychef.app", appName: "MyChef" },
} as const;

// Runtime callers include generated build inputs, not just typed TS callers.
export function nativeIdentity(lane: unknown) {
  if (lane !== "preview" && lane !== "app")
    throw new Error("Select the explicit preview or app identity.");
  return identities[lane];
}
