import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type FormEvent,
} from "react";
import {
  APIError,
  importApi,
  isStale,
  message,
  sessionEpoch,
  type RecipeImport as ImportOperation,
} from "./api";
import type { ImportSeed } from "./ImportReview";
import { Alert, Field } from "./components";
import { focusContentTop } from "./scroll";
import { PhotoInput } from "./PhotoInput";
const requestKey = () => crypto.randomUUID().replaceAll("-", "");
type Availability = Awaited<ReturnType<typeof importApi.availability>>;
export function RecipeImport({
  onDraft,
  onCancel,
  onManual,
  onDirty,
}: {
  onDraft: (seed: ImportSeed) => void;
  onCancel: () => void;
  onManual: () => void;
  onDirty: (dirty: boolean) => void;
}) {
  const [source, setSource] = useState<"url" | "photos">("url"),
    [url, setURL] = useState(""),
    [pages, setPages] = useState<File[]>([]);
  const [permitted, setPermitted] = useState(false),
    [aiConsent, setAIConsent] = useState(false),
    [availability, setAvailability] = useState<Availability | null>(null);
  const [op, setOp] = useState<ImportOperation | null>(null),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const panel = useRef<HTMLElement>(null),
    heading = useRef<HTMLHeadingElement>(null),
    key = useRef(requestKey()),
    current = useRef<ImportOperation | null>(null),
    controller = useRef<AbortController | null>(null),
    alive = useRef(true),
    handedOff = useRef(false),
    sending = useRef(false);
  const active = op?.stage === "checking" || op?.stage === "reading";
  current.current = op;
  useLayoutEffect(() => focusContentTop(panel.current, heading.current), []);
  useEffect(() => {
    alive.current = true;
    const request = new AbortController(),
      epoch = sessionEpoch();
    importApi
      .availability(request.signal)
      .then((value) => {
        if (alive.current && epoch === sessionEpoch()) setAvailability(value);
      })
      .catch((err) => {
        if (alive.current && !isStale(err))
          setAvailability({
            available: false,
            aiAvailable: false,
            fixture: false,
          });
      });
    return () => {
      alive.current = false;
      request.abort();
      controller.current?.abort();
      if (!handedOff.current && current.current)
        void importApi.cancel(current.current.id).catch(() => {});
      onDirty(false);
    };
  }, []);
  useEffect(() => {
    onDirty(!!url || pages.length > 0 || !!op);
  }, [url, pages, op]);
  async function run(work: (signal: AbortSignal) => Promise<ImportOperation>) {
    if (sending.current) return;
    sending.current = true;
    setBusy(true);
    setError("");
    const request = new AbortController(),
      epoch = sessionEpoch();
    controller.current = request;
    try {
      const value = await work(request.signal);
      if (alive.current && epoch === sessionEpoch()) setOp(value);
    } catch (err) {
      if (alive.current && !isStale(err))
        setError(
          err instanceof APIError && err.code === "network_uncertain"
            ? "The connection was interrupted. Check this attempt before starting another import."
            : message(err),
        );
    } finally {
      sending.current = false;
      if (alive.current) setBusy(false);
    }
  }
  useEffect(() => {
    if (!op || !active) return;
    const request = new AbortController(),
      epoch = sessionEpoch();
    const timer = setTimeout(() => {
      importApi
        .read(op.id, request.signal)
        .then((value) => {
          if (alive.current && epoch === sessionEpoch()) setOp(value);
        })
        .catch((err) => {
          if (alive.current && !request.signal.aborted && !isStale(err))
            setError(
              "We could not check this import. Check its status before starting another attempt.",
            );
        });
    }, 600);
    return () => {
      clearTimeout(timer);
      request.abort();
    };
  }, [op]);
  function handoff(value: ImportOperation) {
    if (!value.draft || handedOff.current) return;
    handedOff.current = true;
    onDraft({ operationId: value.id, draft: value.draft });
  }
  useEffect(() => {
    if (op?.stage === "ready" && op.draft && !op.canReadWithAI) handoff(op);
  }, [op]);
  async function leave(manual = false) {
    handedOff.current = true;
    controller.current?.abort();
    if (current.current)
      try {
        await importApi.cancel(current.current.id);
      } catch {
        /* Expiry also removes abandoned content. */
      }
    onDirty(false);
    if (manual) onManual();
    else onCancel();
  }
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!permitted || busy || !availability?.available) return;
    if (source === "url")
      void run((signal) =>
        importApi.url(
          { requestKey: key.current, url, personalUseConfirmed: true },
          signal,
        ),
      );
    else if (aiConsent && pages.length && availability.photosAvailable)
      void run((signal) =>
        importApi.photos(
          {
            requestKey: key.current,
            personalUseConfirmed: true,
            aiConsentConfirmed: true,
          },
          pages,
          signal,
        ),
      );
  }
  const canAI =
    availability?.aiAvailable &&
    (op?.canReadWithAI || op?.stage === "awaiting-ai");
  const terminal = op && ["failed", "cancelled", "expired"].includes(op.stage);
  return (
    <section ref={panel} className="personal-library recipe-import">
      <button
        type="button"
        className="text-button back"
        onClick={() => void leave()}
      >
        ← Back to my recipes
      </button>
      <div className="page-heading">
        <span className="eyebrow">Your own collection</span>
        <h2 ref={heading} tabIndex={-1} className="personal-heading">
          Import recipe
        </h2>
        <p>
          Bring a written recipe into your private library. Review it before you
          save.
        </p>
      </div>
      {availability?.fixture && (
        <p className="notice">
          Private test preview: imports return a sample recipe. No pages are
          sent to Gemini.
        </p>
      )}
      {error && <Alert>{error}</Alert>}
      {!op && (
        <form className="panel import-form" onSubmit={submit}>
          <div
            className="button-row import-source-choice"
            aria-label="Recipe source"
          >
            <button
              type="button"
              className="button secondary"
              aria-pressed={source === "url"}
              disabled={busy}
              onClick={() => {
                setSource("url");
                key.current = requestKey();
                setAIConsent(false);
              }}
            >
              Website link
            </button>
            <button
              type="button"
              className="button secondary"
              aria-pressed={source === "photos"}
              disabled={busy}
              onClick={() => {
                setSource("photos");
                setAIConsent(false);
                key.current = requestKey();
              }}
            >
              Recipe pages
            </button>
          </div>
          <fieldset disabled={busy}>
            {source === "url" ? (
              <Field
                label="Recipe URL"
                hint="One public HTTPS recipe page. No sign-in or paywall."
              >
                <input
                  type="url"
                  inputMode="url"
                  autoComplete="off"
                  autoCapitalize="none"
                  spellCheck={false}
                  required
                  maxLength={4096}
                  pattern="https://.*"
                  value={url}
                  onChange={(event) => {
                    setURL(event.target.value);
                    key.current = requestKey();
                  }}
                />
              </Field>
            ) : (
              <Field
                label="Recipe-page photos"
                hint="Up to three JPEG, PNG or HEIC photos of one written recipe; 10 MB each and 12 MB combined. Finished-dish photos cannot be transcribed."
              >
                <PhotoInput
                  purpose="recipe-pages"
                  onError={setError}
                  disabled={availability?.photosAvailable !== true}
                  onFiles={(next) => {
                    if (
                      next.length > 3 ||
                      next.some((file) => file.size > 10 * 1024 * 1024) ||
                      next.reduce((sum, file) => sum + file.size, 0) >
                        12 * 1024 * 1024
                    ) {
                      setError(
                        "Choose up to three recipe pages, 10 MB each and 12 MB combined.",
                      );
                      setPages([]);
                      return false;
                    }
                    setError("");
                    setPages(next);
                    setAIConsent(false);
                    key.current = requestKey();
                  }}
                />
              </Field>
            )}
            <label className="import-check">
              <input
                type="checkbox"
                required
                checked={permitted}
                onChange={(event) => setPermitted(event.target.checked)}
              />
              <span>
                I have permission to import this recipe for my personal use.
              </span>
            </label>
            {source === "photos" && (
              <label className="import-check">
                <input
                  type="checkbox"
                  required
                  checked={aiConsent}
                  onChange={(event) => setAIConsent(event.target.checked)}
                />
                <span>
                  I agree to send the recipe text or pages to Gemini for
                  reading.
                </span>
              </label>
            )}
          </fieldset>
          {availability && !availability.available && (
            <p className="notice">
              Import is unavailable right now. You can enter the recipe
              manually.
            </p>
          )}
          {source === "photos" &&
            availability?.available &&
            !availability.photosAvailable && (
              <p className="notice">
                Reading recipe photos is unavailable right now. You can enter
                the recipe manually.
              </p>
            )}
          <button
            className="button primary"
            disabled={
              busy ||
              !availability?.available ||
              (source === "photos" &&
                (!availability.photosAvailable || pages.length === 0))
            }
          >
            {busy
              ? "Preparing your import…"
              : source === "url"
                ? "Check page"
                : "Read recipe pages"}
          </button>
        </form>
      )}
      {op && (
        <div className="panel import-progress" aria-live="polite">
          {active ? (
            <>
              <h3>
                {op.stage === "checking" ? "Checking page" : "Reading recipe"}
              </h3>
              <p>Your recipe is being prepared. You can cancel this attempt.</p>
            </>
          ) : op.stage === "selection" ? (
            <>
              <h3>Choose one recipe</h3>
              <p>This page contains more than one recipe.</p>
              <div className="import-candidates">
                {op.candidates.map((candidate) => (
                  <button
                    type="button"
                    className="button secondary"
                    key={candidate.id}
                    disabled={busy}
                    onClick={() =>
                      void run((signal) =>
                        importApi.select(op.id, candidate.id, signal),
                      )
                    }
                  >
                    {candidate.draft.content.title || "Untitled recipe"}
                  </button>
                ))}
              </div>
            </>
          ) : terminal ? (
            <>
              <h3>This recipe could not be imported</h3>
              <p>
                You can enter it manually or start a new attempt. We will not
                automatically repeat the request.
              </p>
              <button
                type="button"
                className="button secondary"
                disabled={busy}
                onClick={() => {
                  setOp(null);
                  setAIConsent(false);
                  setError("");
                  key.current = requestKey();
                }}
              >
                Try a new import
              </button>
            </>
          ) : (
            <>
              <h3>
                {op.draft
                  ? "Check the extracted details"
                  : "Read this recipe with AI"}
              </h3>
              <p>
                {op.draft
                  ? "Review the extracted draft, or ask Gemini to read the missing details."
                  : "This page needs text extraction. You can ask Gemini to read it or enter the recipe yourself."}
              </p>
              {op.draft && (
                <button
                  type="button"
                  className="button secondary"
                  disabled={busy}
                  onClick={() => handoff(op)}
                >
                  Review draft
                </button>
              )}
              {canAI ? (
                <>
                  <label className="import-check">
                    <input
                      type="checkbox"
                      checked={aiConsent}
                      onChange={(event) => setAIConsent(event.target.checked)}
                    />
                    <span>
                      I agree to send the recipe text or pages to Gemini for
                      reading.
                    </span>
                  </label>
                  <button
                    type="button"
                    className="button primary"
                    disabled={busy || !aiConsent}
                    onClick={() =>
                      void run((signal) => importApi.ai(op.id, signal))
                    }
                  >
                    Read with AI
                  </button>
                </>
              ) : (
                <p className="notice">
                  AI reading is unavailable. You can enter the recipe manually.
                </p>
              )}
            </>
          )}
          {error && (
            <button
              type="button"
              className="button secondary"
              disabled={busy}
              onClick={() =>
                void run((signal) => importApi.read(op.id, signal))
              }
            >
              Check status
            </button>
          )}
        </div>
      )}
      <p className="small muted">
        When you choose AI reading, cleaned recipe text or pages go to Gemini.
        Extraction can make mistakes. MyChef deletes local source photos after
        reading or within one hour; the provider has its own{" "}
        <a
          href="https://ai.google.dev/gemini-api/terms"
          target="_blank"
          rel="noopener noreferrer"
          referrerPolicy="no-referrer"
        >
          processing terms
        </a>
        . Page photos are never used as dish pictures.
      </p>
      <div className="button-row">
        <button
          type="button"
          className="button secondary"
          onClick={() => void leave(true)}
        >
          Enter manually
        </button>
        <button
          type="button"
          className="text-button"
          onClick={() => void leave()}
        >
          Cancel import
        </button>
      </div>
      <p className="small muted">
        For questions or a source removal request, use Feedback in Profile.
      </p>
    </section>
  );
}
