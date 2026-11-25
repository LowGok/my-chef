import { api, APIError, sessionEpoch } from "./api";
import {
  createBillingFlow,
  type BillingQuote,
  type BillingReceipt,
  type BillingStatus,
  type CheckoutServer,
  type CheckoutStore,
} from "./billing-flow";

export const billingVerifiedEvent = "mychef-native-billing-verified";

export type NativeBillingBridge = {
  billingQuotes(input: {
    products: string[];
    plans: BillingStatus["plans"];
  }): Promise<{ quotes: BillingQuote[] }>;
  billingPrepare?(input: { access: string }): Promise<{ ready: boolean }>;
  billingPurchase(input: {
    product: string;
    basePlan?: string;
    interval: string;
    displayPrice: string;
    binding: string;
    checkout?: string;
    access?: string;
  }): Promise<{
    state: "pending" | "cancelled" | "proof" | "released";
    proof?: BillingReceipt;
  }>;
  billingUnfinished(): Promise<{ receipts: BillingReceipt[] }>;
  billingRestore(): Promise<{ receipts: BillingReceipt[] }>;
  billingFinish(input: { id: string }): Promise<void>;
};

let configured: CheckoutStore | null = null;
let active: {
  epoch: number;
  flow: ReturnType<typeof createBillingFlow>;
} | null = null;
export function setNativeBillingStore(
  platform: "ios" | "android",
  bridge: NativeBillingBridge,
  authorizeApple?: <T>(work: (access: string) => Promise<T>) => Promise<T>,
) {
  active = null;
  configured = {
    store: platform === "ios" ? "apple" : "play",
    async quotes(plans) {
      const result = await bridge.billingQuotes({
        products: plans.map((p) => p.product),
        plans: [...plans],
      });
      return Array.isArray(result.quotes) ? result.quotes : [];
    },
    async purchase(quote, binding) {
      const result = await bridge.billingPurchase({
        product: quote.product,
        basePlan: quote.basePlan,
        interval: quote.interval,
        displayPrice: quote.displayPrice,
        binding,
      });
      if (result.state === "proof" && result.proof) return result.proof;
      return result.state === "cancelled" ? "cancelled" : "pending";
    },
    ...(platform === "ios" && authorizeApple && bridge.billingPrepare ? {
      async prepareAttestation() {
        try {
          const state = await authorizeApple((access) => bridge.billingPrepare!({ access }));
          return state?.ready === true;
        } catch (err) {
          if (err instanceof DOMException && err.name === "AbortError") throw err;
          return false;
        }
      },
      async purchaseAttested(quote, attempt) {
        const result = await authorizeApple((access) => bridge.billingPurchase({
          product: quote.product,
          interval: quote.interval,
          displayPrice: quote.displayPrice,
          binding: attempt.binding,
          checkout: attempt.id,
          access,
        }));
        if (result.state === "released") return "released";
        if (result.state === "proof" && result.proof) return result.proof;
        return "pending";
      },
    } : {}),
    async unfinished() {
      const result = await bridge.billingUnfinished();
      if (!Array.isArray(result?.receipts))
        throw new Error("The store could not check unfinished purchases.");
      return result.receipts;
    },
    async restore() {
      const result = await bridge.billingRestore();
      if (!Array.isArray(result?.receipts))
        throw new Error("The store could not restore purchases.");
      return result.receipts;
    },
    finish(id) {
      return bridge.billingFinish({ id });
    },
  };
}

async function prove(path: string, receipt: string) {
  try {
    const result = await api<{ completeStoreTransaction: boolean }>(path, {
      method: "POST",
      body: JSON.stringify({ receipt }),
    });
    return result.completeStoreTransaction === true;
  } catch (error) {
    if (
      error instanceof APIError &&
      (error.code === "checkout_unavailable" ||
        error.code === "checkout_pending")
    )
      return false;
    throw error;
  }
}

export function nativeBillingFlow() {
  if (!configured) return null;
  const started = sessionEpoch();
  if (active?.epoch === started) return active.flow;
  const server: CheckoutServer = {
    status: () => api<BillingStatus>("/billing/status"),
    reserve: (interval) =>
      api("/billing/checkouts", {
        method: "POST",
        body: JSON.stringify({ interval }),
      }),
    releaseUnused: (id) =>
      api<void>(`/billing/checkouts/${encodeURIComponent(id)}`, {
        method: "DELETE",
      }),
    launch: (id) =>
      api<void>(`/billing/checkouts/${encodeURIComponent(id)}/launch`, {
        method: "POST",
      }),
    verify: (id, receipt) =>
      prove(`/billing/checkouts/${encodeURIComponent(id)}/verify`, receipt),
    restore: (receipt) => prove("/billing/restore", receipt),
  };
  const device = configured;
  const flow = createBillingFlow(
    server,
    device,
    () => configured === device && sessionEpoch() === started,
  );
  active = { epoch: started, flow };
  return flow;
}
