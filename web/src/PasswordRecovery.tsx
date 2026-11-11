import {
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type FormEvent,
  type MouseEvent,
  type ReactNode,
} from "react";
import {
  APIError,
  completePasswordRecovery,
  isStale,
  message,
  requestPasswordRecovery,
  sessionEpoch,
} from "./api";
import { Brand, Field } from "./components";
import { passwordLengthHint, passwordLengthOK } from "./PasswordChange";

export const recoveryRequestURL = "/app/?view=forgot-password";
export type RecoveryEntry = { kind: "reset-password"; proof: string };
const invalidGuidance =
  "This reset link is invalid or has expired. Request a new one.";

export function readRecoveryProof(): RecoveryEntry | null {
  if (location.pathname !== "/app/reset-password") return null;
  const fragment = location.hash.slice(1);
  return {
    kind: "reset-password",
    proof: /^[A-Za-z0-9_-]{43}$/.test(fragment) ? fragment : "",
  };
}

export function RecoveryLink({
  href,
  onNavigate,
  children,
}: {
  href: string;
  onNavigate: () => void;
  children: ReactNode;
}) {
  function follow(event: MouseEvent<HTMLAnchorElement>) {
    if (
      event.defaultPrevented ||
      event.button !== 0 ||
      event.metaKey ||
      event.ctrlKey ||
      event.shiftKey ||
      event.altKey
    )
      return;
    event.preventDefault();
    onNavigate();
  }

  return (
    <a className="recovery-link" href={href} onClick={follow}>
      {children}
    </a>
  );
}

export function PasswordRecovery({
  entry,
  signedIn,
  viewGeneration,
  onRequest,
  onSignOut,
  onExit,
  onForgetProof,
}: {
  entry?: RecoveryEntry;
  signedIn: boolean;
  viewGeneration: () => number;
  onRequest: () => void;
  onSignOut: () => void;
  onExit: () => void;
  onForgetProof: () => void;
}) {
  const [result, setResult] = useState<"requested" | "reset" | "sign-out">();
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<{
    text: string;
    field?: "password" | "confirmation";
  }>();
  const heading = useRef<HTMLHeadingElement>(null);
  const operation = useRef<AbortController | null>(null);
  const problemId = useId();

  useLayoutEffect(() => {
    heading.current?.focus();
    const abort = () => {
      operation.current?.abort();
      operation.current = null;
    };
    const resume = (event: PageTransitionEvent) => {
      if (event.persisted) setBusy(false);
    };
    window.addEventListener("pagehide", abort);
    window.addEventListener("pageshow", resume);
    return () => {
      window.removeEventListener("pagehide", abort);
      window.removeEventListener("pageshow", resume);
      abort();
    };
  }, []);

  function fieldProblem(
    form: HTMLFormElement,
    field: "password" | "confirmation",
    text: string,
  ) {
    setProblem({ field, text });
    const input = form.elements.namedItem(field);
    if (input instanceof HTMLInputElement) input.focus();
  }

  function describe(field: "password" | "confirmation") {
    if (problem?.field === field)
      return { "aria-invalid": true as const, "aria-describedby": problemId };
    return {};
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (operation.current || signedIn || (entry && !entry.proof)) return;
    const form = event.currentTarget;
    const values = new FormData(form);
    const password = String(values.get("password") ?? "");
    setProblem(undefined);
    if (entry) {
      if (password !== values.get("confirmation")) {
        fieldProblem(form, "confirmation", "Passwords don’t match.");
        return;
      }
      if (!passwordLengthOK(password)) {
        fieldProblem(form, "password", passwordLengthHint);
        return;
      }
    }
    const controller = new AbortController();
    operation.current = controller;
    setBusy(true);
    const epoch = sessionEpoch();
    const generation = viewGeneration();
    const current = () =>
      !controller.signal.aborted &&
      epoch === sessionEpoch() &&
      generation === viewGeneration();
    try {
      if (entry) {
        await completePasswordRecovery(
          { proof: entry.proof, password },
          controller.signal,
        );
      } else {
        await requestPasswordRecovery(
          { email: String(values.get("email") ?? "").trim() },
          controller.signal,
        );
      }
      if (!current()) return;
      form.reset();
      setResult(entry ? "reset" : "requested");
      if (entry) onForgetProof();
    } catch (error) {
      if (!current() || isStale(error)) return;
      const code = error instanceof APIError ? error.code : "";
      if (code === "invalid_link") {
        form.reset();
        onForgetProof();
      } else if (code === "sign_out_required") {
        form.reset();
        setResult("sign-out");
        if (entry) onForgetProof();
      } else if (code === "password_length" || code === "password_common") {
        fieldProblem(form, "password", message(error));
      } else {
        setProblem({
          text:
            error instanceof APIError && error.status === 0
              ? "Check your connection and try again."
              : code === "unauthenticated" || code === "csrf_invalid"
                ? entry
                  ? "Your session expired. Reopen the link from your email."
                  : "Your session expired. Reload and try again."
                : message(error),
        });
      }
    } finally {
      if (operation.current === controller) {
        operation.current = null;
        if (current()) setBusy(false);
      }
    }
  }

  const requiresSignOut = signedIn || result === "sign-out";
  return (
    <main className="welcome password-recovery">
      <section className="welcome-form">
        <div className="auth-card">
          <Brand />
          <h2 ref={heading} tabIndex={-1}>
            {entry ? "Choose a new password" : "Reset your password"}
          </h2>
          {requiresSignOut ? (
            <>
              <p>
                {entry
                  ? "Sign out and reopen the link from your email."
                  : "Sign out before requesting a reset link."}
              </p>
              <button className="button primary full" onClick={onSignOut}>
                Sign out
              </button>
            </>
          ) : result === "reset" ? (
            <p role="status">
              <strong>Password reset.</strong> Sign in with your new password.
            </p>
          ) : result === "requested" ? (
            <div role="status">
              <strong>Check your inbox.</strong>
              <p>
                If your email is verified, you’ll receive a link to reset your
                password.
              </p>
            </div>
          ) : entry && !entry.proof ? (
            <>
              <p role="alert">{invalidGuidance}</p>
              <RecoveryLink href={recoveryRequestURL} onNavigate={onRequest}>
                Request a new link
              </RecoveryLink>
            </>
          ) : (
            <form method="post" onSubmit={submit} aria-busy={busy}>
              {problem && (
                <p className="alert" role="alert" id={problemId}>
                  {problem.text}
                </p>
              )}
              {entry ? (
                <>
                  <Field label="New password" hint={passwordLengthHint}>
                    <input
                      name="password"
                      type="password"
                      autoComplete="new-password"
                      required
                      {...describe("password")}
                    />
                  </Field>
                  <Field label="Confirm new password">
                    <input
                      name="confirmation"
                      type="password"
                      autoComplete="new-password"
                      required
                      {...describe("confirmation")}
                    />
                  </Field>
                </>
              ) : (
                <Field label="Email">
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
              )}
              <button className="button primary full" disabled={busy}>
                {busy
                  ? "Please wait…"
                  : entry
                    ? "Reset password"
                    : "Send reset link"}
              </button>
            </form>
          )}
          <RecoveryLink href="/app/" onNavigate={onExit}>
            Back to sign in
          </RecoveryLink>
        </div>
      </section>
    </main>
  );
}
