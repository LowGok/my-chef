import { expect, test } from "bun:test";
import { setPlatformTransport } from "./platform";
import {
  nativeBillingFlow,
  setNativeBillingStore,
  type NativeBillingBridge,
} from "./native-billing-client";

const badStoreReply = [
  {
    action: "unfinished",
    expected: "The store could not check unfinished purchases.",
  },
  { action: "restore", expected: "The store could not restore purchases." },
] as const;

test.each(badStoreReply)(
  "an unreadable $action response is never treated as an empty store",
  async ({ action, expected }) => {
    const calls: string[] = [];
    const storage = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
    Object.defineProperty(globalThis, "localStorage", {
      configurable: true,
      value: { getItem: () => null },
    });
    setPlatformTransport({
      native: true,
      async request(path) {
        calls.push(path);
        expect(path).toBe("/api/v1/billing/status");
        return Response.json({
          store: "apple",
          checkoutEnabled: true,
          restoreAvailable: true,
          pending: false,
          plans: [
            { interval: "monthly", product: "fixture.monthly", basePlan: "" },
          ],
        });
      },
      async forget() {},
    });
    setNativeBillingStore("ios", {
      async billingUnfinished() {
        calls.push("unfinished");
        if (action === "unfinished")
          return {} as Awaited<
            ReturnType<NativeBillingBridge["billingUnfinished"]>
          >;
        throw new Error("Restore must not call unfinished");
      },
      async billingQuotes() {
        calls.push("quote");
        return { quotes: [] };
      },
      async billingPurchase() {
        throw new Error("No store sheet should open");
      },
      async billingRestore() {
        calls.push("restore");
        if (action === "restore")
          return {} as Awaited<
            ReturnType<NativeBillingBridge["billingRestore"]>
          >;
        throw new Error("No store sign-in should open");
      },
      async billingFinish() {
        throw new Error("No store transaction should be completed");
      },
    });
    try {
      const flow = nativeBillingFlow();
      expect(flow).not.toBeNull();
      const result =
        action === "unfinished" ? flow!.purchase("monthly") : flow!.restore();
      await expect(result).rejects.toThrow(expected);
      expect(calls).toEqual(["/api/v1/billing/status", action]);
    } finally {
      setPlatformTransport(null);
      if (storage) Object.defineProperty(globalThis, "localStorage", storage);
      else Reflect.deleteProperty(globalThis, "localStorage");
    }
  },
);
