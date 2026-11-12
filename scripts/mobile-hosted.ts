import { createHash } from "node:crypto";
import { appendFileSync } from "node:fs";
import {
  readFile,
  writeFile,
  mkdir,
  readdir,
  lstat,
  copyFile,
  rm,
} from "node:fs/promises";
import { join, resolve } from "node:path";
import {
  createMobileConfig,
  prepareMobileBundle,
  verifyMobileBundle,
} from "./mobile-build";
import {
  recordNativeCandidate,
  validateNativeDelivery,
} from "./mobile-delivery";
const failure = () => {
  throw new Error("Native build prerequisites are not verified.");
};
export function hostedInputs(env: Record<string, string | undefined>) {
  const platform = env.NATIVE_PLATFORM,
    commit = env.NATIVE_COMMIT,
    origin = env.NATIVE_ORIGIN;
  if (
    (platform !== "ios-simulator" && platform !== "android") ||
    !commit ||
    !/^[a-f0-9]{40}$/.test(commit) ||
    !origin
  )
    failure();
  createMobileConfig(origin!);
  const facts = JSON.parse(env.NATIVE_BUILD_FACTS ?? "{}");
  const approved = validateNativeDelivery(facts);
  if (
    approved.operation !== "unsigned-build" ||
    approved.sourceCommit !== commit
  )
    failure();
  return {
    platform: platform as "ios-simulator" | "android",
    commit: commit!,
    origin: origin!,
    facts,
  };
}
export function nativeBuildPlan(
  platform: "ios-simulator" | "android",
  version: { version: string; build: number },
) {
  if (
    !/^\d+\.\d+\.\d+$/.test(version.version) ||
    !Number.isSafeInteger(version.build) ||
    version.build < 1 ||
    version.build > 2147483647
  )
    failure();
  if (platform === "ios-simulator")
    return [
      "xcodebuild",
      "-project",
      "ios/App/App.xcodeproj",
      "-scheme",
      "App",
      "-configuration",
      "Release",
      "-sdk",
      "iphonesimulator",
      "-destination",
      "generic/platform=iOS Simulator",
      "-derivedDataPath",
      ".generated/ios-build",
      "-disableAutomaticPackageResolution",
      "-onlyUsePackageVersionsFromResolvedFile",
      "CODE_SIGNING_ALLOWED=NO",
      `MARKETING_VERSION=${version.version}`,
      `CURRENT_PROJECT_VERSION=${version.build}`,
      "build",
    ];
  if (platform === "android")
    return [
      "./gradlew",
      "--no-daemon",
      "--dependency-verification=strict",
      `-PmychefVersionName=${version.version}`,
      `-PmychefVersionCode=${version.build}`,
      "assembleDebug",
      "lintDebug",
      "testDebugUnitTest",
    ];
  return failure();
}
type Asset = { path: string; sha256: string; bytes: number };
async function inventory(root: string, prefix = ""): Promise<string[]> {
  const files: string[] = [];
  for (const entry of await readdir(join(root, prefix), {
    withFileTypes: true,
  })) {
    const path = prefix + entry.name;
    if (entry.isDirectory()) files.push(...(await inventory(root, path + "/")));
    else if (entry.isFile()) files.push(path);
    else failure();
  }
  return files.sort();
}
export async function verifyCopiedNativeAssets(root: string, assets: Asset[]) {
  const known = new Map(assets.map((asset) => [asset.path, asset]));
  const files = await inventory(root);
  for (const path of files) {
    const bytes = await readFile(join(root, path)),
      expected = known.get(path);
    if (!expected) {
      if (
        !["cordova.js", "cordova_plugins.js"].includes(path) ||
        bytes.length !== 0
      )
        failure();
      continue;
    }
    if (
      bytes.length !== expected.bytes ||
      createHash("sha256").update(bytes).digest("hex") !== expected.sha256
    )
      failure();
    known.delete(path);
  }
  if (known.size) failure();
}
async function hostedBuild() {
  // Validate explicit authority/capacity before any install or networked tool.
  const input = hostedInputs(process.env),
    root = resolve(import.meta.dir, ".."),
    mobile = join(root, "mobile");
  const allowed = [
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
  ];
  const env: Record<string, string> = { CI: "1" };
  for (const name of allowed)
    if (process.env[name]) env[name] = process.env[name]!;
  if (input.platform === "ios-simulator")
    env.DEVELOPER_DIR = "/Applications/Xcode_26.6.app/Contents/Developer";
  else {
    const java = process.env.JAVA_HOME_21_X64;
    if (!java) failure();
    env.JAVA_HOME = java!;
    env.PATH = join(java!, "bin") + ":" + env.PATH;
  }
  const privateLog = join(mobile, ".generated/native-build-private.log");
  await mkdir(join(mobile, ".generated"), { recursive: true });
  await writeFile(privateLog, "", { mode: 0o600 });
  let step = 0;
  function run(args: string[], cwd = root) {
    const result = Bun.spawnSync(args, {
      cwd,
      env,
      stdout: "pipe",
      stderr: "pipe",
    });
    // Preserve compiler diagnostics only on the private runner, outside the
    // allowlisted artifact directory. They never enter published app resources.
    appendFileSync(
      privateLog,
      `Step ${++step}: ${args[0]}
` +
        result.stdout.toString() +
        result.stderr.toString() +
        "\n",
      { mode: 0o600 },
    );
    if (result.exitCode)
      throw new Error(
        "A native build step failed; inspect mobile/.generated/native-build-private.log on the private runner.",
      );
    return result.stdout.toString() + result.stderr.toString();
  }
  if (
    run(["git", "rev-parse", "HEAD"]).trim() !== input.commit ||
    run(["git", "status", "--porcelain", "--untracked-files=all"]).trim()
  )
    failure();
  const pkg = JSON.parse(await readFile(join(mobile, "package.json"), "utf8"));
  if (pkg.packageManager !== `bun@${Bun.version}`) failure();
  const version = {
    version: pkg.version as string,
    build: input.facts.candidate.build as number,
  };
  if (version.version !== input.facts.candidate.version) failure();
  let tool: string;
  if (input.platform === "ios-simulator") {
    tool = run(["xcodebuild", "-version"]).trim();
    if (tool !== "Xcode 26.6\nBuild version 17F113") failure();
  } else {
    tool = run(["java", "-version"]).trim();
    if (!/version "21\.0\.13"/.test(tool)) failure();
  }
  run(["bun", "install", "--frozen-lockfile"], join(root, "web"));
  run(["bun", "install", "--frozen-lockfile"], mobile);
  run(["bun", "run", "build"], join(root, "web"));
  const destination = join(mobile, ".generated");
  await prepareMobileBundle(
    join(root, "web/dist/app"),
    destination,
    createMobileConfig(input.origin),
    input.commit,
  );
  const candidate = await recordNativeCandidate(root, destination, version);
  if (
    candidate.bundleSha256 !== input.facts.candidate.bundleSha256 ||
    candidate.dependenciesSha256 !== input.facts.candidate.dependenciesSha256
  )
    failure();
  validateNativeDelivery(input.facts);
  // copy, not sync/update: the reviewed native dependency graph is immutable.
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
  const assets = await verifyMobileBundle(destination);
  const publicRoot =
    input.platform === "android"
      ? "android/app/src/main/assets/public"
      : "ios/App/App/public";
  await verifyCopiedNativeAssets(join(mobile, publicRoot), assets.assets);
  const config = JSON.parse(
    await readFile(
      join(
        mobile,
        input.platform === "android"
          ? "android/app/src/main/assets/capacitor.config.json"
          : "ios/App/App/capacitor.config.json",
      ),
      "utf8",
    ),
  );
  const { packageClassList, ...copiedConfig } = config;
  if (
    (packageClassList !== undefined &&
      (!Array.isArray(packageClassList) || packageClassList.length !== 0)) ||
    JSON.stringify(copiedConfig) !== JSON.stringify(assets.config)
  )
    failure();
  if (input.platform === "android" && !candidate.nativeResolutionRecorded)
    throw new Error(
      "Review and commit the native dependency lockfiles and verification metadata before compiling a candidate.",
    );
  run(
    nativeBuildPlan(input.platform, version),
    input.platform === "android" ? join(mobile, "android") : mobile,
  );
  // A successful compiler does not establish emulator/device behavior.
  const artifacts = join(destination, "artifacts");
  await rm(artifacts, { recursive: true, force: true });
  await mkdir(artifacts, { recursive: true });
  let artifact: string, signature: string;
  if (input.platform === "android") {
    artifact = "mychef-preview.apk";
    await copyFile(
      join(mobile, "android/app/build/outputs/apk/debug/app-debug.apk"),
      join(artifacts, artifact),
    );
    const sdk = env.ANDROID_HOME ?? env.ANDROID_SDK_ROOT;
    if (!sdk) failure();
    signature = run([
      join(sdk!, "build-tools/36.0.0/apksigner"),
      "verify",
      "--print-certs",
      join(artifacts, artifact),
    ]);
    if (!signature.includes("Signer #1 certificate SHA-256 digest:")) failure();
    signature = signature
      .split("\n")
      .find((line) =>
        line.startsWith("Signer #1 certificate SHA-256 digest:"),
      )!;
  } else {
    artifact = "mychef-preview-simulator.app.zip";
    signature = "unsigned simulator build; no distribution identity";
    run([
      "/usr/bin/ditto",
      "-c",
      "-k",
      "--sequesterRsrc",
      "--keepParent",
      join(
        destination,
        "ios-build/Build/Products/Release-iphonesimulator/App.app",
      ),
      join(artifacts, artifact),
    ]);
  }
  const file = join(artifacts, artifact);
  const info = await lstat(file);
  if (
    !info.isFile() ||
    info.size + 65536 > input.facts.capacity.estimatedArtifactBytes
  )
    failure();
  const final = await recordNativeCandidate(root, destination, version);
  if (JSON.stringify(candidate) !== JSON.stringify(final)) failure();
  const evidence = {
    candidate: final,
    platform: input.platform,
    tool,
    runnerImage: process.env.ImageVersion ?? "unreported",
    artifact,
    artifactSha256: createHash("sha256")
      .update(await readFile(file))
      .digest("hex"),
    signature,
    nativeCompile: "passed",
    nativeDeviceChecks: "pending",
  };
  await writeFile(
    join(artifacts, "native-build-evidence.json"),
    JSON.stringify(evidence, null, 2) + "\n",
    { mode: 0o600 },
  );
  process.stdout.write(
    "Private native compilation finished. Device checks and distribution remain pending.\n",
  );
}
if (import.meta.main)
  void hostedBuild().catch(() => {
    process.stderr.write(
      "Native build stopped. No store upload or tester invitation was performed.\n",
    );
    process.exitCode = 1;
  });
