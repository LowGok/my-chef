import { afterEach, expect, test } from "bun:test";
import {
  mkdtemp,
  mkdir,
  readFile,
  rm,
  writeFile,
  symlink,
} from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import {
  createMobileConfig,
  prepareMobileBundle,
  verifyMobileBundle,
} from "./mobile-build";

const roots: string[] = [];
afterEach(async () => {
  for (const root of roots.splice(0))
    await rm(root, { recursive: true, force: true });
});
async function fixture() {
  const root = await mkdtemp(join(tmpdir(), "mychef-native-assets-"));
  roots.push(root);
  const source = join(root, "web");
  await mkdir(join(source, "assets"), { recursive: true });
  await writeFile(
    join(source, "index.html"),
    '<!doctype html><html><head></head><body><div id="root"></div><script type="module" src="/app/assets/app.js"></script></body></html>',
  );
  await writeFile(
    join(source, "assets/app.js"),
    'document.getElementById("root").textContent="MyChef";',
  );
  await writeFile(
    join(source, "sw.js"),
    'self.addEventListener("fetch",()=>{});',
  );
  return { root, source, destination: join(root, "mobile") };
}

test("private Capacitor configuration rejects public, insecure and credential-bearing targets", () => {
  const config = createMobileConfig("https://fixture-node.tailtest.ts.net:8977");
  expect(config.appId).toBe("com.gkldevelopment.mychef.preview");
  expect(config.loggingBehavior).toBe("none");
  expect(config.server.url).toBeUndefined();
  expect(config.server.allowNavigation).toEqual([]);
  expect(config.plugins.MyChefNative.apiOrigin).toBe(
    "https://fixture-node.tailtest.ts.net:8977",
  );
  for (const origin of [
    "",
    "http://fixture-node.tailtest.ts.net",
    "https://foreign.test",
    "https://user:secret@fixture-node.tailtest.ts.net",
    "https://fixture-node.tailtest.ts.net/app",
    "https://fixture-node.tailtest.ts.net?token=secret",
  ])
    expect(() => createMobileConfig(origin)).toThrow();
  expect(() =>
    createMobileConfig("https://api.example.invalid", "production"),
  ).toThrow();
});

test("packaged Android config enables pinch zoom without enabling WebView debugging", async () => {
  const { source, destination } = await fixture();
  await prepareMobileBundle(
    source,
    destination,
    createMobileConfig("https://localhost:8977"),
    "a".repeat(40),
  );
  const packaged = JSON.parse(
    await readFile(join(destination, "runtime-config.json"), "utf8"),
  );
  expect(packaged.android.zoomEnabled).toBe(true);
  expect(packaged.android.webContentsDebuggingEnabled).toBe(false);
});

test("native packaging embeds the app, excludes the service worker and verifies every asset", async () => {
  const { source, destination } = await fixture();
  const config = createMobileConfig("https://fixture-node.tailtest.ts.net:8977");
  const result = await prepareMobileBundle(
    source,
    destination,
    config,
    "a".repeat(40),
  );
  expect(result.sourceCommit).toBe("a".repeat(40));
  expect(result.environment).toBe("preview");
  expect(result.bundleSha256).toMatch(/^[0-9a-f]{64}$/);
  expect(
    await readFile(join(destination, "embedded/index.html"), "utf8"),
  ).toContain("/app/assets/app.js");
  expect(result.assets.some((asset) => asset.path.endsWith("sw.js"))).toBe(
    false,
  );
  await expect(verifyMobileBundle(destination)).resolves.toEqual(result);
  await writeFile(join(destination, "embedded/app/assets/app.js"), "changed");
  await expect(verifyMobileBundle(destination)).rejects.toThrow();
});

test("native packaging rejects remote interface code and provider credentials", async () => {
  for (const payload of [
    'import("https://foreign.test/app.js");',
    'const token="AIza' + "x".repeat(35) + '";',
  ]) {
    const { source, destination } = await fixture();
    await writeFile(join(source, "assets/app.js"), payload);
    await expect(
      prepareMobileBundle(
        source,
        destination,
        createMobileConfig("https://localhost:8977"),
        "b".repeat(40),
      ),
    ).rejects.toThrow();
  }
});

test("native evidence rejects extra assets, missing assets and endpoint substitution", async () => {
  for (const mutation of ["extra", "missing", "config"]) {
    const { source, destination } = await fixture();
    await prepareMobileBundle(
      source,
      destination,
      createMobileConfig("https://localhost:8977"),
      "b".repeat(40),
    );
    if (mutation === "extra")
      await writeFile(join(destination, "embedded/foreign.js"), "unexpected");
    if (mutation === "missing")
      await rm(join(destination, "embedded/app/assets/app.js"));
    if (mutation === "config")
      await writeFile(
        join(destination, "runtime-config.json"),
        JSON.stringify(createMobileConfig("https://localhost:9999")),
      );
    await expect(verifyMobileBundle(destination)).rejects.toThrow();
  }
});

test("packaging refuses source symlinks and leaves existing evidence on invalid input", async () => {
  const { source, destination, root } = await fixture();
  const result = await prepareMobileBundle(
    source,
    destination,
    createMobileConfig("https://localhost:8977"),
    "c".repeat(40),
  );
  await writeFile(join(root, "outside.js"), "outside");
  await symlink(join(root, "outside.js"), join(source, "assets/link.js"));
  await expect(
    prepareMobileBundle(
      source,
      destination,
      createMobileConfig("https://localhost:8977"),
      "d".repeat(40),
    ),
  ).rejects.toThrow();
  await expect(verifyMobileBundle(destination)).resolves.toEqual(result);
});
