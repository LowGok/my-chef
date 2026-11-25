// Loaded by test drivers and evaluated only in a disposable WebView.
// The production web bundle and native builds never import this module.
import { createHash, createHmac } from "node:crypto";

type Platform = "ios" | "android";
type Input = Record<string, unknown>;
type Call = (plugin: string, method: string, input: Input) => Promise<Input>;

export function billingFixtureKey(capability: string, platform: Platform) {
  if (!/^[A-Za-z0-9_-]{43}$/.test(capability))
    throw new Error("The simulated billing fixture is unavailable.");
  const seed = createHash("sha256").update(Buffer.from(capability, "base64url")).digest();
  return createHmac("sha256", seed)
    .update("mychef/billing/fixture/v1\x00proof/" + (platform === "ios" ? "apple" : "play"))
    .digest("base64url");
}

export async function installBillingStoreFixture(options: {
  platform: Platform;
  proofKey: string;
  expiresAt: number;
  loseResult: boolean;
}) {
  // This function also runs on its own when serialized by the test driver.
  const stop = () => new Error("The disposable WebView billing fixture refused the operation.");
  const cap = (globalThis as typeof globalThis & {
    Capacitor?: { nativePromise: Call };
  }).Capacitor;
  if (!cap || typeof cap.nativePromise !== "function" || !crypto.subtle ||
      !/^[A-Za-z0-9_-]{43}$/.test(options.proofKey) ||
      !Number.isSafeInteger(options.expiresAt) || options.expiresAt <= Date.now() ||
      options.expiresAt - Date.now() > 30 * 60_000)
    throw stop();
  const call = cap.nativePromise.bind(cap);
  const configNonce = crypto.randomUUID();
  const config = await call("MyChefNative", "configuration", { _callNonce: configNonce });
  if (config.platform !== options.platform || config.apiOrigin !== "https://localhost:8977" ||
      config._callNonce !== configNonce)
    throw stop();
  const decodeKey = options.proofKey.replace(/-/g, "+").replace(/_/g, "/");
  const key = await crypto.subtle.importKey(
    "raw", Uint8Array.from(atob(decodeKey + "="), (char) => char.charCodeAt(0)),
    { name: "HMAC", hash: "SHA-256" }, false, ["sign"],
  );
  const store = options.platform === "ios" ? "apple" : "play";
  const methods = new Set(["billingQuotes", "billingPurchase", "billingUnfinished", "billingRestore", "billingFinish"]);
  let proof: { id: string; interval: "monthly" | "annual"; receipt: string } | undefined;
  let verified = false;
  let completions = 0;

  function encode(data: Uint8Array) {
    return btoa(String.fromCharCode(...data)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }

  cap.nativePromise = async (plugin, method, input) => {
    if (plugin !== "MyChefNative" || !methods.has(method)) {
      const actual = await call(plugin, method, input);
      // Observe only the success bit from the real, nonce-matched app API.
      // The original response is returned unchanged.
      if (plugin === "MyChefNative" && method === "request" && proof &&
          input.method === "POST" && typeof input.path === "string" &&
          /^\/api\/native\/v1\/billing\/(?:checkouts\/[a-z0-9-]+\/verify|restore)$/.test(input.path) &&
          actual._callNonce === input._callNonce && actual.status === 200 &&
          typeof input.body === "string" && typeof actual.body === "string") {
        const submitted = JSON.parse(atob(input.body));
        const accepted = JSON.parse(atob(actual.body));
        if (submitted.receipt === proof.receipt && accepted.completeStoreTransaction === true) verified = true;
      }
      return actual;
    }
    if (Date.now() >= options.expiresAt || typeof input._callNonce !== "string")
      throw stop();
    const reply = (value: Input) => ({ ...value, _callNonce: input._callNonce });
    switch (method) {
      case "billingQuotes":
        if (!Array.isArray(input.plans)) throw stop();
        return reply({ quotes: input.plans.map((plan) => {
          if (!(plan.interval === "monthly" || plan.interval === "annual") ||
              (store === "apple"
                ? plan.product !== "fixture.billing." + plan.interval || plan.basePlan !== ""
                : plan.product !== "fixture.billing.premium" || plan.basePlan !== plan.interval))
            throw stop();
          return {
            ...plan, store, region: store === "apple" ? "BEL" : "BE", currency: "EUR",
            amountMinor: plan.interval === "monthly" ? 699 : 6990,
            displayPrice: plan.interval === "monthly" ? "€6.99" : "€69.90",
            introductory: false,
          };
        }) });
      case "billingPurchase": {
        const binding = input.binding;
        const interval = input.interval;
        if (proof || typeof binding !== "string" ||
            !/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(binding) ||
            (interval !== "monthly" && interval !== "annual"))
          throw stop();
        const id = crypto.randomUUID();
        const now = Date.now();
        const body = new TextEncoder().encode(JSON.stringify({
          Binding: binding, Order: id, Interval: interval,
          StartsAt: new Date(now - 1000).toISOString(),
          PaidThrough: new Date(Math.min(now + 5 * 60_000, options.expiresAt - 1000)).toISOString(),
        }));
        const signature = new Uint8Array(await crypto.subtle.sign("HMAC", key, body));
        proof = { id, interval, receipt: "fixture." + encode(body) + "." + encode(signature) };
        return reply(options.loseResult ? { state: "pending" } : { state: "proof", proof });
      }
      case "billingUnfinished":
        return reply({ receipts: [] });
      case "billingRestore":
        return reply({ receipts: proof ? [proof] : [] });
      case "billingFinish":
        if (!proof || input.id !== proof.id || !verified ||
            JSON.stringify([location.href, history.state, ...Object.values(sessionStorage), ...Object.values(localStorage)])
              .includes(proof.receipt))
          throw stop();
        verified = false;
        completions++;
        return reply({});
      default:
        throw stop();
    }
  };
  return () => completions;
}
