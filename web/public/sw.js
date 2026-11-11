/* Only the static application shell belongs here. Private data lives in owner-scoped storage. */
const SHELL = "mychef-shell-v2";
const allowed = (url) =>
  url.origin === self.location.origin &&
  /^\/app\/assets\/[a-zA-Z0-9_.-]+\.(js|css|woff2?|png|svg)$/.test(
    url.pathname,
  );

async function cacheShell(response) {
  const cache = await caches.open(SHELL),
    seen = new Set();
  async function references(response) {
    const text = await response.text();
    const paths = [
      ...text.matchAll(
        /\/app\/assets\/[a-zA-Z0-9_.-]+\.(?:js|css|woff2?|png|svg)/g,
      ),
    ].map((m) => m[0]);
    await Promise.all(
      paths
        .filter((path) => allowed(new URL(path, self.location.origin)))
        .map(asset),
    );
  }
  async function asset(path) {
    const url = new URL(path, self.location.origin);
    if (seen.has(url.href)) return;
    seen.add(url.href);
    const result = await fetch(url, { cache: "reload" });
    if (!result.ok) throw new Error("Application shell asset unavailable");
    await cache.put(url, result.clone());
    if (/\.(js|css)$/.test(url.pathname)) await references(result);
  }
  await references(response.clone());
  // Keep the last usable HTML until every referenced asset is cached successfully.
  await cache.put(new URL("/app/", self.location.origin), response);
}
self.addEventListener("install", (event) =>
  event.waitUntil(
    (async () => {
      const response = await fetch("/app/", { cache: "reload" });
      if (!response.ok) throw new Error("Application shell unavailable");
      await cacheShell(response);
      await self.skipWaiting();
    })(),
  ),
);
self.addEventListener("activate", (event) =>
  event.waitUntil(self.clients.claim()),
);
self.addEventListener("fetch", (event) => {
  const url = new URL(event.request.url);
  if (event.request.method !== "GET" || url.origin !== self.location.origin)
    return;
  // Only the exact app root is an offline navigation. Proof pages, including
  // /app/reset-password, always go to the network and stay out of the cache.
  if (event.request.mode === "navigate" && url.pathname === "/app/") {
    event.respondWith(
      (async () => {
        try {
          const response = await fetch(event.request);
          if (response.ok)
            event.waitUntil(cacheShell(response.clone()).catch(() => {}));
          return response;
        } catch {
          return (await caches.open(SHELL)).match("/app/");
        }
      })(),
    );
  } else if (allowed(url)) {
    event.respondWith(
      (async () => {
        const cache = await caches.open(SHELL);
        try {
          const response = await fetch(event.request);
          if (response.ok) await cache.put(event.request, response.clone());
          return response;
        } catch (error) {
          const saved = await cache.match(event.request);
          if (saved) return saved;
          throw error;
        }
      })(),
    );
  }
});
