export type RawNativeBridge = Record<
  string,
  (input: Record<string, unknown>) => Promise<unknown>
>;
const methods = [
  "configuration",
  "readRuntime",
  "reminderPermission",
  "reminderRegistration",
  "stopReminderRegistration",
  "openReminderSettings",
  "readRefresh",
  "writeRefresh",
  "readShopping",
  "writeShopping",
  "clearShopping",
  "request",
  "cancelRequest",
  "pickPhotos",
  "publicImage",
  "saveExport",
  "openExternal",
  "cancelMedia",
];

// Capacitor callback numbers restart on page load. A cryptographic nonce also
// binds private data to the exact invocation, before any consumer sees it.
export function isolateNativeBridge<T>(raw: RawNativeBridge): T {
  return Object.fromEntries(
    methods.map((method) => [
      method,
      async (options: Record<string, unknown> = {}) => {
        const nonce = crypto.randomUUID();
        try {
          const result = await raw[method]({ ...options, _callNonce: nonce });
          if (
            !result ||
            typeof result !== "object" ||
            Array.isArray(result) ||
            (result as Record<string, unknown>)._callNonce !== nonce
          )
            throw new Error();
          const { _callNonce: ignored, ...value } = result as Record<
            string,
            unknown
          >;
          return value;
        } catch {
          throw new Error("The native app response could not be verified.");
        }
      },
    ]),
  ) as T;
}
