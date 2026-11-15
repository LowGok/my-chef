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


test("only the protected iOS bridge launches a verified Apple checkout; the web client never handles an assertion", async () => {
  const calls: string[] = [];
  const access = "a".repeat(64);
  const id = "a0e99b92-a5a2-4d5b-95c1-7307c3747c77";
  const binding = "5095aae8-27e3-45de-bef1-bf9b179cedcf";
  const plan = { product: "fixture.monthly", interval: "monthly" as const, basePlan: "" };
  const storage = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true, value: { getItem: () => null },
  });
  setPlatformTransport({
    native: true,
    async request(path) {
      calls.push(path);
      switch (path) {
        case "/api/v1/billing/status":
          return Response.json({ store: "apple", checkoutEnabled: true, appleAttestationRequired: true,
            restoreAvailable: true, pending: false, plans: [plan] });
        case "/api/v1/billing/checkouts":
          return Response.json({ ...plan, store: "apple", id, binding }, { status: 201 });
        default:
          throw new Error("The web transport reached an unexpected path.");
      }
    },
    async forget() {},
  });
  setNativeBillingStore("ios", {
    async billingQuotes() {
      return { quotes: [{ ...plan, store: "apple", region: "BEL", currency: "EUR", amountMinor: 699,
        displayPrice: "€6.99", introductory: false }] };
    },
    async billingPrepare(input) {
      expect(input).toEqual({ access });
      calls.push("native prepare");
      return { ready: true };
    },
    async billingPurchase(input) {
      expect(input).toEqual({ access, checkout: id, binding, interval: "monthly",
        product: plan.product, displayPrice: "€6.99" });
      calls.push("native purchase");
      return { state: "released" };
    },
    async billingUnfinished() { return { receipts: [] }; },
    async billingRestore() { throw new Error("Purchase should not prompt to restore."); },
    async billingFinish() { throw new Error("A cancellation has no store transaction to finish."); },
  }, (work) => work(access));
  try {
    expect(await nativeBillingFlow()!.purchase("monthly")).toBe("released");
    expect(calls).toEqual(["/api/v1/billing/status", "native prepare", "/api/v1/billing/checkouts", "native purchase"]);
  } finally {
    setPlatformTransport(null);
    if (storage) Object.defineProperty(globalThis, "localStorage", storage);
    else Reflect.deleteProperty(globalThis, "localStorage");
  }
});
