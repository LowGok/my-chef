import { useState, type FormEvent } from "react";
import { api, isStale, message, type PlanningStatus } from "./api";
import { Alert, Field } from "./components";

export type PlanningCalendar = PlanningStatus &
  Required<Pick<PlanningStatus, "today" | "firstWeek" | "lastWeek">>;

export function confirmedCalendar(
  status: PlanningStatus | null | undefined,
): PlanningCalendar | undefined {
  if (status?.zone && status.today && status.firstWeek && status.lastWeek)
    return status as PlanningCalendar;
}

export function PlanningZoneForm({
  zone,
  onSaved,
}: {
  zone: string;
  onSaved: (value: PlanningStatus) => void;
}) {
  const suggested = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);

  async function confirm(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy) return;
    setSaved(false);
    setError("");
    setBusy(true);
    const input = String(new FormData(event.currentTarget).get("zone")).trim();
    try {
      const result = await api<PlanningStatus>("/planning/zone", {
        method: "PUT",
        body: JSON.stringify({ zone: input }),
      });
      onSaved(result);
      setSaved(true);
    } catch (err) {
      if (!isStale(err)) setError(message(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={(event) => void confirm(event)}>
      <p className="muted">
        Your meal plan follows this time zone, even when you travel. Weeks start
        on Monday.
      </p>
      <Field label="Household time zone">
        <input
          key={zone}
          name="zone"
          defaultValue={zone || suggested || ""}
          placeholder="Europe/Brussels"
          autoCapitalize="none"
          autoComplete="off"
          spellCheck={false}
          required
          maxLength={100}
        />
      </Field>
      {error && <Alert>{error}</Alert>}
      {saved && <p role="status">Household time zone saved.</p>}
      <button className="button primary" type="submit" disabled={busy}>
        {busy ? "Saving…" : "Save time zone"}
      </button>
    </form>
  );
}
