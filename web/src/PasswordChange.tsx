import {
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type FormEvent,
} from "react";
import {
  APIError,
  canChangePassword,
  changePassword,
  isStale,
  message,
} from "./api";
import { signedOut } from "./shopping-cache";
import { Field } from "./components";

export const passwordLengthHint = "Use 10 to 128 characters.";

export function passwordLengthOK(raw: string): boolean {
  const normalized = raw.normalize("NFC");
  const length = Array.from(normalized).length;
  const encoder = new TextEncoder();
  return (
    length >= 10 &&
    length <= 128 &&
    encoder.encode(raw).length <= 512 &&
    encoder.encode(normalized).length <= 512
  );
}

type PasswordField = "currentPassword" | "newPassword" | "confirmation";
type PasswordProblem = { text: string; field?: PasswordField };

function inputProblem(error: unknown): PasswordProblem {
  const code = error instanceof APIError ? error.code : "";
  if (code === "current_password_incorrect")
    return { text: message(error), field: "currentPassword" };
  if (["password_length", "password_common", "password_reused"].includes(code))
    return { text: message(error), field: "newPassword" };
  return { text: message(error) };
}

export function PasswordChange({
  active,
  email,
  onChanged,
}: {
  active: boolean;
  email: string;
  onChanged: () => void;
}) {
  const formRef = useRef<HTMLFormElement>(null);
  const detailsRef = useRef<HTMLDetailsElement>(null);
  const revision = useRef(0);
  const errorID = useId();
  const [problem, setProblem] = useState<PasswordProblem | null>(null);
  const [busy, setBusy] = useState(false);
  const [success, setSuccess] = useState(false);

  function clear() {
    revision.current++;
    formRef.current?.reset();
    setProblem(null);
  }

  useLayoutEffect(() => {
    if (!active) {
      revision.current++;
      formRef.current?.reset();
      setProblem(null);
      setSuccess(false);
    }
    return () => {
      revision.current++;
    };
  }, [active]);

  useLayoutEffect(() => {
    if (!success) return;
    // Signal completion after the submitted form leaves the document.
    // Preserve the route and its existing history state.
    history.replaceState(history.state, "", location.href);
    if (detailsRef.current) detailsRef.current.open = false;
  }, [success]);

  function showProblem(form: HTMLFormElement, next: PasswordProblem) {
    setProblem(next);
    if (next.field) {
      const input = form.elements.namedItem(next.field);
      if (input instanceof HTMLInputElement) input.focus();
    }
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy || !active || signedOut()) return;
    const form = event.currentTarget;
    const inputs = new FormData(form);
    const currentPassword = String(inputs.get("currentPassword") ?? "");
    const newPassword = String(inputs.get("newPassword") ?? "");
    const confirmation = String(inputs.get("confirmation") ?? "");
    setSuccess(false);
    setProblem(null);
    if (newPassword !== confirmation) {
      showProblem(form, {
        text: "The new passwords do not match.",
        field: "confirmation",
      });
      return;
    }
    if (!passwordLengthOK(newPassword)) {
      showProblem(form, { text: passwordLengthHint, field: "newPassword" });
      return;
    }
    const before = revision.current;
    const isCurrent = () =>
      before === revision.current && detailsRef.current?.open && !signedOut();
    setBusy(true);
    try {
      await changePassword({ currentPassword, newPassword });
      if (isCurrent()) {
        setSuccess(true);
        onChanged();
      }
    } catch (error) {
      if (isCurrent() && !isStale(error))
        showProblem(form, inputProblem(error));
    } finally {
      setBusy(false);
    }
  }

  function described(name: PasswordField) {
    return problem?.field === name
      ? { "aria-invalid": true as const, "aria-describedby": errorID }
      : {};
  }

  return (
    <details
      className="password-change"
      ref={detailsRef}
      onToggle={(event) => {
        if (event.currentTarget.open) setSuccess(false);
        else clear();
      }}
    >
      <summary>Change password</summary>
      {success ? null : canChangePassword() ? (
        <form
          ref={formRef}
          method="post"
          onSubmit={submit}
          className="password-change-form"
        >
          <Field label="Sign-in email">
            <input
              id="password-change-email"
              type="email"
              name="username"
              autoComplete="username"
              value={email}
              readOnly
              tabIndex={-1}
            />
          </Field>
          <Field label="Current password">
            <input
              id="password-change-current"
              type="password"
              name="currentPassword"
              autoComplete="current-password"
              required
              {...described("currentPassword")}
            />
          </Field>
          <Field label="New password" hint={passwordLengthHint}>
            <input
              id="password-change-new"
              type="password"
              name="newPassword"
              autoComplete="new-password"
              required
              {...described("newPassword")}
            />
          </Field>
          <Field label="Confirm new password">
            <input
              id="password-change-confirm"
              type="password"
              name="confirmation"
              autoComplete="new-password"
              required
              {...described("confirmation")}
            />
          </Field>
          {problem && (
            <p className="alert" role="alert" id={errorID}>
              {problem.text}
            </p>
          )}
          <button type="submit" className="button primary" disabled={busy}>
            {busy ? "Saving…" : "Save password"}
          </button>
        </form>
      ) : (
        <p className="password-browser-help">
          Open MyChef in a supported secure browser to change your password.
        </p>
      )}
    </details>
  );
}
