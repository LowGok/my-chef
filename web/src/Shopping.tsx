import { useState, type FormEvent } from "react";
import type { Profile } from "./api";
import { Alert, Icon } from "./components";
import { addDays, dateLabel } from "./dates";
import { orderShopping, type ShoppingOrder } from "./shopping-order";
import { useShopping } from "./useShopping";

export function Shopping({
  profile,
  week,
  onWeek,
  offlineOnly = false,
}: {
  profile: Profile;
  week: string;
  onWeek: (week: string) => void;
  offlineOnly?: boolean;
}) {
  const {
    list,
    tabOnly,
    error,
    busy,
    savingOnDevice,
    offline,
    conflict,
    notice,
    pending,
    blockedWeek,
    recoveryKeepsRemoved,
    edit,
    retry,
  } = useShopping(profile, week, offlineOnly);
  const [tab, setTab] = useState<"buy" | "home">("buy"),
    [mealsOpen, setMealsOpen] = useState(
      () => matchMedia("(min-width: 900px)").matches,
    );
  const [order, setOrder] = useState<ShoppingOrder>("category");
  function addExtra(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget,
      data = new FormData(form);
    void edit({
      kind: "extra",
      id: crypto.randomUUID(),
      name: String(data.get("name")).trim(),
      quantity: String(data.get("quantity") ?? "").trim() || null,
      unit: String(data.get("unit") ?? "").trim(),
    });
    form.reset();
  }
  const counts = { buy: 0, basket: 0, home: 0 };
  list?.items.forEach((i) => counts[i.state]++);
  const ordered = orderShopping(list?.items ?? [], tab, order);
  const groups = ordered.groups.map(
    (group) => [group.label, group.items] as const,
  );
  if (ordered.basket.length) groups.push(["In basket", ordered.basket]);
  return (
    <section className="shopping">
      <div className="shopping-heading">
        <div>
          <span className="eyebrow">From your meals to your basket</span>
          <h2>Shopping list.</h2>
          <p>Everything you need, a little less to remember.</p>
        </div>
      </div>
      <div className="shopping-week">
        <button
          className="button"
          aria-label="Previous shopping week"
          onClick={() => onWeek(addDays(week, -7))}
        >
          ←
        </button>
        <strong>
          {dateLabel(week)} – {dateLabel(addDays(week, 6))}
        </strong>
        <button
          className="button"
          aria-label="Next shopping week"
          onClick={() => onWeek(addDays(week, 7))}
        >
          →
        </button>
      </div>
      {tabOnly && !offline && (
        <p className="shopping-notice" role="status">
          This list stays in this tab. Keep it open until changes are saved.
        </p>
      )}
      {offline && (
        <p className="shopping-notice" role="status">
          {tabOnly
            ? "Offline · Keep this tab open until your changes are saved."
            : "Offline · Your saved shopping list is available on this device."}
        </p>
      )}
      {error && (
        <Alert>
          {error}{" "}
          <button
            className="button"
            onClick={
              blockedWeek && blockedWeek !== week
                ? () => onWeek(blockedWeek)
                : retry
            }
          >
            {blockedWeek && blockedWeek !== week
              ? `Open week of ${dateLabel(blockedWeek)}`
              : conflict
                ? recoveryKeepsRemoved
                  ? "Keep removed items as extras and retry"
                  : "Reviewed latest list · Retry my changes"
                : "Retry sync"}
          </button>
        </Alert>
      )}
      {notice && (
        <p className="shopping-notice" role="status">
          {notice}
        </p>
      )}
      {!list ? (
        <p role="status">
          {offline
            ? "Connect to load a saved list for this week."
            : "Loading your list…"}
        </p>
      ) : (
        <>
          <div className="shopping-layout">
            <aside className="shopping-meals panel">
              <button
                className="shopping-meal-toggle"
                aria-expanded={mealsOpen}
                onClick={() => setMealsOpen((v) => !v)}
              >
                <Icon name="meal" />
                Choose meals
                <span>
                  {list.meals.filter((m) => m.selected).length}/
                  {list.meals.length}
                </span>
              </button>
              {mealsOpen && (
                <div className="shopping-meal-options">
                  <p className="muted small">
                    Only selected meals contribute to this week’s list.
                  </p>
                  {list.meals.length === 0 && (
                    <p>
                      No saved meals for this week. You can still add household
                      extras.
                    </p>
                  )}
                  {list.meals.map((m) => (
                    <label key={m.position} className="shopping-meal">
                      <input
                        type="checkbox"
                        checked={m.selected}
                        disabled={offline || busy || pending > 0}
                        aria-label={`Include ${m.title} on ${dateLabel(m.date)}`}
                        onChange={(e) =>
                          void edit({
                            kind: "meal",
                            id: m.position,
                            selected: e.target.checked,
                          })
                        }
                      />
                      <span>
                        <small>{dateLabel(m.date)}</small>
                        <strong>{m.title.replace(/^\[TEST\]\s*/, "")}</strong>
                      </span>
                    </label>
                  ))}
                </div>
              )}
            </aside>
            <div className="shopping-list-body">
              <div className="shopping-progress">
                <span>
                  {counts.basket + counts.home} of {list.items.length} items
                  ready
                </span>
                <span role="status">
                  {savingOnDevice
                    ? "Saving on device…"
                    : pending
                      ? `Pending sync · ${pending} change${pending === 1 ? "" : "s"}`
                      : busy
                        ? "Checking…"
                        : "Saved"}
                </span>
                <progress
                  value={counts.basket + counts.home}
                  max={Math.max(list.items.length, 1)}
                  aria-label="Shopping progress"
                />
              </div>
              <div
                className="shopping-tabs"
                role="group"
                aria-label="Shopping views"
              >
                {(
                  [
                    ["buy", "To buy"],
                    ["home", "At home"],
                  ] as const
                ).map(([state, label]) => (
                  <button
                    key={state}
                    aria-pressed={tab === state}
                    onClick={() => setTab(state)}
                  >
                    {label} (
                    {state === "buy" ? counts.buy + counts.basket : counts.home}
                    )
                  </button>
                ))}
              </div>
              <label className="shopping-sort">
                Sort by
                <select
                  aria-label="Sort by"
                  value={order}
                  onChange={(event) =>
                    setOrder(event.target.value as ShoppingOrder)
                  }
                >
                  <option value="category">Category</option>
                  <option value="fresh">Fresh items first</option>
                  <option value="name">Name (A–Z)</option>
                  <option value="quantity">Quantity (high to low)</option>
                </select>
              </label>
              {groups.length === 0 && (
                <div className="shopping-empty panel">
                  <Icon name="leaf" />
                  <h3>
                    {tab === "buy"
                      ? "All set for this week."
                      : "Nothing marked at home."}
                  </h3>
                  <p>
                    {tab === "buy"
                      ? "Choose meals or add an extra below."
                      : "Move an item here from your list whenever you’re ready."}
                  </p>
                </div>
              )}
              {[...groups].map(([category, items]) => (
                <section
                  className="shopping-category"
                  key={category}
                  aria-label={
                    category === "In basket" ? "In basket" : undefined
                  }
                >
                  {category && (
                    <h3>
                      <Icon
                        name={category === "Household extras" ? "plus" : "leaf"}
                      />
                      {category}
                      <span>{items.length}</span>
                    </h3>
                  )}
                  <div className="panel shopping-rows">
                    {items.map((item) => (
                      <div
                        className={`shopping-row ${item.state}`}
                        key={item.id}
                      >
                        <input
                          type="checkbox"
                          checked={item.state === "basket"}
                          disabled={conflict}
                          aria-label={
                            item.state === "basket"
                              ? `Mark ${item.name} as needed`
                              : `Mark ${item.name} as bought`
                          }
                          onChange={() =>
                            void edit({
                              kind: "state",
                              id: item.id,
                              state: item.state === "basket" ? "buy" : "basket",
                            })
                          }
                        />
                        <div className="shopping-item-copy">
                          <strong>{item.name}</strong>
                          {item.updated && (
                            <span className="shopping-updated">Updated</span>
                          )}
                          {item.manual && <small>Household extra</small>}
                        </div>
                        <span className="shopping-quantity">
                          {item.quantity ?? "As needed"} {item.unit}
                        </span>
                        <button
                          className="shopping-icon-button"
                          disabled={conflict}
                          aria-label={
                            item.state === "home"
                              ? `Return ${item.name} from At home to buy`
                              : `Move ${item.name} to At home`
                          }
                          title={
                            item.state === "home" ? "Return to buy" : "At home"
                          }
                          onClick={() =>
                            void edit({
                              kind: "state",
                              id: item.id,
                              state: item.state === "home" ? "buy" : "home",
                            })
                          }
                        >
                          <Icon
                            name={item.state === "home" ? "arrow" : "home"}
                          />
                        </button>
                        {item.manual && (
                          <button
                            className="shopping-icon-button"
                            disabled={conflict}
                            aria-label={`Remove ${item.name}`}
                            onClick={() =>
                              void edit({ kind: "remove", id: item.id })
                            }
                          >
                            ×
                          </button>
                        )}
                      </div>
                    ))}
                  </div>
                </section>
              ))}
              <details className="shopping-add panel">
                <summary>
                  <Icon name="plus" />A few extras?
                </summary>
                <form onSubmit={addExtra}>
                  <p>For the things your recipes don’t cover.</p>
                  <label>
                    Extra item
                    <input
                      name="name"
                      required
                      maxLength={200}
                      placeholder="Dish soap, coffee, a treat…"
                    />
                  </label>
                  <div className="shopping-add-amount">
                    <label>
                      Amount
                      <input name="quantity" maxLength={100} placeholder="1" />
                    </label>
                    <label>
                      Unit
                      <input name="unit" maxLength={80} placeholder="bottle" />
                    </label>
                  </div>
                  <button className="button primary" disabled={conflict}>
                    Add extra
                  </button>
                </form>
              </details>
            </div>
          </div>
        </>
      )}
    </section>
  );
}

export function BackgroundShoppingSync({
  profile,
  week,
  onOpen,
}: {
  profile: Profile;
  week: string;
  onOpen: (week: string) => void;
}) {
  const { pending, error, blockedWeek } = useShopping(profile, week, false);
  if (!pending && !error) return null;
  return (
    <div className="shopping-notice" role="status">
      <p>
        {error ||
          `${pending} shopping change${pending === 1 ? "" : "s"} pending sync.`}
      </p>
      <button className="button" onClick={() => onOpen(blockedWeek ?? week)}>
        Open shopping list to review
      </button>
    </div>
  );
}
