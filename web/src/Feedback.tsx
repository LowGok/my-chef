import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type FormEvent,
} from "react";
import {
  api,
  APIError,
  sessionEpoch,
  readFeedbackDraft,
  writeFeedbackDraft,
  readFeedbackPointer,
  writeFeedbackPointer,
  type FeedbackInput,
  type FeedbackAvailability,
  type FeedbackRating,
  type FeedbackPointer,
  type FeedbackReceipt,
  type PendingFeedback,
} from "./api";
import { Alert, Field } from "./components";

const topics: Record<FeedbackInput["topic"], string> = {
  technical_issue: "Technical issue",
  recipe_issue: "Recipe issue",
  subscription: "My subscription",
  other: "Other",
};
const recipeTopic = (topic: string) => topic === "recipe_issue";
const makeKey = () =>
  Array.from(crypto.getRandomValues(new Uint8Array(16)), (byte) =>
    byte.toString(16).padStart(2, "0"),
  ).join("");
const ratingPath = (id: string) => `/recipes/${encodeURIComponent(id)}/rating`;

type View = {
  owner: string;
  generation: number;
  viewGeneration: () => number;
};

function useActive({ generation, viewGeneration }: View) {
  const mounted = useRef(false);
  const epoch = useRef(sessionEpoch());
  useLayoutEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  return () =>
    mounted.current &&
    epoch.current === sessionEpoch() &&
    generation === viewGeneration();
}

function useSubmission(
  view: View,
  operation: FeedbackPointer["operation"],
  onReceipt?: () => void,
) {
  const { owner } = view;
  const active = useActive(view);
  const [pointer, setPointer] = useState(() => readFeedbackPointer(owner));
  const [draft, setDraft] = useState(() => readFeedbackDraft(owner, pointer));
  const [receipt, setReceipt] = useState<FeedbackReceipt | null>(null);
  const [pending, setPending] = useState(!!pointer);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState("");
  const [warning, setWarning] = useState(false);
  const lock = useRef(false);
  function savePointer(next: FeedbackPointer | null) {
    writeFeedbackPointer(owner, next);
    setPointer(next);
  }
  function saveDraft(next: PendingFeedback | null) {
    writeFeedbackDraft(owner, next);
    setDraft(next);
  }
  function reset() {
    if (!active() || lock.current) return;
    savePointer(null);
    saveDraft(null);
    setPending(false);
    setReceipt(null);
    setProblem("");
    setWarning(false);
  }
  function accept(result: FeedbackReceipt, current: FeedbackPointer) {
    if (
      !/^[0-9a-f]{32}$/.test(result.reference) ||
      !Number.isFinite(Date.parse(result.createdAt))
    )
      throw new Error("Invalid receipt");
    saveDraft(null);
    setPending(false);
    setWarning(false);
    if (current.operation === operation) setReceipt(result);
    onReceipt?.();
  }
  async function check(current = pointer) {
    if (!current || !active() || lock.current) return;
    lock.current = true;
    setBusy(true);
    setProblem("");
    try {
      const result = await api<FeedbackReceipt>("/feedback/receipt", {
        method: "POST",
        body: JSON.stringify(current),
      });
      if (active()) accept(result, current);
    } catch (err) {
      if (active())
        setProblem(
          err instanceof APIError && err.status === 404
            ? "No receipt yet. The submission may still be processing."
            : "Could not check the receipt. Please try again.",
        );
    } finally {
      lock.current = false;
      if (active()) setBusy(false);
    }
  }
  useEffect(() => {
    // Recover only the latest owner-scoped receipt. Never send the draft here.
    if (pointer) void check(pointer);
  }, []);
  async function send(payload: PendingFeedback) {
    if (!active() || lock.current) return;
    lock.current = true;
    setBusy(true);
    setProblem("");
    setReceipt(null);
    const current = {
      operation: payload.operation,
      requestKey: payload.input.requestKey,
    };
    savePointer(current);
    saveDraft(payload);
    setPending(true);
    setWarning(false);
    try {
      const isReport = payload.operation === "feedback";
      const result = await api<FeedbackReceipt>(
        isReport ? "/feedback" : ratingPath(payload.recipeId),
        {
          method: isReport ? "POST" : "PUT",
          body: JSON.stringify(payload.input),
        },
      );
      if (active()) accept(result, current);
    } catch (err) {
      if (!active()) return;
      // A rejection with an app error code is definitive. Unknown HTTP or
      // connection failures may have hidden a committed write.
      if (
        err instanceof APIError &&
        err.requestId &&
        [
          "invalid_input",
          "not_found",
          "csrf_invalid",
          "email_verification_required",
          "rating_unchanged",
          "rate_limited",
          "feedback_unavailable",
          "storage_unavailable",
        ].includes(err.code)
      ) {
        savePointer(null);
        saveDraft(null);
        setPending(false);
        setProblem(err.message);
        if (err.code === "rating_unchanged") onReceipt?.();
      } else {
        setProblem(
          "We couldn’t confirm your submission. Check for a receipt before sending it again.",
        );
      }
    } finally {
      lock.current = false;
      if (active()) setBusy(false);
    }
  }
  return {
    receipt,
    draft,
    pending,
    busy,
    problem,
    warning,
    setWarning,
    check,
    send,
    reset,
  };
}

type Submission = ReturnType<typeof useSubmission>;

function PendingActions({
  state,
  canRetry,
}: {
  state: Submission;
  canRetry: boolean;
}) {
  if (!state.pending) return null;
  const draft = state.draft;
  return (
    <div className="feedback-pending">
      <p role="status">
        Check for a receipt before starting another submission.
      </p>
      <div className="feedback-actions">
        <button
          className="button secondary"
          disabled={state.busy}
          onClick={() => void state.check()}
        >
          Check receipt
        </button>
        {canRetry && draft && (
          <button
            className="button secondary"
            disabled={state.busy}
            onClick={() => void state.send(draft)}
          >
            Retry exact submission
          </button>
        )}
        {!state.warning && (
          <button
            className="text-button"
            disabled={state.busy}
            onClick={() => state.setWarning(true)}
          >
            Start a separate submission
          </button>
        )}
      </div>
      {state.warning && (
        <div className="notice">
          <p>
            The earlier submission may still be saved. A new submission could
            create a duplicate.
          </p>
          <button className="button secondary" onClick={state.reset}>
            Start anyway
          </button>{" "}
          <button
            className="text-button"
            onClick={() => state.setWarning(false)}
          >
            Keep checking
          </button>
        </div>
      )}
    </div>
  );
}

function Receipt({ receipt }: { receipt: FeedbackReceipt }) {
  return (
    <div className="feedback-receipt" data-testid="feedback-receipt">
      <p>
        Reference: <strong>{receipt.reference}</strong>
      </p>
      <p>
        Submitted{" "}
        <time dateTime={receipt.createdAt}>
          {new Date(receipt.createdAt).toLocaleString()}
        </time>
      </p>
    </div>
  );
}

export function Feedback({
  owner,
  generation,
  viewGeneration,
  recipeId,
  onBack,
}: View & { recipeId?: string; onBack: () => void }) {
  const view = { owner, generation, viewGeneration };
  const active = useActive(view);
  const state = useSubmission(view, "feedback");
  const prior =
    state.draft?.operation === "feedback" ? state.draft.input : null;
  const [topic, setTopic] = useState<FeedbackInput["topic"] | "">(
    prior?.topic ?? (recipeId ? "recipe_issue" : ""),
  );
  const [text, setText] = useState(prior?.message ?? "");
  const [attach, setAttach] = useState(
    prior ? prior.recipeId === recipeId : !!recipeId,
  );
  const [invalid, setInvalid] = useState<"topic" | "message" | "">("");
  const [availability, setAvailability] = useState<
    "loading" | "enabled" | "disabled" | "error"
  >("loading");
  const [availabilityAttempt, retryAvailability] = useState(0);
  const topicRef = useRef<HTMLSelectElement>(null);
  const textRef = useRef<HTMLTextAreaElement>(null);
  const receiptHeading = useRef<HTMLHeadingElement>(null);
  const length = Array.from(text).length;
  const disabled = state.pending || state.busy || availability !== "enabled";
  useEffect(() => {
    const controller = new AbortController();
    void api<FeedbackAvailability>("/feedback/availability", {
      signal: controller.signal,
    })
      .then(({ enabled }) => {
        if (active()) setAvailability(enabled ? "enabled" : "disabled");
      })
      .catch(() => {
        if (active() && !controller.signal.aborted) setAvailability("error");
      });
    return () => controller.abort();
  }, [availabilityAttempt]);
  useLayoutEffect(() => {
    if (state.receipt) receiptHeading.current?.focus();
  }, [state.receipt]);

  function submit(event: FormEvent) {
    event.preventDefault();
    if (disabled) return;
    if (!topic) {
      setInvalid("topic");
      topicRef.current?.focus();
      return;
    }
    if (length < 20 || length > 4000 || !text.trim()) {
      setInvalid("message");
      textRef.current?.focus();
      return;
    }
    setInvalid("");
    void state.send({
      operation: "feedback",
      input: {
        topic,
        message: text,
        requestKey: makeKey(),
        ...(recipeTopic(topic) && recipeId && attach ? { recipeId } : {}),
      },
    });
  }
  function another() {
    state.reset();
    setText("");
    setInvalid("");
  }
  return (
    <>
      <div className="page-heading">
        <h2>Your feedback.</h2>
      </div>
      <section
        className="panel profile-panel feedback-panel"
        aria-label="Feedback"
      >
        {state.receipt ? (
          <>
            <h3 ref={receiptHeading} tabIndex={-1}>
              Thanks for your feedback.
            </h3>
            <Receipt receipt={state.receipt} />
            <p>
              Save this reference before closing the tab, signing out, or
              starting another report.
            </p>
            <div className="feedback-actions">
              <button className="button secondary" onClick={onBack}>
                {recipeId ? "Back to recipe" : "Back to profile"}
              </button>
              <button className="button primary" onClick={another}>
                Send another report
              </button>
            </div>
          </>
        ) : (
          <>
            <p>Tell us what we can improve or what went wrong.</p>
            {availability === "disabled" && (
              <p role="status">
                Feedback is currently unavailable. For help, return to Profile
                and open Help & support.
              </p>
            )}
            {availability === "error" && (
              <p>
                Couldn’t check feedback availability.{" "}
                <button
                  className="text-button"
                  onClick={() => retryAvailability((n) => n + 1)}
                >
                  Try again
                </button>
              </p>
            )}
            <form onSubmit={submit} noValidate>
              <Field label="Contact reason">
                <select
                  ref={topicRef}
                  value={topic}
                  disabled={disabled}
                  aria-invalid={invalid === "topic" || undefined}
                  aria-describedby={
                    invalid === "topic" ? "feedback-input-error" : undefined
                  }
                  onChange={(event) => {
                    setTopic(event.target.value as typeof topic);
                    setInvalid("");
                  }}
                >
                  <option value="">Choose a reason</option>
                  {Object.entries(topics).map(([value, label]) => (
                    <option key={value} value={value}>
                      {label}
                    </option>
                  ))}
                </select>
              </Field>
              {recipeId && recipeTopic(topic) && (
                <label className="feedback-attachment">
                  <input
                    type="checkbox"
                    checked={attach}
                    disabled={disabled}
                    onChange={(e) => setAttach(e.target.checked)}
                  />
                  Include this recipe’s reference
                </label>
              )}
              <Field label="Message">
                <textarea
                  ref={textRef}
                  value={text}
                  rows={5}
                  disabled={disabled}
                  aria-invalid={invalid === "message" || undefined}
                  aria-describedby={`feedback-counter${invalid === "message" ? " feedback-input-error" : ""}`}
                  onChange={(event) => {
                    setText(event.target.value);
                    setInvalid("");
                  }}
                />
              </Field>
              <small id="feedback-counter">
                {length > 4000
                  ? `${length - 4000} ${length === 4001 ? "character" : "characters"} over the limit`
                  : `${4000 - length} characters remaining`}
              </small>
              {invalid && (
                <div id="feedback-input-error">
                  <Alert>
                    {invalid === "topic"
                      ? "Choose a reason."
                      : "Enter a message of 20 to 4,000 characters."}
                  </Alert>
                </div>
              )}
              <p>
                Our team may use your verified account email to ask for details
                or answer your question.
              </p>
              <div className="feedback-actions">
                <button className="button primary" disabled={disabled}>
                  Send feedback
                </button>
                <button
                  className="button secondary"
                  type="button"
                  onClick={onBack}
                >
                  {recipeId ? "Back to recipe" : "Back to profile"}
                </button>
              </div>
            </form>
            {state.problem && <Alert>{state.problem}</Alert>}
            <PendingActions
              state={state}
              canRetry={state.draft?.operation === "feedback"}
            />
          </>
        )}
      </section>
    </>
  );
}

export function RecipeRating({
  recipeId,
  ...view
}: View & { recipeId: string }) {
  const active = useActive(view);
  const [rating, setRating] = useState<number | null>();
  const [chosen, choose] = useState<number | null>(null);
  const [readProblem, setReadProblem] = useState(false);
  const [revision, refresh] = useState(0);
  const state = useSubmission(view, "rating", () => refresh((n) => n + 1));
  useEffect(() => {
    const controller = new AbortController();
    setReadProblem(false);
    void api<FeedbackRating>(ratingPath(recipeId), {
      signal: controller.signal,
    })
      .then((result) => {
        if (active()) {
          setRating(result.rating);
          choose(result.rating);
        }
      })
      .catch(() => {
        if (active() && !controller.signal.aborted) setReadProblem(true);
      });
    return () => controller.abort();
  }, [recipeId, revision]);
  const disabled =
    rating === undefined || readProblem || state.pending || state.busy;
  return (
    <section
      className="panel profile-panel feedback-panel recipe-rating"
      aria-label="Private recipe rating"
    >
      <h3>Your rating</h3>
      <p>Only you can see this rating.</p>
      {readProblem && (
        <p>
          Couldn’t load your rating.{" "}
          <button className="text-button" onClick={() => refresh((n) => n + 1)}>
            Try again
          </button>
        </p>
      )}
      <form
        onSubmit={(event) => {
          event.preventDefault();
          if (!disabled && chosen && chosen !== rating)
            void state.send({
              operation: "rating",
              recipeId,
              input: { requestKey: makeKey(), rating: chosen },
            });
        }}
      >
        <Field label="Rating">
          <select
            value={chosen ?? ""}
            disabled={disabled}
            onChange={(event) => choose(Number(event.target.value) || null)}
          >
            <option value="">Choose a rating</option>
            {[1, 2, 3, 4, 5].map((value) => (
              <option key={value} value={value}>
                {value} out of 5
              </option>
            ))}
          </select>
        </Field>
        <button
          className="button secondary"
          disabled={disabled || !chosen || chosen === rating}
        >
          Save rating
        </button>
      </form>
      {state.problem && <Alert>{state.problem}</Alert>}
      <PendingActions
        state={state}
        canRetry={
          state.draft?.operation === "rating" &&
          state.draft.recipeId === recipeId
        }
      />
      {state.receipt && (
        <div role="status">
          <p>Rating saved. Save this reference if you need it later.</p>
          <Receipt receipt={state.receipt} />
        </div>
      )}
    </section>
  );
}
