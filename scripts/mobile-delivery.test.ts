import { expect, test, afterEach } from "bun:test";
import { mkdtemp, mkdir, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { prepareMobileBundle, createMobileConfig } from "./mobile-build";
import {
  validateNativeDelivery,
  recordNativeCandidate,
} from "./mobile-delivery";
const commit = "a".repeat(40),
  bundle = "b".repeat(64),
  deps = "c".repeat(64);
function facts() {
  return {
    candidate: {
      sourceCommit: commit,
      bundleSha256: bundle,
      dependenciesSha256: deps,
      appId: "com.gkldevelopment.mychef.preview",
      version: "0.1.0",
      build: 1,
      environment: "preview",
      nativeResolutionVerified: true,
    },
    approval: {
      operation: "owner-testflight",
      sourceCommit: commit,
      bundleSha256: bundle,
      artifactSha256: "d".repeat(64),
      expiresUnix: 2000,
    },
    capacity: {
      verifiedAtUnix: 990,
      expiresUnix: 1100,
      includedComputeRemainingUSD: 10,
      estimatedComputeUSD: 1,
      artifactStorageRemainingBytes: 500000000,
      estimatedArtifactBytes: 200000000,
      paidOveragesDisabled: true,
      standardRunner: true,
    },
    apple: {
      paidMembership: true,
      signingVerified: true,
      processing: "VALID",
      internalOnly: true,
      bundleId: "com.gkldevelopment.mychef.preview",
      version: "0.1.0",
      build: 1,
      artifactSha256: "d".repeat(64),
      groupId: "owner-group",
      expectedOwnerId: "owner-id",
      memberIds: ["owner-id"],
      automaticDistribution: false,
      externalGroups: 0,
      publicLinkEnabled: false,
    },
  };
}
test("private delivery accepts only the exact approved internal candidate with verified free capacity", () => {
  const value = facts();
  expect(validateNativeDelivery(value, 1000).operation).toBe(
    "owner-testflight",
  );
  for (const change of [
    (v: any) => (v.approval.sourceCommit = "e".repeat(40)),
    (v: any) => (v.approval = undefined),
    (v: any) => (v.capacity.paidOveragesDisabled = false),
    (v: any) => (v.capacity.includedComputeRemainingUSD = 0),
    (v: any) => (v.capacity.expiresUnix = 999),
    (v: any) => (v.capacity.estimatedComputeUSD = NaN),
    (v: any) => v.apple.memberIds.push("another-person"),
    (v: any) => (v.apple.automaticDistribution = true),
    (v: any) => (v.apple.publicLinkEnabled = true),
    (v: any) => (v.apple.externalGroups = 1),
    (v: any) => (v.apple.paidMembership = false),
    (v: any) => (v.apple.internalOnly = false),
    (v: any) => (v.apple.artifactSha256 = "e".repeat(64)),
    (v: any) => (v.apple.build = 2),
    (v: any) => (v.candidate.environment = "production"),
    (v: any) => (v.approval.operation = "public-beta"),
  ]) {
    const invalid = structuredClone(value);
    change(invalid);
    expect(() => validateNativeDelivery(invalid, 1000)).toThrow();
  }
});
test("unsigned builds cannot authorize signing or store distribution", () => {
  const value: any = facts();
  value.approval.operation = "unsigned-build";
  delete value.apple;
  expect(validateNativeDelivery(value, 1000)).toEqual({
    operation: "unsigned-build",
    sourceCommit: commit,
    bundleSha256: bundle,
  });
});
const roots: string[] = [];
afterEach(async () => {
  for (const root of roots.splice(0))
    await rm(root, { recursive: true, force: true });
});
function git(root: string, ...args: string[]) {
  const run = Bun.spawnSync(["git", "-C", root, ...args], {
    env: { PATH: process.env.PATH, LANG: "C.UTF-8" },
  });
  if (run.exitCode) throw new Error("Fixture git command failed");
  return run.stdout.toString().trim();
}
test("candidate recording checks the actual clean checkout and prepared bundle instead of caller claims", async () => {
  const root = await mkdtemp(join(tmpdir(), "mychef-native-identity-"));
  roots.push(root);
  await mkdir(join(root, "web"), { recursive: true });
  await writeFile(
    join(root, "web/index.html"),
    "<html><head></head><body>MyChef</body></html>",
  );
  const inputs = [
    "mobile/bun.lock",
    "web/bun.lock",
    "go.sum",
    "mobile/android/gradle/wrapper/gradle-wrapper.properties",
    "mobile/ios/App/App.xcodeproj/project.xcworkspace/xcshareddata/swiftpm/Package.resolved",
  ];
  for (const input of inputs) {
    await mkdir(join(root, input, ".."), { recursive: true });
    await writeFile(join(root, input), "locked");
  }
  await writeFile(join(root, ".gitignore"), ".generated/\n");
  git(root, "init", "--quiet");
  git(root, "add", ".");
  git(
    root,
    "-c",
    "user.name=Fixture",
    "-c",
    "user.email=fixture@example.test",
    "commit",
    "--quiet",
    "-m",
    "fixture",
  );
  const sourceCommit = git(root, "rev-parse", "HEAD");
  const destination = join(root, ".generated");
  await prepareMobileBundle(
    join(root, "web"),
    destination,
    createMobileConfig("https://localhost:8977"),
    sourceCommit,
  );
  const result = await recordNativeCandidate(root, destination, {
    version: "0.1.0",
    build: 1,
  });
  expect(result.sourceCommit).toBe(sourceCommit);
  expect(result.nativeResolutionVerified).toBe(false);
  expect(result.dependenciesSha256).toMatch(/^[a-f0-9]{64}$/);
  await writeFile(join(root, "web/index.html"), "changed");
  await expect(
    recordNativeCandidate(root, destination, { version: "0.1.0", build: 1 }),
  ).rejects.toThrow();
  git(root, "restore", "web/index.html");
  await prepareMobileBundle(
    join(root, "web"),
    destination,
    createMobileConfig("https://localhost:8977"),
    "f".repeat(40),
  );
  await expect(
    recordNativeCandidate(root, destination, { version: "0.1.0", build: 1 }),
  ).rejects.toThrow();
});

test("public native jobs run on owner labels with read-only access and receipt-only artifacts", async () => {
  const { readFile } = await import("node:fs/promises");
  const source = await readFile(
    join(import.meta.dir, "../.github/workflows/native-compile.yml"),
    "utf8",
  );
  const workflow = Bun.YAML.parse(source) as any;
  expect(Object.keys(workflow.on)).toEqual(["pull_request"]);
  expect(workflow.permissions).toEqual({ contents: "read" });
  expect(workflow.jobs.compile.if).toContain("NATIVE_VERIFICATION_PR");
  expect(workflow.jobs.compile["timeout-minutes"]).toBe(25);
  for (const step of workflow.jobs.compile.steps) {
    if (step.uses) expect(step.uses).toMatch(/@[a-f0-9]{40}$/);
  }
  const artifact = workflow.jobs.compile.steps.find((step: { uses?: string }) =>
    step.uses?.startsWith("actions/upload-artifact@"),
  );
  expect(artifact.with.path.trim().split("\n")).toEqual([
    "mobile/.generated/artifacts/compilation.json",
    "mobile/.generated/artifacts/installed-smoke.json",
    "mobile/.generated/artifacts/installed-journeys.json",
  ]);
  expect(source).not.toMatch(
    /secrets\.|pull_request_target|app-store-connect|altool|notarytool|pilot upload|fastlane upload|gkldevelopment\.com/,
  );
});
