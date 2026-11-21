import { createHash } from "node:crypto";
import { lstat, mkdir, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";

const origin = "https://mychef.dev",
  appId = "dev.mychef.app";
const routes = [
  "/.well-known/apple-app-site-association",
  "/.well-known/assetlinks.json",
  "/app/open",
] as const;
const names = [
  "apple-app-site-association.json",
  "assetlinks.json",
  "app-open.html",
];
const hash = (value: string) =>
  createHash("sha256").update(value).digest("hex");
const fail = (): never => {
  throw new Error("Native association identity or artifact is not verified.");
};
function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value))
    return fail();
  return value as Record<string, unknown>;
}
function keys(value: Record<string, unknown>, expected: string[]) {
  if (Object.keys(value).sort().join(",") !== [...expected].sort().join(","))
    fail();
}
type AssociationInput =
  | { enabled: false; appId: string }
  | {
      enabled: true;
      appId: string;
      appleApplicationIdentifier: string;
      playSigning: {
        source: "play-app-signing";
        complete: true;
        fingerprints: string[];
        fingerprintSetSha256: string;
      };
    };
export type AssociationBundle = {
  schemaVersion: 1;
  origin: string;
  input: AssociationInput;
  inputSha256: string;
  files: {
    path: string;
    body: string;
    contentType: string;
    headers: Record<string, string>;
    sha256: string;
  }[];
  bundleSha256: string;
};

function identity(input: unknown): AssociationInput {
  const value = object(input);
  if (value.appId !== appId || typeof value.enabled !== "boolean")
    return fail();
  if (!value.enabled) {
    keys(value, ["enabled", "appId"]);
    return { enabled: false, appId };
  }
  keys(value, [
    "enabled",
    "appId",
    "appleApplicationIdentifier",
    "playSigning",
  ]);
  if (
    typeof value.appleApplicationIdentifier !== "string" ||
    !/^[A-Z0-9]{10}\.dev\.mychef\.app$/.test(value.appleApplicationIdentifier)
  )
    return fail();
  const play = object(value.playSigning);
  keys(play, ["source", "complete", "fingerprints", "fingerprintSetSha256"]);
  if (
    play.source !== "play-app-signing" ||
    play.complete !== true ||
    !Array.isArray(play.fingerprints) ||
    play.fingerprints.length < 1 ||
    play.fingerprints.length > 16 ||
    play.fingerprints.some(
      (v) =>
        typeof v !== "string" || !/^(?:[A-F0-9]{2}:){31}[A-F0-9]{2}$/.test(v),
    )
  )
    return fail();
  const fingerprints = [...(play.fingerprints as string[])].sort();
  if (
    new Set(fingerprints).size !== fingerprints.length ||
    play.fingerprintSetSha256 !== hash(JSON.stringify(fingerprints))
  )
    return fail();
  return {
    enabled: true,
    appId,
    appleApplicationIdentifier: value.appleApplicationIdentifier,
    playSigning: {
      source: "play-app-signing",
      complete: true,
      fingerprints,
      fingerprintSetSha256: hash(JSON.stringify(fingerprints)),
    },
  };
}

const style =
  "body{margin:0;background:#f9f5ea;color:#30342d;font:18px system-ui,sans-serif}main{max-width:34rem;margin:auto;padding:clamp(24px,6vw,64px);min-height:80vh;display:flex;flex-direction:column;justify-content:center;gap:20px}h1{font-size:2.5rem;margin:0}p{line-height:1.6;margin:0}a{background:#4a694b;color:white;text-align:center;text-decoration:none;border-radius:12px;padding:16px;font-weight:600}a:focus-visible{outline:3px solid #a64f26;outline-offset:4px}";
const styleHash = createHash("sha256").update(style).digest("base64");
const csp = `default-src 'none'; style-src 'sha256-${styleHash}'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'`;
const fallback = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="referrer" content="no-referrer"><title>MyChef</title><style>${style}</style></head><body><main><h1>MyChef</h1><p>A little planning. More room for everyday life.</p><p>Continue in your browser to open MyChef.</p><a href="/app/">Continue in browser</a></main></body></html>\n`;

// This renders non-secret facts. It does not authenticate a provider readback
// or authorize publication; the delivery gate binds those separate proofs.
export function renderAssociationBundle(input: unknown): AssociationBundle {
  const value = identity(input);
  const apple = {
    applinks: {
      details: value.enabled
        ? [
            {
              appIDs: [value.appleApplicationIdentifier],
              components: [{ "/": "/app/open" }],
            },
          ]
        : [],
    },
  };
  const android = value.enabled
    ? [
        {
          relation: ["delegate_permission/common.handle_all_urls"],
          target: {
            namespace: "android_app",
            package_name: appId,
            sha256_cert_fingerprints: value.playSigning.fingerprints,
          },
          relation_extensions: {
            "delegate_permission/common.handle_all_urls": {
              dynamic_app_link_components: [
                { "/": "/app/open" },
                { "/": "*", exclude: true },
              ],
            },
          },
        },
      ]
    : [];
  const bodies = [
    JSON.stringify(apple, null, 2) + "\n",
    JSON.stringify(android, null, 2) + "\n",
    fallback,
  ];
  const files = routes.map((path, i) => ({
    path,
    body: bodies[i],
    contentType:
      i < 2 ? "application/json; charset=utf-8" : "text/html; charset=utf-8",
    headers: {
      "cache-control": "public, max-age=300",
      "referrer-policy": "no-referrer",
      "x-content-type-options": "nosniff",
      ...(i === 2 ? { "content-security-policy": csp } : {}),
    },
    sha256: hash(bodies[i]),
  }));
  const content = {
    schemaVersion: 1 as const,
    origin,
    input: value,
    inputSha256: hash(JSON.stringify(value)),
    files,
  };
  return { ...content, bundleSha256: hash(JSON.stringify(content)) };
}

function verify(value: unknown): AssociationBundle {
  const candidate = object(value);
  const expected = renderAssociationBundle(candidate.input);
  if (JSON.stringify(candidate) !== JSON.stringify(expected)) return fail();
  return expected;
}

export async function writeAssociationBundle(
  directory: string,
  value: AssociationBundle,
) {
  const bundle = verify(value);
  await mkdir(directory, { mode: 0o700 });
  for (let i = 0; i < names.length; i++)
    await writeFile(join(directory, names[i]), bundle.files[i].body, {
      mode: 0o600,
      flag: "wx",
    });
  await writeFile(join(directory, "manifest.json"), JSON.stringify(bundle), {
    mode: 0o600,
    flag: "wx",
  });
}
export async function readAssociationBundle(
  directory: string,
): Promise<AssociationBundle> {
  const root = await lstat(directory);
  if (!root.isDirectory() || root.isSymbolicLink()) return fail();
  const bounded = async (name: string) => {
    const p = join(directory, name),
      stat = await lstat(p);
    if (!stat.isFile() || stat.isSymbolicLink() || stat.size > 65536)
      return fail();
    return readFile(p, "utf8");
  };
  const bundle = verify(JSON.parse(await bounded("manifest.json")));
  for (let i = 0; i < names.length; i++)
    if ((await bounded(names[i])) !== bundle.files[i].body) return fail();
  return bundle;
}

// Unknown routes are left to the existing website router. Only the controlled
// fixture and a separately approved publisher mount these exact three paths.
export function associationResponse(
  request: Request,
  bundle: AssociationBundle,
): Response | null {
  const url = new URL(request.url);
  if (url.origin !== origin) return null;
  const file = bundle.files.find((value) => value.path === url.pathname);
  if (!file) return null;
  if (request.method !== "GET" && request.method !== "HEAD")
    return new Response(null, { status: 405, headers: { allow: "GET, HEAD" } });
  return new Response(request.method === "HEAD" ? null : file.body, {
    status: 200,
    headers: { ...file.headers, "content-type": file.contentType },
  });
}

if (import.meta.main) {
  try {
    const [operation, input, output, ...extra] = process.argv.slice(2);
    if (extra.length || !input) fail();
    let bundle: AssociationBundle | undefined;
    if (operation === "render" && output) {
      bundle = renderAssociationBundle(
        JSON.parse(await readFile(input, "utf8")),
      );
      await writeAssociationBundle(output, bundle);
    } else if (operation === "verify" && !output)
      bundle = await readAssociationBundle(input);
    else fail();
    if(!bundle)throw new Error("Association operation did not produce a bundle.");
    console.log(
      JSON.stringify({
        bundleSha256: bundle.bundleSha256,
        paths: bundle.files.map((file) => file.path),
        enabled: bundle.input.enabled,
      }),
    );
  } catch {
    console.error(
      "Native association preparation failed. No publication was performed.",
    );
    process.exit(1);
  }
}
