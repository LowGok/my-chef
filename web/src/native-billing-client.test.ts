import { expect, test } from "bun:test";
import { setPlatformTransport } from "./platform";
import {
  nativeBillingFlow,
  setNativeBillingStore,
  type NativeBillingBridge,
} from "./native-billing-client";

test("an unreadable unfinished-purchase response cannot authorize checkout", async () => {
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
      return {} as Awaited<
        ReturnType<NativeBillingBridge["billingUnfinished"]>
      >;
    },
    async billingQuotes() {
      calls.push("quote");
      return { quotes: [] };
    },
    async billingPurchase() {
      throw new Error("No store sheet should open");
    },
    async billingRestore() {
      throw new Error("No store sign-in should open");
    },
    async billingFinish() {
      throw new Error("No store transaction should be completed");
    },
  });
  try {
    const flow = nativeBillingFlow();
    expect(flow).not.toBeNull();
    await expect(flow!.purchase("monthly")).rejects.toThrow(
      "The store could not check unfinished purchases.",
    );
    expect(calls).toEqual(["/api/v1/billing/status", "unfinished"]);
  } finally {
    setPlatformTransport(null);
    if (storage) Object.defineProperty(globalThis, "localStorage", storage);
    else Reflect.deleteProperty(globalThis, "localStorage");
  }
});
