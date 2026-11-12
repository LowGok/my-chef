import { createHash } from "node:crypto";
import { appendFileSync, readFileSync } from "node:fs";
import {
  copyFile,
  lstat,
  mkdir,
  readFile,
  readdir,
  writeFile,
} from "node:fs/promises";
import { join, resolve } from "node:path";
import {
  createMobileConfig,
  prepareMobileBundle,
  verifyMobileBundle,
} from "./mobile-build";
import { recordNativeCandidate } from "./mobile-delivery";
import { nativeBuildPlan, verifyCopiedNativeAssets } from "./mobile-hosted";

type Platform = "android" | "ios-simulator" | "ios-device";
// Build 1 is already installed through owner-only TestFlight.
const compilationVersion = { version: "0.1.0", build: 2 } as const;
const xcode = {
  "ios-simulator": { version: "26.3", build: "17C529" },
  "ios-device": { version: "26.6", build: "17F113" },
} as const;
const stop = () => {
  throw new Error("Compile-only authorization or source identity is invalid.");
};
export function compileInputs(value: unknown, platform: string | undefined, approvedPR: string | undefined) {
  const event = value as {
    action?: string;
    number?: number;
    sender?: { login?: string };
    label?: { name?: string };
    repository?: { id?: number; full_name?: string; private?: boolean };
    pull_request?: {
      base?: { ref?: string };
      head?: { sha?: string; ref?: string; repo?: { id?: number; full_name?: string } };
    };
  };
  const head = event?.pull_request?.head;
  const label = event?.label?.name;
  if (
    event?.action !== "labeled" ||
    !/^[1-9][0-9]*$/.test(approvedPR ?? "") ||
    String(event.number) !== approvedPR ||
    event.sender?.login !== "LowGok" ||
    (label !== "native-compile" &&
      !(label === "native-installed" && platform !== "ios-device") &&
      !(label === "native-installed-android" && platform === "android") &&
      !(
        label === "native-installed-ios-simulator" &&
        platform === "ios-simulator"
      )) ||
    event.repository?.full_name !== "LowGok/my-chef" ||
    event.repository.id !== 1454481532 ||
    event.repository.private !== false ||
    event.pull_request?.base?.ref !== "main" ||
    head?.repo?.full_name !== "LowGok/my-chef" ||
    head.repo.id !== 1454481532 ||
    head.ref !== "feat/native-mobile" ||
    !/^[a-f0-9]{40}$/.test(head.sha ?? "") ||
    !["android", "ios-simulator", "ios-device"].includes(platform ?? "")
  )
    stop();
  return {
    platform: platform as Platform,
    commit: head!.sha!,
    installed:
      label === "native-installed" || label === `native-installed-${platform}`,
  };
}
export function compilePlan(
  platform: Platform,
  bootstrap: boolean,
  simulatorEntitlements?: string,
) {
  if (
    simulatorEntitlements &&
    (platform !== "ios-simulator" ||
      !/^\/[A-Za-z0-9/_.-]+\.plist$/.test(simulatorEntitlements))
  )
    stop();
  if (platform === "ios-device")
    return nativeBuildPlan("ios-simulator", compilationVersion).map((value) =>
      value === "iphonesimulator"
        ? "iphoneos"
        : value === "generic/platform=iOS Simulator"
          ? "generic/platform=iOS"
          : value === ".generated/ios-build"
            ? ".generated/ios-device-build"
            : value,
    );
  if (platform === "ios-simulator") {
    const plan = nativeBuildPlan(platform, compilationVersion);
    if (simulatorEntitlements)
      plan.splice(
        -1,
        0,
        `MYCHEF_SIMULATOR_LDFLAGS=-Wl,-sectcreate,__TEXT,__entitlements,${simulatorEntitlements}`,
      );
    return plan;
  }
  if (platform !== "android") return stop();
  return [
    "./gradlew",
    "--no-daemon",
    `-PmychefVersionName=${compilationVersion.version}`,
    `-PmychefVersionCode=${compilationVersion.build}`,
    ...(bootstrap
      ? ["--write-locks", "--write-verification-metadata", "sha256"]
      : ["--dependency-verification=strict"]),
    "assembleDebug",
    "buildEnvironment",
  ];
}
export function sanitizeCompilerLog(value: string) {
  return value
    .replace(/\x1b\[[0-9;]*m/g, "")
    .split("\n")
    .map((line) =>
      /(?:Bearer\s+|(?:accessToken|refreshToken|csrfToken|password|api[_-]?key|secret)\s*["':=]|\/app\/(?:signup|verify-email|change-email|reset-password)#|(?:re|sk)_[A-Za-z0-9]{10})/i.test(
        line,
      )
        ? "[credential-like diagnostic omitted]"
        : line.replace(/\/(?:Users|home)\/[^/\s]+/g, "[runner]"),
    )
    .join("\n")
    .slice(-65536);
}

async function compile() {
  if (
    process.env.GITHUB_EVENT_NAME !== "pull_request" ||
    process.env.GITHUB_REPOSITORY !== "LowGok/my-chef"
  )
    stop();
  const input = compileInputs(
    JSON.parse(readFileSync(process.env.GITHUB_EVENT_PATH!, "utf8")),
    process.env.NATIVE_PLATFORM,
    process.env.NATIVE_VERIFICATION_PR,
  );
  const root = resolve(import.meta.dir, ".."),
    mobile = join(root, "mobile"),
    generated = join(mobile, ".generated"),
    artifacts = join(generated, "artifacts");
  const env: Record<string, string> = {
    CI: "1",
    CAPACITOR_TELEMETRY_DISABLED: "1",
  };
  for (const key of [
    "PATH",
    "HOME",
    "USER",
    "LOGNAME",
    "LANG",
    "LC_ALL",
    "TZ",
    "TMPDIR",
    "TEMP",
    "ANDROID_HOME",
    "ANDROID_SDK_ROOT",
  ])
    if (process.env[key]) env[key] = process.env[key]!;
  if (input.platform !== "android")
    env.DEVELOPER_DIR = `/Applications/Xcode_${xcode[input.platform].version}.app/Contents/Developer`;
  else {
    if (!process.env.JAVA_HOME_21_X64) stop();
    env.JAVA_HOME = process.env.JAVA_HOME_21_X64;
    env.PATH = join(env.JAVA_HOME, "bin") + ":" + env.PATH;
  }
  await mkdir(artifacts, { recursive: true });
  const log = join(generated, "native-compile-private.log");
  await writeFile(log, "", { mode: 0o600 });
  function run(args: string[], cwd = root) {
    const result = Bun.spawnSync(args, {
      cwd,
      env,
      stdout: "pipe",
      stderr: "pipe",
    });
    const output = result.stdout.toString() + result.stderr.toString();
    appendFileSync(log, `\n${args[0]}\n${output}`, { mode: 0o600 });
    if (result.exitCode) throw new Error("Native compiler command failed.");
    return output.trim();
  }
  let bootstrap = false;
  async function retainDependencies() {
    const paths = [
      "android/buildscript-gradle.lockfile",
      "android/gradle/verification-metadata.xml",
    ];
    try {
      paths.push(
        ...(await readdir(join(mobile, "android/dependency-locks")))
          .filter((name) => name.endsWith(".lockfile"))
          .map((name) => "android/dependency-locks/" + name),
      );
    } catch {}
    for (const path of paths) {
      try {
        if (!(await lstat(join(mobile, path))).isFile()) continue;
        const target = join(artifacts, "dependencies", path);
        await mkdir(resolve(target, ".."), { recursive: true });
        await copyFile(join(mobile, path), target);
      } catch (error) {
        if ((error as { code?: string }).code !== "ENOENT") throw error;
      }
    }
  }
  try {
    if (
      run(["git", "rev-parse", "HEAD"]) !== input.commit ||
      run(["git", "status", "--porcelain", "--untracked-files=all"])
    )
      stop();
    const tool =
      input.platform !== "android"
        ? run(["xcodebuild", "-version"])
        : run(["java", "-version"]);
    if (
      input.platform !== "android"
        ? tool !== `Xcode ${xcode[input.platform].version}\nBuild version ${xcode[input.platform].build}`
        : !/version "21\.0\.13"/.test(tool)
    )
      stop();
    const pkg = JSON.parse(
      await readFile(join(mobile, "package.json"), "utf8"),
    );
    if (
      pkg.packageManager !== `bun@${Bun.version}` ||
      pkg.version !== compilationVersion.version
    )
      stop();
    run(["bun", "install", "--frozen-lockfile"], join(root, "web"));
    run(["bun", "install", "--frozen-lockfile"], mobile);
    run(["bun", "run", "build"], join(root, "web"));
    await prepareMobileBundle(
      join(root, "web/dist/app"),
      generated,
      createMobileConfig("https://localhost:8977"),
      input.commit,
    );
    const candidate = await recordNativeCandidate(
      root,
      generated,
      compilationVersion,
    );
    run(
      [
        "bun",
        "--bun",
        "./node_modules/@capacitor/cli/bin/capacitor",
        "copy",
        input.platform === "android" ? "android" : "ios",
      ],
      mobile,
    );
    const bundle = await verifyMobileBundle(generated);
    await verifyCopiedNativeAssets(
      join(
        mobile,
        input.platform === "android"
          ? "android/app/src/main/assets/public"
          : "ios/App/App/public",
      ),
      bundle.assets,
    );
    bootstrap =
      input.platform === "android" && !candidate.nativeResolutionRecorded;
    let simulatorEntitlements: string | undefined;
    if (input.installed && input.platform === "ios-simulator") {
      simulatorEntitlements = join(generated, "simulator-keychain.plist");
      const identity = "MYCHEF0000.com.gkldevelopment.mychef.preview";
      await writeFile(
        simulatorEntitlements,
        `<?xml version="1.0" encoding="UTF-8"?>\n<plist version="1.0"><dict><key>application-identifier</key><string>${identity}</string><key>keychain-access-groups</key><array><string>${identity}</string></array></dict></plist>\n`,
      );
    }
    const plan = compilePlan(input.platform, bootstrap, simulatorEntitlements);
    run(plan, input.platform === "android" ? join(mobile, "android") : mobile);
    if (input.platform === "android") await retainDependencies();
    const artifact =
      input.platform === "android"
        ? "mychef-compile.apk"
        : input.platform === "ios-device"
          ? "mychef-compile-device.app.zip"
          : "mychef-compile-simulator.app.zip";
    if (input.platform === "android")
      await copyFile(
        join(mobile, "android/app/build/outputs/apk/debug/app-debug.apk"),
        join(artifacts, artifact),
      );
    else {
      run([
        "/usr/bin/ditto",
        "-c",
        "-k",
        "--sequesterRsrc",
        "--keepParent",
        join(
          generated,
          input.platform === "ios-device"
            ? "ios-device-build/Build/Products/Release-iphoneos/App.app"
            : "ios-build/Build/Products/Release-iphonesimulator/App.app",
        ),
        join(artifacts, artifact),
      ]);
    }
    const bytes = await readFile(join(artifacts, artifact));
    if (bytes.length > 64 * 1024 * 1024)
      throw new Error("Compile artifact exceeds the storage bound.");
    if (!bootstrap) {
      const final = await recordNativeCandidate(
        root,
        generated,
        compilationVersion,
      );
      if (JSON.stringify(final) !== JSON.stringify(candidate)) stop();
    }
    await writeFile(
      join(artifacts, "compilation.json"),
      JSON.stringify(
        {
          candidate,
          platform: input.platform,
          tool,
          runnerImage: process.env.ImageVersion,
          compilation: "passed",
          dependencyVerification: bootstrap
            ? "bootstrap; graph requires review and a strict rebuild"
            : "locked",
          tests: "not run",
          distribution: "not authorized",
          artifact,
          artifactSha256: createHash("sha256").update(bytes).digest("hex"),
        },
        null,
        2,
      ) + "\n",
      { mode: 0o600 },
    );
    console.log(
      "Native compilation passed. Device tests and distribution were not run.",
    );
  } finally {
    if (bootstrap) await retainDependencies();
    const diagnostic = sanitizeCompilerLog(await readFile(log, "utf8"));
    await writeFile(join(artifacts, "compiler.log"), diagnostic, {
      mode: 0o600,
    });
    console.log(diagnostic.split("\n").slice(-45).join("\n"));
  }
}
if (import.meta.main)
  void compile().catch((error) => {
    console.error(
      error instanceof Error ? error.message : "Native compilation failed.",
    );
    process.exitCode = 1;
  });
