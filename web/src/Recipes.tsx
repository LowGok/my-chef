import { useEffect, useRef, type ReactNode } from "react";
import { FavoriteToggle } from "./Favorites";
import type { Recipe } from "./api";
import { Icon } from "./components";
import { dateLabel, today } from "./dates";
import { RecipeImage } from "./RecipeImage";
import { ImportSourceLink } from "./ImportReview";

export function RecipeCard({
  recipe,
  onOpen,
}: {
  recipe: Recipe;
  onOpen: () => void;
}) {
  const isToday = recipe.date === today();
  const weekday = new Date(`${recipe.date}T12:00:00Z`).toLocaleDateString(
    undefined,
    { weekday: "long", timeZone: "UTC" },
  );
  const titleRef = useRef<HTMLHeadingElement>(null);
  useEffect(() => {
    const title = titleRef.current;
    if (!title) return;
    const update = () => {
      title.classList.toggle(
        "recipe-title-enlarged",
        parseFloat(getComputedStyle(title).fontSize) > 16,
      );
    };
    const observer = new ResizeObserver(update);
    observer.observe(title);
    update();
    return () => observer.disconnect();
  }, []);
  return (
    <article
      className={`recipe-card${isToday ? " meal-today" : ""}`}
      aria-current={isToday ? "date" : undefined}
    >
      <div className="recipe-image">
        <RecipeImage
          url={recipe.imageUrl}
          fallback={
            <span
              className="food-placeholder recipe-plate"
              aria-hidden="true"
            />
          }
        />
      </div>
      <div className="recipe-card-body">
        <time className="recipe-schedule" dateTime={recipe.date}>
          <span className="recipe-weekday">{weekday}</span>
          <span className="recipe-date">{dateLabel(recipe.date)}</span>
        </time>
        <h3 ref={titleRef} title={recipe.title}>
          {recipe.title}
        </h3>
        {recipe.note?.trim() && (
          <p className="muted recipe-note">{recipe.note}</p>
        )}
      </div>
      <button
        className="recipe-open"
        aria-label={`Open ${recipe.title}`}
        onClick={onOpen}
      />
    </article>
  );
}

export function RecipeDetail({
  recipe,
  onBack,
  onFavorite,
  onReport,
  rating,
  copyAction,
  fromProfile = false,
}: {
  recipe: Recipe;
  onBack: () => void;
  onFavorite: (recipe: Recipe) => void;
  onReport: () => void;
  rating: ReactNode;
  copyAction?: ReactNode;
  fromProfile?: boolean;
}) {
  return (
    <>
      <div className="recipe-detail-actions">
        <button className="text-button back" onClick={onBack}>
          ← {fromProfile ? "Back to favorites" : "Back to your menu"}
        </button>
        <FavoriteToggle key={recipe.id} recipe={recipe} onChange={onFavorite} />
      </div>
      {copyAction}
      <div className="page-heading">
        <span className="eyebrow">{dateLabel(recipe.date)}</span>
        <h2>{recipe.title}</h2>
        <p>{recipe.note}</p>
        <ImportSourceLink source={recipe.source?.import} />
        <div className="detail-meta">
          <span>
            <Icon name="clock" />
            {recipe.totalMinutes != null
              ? `${recipe.totalMinutes} minutes`
              : (recipe.legacyRaw?.totaltime ?? "Timing unavailable")}
          </span>
          <span>{recipe.yield?.join(" · ")}</span>
          <span>{recipe.equipment}</span>
        </div>
      </div>
      {recipe.imageStatus === "unavailable" && (
        <p className="notice" role="status">
          Image unavailable. Your saved recipe is ready to cook.
        </p>
      )}
      <div className="detail-grid">
        <section className="panel instructions">
          <h3>Let’s make it</h3>
          {recipe.instructions?.length ? (
            <ol>
              {recipe.instructions.map((step, i) => (
                <li key={i}>
                  <span>{String(i + 1).padStart(2, "0")}</span>
                  <p>{step}</p>
                </li>
              ))}
            </ol>
          ) : (
            <p>
              {recipe.legacyRaw?.instructions ??
                "No instructions were saved for this recipe."}
            </p>
          )}
          {!!recipe.tips?.length && (
            <div className="tips">
              <h4>A note from MyChef</h4>
              {recipe.tips.map((tip, i) => (
                <p key={i}>{tip}</p>
              ))}
            </div>
          )}
        </section>
        <aside className="panel ingredients">
          <span className="eyebrow">On your counter</span>
          <h3>Ingredients</h3>
          <ul>
            {recipe.ingredients.map((i) => (
              <li key={i.id}>
                <span>{i.name}</span>
                <strong>
                  {[i.quantity, i.unit]
                    .filter((v) => v != null && v !== "")
                    .join(" ") || "To taste"}
                </strong>
              </li>
            ))}
          </ul>
          {recipe.equipment && (
            <>
              <h4>Equipment</h4>
              <p>{recipe.equipment}</p>
            </>
          )}
        </aside>
      </div>
      <div className="advisory">
        <strong>Before you cook</strong>
        <p>
          {recipe.allergenNote ??
            "Check ingredient labels and review this recipe before cooking."}
        </p>
        {!!recipe.allergens?.length && (
          <p>Listed allergens: {recipe.allergens.join(", ")}</p>
        )}
        <p>
          {recipe.source?.kind === "personal"
            ? "User-entered information. Check labels and cross-contamination before cooking."
            : "AI suggestions cannot guarantee allergy safety."}
        </p>
      </div>
      <div className="recipe-report">
        <button className="text-button" onClick={onReport}>
          Report an issue
        </button>
      </div>
      {rating}
    </>
  );
}
