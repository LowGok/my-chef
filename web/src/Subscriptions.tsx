import { useEffect, useRef, useState } from "react";
import { APIError, isStale, message, sessionEpoch } from "./api";
import type { BillingQuote, BillingStatus } from "./billing-flow";
import {
  billingVerifiedEvent,
  nativeBillingFlow,
} from "./native-billing-client";
import { Alert } from "./components";

const management = {
  apple: {
    href: "https://apps.apple.com/account/subscriptions",
    title: "Open Apple subscriptions",
  },
  play: {
    href: "https://play.google.com/store/account/subscriptions",
    title: "Open Google Play subscriptions",
  },
} as const;

export function Subscriptions({ onHelp }: { onHelp: () => void }) {
  const flow = nativeBillingFlow();
  const [status, setStatus] = useState<BillingStatus | null>(null);
  const [quotes, setQuotes] = useState<BillingQuote[]>([]);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const read = useRef(0);

  async function refresh() {
    if (!flow) return;
    const started = sessionEpoch();
    const id = ++read.current;
    const isCurrent = () => started === sessionEpoch() && read.current === id;
    try {
      const next = await flow.status();
      if (!isCurrent()) return;
      setStatus(next);
      setQuotes([]);
      if (next.checkoutEnabled && !next.subscription && !next.pending) {
        const current = await flow.quotes();
        if (isCurrent()) setQuotes(current);
      }
    } catch (err) {
      if (
        !isStale(err) &&
        isCurrent() &&
        !(err instanceof APIError && err.status === 404)
      )
        setError(message(err));
    }
  }

  useEffect(() => {
    const verify = () => void refresh();
    verify();
    window.addEventListener(billingVerifiedEvent, verify);
    return () => {
      read.current++;
      window.removeEventListener(billingVerifiedEvent, verify);
    };
  }, [flow]);

  async function run(action: NonNullable<typeof flow>["restore"]) {
    if (!flow || busy) return;
    const started = sessionEpoch();
    setBusy(true);
    setError("");
    setNotice("");
    let refreshed = false;
    try {
      const result = await action();
      if (started !== sessionEpoch()) return;
      if (result === "verified") {
        refreshed = true;
        window.dispatchEvent(new Event(billingVerifiedEvent));
      }
      setNotice(
        {
          verified: "Purchase verified.",
          pending:
            "Your purchase is still being checked. You can safely come back later.",
          unavailable: "This purchase option is unavailable.",
          empty: "No purchases were found for this store account.",
        }[result],
      );
    } catch (err) {
      if (!isStale(err) && started === sessionEpoch()) setError(message(err));
    } finally {
      if (started === sessionEpoch()) {
        setBusy(false);
        if (!refreshed) await refresh();
      }
    }
  }

  if (!flow || (!status && !error)) return null;
  const subscription = status?.subscription;
  const manage = subscription && management[subscription.store];
  const reservation = status?.resume;

  return (
    <details className="account-details subscription-details">
      <summary>Subscription</summary>
      {error && <Alert>{error}</Alert>}
      {notice && <p role="status">{notice}</p>}
      {subscription && manage && (
        <>
          <p>
            Premium ·{" "}
            {subscription.interval === "monthly" ? "Monthly" : "Annual"}
          </p>
          <p>
            Paid through{" "}
            {new Date(subscription.paidThrough).toLocaleDateString()}.
          </p>
          <p>
            <a href={manage.href}>{manage.title}</a>
          </p>
        </>
      )}
      {status?.pending && !subscription && (
        <>
          <p>A purchase is pending verification.</p>
          {status.restoreAvailable && (
            <p>
              If the store shows a charge, restore purchases using the same
              store account.
            </p>
          )}
          <button className="button secondary" type="button" onClick={onHelp}>
            Get help with this purchase
          </button>
        </>
      )}
      {reservation && (
        <button
          className="button secondary"
          type="button"
          disabled={busy || (!reservation.launched && !status?.checkoutEnabled)}
          onClick={() =>
            void run(
              reservation.launched
                ? flow.resume
                : () => flow.purchase(reservation.interval),
            )
          }
        >
          {reservation.launched ? "Check purchase status" : "Continue purchase"}
        </button>
      )}
      {quotes.map((quote) => (
        <div key={quote.interval}>
          <p>
            {quote.displayPrice} per{" "}
            {quote.interval === "monthly" ? "month" : "year"}. Renews until
            cancelled in the store.
          </p>
          <button
            className="button primary"
            type="button"
            disabled={busy}
            onClick={() => void run(() => flow.purchase(quote.interval))}
          >
            Subscribe {quote.interval === "monthly" ? "monthly" : "annually"}
          </button>
        </div>
      ))}
      {status?.restoreAvailable && (
        <button
          className="button secondary"
          type="button"
          disabled={busy}
          onClick={() => void run(flow.restore)}
        >
          Restore purchases
        </button>
      )}
      {!status && (
        <button
          className="button secondary"
          type="button"
          onClick={() => void refresh()}
        >
          Check subscription status
        </button>
      )}
    </details>
  );
}
