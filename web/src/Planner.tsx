import { focusContentTop } from "./scroll";
import {
  useLayoutEffect,
  useEffect,
  useRef,
  useState,
  type FormEvent,
} from "react";
import {
  api,
  isStale,
  message,
  personalApi,
  type MenuInput,
  type Profile,
  type RecipePage,
} from "./api";
import { Alert, Field, Icon } from "./components";
import { NumberStepper } from "./Choices";
import { addDays, dateLabel, monday, today } from "./dates";
import { menuInputError, validDate } from "./validation";
import type { FavoriteWeek } from "./Favorites";
import { effortLevels } from "./effort";
import type { PlanningCalendar } from "./PlanningZone";

export function Planner({
  onSubmit,
  onCancel,
  preferences,
  initialInput,
  calendar,
  week,
}: {
  onSubmit: (input: MenuInput) => void;
  onCancel: () => void;
  preferences: Profile["preferences"];
  initialInput?: MenuInput | null;
  calendar?: PlanningCalendar;
  week: string;
}) {
  const [queued, setQueued] = useState<FavoriteWeek[]>([]);
  const [input, setInput] = useState<MenuInput | null>(null),
    [error, setError] = useState("");
  const [draft, setDraft] = useState<MenuInput | null>(initialInput ?? null);
  const [confirmation, setConfirmation] = useState<MenuInput | null>(null);
  const [checking, setChecking] = useState(false);
  const [savedDates, setSavedDates] = useState<string[] | null>(null);
  const [replaceSlots, setReplaceSlots] = useState(
    !!initialInput?.replaceExisting,
  );
  const weekDates = Array.from({ length: calendar?.premium ? 7 : 3 }, (_, i) =>
    addDays(week, i),
  );
  const openDates = weekDates.filter((date) => !savedDates?.includes(date));
  const occupiedDates = weekDates.filter((date) => savedDates?.includes(date));
  const availableDates = replaceSlots ? occupiedDates : openDates;
  const singleReplacement = replaceSlots && !calendar?.premium;

  useEffect(() => {
    if (!calendar) return;
    let active = true;
    setSavedDates(null);
    api<RecipePage>(`/menus?from=${week}&to=${addDays(week, 7)}`)
      .then((page) => {
        if (!active) return;
        const dates = page.recipes.map((r) => r.date);
        setSavedDates(dates);
        if (weekDates.every((date) => dates.includes(date)))
          setReplaceSlots(true);
      })
      .catch((err) => {
        if (active && !isStale(err)) setError(message(err));
      });
    return () => {
      active = false;
    };
  }, [calendar?.premium, week]);

  const dialog = useRef<HTMLDialogElement>(null);
  const panel = useRef<HTMLElement>(null),
    reviewHeading = useRef<HTMLHeadingElement>(null);
  useLayoutEffect(() => {
    if (input) {
      // Modal focus containment must end before the review heading can receive focus.
      dialog.current?.close();
      return focusContentTop(panel.current, reviewHeading.current);
    }
  }, [input]);
  useEffect(() => {
    if (confirmation && !dialog.current?.open) dialog.current?.showModal();
    if (!confirmation && dialog.current?.open) dialog.current.close();
  }, [confirmation]);
  async function review(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const form = new FormData(e.currentTarget);
    const date = String(form.get("date")),
      count = Number(form.get("count"));
    let dates: string[];
    if (calendar) {
      dates = form.getAll("slot").map(String).sort();
      if (
        !savedDates ||
        dates.length === 0 ||
        (singleReplacement && dates.length !== 1) ||
        dates.some((day) => !availableDates.includes(day))
      ) {
        setError("Choose an available dinner date.");
        return;
      }
    } else {
      if (
        !validDate(date) ||
        !Number.isInteger(count) ||
        count < 1 ||
        count > 7
      ) {
        setError("Choose one to seven meals and a valid date.");
        return;
      }
      dates = Array.from({ length: count }, (_, i) => addDays(date, i));
    }
    const value: MenuInput = {
      dates,
      ...(calendar && replaceSlots ? { replaceExisting: true } : {}),
      adults: Number(form.get("adults")),
      children: Number(form.get("children")),
      maxMinutes: Number(form.get("minutes")),
      budget: String(form.get("budget")) as MenuInput["budget"],
      region: "Belgium",
      creativity: 5,
    };
    const problem = menuInputError(
      value.dates,
      value.adults,
      value.children,
      value.maxMinutes,
    );
    if (problem) {
      setError(problem);
      return;
    }
    setError("");
    setDraft(value);
    setChecking(true);
    try {
      const weeks = Array.from(new Set(value.dates.map(monday))).sort();
      const pending = await Promise.all(
        weeks.map((week) => api<FavoriteWeek>(`/favorite-weeks/${week}`)),
      );
      value.favoriteRevisions = Object.fromEntries(
        pending.map((v) => [v.week, v.revision]),
      );
      setQueued(pending);
      if (
        pending.some((week) => week.entries?.some((entry) => entry.blocked))
      ) {
        setError(
          "Some saved personal recipes need Premium. Remove the blocked entries below or return after Premium is active, then review again.",
        );
        return;
      }
      for (const list of pending) {
        if (
          list.recipes.length >
          value.dates.filter((d) => monday(d) === list.week).length
        ) {
          setError(
            `You have ${list.recipes.length} saved recipes for the week of ${dateLabel(list.week)}. Choose at least that many meals in that week.`,
          );
          return;
        }
      }
      if (calendar) {
        const latest = await api<RecipePage>(
          `/menus?from=${week}&to=${addDays(week, 7)}`,
        );
        const dates = latest.recipes.map((r) => r.date);
        if (value.dates.some((day) => dates.includes(day) !== replaceSlots)) {
          setSavedDates(dates);
          if (weekDates.every((day) => dates.includes(day)))
            setReplaceSlots(true);
          setError("This week changed. Choose your dinner dates again.");
          return;
        }
        if (replaceSlots) setConfirmation(value);
        else setInput(value);
        return;
      }
      const saved = await Promise.all(
        weeks.map((week) =>
          api<RecipePage>(`/menus?from=${week}&to=${addDays(week, 7)}&limit=1`),
        ),
      );
      if (saved.some((page) => page.recipes.length > 0)) setConfirmation(value);
      else setInput(value);
    } catch (err) {
      if (!isStale(err)) setError(message(err));
    } finally {
      setChecking(false);
    }
  }
  return (
    <section ref={panel} className="panel planner">
      <div className="section-title">
        <div>
          <span className="eyebrow">
            {input ? "Step 2 of 2" : "Step 1 of 2"}
          </span>
          <h3 ref={reviewHeading} tabIndex={-1}>
            {input ? "Review planned dinners" : "Plan dinners"}
          </h3>
        </div>
        <button className="text-button" onClick={onCancel}>
          Close
        </button>
      </div>
      <ol className="planner-steps" aria-label="Menu planning steps">
        <li aria-current={!input ? "step" : undefined}>
          <span>1</span> Your household
        </li>
        <li aria-current={input ? "step" : undefined}>
          <span>2</span> Review your menu
        </li>
      </ol>
      {error && <Alert>{error}</Alert>}
      {queued.some((week) => week.entries?.some((entry) => entry.blocked)) && (
        <section className="notice" aria-label="Blocked personal recipes">
          <h4>Saved recipes need your attention</h4>
          {queued.flatMap((week) =>
            (week.entries ?? [])
              .filter((entry) => entry.blocked)
              .map((entry) => (
                <div key={entry.id}>
                  <p>{entry.recipe.title}</p>
                  <button
                    type="button"
                    className="text-button"
                    disabled={checking}
                    onClick={async () => {
                      setChecking(true);
                      try {
                        const value = await personalApi.removeQueued(
                          entry.id,
                          crypto.randomUUID().replaceAll("-", ""),
                        );
                        setQueued((old) =>
                          old.map((item) =>
                            item.week === value.week ? value : item,
                          ),
                        );
                        setError("");
                      } catch (err) {
                        if (!isStale(err)) setError(message(err));
                      } finally {
                        setChecking(false);
                      }
                    }}
                  >
                    Remove blocked recipe
                  </button>
                </div>
              )),
          )}
        </section>
      )}
      <dialog
        ref={dialog}
        className="replacement-dialog"
        aria-labelledby="replacement-title"
        aria-describedby="replacement-description"
        onCancel={(event) => {
          event.preventDefault();
          setConfirmation(null);
        }}
      >
        <h3 id="replacement-title">You have meals planned.</h3>
        <p id="replacement-description">
          {calendar
            ? "Only the selected dinners will be replaced. Your other meals will stay."
            : `Planning again will replace all meals in ${
                confirmation && new Set(confirmation.dates.map(monday)).size > 1
                  ? "these weeks"
                  : "this week"
              }. Your current meals stay until the new plan is ready.`}
        </p>
        <ul className="replacement-weeks">
          {calendar
            ? confirmation?.dates.map((day) => (
                <li key={day}>{dateLabel(day)}</li>
              ))
            : Array.from(new Set(confirmation?.dates.map(monday) ?? []))
                .sort()
                .map((week) => (
                  <li key={week}>
                    {dateLabel(week)} – {dateLabel(addDays(week, 6))}
                  </li>
                ))}
        </ul>
        <div className="button-row replacement-actions">
          <button
            type="button"
            className="button secondary"
            onClick={() => setConfirmation(null)}
          >
            Keep my menu
          </button>
          <button
            type="button"
            className="button primary"
            onClick={() => {
              if (confirmation)
                setInput({ ...confirmation, replaceExisting: true });
              setConfirmation(null);
            }}
          >
            Review replacement
          </button>
        </div>
      </dialog>
      {input ? (
        <>
          {queued.some((q) => q.recipes.length > 0) && (
            <div className="notice">
              <strong>Saved recipes included</strong>
              <ul>
                {queued.flatMap((q) =>
                  q.recipes.map((r) => (
                    <li key={`${q.week}/${r.id}`}>
                      {r.title}
                      {r.source && (
                        <span className="personal-version small muted">
                          Personal version {r.source.versionId.slice(0, 8)} ·{" "}
                          {r.totalMinutes} minutes · Declared allergens:{" "}
                          {r.allergens?.join(", ") || "not listed"}. Check
                          labels before cooking.
                          {r.allergenNote && (
                            <span className="personal-version">
                              {r.allergenNote}
                            </span>
                          )}
                        </span>
                      )}
                    </li>
                  )),
                )}
              </ul>
              <p>
                These count toward your meal total and will be shuffled into the
                week. Saved portions and ingredients stay as they are; review
                them for your household and allergies.
              </p>
            </div>
          )}
          <div className="review-overview">
            <dl className="review-summary">
              <div>
                <dt>
                  <Icon name="meal" />
                  Meals
                </dt>
                <dd>
                  {input.dates.length}{" "}
                  {input.dates.length === 1 ? "meal planned" : "meals planned"}
                </dd>
              </div>
              <div>
                <dt>
                  <Icon name="person" />
                  Your household
                </dt>
                <dd className="review-household">
                  <span>
                    {input.adults} {input.adults === 1 ? "adult" : "adults"}
                  </span>
                  <span>
                    {input.children}{" "}
                    {input.children === 1 ? "child" : "children"}
                  </span>
                </dd>
              </div>
              <div>
                <dt>
                  <Icon name="clock" />
                  Cooking effort
                </dt>
                <dd>
                  {
                    effortLevels.find(
                      (level) => level.minutes === input.maxMinutes,
                    )?.label
                  }
                  <small>Up to {input.maxMinutes} min</small>
                </dd>
              </div>
              <div>
                <dt>
                  <Icon name="wallet" />
                  Budget
                </dt>
                <dd>
                  {
                    {
                      low: "Everyday",
                      medium: "Flexible",
                      high: "Treat yourself",
                    }[input.budget]
                  }
                </dd>
              </div>
            </dl>
          </div>
          <dl className="review-grid">
            <div className="review-dates">
              <dt>
                <Icon name="calendar" />
                Your dates
              </dt>
              <dd>
                <ul className="review-date-list">
                  {input.dates.map((date) => {
                    const day = new Date(`${date}T12:00:00Z`);
                    return (
                      <li key={date}>
                        <time dateTime={date}>
                          <span>
                            {day.toLocaleDateString(undefined, {
                              weekday: "short",
                              timeZone: "UTC",
                            })}
                          </span>
                          <strong>
                            {day.toLocaleDateString(undefined, {
                              day: "numeric",
                              month: "short",
                              timeZone: "UTC",
                            })}
                          </strong>
                          <span>{date.slice(0, 4)}</span>
                        </time>
                      </li>
                    );
                  })}
                </ul>
              </dd>
            </div>
          </dl>
          <p className="review-included">
            <Icon name="leaf" />
            Your saved preferences are included.
          </p>
          <div className="button-row review-actions">
            <button className="button secondary" onClick={() => setInput(null)}>
              Edit request
            </button>
            <button className="button primary" onClick={() => onSubmit(input)}>
              Plan my week
              <Icon name="arrow" />
            </button>
          </div>
        </>
      ) : (
        <form onSubmit={review}>
          <fieldset className="household-picker">
            <legend>Who’s eating?</legend>
            <div className="household-grid">
              <NumberStepper
                label="Adults"
                name="adults"
                icon="adult"
                min={1}
                max={20}
                initial={draft?.adults ?? 2}
              />
              <NumberStepper
                label="Children"
                name="children"
                icon="child"
                min={0}
                max={20}
                initial={draft?.children ?? 0}
              />
            </div>
          </fieldset>
          {calendar ? (
            <fieldset
              className="planning-slots"
              key={String(replaceSlots) + availableDates.join()}
            >
              <legend>Dinner dates</legend>
              {!savedDates ? (
                <p role="status">Checking saved dinners…</p>
              ) : (
                <>
                  {!calendar.premium && (
                    <p className="muted">Free includes Monday to Wednesday.</p>
                  )}
                  {openDates.length > 0 && occupiedDates.length > 0 && (
                    <button
                      className="button secondary"
                      type="button"
                      onClick={() => setReplaceSlots(!replaceSlots)}
                    >
                      {replaceSlots
                        ? "Plan open dinners"
                        : "Replace saved dinners"}
                    </button>
                  )}
                  {availableDates.map((day, index) => (
                    <label className="planning-slot" key={day}>
                      <input
                        name="slot"
                        type={singleReplacement ? "radio" : "checkbox"}
                        value={day}
                        defaultChecked={
                          draft && !!draft.replaceExisting === replaceSlots
                            ? draft.dates.includes(day)
                            : !replaceSlots ||
                              (singleReplacement && index === 0)
                        }
                      />
                      <span>
                        {new Date(day + "T12:00:00Z").toLocaleDateString(
                          undefined,
                          { weekday: "long", timeZone: "UTC" },
                        )}{" "}
                        · {dateLabel(day)}
                      </span>
                    </label>
                  ))}
                </>
              )}
            </fieldset>
          ) : (
            <div className="form-row">
              <Field label="First meal date">
                <input
                  name="date"
                  type="date"
                  defaultValue={draft?.dates[0] ?? today()}
                  required
                />
              </Field>
              <NumberStepper
                label="Number of meals"
                name="count"
                min={1}
                max={7}
                initial={draft?.dates.length ?? 3}
              />
            </div>
          )}
          <fieldset className="menu-preferences">
            <legend>Your preferences</legend>
            <div className="form-row">
              <Field label="Cooking effort">
                <select
                  name="minutes"
                  defaultValue={
                    draft?.maxMinutes ??
                    effortLevels.find(
                      (level) => level.label === preferences.effort,
                    )?.minutes ??
                    30
                  }
                >
                  {effortLevels.map((level) => (
                    <option key={level.minutes} value={level.minutes}>
                      {level.shortLabel}
                    </option>
                  ))}
                </select>
              </Field>
              <Field label="Budget">
                <select name="budget" defaultValue={draft?.budget ?? "low"}>
                  <option value="low">Everyday / low</option>
                  <option value="medium">More flexibility</option>
                  <option value="high">Treat yourself</option>
                </select>
              </Field>
            </div>
            {(preferences.diet.length > 0 ||
              preferences.allergens.length > 0 ||
              preferences.dislikes.length > 0) && (
              <div className="preference-chips" aria-label="Saved preferences">
                {preferences.diet.map((value, index) => (
                  <span key={`diet-${index}`}>
                    <Icon name="leaf" />
                    {value}
                  </span>
                ))}
                {[...preferences.allergens, ...preferences.dislikes].map(
                  (value, index) => (
                    <span key={`avoid-${index}`}>Avoid {value}</span>
                  ),
                )}
              </div>
            )}
          </fieldset>
          <button
            className="button primary"
            disabled={checking || (!!calendar && !savedDates)}
          >
            {checking ? "Checking your week…" : "Review menu"}
            <Icon name="arrow" />
          </button>
        </form>
      )}
    </section>
  );
}
