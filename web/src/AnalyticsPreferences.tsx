import { useEffect, useId, useState } from "react";
import {
  api,
  APIError,
  isStale,
  message,
  refreshAnalyticsPreference,
  saveAnalyticsPreference,
  type AnalyticsPreference,
} from "./api";
import type { components } from "./api.generated";
import { Alert } from "./components";
type Notice = components["schemas"]["AnalyticsNotice"];
export type SignupAnalyticsChoice = {
  analyticsEnabled: boolean;
  analyticsNoticeVersion: string;
};

export function SignupAnalytics({
  onChoice,
  initialEnabled,
  initialNoticeVersion,
}: {
  onChoice: (choice: SignupAnalyticsChoice) => void;
  initialEnabled?: boolean;
  initialNoticeVersion?: string;
}) {
  const id = useId(),
    [notice, setNotice] = useState<Notice>(),
    [enabled, setEnabled] = useState(false),
    [failed, setFailed] = useState(false);
  useEffect(() => {
    let active = true;
    api<Notice>("/analytics/notice")
      .then((value) => {
        if (!active) return;
        setNotice(value);
        const choice =
          value.available &&
          (initialEnabled === undefined
            ? value.defaultEnabled
            : initialEnabled && initialNoticeVersion === value.version);
        setEnabled(choice);
        onChoice({
          analyticsEnabled: choice,
          analyticsNoticeVersion: value.version,
        });
      })
      .catch(() => {
        if (active) {
          setFailed(true);
          onChoice({ analyticsEnabled: false, analyticsNoticeVersion: "" });
        }
      });
    return () => {
      active = false;
    };
  }, []);
  return (
    <fieldset className="analytics-control">
      <legend>Privacy</legend>
      <label className="analytics-switch">
        <input
          type="checkbox"
          checked={enabled}
          disabled={!notice?.available}
          aria-describedby={id}
          onChange={(e) => {
            const checked = e.currentTarget.checked;
            setEnabled(checked);
            onChoice({
              analyticsEnabled: checked,
              analyticsNoticeVersion: notice?.version ?? "",
            });
          }}
        />
        Help improve MyChef
      </label>
      <p id={id} className="small">
        {notice?.text ??
          "Optional records of completed core actions help us understand how MyChef is used."}
      </p>
      {notice?.available && (
        <p className="small">
          These limited product metrics use assessed legitimate interests. You
          can turn them off before creating your account and later in Profile.
        </p>
      )}
      {(!notice?.available || failed) && (
        <p className="small" role="status">
          Analytics is Off while unavailable. You can create your account
          normally.
        </p>
      )}
    </fieldset>
  );
}

export function AnalyticsPreferences({
  active,
  scope,
}: {
  active: boolean;
  scope: string;
}) {
  const id = useId(),
    [preference, setPreference] = useState<AnalyticsPreference>(),
    [busy, setBusy] = useState<boolean | null>(null),
    [error, setError] = useState("");
  async function load() {
    setError("");
    try {
      const p = await refreshAnalyticsPreference();
      if (p) setPreference(p);
      else setError("Sign in again to load your analytics setting.");
    } catch (e) {
      if (!isStale(e)) setError(message(e));
    }
  }
  useEffect(() => {
    setPreference(undefined);
    if (active) void load();
  }, [active, scope]);
  async function update(enabled: boolean) {
    if (!preference) return;
    setBusy(enabled);
    setError("");
    try {
      setPreference(await saveAnalyticsPreference(enabled, preference));
    } catch (e) {
      if (isStale(e)) return;
      if (e instanceof APIError && e.status === 409) {
        await load();
        setError(
          "Your setting changed elsewhere. Review the current setting and try again.",
        );
      } else
        setError(
          enabled
            ? message(e)
            : "Turning off was not confirmed. Analytics may still be On. Reconnect and retry.",
        );
    } finally {
      setBusy(null);
    }
  }
  const status =
    busy === false
      ? "Turning off…"
      : busy === true
        ? "Turning on…"
        : preference?.state === "review_required"
          ? "Review required. Collection is paused; retained analytics can be exported or erased."
          : preference?.enabled
            ? "On"
            : preference?.deletionStatus === "backup_pending"
              ? "Off. Active data is erased; isolated backup cleanup is pending for up to seven days."
              : preference?.deletionStatus === "pending"
                ? "Off. Active deletion is in progress."
                : "Off";
  return (
    <section className="analytics-control" aria-labelledby={id}>
      <h3 id={id}>Privacy and data</h3>
      {error && <Alert>{error}</Alert>}
      {preference ? (
        <>
          <label className="analytics-switch">
            <input
              type="checkbox"
              checked={preference.enabled}
              disabled={
                busy !== null || (!preference.available && !preference.enabled)
              }
              onChange={(e) => void update(e.currentTarget.checked)}
            />
            Help improve MyChef
          </label>
          <p role="status" aria-live="polite">
            {status}
          </p>
          <p className="small">
            Completed core actions are kept for 90 days; erasable monthly
            contributions for 13 UTC month buckets. Turning this off stops
            collection and reporting immediately and starts erasure. Isolated
            encrypted backups may take up to seven days to clear.
          </p>
          <p className="small">
            This setting covers core app actions. Account access and pricing
            stay the same.
          </p>
          {preference.state === "review_required" && (
            <button
              type="button"
              className="button secondary"
              disabled={busy !== null}
              onClick={() => void update(false)}
            >
              Turn off and erase retained analytics
            </button>
          )}
          {!preference.available && (
            <p className="small">
              Analytics is currently unavailable. Your account remains
              available.
            </p>
          )}
        </>
      ) : (
        <p role="status">Loading your analytics setting…</p>
      )}
      <button
        type="button"
        className="button secondary"
        disabled={busy !== null}
        onClick={() => void load()}
      >
        Refresh analytics setting
      </button>
    </section>
  );
}
