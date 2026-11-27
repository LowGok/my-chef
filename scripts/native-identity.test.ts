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

test("native build commands bind package identity independently of the compile namespace", async () => {
  const {nativeBuildPlan} = await import("./mobile-hosted");
  const {compilePlan} = await import("./mobile-compile");
  const version = {version: "0.1.0", build: 10};
  for (const lane of ["preview","app"] as const) {
    const id = nativeIdentity(lane).appId;
    expect(nativeBuildPlan("android", version, lane)).toContain(`-PmychefIdentityLane=${lane}`);
    expect(nativeBuildPlan("ios-simulator", version, lane)).toContain(`PRODUCT_BUNDLE_IDENTIFIER=${id}`);
    expect(compilePlan("ios-device", false, undefined, lane)).toContain(`PRODUCT_BUNDLE_IDENTIFIER=${id}`);
    expect(compilePlan("android", false, undefined, lane)).toContain(`-PmychefIdentityLane=${lane}`);
    if(lane==="app")expect(compilePlan("ios-device",false,undefined,lane)).toContain("CODE_SIGN_ENTITLEMENTS=App/AppIdentity.entitlements");
    else expect(compilePlan("ios-device",false,undefined,lane)).not.toContain("CODE_SIGN_ENTITLEMENTS=App/AppIdentity.entitlements");
  }
});

test("the actual JVM package guard rejects a copied config from the sibling app", async () => {
  const output = await mkdtemp(join(tmpdir(), "mychef-native-identity-jvm-"));
  temporary.push(output);
  const source = join(import.meta.dir, "../mobile/android/app/src/main/java/com/gkldevelopment/mychef/preview/NativeAppIdentity.java");
  const harness = join(output,"IdentityContract.java");
  await writeFile(harness, `import com.gkldevelopment.mychef.preview.NativeAppIdentity;
public class IdentityContract {
  public static void main(String[] args) {
    if (!NativeAppIdentity.appOpeningLink("app","https://mychef.dev/app/open")) throw new AssertionError("Missing exact app link");
    for (String value: new String[]{"https://mychef.dev/app/open?x=1","https://mychef.dev/app/open#x","https://mychef.dev:443/app/open","https://mychef.dev/app/%6fpen"}) if (NativeAppIdentity.appOpeningLink("app",value)) throw new AssertionError("Non-exact app link accepted");
    if (NativeAppIdentity.appOpeningLink("preview","https://mychef.dev/app/open")) throw new AssertionError("Preview claims app link");
    if (!NativeAppIdentity.apiPath("app", "/api/native/v1/shopping/2026-11-16?x=a%20b").equals("/api/native/app/v1/shopping/2026-11-16?x=a%20b")) throw new AssertionError("Candidate uses wrong ingress");
    if (!NativeAppIdentity.apiPath("preview", "/api/native/v1/profile").equals("/api/native/v1/profile")) throw new AssertionError("Preview ingress changed");
    try { NativeAppIdentity.apiPath("app", "/api/native/app/v1/profile"); throw new AssertionError("Arbitrary bridge route accepted"); } catch (IllegalArgumentException expected) {}
    NativeAppIdentity.require("dev.mychef.app", "app");
    NativeAppIdentity.require("com.gkldevelopment.mychef.preview", "preview");
    for (String[] pair: new String[][]{
      {"dev.mychef.app","preview"}, {"com.gkldevelopment.mychef.preview","app"},
      {"foreign.app","app"}, {"dev.mychef.app","release"}
    }) {
      try { NativeAppIdentity.require(pair[0],pair[1]); }
      catch (IllegalArgumentException expected) { continue; }
      throw new AssertionError("Identity mismatch accepted");
    }
  }
}`);
  const compile = Bun.spawnSync(["javac","--release","17","-d",output,source,harness]);
  expect(compile.exitCode,compile.stderr.toString()).toBe(0);
  const run = Bun.spawnSync(["java","-cp",output,"IdentityContract"]);
  expect(run.exitCode,run.stderr.toString()).toBe(0);
});

test("installed probe package selection matches the requested lane and rejects unknown names", () => {
  for (const [lane, id] of [["preview",nativeIdentity("preview").appId],["app",nativeIdentity("app").appId]]) {
    const result = Bun.spawnSync(["python","-c","import sys; sys.path.insert(0,'scripts'); from native_identity import application_id; print(application_id())"], {cwd:join(import.meta.dir,".."),env:{...process.env,NATIVE_IDENTITY_LANE:lane}});
    expect(result.exitCode,result.stderr.toString()).toBe(0);
    expect(result.stdout.toString().trim()).toBe(id);
  }
  const rejected=Bun.spawnSync(["python","-c","import sys; sys.path.insert(0,'scripts'); from native_identity import application_id; application_id()"],{cwd:join(import.meta.dir,".."),env:{...process.env,NATIVE_IDENTITY_LANE:"foreign"}});
  expect(rejected.exitCode).not.toBe(0);
});

test("candidate associations use only the exact public path and preserve Preview manifest behavior",()=>{
 const result=Bun.spawnSync(["python3","-c",`import xml.etree.ElementTree as E, plistlib
from pathlib import Path
base=E.parse('mobile/android/app/src/main/AndroidManifest.xml').getroot()
app=E.parse('mobile/android/app/src/appIdentity/AndroidManifest.xml').getroot()
a='{http://schemas.android.com/apk/res/android}'
links=[f for f in app.findall('./application/activity/intent-filter') if f.find('data') is not None]
assert len(links)==1
link=links[0]
assert link.attrib=={a+'autoVerify':'true'}
assert [d.attrib for d in link.findall('data')]==[{a+'scheme':'https',a+'host':'mychef.dev',a+'path':'/app/open'}]
assert [n.get(a+'name') for n in link.findall('action')]==['android.intent.action.VIEW']
assert sorted(n.get(a+'name') for n in link.findall('category'))==['android.intent.category.BROWSABLE','android.intent.category.DEFAULT']
app.find('./application/activity').remove(link)
def shape(n):return (n.tag,sorted(n.attrib.items()),(n.text or '').strip(),[shape(c) for c in n])
assert shape(base)==shape(app), 'candidate manifest changed unrelated permissions or activities'
with open('mobile/ios/App/App/AppIdentity.entitlements','rb') as f:ent=plistlib.load(f)
assert ent=={'aps-environment':'production','com.apple.developer.associated-domains':['applinks:mychef.dev']}
with open('mobile/ios/App/App/PushPreview.entitlements','rb') as f:old=plistlib.load(f)
assert 'com.apple.developer.associated-domains' not in old
`],{cwd:join(import.meta.dir,"..")});
 expect(result.exitCode,result.stderr.toString()).toBe(0);
});
