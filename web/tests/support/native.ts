import type { BrowserContext, APIRequestContext } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { resolve, join } from "node:path";
import type { NativeShoppingSnapshot } from "../../src/native-shopping";
import type { RuntimeSnapshot } from "../../src/native-runtime";
import type { NativeMediaBridge } from "../../src/platform-media";
import { personalPhoto } from "./personal-photo";
// This models OS ports around the real bundle/native server. It is deliberately
// not evidence of an Android install, filesystem encryption or Keystore use.
export async function fakeNative(
  context: BrowserContext,
  server: APIRequestContext,
) {
  let refresh: string | null = null,
    value: NativeShoppingSnapshot | null = null;
  let generation = crypto.randomUUID(),
    network = true,
    failStorage = false,
    access = "";
  let photos: Awaited<ReturnType<NativeMediaBridge["pickPhotos"]>>["files"] = [
    { mimeType: "image/png", data: personalPhoto.toString("base64") },
  ];
  let pickerCalls = 0,
    saveAllowed = true,
    failSave = false,
    recipeSaves = 0,
    photoReads = 0,
    pageImports = 0;
  let exportResponse: Buffer | null = null;
  const exports: { filename: string; bytes: Buffer }[] = [],
    links: string[] = [];
  let runtime: RuntimeSnapshot = { active: true, online: true, back: false };
  const bundle = resolve(import.meta.dirname, "../../dist/app");
  await context.route("**/app/**", async (route) => {
    const path =
      new URL(route.request().url()).pathname.slice("/app/".length) ||
      "index.html";
    if (path.includes("..")) return route.abort();
    const ext = path.split(".").at(-1)!;
    const type: Record<string, string> = {
      html: "text/html",
      js: "application/javascript",
      css: "text/css",
      png: "image/png",
      woff: "font/woff",
      woff2: "font/woff2",
    };
    try {
      await route.fulfill({
        body: await readFile(join(bundle, path)),
        contentType: type[ext] ?? "application/octet-stream",
      });
    } catch {
      await route.fulfill({ status: 404, body: "" });
    }
  });
  async function invoke(method: string, input: any) {
    if (method === "readRuntime") {
      const result = { ...runtime, online: network };
      runtime = {
        ...runtime,
        back: false,
        link: undefined,
        invalidLink: undefined,
      };
      return result;
    }
    if (method === "removeListener") return;
    if (method === "configuration")
      return {
        platform: "android",
        apiOrigin: "https://fixture-node.tailtest.ts.net:8977",
      };
    if (method === "readRefresh") return { value: refresh };
    if (method === "writeRefresh") {
      refresh = input.value;
      return;
    }
    if (method === "readShopping") return { value, generation };
    if (method === "clearShopping") {
      value = null;
      generation = crypto.randomUUID();
      return { generation };
    }
    if (method === "writeShopping") {
      if (failStorage || input.generation !== generation)
        throw new Error("Synthetic protected-store failure");
      value = structuredClone(input.value);
      return;
    }
    if (method === "cancelRequest" || method === "cancelMedia") return;
    if (method === "pickPhotos") {
      pickerCalls++;
      return { files: structuredClone(photos) };
    }
    if (method === "saveExport") {
      if (saveAllowed)
        exports.push({
          filename: input.filename,
          bytes: Buffer.from(input.data, "base64"),
        });
      return { saved: saveAllowed };
    }
    if (method === "openExternal") {
      links.push(input.url);
      return;
    }
    if (method === "publicImage")
      return { mimeType: "image/png", data: personalPhoto.toString("base64") };
    if (method !== "request" || !network)
      throw new Error("Synthetic native network offline");
    if (!input.path.startsWith("/api/native/v1/") || input.headers.cookie)
      throw new Error("Unsafe native request");
    if (input.headers.authorization) access = input.headers.authorization;
    const photoSave =
      input.path === "/api/native/v1/personal-recipes" &&
      input.method === "POST";
    if (photoSave) recipeSaves++;
    if (input.path === "/api/native/v1/recipe-imports/photos") pageImports++;
    if (/^\/api\/native\/v1\/personal-photos\/[a-f0-9]{32}$/.test(input.path))
      photoReads++;
    const response = await server.fetch(input.path, {
      method: input.method,
      headers: input.headers,
      data: input.body ? Buffer.from(input.body, "base64") : undefined,
      maxRedirects: 0,
    });
    const body = await response.body();
    if (input.path === "/api/native/v1/account/export" && response.ok())
      exportResponse = body;
    if (photoSave && failSave && response.ok()) {
      failSave = false;
      throw new Error("Synthetic uncertain save");
    }
    return {
      status: response.status(),
      headers: response.headers(),
      body: body.toString("base64"),
    };
  }
  await context.exposeBinding(
    "__mychefNative",
    async (_source, method: string, input: any) => ({
      ...((await invoke(method, input)) ?? {}),
      _callNonce: input._callNonce,
    }),
  );
  await context.addInitScript(() => {
    const win = window as any;
    win.CapacitorCustomPlatform = { name: "android" };
    const methods = [
      "configuration",
      "readRuntime",
      "removeListener",
      "readRefresh",
      "writeRefresh",
      "readShopping",
      "writeShopping",
      "clearShopping",
      "request",
      "cancelRequest",
      "cancelMedia",
      "pickPhotos",
      "saveExport",
      "openExternal",
      "publicImage",
    ];
    win.Capacitor = {
      PluginHeaders: [
        {
          name: "MyChefNative",
          methods: [
            ...methods.map((name) => ({ name, rtype: "promise" })),
            { name: "addListener", rtype: "callback" },
          ],
        },
      ],
      nativeCallback: (
        _plugin: string,
        method: string,
        _input: unknown,
        callback: () => void,
      ) => {
        if (method === "addListener") {
          win.__mychefRuntimeWake = callback;
          return "fixture-runtime";
        }
      },
      nativePromise: (_plugin: string, method: string, input: unknown) =>
        win.__mychefNative(method, input),
    };
  });
  return {
    runtime: async (value: Partial<RuntimeSnapshot>) => {
      runtime = { ...runtime, ...value };
      for (const page of context.pages())
        await page.evaluate(() => (window as any).__mychefRuntimeWake?.({}));
    },
    network: (enabled: boolean) => {
      network = enabled;
    },
    failStorage: (enabled: boolean) => {
      failStorage = enabled;
    },
    snapshot: () => value,
    access: () => access,
    refresh: () => refresh,
    photos: (value: typeof photos) => {
      photos = value;
    },
    pickerCalls: () => pickerCalls,
    failNextSave: () => {
      failSave = true;
    },
    recipeSaves: () => recipeSaves,
    photoReads: () => photoReads,
    pageImports: () => pageImports,
    saveAllowed: (value: boolean) => {
      saveAllowed = value;
    },
    exports: () => exports,
    exportResponse: () => exportResponse,
    links: () => links,
  };
}
