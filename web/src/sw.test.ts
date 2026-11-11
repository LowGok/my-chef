import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";

test("offline shell follows the last successful connected application load", async () => {
  const origin = "https://mychef.test",
    entries = new Map<string, Response>();
  const handlers: Record<string, (event: unknown) => void> = {};
  let html = '<script src="/app/assets/old.js"></script>',
    offline = false;
  const key = (request: Request | URL | string) =>
    new URL(
      typeof request === "string"
        ? request
        : request instanceof URL
          ? request.href
          : request.url,
      origin,
    ).href;
  const cache = {
    async put(request: Request | URL | string, response: Response) {
      entries.set(key(request), response.clone());
    },
    async match(request: Request | URL | string) {
      return entries.get(key(request))?.clone();
    },
  };
  runInNewContext(
    readFileSync(new URL("../public/sw.js", import.meta.url), "utf8"),
    {
      self: {
        location: { origin },
        addEventListener: (name: string, handler: (event: unknown) => void) => {
          handlers[name] = handler;
        },
        skipWaiting: async () => {},
        clients: { claim: async () => {} },
      },
      caches: { open: async () => cache },
      URL,
      Request,
      Response,
      fetch: async (request: Request | URL | string) => {
        if (offline) throw new Error("offline");
        if (key(request).endsWith("/missing.js"))
          return new Response("", { status: 503 });
        return new Response(
          new URL(key(request)).pathname === "/app/"
            ? html
            : "// application asset",
          {
            headers: {
              "Content-Type":
                new URL(key(request)).pathname === "/app/"
                  ? "text/html"
                  : "text/javascript",
            },
          },
        );
      },
    },
  );
  let installed!: Promise<unknown>;
  handlers.install({
    waitUntil: (promise: Promise<unknown>) => {
      installed = promise;
    },
  });
  await installed;
  async function navigation() {
    let response!: Promise<Response>;
    const work: Promise<unknown>[] = [];
    handlers.fetch({
      request: {
        method: "GET",
        mode: "navigate",
        url: origin + "/app/?view=shopping",
      },
      waitUntil: (promise: Promise<unknown>) => work.push(promise),
      respondWith: (promise: Promise<Response>) => {
        response = promise;
      },
    });
    const result = await response;
    await Promise.all(work);
    return result;
  }
  expect(await (await navigation()).text()).toContain("old.js");
  html = '<script src="/app/assets/new.js"></script>';
  expect(await (await navigation()).text()).toContain("new.js");
  expect(entries.has(origin + "/app/assets/new.js")).toBe(true);
  offline = true;
  expect(await (await navigation()).text()).toContain("new.js");
  let asset!: Promise<Response>;
  handlers.fetch({
    request: new Request(origin + "/app/assets/new.js"),
    respondWith: (promise: Promise<Response>) => {
      asset = promise;
    },
  });
  expect(await (await asset).text()).toBe("// application asset");
  offline = false;
  html = '<script src="/app/assets/missing.js"></script>';
  expect(await (await navigation()).text()).toContain("missing.js");
  offline = true;
  expect(await (await navigation()).text()).toContain("new.js");
  let intercepted = false;
  handlers.fetch({
    request: new Request(origin + "/api/v1/shopping/2026-11-02"),
    respondWith: () => {
      intercepted = true;
    },
  });
  expect(intercepted).toBe(false);
  for (const path of [
    "/api/v1/personal-photos/" + "a".repeat(32),
    "/api/v1/personal-recipes/" + "b".repeat(32),
    "/api/v1/personal-recipes/search",
  ]) {
    handlers.fetch({
      request: new Request(origin + path),
      respondWith: () => {
        intercepted = true;
      },
    });
    expect(intercepted).toBe(false);
  }
  for (const path of ["/", "/landing/"]) {
    handlers.fetch({
      request: { method: "GET", mode: "navigate", url: origin + path },
      respondWith: () => {
        intercepted = true;
      },
    });
    expect(intercepted).toBe(false);
  }
  expect([...entries.keys()].every((url) => !url.includes("/api/"))).toBe(true);
});
