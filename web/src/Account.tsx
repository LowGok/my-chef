import {
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
  type FormEvent,
} from "react";
import {
  APIError,
  deleteOwnAccount,
  requestAccountExport,
  saveAccountDownload,
  signIn,
  requestSignUp,
  api,
  message,
  sessionEpoch,
  isStale,
  type Profile,
  type ProfileUpdate,
  type Session,
} from "./api";
import { Alert, Brand, Field, Icon } from "./components";
import { AllergenPicker } from "./Choices";
import { today } from "./dates";
import { effortLevels } from "./effort";
import { focusContentTop } from "./scroll";
import { PasswordChange } from "./PasswordChange";
import { EmailChangeForm } from "./EmailVerification";

import { RecoveryLink, recoveryRequestURL } from "./PasswordRecovery";

export function Welcome({
  onSession,
  onForgotPassword,
}: {
  onSession: (value: Session) => void;
  onForgotPassword: () => void;
}) {
  const [register, setRegister] = useState(false),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [requested, setRequested] = useState(false);
  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const form = new FormData(e.currentTarget);
    const get = (name: string) => String(form.get(name) ?? "");
    try {
      if (register) {
        await requestSignUp({
          firstName: get("firstName"),
          lastName: get("lastName"),
          username: get("username"),
          email: get("email"),
        });
        setRequested(true);
      } else {
        onSession(
          await signIn(
            JSON.stringify({ email: get("email"), password: get("password") }),
          ),
        );
      }
    } catch (err) {
      if (!isStale(err)) setError(message(err));
    } finally {
      setBusy(false);
    }
  }
  return (
    <main className="welcome">
      <section className="welcome-form">
        <div className="auth-card">
          <Brand />
          <p className="auth-tagline">Saved recipes and shopping lists</p>
          <h2>{register ? "Let’s get started." : "Welcome back."}</h2>
          <div className="auth-tabs">
            <button
              className={!register ? "active" : ""}
              aria-pressed={!register}
              disabled={busy}
              onClick={() => {
                setRegister(false);
                setRequested(false);
                setError("");
              }}
            >
              Sign in
            </button>
            <button
              className={register ? "active" : ""}
              aria-label="Create an account"
              aria-pressed={register}
              disabled={busy}
              onClick={() => {
                setRegister(true);
                setRequested(false);
                setError("");
              }}
            >
              Sign up
            </button>
          </div>
          {error && <Alert>{error}</Alert>}
          {register && requested ? (
            <div role="status">
              <h3>Check your inbox</h3>
              <p>
                If you can create an account with this email, you’ll receive a
                link to finish setting it up. Already have an account? Sign in
                as usual.
              </p>
            </div>
          ) : (
            <form method="post" onSubmit={submit}>
              {register && (
                <>
                  <div className="form-row">
                    <Field label="First name">
                      <input
                        name="firstName"
                        autoComplete="given-name"
                        maxLength={100}
                        required
                      />
                    </Field>
                    <Field label="Last name">
                      <input
                        name="lastName"
                        autoComplete="family-name"
                        maxLength={100}
                        required
                      />
                    </Field>
                  </div>
                  <Field label="Username">
                    <input
                      name="username"
                      autoComplete="nickname"
                      maxLength={80}
                      required
                    />
                  </Field>
                </>
              )}
              <Field label="Email">
                <input
                  id="sign-in-email"
                  name="email"
                  type="text"
                  inputMode="email"
                  autoComplete="username"
                  autoCapitalize="none"
                  spellCheck={false}
                  maxLength={254}
                  required
                />
              </Field>
              {!register && (
                <Field label="Password">
                  <input
                    id="sign-in-password"
                    name="password"
                    type="password"
                    autoComplete="current-password"
                    required
                  />
                </Field>
              )}
              <button
                className="button primary full"
                disabled={busy}
                aria-label={
                  register ? "Create my account" : "Sign in to MyChef"
                }
              >
                {busy
                  ? "Please wait…"
                  : register
                    ? "Create account"
                    : "Sign in"}
                <Icon name="arrow" />
              </button>
            </form>
          )}
          {!register && (
            <RecoveryLink
              href={recoveryRequestURL}
              onNavigate={onForgotPassword}
            >
              Forgot password?
            </RecoveryLink>
          )}
        </div>
      </section>
    </main>
  );
}

export function ProfileForm({
  active,
  favorites,
  personalRecipes,
  user,
  onSave,
  onLogout,
  onFeedback,
  onDeleted,
}: {
  active: boolean;
  favorites?: ReactNode;
  personalRecipes?: ReactNode;
  user: Profile;
  onSave: (profile: Profile) => void;
  onLogout: () => void;
  onFeedback: () => void;
  onDeleted: () => void;
}) {
  const [error, setError] = useState(""),
    [saved, setSaved] = useState<{ text: string } | null>(null),
    [busy, setBusy] = useState(false);
  const profileForm = useId();
  const [detailsOpen, setDetailsOpen] = useState(false);
  const savedEffort = user.preferences.effort;
  const legacyEffort =
    savedEffort && !effortLevels.some((level) => level.label === savedEffort);
  const profileHeading = useRef<HTMLHeadingElement>(null);
  useLayoutEffect(() => {
    if (!active) setSaved(null);
    else if (saved)
      return focusContentTop(profileHeading.current, profileHeading.current);
  }, [active, saved]);
  async function submit(e: FormEvent<HTMLFormElement>) {
    const started = sessionEpoch();
    e.preventDefault();
    const submitter = (e.nativeEvent as SubmitEvent).submitter;
    const savingDetails = submitter?.getAttribute("value") === "account";
    setBusy(true);
    setError("");
    setSaved(null);
    const form = new FormData(e.currentTarget);
    const get = (k: string) => String(form.get(k) ?? "").trim();
    const list = (k: string) =>
      get(k)
        .split(/\r?\n/)
        .map((v) => v.trim())
        .filter(Boolean);
    const body: ProfileUpdate = {
      firstName: get("firstName"),
      lastName: get("lastName"),
      username: get("username"),
      birthdate: get("birthdate") || null,
      sex: user.sex,
      preferences: {
        diet: list("diet"),
        allergens: list("allergens"),
        dislikes: list("dislikes"),
        technique: list("technique"),
        effort: get("effort"),
      },
    };
    try {
      const value = await api<Profile>("/profile", {
        method: "PUT",
        body: JSON.stringify(body),
      });
      if (started !== sessionEpoch()) return;
      onSave(value);
      setSaved({
        text: savingDetails ? "Account details saved." : "Preferences saved.",
      });
    } catch (err) {
      if (!isStale(err)) setError(message(err));
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <section className="profile-feedback" aria-label="Feedback">
        <button className="button full feedback-button" onClick={onFeedback}>
          Provide your feedback
          <Icon name="arrow" />
        </button>
      </section>
      <div className="page-heading profile-heading">
        <span className="eyebrow">Made for you</span>
        <h2 ref={profileHeading} tabIndex={-1}>
          Your profile.
        </h2>
        {saved && (
          <p className="success" role="status">
            {saved.text}
          </p>
        )}
      </div>
      <section className="panel profile-panel" aria-label="Profile settings">
        {error && <Alert>{error}</Alert>}
        <details
          className="account-details"
          onToggle={(event) => setDetailsOpen(event.currentTarget.open)}
        >
          <summary>Account details</summary>
          <form id={profileForm} onSubmit={submit}>
            <div className="form-row">
              <Field label="First name">
                <input
                  name="firstName"
                  defaultValue={user.firstName}
                  maxLength={100}
                  required
                />
              </Field>
              <Field label="Last name">
                <input
                  name="lastName"
                  defaultValue={user.lastName}
                  maxLength={100}
                  required
                />
              </Field>
            </div>
            <div className="form-row">
              <Field label="Username">
                <input
                  name="username"
                  defaultValue={user.username}
                  maxLength={80}
                  required
                />
              </Field>
              <Field label="Date of birth">
                <input
                  name="birthdate"
                  type="date"
                  defaultValue={user.birthdate ?? ""}
                  max={today()}
                />
              </Field>
            </div>
            <p className="small muted">Account email: {user.email}</p>
            <button
              type="submit"
              value="account"
              className="button primary account-save"
              disabled={busy}
            >
              {busy ? "Saving…" : "Save"}
            </button>
          </form>
          <EmailChangeForm active={active && detailsOpen} />
          <PasswordChange
            active={active && detailsOpen}
            email={user.email}
            onChanged={() =>
              setSaved({
                text: "Password changed. Other devices need to sign in again.",
              })
            }
          />
          <AccountLifecycle
            active={active && detailsOpen}
            onDeleted={onDeleted}
          />
        </details>
        <details className="account-details preferences-details" open>
          <summary>Preferences</summary>
          <div className="form-row">
            <Field label="Diet">
              <textarea
                name="diet"
                form={profileForm}
                rows={3}
                defaultValue={user.preferences.diet.join("\n")}
                placeholder="Vegetarian"
              />
            </Field>
          </div>
          <AllergenPicker
            initial={user.preferences.allergens}
            form={profileForm}
          />
          <div className="form-row">
            <Field label="Disliked ingredients">
              <textarea
                name="dislikes"
                form={profileForm}
                rows={3}
                defaultValue={user.preferences.dislikes.join("\n")}
                placeholder="Mushrooms"
              />
            </Field>
            <Field label="Cooking techniques">
              <textarea
                name="technique"
                form={profileForm}
                rows={3}
                defaultValue={user.preferences.technique.join("\n")}
                placeholder="Roasting"
              />
            </Field>
          </div>
          <Field
            label="Cooking effort"
            hint={legacyEffort ? `Saved preference: ${savedEffort}` : undefined}
          >
            <select name="effort" form={profileForm} defaultValue={savedEffort}>
              <option value="" disabled>
                Choose effort
              </option>
              {legacyEffort && (
                <option value={savedEffort}>Saved preference</option>
              )}
              {effortLevels.map((level) => (
                <option key={level.minutes} value={level.label}>
                  {level.shortLabel}
                </option>
              ))}
            </select>
          </Field>
          <p className="advisory">
            Always review ingredients and product labels before cooking. AI
            suggestions cannot guarantee allergy safety.
          </p>
          <button
            type="submit"
            form={profileForm}
            className="button primary"
            disabled={busy}
          >
            {busy ? "Saving…" : "Save preferences"}
          </button>
        </details>
        {favorites}
        {personalRecipes}
      </section>
      <section className="panel profile-panel profile-support">
        <details className="account-details">
          <summary>
            <Icon name="help" />
            Help &amp; support
          </summary>
          <p>Need a hand or found a bug?</p>
          <a className="support-link" href="mailto:admin@example.invalid">
            Contact support
            <Icon name="arrow" />
          </a>
        </details>
        <details className="account-details">
          <summary>
            <Icon name="info" />
            About
          </summary>
          <p>MyChef organizes recipes and shopping lists.</p>
          
        </details>
      </section>
      <div className="profile-actions">
        <button className="button secondary" onClick={onLogout}>
          <Icon name="logout" />
          Sign out
        </button>
      </div>
    </>
  );
}

export function AccountLifecycle({
  active,
  onDeleted,
}: {
  active: boolean;
  onDeleted: () => void;
}) {
  const [mode, setMode] = useState<"export" | "delete" | null>(null);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<{
    text: string;
    field?: string;
  } | null>(null);
  const [notice, setNotice] = useState("");
  const dialog = useRef<HTMLDialogElement>(null);
  const form = useRef<HTMLFormElement>(null);
  const trigger = useRef<HTMLElement | null>(null);
  const request = useRef<AbortController | null>(null);
  const pending = useRef(false);
  const revision = useRef(0);
  const operation = useRef("");
  const id = useId();

  function close(restoreFocus = true) {
    revision.current++;
    request.current?.abort();
    request.current = null;
    pending.current = false;
    form.current?.reset();
    dialog.current?.close();
    setMode(null);
    setProblem(null);
    setBusy(false);
    if (restoreFocus) trigger.current?.focus({ preventScroll: true });
  }
  useLayoutEffect(() => {
    if (!active) close(false);
    return () => {
      revision.current++;
      request.current?.abort();
      form.current?.reset();
    };
  }, [active]);
  useLayoutEffect(() => {
    if (!mode || !active) return;
    setProblem(null);
    form.current?.reset();
    if (!dialog.current?.open) dialog.current?.showModal();
    const field = form.current?.elements.namedItem("currentPassword");
    if (field instanceof HTMLInputElement) field.focus({ preventScroll: true });
  }, [mode, active]);

  useLayoutEffect(() => {
    if (busy || !problem?.field || !dialog.current?.open) return;
    const input = form.current?.elements.namedItem(problem.field);
    if (input instanceof HTMLInputElement) input.focus();
  }, [busy, problem]);

  function open(next: "export" | "delete", source: HTMLElement) {
    trigger.current = source;
    operation.current = Array.from(
      crypto.getRandomValues(new Uint8Array(16)),
      (b) => b.toString(16).padStart(2, "0"),
    ).join("");
    setNotice("");
    setMode(next);
  }
  function showProblem(text: string, field?: string) {
    setProblem({ text, field });
    const input = field && form.current?.elements.namedItem(field);
    if (input instanceof HTMLInputElement) input.focus();
  }
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!active || pending.current || !mode) return;
    const data = new FormData(event.currentTarget);
    const currentPassword = String(data.get("currentPassword") ?? "");
    const confirmation = String(data.get("confirmation") ?? "");
    if (!currentPassword) {
      showProblem("Enter your current password.", "currentPassword");
      return;
    }
    if (mode === "delete" && confirmation !== "DELETE") {
      showProblem("Type DELETE exactly to confirm.", "confirmation");
      return;
    }
    const before = revision.current;
    const epoch = sessionEpoch();
    const controller = new AbortController();
    request.current = controller;
    pending.current = true;
    setBusy(true);
    setProblem(null);
    try {
      if (mode === "delete") {
        await deleteOwnAccount(
          { currentPassword, confirmation: "DELETE" },
          operation.current,
          controller.signal,
        );
        close(false);
        onDeleted();
      } else {
        const result = await requestAccountExport(
          currentPassword,
          controller.signal,
        );
        if (
          before !== revision.current ||
          epoch !== sessionEpoch() ||
          controller.signal.aborted
        )
          return;
        const saved = await saveAccountDownload(result);
        if (
          before !== revision.current ||
          epoch !== sessionEpoch() ||
          controller.signal.aborted
        )
          return;
        close();
        setNotice(
          saved
            ? "Your download is ready. Keep the ZIP private."
            : "Download cancelled.",
        );
      }
    } catch (error) {
      if (
        before !== revision.current ||
        controller.signal.aborted ||
        isStale(error)
      )
        return;
      const code = error instanceof APIError ? error.code : "";
      const text =
        code === "network_uncertain"
          ? mode === "delete"
            ? "The connection was interrupted. Your account may already be deleted. Check whether you can still sign in before starting another request."
            : "The download could not be completed. Try again."
          : message(error);
      showProblem(
        text,
        code === "current_password_incorrect" ? "currentPassword" : undefined,
      );
    } finally {
      if (before === revision.current) {
        pending.current = false;
        request.current = null;
        setBusy(false);
      }
    }
  }
  const described = (name: string) =>
    problem?.field === name
      ? { "aria-invalid": true as const, "aria-describedby": id + "-error" }
      : {};

  return (
    <section className="account-lifecycle" aria-label="Your account data">
      <h3>Your account data</h3>
      <p>Download a copy of your data or permanently delete your account.</p>
      {notice && (
        <p className="success" role="status">
          {notice}
        </p>
      )}
      <div className="account-data-actions">
        <button
          className="button secondary"
          onClick={(e) => open("export", e.currentTarget)}
        >
          Download my data
        </button>
        <button
          className="button account-delete"
          onClick={(e) => open("delete", e.currentTarget)}
        >
          Delete my account
        </button>
      </div>
      <dialog
        ref={dialog}
        className="account-data-dialog"
        aria-labelledby={id + "-title"}
        aria-describedby={id + "-description"}
        onCancel={(e) => {
          e.preventDefault();
          if (!(busy && mode === "delete")) close();
        }}
      >
        <h2 id={id + "-title"}>
          {mode === "delete" ? "Delete my account" : "Download my data"}
        </h2>
        <div id={id + "-description"}>
          {mode === "delete" ? (
            <>
              <p>
                Permanently removes your account, preferences, meals and
                recipes, shopping lists, favorites, feedback and private
                ratings. This cannot be undone.
              </p>
              <button
                className="button secondary"
                type="button"
                disabled={busy}
                onClick={() => {
                  revision.current++;
                  setMode("export");
                }}
              >
                Download my data first
              </button>
              <details className="account-data-explanation">
                <summary>About copies and cleanup</summary>
                <p>
                  Provider images and private traces are tracked for cleanup,
                  which may finish later. Unfinished targets stay protected
                  until resolved; completed targets are removed.
                </p>
                <p>
                  An opaque deletion marker prevents an older database restore
                  from bringing your account back. It stays while old backups
                  could exist.
                </p>
                <p>
                  Already delivered email, downloaded copies and data on offline
                  devices cannot be recalled. Offline devices lose server access
                  when they reconnect.
                </p>
              </details>
            </>
          ) : (
            <>
              <p>
                A ZIP with JSON files and a readable guide: account details,
                preferences, meal-planning history, menus and recipes, shopping
                lists, favorites, feedback and private ratings.
              </p>
              <p>
                Includes your owned history. Passwords and security secrets are
                excluded. MyChef does not keep a copy or send it by email.
              </p>
            </>
          )}
        </div>
        {mode && (
          <form
            key={mode}
            ref={form}
            onSubmit={submit}
            noValidate
            aria-busy={busy}
          >
            <Field label="Current password">
              <input
                type="password"
                name="currentPassword"
                autoComplete="current-password"
                required
                disabled={busy}
                {...described("currentPassword")}
              />
            </Field>
            {mode === "delete" && (
              <Field label="Type DELETE to confirm">
                <input
                  name="confirmation"
                  autoComplete="off"
                  autoCapitalize="characters"
                  spellCheck={false}
                  required
                  disabled={busy}
                  {...described("confirmation")}
                />
              </Field>
            )}
            {problem && (
              <p className="alert" role="alert" id={id + "-error"}>
                {problem.text}
              </p>
            )}
            {busy && (
              <p role="status">
                {mode === "delete"
                  ? "Deleting your account…"
                  : "Preparing your download…"}
              </p>
            )}
            <div className="account-data-actions">
              <button
                className="button secondary"
                type="button"
                disabled={busy && mode === "delete"}
                onClick={() => close()}
              >
                Cancel
              </button>
              <button
                className={
                  "button " + (mode === "delete" ? "account-delete" : "primary")
                }
                type="submit"
                disabled={busy}
              >
                {mode === "delete"
                  ? "Permanently delete account"
                  : "Download ZIP"}
              </button>
            </div>
          </form>
        )}
      </dialog>
    </section>
  );
}
