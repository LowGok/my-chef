import { useEffect, useState, type FormEvent } from "react";
import { api, isStale } from "./api";
import {
  disableReminderDevice,
  enableReminderDevice,
  hasReminderBridge,
  reminderDeviceActive,
  showReminderOSSettings,
} from "./reminder-device";

type Preferences = {
  zone: string;
  current: boolean;
  advance: boolean;
  advanceDay: number;
  advanceMinute: number;
  premium: boolean;
  deviceEnabled: boolean;
};

const days = [
  "Sunday",
  "Monday",
  "Tuesday",
  "Wednesday",
  "Thursday",
  "Friday",
  "Saturday",
];
const path = "/planning-reminders";
const timeValue = (minutes: number) =>
  [Math.floor(minutes / 60), minutes % 60]
    .map((part) => String(part).padStart(2, "0"))
    .join(":");

export function PlanningReminders() {
  const [prefs, setPrefs] = useState<Preferences | null>(null);
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState("");
  const [needsSettings, setNeedsSettings] = useState(false);
  const [registering, setRegistering] = useState(reminderDeviceActive);
  const native = hasReminderBridge();

  useEffect(() => {
    let live = true;
    const refreshDevice = () => {
      setRegistering(reminderDeviceActive());
      void api<Preferences>(path)
        .then((value) => { if (live) setPrefs(value); })
        .catch(() => undefined);
    };
    refreshDevice();
    if (native) window.addEventListener("mychef-reminder-device", refreshDevice);
    return () => {
      live = false;
      window.removeEventListener("mychef-reminder-device", refreshDevice);
    };
  }, [native]);

  if (!prefs) return null;
  const value = prefs;

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy) return;
    const form = new FormData(event.currentTarget);
    const [hour, minute] = String(form.get("advanceTime") ?? "00:00")
      .split(":")
      .map(Number);
    const input = {
      zone: String(form.get("zone") ?? ""),
      current: form.get("current") === "on",
      advance: value.premium && form.get("advance") === "on",
      advanceDay: Number(form.get("advanceDay") ?? 0),
      advanceMinute: hour * 60 + minute,
    };
    setBusy(true);
    setStatus("");
    setNeedsSettings(false);
    try {
      const saved = await api<Preferences>(path, {
        method: "PUT",
        body: JSON.stringify(input),
      });
      setPrefs(saved);
      if (native && !saved.current && !saved.advance && reminderDeviceActive())
        await disableReminderDevice();
      setStatus("Reminder preferences saved.");
    } catch (err) {
      if (!isStale(err))
        setStatus(
          "Could not save reminder preferences. Check your time zone and try again.",
        );
    } finally {
      setBusy(false);
    }
  }

  async function changeDevice(enabled: boolean) {
    setBusy(true);
    setStatus("");
    setNeedsSettings(false);
    try {
      if (enabled) {
        const allowed = await enableReminderDevice(true);
        setNeedsSettings(!allowed);
        setStatus(
          allowed
            ? "Notifications allowed on this device."
            : "Notifications are off on this device.",
        );
      } else {
        await disableReminderDevice();
        setStatus("Reminders turned off on this device.");
      }
      setPrefs(await api<Preferences>(path));
    } catch (err) {
      if (!isStale(err))
        setStatus("Could not update reminders on this device.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <details className="account-details planning-reminders">
      <summary>Planning reminders</summary>
      <p>
        Get a push on Monday at 17:00 if you haven’t planned a meal for the
        week.
      </p>
      <form onSubmit={(event) => void save(event)}>
        <label>
          Time zone
          <input
            name="zone"
            required
            maxLength={100}
            defaultValue={
              prefs.zone ||
              Intl.DateTimeFormat().resolvedOptions().timeZone ||
              "UTC"
            }
          />
        </label>
        <label className="reminder-check">
          <input
            type="checkbox"
            name="current"
            defaultChecked={prefs.current}
          />
          Monday reminder
        </label>
        {prefs.premium && (
          <>
            <label className="reminder-check">
              <input
                type="checkbox"
                name="advance"
                defaultChecked={prefs.advance}
              />
              Reminder for next week
            </label>
            <label htmlFor="reminder-advance-day">
              Day
              <select id="reminder-advance-day" name="advanceDay" defaultValue={prefs.advanceDay}>
                {days.map((day, index) => (
                  <option key={day} value={index}>
                    {day}
                  </option>
                ))}
              </select>
            </label>
            <label>
              Time
              <input
                type="time"
                name="advanceTime"
                defaultValue={timeValue(prefs.advanceMinute)}
                required
              />
            </label>
          </>
        )}
        <button className="button primary" disabled={busy}>
          Save reminder preferences
        </button>
      </form>
      {native ? (
        <div className="reminder-device-actions">
          <p>
            {prefs.deviceEnabled
              ? "Reminders are on for this device."
              : registering ? "Connecting notifications on this device…"
              : "Save a reminder preference, then turn on notifications for this device. If your sign-in expired, turn them on again here."}
          </p>
          {prefs.deviceEnabled || registering ? (
            <button
              type="button"
              className="button secondary"
              disabled={busy}
              onClick={() => void changeDevice(false)}
            >
              Turn off on this device
            </button>
          ) : (
            <button
              type="button"
              className="button secondary"
              disabled={busy || (!prefs.current && !prefs.advance)}
              onClick={() => void changeDevice(true)}
            >
              Turn on for this device
            </button>
          )}
          {needsSettings && (
            <button
              type="button"
              className="button secondary"
              onClick={() => void showReminderOSSettings()}
            >
              Open notification settings
            </button>
          )}
        </div>
      ) : (
        <p>
          Turn on push notifications separately on each device in the MyChef
          app.
        </p>
      )}
      <p role="status" aria-live="polite">
        {status}
      </p>
    </details>
  );
}
