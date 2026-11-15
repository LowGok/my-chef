import type { components } from "./api.generated";

export type BillingStatus = components["schemas"]["NativeBillingStatus"];
type Plan = BillingStatus["plans"][number];
type BillingStore = BillingStatus["store"];
type Interval = Plan["interval"];

export type BillingQuote = Plan & {
  store: BillingStore;
  region: string;
  currency: string;
  amountMinor: number;
  displayPrice: string;
  introductory: boolean;
};

export type BillingReceipt = {
  id: string;
  receipt: string;
  interval?: Interval;
};

type Reserved = Plan & { store: BillingStore; id: string; binding: string };
type Result = "verified" | "unavailable" | "pending" | "empty";
export type CheckoutStore = {
  readonly store: BillingStore;
  quotes(plans: readonly Plan[]): Promise<BillingQuote[]>;
  // Re-check the passed terms in the platform adapter before launching its sheet.
  purchase(
    quote: BillingQuote,
    binding: string,
  ): Promise<BillingReceipt | "pending" | "cancelled">;
  unfinished(): Promise<BillingReceipt[]>;
  restore(): Promise<BillingReceipt[]>;
  finish(id: string): Promise<void>;
};
export type CheckoutServer = {
  status(): Promise<BillingStatus>;
  reserve(interval: Interval): Promise<Reserved>;
  launch(id: string): Promise<void>;
  releaseUnused(id: string): Promise<void>;
  verify(id: string, receipt: string): Promise<boolean>;
  restore(receipt: string): Promise<boolean>;
};

const amounts: Record<Interval, number> = { monthly: 699, annual: 6990 };
const uuid =
  /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

function samePlan(a: Plan, b: Plan) {
  return (
    a.interval === b.interval &&
    a.product === b.product &&
    a.basePlan === b.basePlan
  );
}

function validQuote(quote: BillingQuote, plan: Plan, store: BillingStore) {
  return (
    quote.store === store &&
    samePlan(quote, plan) &&
    quote.region === (store === "apple" ? "BEL" : "BE") &&
    quote.currency === "EUR" &&
    quote.amountMinor === amounts[plan.interval] &&
    quote.introductory === false &&
    typeof quote.displayPrice === "string" &&
    quote.displayPrice.length <= 64 &&
    !!quote.displayPrice.trim() &&
    !/[\x00-\x1f\x7f]/.test(quote.displayPrice)
  );
}

function validReceipt(value: BillingReceipt) {
  return (
    typeof value.id === "string" &&
    /^[a-zA-Z0-9._-]{1,128}$/.test(value.id) &&
    typeof value.receipt === "string" &&
    value.receipt.length >= 1 &&
    value.receipt.length <= 16 * 1024
  );
}

// Store interaction never grants access. Only a verified, committed reply from
// the matching app session allows the native transaction to be finished.
export function createBillingFlow(
  server: CheckoutServer,
  device: CheckoutStore,
  ownsSession: () => boolean,
) {
  let operation: Promise<Result> | null = null;
  let discovery: Promise<Result> | null = null;
  function requireSession() {
    if (!ownsSession()) throw new Error("The app session changed.");
  }
  async function run(work: () => Promise<Result>): Promise<Result> {
    requireSession();
    if (operation) return "pending";
    const active = Promise.resolve().then(() => {
      requireSession();
      return work();
    });
    operation = active;
    try {
      return await active;
    } finally {
      operation = null;
    }
  }
  async function quotePlans(status: BillingStatus, plans: Plan[]) {
    if (
      !status.checkoutEnabled ||
      status.store !== device.store ||
      !plans.length
    )
      return [];
    requireSession();
    const quoted = await device.quotes(plans);
    requireSession();
    return plans.flatMap((plan) => {
      const matches = quoted.filter((quote) =>
        validQuote(quote, plan, device.store),
      );
      return matches.length === 1 ? matches : [];
    });
  }
  async function finish(
    proof: BillingReceipt,
    verify: (value: string) => Promise<boolean>,
  ): Promise<Result> {
    requireSession();
    if (!validReceipt(proof)) return "pending";
    const accepted = await verify(proof.receipt);
    requireSession();
    if (accepted !== true) return "pending";
    try {
      await device.finish(proof.id);
    } catch {
      requireSession();
      // The purchase is already committed. Leave an unfinished store
      // transaction eligible for discovery without blocking other receipts.
      return "pending";
    }
    requireSession();
    return "verified";
  }
  async function status() {
    requireSession();
    const value = await server.status();
    requireSession();
    return value;
  }
  async function quotes() {
    const current = await status();
    if (current.subscription || current.pending) return [];
    return quotePlans(current, current.plans);
  }
  function purchase(interval: Interval): Promise<Result> {
    return run(async () => {
      const current = await status();
      if (
        current.subscription ||
        !current.checkoutEnabled ||
        current.store !== device.store
      )
        return "unavailable";
      if (
        current.pending &&
        (current.resume?.launched !== false ||
          current.resume.interval !== interval)
      )
        return "pending";
      const plans = current.plans.filter((p) => p.interval === interval);
      if (plans.length !== 1) return "unavailable";
      const [first] = await quotePlans(current, plans);
      if (!first) return "unavailable";
      requireSession();
      const attempt = await server.reserve(interval);
      requireSession();
      if (
        !uuid.test(attempt.id) ||
        !uuid.test(attempt.binding) ||
        attempt.store !== device.store ||
        !samePlan(attempt, first) ||
        (current.resume && current.resume.id !== attempt.id)
      )
        return "unavailable";
      const [fresh] = await quotePlans(current, plans);
      if (!fresh) {
        try {
          await server.releaseUnused(attempt.id);
        } catch {
          // If the release is lost, an unused reservation still expires.
        }
        requireSession();
        return "unavailable";
      }
      await server.launch(attempt.id);
      requireSession();
      const evidence = await device.purchase(fresh, attempt.binding);
      requireSession();
      if (evidence === "pending" || evidence === "cancelled") return "pending";
      if (evidence.interval !== interval) return "pending";
      return finish(evidence, (value) => server.verify(attempt.id, value));
    });
  }
  function resume(): Promise<Result> {
    return run(async () => {
      const current = await status();
      const attempt = current.resume;
      if (!attempt || current.store !== device.store || !uuid.test(attempt.id))
        return "unavailable";
      if (!attempt.launched) return "pending";
      const receipts = await device.unfinished();
      requireSession();
      for (const proof of receipts.slice(0, 10)) {
        if (!proof.interval || proof.interval === attempt.interval) {
          const result = await finish(proof, (value) =>
            server.verify(attempt.id, value),
          );
          if (result === "verified") return result;
        }
      }
      // Absence of evidence does not authorize a new store sheet.
      return "pending";
    });
  }
  async function recover(prompt: boolean): Promise<Result> {
    const current = await status();
    if (!current.restoreAvailable || current.store !== device.store)
      return "unavailable";
    const receipts = prompt
      ? await device.restore()
      : await device.unfinished();
    requireSession();
    let result: Result = "empty";
    for (const proof of receipts.slice(0, 10)) {
      const checked = await finish(proof, server.restore);
      if (result !== "verified") result = checked;
    }
    return result;
  }
  function restore(): Promise<Result> {
    return run(() => recover(true));
  }
  function discover(): Promise<Result> {
    if (discovery) return discovery;
    discovery = (async () => {
      // A store update can arrive while its sheet is dismissing. Let the
      // existing transaction finish before reading unfinished purchases.
      while (operation) {
        try {
          await operation;
        } catch {
          // An interrupted checkout must still be eligible for discovery.
        }
      }
      return run(() => recover(false));
    })().finally(() => {
      discovery = null;
    });
    return discovery;
  }
  return { status, quotes, purchase, resume, restore, discover };
}
