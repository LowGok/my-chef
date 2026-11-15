// Dedicated unsigned verification entry point. It never fabricates PR events.
import { authorizedAnalyticsCI } from "./analytics-ci";
import { readFile, mkdir, copyFile, writeFile } from "node:fs/promises";
import { resolve, join } from "node:path";
import { createHash } from "node:crypto";
import { compilePlan, sanitizeCompilerLog } from "../../scripts/mobile-compile";
import {
  prepareMobileBundle,
  createMobileConfig,
  verifyMobileBundle,
} from "../../scripts/mobile-build";
import { recordNativeCandidate } from "../../scripts/mobile-delivery";
import { verifyCopiedNativeAssets } from "../../scripts/mobile-hosted";
const root = resolve(import.meta.dir, "../.."),
  mobile = join(root, "mobile"),
  generated = join(mobile, ".generated"),
  artifacts = join(generated, "artifacts");
const platform = process.env.NATIVE_PLATFORM;
const event = JSON.parse(
  await readFile(process.env.GITHUB_EVENT_PATH!, "utf8"),
);
if (!authorizedAnalyticsCI(event, process.env))
  throw Error("Analytics verification authorization refused");
const env = { ...process.env, CI: "1", CAPACITOR_TELEMETRY_DISABLED: "1" };
if (platform === "android") {
  env.JAVA_HOME = process.env.JAVA_HOME_21_X64;
  env.PATH = env.JAVA_HOME + "/bin:" + env.PATH;
} else env.DEVELOPER_DIR = "/Applications/Xcode_26.3.app/Contents/Developer";
function run(args: string[], cwd = root) {
  for (let attempt = 0; attempt < 3; attempt++) {
    const p = Bun.spawnSync(args, { cwd, env, stdout: "pipe", stderr: "pipe" });
    if (p.exitCode === 0) return p.stdout.toString().trim();
    const log = p.stdout.toString() + p.stderr.toString();
    if (args[0] === "./gradlew" && attempt < 2 &&
        /Received status code (429|502|503|504)|Read timed out|Connect timed out/i.test(log)) {
      console.log("Transient public dependency download failure; bounded retry");
      Bun.sleepSync(15_000 * (attempt + 1));
      continue;
    }
    throw Error("Unsigned analytics verification build failed: " + args[0] + "\n" + sanitizeCompilerLog(log).slice(-4000));
  }
  throw Error("Unsigned analytics build retries exhausted");
}
if (
  run(["git", "rev-parse", "HEAD"]) !== process.env.GITHUB_SHA ||
  run(["git", "status", "--porcelain", "--untracked-files=all"])
)
  throw Error("Candidate is not exact clean CI source");
run([
  "git",
  "merge-base",
  "--is-ancestor",
  "b9e9699755fa752a3816bd6f8172e35c65ed16f9",
  "HEAD",
]);
await mkdir(artifacts, { recursive: true });
run(["bun", "install", "--frozen-lockfile"], join(root, "web"));
run(["bun", "install", "--frozen-lockfile"], mobile);
run(["bun", "run", "build"], join(root, "web"));
await prepareMobileBundle(
  join(root, "web/dist/app"),
  generated,
  createMobileConfig("https://localhost:8977"),
  process.env.GITHUB_SHA!,
);
const candidate = await recordNativeCandidate(root, generated, {
  version: "0.1.0",
  build: 2,
});
run(
  [
    "bun",
    "--bun",
    "./node_modules/@capacitor/cli/bin/capacitor",
    "copy",
    platform === "android" ? "android" : "ios",
  ],
  mobile,
);
const bundle = await verifyMobileBundle(generated);
await verifyCopiedNativeAssets(
  join(
    mobile,
    platform === "android"
      ? "android/app/src/main/assets/public"
      : "ios/App/App/public",
  ),
  bundle.assets,
);
let entitlements: string | undefined;
if (platform === "ios-simulator") {
  entitlements = join(generated, "simulator-keychain.plist");
  const id = "MYCHEF0000.com.gkldevelopment.mychef.preview";
  await writeFile(
    entitlements,
    `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>application-identifier</key><string>${id}</string><key>keychain-access-groups</key><array><string>${id}</string></array></dict></plist>`,
  );
}
run(
  compilePlan(platform as "android" | "ios-simulator", false, entitlements),
  platform === "android" ? join(mobile, "android") : mobile,
);
if (platform === "android") {
  const report = run(
    [
      "./gradlew",
      "--no-daemon",
      "--console",
      "plain",
      "--dependency-verification=strict",
      ":app:signingReport",
    ],
    join(mobile, "android"),
  );
  const block = report
    .split(/^Variant: /m)
    .find((x) => x.startsWith("debug\n"));
  const store = block?.match(/^Store: (.+)$/m)?.[1];
  if (!store || !(await Bun.file(store).exists()))
    throw Error("Exact app debug signer unavailable");
  await writeFile(
    join(generated, "analytics-debug-keystore-path"),
    store + "\n",
    { mode: 0o600 },
  );
}
const artifact =
  platform === "android"
    ? "analytics-unsigned.apk"
    : "analytics-simulator.app.zip";
if (platform === "android")
  await copyFile(
    join(mobile, "android/app/build/outputs/apk/debug/app-debug.apk"),
    join(artifacts, artifact),
  );
else
  run([
    "/usr/bin/ditto",
    "-c",
    "-k",
    "--sequesterRsrc",
    "--keepParent",
    join(generated, "ios-build/Build/Products/Release-iphonesimulator/App.app"),
    join(artifacts, artifact),
  ]);
const bytes = await readFile(join(artifacts, artifact));
if (bytes.length > 64 * 1024 * 1024)
  throw Error("Artifact exceeds verification bound");
if (
  run(["git", "status", "--porcelain", "--untracked-files=all"]) ||
  JSON.stringify(
    await recordNativeCandidate(root, generated, {
      version: "0.1.0",
      build: 2,
    }),
  ) !== JSON.stringify(candidate)
)
  throw Error("Candidate changed during native build");
await writeFile(
  join(artifacts, "analytics-compilation.json"),
  JSON.stringify({
    candidate,
    platform,
    artifact,
    artifactSha256: createHash("sha256").update(bytes).digest("hex"),
    distribution: "not authorized",
  }) + "\n",
);
console.log(
  "Exact analytics candidate compiled for unsigned installed verification",
);
