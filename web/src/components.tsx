import { cloneElement, useId, type ReactElement, type ReactNode } from "react";


export function Icon({ name = "item" }: { name?: string }) {
  return <span className={name === "plus" ? "icon icon-plus" : "icon"} aria-hidden="true" />;
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
          <span>MyChef</span>
        </button>
      ) : (
        <span>MyChef</span>
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
      <p>{message ?? "Saved recipes and shopping lists"}</p>
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
  return <div className="food-placeholder" aria-hidden="true">Recipe</div>;
}
