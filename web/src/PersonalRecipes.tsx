import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type FormEvent,
} from "react";
import {
  APIError,
  api,
  isStale,
  message,
  personalApi,
  importApi,
  personalAccountReady,
  sessionEpoch,
  type PersonalContent,
  type PersonalRecipe,
  type PersonalSaveInput,
  type Recipe,
  type RecipePage,
} from "./api";
import { Alert, Field, FoodPlaceholder } from "./components";
import { addDays, monday, today } from "./dates";
import { focusContentTop } from "./scroll";
import { DurationPicker } from "./DurationPicker";
import { RecipeImport } from "./RecipeImport";
import {
  ImportReview,
  ImportSourceLink,
  importReviewKeys,
  type ImportSeed,
} from "./ImportReview";

const requestKey = () => crypto.randomUUID().replaceAll("-", "");
export const recipeImage = (url: string | null | undefined) =>
  !!url &&
  (url.startsWith("https://") ||
    /^\/api\/v1\/personal-photos\/[0-9a-f]{32}$/.test(url));

function Dish({ url }: { url: string | null }) {
  const [failed, setFailed] = useState(false);
  useEffect(() => setFailed(false), [url]);
  return !failed && recipeImage(url) ? (
    <img src={url!} alt="" loading="lazy" onError={() => setFailed(true)} />
  ) : (
    <FoodPlaceholder />
  );
}
export function MyRecipesSection({ onOpen }: { onOpen: () => void }) {
  return (
    <details className="account-details personal-disclosure">
      <summary>My recipes</summary>
      <p className="muted">Your own recipes, kept together.</p>
      <button type="button" className="button secondary" onClick={onOpen}>
        Open my recipes
      </button>
    </details>
  );
}

export function PersonalCopyAction({ onCopy }: { onCopy: () => void }) {
  const [allowed, setAllowed] = useState<boolean | null>(null);
  const ready = personalAccountReady();
  useEffect(() => {
    if (!ready) return;
    const request = new AbortController();
    let active = true;
    personalApi
      .permissions(request.signal)
      .then((value) => {
        if (active) setAllowed(value.canWrite);
      })
      .catch(() => {
        if (active) setAllowed(false);
      });
    return () => {
      active = false;
      request.abort();
    };
  }, []);
  if (!ready) return null;
  return (
    <div className="personal-copy-action">
      <button
        type="button"
        className="text-button"
        disabled={allowed !== true}
        onClick={onCopy}
      >
        Save as my recipe
      </button>
      {allowed === false && (
        <p className="small muted">
          Premium is needed to save a personal copy.
        </p>
      )}
    </div>
  );
}
type DraftSeed = {
  importSeed?: ImportSeed;
  content: PersonalContent;
  sourceRecipeId?: string;
  imageUrl: string | null;
  sourceYield: string[];
  needsServings: boolean;
};
const blank = (): PersonalContent => ({
  schemaVersion: 1,
  title: "",
  description: "",
  servings: 2,
  equipment: "",
  prepMinutes: 0,
  cookMinutes: 0,
  totalMinutes: 1,
  ingredients: [{ name: "", quantity: null, unit: null }],
  instructions: [""],
  tips: [],
  allergens: [],
  allergenNote: "",
});

export function PersonalRecipes({
  copySource,
  onBack,
  onDirty,
  onPlanned,
  onCopyResolved,
}: {
  copySource?: string;
  onBack: () => void;
  onDirty: (dirty: boolean) => void;
  onPlanned: () => void;
  onCopyResolved: () => void;
}) {
  const [importOpen, setImportOpen] = useState(false);
  const [permission, setPermission] = useState<{
    canWrite: boolean;
    photoUploadsEnabled: boolean;
  } | null>(null);
  const [rows, setRows] = useState<PersonalRecipe[]>([]),
    [cursor, setCursor] = useState<string | null>(null),
    [query, setQuery] = useState("");
  const [selected, setSelected] = useState<PersonalRecipe | null>(null),
    [seed, setSeed] = useState<DraftSeed | null>(null),
    [existing, setExisting] = useState<string | null>(null);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [notice, setNotice] = useState("");
  const [deleting, setDeleting] = useState(false),
    [swap, setSwap] = useState(false),
    [meals, setMeals] = useState<Recipe[]>([]),
    [target, setTarget] = useState("");
  const heading = useRef<HTMLHeadingElement>(null),
    panel = useRef<HTMLElement>(null),
    dialog = useRef<HTMLDialogElement>(null);
  const generation = useRef(0),
    controller = useRef<AbortController | null>(null),
    alive = useRef(true);
  const deleteKey = useRef(requestKey()),
    assignment = useRef({ key: requestKey(), target: "" });
  async function run<T>(
    work: (signal: AbortSignal) => Promise<T>,
    accept: (value: T) => void,
  ) {
    const current = ++generation.current,
      epoch = sessionEpoch();
    controller.current?.abort();
    const request = new AbortController();
    controller.current = request;
    setBusy(true);
    setError("");
    try {
      const value = await work(request.signal);
      if (
        alive.current &&
        current === generation.current &&
        epoch === sessionEpoch()
      )
        accept(value);
    } catch (err) {
      if (alive.current && current === generation.current && !isStale(err))
        setError(message(err));
    } finally {
      if (alive.current && current === generation.current) setBusy(false);
    }
  }
  function load(next?: string) {
    void run(
      (signal) =>
        personalApi.search({ query, limit: 12, cursor: next ?? "" }, signal),
      (value) => {
        setRows((old) => (next ? [...old, ...value.recipes] : value.recipes));
        setCursor(value.nextCursor);
      },
    );
  }
  function open(id: string) {
    setNotice("");
    setExisting(null);
    void run(
      (signal) => personalApi.read(id, signal),
      (value) => {
        setSelected(value);
        deleteKey.current = requestKey();
      },
    );
  }
  useEffect(() => {
    alive.current = true;
    const permissionRequest = new AbortController(),
      epoch = sessionEpoch();
    personalApi
      .permissions(permissionRequest.signal)
      .then((value) => {
        if (alive.current && epoch === sessionEpoch()) setPermission(value);
      })
      .catch((err) => {
        if (alive.current && !isStale(err)) setError(message(err));
      });
    if (copySource)
      void run(
        (signal) => personalApi.copy(copySource, signal),
        (value) => {
          onCopyResolved();
          if (value.existingId) setExisting(value.existingId);
          else
            setSeed({
              content: value.draft.content,
              sourceRecipeId: copySource,
              imageUrl: value.imageUrl,
              sourceYield: value.draft.sourceYield,
              needsServings: value.draft.needsServings,
            });
        },
      );
    else load();
    return () => {
      alive.current = false;
      generation.current++;
      controller.current?.abort();
      permissionRequest.abort();
      onDirty(false);
    };
  }, []);
  useLayoutEffect(() => {
    if (!seed) return focusContentTop(panel.current, heading.current);
  }, [selected?.versionId, seed, existing]);
  useEffect(() => {
    if (deleting || swap) dialog.current?.showModal();
    else dialog.current?.close();
  }, [deleting, swap]);
  function backToList() {
    setSelected(null);
    setExisting(null);
    setNotice("");
    setError("");
    setSeed(null);
    load();
  }
  function queue() {
    if (!selected) return;
    const date = today(),
      week = addDays(monday(date), 7);
    void run(
      (signal) =>
        personalApi.queue(
          selected.id,
          {
            versionId: selected.versionId,
            requestKey: assignment.current.key,
            today: date,
            week,
          },
          signal,
        ),
      () => {
        setNotice("Added to next week. It will be shuffled in when you plan.");
        assignment.current = { key: requestKey(), target: "" };
        onPlanned();
      },
    );
  }
  function chooseSwap() {
    const week = monday(today());
    setTarget("");
    setMeals([]);
    setSwap(true);
    void run(async (signal) => {
      const result: Recipe[] = [];
      let offset: number | null = 0;
      while (offset !== null) {
        const value: RecipePage = await api(
          `/menus?from=${week}&to=${addDays(week, 7)}&limit=50&offset=${offset}`,
          { signal },
        );
        result.push(...value.recipes);
        offset = value.nextOffset;
      }
      return result;
    }, setMeals);
  }
  if (importOpen)
    return (
      <RecipeImport
        onDirty={onDirty}
        onCancel={() => {
          setImportOpen(false);
          backToList();
        }}
        onManual={() => {
          setImportOpen(false);
          setSeed({
            content: blank(),
            imageUrl: null,
            sourceYield: [],
            needsServings: false,
          });
        }}
        onDraft={(value) => {
          setImportOpen(false);
          setSeed({
            content: {
              ...value.draft.content,
              ingredients: value.draft.content.ingredients ?? [],
              instructions: value.draft.content.instructions ?? [],
              tips: value.draft.content.tips ?? [],
              allergens: value.draft.content.allergens ?? [],
            },
            imageUrl: null,
            sourceYield: value.draft.sourceYield ?? [],
            needsServings: value.draft.content.servings < 1,
            importSeed: value,
          });
        }}
      />
    );
  if (seed)
    return (
      <PersonalEditor
        key={`${selected?.id ?? "new"}/${seed.sourceRecipeId ?? seed.importSeed?.operationId ?? ""}`}
        initial={seed}
        saved={selected}
        photoEnabled={permission?.photoUploadsEnabled === true}
        onDirty={onDirty}
        onDiscard={() => {
          onDirty(false);
          setSeed(null);
          if (!selected) backToList();
        }}
        onSaved={(value) => {
          onDirty(false);
          setSelected(value);
          setSeed(null);
          setNotice("Recipe saved.");
        }}
      />
    );
  return (
    <section ref={panel} className="personal-library">
      <button
        className="text-button back"
        aria-label={
          selected || existing ? "Back to my recipes" : "Back to profile"
        }
        onClick={selected || existing ? backToList : onBack}
      >
        ← {selected || existing ? "Back to my recipes" : "Back to profile"}
      </button>
      <div className="page-heading">
        <span className="eyebrow">Made by you</span>
        <h2 ref={heading} className="personal-heading" tabIndex={-1}>
          {selected ? selected.content.title : "My recipes"}
        </h2>
        <p>Your recipes stay private.</p>
      </div>
      {error && <Alert>{error}</Alert>}
      {notice && (
        <p className="success" role="status">
          {notice}
        </p>
      )}
      {existing ? (
        <div className="panel">
          <h3>Already in My recipes</h3>
          <p>You have saved a copy of this meal.</p>
          <button
            className="button primary"
            disabled={busy}
            onClick={() => open(existing)}
          >
            Open saved recipe
          </button>
        </div>
      ) : selected ? (
        <>
          <PersonalRecap value={selected} />
          {!permission?.canWrite && (
            <p className="notice">
              Premium is needed to edit or add this recipe to a plan. You can
              still read or delete it.
            </p>
          )}
          <div className="button-row personal-actions">
            <button
              className="button secondary"
              disabled={busy || !permission?.canWrite}
              onClick={() => {
                setNotice("");
                setSeed({
                  content: structuredClone(selected.content),
                  imageUrl: selected.imageUrl,
                  sourceYield: selected.sourceYield,
                  needsServings: false,
                });
              }}
            >
              Edit recipe
            </button>
            <button
              className="button primary"
              disabled={busy || !permission?.canWrite}
              onClick={queue}
            >
              Add to next week
            </button>
            <button
              className="button secondary"
              disabled={busy || !permission?.canWrite}
              onClick={chooseSwap}
            >
              Swap this week’s meal
            </button>
            <button
              className="text-button danger"
              disabled={busy}
              onClick={() => setDeleting(true)}
            >
              Delete recipe
            </button>
          </div>
        </>
      ) : (
        <>
          {!permission?.canWrite && permission && (
            <p className="notice">
              Premium is needed to create and edit recipes.
            </p>
          )}
          <form
            className="personal-search"
            onSubmit={(event) => {
              event.preventDefault();
              load();
            }}
          >
            <Field label="Search by title">
              <input
                type="search"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
              />
            </Field>
            <button className="button secondary" disabled={busy}>
              Search
            </button>
          </form>
          <button
            className="button primary"
            disabled={busy || !permission?.canWrite}
            onClick={() => {
              setError("");
              setNotice("");
              setSeed({
                content: blank(),
                imageUrl: null,
                sourceYield: [],
                needsServings: false,
              });
            }}
          >
            Create a recipe
          </button>
          <button
            type="button"
            className="button secondary"
            disabled={busy || !permission?.canWrite}
            onClick={() => {
              setError("");
              setNotice("");
              setImportOpen(true);
            }}
          >
            Import recipe
          </button>
          {!rows.length && !busy && (
            <p className="personal-empty muted">
              Keep the dishes you love to make here.
            </p>
          )}
          <div className="personal-rows">
            {rows.map((value) => (
              <button
                type="button"
                className="personal-row"
                key={value.id}
                onClick={() => open(value.id)}
                disabled={busy}
              >
                <span className="personal-row-image">
                  <Dish url={value.imageUrl} />
                </span>
                <span className="personal-row-text">
                  <strong>{value.content.title}</strong>
                  <span className="muted">
                    {value.content.description || "Your own recipe"}
                  </span>
                </span>
              </button>
            ))}
          </div>
          {cursor && (
            <button
              className="button secondary"
              disabled={busy}
              onClick={() => load(cursor)}
            >
              Load more recipes
            </button>
          )}
        </>
      )}
      {busy && <p role="status">Loading…</p>}
      <dialog
        ref={dialog}
        className="replacement-dialog personal-dialog"
        aria-label={deleting ? "Delete recipe" : "Swap this week’s meal"}
        onCancel={(event) => {
          event.preventDefault();
          if (!busy) {
            setDeleting(false);
            setSwap(false);
          }
        }}
      >
        {deleting ? (
          <>
            <h3>Delete this recipe?</h3>
            <p>
              Its linked favorites and unscheduled entries will also be removed.
              Meals already planned stay as they are.
            </p>
            <div className="button-row">
              <button
                className="button secondary"
                disabled={busy}
                onClick={() => setDeleting(false)}
              >
                Cancel
              </button>
              <button
                className="button danger"
                disabled={busy || !selected}
                onClick={() => {
                  if (selected)
                    void run(
                      (signal) =>
                        personalApi.remove(
                          selected.id,
                          selected.versionId,
                          deleteKey.current,
                          signal,
                        ),
                      () => {
                        setDeleting(false);
                        backToList();
                        setNotice("Recipe deleted.");
                        onPlanned();
                      },
                    );
                }}
              >
                Delete recipe
              </button>
            </div>
          </>
        ) : (
          <>
            <h3>Choose a meal to replace</h3>
            <p>The saved recipe will replace one meal in your current week.</p>
            <Field label="Meal to replace">
              <select
                value={target}
                disabled={busy}
                onChange={(event) => {
                  setTarget(event.target.value);
                  assignment.current = {
                    key: requestKey(),
                    target: event.target.value,
                  };
                }}
              >
                <option value="">Choose a meal</option>
                {meals.map((meal) => (
                  <option key={meal.id} value={meal.id}>
                    {meal.title}
                  </option>
                ))}
              </select>
            </Field>
            {!busy && !meals.length && <p>No meals are planned this week.</p>}
            <div className="button-row">
              <button
                className="button secondary"
                disabled={busy}
                onClick={() => setSwap(false)}
              >
                Cancel
              </button>
              <button
                className="button primary"
                disabled={busy || !target || !selected}
                onClick={() => {
                  if (selected)
                    void run(
                      (signal) =>
                        personalApi.swap(
                          selected.id,
                          {
                            versionId: selected.versionId,
                            week: monday(today()),
                            today: today(),
                            targetId: target,
                            requestKey: assignment.current.key,
                          },
                          signal,
                        ),
                      () => {
                        setSwap(false);
                        setNotice("Meal replaced.");
                        assignment.current = { key: requestKey(), target: "" };
                        onPlanned();
                      },
                    );
                }}
              >
                Replace meal
              </button>
            </div>
          </>
        )}
        {error && <Alert>{error}</Alert>}
      </dialog>
    </section>
  );
}

function PersonalRecap({ value }: { value: PersonalRecipe }) {
  const content = value.content;
  return (
    <article className="personal-detail panel">
      <div className="personal-detail-photo">
        <Dish url={value.imageUrl} />
      </div>
      {content.description && <p>{content.description}</p>}
      <p>
        {content.servings} servings · {content.totalMinutes} minutes total
      </p>
      <p className="muted">
        Preparation {content.prepMinutes} min · Cooking {content.cookMinutes}{" "}
        min
      </p>
      {content.equipment && (
        <p className="personal-multiline">{content.equipment}</p>
      )}
      <h3>Ingredients</h3>
      <ul>
        {content.ingredients.map((item, index) => (
          <li key={index}>
            {[item.quantity, item.unit, item.name].filter(Boolean).join(" ")}
          </li>
        ))}
      </ul>
      <h3>Let’s make it</h3>
      <ol>
        {content.instructions.map((step, index) => (
          <li key={index} className="personal-multiline">
            {step}
          </li>
        ))}
      </ol>
      {!!content.tips.length && (
        <>
          <h3>Tips</h3>
          <ul>
            {content.tips.map((tip, index) => (
              <li key={index} className="personal-multiline">
                {tip}
              </li>
            ))}
          </ul>
        </>
      )}
      <h3>Allergens</h3>
      <p>
        {content.allergens.length
          ? content.allergens.join(" · ")
          : "No allergens declared by the author."}
      </p>
      <ImportSourceLink source={value.importSource} />
      {content.allergenNote && (
        <p className="personal-multiline">{content.allergenNote}</p>
      )}
      <p className="advisory">
        User-entered information. Check ingredient labels and
        cross-contamination before cooking.
      </p>
    </article>
  );
}

function PersonalEditor({
  initial,
  saved,
  photoEnabled,
  onDirty,
  onDiscard,
  onSaved,
}: {
  initial: DraftSeed;
  saved: PersonalRecipe | null;
  photoEnabled: boolean;
  onDirty: (dirty: boolean) => void;
  onDiscard: () => void;
  onSaved: (value: PersonalRecipe) => void;
}) {
  const [draft, setDraft] = useState(() => structuredClone(initial.content)),
    [expected, setExpected] = useState(saved?.versionId ?? "");
  const [reviewed, setReviewed] = useState<Set<string>>(() => new Set());
  const pendingImportSave = useRef<PersonalSaveInput | null>(null),
    importSaved = useRef(false);
  const [dirty, setDirty] = useState(
      !!initial.sourceRecipeId || !!initial.importSeed,
    ),
    [busy, setBusy] = useState(false),
    [uncertain, setUncertain] = useState(false),
    [error, setError] = useState("");
  const [savedImageURL, setSavedImageURL] = useState(initial.imageUrl);
  const [fields, setFields] = useState<Record<string, string>>({}),
    [conflict, setConflict] = useState<{
      current: PersonalRecipe;
      changed: string[];
    } | null>(null);
  const [photo, setPhoto] = useState<File | null>(null),
    [photoAction, setPhotoAction] =
      useState<PersonalSaveInput["photoAction"]>("keep"),
    [preview, setPreview] = useState<string | null>(null);
  const key = useRef(requestKey()),
    explicitTotal = useRef(
      !!initial.sourceRecipeId || !!saved || !!initial.importSeed,
    ),
    alive = useRef(true),
    sending = useRef(false),
    controller = useRef<AbortController | null>(null);
  const form = useRef<HTMLFormElement>(null),
    heading = useRef<HTMLHeadingElement>(null),
    photoInput = useRef<HTMLInputElement>(null);
  const conflictPanel = useRef<HTMLElement>(null),
    conflictHeading = useRef<HTMLHeadingElement>(null);
  useLayoutEffect(() => focusContentTop(form.current, heading.current), []);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      controller.current?.abort();
      if (initial.importSeed && !importSaved.current)
        void importApi.cancel(initial.importSeed.operationId).catch(() => {});
      onDirty(false);
    };
  }, []);
  useEffect(() => {
    onDirty(dirty);
    const warn = (event: BeforeUnloadEvent) => {
      if (dirty) {
        event.preventDefault();
        event.returnValue = "";
      }
    };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);
  useEffect(() => {
    if (!photo) {
      setPreview(null);
      return;
    }
    const url = URL.createObjectURL(photo);
    setPreview(url);
    return () => URL.revokeObjectURL(url);
  }, [photo]);
  useLayoutEffect(() => {
    const first = Object.keys(fields)[0];
    if (first) {
      const field = form.current?.querySelector<HTMLElement>(
        `[data-personal-field="${CSS.escape(first)}"]`,
      );
      field?.focus();
      if (field?.tagName === "FIELDSET") focusContentTop(field, field);
      else field?.scrollIntoView({ block: "center" });
    }
  }, [fields]);
  useLayoutEffect(() => {
    if (conflict)
      return focusContentTop(conflictPanel.current, conflictHeading.current);
  }, [conflict]);
  useLayoutEffect(() => {
    if (
      error &&
      !Object.keys(fields).some((key) =>
        form.current?.querySelector(
          `[data-personal-field="${CSS.escape(key)}"]`,
        ),
      )
    )
      return focusContentTop(form.current, heading.current);
  }, [error, fields]);
  function changed(contentChanged = true) {
    pendingImportSave.current = null;
    if (contentChanged) setReviewed(new Set());
    setDirty(true);
    onDirty(true);
    setError("");
    setFields({});
    key.current = requestKey();
  }
  function update<K extends keyof PersonalContent>(
    name: K,
    value: PersonalContent[K],
  ) {
    changed();
    setDraft((old) => ({ ...old, [name]: value }));
  }
  function minutes(name: "prepMinutes" | "cookMinutes", value: number) {
    changed();
    setDraft((old) => ({
      ...old,
      [name]: value,
      totalMinutes: explicitTotal.current
        ? old.totalMinutes
        : Math.max(
            1,
            value +
              (name === "prepMinutes" ? old.cookMinutes : old.prepMinutes),
          ),
    }));
  }
  async function save(version = expected) {
    if (sending.current || !form.current?.reportValidity()) return;
    if (
      initial.importSeed &&
      !importReviewKeys(initial.importSeed.draft, draft).every((field) =>
        reviewed.has(field),
      )
    ) {
      setError("Review each imported part before saving.");
      setFields({
        importReview: "Check each part after correcting the draft.",
      });
      return;
    }
    sending.current = true;
    setBusy(true);
    setError("");
    setFields({});
    const epoch = sessionEpoch();
    const request = new AbortController();
    controller.current = request;
    try {
      let input: PersonalSaveInput = pendingImportSave.current ?? {
        requestKey: key.current,
        expectedVersion: version,
        content: draft,
        photoAction,
        ...(initial.sourceRecipeId
          ? { sourceRecipeId: initial.sourceRecipeId }
          : {}),
      };
      if (initial.importSeed && !pendingImportSave.current) {
        const review = await importApi.review(
          initial.importSeed.operationId,
          draft,
          request.signal,
        );
        if (epoch !== sessionEpoch())
          throw new APIError("", "stale_session", 409);
        if (!review.requiredFields.every((field) => reviewed.has(field)))
          throw new APIError(
            "Review each imported part before saving.",
            "import_review_required",
            409,
          );
        input = {
          ...input,
          content: review.content,
          import: {
            operationId: initial.importSeed.operationId,
            confirmation: {
              draftRevision: initial.importSeed.draft.revision,
              contentRevision: review.contentRevision,
              reviewedFields: review.requiredFields,
              resolvedFields: initial.importSeed.draft.issues.map(
                (issue) => issue.field,
              ),
            },
          },
        };
        pendingImportSave.current = input;
      }
      const value = await personalApi.save(
        saved?.id ?? null,
        input,
        photo,
        request.signal,
      );
      if (alive.current && epoch === sessionEpoch()) {
        importSaved.current = true;
        onDirty(false);
        onSaved(value);
      }
    } catch (err) {
      if (!alive.current || isStale(err)) return;
      if (
        err instanceof APIError &&
        err.code === "personal_version_conflict" &&
        err.details?.current
      ) {
        setConflict({
          current: err.details.current,
          changed: err.details.changedFields ?? [],
        });
        setUncertain(false);
      } else {
        setError(
          initial.importSeed &&
            err instanceof APIError &&
            err.code === "network_uncertain"
            ? "The connection was interrupted. Your reviewed draft is still here. Retry save to check the same request."
            : message(err),
        );
        setFields(
          err instanceof APIError && err.code.startsWith("photo_")
            ? { photo: message(err) }
            : err instanceof APIError
              ? (err.details?.fields ?? {})
              : {},
        );
        setUncertain(
          err instanceof APIError && err.code === "network_uncertain",
        );
        if (
          err instanceof APIError &&
          ["photo_failed", "photo_invalid", "photo_processing"].includes(
            err.code,
          )
        ) {
          key.current = requestKey();
          pendingImportSave.current = null;
        }
      }
    } finally {
      sending.current = false;
      if (alive.current) setBusy(false);
    }
  }
  function move<T>(values: T[], index: number, direction: number): T[] {
    const copy = [...values];
    [copy[index], copy[index + direction]] = [
      copy[index + direction],
      copy[index],
    ];
    return copy;
  }
  function controls(
    kind: string,
    index: number,
    count: number,
    reorder: (direction: number) => void,
    remove: () => void,
    minimum = 0,
  ) {
    return (
      <div className="personal-row-controls">
        <button
          type="button"
          className="text-button"
          aria-label={`Move ${kind} ${index + 1} up`}
          disabled={index === 0}
          onClick={() => reorder(-1)}
        >
          ↑
        </button>
        <button
          type="button"
          className="text-button"
          aria-label={`Move ${kind} ${index + 1} down`}
          disabled={index === count - 1}
          onClick={() => reorder(1)}
        >
          ↓
        </button>
        <button
          type="button"
          className="text-button"
          aria-label={`Remove ${kind} ${index + 1}`}
          disabled={count <= minimum}
          onClick={remove}
        >
          Remove
        </button>
      </div>
    );
  }
  const errorHint = (path: string) => fields[path];
  function textRows(
    name: "instructions" | "tips" | "allergens",
    title: string,
    label: string,
    maximum: number,
    minimum = 0,
  ) {
    const values = draft[name];
    return (
      <fieldset data-personal-field={name} tabIndex={-1}>
        <legend>{title}</legend>
        {fields[name] && <p className="alert">{fields[name]}</p>}
        <ol className="personal-edit-rows">
          {values.map((value, index) => (
            <li key={index}>
              <Field
                label={`${label} ${index + 1}`}
                hint={errorHint(`${name}.${index}`)}
              >
                {name === "allergens" ? (
                  <input
                    data-personal-field={`${name}.${index}`}
                    aria-invalid={!!errorHint(`${name}.${index}`)}
                    value={value}
                    required
                    onChange={(event) =>
                      update(
                        name,
                        values.map((old, i) =>
                          i === index ? event.target.value : old,
                        ),
                      )
                    }
                  />
                ) : (
                  <textarea
                    data-personal-field={`${name}.${index}`}
                    aria-invalid={!!errorHint(`${name}.${index}`)}
                    rows={3}
                    value={value}
                    required
                    onChange={(event) =>
                      update(
                        name,
                        values.map((old, i) =>
                          i === index ? event.target.value : old,
                        ),
                      )
                    }
                  />
                )}
              </Field>
              {controls(
                label.toLowerCase(),
                index,
                values.length,
                (direction) => update(name, move(values, index, direction)),
                () =>
                  update(
                    name,
                    values.filter((_, i) => i !== index),
                  ),
                minimum,
              )}
            </li>
          ))}
        </ol>
        <button
          type="button"
          className="button secondary"
          disabled={values.length >= maximum}
          onClick={() => update(name, [...values, ""])}
        >
          Add {label.toLowerCase()}
        </button>
      </fieldset>
    );
  }
  return (
    <form
      ref={form}
      className="personal-editor panel"
      onSubmit={(event: FormEvent) => {
        event.preventDefault();
        void save();
      }}
    >
      <h2 ref={heading} tabIndex={-1} className="personal-heading">
        {saved ? "Edit your recipe" : "Create a recipe"}
      </h2>
      <p>Your recipes stay private. Save when you’re ready.</p>
      <ImportSourceLink
        source={initial.importSeed?.draft.source ?? saved?.importSource}
      />
      {error && <Alert>{error}</Alert>}
      {fields.recipe && <Alert>{fields.recipe}</Alert>}
      {uncertain && (
        <p className="notice">
          The connection ended before confirmation. Retry the same save to check
          its result before making other changes.
        </p>
      )}
      <fieldset disabled={busy || uncertain} className="personal-editor-body">
        <Field label="Title" hint={errorHint("title")}>
          <input
            data-personal-field="title"
            value={draft.title}
            aria-invalid={!!errorHint("title")}
            required
            onChange={(event) => update("title", event.target.value)}
          />
        </Field>
        <Field label="Short description" hint={errorHint("description")}>
          <textarea
            data-personal-field="description"
            value={draft.description}
            rows={3}
            onChange={(event) => update("description", event.target.value)}
          />
        </Field>
        {initial.needsServings && (
          <p className="notice">
            Confirm how many servings this recipe makes.
            {initial.sourceYield.length
              ? ` Original yield: ${initial.sourceYield.join(" · ")}`
              : " The original has no clear serving count."}
          </p>
        )}
        <Field label="Servings" hint={errorHint("servings")}>
          <input
            data-personal-field="servings"
            type="number"
            inputMode="numeric"
            min={1}
            max={40}
            step={1}
            value={draft.servings || ""}
            required
            onChange={(event) => update("servings", Number(event.target.value))}
          />
        </Field>
        <div className="personal-time-fields">
          <DurationPicker
            label="Preparation time"
            field="prepMinutes"
            value={draft.prepMinutes}
            maximum={1440}
            error={errorHint("prepMinutes")}
            onChange={(value) => minutes("prepMinutes", value)}
          />
          <DurationPicker
            label="Cooking time"
            field="cookMinutes"
            value={draft.cookMinutes}
            maximum={1440}
            error={errorHint("cookMinutes")}
            onChange={(value) => minutes("cookMinutes", value)}
          />
          <DurationPicker
            label="Total time"
            field="totalMinutes"
            value={draft.totalMinutes}
            minimum={1}
            maximum={2880}
            error={errorHint("totalMinutes")}
            onChange={(value) => {
              explicitTotal.current = true;
              update("totalMinutes", value);
            }}
          />
        </div>
        <fieldset data-personal-field="ingredients" tabIndex={-1}>
          <legend>Ingredients</legend>
          {fields.ingredients && <p className="alert">{fields.ingredients}</p>}
          <ol className="personal-edit-rows">
            {draft.ingredients.map((item, index) => (
              <li key={index}>
                <div className="personal-ingredient-fields">
                  <Field
                    label={`Ingredient ${index + 1}`}
                    hint={errorHint(`ingredients.${index}.name`)}
                  >
                    <input
                      data-personal-field={`ingredients.${index}.name`}
                      value={item.name}
                      required
                      onChange={(event) =>
                        update(
                          "ingredients",
                          draft.ingredients.map((old, i) =>
                            i === index
                              ? { ...old, name: event.target.value }
                              : old,
                          ),
                        )
                      }
                    />
                  </Field>
                  <Field
                    label={`Quantity ${index + 1}`}
                    hint={errorHint(`ingredients.${index}.quantity`)}
                  >
                    <input
                      data-personal-field={`ingredients.${index}.quantity`}
                      type="text"
                      inputMode="decimal"
                      value={item.quantity ?? ""}
                      onChange={(event) =>
                        update(
                          "ingredients",
                          draft.ingredients.map((old, i) =>
                            i === index
                              ? { ...old, quantity: event.target.value || null }
                              : old,
                          ),
                        )
                      }
                    />
                  </Field>
                  <Field
                    label={`Unit ${index + 1}`}
                    hint={errorHint(`ingredients.${index}.unit`)}
                  >
                    <input
                      data-personal-field={`ingredients.${index}.unit`}
                      value={item.unit ?? ""}
                      onChange={(event) =>
                        update(
                          "ingredients",
                          draft.ingredients.map((old, i) =>
                            i === index
                              ? { ...old, unit: event.target.value || null }
                              : old,
                          ),
                        )
                      }
                    />
                  </Field>
                </div>
                {controls(
                  "ingredient",
                  index,
                  draft.ingredients.length,
                  (direction) =>
                    update(
                      "ingredients",
                      move(draft.ingredients, index, direction),
                    ),
                  () =>
                    update(
                      "ingredients",
                      draft.ingredients.filter((_, i) => i !== index),
                    ),
                  1,
                )}
              </li>
            ))}
          </ol>
          <button
            type="button"
            className="button secondary"
            disabled={draft.ingredients.length >= 100}
            onClick={() =>
              update("ingredients", [
                ...draft.ingredients,
                { name: "", quantity: null, unit: null },
              ])
            }
          >
            Add ingredient
          </button>
        </fieldset>
        <Field label="Equipment" hint={errorHint("equipment")}>
          <textarea
            data-personal-field="equipment"
            rows={2}
            value={draft.equipment}
            onChange={(event) => update("equipment", event.target.value)}
          />
        </Field>
        {textRows("instructions", "Instructions", "Step", 100, 1)}
        {textRows("tips", "Tips (optional)", "Tip", 30)}
        {textRows("allergens", "Allergens (optional)", "Allergen", 30)}
        <Field label="Allergen note" hint={errorHint("allergenNote")}>
          <textarea
            data-personal-field="allergenNote"
            value={draft.allergenNote}
            rows={2}
            onChange={(event) => update("allergenNote", event.target.value)}
          />
        </Field>
        <p className="advisory">
          This is your own allergen declaration. Check ingredient labels and
          cross-contamination before cooking.
        </p>
        {initial.importSeed && (
          <ImportReview
            seed={initial.importSeed}
            content={draft}
            checked={reviewed}
            onChange={(value) => {
              setReviewed(value);
              pendingImportSave.current = null;
              setError("");
              setFields({});
            }}
            error={fields.importReview}
          />
        )}
        <fieldset>
          <legend>Photo (optional)</legend>
          <p className="small muted">
            JPEG, PNG or static WebP. Up to 12 MB, 8192 pixels per side and 20
            million pixels. Your photo is uploaded only when you save.
          </p>
          <div className="personal-photo-preview">
            {preview ? (
              <img src={preview} alt="Selected recipe photo preview" />
            ) : (
              <Dish url={photoAction === "remove" ? null : savedImageURL} />
            )}
          </div>
          <button
            type="button"
            className="button secondary"
            disabled={!photoEnabled}
            onClick={() => photoInput.current?.click()}
          >
            {preview || (savedImageURL && photoAction !== "remove")
              ? "Replace photo"
              : "Add photo"}
          </button>
          <Field label="Choose recipe photo" hint={errorHint("photo")}>
            <input
              ref={photoInput}
              data-personal-field="photo"
              type="file"
              accept="image/jpeg,image/png,image/webp"
              disabled={!photoEnabled}
              onChange={(event) => {
                const file = event.target.files?.[0];
                if (!file) return;
                if (file.size > 12 * 1024 * 1024) {
                  setError("Choose a photo up to 12 MB.");
                  event.target.value = "";
                  return;
                }
                changed(false);
                setPhoto(file);
                setPhotoAction("replace");
              }}
            />
          </Field>
          {!photoEnabled && (
            <p className="notice">
              Photo uploads are temporarily unavailable. You can save the recipe
              without a new photo.
            </p>
          )}
          <button
            type="button"
            className="text-button"
            onClick={() => {
              changed(false);
              setPhoto(null);
              setPhotoAction("remove");
              if (photoInput.current) photoInput.current.value = "";
            }}
          >
            Remove photo
          </button>
        </fieldset>
      </fieldset>
      {conflict && (
        <section
          ref={conflictPanel}
          className="notice personal-conflict"
          aria-label="Recipe version conflict"
        >
          <h3 ref={conflictHeading} tabIndex={-1}>
            A newer version is saved
          </h3>
          <p>
            Your draft is still here. Changed fields:{" "}
            {conflict.changed.join(", ") || "recipe content"}.
          </p>
          <details>
            <summary>Review the saved recipe</summary>
            <h4>{conflict.current.content.title}</h4>
            <PersonalRecap value={conflict.current} />
          </details>
          <div className="button-row">
            <button
              type="button"
              className="button secondary"
              disabled={busy}
              onClick={() => {
                setDraft(structuredClone(conflict.current.content));
                setExpected(conflict.current.currentVersionId);
                setSavedImageURL(conflict.current.imageUrl);
                setPhoto(null);
                setPhotoAction("keep");
                if (photoInput.current) photoInput.current.value = "";
                key.current = requestKey();
                setConflict(null);
                setDirty(false);
                onDirty(false);
              }}
            >
              Reload saved version
            </button>
            <button
              type="button"
              className="button primary"
              disabled={busy}
              onClick={() => {
                const version = conflict.current.currentVersionId;
                setExpected(version);
                key.current = requestKey();
                setConflict(null);
                void save(version);
              }}
            >
              Save my draft as a new version
            </button>
          </div>
        </section>
      )}
      <div className="button-row personal-save-actions">
        <button
          type="submit"
          className="button primary"
          disabled={busy || !!conflict}
        >
          {busy ? "Saving…" : uncertain ? "Retry save" : "Save"}
        </button>
        <button
          type="button"
          className="button secondary"
          disabled={busy}
          onClick={() => {
            onDirty(false);
            onDiscard();
          }}
        >
          Discard changes
        </button>
      </div>
    </form>
  );
}
