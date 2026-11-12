import { createHash } from "node:crypto";
import { readFile, lstat, readdir } from "node:fs/promises";
import { join, resolve } from "node:path";
import { verifyMobileBundle } from "./mobile-build";
const appId = "com.gkldevelopment.mychef.preview";
const hex40 = /^[a-f0-9]{40}$/,
  hex64 = /^[a-f0-9]{64}$/;
export type NativeCandidate = {
  sourceCommit: string;
  bundleSha256: string;
  dependenciesSha256: string;
  appId: string;
  version: string;
  build: number;
  environment: string;
  nativeResolutionVerified?: boolean;
  nativeResolutionRecorded?: boolean;
};
type Facts = {
  candidate: NativeCandidate;
  approval: {
    operation: string;
    sourceCommit: string;
    bundleSha256: string;
    artifactSha256?: string;
    expiresUnix: number;
  };
  capacity: {
    verifiedAtUnix: number;
    expiresUnix: number;
    includedComputeRemainingUSD: number;
    estimatedComputeUSD: number;
    artifactStorageRemainingBytes: number;
    estimatedArtifactBytes: number;
    paidOveragesDisabled: boolean;
    standardRunner: boolean;
  };
  apple?: {
    paidMembership: boolean;
    signingVerified: boolean;
    processing: string;
    internalOnly: boolean;
    bundleId: string;
    version: string;
    build: number;
    artifactSha256: string;
    groupId: string;
    expectedOwnerId: string;
    memberIds: string[];
    automaticDistribution: boolean;
    externalGroups: number;
    publicLinkEnabled: boolean;
  };
};
const fail = () => {
  throw new Error("Native delivery prerequisites are not verified.");
};
function identity(value: NativeCandidate) {
  if (
    !value ||
    !hex40.test(value.sourceCommit) ||
    !hex64.test(value.bundleSha256) ||
    !hex64.test(value.dependenciesSha256) ||
    value.appId !== appId ||
    value.environment !== "preview" ||
    !/^\d+\.\d+\.\d+$/.test(value.version) ||
    !Number.isSafeInteger(value.build) ||
    value.build < 1 ||
    value.build > 2147483647
  )
    fail();
}
// This validates facts collected by the operator. It does not establish billing,
// Apple membership or human permission, and performs no upload/account mutation.
export function validateNativeDelivery(
  input: unknown,
  now = Math.floor(Date.now() / 1000),
) {
  try {
    const { candidate: c, approval: a, capacity: b, apple } = input as Facts;
    identity(c);
    if (
      !a ||
      !b ||
      !["unsigned-build", "owner-testflight"].includes(a.operation) ||
      a.sourceCommit !== c.sourceCommit ||
      a.bundleSha256 !== c.bundleSha256 ||
      !Number.isFinite(a.expiresUnix) ||
      a.expiresUnix <= now ||
      a.expiresUnix > now + 86400
    )
      fail();
    if (
      b.paidOveragesDisabled !== true ||
      b.standardRunner !== true ||
      ![
        b.verifiedAtUnix,
        b.expiresUnix,
        b.includedComputeRemainingUSD,
        b.estimatedComputeUSD,
        b.artifactStorageRemainingBytes,
        b.estimatedArtifactBytes,
      ].every(Number.isFinite) ||
      b.verifiedAtUnix > now ||
      b.verifiedAtUnix < now - 300 ||
      b.expiresUnix <= now ||
      b.expiresUnix > b.verifiedAtUnix + 600 ||
      b.estimatedComputeUSD <= 0 ||
      b.includedComputeRemainingUSD < 2 * b.estimatedComputeUSD ||
      b.estimatedArtifactBytes <= 0 ||
      b.artifactStorageRemainingBytes < b.estimatedArtifactBytes
    )
      fail();
    if (a.operation === "owner-testflight") {
      if (
        c.nativeResolutionVerified !== true ||
        !hex64.test(a.artifactSha256 ?? "") ||
        !apple ||
        apple.paidMembership !== true ||
        apple.signingVerified !== true ||
        apple.processing !== "VALID" ||
        apple.internalOnly !== true ||
        apple.bundleId !== c.appId ||
        apple.version !== c.version ||
        apple.build !== c.build ||
        apple.artifactSha256 !== a.artifactSha256 ||
        !apple.groupId ||
        !apple.expectedOwnerId ||
        !Array.isArray(apple.memberIds) ||
        apple.memberIds.length !== 1 ||
        apple.memberIds[0] !== apple.expectedOwnerId ||
        apple.automaticDistribution !== false ||
        apple.externalGroups !== 0 ||
        apple.publicLinkEnabled !== false
      )
        fail();
    }
    return {
      operation: a.operation,
      sourceCommit: c.sourceCommit,
      bundleSha256: c.bundleSha256,
    };
  } catch {
    return fail();
  }
}
function git(root: string, ...args: string[]) {
  const result = Bun.spawnSync(["git", "-C", root, ...args], {
    env: { PATH: process.env.PATH, LANG: "C.UTF-8" },
  });
  if (result.exitCode) fail();
  return result.stdout.toString().trim();
}
const dependencyPaths = [
  "go.sum",
  "mobile/android/gradle/wrapper/gradle-wrapper.properties",
  "mobile/bun.lock",
  "mobile/ios/App/App.xcodeproj/project.xcworkspace/xcshareddata/swiftpm/Package.resolved",
  "web/bun.lock",
];
export async function recordNativeCandidate(
  root: string,
  destination: string,
  version: { version: string; build: number },
): Promise<NativeCandidate> {
  root = resolve(root);
  if (git(root, "status", "--porcelain", "--untracked-files=all")) fail();
  const sourceCommit = git(root, "rev-parse", "HEAD");
  const bundle = await verifyMobileBundle(destination);
  if (bundle.sourceCommit !== sourceCommit) fail();
  const entries: { path: string; sha256: string }[] = [];
  for (const path of dependencyPaths) {
    const file = join(root, path);
    if (!(await lstat(file)).isFile()) fail();
    entries.push({
      path,
      sha256: createHash("sha256")
        .update(await readFile(file))
        .digest("hex"),
    });
  }
  // Resolving native artifacts is a separate build prerequisite. Do not label
  // npm/Swift pins alone as a locked Gradle transitive graph.
  let nativeResolutionRecorded = false;
  try {
    const names = (await readdir(join(root, "mobile/android/dependency-locks")))
      .filter((name) => name.endsWith(".lockfile"))
      .sort();
    if (names.length < 3) fail();
    const paths = [
      ...names.map((name) => "mobile/android/dependency-locks/" + name),
      "mobile/android/gradle/verification-metadata.xml",
      "mobile/android/buildscript-gradle.lockfile",
    ];
    for (const path of paths) {
      if (
        !(await lstat(join(root, path))).isFile() ||
        git(root, "ls-files", "--error-unmatch", path) !== path
      )
        fail();
      entries.push({
        path,
        sha256: createHash("sha256")
          .update(await readFile(join(root, path)))
          .digest("hex"),
      });
    }
    nativeResolutionRecorded = true;
  } catch {
    nativeResolutionRecorded = false;
  }
  const value = {
    sourceCommit,
    bundleSha256: bundle.bundleSha256,
    dependenciesSha256: createHash("sha256")
      .update(JSON.stringify(entries))
      .digest("hex"),
    appId,
    environment: "preview",
    ...version,
    nativeResolutionRecorded,
    nativeResolutionVerified: false,
  };
  identity(value);
  if (
    git(root, "status", "--porcelain", "--untracked-files=all") ||
    git(root, "rev-parse", "HEAD") !== sourceCommit
  )
    fail();
  return value;
}
if (import.meta.main) {
  try {
    const [command, ...args] = Bun.argv.slice(2);
    if (command === "candidate" && args.length === 4) {
      const result = await recordNativeCandidate(args[0], args[1], {
        version: args[2],
        build: Number(args[3]),
      });
      process.stdout.write(JSON.stringify(result, null, 2) + "\n");
    } else if (command === "check" && args.length === 1) {
      const result = validateNativeDelivery(
        JSON.parse(await readFile(args[0], "utf8")),
      );
      process.stdout.write(JSON.stringify(result) + "\n");
    } else throw new Error();
  } catch {
    process.stderr.write(
      "Native delivery check failed. No upload or account change was performed.\n",
    );
    process.exitCode = 1;
  }
}
