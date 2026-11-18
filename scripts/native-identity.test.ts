import { afterEach, expect, test } from "bun:test";
import { createHash } from "node:crypto";
import { mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { nativeIdentity } from "./native-identity";
import { createMobileConfig, prepareMobileBundle, verifyMobileBundle } from "./mobile-build";

const temporary: string[] = [];
afterEach(async () => {
  for (const p of temporary.splice(0)) await rm(p, {recursive: true, force: true});
});

test("only the explicit current Preview and approved app identities can be selected", () => {
  expect(nativeIdentity("preview")).toEqual({lane: "preview", appId: "com.gkldevelopment.mychef.preview", appName: "MyChef Preview"});
  expect(nativeIdentity("app")).toEqual({lane: "app", appId: "dev.mychef.app", appName: "MyChef"});
  for (const invalid of ["", "production", "release", "dev.mychef.preview", undefined, null, {}, " app", "APP"]) {
    expect(() => nativeIdentity(invalid)).toThrow();
  }
});

test("both native identities keep the same private API and browser isolation", () => {
  for (const lane of ["preview", "app"] as const) {
    const config = createMobileConfig("https://fixture.tailtest.ts.net:8990", "preview", lane);
    expect(config.appId).toBe(nativeIdentity(lane).appId);
    expect(config.plugins.MyChefNative.identityLane).toBe(lane);
    expect(config.plugins.MyChefNative.apiOrigin).toBe("https://fixture.tailtest.ts.net:8990");
    expect(config.server.url).toBeUndefined();
    expect(config.server.allowNavigation).toEqual([]);
    expect(config.android.zoomEnabled).toBe(true);
    for (const origin of ["https://mychef.dev", "http://localhost", "https://foreign.test", "https://fixture.tailtest.ts.net/path"]) {
      expect(() => createMobileConfig(origin, "preview", lane)).toThrow();
    }
  }
});

test("prepared manifest binds lane, config and bytes even after a caller recomputes its digest", async () => {
  const root = await mkdtemp(join(tmpdir(), "mychef-identity-manifest-"));
  temporary.push(root);
  const source = join(root, "source"), output = join(root, "candidate");
  await mkdir(source);
  await writeFile(join(source, "index.html"), "<html><head></head><body>Fixture</body></html>");
  const manifest = await prepareMobileBundle(source, output,
    createMobileConfig("https://localhost:8990", "preview", "app"), "b".repeat(40));
  expect(manifest.version).toBe(2);
  expect(manifest.identityLane).toBe("app");
  await expect(verifyMobileBundle(output)).resolves.toEqual(manifest);

  const original = JSON.parse(await readFile(join(output, "bundle-manifest.json"), "utf8"));
  for (const variant of ["lane", "appId", "configLane", "missingLane"]) {
    const tampered = structuredClone(original);
    if (variant === "lane") tampered.identityLane = "preview";
    if (variant === "appId") tampered.config.appId = nativeIdentity("preview").appId;
    if (variant === "configLane") tampered.config.plugins.MyChefNative.identityLane = "preview";
    if (variant === "missingLane") delete tampered.identityLane;
    const {bundleSha256: _, ...evidence} = tampered;
    tampered.bundleSha256 = createHash("sha256").update(JSON.stringify(evidence)).digest("hex");
    await writeFile(join(output, "bundle-manifest.json"), JSON.stringify(tampered));
    await writeFile(join(output, "runtime-config.json"), JSON.stringify(tampered.config));
    await expect(verifyMobileBundle(output)).rejects.toThrow();
  }
});

test("a selected identity cannot package a substituted app or invalid lane", async () => {
  const root = await mkdtemp(join(tmpdir(), "mychef-identity-reject-"));
  temporary.push(root);
  const source = join(root, "source");
  await mkdir(source);
  await writeFile(join(source, "index.html"), "<html><head></head><body>Fixture</body></html>");
  const config = createMobileConfig("https://localhost:8990", "preview", "app");
  const invalid = {...config, appId: nativeIdentity("preview").appId};
  await expect(prepareMobileBundle(source, join(root,"out"), invalid, "b".repeat(40))).rejects.toThrow();
});
