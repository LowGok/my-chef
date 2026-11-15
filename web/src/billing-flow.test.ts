import { expect, test } from "bun:test";
import {
  createBillingFlow,
  type BillingStatus,
  type BillingQuote,
  type BillingReceipt,
} from "./billing-flow";

const plan = {
  interval: "monthly",
  product: "fixture.monthly",
  basePlan: "",
} as const;
const status: BillingStatus = {
  store: "apple",
  checkoutEnabled: true,
  restoreAvailable: true,
  pending: false,
  plans: [plan],
};
const quote: BillingQuote = {
  ...plan,
  store: "apple",
  region: "BEL",
  currency: "EUR",
  amountMinor: 699,
  displayPrice: "€6.99",
  introductory: false,
};
const proof: BillingReceipt = {
  id: "fixture-transaction",
  receipt: "fixture-receipt",
  interval: "monthly",
};
const id = "a0e99b92-a5a2-4d5b-95c1-7307c3747c77";
const binding = "5095aae8-27e3-45de-bef1-bf9b179cedcf";

function fixture() {
  const events: string[] = [];
  let current = status;
  let owned = true;
  let allowFinish: () => Promise<boolean> = async () => true;
  let nativeQuotes = [quote, quote];
  let purchase: "pending" | "cancelled" | BillingReceipt = proof;
  const flow = createBillingFlow(
    {
      async status() {
        return current;
      },
      async reserve() {
        events.push("reserve");
        return { ...plan, id, binding, store: "apple" };
      },
      async launch() {
        events.push("launch");
      },
      async verify(_, value) {
        expect(value).toBe(proof.receipt);
        events.push("verify");
        return allowFinish();
      },
      async restore(value) {
        expect(value).toBe(proof.receipt);
        events.push("verify restore");
        return allowFinish();
      },
    },
    {
      store: "apple",
      async quotes() {
        events.push("quote");
        return [nativeQuotes.shift() ?? quote];
      },
      async purchase(value, accountBinding) {
        expect(value).toEqual(quote);
        expect(accountBinding).toBe(binding);
        events.push("purchase");
        return purchase;
      },
      async unfinished() {
        events.push("unfinished");
        return [proof];
      },
      async restore() {
        events.push("restore");
        return [proof];
      },
      async finish(value) {
        expect(value).toBe(proof.id);
        events.push("finish");
      },
    },
    () => owned,
  );
  return {
    flow,
    events,
    setStatus: (v: BillingStatus) => {
      current = v;
    },
    setQuotes: (...v: BillingQuote[]) => {
      nativeQuotes = v;
    },
    setFinish: (v: boolean) => {
      allowFinish = async () => v;
    },
    onVerify: (verify: typeof allowFinish) => {
      allowFinish = verify;
    },
    setPurchase: (v: typeof purchase) => {
      purchase = v;
    },
    endSession: () => {
      owned = false;
    },
  };
}

test("a storefront switch or unexpected terms stop before the store sheet", async () => {
  const f = fixture();
  f.setQuotes(quote, { ...quote, region: "FRA" });
  expect(await f.flow.purchase("monthly")).toBe("unavailable");
  expect(f.events).toEqual(["quote", "reserve", "quote"]);
  for (const changed of [
    { amountMinor: 700 },
    { introductory: true },
    { interval: "annual" as const },
  ]) {
    const attempt = fixture();
    attempt.setQuotes({ ...quote, ...changed });
    expect(await attempt.flow.purchase("monthly")).toBe("unavailable");
    expect(attempt.events).toEqual(["quote"]);
  }
});

test("checkout records launch before opening the store and finishes only after verification", async () => {
  const success = fixture();
  expect(await success.flow.purchase("monthly")).toBe("verified");
  expect(success.events).toEqual([
    "quote",
    "reserve",
    "quote",
    "launch",
    "purchase",
    "verify",
    "finish",
  ]);

  const pending = fixture();
  pending.setFinish(false);
  expect(await pending.flow.purchase("monthly")).toBe("pending");
  expect(pending.events).not.toContain("finish");
});

test("sales-off restore and resumed checkout never open a new sheet", async () => {
  const f = fixture();
  f.setStatus({
    ...status,
    checkoutEnabled: false,
    plans: [],
    pending: true,
    resume: { id, interval: "monthly", launched: true },
  });
  expect(await f.flow.purchase("monthly")).toBe("unavailable");
  expect(await f.flow.resume()).toBe("verified");
  expect(await f.flow.restore()).toBe("verified");
  expect(f.events).toEqual([
    "unfinished",
    "verify",
    "finish",
    "restore",
    "verify restore",
    "finish",
  ]);
  f.endSession();
  await expect(f.flow.restore()).rejects.toThrow("session changed");
  expect(f.events).toHaveLength(6);
});

test("store discovery recovers an unfinished purchase without opening checkout or prompting for store sign-in", async () => {
  const f = fixture();
  f.setStatus({ ...status, checkoutEnabled: false, plans: [], pending: true });
  f.setFinish(false);
  expect(await f.flow.discover()).toBe("pending");
  expect(f.events).toEqual(["unfinished", "verify restore"]);
  f.setFinish(true);
  expect(await f.flow.discover()).toBe("verified");
  expect(f.events).toEqual([
    "unfinished",
    "verify restore",
    "unfinished",
    "verify restore",
    "finish",
  ]);
  f.setStatus({ ...status, restoreAvailable: false });
  expect(await f.flow.discover()).toBe("unavailable");
  expect(f.events).toHaveLength(5);
});

test("a store update during checkout is coalesced and waits before reading unfinished results", async () => {
  const f = fixture();
  f.setPurchase("pending");
  const purchase = f.flow.purchase("monthly");
  const discovery = f.flow.discover();
  const duplicate = f.flow.discover();
  expect(discovery).toBe(duplicate);
  expect(await purchase).toBe("pending");
  expect(await discovery).toBe("verified");
  expect(f.events).toEqual([
    "quote",
    "reserve",
    "quote",
    "launch",
    "purchase",
    "unfinished",
    "verify restore",
    "finish",
  ]);
});

test("sign-out during automatic verification prevents device acknowledgement", async () => {
  const f = fixture();
  let reached!: () => void;
  let complete!: (value: boolean) => void;
  const verifying = new Promise<void>((resolve) => (reached = resolve));
  f.onVerify(
    () =>
      new Promise((resolve) => {
        complete = resolve;
        reached();
      }),
  );
  const outcome = f.flow.discover().then(
    () => undefined,
    (error: unknown) => error,
  );
  await verifying;
  f.endSession();
  complete(true);
  expect(await outcome).toEqual(new Error("The app session changed."));
  expect(f.events).toEqual(["unfinished", "verify restore"]);
});
