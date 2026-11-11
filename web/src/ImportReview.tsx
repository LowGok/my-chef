import type { ImportedDraft, ImportSource, PersonalContent } from "./api";
export type ImportSeed = { operationId: string; draft: ImportedDraft };
export function importReviewFields(content: PersonalContent): string[] {
  return [
    "title",
    "servings",
    "prepMinutes",
    "cookMinutes",
    "totalMinutes",
    "allergens",
    ...content.ingredients.map((_, i) => `ingredients.${i}`),
    ...content.instructions.map((_, i) => `instructions.${i}`),
  ];
}
export function issueReviewField(field: string): string {
  return /^(ingredients|instructions)\.\d+/.exec(field)?.[0] ?? field;
}
export function importReviewKeys(
  draft: ImportedDraft,
  content: PersonalContent,
): string[] {
  return [
    ...new Set([
      ...importReviewFields(content),
      ...draft.issues.map((issue) => issueReviewField(issue.field)),
    ]),
  ];
}
function label(field: string, content: PersonalContent): [string, string] {
  const ingredient = /^ingredients\.(\d+)$/.exec(field),
    step = /^instructions\.(\d+)$/.exec(field);
  if (ingredient) {
    const i = Number(ingredient[1]),
      item = content.ingredients[i];
    return [
      `Ingredient ${i + 1}`,
      item
        ? [item.quantity ?? "Quantity not specified", item.unit, item.name]
            .filter(Boolean)
            .join(" ")
        : "Check the corrected ingredient list.",
    ];
  }
  if (step) {
    const i = Number(step[1]);
    return [
      `Step ${i + 1}`,
      content.instructions[i] ?? "Check the corrected instructions.",
    ];
  }
  const values: Record<string, [string, string]> = {
    title: ["Title", content.title],
    description: ["Short description", content.description],
    equipment: ["Equipment", content.equipment],
    ingredients: [
      "Ingredient list",
      content.ingredients.map((item) => item.name).join(", "),
    ],
    instructions: ["Instructions", content.instructions.join(" ")],
    tips: ["Recipe notes", content.tips.join(" ")],
    allergenNote: ["Allergen note (unverified)", content.allergenNote],
    servings: ["Servings", `${content.servings} portions`],
    prepMinutes: ["Preparation time", `${content.prepMinutes} minutes`],
    cookMinutes: ["Cooking time", `${content.cookMinutes} minutes`],
    totalMinutes: ["Total time", `${content.totalMinutes} minutes`],
    allergens: [
      "Allergen declarations (unverified)",
      content.allergens.join(", ") ||
        "None declared; this does not mean allergen-free.",
    ],
  };
  return (
    values[field] ?? ["Recipe details", "Check the corrected recipe above."]
  );
}
export function ImportSourceLink({
  source,
}: {
  source: ImportSource | undefined;
}) {
  if (!source) return null;
  if (source.kind === "photos")
    return <p className="small muted">Imported from recipe pages.</p>;
  try {
    const url = new URL(source.url ?? "");
    if (url.protocol !== "https:" || url.username || url.password || url.hash)
      return null;
    return (
      <p className="import-source">
        <a
          href={url.href}
          target="_blank"
          rel="noopener noreferrer"
          referrerPolicy="no-referrer"
        >
          Original recipe
        </a>
        {source.title && <span className="muted"> · {source.title}</span>}
      </p>
    );
  } catch {
    return null;
  }
}
export function ImportReview({
  seed,
  content,
  checked,
  onChange,
  error,
}: {
  seed: ImportSeed;
  content: PersonalContent;
  checked: Set<string>;
  onChange: (value: Set<string>) => void;
  error?: string;
}) {
  const keys = importReviewKeys(seed.draft, content);
  return (
    <fieldset
      className="import-review"
      aria-label="Review imported recipe"
      data-personal-field="importReview"
      tabIndex={-1}
    >
      <legend>Review imported recipe</legend>
      <p>
        Check each part before saving. Correct anything unclear in the editor
        above. Allergen declarations remain unverified.
      </p>
      {error && <p className="alert">{error}</p>}
      <details className="import-issues">
        <summary>Imported details to check</summary>
        <ul>
          {seed.draft.issues.map((issue) => (
            <li key={issue.field}>
              <strong>
                {label(issueReviewField(issue.field), content)[0]}
              </strong>
              :{" "}
              {issue.reason === "unverified"
                ? "Unverified declaration"
                : issue.reason === "missing"
                  ? "Not specified in the source"
                  : issue.reason === "invalid"
                    ? "Needs correction"
                    : "Unclear in the source"}
              {issue.sourceText && (
                <span className="personal-multiline">
                  {" "}
                  — {issue.sourceText}
                </span>
              )}
            </li>
          ))}
        </ul>
      </details>
      <div className="import-checks">
        {keys.map((field) => {
          const [title, value] = label(field, content);
          return (
            <label key={field} className="import-check">
              <input
                type="checkbox"
                checked={checked.has(field)}
                onChange={(event) => {
                  const next = new Set(checked);
                  if (event.target.checked) next.add(field);
                  else next.delete(field);
                  onChange(next);
                }}
              />
              <span>
                <strong>{title}</strong>
                <span>{value}</span>
              </span>
            </label>
          );
        })}
      </div>
    </fieldset>
  );
}
