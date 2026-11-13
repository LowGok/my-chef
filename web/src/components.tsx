import { cloneElement, useId, type ReactElement, type ReactNode } from "react";

const logo = new URL("./mychef-logo.png", import.meta.url)
  .href;

export function Icon({ name = "leaf" }: { name?: string }) {
  const paths: Record<string, ReactNode> = {
    heart: (
      <path d="M20.8 4.6a5.5 5.5 0 0 0-7.8 0L12 5.7l-1.1-1.1a5.5 5.5 0 0 0-7.8 7.8L12 21l8.8-8.6a5.5 5.5 0 0 0 0-7.8Z" />
    ),
    basket: (
      <>
        <path d="m4 9 2 11h12l2-11ZM8 9l4-6 4 6M9 12v5m6-5v5M3 9h18" />
      </>
    ),
    home: (
      <>
        <path d="m3 11 9-8 9 8M5 10v11h14V10M10 21v-7h4v7" />
      </>
    ),
    leaf: (
      <>
        <path d="M20 4C8 2 3 8 5 15c7 4 15-1 15-11Z" />
        <path d="m5 20 10-10" />
      </>
    ),
    book: (
      <>
        <path d="M4 4h6a3 3 0 0 1 3 3v14a4 4 0 0 0-4-3H4Z" />
        <path d="M13 7a3 3 0 0 1 3-3h4v14h-3a4 4 0 0 0-4 3" />
      </>
    ),
    meal: (
      <>
        <circle cx="12" cy="12" r="5" />
        <path d="M3 3v5c0 2 3 2 3 0V3M4.5 3v18M21 3c-3 3-3 7 0 8v10M21 3v8" />
      </>
    ),
    help: (
      <>
        <circle cx="12" cy="12" r="9" />
        <path d="M9 8a3 3 0 0 1 6 1c0 2-3 2-3 4m0 3v1" />
      </>
    ),
    info: (
      <>
        <circle cx="12" cy="12" r="9" />
        <path d="M12 11v6m0-10v1" />
      </>
    ),
    logout: (
      <>
        <path d="M10 3H4v18h6m3-15 6 6-6 6m-5-6h11" />
      </>
    ),
    person: (
      <>
        <circle cx="12" cy="8" r="4" />
        <path d="M4 21v-2a8 8 0 0 1 16 0v2" />
      </>
    ),
    clock: (
      <>
        <circle cx="12" cy="12" r="9" />
        <path d="M12 7v5l3 2" />
      </>
    ),
    arrow: <path d="M4 12h16m-6-6 6 6-6 6" />,
    calendar: (
      <>
        <rect x="3" y="5" width="18" height="16" rx="2" />
        <path d="M7 3v4m10-4v4M3 11h18m-12 4h.01M12 15h.01M16 15h.01" />
      </>
    ),
    wallet: (
      <>
        <path d="M20 8V5a2 2 0 0 0-2-2L5 6a3 3 0 0 0-2 3v10a2 2 0 0 0 2 2h15V8H6a2 2 0 0 1-1-4" />
        <path d="M20 12h-5v5h5m-2-2.5h.01" />
      </>
    ),
    plus: <path d="M12 5v14M5 12h14" />,
    chef: (
      <>
        <path d="M7 12a5 5 0 1 1 1-9 5 5 0 0 1 8 0 5 5 0 1 1 1 9v8H7Z" />
        <path d="M7 16h10" />
      </>
    ),
  };
  return (
    <svg
      width="22"
      height="22"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.65"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {paths[name] ?? paths.leaf}
    </svg>
  );
}
export function Brand({
  onHome,
  disabled = false,
}: {
  onHome?: () => void;
  disabled?: boolean;
}) {
  return (
    <h1 className="brand">
      {onHome ? (
        <button
          className="brand-home"
          aria-label="Go to Meals"
          disabled={disabled}
          onClick={onHome}
        >
          <img src={logo} alt="MyChef" width="1000" height="249" />
        </button>
      ) : (
        <img src={logo} alt="MyChef" width="1000" height="249" />
      )}
    </h1>
  );
}

export function Startup({ message }: { message?: string } = {}) {
  return (
    <div
      className="startup-screen"
      role="status"
      aria-label={message ?? "Loading MyChef"}
    >
      <Brand />
      <p>{message ?? "Your week, made easier."}</p>
      <span className="startup-dots" aria-hidden="true">
        <i />
        <i />
        <i />
      </span>
    </div>
  );
}
export function Field({
  label,
  children,
  hint,
}: {
  label: string;
  children: ReactElement<{ id?: string; "aria-describedby"?: string }>;
  hint?: string;
}) {
  const generatedID = useId();
  const id = children.props.id ?? generatedID;
  return (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      {cloneElement(children, {
        id,
        "aria-describedby":
          [children.props["aria-describedby"], hint ? `${id}-hint` : undefined]
            .filter(Boolean)
            .join(" ") || undefined,
      })}
      {hint && <small id={`${id}-hint`}>{hint}</small>}
    </div>
  );
}
export function Alert({ children }: { children: ReactNode }) {
  return (
    <div className="alert" role="alert">
      {children}
    </div>
  );
}
export function FoodPlaceholder() {
  return (
    <div className="food-placeholder" aria-hidden="true">
      <svg viewBox="0 0 240 150" fill="none">
        <ellipse cx="120" cy="120" rx="75" ry="10" fill="#d4dbca" />
        <path
          d="M55 66h130c-2 35-25 57-65 57S57 101 55 66Z"
          fill="#f9f6eb"
          stroke="#9daa8c"
          strokeWidth="2"
        />
        <ellipse
          cx="120"
          cy="66"
          rx="65"
          ry="22"
          fill="#dfe8ce"
          stroke="#9daa8c"
          strokeWidth="2"
        />
        <path d="M100 50c-6-23 14-32 22-33 8 19-1 32-22 33Z" fill="#447758" />
        <path d="M125 52c-1-18 15-24 26-22 2 18-7 25-26 22Z" fill="#71935c" />
        <circle cx="91" cy="68" r="11" fill="#e7a264" />
        <circle cx="139" cy="71" r="12" fill="#d7bd70" />
        <path
          d="m109 66 12 11 11-20"
          stroke="#f7f0d8"
          strokeWidth="7"
          strokeLinecap="round"
        />
      </svg>
    </div>
  );
}
