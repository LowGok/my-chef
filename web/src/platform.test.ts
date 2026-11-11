import { afterEach, expect, test } from "bun:test";
import {
  NativeTransport,
  platformRequest,
  setPlatformTransport,
  type NativeCredentialStore,
} from "./platform";

const originalFetch = globalThis.fetch;
afterEach(() => {
  setPlatformTransport(null);
  globalThis.fetch = originalFetch;
});
const now = 1_800_000_000_000;
function tokens(access = "a", refresh = "b") {
  return {
    accessToken: access.repeat(64),
    refreshToken: refresh.repeat(64),
    accessExpiresUnix: now / 1000 + 300,
    sessionExpiresUnix: now / 1000 + 3600,
  };
}
const reply = (value: unknown, status = 200) =>
  new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json" },
  });
function credentialStore(initial: string | null = null) {
  let saved = initial;
  const writes: (string | null)[] = [];
  const store: NativeCredentialStore = {
    read: async () => saved,
    write: async (value) => {
      writes.push(value);
      saved = value;
    },
  };
  return { store, writes, value: () => saved };
}

test("web transport preserves the existing fetch contract", async () => {
  let received: unknown[] = [];
  globalThis.fetch = (async (...args: unknown[]) => {
    received = args;
    return reply({ ok: true });
  }) as typeof fetch;
  const options = { credentials: "same-origin" as const };
  await platformRequest("/api/v1/profile", options);
  expect(received).toEqual(["/api/v1/profile", options]);
});

test("replacement-account credentials wait for private-cache erasure", async () => {
  const vault = credentialStore();
  let started!: () => void, release!: () => void;
  const ready = new Promise<void>((resolve) => {
    started = resolve;
  });
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  let erased = false;
  const transport = new NativeTransport({
    platform: "ios",
    credentials: vault.store,
    now: () => now,
    beforeAccount: async () => {
      started();
      await gate;
      erased = true;
    },
    send: async (path) =>
      path.endsWith("/login")
        ? reply({
            native: tokens("c", "d"),
            user: { id: "2", accountInstance: "e".repeat(32) },
          })
        : reply({ native: tokens(), user: null, csrfToken: "" }),
  });
  const login = transport.request("/api/v1/login", {
    method: "POST",
    body: "{}",
  });
  // Without the cache barrier login resolves and persists the replacement first.
  const observed = await Promise.race([
    ready.then(() => "erasing"),
    login.then(() => "accepted"),
  ]);
  expect(observed).toBe("erasing");
  expect(vault.value()).toBe("b".repeat(64));
  release();
  await login;
  expect(erased).toBe(true);
  expect(vault.value()).toBe("d".repeat(64));
});

test("native bootstrap and sign-in persist only refresh credentials and isolate headers", async () => {
  const vault = credentialStore();
  const requests: { path: string; options: RequestInit }[] = [];
  const transport = new NativeTransport({
    platform: "ios",
    credentials: vault.store,
    now: () => now,
    send: async (path, options) => {
      requests.push({ path, options });
      if (path.endsWith("/session") && options.method === "POST")
        return reply({ native: tokens(), user: null, csrfToken: "" }, 201);
      if (path.endsWith("/login"))
        return reply({ native: tokens("c", "d"), user: { id: "1" } });
      return reply({ user: null, csrfToken: "" });
    },
  });
  await transport.request("/api/v1/session");
  const signed = await transport.request("/api/v1/login", {
    method: "POST",
    body: "{}",
    headers: {
      Cookie: "ignored",
      Authorization: "ignored",
      "X-CSRF-Token": "ignored",
    },
  });
  expect((await signed.json()).native).toBeUndefined();
  expect(vault.writes).toEqual([
    tokens().refreshToken,
    tokens("c", "d").refreshToken,
  ]);
  const login = requests.find((request) => request.path.endsWith("/login"))!;
  const headers = new Headers(login.options.headers);
  expect(headers.get("Authorization")).toBe("Bearer " + tokens().accessToken);
  expect(headers.has("Cookie")).toBe(false);
  expect(headers.has("X-CSRF-Token")).toBe(false);
  expect(login.options.credentials).toBe("omit");
  expect(login.options.redirect).toBe("error");
  expect(
    requests.every((request) => request.path.startsWith("/api/native/v1/")),
  ).toBe(true);
  expect(JSON.parse(requests[0].options.body as string)).toEqual({
    platform: "ios",
  });
});

test("concurrent native requests share one refresh exchange", async () => {
  const vault = credentialStore(tokens().refreshToken);
  let refreshes = 0;
  const transport = new NativeTransport({
    platform: "android",
    credentials: vault.store,
    now: () => now,
    send: async (path, options) => {
      if (path.endsWith("/refresh")) {
        refreshes++;
        expect(JSON.parse(options.body as string).refreshToken).toBe(
          tokens().refreshToken,
        );
        return reply({ native: tokens("c", "d") });
      }
      expect(new Headers(options.headers).get("Authorization")).toBe(
        "Bearer " + tokens("c", "d").accessToken,
      );
      return reply({ ok: true });
    },
  });
  await Promise.all(
    Array.from({ length: 12 }, () => transport.request("/api/v1/profile")),
  );
  expect(refreshes).toBe(1);
  expect(vault.value()).toBe(tokens("c", "d").refreshToken);
});

test("late concurrent 401 responses reuse the rotation and preserve one mutation retry", async () => {
  const vault = credentialStore(tokens().refreshToken);
  let refreshes = 0,
    staleRequests = 0,
    retried = 0;
  let releaseLate!: () => void;
  const late = new Promise<void>((resolve) => {
    releaseLate = resolve;
  });
  const transport = new NativeTransport({
    platform: "ios",
    credentials: vault.store,
    now: () => now,
    send: async (path, options) => {
      if (path.endsWith("/refresh")) {
        refreshes++;
        return reply({
          native: refreshes === 1 ? tokens("c", "d") : tokens("e", "f"),
        });
      }
      if (path.endsWith("/session")) return reply({ user: { id: "1" } });
      expect(new Headers(options.headers).get("Idempotency-Key")).toBe(
        "same-mutation-key",
      );
      expect(options.body).toBe('{"change":"same"}');
      if (
        new Headers(options.headers).get("Authorization") ===
        "Bearer " + tokens("c", "d").accessToken
      ) {
        staleRequests++;
        if (staleRequests === 2) await late;
        return reply({ error: { code: "unauthenticated" } }, 401);
      }
      retried++;
      releaseLate();
      return reply({ ok: true });
    },
  });
  await transport.request("/api/v1/session");
  await Promise.all(
    Array.from({ length: 2 }, () =>
      transport.request("/api/v1/generations", {
        method: "POST",
        body: '{"change":"same"}',
        headers: { "Idempotency-Key": "same-mutation-key" },
      }),
    ),
  );
  expect(refreshes).toBe(2);
  expect(staleRequests).toBe(2);
  expect(retried).toBe(2);
});

test("provider errors and network uncertainty do not retry a native mutation", async () => {
  for (const mode of ["unavailable", "network"]) {
    const vault = credentialStore(tokens().refreshToken);
    let mutations = 0;
    const transport = new NativeTransport({
      platform: "ios",
      credentials: vault.store,
      now: () => now,
      send: async (path) => {
        if (path.endsWith("/refresh"))
          return reply({ native: tokens("c", "d") });
        mutations++;
        if (mode === "network") throw new Error("synthetic interruption");
        return reply({ error: { code: "provider_unavailable" } }, 503);
      },
    });
    if (mode === "network")
      await expect(
        transport.request("/api/v1/generations", { method: "POST" }),
      ).rejects.toThrow();
    else
      expect(
        (await transport.request("/api/v1/generations", { method: "POST" }))
          .status,
      ).toBe(503);
    expect(mutations).toBe(1);
  }
});

test("native requests cannot forward credentials to another origin or traversal path", async () => {
  const vault = credentialStore();
  let calls = 0;
  const transport = new NativeTransport({
    platform: "ios",
    credentials: vault.store,
    now: () => now,
    send: async () => {
      calls++;
      return reply({});
    },
  });
  for (const path of [
    "https://foreign.test/api/v1/profile",
    "//foreign.test/profile",
    "/api/v1/../private",
    "/api/v1/%2e%2e/private",
    "/api/v1/profile#proof",
  ]) {
    await expect(transport.request(path)).rejects.toThrow();
  }
  expect(calls).toBe(0);
});

test("forgetting a native account fences a late sign-in response and clears protected credentials", async () => {
  const vault = credentialStore(tokens().refreshToken);
  let finishLogin!: (response: Response) => void;
  const login = new Promise<Response>((resolve) => {
    finishLogin = resolve;
  });
  let loginStarted!: () => void;
  const started = new Promise<void>((resolve) => {
    loginStarted = resolve;
  });
  const transport = new NativeTransport({
    platform: "ios",
    credentials: vault.store,
    now: () => now,
    send: async (path) => {
      if (path.endsWith("/refresh")) return reply({ native: tokens("c", "d") });
      if (path.endsWith("/logout")) return new Response(null, { status: 204 });
      if (path.endsWith("/login")) {
        loginStarted();
        return login;
      }
      return reply({ user: { id: "1" } });
    },
  });
  await transport.request("/api/v1/session");
  const pending = transport.request("/api/v1/login", {
    method: "POST",
    body: "{}",
  });
  await started;
  await transport.forget();
  finishLogin(reply({ native: tokens("e", "f"), user: { id: "2" } }));
  await expect(pending).rejects.toThrow();
  expect(vault.value()).toBeNull();
  expect(vault.writes).not.toContain(tokens("e", "f").refreshToken);
});

test("a protected-store failure never falls back to web storage or sends a private request", async () => {
  let privateRequests = 0;
  const transport = new NativeTransport({
    platform: "ios",
    now: () => now,
    credentials: {
      read: async () => tokens().refreshToken,
      write: async () => {
        throw new Error("store unavailable");
      },
    },
    send: async (path) => {
      if (path.endsWith("/refresh")) return reply({ native: tokens("c", "d") });
      privateRequests++;
      return reply({});
    },
  });
  await expect(transport.request("/api/v1/profile")).rejects.toThrow();
  expect(privateRequests).toBe(0);
});

test("native profile-first signup accepts its acknowledgement without inventing new credentials", async () => {
  const vault = credentialStore(tokens().refreshToken);
  const transport = new NativeTransport({
    platform: "ios",
    credentials: vault.store,
    now: () => now,
    send: async (path) =>
      path.endsWith("/refresh")
        ? reply({ native: tokens("c", "d") })
        : reply({ status: "accepted" }, 202),
  });
  const result = await transport.request("/api/v1/register", {
    method: "POST",
    body: "{}",
  });
  expect(result.status).toBe(202);
  expect(await result.json()).toEqual({ status: "accepted" });
  expect(vault.writes).toEqual([tokens("c", "d").refreshToken]);
});

test("revoked stored refresh can start anonymous session recovery but cannot retry a private action", async () => {
  for (const path of ["/api/v1/session", "/api/v1/profile"]) {
    const vault = credentialStore(tokens().refreshToken);
    let starts = 0,
      privateCalls = 0;
    const transport = new NativeTransport({
      platform: "ios",
      credentials: vault.store,
      now: () => now,
      send: async (target, options) => {
        if (target.endsWith("/refresh"))
          return reply({ error: { code: "unauthenticated" } }, 401);
        if (options.method === "POST") {
          starts++;
          return reply(
            { native: tokens("e", "f"), user: null, csrfToken: "" },
            201,
          );
        }
        if (target.endsWith("/profile")) privateCalls++;
        return reply({ user: null, csrfToken: "" });
      },
    });
    const result = await transport.request(path);
    expect(result.status).toBe(path.endsWith("/session") ? 200 : 401);
    expect(starts).toBe(path.endsWith("/session") ? 1 : 0);
    expect(privateCalls).toBe(0);
  }
});

test("aborting while native credentials rotate prevents the private request from being sent", async () => {
  const vault = credentialStore(tokens().refreshToken);
  const controller = new AbortController();
  let privateCalls = 0;
  const transport = new NativeTransport({
    platform: "ios",
    credentials: vault.store,
    now: () => now,
    send: async (target) => {
      if (target.endsWith("/refresh")) {
        controller.abort();
        return reply({ native: tokens("c", "d") });
      }
      privateCalls++;
      return reply({});
    },
  });
  await expect(
    transport.request("/api/v1/profile", { signal: controller.signal }),
  ).rejects.toThrow();
  expect(privateCalls).toBe(0);
});
