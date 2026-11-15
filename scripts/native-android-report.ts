import contract from "../mobile/tests/analytics-contract.json";

export type AnalyticsBinding = {
  sourceCommit: string;
  apiSha256: string;
  bundleSha256: string;
  artifactSha256: string;
  fixtureConfigSha256: string;
  contractVersion: number;
};
const bindingKeys = [
  "sourceCommit",
  "apiSha256",
  "bundleSha256",
  "artifactSha256",
  "fixtureConfigSha256",
  "contractVersion",
];
function record(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}
function keys(value: Record<string, unknown>, names: string[]) {
  return (
    Object.keys(value).length === names.length &&
    names.every((name) => Object.hasOwn(value, name))
  );
}
/** Validate bounded observations against identities measured by the runner.
 * This validates structure and bindings; it does not authenticate JSON or prove
 * a device run. Only the guarded runner may issue the retained receipt.
 */
export function validateAnalyticsProbe(
  value: unknown,
  platform: string,
  expected: AnalyticsBinding,
): boolean {
  if (
    !record(value) ||
    !keys(value, ["schemaVersion", "platform", "binding", "checks"]) ||
    value.schemaVersion !== contract.schemaVersion ||
    value.platform !== platform ||
    !["android", "ios-simulator"].includes(platform) ||
    !record(value.binding) ||
    !keys(value.binding, bindingKeys) ||
    !record(expected) ||
    !keys(expected, bindingKeys)
  )
    return false;
  for (const key of bindingKeys) {
    const actual = value.binding[key];
    if (actual !== expected[key as keyof AnalyticsBinding]) return false;
    if (key === "contractVersion") {
      if (actual !== contract.contractVersion) return false;
    } else if (
      typeof actual !== "string" ||
      !(key === "sourceCommit" ? /^[a-f0-9]{40}$/ : /^[a-f0-9]{64}$/).test(
        actual,
      )
    )
      return false;
  }
  if (
    !record(value.checks) ||
    !keys(value.checks, Object.keys(contract.checks))
  )
    return false;
  for (const [group, names] of Object.entries(contract.checks)) {
    const observed = value.checks[group];
    if (
      !record(observed) ||
      !keys(observed, names) ||
      !names.every((name) => observed[name] === true)
    )
      return false;
  }
  return true;
}

/** Keep fixed phase and protocol errors, never credentials, addresses or bodies. */
export function safeJourneyError(
  error: unknown,
  stage: string,
  secrets: string[] = [],
) {
  const phase = /^[a-z0-9 -]{1,100}$/i.test(stage) ? stage : "unknown";
  const kind =
    error instanceof Error && /^[A-Za-z]{1,40}$/.test(error.name)
      ? error.name
      : "Failure";
  let reason = error instanceof Error ? error.message.split(/\r?\n/)[0] : "";
  for (const secret of secrets)
    if (secret) reason = reason.split(secret).join("[private]");
  reason = reason
    .replace(/Bearer\s+\S+/gi, "[credential]")
    .replace(/https?:\/\/\S+/gi, "[url]")
    .replace(/[^\s<>"']+@[^\s<>"']+/g, "[address]")
    .replace(/[A-Za-z0-9_-]{43,}/g, "[opaque]")
    .slice(0, 350);
  const location =
    error instanceof Error
      ? error.stack?.match(/native-android-journeys\.ts:\d+:\d+/)?.[0]
      : undefined;
  return (
    "Installed Android journey failed at stage: " +
    phase +
    "; " +
    kind +
    (reason ? "; " + reason : "") +
    (location ? "; " + location : "")
  );
}
