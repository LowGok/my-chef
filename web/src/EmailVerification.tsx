import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";
import {
  api,
  confirmEmailVerification,
  isStale,
  message,
  sessionEpoch,
  type Session,
} from "./api";
import type { components } from "./api.generated";
import { Alert, Brand, Field } from "./components";
import { passwordLengthHint, passwordLengthOK } from "./PasswordChange";

type Schemas = components["schemas"];
type ProofKind = "signup" | "verify-email" | "change-email";
export type ProofEntry = { kind: ProofKind; proof: string };
export function readEmailProof(): ProofEntry | null {
  const path = location.pathname;
  const kind = path.slice("/app/".length);
  if (
    path !== "/app/" + kind ||
    (kind !== "signup" && kind !== "verify-email" && kind !== "change-email")
  )
    return null;
  const fragment = location.hash.slice(1);
  return { kind, proof: /^[A-Za-z0-9_-]{43}$/.test(fragment) ? fragment : "" };
}

function VerificationPage({ children }: { children: ReactNode }) {
  return (
    <main className="welcome email-verification">
      <section className="welcome-form">
        <div className="auth-card">
          <Brand />
          {children}
        </div>
      </section>
    </main>
  );
}

export function EmailChangeForm({
  active,
  correction = false,
}: {
  active: boolean;
  correction?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState("");
  const [sent, setSent] = useState(false);
  const details = useRef<HTMLDetailsElement>(null);
  const operation = useRef<AbortController | null>(null);
  const visible = active && open;
  useLayoutEffect(() => {
    if (!visible) {
      operation.current?.abort();
      operation.current = null;
      setBusy(false);
      setProblem("");
      setSent(false);
    }
    if (!active && details.current?.open) details.current.open = false;
    return () => operation.current?.abort();
  }, [active, visible]);

  async function start(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (operation.current) return;
    const form = event.currentTarget;
    const fields = new FormData(form);
    const body: Schemas["EmailChangeStart"] = {
      email: String(fields.get("email") ?? ""),
      currentPassword: String(fields.get("currentPassword") ?? ""),
    };
    const password = form.elements.namedItem(
      "currentPassword",
    ) as HTMLInputElement;
    password.value = "";
    const controller = new AbortController();
    operation.current = controller;
    setBusy(true);
    setProblem("");
    setSent(false);
    const started = sessionEpoch();
    try {
      await api<Schemas["EmailRequestAccepted"]>("/email/change", {
        method: "POST",
        body: JSON.stringify(body),
        signal: controller.signal,
      });
      if (!controller.signal.aborted && started === sessionEpoch()) {
        form.reset();
        setSent(true);
      }
    } catch (error) {
      if (
        !controller.signal.aborted &&
        started === sessionEpoch() &&
        !isStale(error)
      )
        setProblem(message(error));
    } finally {
      if (operation.current === controller) {
        operation.current = null;
        setBusy(false);
      }
    }
  }

  return (
    <details
      className="email-change"
      ref={details}
      onToggle={(event) => setOpen(event.currentTarget.open)}
    >
      <summary>
        {correction ? "Correct your email address" : "Change email address"}
      </summary>
      {visible && (
        <form className="email-change-form" method="post" onSubmit={start}>
          <p>
            {correction
              ? "Confirm your corrected address using the link sent to the new inbox."
              : "Keep using your current address until you confirm the change from both inboxes."}
          </p>
          {problem && <Alert>{problem}</Alert>}
          {sent && (
            <p role="status">
              Request received. Check{" "}
              {correction ? "the new inbox" : "both inboxes"} for confirmation
              instructions.
            </p>
          )}
          <Field label="New email address">
            <input
              type="text"
              inputMode="email"
              name="email"
              autoComplete="email"
              autoCapitalize="none"
              spellCheck={false}
              maxLength={254}
              required
            />
          </Field>
          <Field label="Current password">
            <input
              type="password"
              name="currentPassword"
              autoComplete="current-password"
              required
            />
          </Field>
          <button className="button primary full" disabled={busy}>
            {busy ? "Sending…" : "Send confirmation"}
          </button>
        </form>
      )}
    </details>
  );
}

export function VerificationRequired({ onSignOut }: { onSignOut: () => void }) {
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const operation = useRef<AbortController | null>(null);
  useEffect(() => () => operation.current?.abort(), []);
  async function resend() {
    if (operation.current) return;
    const controller = new AbortController();
    operation.current = controller;
    setBusy(true);
    setNotice("");
    setError("");
    try {
      await api<Schemas["EmailRequestAccepted"]>("/email/verification/resend", {
        method: "POST",
        body: "{}",
        signal: controller.signal,
      });
      if (!controller.signal.aborted)
        setNotice("Verification email requested. Check your inbox.");
    } catch (problem) {
      if (!controller.signal.aborted && !isStale(problem))
        setError(message(problem));
    } finally {
      if (!controller.signal.aborted) setBusy(false);
      operation.current = null;
    }
  }
  return (
    <VerificationPage>
      <h2>Verify your email</h2>
      <p>
        Open the verification link from your inbox in this browser to continue
        to MyChef.
      </p>
      {error && <Alert>{error}</Alert>}
      {notice && <p role="status">{notice}</p>}
      <button
        className="button primary full"
        disabled={busy}
        onClick={() => void resend()}
      >
        {busy ? "Sending…" : "Send verification email"}
      </button>
      <EmailChangeForm active correction />
      <p>
        <a href="mailto:admin@example.invalid">Contact support</a>
      </p>
      <button className="button secondary full" onClick={onSignOut}>
        Sign out
      </button>
    </VerificationPage>
  );
}

const decisions = {
  approve: {
    text: "Approve the request from your current inbox.",
    button: "Approve email change",
  },
  confirm: {
    text: "Confirm you own this inbox.",
    button: "Confirm email address",
  },
  cancel: {
    text: "Keep the current email address and cancel this request.",
    button: "Cancel email change",
  },
};

export function EmailProofPage({
  entry,
  restricted,
  signedIn,
  onVerified,
  onReauthenticate,
  onSignOut,
  onExit,
}: {
  entry: ProofEntry;
  restricted: boolean;
  signedIn: boolean;
  onVerified: (value: Session) => void;
  onReauthenticate: () => void;
  onSignOut: () => void;
  onExit: () => void;
}) {
  const { kind, proof } = entry;
  const [profile, setProfile] = useState<Schemas["SignUpProfile"]>();
  const [change, setChange] = useState<Schemas["EmailChangeView"]>();
  const [problem, setProblem] = useState("");
  const [result, setResult] = useState("");
  const [busy, setBusy] = useState(false);
  const active = useRef(true);
  const operation = useRef<AbortController | null>(null);
  const signedInSignUp = kind === "signup" && (signedIn || restricted);
  const canInspect = !!proof && !signedInSignUp && kind !== "verify-email";
  useLayoutEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
      operation.current?.abort();
    };
  }, []);

  useEffect(() => {
    if (!canInspect) return;
    const controller = new AbortController();
    const options = {
      method: "POST",
      body: JSON.stringify({ proof }),
      signal: controller.signal,
    };
    const request =
      kind === "signup"
        ? api<Schemas["SignUpProfile"]>("/signup/inspect", options).then(
            (value) => {
              if (!controller.signal.aborted) setProfile(value);
            },
          )
        : api<Schemas["EmailChangeView"]>(
            "/email/change/inspect",
            options,
          ).then((value) => {
            if (!controller.signal.aborted) setChange(value);
          });
    void request.catch((error) => {
      if (!controller.signal.aborted && !isStale(error))
        setProblem(message(error));
    });
    return () => controller.abort();
  }, [canInspect, kind, proof]);

  async function apply(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!proof || operation.current) return;
    const form = event.currentTarget;
    const fields = new FormData(form);
    const get = (name: string) => String(fields.get(name) ?? "");
    let body: Schemas["SignUpCompletion"] | undefined;
    setProblem("");
    if (kind === "signup") {
      const password = get("password");
      if (!passwordLengthOK(password)) {
        setProblem(passwordLengthHint);
        (form.elements.namedItem("password") as HTMLInputElement).focus();
        return;
      }
      if (password !== get("confirmation")) {
        setProblem("The passwords don’t match.");
        (form.elements.namedItem("confirmation") as HTMLInputElement).focus();
        return;
      }
      body = {
        proof,
        password,
        firstName: get("firstName"),
        lastName: get("lastName"),
        username: get("username"),
      };
      for (const name of ["password", "confirmation"])
        (form.elements.namedItem(name) as HTMLInputElement).value = "";
    }
    const controller = new AbortController();
    operation.current = controller;
    setBusy(true);
    try {
      if (kind === "verify-email") {
        const value = await confirmEmailVerification(proof);
        if (!controller.signal.aborted && active.current) onVerified(value);
        return;
      }
      const options = {
        method: "POST",
        body: JSON.stringify(body ?? { proof }),
        signal: controller.signal,
      };
      if (kind === "signup") {
        await api<Schemas["SignUpCreated"]>("/signup/complete", options);
        if (!controller.signal.aborted)
          setResult("Account created. Sign in to MyChef.");
      } else {
        const outcome = await api<Schemas["EmailChangeDecision"]>(
          "/email/change/decision",
          options,
        );
        if (controller.signal.aborted) return;
        setResult(
          outcome.status === "pending"
            ? "Confirmed. The other inbox still needs to approve."
            : outcome.status === "cancelled"
              ? "Email change cancelled. Your address hasn’t changed."
              : "Email address updated. Sign in with the new address.",
        );
        if (outcome.signInRequired) onReauthenticate();
      }
    } catch (error) {
      if (!controller.signal.aborted && !isStale(error))
        setProblem(message(error));
    } finally {
      if (operation.current === controller) {
        operation.current = null;
        if (!controller.signal.aborted) setBusy(false);
      }
    }
  }

  return (
    <VerificationPage>
      <h2>
        {kind === "signup"
          ? "Finish signing up"
          : kind === "verify-email"
            ? "Verify your email"
            : "Confirm email change"}
      </h2>
      {!proof ? (
        <p>Open the link from your email.</p>
      ) : result ? (
        <p role="status">{result}</p>
      ) : (
        <>
          {problem && <Alert>{problem}</Alert>}
          {signedInSignUp && (
            <>
              <p>Sign out of this account, then open the sign-up link again.</p>
              <button className="button secondary full" onClick={onSignOut}>
                Sign out
              </button>
            </>
          )}
          {kind === "verify-email" && !restricted && (
            <p>
              Sign in to the account that requested the verification email, then
              open the link again.
            </p>
          )}
          {kind === "verify-email" && restricted && (
            <form method="post" onSubmit={apply}>
              <p>Confirm that this inbox belongs to you.</p>
              <button className="button primary full" disabled={busy}>
                {busy ? "Verifying…" : "Verify email"}
              </button>
            </form>
          )}
          {kind === "signup" && !signedInSignUp && profile && (
            <form method="post" onSubmit={apply}>
              <Field label="First name">
                <input
                  name="firstName"
                  autoComplete="given-name"
                  defaultValue={profile.firstName}
                  maxLength={100}
                  required
                />
              </Field>
              <Field label="Last name">
                <input
                  name="lastName"
                  autoComplete="family-name"
                  defaultValue={profile.lastName}
                  maxLength={100}
                  required
                />
              </Field>
              <Field label="Username">
                <input
                  name="username"
                  autoComplete="nickname"
                  defaultValue={profile.username}
                  maxLength={80}
                  required
                />
              </Field>
              <Field label="Password" hint={passwordLengthHint}>
                <input
                  name="password"
                  type="password"
                  autoComplete="new-password"
                  required
                />
              </Field>
              <Field label="Confirm password">
                <input
                  name="confirmation"
                  type="password"
                  autoComplete="new-password"
                  required
                />
              </Field>
              <button className="button primary full" disabled={busy}>
                {busy ? "Creating…" : "Create account"}
              </button>
            </form>
          )}
          {kind === "change-email" && change && (
            <form method="post" onSubmit={apply}>
              <p>{decisions[change.action].text}</p>
              {change.maskedAddress && (
                <p>Requested address: {change.maskedAddress}</p>
              )}
              <button className="button primary full" disabled={busy}>
                {busy ? "Please wait…" : decisions[change.action].button}
              </button>
            </form>
          )}
          {canInspect && !change && !profile && !problem && (
            <p role="status">Checking the link…</p>
          )}
        </>
      )}
      <button
        className="button secondary full email-proof-back"
        onClick={onExit}
      >
        Back to MyChef
      </button>
    </VerificationPage>
  );
}
