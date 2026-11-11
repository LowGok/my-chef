import { useEffect, useRef, useState } from "react";
import {
  api,
  APIError,
  isStale,
  message,
  personalApi,
  sessionEpoch,
  type Recipe,
  type RecipePage,
} from "./api";
import type { components } from "./api.generated";
import { Alert, FoodPlaceholder, Icon } from "./components";
import { addDays, dateLabel, monday, today } from "./dates";
import { PersonalCopyAction, recipeImage } from "./PersonalRecipes";
export type FavoriteWeek = components["schemas"]["FavoriteWeek"];

function DishImage({ recipe }: { recipe: Recipe }) {
  return recipeImage(recipe.imageUrl) ? (
    <img
      src={recipe.imageUrl!}
      alt=""
      loading="lazy"
      onError={(e) => {
        e.currentTarget.hidden = true;
        e.currentTarget.parentElement?.classList.add("image-failed");
      }}
    />
  ) : (
    <FoodPlaceholder />
  );
}
export function FavoriteToggle({
  recipe,
  onChange,
}: {
  recipe: Recipe;
  onChange: (r: Recipe) => void;
}) {
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const alive = useRef(true),
    pending = useRef(false);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  async function toggle() {
    if (pending.current) return;
    pending.current = true;
    setBusy(true);
    setError("");
    const epoch = sessionEpoch();
    try {
      const value = await api<Recipe>(`/recipes/${recipe.id}/favorite`, {
        method: recipe.isFavorite ? "DELETE" : "PUT",
      });
      if (alive.current && epoch === sessionEpoch()) onChange(value);
    } catch (e) {
      if (alive.current && !isStale(e))
        setError(
          "Couldn't update your favorite. Check your connection and try again.",
        );
    } finally {
      pending.current = false;
      if (alive.current) setBusy(false);
    }
  }
  return (
    <div className="favorite-toggle">
      <button
        type="button"
        className={`button secondary heart-toggle${recipe.isFavorite ? " is-favorite" : ""}`}
        aria-label={
          recipe.isFavorite ? "Remove from favorites" : "Add to favorites"
        }
        aria-pressed={recipe.isFavorite}
        disabled={busy}
        onClick={() => void toggle()}
      >
        <Icon name="heart" />
        {recipe.isFavorite ? "Saved" : "Save recipe"}
      </button>
      {error && <Alert>{error}</Alert>}
    </div>
  );
}
export function QueuedFavorites({ week }: { week: string }) {
  const [value, setValue] = useState<FavoriteWeek | null>(null),
    [error, setError] = useState("");
  const [retry, setRetry] = useState(0);
  const [removing, setRemoving] = useState(false);
  const removalKeys = useRef(new Map<string, string>()),
    alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  useEffect(() => {
    let active = true;
    setValue(null);
    setError("");
    api<FavoriteWeek>(`/favorite-weeks/${week}`)
      .then((v) => {
        if (active) setValue(v);
      })
      .catch((e) => {
        if (active && !isStale(e))
          setError("Couldn't load saved recipes for this week.");
      });
    return () => {
      active = false;
    };
  }, [week, retry]);
  if (error)
    return (
      <p className="notice">
        {error}{" "}
        <button className="text-button" onClick={() => setRetry((v) => v + 1)}>
          Retry saved recipes
        </button>
      </p>
    );
  if (!value?.recipes.length) return null;
  return (
    <section
      className="queued-favorites panel"
      aria-label="Saved for this week"
    >
      <h4>Saved for this week</h4>
      <p className="muted small">
        These recipes will be shuffled in when you plan this week.
      </p>
      <ul>
        {value.recipes.map((r) => {
          const entry = value.entries?.find(
            (candidate) => candidate.recipe.id === r.id,
          );
          return (
            <li key={r.id}>
              {r.title}
              {r.source && (
                <small className="muted personal-version">
                  Saved personal version {r.source.versionId.slice(0, 8)}
                </small>
              )}
              {entry?.blocked && (
                <p className="notice">
                  Premium is needed to include this recipe. Remove it to plan
                  other meals.
                </p>
              )}
              {entry && (
                <button
                  type="button"
                  className="text-button"
                  disabled={removing}
                  onClick={async () => {
                    let key = removalKeys.current.get(entry.id);
                    if (!key) {
                      key = crypto.randomUUID().replaceAll("-", "");
                      removalKeys.current.set(entry.id, key);
                    }
                    setRemoving(true);
                    setError("");
                    try {
                      const result = await personalApi.removeQueued(
                        entry.id,
                        key,
                      );
                      if (alive.current) setValue(result);
                    } catch (err) {
                      if (alive.current && !isStale(err))
                        setError(message(err));
                    } finally {
                      if (alive.current) setRemoving(false);
                    }
                  }}
                >
                  Remove from this week
                </button>
              )}
            </li>
          );
        })}
      </ul>
    </section>
  );
}
async function weekMeals(week: string) {
  const recipes: Recipe[] = [];
  let offset: number | null = 0;
  while (offset !== null) {
    const page: RecipePage = await api(
      `/menus?from=${week}&to=${addDays(week, 7)}&limit=50&offset=${offset}`,
    );
    recipes.push(...page.recipes);
    offset = page.nextOffset ?? null;
  }
  return recipes;
}
export function Favorites({
  onOpen,
  onWeek,
  onCopy,
}: {
  onOpen: (r: Recipe) => void;
  onWeek: (week: string) => void;
  onCopy?: (recipe: Recipe) => void;
}) {
  const [recipes, setRecipes] = useState<Recipe[]>([]),
    [next, setNext] = useState<number | null>(null),
    [loading, setLoading] = useState(true),
    [loadError, setLoadError] = useState("");
  const [selected, setSelected] = useState<Recipe | null>(null),
    [mode, setMode] = useState<"recap" | "swap">("recap"),
    [target, setTarget] = useState("");
  const [reference, setReference] = useState(today()),
    [queue, setQueue] = useState<FavoriteWeek | null>(null),
    [meals, setMeals] = useState<Recipe[]>([]),
    [choicesError, setChoicesError] = useState("");
  const [personalAllowed, setPersonalAllowed] = useState<boolean | null>(null);
  const canAssign = !selected?.source || personalAllowed === true;
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [success, setSuccess] = useState("");
  const [choiceRetry, setChoiceRetry] = useState(0);
  const dialog = useRef<HTMLDialogElement>(null),
    heading = useRef<HTMLElement>(null),
    alive = useRef(true),
    pending = useRef(false),
    swapRequest = useRef<{ targetId: string; requestKey: string } | null>(null),
    loadVersion = useRef(0);
  const current = monday(reference),
    upcoming = addDays(current, 7);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      loadVersion.current++;
    };
  }, []);
  async function load(offset = 0) {
    const version = ++loadVersion.current;
    setLoading(true);
    setLoadError("");
    try {
      const value = await api<RecipePage>(
        `/favorites?limit=12&offset=${offset}`,
      );
      if (alive.current && version === loadVersion.current) {
        setRecipes((r) => (offset ? [...r, ...value.recipes] : value.recipes));
        setNext(value.nextOffset ?? null);
      }
    } catch (e) {
      if (alive.current && version === loadVersion.current && !isStale(e))
        setLoadError("Couldn't load your favorites. Try again.");
    } finally {
      if (alive.current && version === loadVersion.current) setLoading(false);
    }
  }
  useEffect(() => {
    void load();
  }, []);
  useEffect(() => {
    if (selected && !dialog.current?.open) dialog.current?.showModal();
    else if (!selected && dialog.current?.open) dialog.current.close();
  }, [selected]);
  useEffect(() => {
    if (!selected) return;
    let active = true;
    setQueue(null);
    setMeals([]);
    setChoicesError("");
    setPersonalAllowed(null);
    Promise.all([
      api<FavoriteWeek>(`/favorite-weeks/${upcoming}`),
      weekMeals(current),
      selected.source ? personalApi.permissions() : Promise.resolve(null),
    ])
      .then(([q, m, capability]) => {
        if (active) {
          setQueue(q);
          setMeals(m);
          setPersonalAllowed(capability?.canWrite ?? false);
        }
      })
      .catch((e) => {
        if (active && !isStale(e))
          setChoicesError("Couldn't load week information. Try again.");
      });
    return () => {
      active = false;
    };
  }, [selected?.id, current, upcoming, choiceRetry]);
  function open(recipe: Recipe) {
    setReference(today());
    setSelected(recipe);
    setMode("recap");
    setTarget("");
    swapRequest.current = null;
    setError("");
    setSuccess("");
  }
  function close() {
    if (!pending.current) setSelected(null);
  }
  async function act(kind: "queue" | "swap" | "remove") {
    if (!selected || pending.current) return;
    if (kind !== "remove" && !canAssign) {
      setError("Premium is needed to add this personal recipe to a plan.");
      return;
    }
    if (kind !== "remove" && monday(today()) !== current) {
      setReference(today());
      setTarget("");
      setMode("recap");
      setError("The week changed. Review the new dates before continuing.");
      return;
    }
    pending.current = true;
    setBusy(true);
    setError("");
    setSuccess("");
    const epoch = sessionEpoch(),
      recipe = selected;
    try {
      if (kind === "remove") {
        await api<Recipe>(`/recipes/${recipe.id}/favorite`, {
          method: "DELETE",
        });
        if (!alive.current || epoch !== sessionEpoch()) return;
        setRecipes((r) => r.filter((v) => v.id !== recipe.id));
        dialog.current?.close();
        setSelected(null);
        heading.current?.focus();
        void load();
      } else if (kind === "queue") {
        const result = await api<FavoriteWeek>(
          `/favorites/${recipe.id}/queue`,
          {
            method: "POST",
            body: JSON.stringify({ week: upcoming, today: reference }),
          },
        );
        if (!alive.current || epoch !== sessionEpoch()) return;
        setQueue(result);
        setSuccess("Added to next week.");
      } else {
        if (!swapRequest.current || swapRequest.current.targetId !== target)
          swapRequest.current = {
            targetId: target,
            requestKey: crypto.randomUUID(),
          };
        await api<Recipe>(`/favorites/${recipe.id}/swap`, {
          method: "POST",
          body: JSON.stringify({
            ...swapRequest.current,
            week: current,
            today: reference,
          }),
        });
        if (!alive.current || epoch !== sessionEpoch()) return;
        setSuccess("Meal swapped.");
        setMode("recap");
        setTarget("");
        setChoiceRetry((v) => v + 1);
      }
    } catch (e) {
      if (alive.current && !isStale(e)) {
        if (e instanceof APIError && e.code === "premium_required") {
          setPersonalAllowed(false);
          setError("Premium is needed to add this personal recipe to a plan.");
          return;
        }
        setError(
          kind === "queue"
            ? "Couldn't add this recipe. The week may be full or the connection interrupted. Retry to check the saved result."
            : kind === "swap"
              ? "Couldn't confirm the swap. Retry the same selection to check its result, or go back and review the latest meals."
              : message(e),
        );
        if (kind === "queue") setChoiceRetry((v) => v + 1);
        if (
          kind === "swap" &&
          e instanceof APIError &&
          [404, 409].includes(e.status)
        ) {
          setChoiceRetry((v) => v + 1);
          setTarget("");
          swapRequest.current = null;
          setError(
            "This meal changed. Choose from the latest meals and confirm again.",
          );
        }
      }
    } finally {
      pending.current = false;
      if (alive.current) setBusy(false);
    }
  }
  const added = !!selected && !!queue?.addedIds.includes(selected.id),
    full = (queue?.recipes.length ?? 0) >= 7;
  return (
    <section className="favorites" aria-labelledby="favorites-heading">
      <details className="account-details" open>
        <summary id="favorites-heading" ref={heading}>
          My favorites
        </summary>
        {loadError && (
          <Alert>
            {loadError}
            <button
              className="text-button"
              onClick={() => void load(recipes.length)}
            >
              Retry favorites
            </button>
          </Alert>
        )}
        {loading && (
          <p role="status" className="muted small">
            Loading favorites…
          </p>
        )}
        {!loading && !loadError && !recipes.length && (
          <p className="muted">
            Save recipes you’d like to cook again using the heart on a recipe.
          </p>
        )}
        <div className="favorites-grid">
          {recipes.map((recipe) => (
            <button
              type="button"
              key={recipe.id}
              className="favorite-card"
              onClick={() => open(recipe)}
            >
              <span className="favorite-image">
                <DishImage recipe={recipe} />
              </span>
              <span>{recipe.title}</span>
            </button>
          ))}
        </div>
        {next !== null && !loading && (
          <button
            className="text-button load-more"
            onClick={() => void load(next)}
          >
            Load more favorites
          </button>
        )}
      </details>
      <dialog
        ref={dialog}
        className="favorite-dialog"
        aria-label="Favorite recipe"
        onKeyDown={(event) => {
          if (event.key !== "Tab") return;
          const controls = Array.from(
            event.currentTarget.querySelectorAll<HTMLElement>(
              'button:not([disabled]),a[href],input:not([disabled]),select:not([disabled]),[tabindex]:not([tabindex="-1"])',
            ),
          ).filter((el) => el.getClientRects().length > 0);
          const first = controls[0],
            last = controls[controls.length - 1];
          if (event.shiftKey && document.activeElement === first) {
            event.preventDefault();
            last?.focus();
          } else if (!event.shiftKey && document.activeElement === last) {
            event.preventDefault();
            first?.focus();
          }
        }}
        onCancel={(e) => {
          e.preventDefault();
          close();
        }}
        onClose={() => {
          if (!pending.current) setSelected(null);
        }}
      >
        {selected && (
          <>
            <header className="favorite-dialog-header">
              <span className="eyebrow">Your saved recipe</span>
              <button
                className="text-button dialog-close"
                aria-label="Close favorite"
                disabled={busy}
                onClick={close}
              >
                ×
              </button>
            </header>
            <div className="favorite-dialog-body">
              <div className="favorite-recap">
                <span className="favorite-image">
                  <DishImage recipe={selected} />
                </span>
                <h3>{selected.title}</h3>
                <p className="small muted">
                  {[
                    selected.totalMinutes != null
                      ? `${selected.totalMinutes} min`
                      : null,
                    selected.yield?.join(" · "),
                  ]
                    .filter(Boolean)
                    .join(" · ")}
                </p>
                <p className="small">
                  {selected.ingredients
                    .slice(0, 5)
                    .map((i) => i.name)
                    .join(", ")}
                  {selected.ingredients.length > 5
                    ? ` + ${selected.ingredients.length - 5} more`
                    : ""}
                </p>
                <p className="small muted">
                  {(selected.allergenNote && selected.allergenNote.length > 180
                    ? `${selected.allergenNote.slice(0, 180)}…`
                    : selected.allergenNote) ||
                    "Check ingredients and labels before cooking."}
                  {!!selected.allergens?.length &&
                    ` Listed allergens: ${selected.allergens.join(", ")}.`}
                </p>
              </div>
              <div className="favorite-secondary">
                {onCopy && (
                  <PersonalCopyAction
                    onCopy={() => {
                      setSelected(null);
                      onCopy(selected);
                    }}
                  />
                )}
                <button
                  className="text-button"
                  disabled={busy}
                  onClick={() => {
                    setSelected(null);
                    onOpen(selected);
                  }}
                >
                  View full recipe
                </button>
                <button
                  className="text-button"
                  disabled={busy}
                  onClick={() => void act("remove")}
                >
                  Remove from favorites
                </button>
              </div>
              {choicesError && (
                <Alert>
                  {choicesError}
                  <button
                    className="text-button"
                    onClick={() => setChoiceRetry((v) => v + 1)}
                  >
                    Retry week information
                  </button>
                </Alert>
              )}
              {error && <Alert>{error}</Alert>}
              {selected.source && personalAllowed === false && (
                <p className="notice">
                  This favorite stays linked to your personal recipe. Premium is
                  needed to add it to a new plan.
                </p>
              )}
              {success && (
                <p role="status" className="success">
                  {success}{" "}
                  <button
                    className="text-button"
                    onClick={() => {
                      setSelected(null);
                      onWeek(success === "Meal swapped." ? current : upcoming);
                    }}
                  >
                    View week
                  </button>
                </p>
              )}
              {mode === "swap" ? (
                <fieldset className="favorite-meal-choice" disabled={busy}>
                  <legend>Choose a meal to replace</legend>
                  {meals.map((meal) => (
                    <label key={meal.id}>
                      <input
                        type="radio"
                        name="favorite-swap-target"
                        value={meal.id}
                        checked={target === meal.id}
                        onChange={() => {
                          setTarget(meal.id);
                          swapRequest.current = null;
                          setError("");
                        }}
                      />
                      <span>
                        {dateLabel(meal.date)}
                        <strong>{meal.title}</strong>
                      </span>
                    </label>
                  ))}
                </fieldset>
              ) : (
                <p className="small muted favorite-week-hint">
                  Next week: {dateLabel(upcoming)}–
                  {dateLabel(addDays(upcoming, 6))}
                  <br />
                  This week: {dateLabel(current)}–
                  {dateLabel(addDays(current, 6))}
                  {!meals.length && queue && (
                    <>
                      <br />
                      No meals to swap this week.
                    </>
                  )}
                  {full && !added && (
                    <>
                      <br />
                      Next week already has seven saved recipes.
                    </>
                  )}
                </p>
              )}
            </div>
            <footer className="favorite-actions">
              {mode === "recap" ? (
                <>
                  <button
                    className="button primary"
                    disabled={busy || !queue || added || full || !canAssign}
                    onClick={() => void act("queue")}
                  >
                    {added ? "Added to next week" : "Add to next week"}
                  </button>
                  <button
                    className="button secondary"
                    disabled={busy || !queue || !meals.length || !canAssign}
                    onClick={() => {
                      setMode("swap");
                      setError("");
                      setSuccess("");
                      setChoiceRetry((v) => v + 1);
                    }}
                  >
                    Swap this week
                  </button>
                </>
              ) : (
                <>
                  <button
                    className="button secondary"
                    disabled={busy}
                    onClick={() => {
                      setMode("recap");
                      setError("");
                      setTarget("");
                      swapRequest.current = null;
                    }}
                  >
                    Back
                  </button>
                  <button
                    className="button primary"
                    disabled={busy || !target || !canAssign}
                    onClick={() => void act("swap")}
                  >
                    {busy ? "Saving…" : "Confirm swap"}
                  </button>
                </>
              )}
            </footer>
          </>
        )}
      </dialog>
    </section>
  );
}
