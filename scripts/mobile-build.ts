import { createHash } from "node:crypto";
import { mkdir, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";

// No production target is available until the later native release review.
export function createMobileConfig(origin: string, environment = "preview") {
  const url = new URL(origin);
  if (
    environment !== "preview" ||
    url.protocol !== "https:" ||
    url.username ||
    url.password ||
    url.pathname !== "/" ||
    url.search ||
    url.hash ||
    origin !== url.origin ||
    !(
      url.hostname === "localhost" ||
      /^[a-z0-9-]+\.tail[a-z0-9]+\.ts\.net$/.test(url.hostname)
    )
  )
    throw new Error("An explicit private HTTPS preview origin is required.");
  return {
    appId: "com.gkldevelopment.mychef.preview",
    appName: "MyChef Preview",
    webDir: ".generated/embedded",
    loggingBehavior: "none" as const,
    server: {
      hostname: "localhost",
      androidScheme: "https",
      iosScheme: "capacitor",
      allowNavigation: [] as string[],
      url: undefined,
    },
    android: {
      allowMixedContent: false,
      webContentsDebuggingEnabled: false,
      zoomEnabled: true,
    },
    ios: { webContentsDebuggingEnabled: false },
    plugins: { MyChefNative: { apiOrigin: url.origin } },
  };
}
type Config = ReturnType<typeof createMobileConfig>;
type Asset = { path: string; sha256: string; bytes: number };
type Manifest = {
  version: 1;
  environment: "preview";
  sourceCommit: string;
  config: Config;
  assets: Asset[];
  bundleSha256: string;
};
const digest = (bytes: string | Uint8Array) =>
  createHash("sha256").update(bytes).digest("hex");
const policy =
  "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' blob: data:; font-src 'self'; connect-src 'none'; object-src 'none'; frame-src 'none'; base-uri 'none'; form-action 'none'";

async function files(directory: string, prefix = ""): Promise<string[]> {
  const result: string[] = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const name = prefix + entry.name;
    if (entry.isDirectory())
      result.push(...(await files(join(directory, entry.name), name + "/")));
    else if (entry.isFile()) result.push(name);
    else throw new Error("Native assets must be regular files, with no links.");
  }
  return result.sort();
}

function inspect(path: string, data: Uint8Array) {
  if (/\.(?:map|pem|key|p12|pfx|sqlite3?|db)$/i.test(path))
    throw new Error("Unexpected private or debug asset.");
  if (/\.(?:js|html|css|json)$/i.test(path)) {
    const text = Buffer.from(data).toString("utf8");
    if (
      /AIza[\w-]{35}|re_[\w]{20,}|-----BEGIN [\w ]*PRIVATE KEY-----|(?:sk_live|sk_test)_[\w]+/.test(
        text,
      )
    )
      throw new Error("Provider credential in native assets.");
    if (
      /(?:import\s*\(\s*["']|(?:src|href)\s*=\s*["']|import\s*[^;]*?from\s*["'])(?:https?:)?\/\//i.test(
        text,
      )
    )
      throw new Error("Remote interface resource in native assets.");
    if (/\b(?:eval|importScripts)\s*\(|new\s+Function\s*\(/.test(text))
      throw new Error("Dynamic executable code in native assets.");
  }
}

export async function prepareMobileBundle(
  source: string,
  destination: string,
  config: Config,
  sourceCommit: string,
): Promise<Manifest> {
  const src = resolve(source),
    dest = resolve(destination);
  if (dest === src || src.startsWith(dest + "/") || dest.startsWith(src + "/"))
    throw new Error("Separate native output from source.");
  if (!/^[a-f0-9]{40}$/.test(sourceCommit))
    throw new Error("An exact source commit is required.");
  const canonicalConfig = createMobileConfig(
    config.plugins.MyChefNative.apiOrigin,
  );
  if (JSON.stringify(config) !== JSON.stringify(canonicalConfig))
    throw new Error("Unsupported native configuration.");
  const content = new Map<string, Uint8Array>();
  let total = 0;
  for (const path of await files(src)) {
    if (path === "sw.js") continue;
    const data = await readFile(join(src, path));
    total += data.length;
    if (total > 64 * 1024 * 1024)
      throw new Error("Native bundle exceeds 64 MiB.");
    inspect(path, data);
    content.set("app/" + path, data);
  }
  const original = content.get("app/index.html");
  if (!original || !Buffer.from(original).toString().includes("</head>"))
    throw new Error("Missing app entry point.");
  const html = Buffer.from(original)
    .toString()
    .replace(
      'content="width=device-width, initial-scale=1"',
      'content="width=device-width, initial-scale=1, viewport-fit=cover"',
    )
    .replace(
      "</head>",
      `<meta http-equiv="Content-Security-Policy" content="${policy}"></head>`,
    );
  content.set("index.html", Buffer.from(html));
  content.set("app/index.html", Buffer.from(html));
  // Only clear the named generated directory after all inputs have passed inspection.
  await mkdir(dest, { recursive: true });
  await rm(join(dest, "embedded"), { recursive: true, force: true });
  const assets: Asset[] = [];
  for (const [path, data] of [...content].sort(([a], [b]) =>
    a.localeCompare(b),
  )) {
    await mkdir(dirname(join(dest, "embedded", path)), { recursive: true });
    await writeFile(join(dest, "embedded", path), data);
    assets.push({ path, sha256: digest(data), bytes: data.length });
  }
  const evidence = {
    version: 1 as const,
    environment: "preview" as const,
    sourceCommit,
    config: canonicalConfig,
    assets,
  };
  const manifest = {
    ...evidence,
    bundleSha256: digest(JSON.stringify(evidence)),
  };
  await writeFile(
    join(dest, "runtime-config.json"),
    JSON.stringify(canonicalConfig, null, 2) + "\n",
  );
  await writeFile(
    join(dest, "bundle-manifest.json"),
    JSON.stringify(manifest, null, 2) + "\n",
  );
  return manifest;
}

export async function verifyMobileBundle(
  destination: string,
): Promise<Manifest> {
  const manifest: Manifest = JSON.parse(
    await readFile(join(destination, "bundle-manifest.json"), "utf8"),
  );
  const { bundleSha256, ...evidence } = manifest;
  if (
    manifest.version !== 1 ||
    manifest.environment !== "preview" ||
    !/^[a-f0-9]{40}$/.test(manifest.sourceCommit) ||
    digest(JSON.stringify(evidence)) !== bundleSha256
  )
    throw new Error("Invalid native bundle identity.");
  const canonicalConfig = createMobileConfig(
    manifest.config.plugins.MyChefNative.apiOrigin,
  );
  if (
    JSON.stringify(canonicalConfig) !== JSON.stringify(manifest.config) ||
    JSON.stringify(
      JSON.parse(
        await readFile(join(destination, "runtime-config.json"), "utf8"),
      ),
    ) !== JSON.stringify(canonicalConfig)
  )
    throw new Error("Native configuration changed.");
  const paths = await files(join(destination, "embedded"));
  if (
    JSON.stringify([...manifest.assets.map((asset) => asset.path)].sort()) !==
    JSON.stringify(paths)
  )
    throw new Error("Native asset inventory changed.");
  for (const asset of manifest.assets) {
    const data = await readFile(join(destination, "embedded", asset.path));
    inspect(asset.path, data);
    if (data.length !== asset.bytes || digest(data) !== asset.sha256)
      throw new Error("Native asset changed.");
  }
  return manifest;
}

if (import.meta.main) {
  const [command, source, destination, origin, commit, ...extra] =
    process.argv.slice(2);
  if (command === "verify" && source && !destination) {
    const result = await verifyMobileBundle(source);
    console.log(
      JSON.stringify({
        sourceCommit: result.sourceCommit,
        bundleSha256: result.bundleSha256,
        environment: result.environment,
      }),
    );
  } else if (
    command === "prepare" &&
    source &&
    destination &&
    origin &&
    commit &&
    !extra.length
  ) {
    const result = await prepareMobileBundle(
      source,
      destination,
      createMobileConfig(origin),
      commit,
    );
    console.log(
      JSON.stringify({
        sourceCommit: result.sourceCommit,
        bundleSha256: result.bundleSha256,
        environment: result.environment,
      }),
    );
  } else
    throw new Error(
      "Usage: bun scripts/mobile-build.ts prepare <web-app> <generated-dir> <private-https-origin> <commit> | verify <generated-dir>",
    );
}
