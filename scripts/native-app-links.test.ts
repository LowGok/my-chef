import { afterEach, expect, test } from "bun:test";
import { createHash } from "node:crypto";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  associationResponse,
  readAssociationBundle,
  renderAssociationBundle,
  writeAssociationBundle,
} from "./native-app-links";

const fingerprints = ["AA", "BB", "CC"].map((value) =>
  Array(32).fill(value).join(":"),
);
const input = {
  enabled: true,
  appId: "dev.mychef.app",
  appleApplicationIdentifier: "ABCDE12345.dev.mychef.app",
  playSigning: {
    source: "play-app-signing",
    complete: true,
    fingerprints,
    fingerprintSetSha256: createHash("sha256")
      .update(JSON.stringify(fingerprints))
      .digest("hex"),
  },
};
const temporary: string[] = [];
afterEach(async () => {
  for (const p of temporary.splice(0))
    await rm(p, { recursive: true, force: true });
});

test("association artifacts bind only the new identity, exact path and complete Play signer set", () => {
  const bundle = renderAssociationBundle(input);
  expect(bundle.files.map((f) => f.path).sort()).toEqual([
    "/.well-known/apple-app-site-association",
    "/.well-known/assetlinks.json",
    "/app/open",
  ]);
  const apple = JSON.parse(
    bundle.files.find((f) => f.path.endsWith("apple-app-site-association"))!
      .body,
  );
  expect(apple).toEqual({
    applinks: {
      details: [
        {
          appIDs: ["ABCDE12345.dev.mychef.app"],
          components: [{ "/": "/app/open" }],
        },
      ],
    },
  });
  const android = JSON.parse(
    bundle.files.find((f) => f.path.endsWith("assetlinks.json"))!.body,
  );
  expect(android[0].target.package_name).toBe("dev.mychef.app");
  expect(android[0].target.sha256_cert_fingerprints).toEqual(fingerprints);
  expect(
    android[0].relation_extensions["delegate_permission/common.handle_all_urls"]
      .dynamic_app_link_components,
  ).toEqual([{ "/": "/app/open" }, { "/": "*", exclude: true }]);
  for (const bad of [
    { ...input, appId: "com.gkldevelopment.mychef.preview" },
    { ...input, appleApplicationIdentifier: "ABCDE12345.foreign.app" },
    { ...input, privateOrigin: "https://private.tailtest.ts.net" },
    { ...input, playSigning: { ...input.playSigning, source: "upload" } },
    { ...input, playSigning: { ...input.playSigning, complete: false } },
    {
      ...input,
      playSigning: {
        ...input.playSigning,
        fingerprints: fingerprints.slice(1),
      },
    },
  ])
    expect(() => renderAssociationBundle(bad)).toThrow();
});

test("disabled associations can be prepared before store setup and preserve the browser fallback", () => {
  const bundle = renderAssociationBundle({
    enabled: false,
    appId: "dev.mychef.app",
  });
  expect(JSON.parse(bundle.files[0].body)).toEqual({
    applinks: { details: [] },
  });
  expect(JSON.parse(bundle.files[1].body)).toEqual([]);
  const html = bundle.files[2];
  expect(html.body).toContain('href="/app/"');
  expect(html.body).not.toMatch(
    /<script|http-equiv="refresh"|tail[a-z0-9]*\.ts\.net|gkldevelopment|token|password/i,
  );
  expect(html.headers["content-security-policy"]).toContain(
    "default-src 'none'",
  );
  expect(html.headers["referrer-policy"]).toBe("no-referrer");
});

test("a staged artifact verifies bytes and refuses altered files before hosting", async () => {
  const parent = await mkdtemp(join(tmpdir(), "mychef-associations-"));
  temporary.push(parent);
  const directory = join(parent, "bundle");
  const bundle = renderAssociationBundle(input);
  await writeAssociationBundle(directory, bundle);
  expect(await readAssociationBundle(directory)).toEqual(bundle);
  await writeFile(join(directory, "app-open.html"), "<p>Changed</p>");
  await expect(readAssociationBundle(directory)).rejects.toThrow();
  await expect(writeAssociationBundle(directory, bundle)).rejects.toThrow();
});

test("the controlled handler serves only the three approved HTTPS routes without redirects", async () => {
  const bundle = renderAssociationBundle(input);
  for (const file of bundle.files) {
    const response = associationResponse(
      new Request("https://mychef.dev" + file.path),
      bundle,
    )!;
    expect(response.status).toBe(200);
    expect(response.headers.get("location")).toBeNull();
    expect(await response.text()).toBe(file.body);
    expect(response.headers.get("content-type")).toBe(file.contentType);
  }
  for (const path of [
    "/app/",
    "/app/signup",
    "/app/open/extra",
    "/manifest.json",
    "/api/native/app/v1/session",
  ])
    expect(
      associationResponse(new Request("https://mychef.dev" + path), bundle),
    ).toBeNull();
  expect(
    associationResponse(new Request("https://foreign.test/app/open"), bundle),
  ).toBeNull();
  expect(
    associationResponse(new Request("http://mychef.dev/app/open"), bundle),
  ).toBeNull();
});

test("controlled HTTPS rehearsal preserves exact bodies, headers and the existing web route", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mychef-link-tls-"));
  temporary.push(directory);
  const key = join(directory, "fixture.key"),
    cert = join(directory, "fixture.crt");
  const generated = Bun.spawnSync([
    "openssl",
    "req",
    "-newkey",
    "ec",
    "-pkeyopt",
    "ec_paramgen_curve:P-256",
    "-nodes",
    "-x509",
    "-days",
    "1",
    "-subj",
    "/CN=mychef.dev",
    "-addext",
    "subjectAltName=DNS:mychef.dev,IP:127.0.0.1",
    "-keyout",
    key,
    "-out",
    cert,
  ]);
  expect(generated.exitCode).toBe(0);
  const bundle = renderAssociationBundle(input);
  const server = Bun.serve({
    hostname: "127.0.0.1",
    port: 0,
    tls: { key: Bun.file(key), cert: Bun.file(cert) },
    fetch(request) {
      const prepared = associationResponse(request, bundle);
      if (prepared) return prepared;
      return new Response(
        new URL(request.url).pathname === "/app/"
          ? "Existing browser route"
          : "Unknown route",
        { status: new URL(request.url).pathname === "/app/" ? 200 : 404 },
      );
    },
  });
  try {
    for (const file of bundle.files) {
      const response = await fetch(
        `https://127.0.0.1:${server.port}${file.path}`,
        {
          headers: { host: "mychef.dev" },
          tls: { ca: Bun.file(cert) },
          redirect: "manual",
        },
      );
      expect(response.status).toBe(200);
      expect(response.headers.get("location")).toBeNull();
      expect(await response.text()).toBe(file.body);
      expect(response.headers.get("content-type")).toBe(file.contentType);
    }
    const old = await fetch(`https://127.0.0.1:${server.port}/app/`, {
      headers: { host: "mychef.dev" },
      tls: { ca: Bun.file(cert) },
    });
    expect(await old.text()).toBe("Existing browser route");
  } finally {
    server.stop(true);
  }
});
