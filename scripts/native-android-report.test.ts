import { expect, test } from "bun:test";
import { safeJourneyError } from "./native-android-report";
import * as report from "./native-android-report";

const binding = {
  sourceCommit: "a".repeat(40),
  apiSha256: "b".repeat(64),
  bundleSha256: "c".repeat(64),
  artifactSha256: "d".repeat(64),
  fixtureConfigSha256: "e".repeat(64),
  contractVersion: 1,
};
const journeyChecks = {
  signup: [
    "disabledNotice",
    "disabledNoCapture",
    "assessedNotice",
    "offBeforeCompletion",
    "completedOffNoCapture",
  ],
  participation: [
    "existingOff",
    "explicitEnable",
    "committedCoreAction",
    "restarted",
    "exactlyOneEvent",
    "noClientAnalyticsIdentifiers",
  ],
  conflict: [
    "staleConflict",
    "currentState",
    "offlineOffUnconfirmed",
    "reconnectFenceAcknowledged",
  ],
  queue: [
    "originalRevision",
    "productEditCommitted",
    "noOldEpochEvent",
    "retryNoDuplicate",
    "relaunchNoDuplicate",
  ],
  lifecycle: [
    "activeCleanup",
    "isolatedBackupPending",
    "nativeSave",
    "nativeCancel",
    "ownExportOnly",
    "foreignExportRefused",
    "accountDeleted",
    "relaunchSessionCacheCleared",
  ],
  accessibility: [
    "platformScreenReader",
    "spokenStatesAndErrors",
    "focusOrder",
    "enlargedOSText",
    "textScalePropagated",
    "phoneWidths",
    "keyboardUnobstructed",
    "noClipping",
  ],
};
function completeProbe() {
  return {
    schemaVersion: 1,
    platform: "android",
    binding: { ...binding },
    checks: Object.fromEntries(
      Object.entries(journeyChecks).map(([group, names]) => [
        group,
        Object.fromEntries(names.map((name) => [name, true])),
      ]),
    ),
  };
}

test("full analytics probe requires all six measured journeys at the exact candidate", () => {
  expect(
    report.validateAnalyticsProbe(completeProbe(), "android", binding),
  ).toBe(true);
  for (const [group, names] of Object.entries(journeyChecks)) {
    for (const name of names) {
      const probe = completeProbe();
      probe.checks[group]![name] = false;
      expect(report.validateAnalyticsProbe(probe, "android", binding)).toBe(
        false,
      );
    }
    const missing = completeProbe();
    delete missing.checks[group];
    expect(report.validateAnalyticsProbe(missing, "android", binding)).toBe(
      false,
    );
  }
});

test("foreign, stale or invented probe evidence cannot produce a complete receipt", () => {
  for (const key of Object.keys(binding)) {
    const probe = completeProbe();
    (probe.binding as Record<string, unknown>)[key] =
      key === "contractVersion"
        ? 2
        : "f".repeat(key === "sourceCommit" ? 40 : 64);
    expect(report.validateAnalyticsProbe(probe, "android", binding)).toBe(
      false,
    );
  }
  expect(
    report.validateAnalyticsProbe(completeProbe(), "ios-simulator", binding),
  ).toBe(false);
  for (const payload of [
    null,
    [],
    { passed: true },
    { ...completeProbe(), schemaVersion: 2 },
    { ...completeProbe(), email: "fixture@example.test" },
    { ...completeProbe(), proof: "Bearer private" },
  ])
    expect(report.validateAnalyticsProbe(payload, "android", binding)).toBe(
      false,
    );
  const coerced = completeProbe();
  (coerced.checks.signup as Record<string, unknown>).disabledNoCapture = "true";
  expect(report.validateAnalyticsProbe(coerced, "android", binding)).toBe(
    false,
  );
  const invented = completeProbe();
  invented.checks.signup!.subject = true;
  expect(report.validateAnalyticsProbe(invented, "android", binding)).toBe(
    false,
  );
});

test("native journey diagnostics retain a useful error without private values", () => {
  const password = "A private synthetic password!";
  const error = new Error(
    `locator.fill: ${password} user@example.test Bearer ${"a".repeat(64)} https://localhost/app/reset-password#${"b".repeat(43)}\nSecret second line`,
  );
  error.stack =
    "Error\n at /home/runner/project/scripts/native-android-journeys.ts:95:51";
  const result = safeJourneyError(error, "native sign-in", [password]);
  expect(result).toContain("locator.fill:");
  expect(result).toContain("native-android-journeys.ts:95:51");
  for (const value of [
    password,
    "user@example.test",
    "a".repeat(64),
    "b".repeat(43),
    "Secret second line",
    "/home/runner",
  ])
    expect(result).not.toContain(value);
});

test("unexpected diagnostic objects never stringify private payloads", () => {
  const result = safeJourneyError(
    {
      password: "private",
      toString() {
        throw Error("do not serialize");
      },
    },
    "unsafe\nstage",
  );
  expect(result).toBe(
    "Installed Android journey failed at stage: unknown; Failure",
  );
});

test("Android crash summaries retain only fixed facts from the target process", () => {
  const raw = `FATAL EXCEPTION: main
Process: com.gkldevelopment.mychef.preview, PID: 123
java.lang.SecurityException: private@example.test Bearer ${"x".repeat(43)}
    at com.gkldevelopment.mychef.preview.ProtectedRefresh.write(ProtectedRefresh.java:48)
    at unknown.Customer.privateValue(Customer.java:99)
FATAL EXCEPTION: main
Process: another.app, PID: 456
java.lang.NullPointerException: private
    at com.getcapacitor.Bridge.call(Bridge.java:123)`;
  const result = report.androidCrashFacts(raw);
  expect(result.targetProcess).toBe(true);
  expect(result.exceptions).toEqual(["SecurityException"]);
  expect(result.frames).toEqual(["ProtectedRefresh.java:48"]);
  for (const secret of ["private", "@example", "Bearer", "Customer", "Bridge.java", "x".repeat(43)])
    expect(JSON.stringify(result)).not.toContain(secret);
  expect(report.androidCrashFacts("Process: another.app\njava.lang.SecurityException").targetProcess).toBe(false);
  expect(report.androidCrashFacts("x".repeat(131073)).targetProcess).toBe(false);
});
