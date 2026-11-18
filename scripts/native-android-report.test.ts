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

test("native crash observations bind the current PID and retain only known categories", () => {
  const raw = `pid: 100, tid: 101, name: RenderThread >>> com.gkldevelopment.mychef.preview <<<
signal 11 (SIGSEGV), code 1
    #00 pc 123 /private/user/libprivate-canary.so
    #01 pc 234 /apex/com.android.runtime/lib64/bionic/libc.so
pid: 200, tid: 201, name: RenderThread >>> com.gkldevelopment.mychef.preview <<<
signal 6 (SIGABRT), code -1
Abort message: 'JNI DETECTED ERROR IN APPLICATION: private@example.test'
    #00 pc 456 /system/lib64/libandroid_runtime.so
    #01 pc 789 /system/lib64/libart.so`;
  const result = report.androidCrashFacts(raw, "200");
  expect(result.targetProcess).toBe(true);
  expect(result.signals).toEqual(["SIGABRT"]);
  expect(result.libraries).toEqual(["libart.so", "libandroid_runtime.so"]);
  expect(result.abortClasses).toEqual(["jni"]);
  expect(report.androidCrashFacts(raw, "300").targetProcess).toBe(false);
  for (const value of ["private", "@example", "libc.so", "100", "200", "456", "/system"])
    expect(JSON.stringify(result)).not.toContain(value);
});

test("exit observations ignore other app instances and all private details", () => {
  const raw = `ApplicationExitInfo #0:
  pid=100
  process=com.gkldevelopment.mychef.preview
  reason=1 (EXIT_SELF)
  status=0
ApplicationExitInfo #1:
  pid=200
  process=com.gkldevelopment.mychef.preview
  reason=5 (CRASH_NATIVE)
  status=6
  description=private@example.test`;
  expect(report.androidExitFacts(raw, "200")).toEqual({ currentProcess: true, reason: "5", status: "6" });
  expect(report.androidExitFacts(raw, "300")).toEqual({ currentProcess: false, reason: "unknown", status: "unknown" });
  expect(report.androidExitFacts(raw)).toEqual({ currentProcess: false, reason: "unknown", status: "unknown" });
});


test("native runtime crash categories bind the current thread without disclosing symbols", () => {
  const raw = `pid: 100, tid: 101, name: HeapTaskDaemon >>> com.gkldevelopment.mychef.preview <<<
    #00 pc 123 /apex/libart.so (art::gc::Heap::CollectGarbage+48)
pid: 200, tid: 201, name: Instr:com.gkl >>> com.gkldevelopment.mychef.preview <<<
signal 11 (SIGSEGV), code 1
11-17 21:44:00.123 200 201 F DEBUG   : #00 pc 456 /private/customer-canary/libart.so (art::Thread::DecodeJObject+77)
    #01 pc 789 /private/customer-canary/libprivate.so (secret_customer_symbol+99)
Abort message: private@example.test art::gc::Heap::CollectGarbage`;
  const result = report.androidCrashFacts(raw, "200");
  expect(result.threadRole).toBe("instrumentation");
  expect(result.nativeFrameCategories).toEqual(["jni-reference"]);
  for (const secret of ["customer", "private", "@example", "DecodeJObject", "200", "201", "456", "77", "Instr:"])
    expect(JSON.stringify(result)).not.toContain(secret);
  expect(report.androidCrashFacts(raw, "300").nativeFrameCategories).toEqual([]);
});

test("native runtime crash categories ignore unknown names and non-frame messages", () => {
  const raw = `pid: 200, tid: 201, name: private-canary >>> com.gkldevelopment.mychef.preview <<<
signal 11 (SIGSEGV), code 1
Abort message: art::jit::JitCompileTask private@example.test
    #00 pc abc /private/libunknown.so (unknown_customer_symbol+42)`;
  const result = report.androidCrashFacts(raw, "200");
  expect(result.threadRole).toBe("unknown");
  expect(result.nativeFrameCategories).toEqual([]);
  for (const secret of ["private", "customer", "@example", "JitCompileTask"])
    expect(JSON.stringify(result)).not.toContain(secret);
});

test("native crash frames distinguish framework entry families without exposing symbols", () => {
  const raw = `pid: 200, tid: 201, name: private-worker >>> com.gkldevelopment.mychef.preview <<<
signal 11 (SIGSEGV), code 1
    #00 pc abc /private/customer/libandroid_runtime.so (android::NativeInputEventReceiver::consumeEvents+77)
    #01 pc def /private/customer/libandroid_runtime.so (android_view_accessibility_AccessibilityNodeInfo+42)
    #02 pc abc /private/customer/libbinder.so (android::IPCThreadState::transact+33)
    #03 pc def /private/customer/libart.so (art::JNI::CallVoidMethod+12)
    #04 pc abc /private/customer/base.odex (com.gkldevelopment.mychef.testing.NativeJourneyProbe.activate+22)
    #05 pc def /private/customer/base.odex (com.gkldevelopment.mychef.preview.MyChefNativePlugin.clearShopping+99)`;
  const result = report.androidCrashFacts(raw, "200");
  expect(result.nativeFrameCategories).toEqual([
    "input-event", "accessibility", "binder", "jni-call", "test-probe", "app-bridge",
  ]);
  for (const secret of ["private", "customer", "consumeEvents", "transact", "CallVoidMethod",
                        "NativeJourneyProbe", "MyChefNativePlugin", "clearShopping", "200", "201"])
    expect(JSON.stringify(result)).not.toContain(secret);
  expect(report.androidCrashFacts(raw.replaceAll(/#[0-9]{2} pc/g, "message"), "200")
    .nativeFrameCategories).toEqual([]);
});
