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

/** Raw emulator crash logs stay private; only closed categories reach CI. */
export function androidCrashFacts(raw: string) {
  const empty = { targetProcess: false, javaCrash: false, nativeCrash: false,
    rendererFailure: false, exceptions: [] as string[], frames: [] as string[] };
  if (raw.length > 131072) return empty;
  const lines = raw.split(/\r?\n/);
  const target = /Process: com\.gkldevelopment\.mychef\.preview(?:,|$)|>>> com\.gkldevelopment\.mychef\.preview <<</;
  let index = -1;
  for (let i = 0; i < lines.length; i++) if (target.test(lines[i]!)) index = i;
  if (index < 0) return empty;
  let end = index + 1;
  while (end < lines.length && !/FATAL EXCEPTION:|\bProcess: |>>> .* <<</.test(lines[end]!)) end++;
  const block = lines.slice(Math.max(0, index - 1), end).join("\n");
  const known = ["SecurityException", "NullPointerException", "IllegalStateException",
    "FileUriExposedException", "RuntimeException", "UnsupportedOperationException",
    "OutOfMemoryError", "NoSuchMethodError", "UnsatisfiedLinkError"];
  const frames = [...block.matchAll(/\bat\s+(?:com\.gkldevelopment\.mychef\.(?:preview|testing)|com\.getcapacitor)\.[\w.$]+\(((?:ProtectedRefresh|ProtectedShopping|MyChefNativePlugin|MainActivity|NativeBoundary|NativeRuntime|NativeMediaFiles|NativeJourneyProbe|Bridge|BridgeWebViewClient|WebViewLocalServer|PluginCall|PluginHandle)\.java):([0-9]{1,5})\)/g)]
    .map(match => match[1] + ":" + match[2]);
  return { targetProcess: true, javaCrash: block.includes("FATAL EXCEPTION:"),
    nativeCrash: /signal [0-9]{1,2} \(SIG(?:ABRT|SEGV|BUS|ILL)\)/.test(block),
    rendererFailure: /[Rr]ender(?:er| process).*crash|lib(?:webviewchromium|monochrome)\.so/.test(block),
    exceptions: known.filter(name => new RegExp("\\b" + name + "\\b").test(block)),
    frames: [...new Set(frames)].slice(0, 8) };
}
