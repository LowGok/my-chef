import { expect, test } from "bun:test";
import {
  hostedInputs,
  nativeBuildPlan,
  verifyCopiedNativeAssets,
} from "./mobile-hosted";
import { mkdtemp, writeFile, mkdir, rm } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import {
  compileInputs,
  compilePlan,
  sanitizeCompilerLog,
} from "./mobile-compile";

test("a clean checkout includes the empty Cordova module required by Android Gradle", () => {
  const result = Bun.spawnSync(
    [
      "git",
      "ls-files",
      "--error-unmatch",
      "mobile/android/capacitor-cordova-android-plugins/build.gradle",
      "mobile/android/capacitor-cordova-android-plugins/cordova.variables.gradle",
      "mobile/android/capacitor-cordova-android-plugins/src/main/AndroidManifest.xml",
    ],
    { cwd: resolve(import.meta.dir, ".."), stdout: "pipe", stderr: "pipe" },
  );
  expect(result.exitCode).toBe(0);
});

test("compile-only jobs accept only the owner's labelled public native PR", () => {
  const approvedPR = "2";
  const input = (event: unknown, platform: string, expected = approvedPR) =>
    compileInputs(event, platform, expected);
  const event = {
    action: "labeled",
    number: 2,
    sender: { login: "LowGok" },
    label: { name: "native-compile" },
    repository: { id: 1454481532, full_name: "LowGok/my-chef", private: false },
    pull_request: {
      head: {
        sha: "a".repeat(40),
        ref: "feat/native-mobile",
        repo: { id: 1454481532, full_name: "LowGok/my-chef" },
      },
      base: { ref: "main" },
    },
  };
  expect(input(event, "ios-simulator").commit).toBe("a".repeat(40));
  expect(input(event, "ios-device").commit).toBe("a".repeat(40));
  const installed = { ...event, label: { name: "native-installed" } };
  expect(input(installed, "android").commit).toBe("a".repeat(40));
  expect(input(installed, "ios-simulator").commit).toBe("a".repeat(40));
  expect(() => input(installed, "ios-device")).toThrow();
  for (const [platform, other] of [
    ["android", "ios-simulator"],
    ["ios-simulator", "android"],
  ]) {
    const retry = { ...event, label: { name: `native-installed-${platform}` } };
    expect(input(retry, platform).commit).toBe("a".repeat(40));
    expect(() => input(retry, other)).toThrow();
    expect(() => input(retry, "ios-device")).toThrow();
  }
  const archive = { ...event, label: { name: "native-iphone-archive" } };
  expect(() => input(archive, "ios-device")).toThrow();
  expect(() => input(archive, "ios-simulator")).toThrow();
  for (const changed of [
    { ...event, number: 3 },
    { ...event, repository: { ...event.repository, id: 1 } },
    { ...event, sender: { login: "someone-else" } },
    { ...event, repository: { ...event.repository, private: true } },
    { ...event, label: { name: "other" } },
    { ...event, action: "opened" },
    {
      ...event,
      pull_request: {
        ...event.pull_request,
        head: { ...event.pull_request.head, ref: "main" },
      },
    },
  ])
    expect(() => input(changed, "android")).toThrow();
  expect(() => input(event, "testflight")).toThrow();
  expect(() => input(event, "android", "")).toThrow();
});

test("reminder fixtures require their own owner-labelled public PR and cannot sign or archive", () => {
  const event = {
    action: "labeled", number: 12, sender: { login: "LowGok" },
    label: { name: "native-reminder-fixture" },
    repository: { id: 1454481532, full_name: "LowGok/my-chef", private: false },
    pull_request: {
      base: { ref: "main" },
      head: { sha: "c".repeat(40), ref: "feat/weekly-reminders",
        repo: { id: 1454481532, full_name: "LowGok/my-chef" } },
    },
  };
  for (const platform of ["android", "ios-simulator"]) {
    expect(compileInputs(event, platform, "2", "12")).toMatchObject({
      installed: true, commit: "c".repeat(40),
    });
  }
  expect(() => compileInputs(event, "ios-device", "2", "12")).toThrow();
  expect(() => compileInputs(event, "android", "12", "2")).toThrow();
  expect(() => compileInputs({ ...event, label: { name: "native-compile" } }, "android", "12", "12")).toThrow();
  expect(() => compileInputs({ ...event, sender: { login: "contributor" } }, "android", "2", "12")).toThrow();
});

test("compile-only plans build without executing tests, emulators or distribution", () => {
  const ios = compilePlan("ios-simulator", false);
  expect(ios).toContain("CODE_SIGNING_ALLOWED=NO");
  const device = compilePlan("ios-device", false);
  expect(device).toContain("iphoneos");
  expect(device).toContain("generic/platform=iOS");
  expect(device).toContain("CODE_SIGNING_ALLOWED=NO");
  const bootstrap = compilePlan("android", true);
  expect(bootstrap).toContain("--write-locks");
  expect(bootstrap).toContain("--write-verification-metadata");
  const locked = compilePlan("android", false);
  expect(locked).toContain("--dependency-verification=strict");
  expect(locked).not.toContain("--write-locks");
  expect([...ios, ...bootstrap, ...locked].join(" ")).not.toMatch(
    /testDebug|lintDebug|connectedAndroid|simctl|emulator|installDebug|exportArchive|upload|publish|allowProvisioningUpdates/,
  );
});

test("the installed simulator links fixture entitlements without enabling device signing", () => {
  const entitlements = "/tmp/mychef-sim/keychain.plist";
  expect(compilePlan("ios-simulator", false, entitlements)).toContain(
    `MYCHEF_SIMULATOR_LDFLAGS=-Wl,-sectcreate,__TEXT,__entitlements,${entitlements}`,
  );
  expect(compilePlan("ios-simulator", false, entitlements)).toContain(
    "CODE_SIGNING_ALLOWED=NO",
  );
  expect(() => compilePlan("ios-device", false, entitlements)).toThrow();
  expect(() => compilePlan("android", false, entitlements)).toThrow();
});

test("the next private candidate uses build 2 on every native platform", () => {
  for (const platform of ["ios-device", "ios-simulator"] as const) {
    const plan = compilePlan(platform, false);
    expect(plan).toContain("MARKETING_VERSION=0.1.0");
    expect(plan).toContain("CURRENT_PROJECT_VERSION=2");
    expect(plan).toContain("CODE_SIGNING_ALLOWED=NO");
  }
  const android = compilePlan("android", false);
  expect(android).toContain("-PmychefVersionName=0.1.0");
  expect(android).toContain("-PmychefVersionCode=2");
});

test("compiler diagnostics omit credentials, proofs and account paths", () => {
  const secret = "a".repeat(64);
  const value = sanitizeCompilerLog(
    `Swift error: missing argument\nAuthorization: Bearer ${secret}\n/app/reset-password#${"b".repeat(43)}\n/Users/runner/private-file\n`,
  );
  expect(value).toContain("Swift error: missing argument");
  expect(value).not.toContain(secret);
  expect(value).not.toContain("b".repeat(43));
  expect(value).not.toContain("/Users/runner");
});
test("hosted build input cannot select a public origin, another source or a store operation", () => {
  const env = {
    NATIVE_PLATFORM: "ios-simulator",
    NATIVE_COMMIT: "a".repeat(40),
    NATIVE_ORIGIN: "https://localhost:8977",
    NATIVE_BUILD_FACTS: "{}",
  };
  expect(() => hostedInputs(env)).toThrow();
  for (const change of [
    { NATIVE_COMMIT: "main" },
    { NATIVE_COMMIT: "a".repeat(40) + "; echo secret" },
    { NATIVE_ORIGIN: "https://gkldevelopment.com" },
    { NATIVE_PLATFORM: "testflight" },
  ])
    expect(() => hostedInputs({ ...env, ...change })).toThrow();
});
test("native compile plans disable iOS signing and never upload or invite testers", () => {
  const ios = nativeBuildPlan("ios-simulator", { version: "0.1.0", build: 7 });
  expect(ios).toContain("CODE_SIGNING_ALLOWED=NO");
  expect(ios).toContain("CURRENT_PROJECT_VERSION=7");
  expect(ios).toContain("-disableAutomaticPackageResolution");
  const android = nativeBuildPlan("android", { version: "0.1.0", build: 7 });
  expect(android).toContain("--dependency-verification=strict");
  expect(android).toContain("assembleDebug");
  expect([...ios, ...android].join(" ")).not.toMatch(
    /allowProvisioningUpdates|exportArchive|altool|fastlane|upload|publish|write-locks/,
  );
});
test("copied assets must match all reviewed bytes and contain only the two empty Cordova shims as extras", async () => {
  const root = await mkdtemp(join(tmpdir(), "mychef-native-copied-"));
  try {
    await mkdir(join(root, "assets"));
    await writeFile(join(root, "index.html"), "MyChef");
    await writeFile(join(root, "cordova.js"), "");
    await writeFile(join(root, "cordova_plugins.js"), "");
    const sha = Bun.CryptoHasher.hash("sha256", "MyChef", "hex");
    const inventory = [{ path: "index.html", sha256: sha, bytes: 6 }];
    await verifyCopiedNativeAssets(root, inventory);
    await writeFile(join(root, "cordova.js"), "remote code");
    await expect(verifyCopiedNativeAssets(root, inventory)).rejects.toThrow();
    await writeFile(join(root, "cordova.js"), "");
    await writeFile(join(root, "unreviewed.js"), "extra");
    await expect(verifyCopiedNativeAssets(root, inventory)).rejects.toThrow();
    await rm(join(root, "unreviewed.js"));
    await writeFile(join(root, "index.html"), "changed");
    await expect(verifyCopiedNativeAssets(root, inventory)).rejects.toThrow();
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
