import {
  useId,
  useImperativeHandle,
  useLayoutEffect,
  useRef,
  useState,
  type Ref,
} from "react";
import { Field } from "./components";

const bounded = (value: number, min: number, max: number) =>
  Math.max(min, Math.min(max, value));
const durationText = (minutes: number) => {
  const hours = Math.floor(minutes / 60),
    rest = minutes % 60;
  return hours ? `${hours} h${rest ? ` ${rest} min` : ""}` : `${rest} min`;
};
type WheelHandle = { selected: () => number };

function Wheel({
  label,
  value,
  min,
  max,
  onChange,
  ref,
}: {
  label: string;
  value: number;
  min: number;
  max: number;
  onChange: (value: number) => void;
  ref: Ref<WheelHandle>;
}) {
  const id = useId(),
    element = useRef<HTMLDivElement>(null),
    ready = useRef(false);
  const selected = bounded(value, min, max);
  const current = useRef(selected);
  current.current = selected;
  const rowHeight = () =>
    element.current
      ?.querySelector<HTMLElement>("[data-wheel-value]")
      ?.getBoundingClientRect().height ?? 0;
  const read = () => {
    const height = rowHeight();
    // A newly opened dialog needs its first layout before its scroll position
    // represents the existing value.
    return ready.current && height && element.current
      ? bounded(min + Math.round(element.current.scrollTop / height), min, max)
      : selected;
  };
  const position = (next: number) => {
    element.current?.scrollTo({
      top: (next - min) * rowHeight(),
      behavior: "instant",
    });
  };
  const choose = (next: number) => {
    next = bounded(next, min, max);
    onChange(next);
    position(next);
  };
  useImperativeHandle(ref, () => ({ selected: read }));
  useLayoutEffect(() => {
    ready.current = false;
    const frame = requestAnimationFrame(() => {
      position(current.current);
      ready.current = true;
    });
    return () => cancelAnimationFrame(frame);
  }, [min, max]);
  return (
    <div className="duration-column">
      <span className="duration-wheel-label" id={id}>
        {label}
      </span>
      <div className="duration-wheel-frame">
        <div
          ref={element}
          className="duration-wheel"
          role="spinbutton"
          tabIndex={0}
          aria-labelledby={id}
          aria-valuemin={min}
          aria-valuemax={max}
          aria-valuenow={selected}
          aria-valuetext={`${selected} ${label.toLowerCase()}`}
          onScroll={() => {
            if (!ready.current) return;
            const next = read();
            if (next !== selected) onChange(next);
          }}
          onKeyDown={(event) => {
            const changes: Record<string, number> = {
              ArrowUp: selected + 1,
              ArrowDown: selected - 1,
              PageUp: selected + 10,
              PageDown: selected - 10,
              Home: min,
              End: max,
            };
            if (!(event.key in changes)) return;
            event.preventDefault();
            choose(changes[event.key]);
          }}
        >
          {Array.from({ length: max - min + 1 }, (_, index) => min + index).map(
            (number) => (
              <div
                key={number}
                data-wheel-value={number}
                className={
                  number === selected
                    ? "duration-wheel-item selected"
                    : "duration-wheel-item"
                }
                aria-hidden="true"
              >
                {String(number).padStart(2, "0")}
              </div>
            ),
          )}
        </div>
      </div>
      <div className="duration-adjustments">
        <button
          type="button"
          className="duration-step"
          aria-label={`Decrease ${label.toLowerCase()}`}
          disabled={selected === min}
          onClick={() => choose(read() - 1)}
        >
          −
        </button>
        <button
          type="button"
          className="duration-step"
          aria-label={`Increase ${label.toLowerCase()}`}
          disabled={selected === max}
          onClick={() => choose(read() + 1)}
        >
          +
        </button>
      </div>
    </div>
  );
}

export function DurationPicker({
  label,
  field,
  value,
  minimum = 0,
  maximum,
  error,
  onChange,
}: {
  label: string;
  field: string;
  value: number;
  minimum?: number;
  maximum: number;
  error?: string;
  onChange: (value: number) => void;
}) {
  const id = useId(),
    dialog = useRef<HTMLDialogElement>(null),
    trigger = useRef<HTMLButtonElement>(null);
  const hoursWheel = useRef<WheelHandle>(null),
    minutesWheel = useRef<WheelHandle>(null);
  const [open, setOpen] = useState(false),
    [hours, setHours] = useState(0),
    [minutes, setMinutes] = useState(0);
  const maxHours = Math.floor(maximum / 60),
    maxMinutes = hours === maxHours ? maximum % 60 : 59;
  const minMinutes = hours === 0 ? minimum : 0;
  useLayoutEffect(() => {
    if (!open || !dialog.current) return;
    const top = window.scrollY,
      left = window.scrollX,
      body = document.body;
    const previous = {
      position: body.style.position,
      top: body.style.top,
      left: body.style.left,
      width: body.style.width,
      overflow: body.style.overflow,
    };
    Object.assign(body.style, {
      position: "fixed",
      top: `${-top}px`,
      left: `${-left}px`,
      width: "100%",
      overflow: "hidden",
    });
    const modal = dialog.current;
    modal.showModal();
    modal
      .querySelector<HTMLElement>("[role=spinbutton]")
      ?.focus({ preventScroll: true });
    return () => {
      modal.close();
      Object.assign(body.style, previous);
      window.scrollTo({ top, left, behavior: "instant" });
      if (trigger.current?.isConnected)
        trigger.current.focus({ preventScroll: true });
    };
  }, [open]);
  function done() {
    const chosenHours = hoursWheel.current?.selected() ?? hours;
    const chosenMinutes = minutesWheel.current?.selected() ?? minutes;
    const next = bounded(chosenHours * 60 + chosenMinutes, minimum, maximum);
    if (next !== value) onChange(next);
    setOpen(false);
  }
  return (
    <div className="duration-field">
      <Field label={label} hint={error}>
        <button
          ref={trigger}
          type="button"
          className="duration-trigger"
          aria-label={label}
          aria-describedby={`${id}-value`}
          aria-haspopup="dialog"
          aria-expanded={open}
          aria-invalid={!!error}
          data-personal-field={field}
          onClick={() => {
            const staged = bounded(value, minimum, maximum);
            setHours(Math.floor(staged / 60));
            setMinutes(staged % 60);
            setOpen(true);
          }}
        >
          <span id={`${id}-value`}>{durationText(value)}</span>
        </button>
      </Field>
      <dialog
        ref={dialog}
        className="duration-dialog"
        aria-labelledby={`${id}-title`}
        onCancel={(event) => {
          event.preventDefault();
          setOpen(false);
        }}
        onClick={(event) => {
          if (event.target !== event.currentTarget) return;
          const rect = event.currentTarget.getBoundingClientRect();
          if (
            event.clientX < rect.left ||
            event.clientX > rect.right ||
            event.clientY < rect.top ||
            event.clientY > rect.bottom
          )
            setOpen(false);
        }}
      >
        <h3 id={`${id}-title`}>{label}</h3>
        {open && (
          <div className="duration-columns">
            <Wheel
              ref={hoursWheel}
              label="Hours"
              value={hours}
              min={0}
              max={maxHours}
              onChange={(next) => {
                setHours(next);
                setMinutes((old) =>
                  bounded(
                    old,
                    next === 0 ? minimum : 0,
                    next === maxHours ? maximum % 60 : 59,
                  ),
                );
              }}
            />
            <Wheel
              ref={minutesWheel}
              label="Minutes"
              value={minutes}
              min={minMinutes}
              max={maxMinutes}
              onChange={setMinutes}
            />
          </div>
        )}
        <div className="duration-footer">
          <button
            type="button"
            className="button secondary"
            onClick={() => setOpen(false)}
          >
            Cancel
          </button>
          <button type="button" className="button primary" onClick={done}>
            Done
          </button>
        </div>
      </dialog>
    </div>
  );
}
