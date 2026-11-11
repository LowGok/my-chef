import { useId, useState, type ReactNode } from "react";
import { Field } from "./components";

const allergenChoices = [
  ["Milk", "milk"],
  ["Eggs", "egg"],
  ["Gluten", "wheat"],
  ["Peanuts", "peanut"],
  ["Nuts", "nut"],
  ["Fish", "fish"],
  ["Shellfish", "shell"],
  ["Soy", "bean"],
  ["Sesame", "seed"],
  ["Celery", "celery"],
  ["Mustard", "mustard"],
  ["Sulphites", "bottle"],
  ["Lupin", "flower"],
  ["Molluscs", "shell"],
] as const;

function ChoiceIcon({ name }: { name: string }) {
  const drawings: Record<string, ReactNode> = {
    milk: (
      <>
        <path d="M8 3h8l-1 4 3 4v11H6V11l3-4Z" />
        <path d="M9 7h6M6 11h12M10 15h4v4h-4Z" />
      </>
    ),
    egg: <path d="M19 15c0 4-3 7-7 7s-7-3-7-7S9 2 12 2s7 9 7 13Z" />,
    wheat: (
      <>
        <path d="M12 22V3M12 11C5 11 5 6 5 6s7-1 7 5Zm0 5c-7 0-7-5-7-5s7-1 7 5Zm0-9c7 0 7-5 7-5s-7 0-7 5Zm0 6c7 0 7-5 7-5s-7 0-7 5Zm0 6c7 0 7-5 7-5s-7 0-7 5Z" />
      </>
    ),
    peanut: (
      <>
        <path d="M8 4c3-3 9 0 8 4-1 3-1 4 1 7 3 6-5 10-8 5-2-3-1-4-3-6-4-4-2-8 2-10Z" />
        <path d="m8 7 5 2m-6 2 5 2m-2 3 5 2" />
      </>
    ),
    nut: (
      <>
        <path d="M5 13c0-5 3-9 7-10 4 1 7 5 7 10s-3 9-7 9-7-4-7-9Z" />
        <path d="M12 3v19M5 13h14" />
      </>
    ),
    fish: (
      <>
        <path d="M19 12C13 3 6 5 3 12c3 7 10 9 16 0Zm0 0 3-5v10Z" />
        <circle cx="7" cy="11" r="1" />
      </>
    ),
    shell: (
      <>
        <path d="M12 22C8 21 2 15 2 9a4 4 0 0 1 5-4 5 5 0 0 1 10 0 4 4 0 0 1 5 4c0 6-6 12-10 13Z" />
        <path d="M12 22V5m0 17L6 7m6 15 6-15" />
      </>
    ),
    bean: (
      <>
        <path d="M7 21C-1 13 5 2 16 3c8 1 4 17-9 18Z" />
        <path d="M8 17c7 0 6-7 9-10" />
      </>
    ),
    seed: (
      <>
        <path d="M8 3c-6 6-4 11 0 11S14 9 8 3Zm9 7c-6 6-4 11 0 11s6-5 0-11Z" />
      </>
    ),
    celery: (
      <>
        <path d="m9 22-3-14m6 14V6m3 16 3-14M7 10C0 9 2 3 7 4c1-5 7-3 6 1 5-4 10 0 6 5" />
      </>
    ),
    mustard: (
      <>
        <path d="M9 3h6v4l3 5v10H6V12l3-5Z" />
        <path d="M6 14h12M9 18h6" />
      </>
    ),
    bottle: (
      <>
        <path d="M10 2h4v6l4 5v9H6v-9l4-5Z" />
        <path d="M6 15h12M10 5h4" />
      </>
    ),
    flower: (
      <>
        <path d="M12 22V11m0 7-5-3m5 0 5-3" />
        <path d="M12 11C3 14 1 5 7 5c-1-6 10-6 9 0 6 0 4 9-4 6Z" />
      </>
    ),
    adult: (
      <>
        <circle cx="12" cy="5" r="3" />
        <path d="M7 22v-7H4l3-6h10l3 6h-3v7M12 16v6" />
      </>
    ),
    child: (
      <>
        <circle cx="12" cy="8" r="3" />
        <path d="M8 22v-5H5l3-5h8l3 5h-3v5M12 18v4" />
      </>
    ),
  };
  return (
    <svg
      viewBox="0 0 24 24"
      width="32"
      height="32"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {drawings[name]}
    </svg>
  );
}

export function AllergenPicker({
  initial,
  form,
}: {
  initial: string[];
  form?: string;
}) {
  const [values, setValues] = useState(initial);
  const known = (value: string) =>
    allergenChoices.some(
      ([label]) => label.toLowerCase() === value.toLowerCase(),
    );
  const custom = values.filter((value) => !known(value));
  return (
    <fieldset className="allergen-picker">
      <legend>Allergens</legend>
      <div className="allergen-grid">
        {allergenChoices.map(([label, icon]) => {
          const selected = values.some(
            (value) => value.toLowerCase() === label.toLowerCase(),
          );
          return (
            <button
              key={label}
              type="button"
              className="choice-tile"
              aria-pressed={selected}
              onClick={() =>
                setValues((current) =>
                  selected
                    ? current.filter(
                        (value) => value.toLowerCase() !== label.toLowerCase(),
                      )
                    : [...current, label],
                )
              }
            >
              <ChoiceIcon name={icon} />
              <span>{label}</span>
              <span className="choice-check" aria-hidden="true">
                {selected ? "✓" : "+"}
              </span>
            </button>
          );
        })}
      </div>
      <p className="selection-feedback" role="status">
        {values.some((value) => value.trim())
          ? `${values.filter((value) => value.trim()).length} selected`
          : "Choose any that apply"}
      </p>
      <Field label="Other allergens">
        <textarea
          rows={2}
          placeholder="One per line"
          value={custom.join("\n")}
          onChange={(event) =>
            setValues([
              ...values.filter(known),
              ...event.target.value.split("\n"),
            ])
          }
        />
      </Field>
      <input
        type="hidden"
        name="allergens"
        form={form}
        value={values.join("\n")}
      />
    </fieldset>
  );
}

export function NumberStepper({
  label,
  name,
  initial,
  min,
  max,
  icon,
}: {
  label: string;
  name: string;
  initial: number;
  min: number;
  max: number;
  icon?: "adult" | "child";
}) {
  const id = useId();
  const [value, setValue] = useState(String(initial));
  const number = Number(value);
  return (
    <div className={`number-choice ${icon ? "household-choice" : ""}`}>
      {icon && (
        <div className="household-icon">
          <ChoiceIcon name={icon} />
        </div>
      )}
      <label htmlFor={id}>{label}</label>
      <div className="stepper">
        <button
          type="button"
          aria-label={`Decrease ${label.toLowerCase()}`}
          disabled={number <= min}
          onClick={() => setValue(String(Math.max(min, number - 1)))}
        >
          −
        </button>
        <input
          id={id}
          name={name}
          type="number"
          min={min}
          max={max}
          required
          value={value}
          onChange={(event) => setValue(event.target.value)}
        />
        <button
          type="button"
          aria-label={`Increase ${label.toLowerCase()}`}
          disabled={number >= max}
          onClick={() => setValue(String(Math.min(max, number + 1)))}
        >
          +
        </button>
      </div>
    </div>
  );
}
