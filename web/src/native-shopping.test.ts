import { afterEach, expect, test } from "bun:test";
import {
  openNativeShoppingStorage,
  type NativeShoppingBridge,
  type NativeShoppingSnapshot,
} from "./native-shopping";
import {
  setShoppingStorage,
  flushShoppingCache,
  rememberShoppingOwner,
  saveShoppingCache,
  loadShoppingCache,
  pendingShoppingWeeks,
  localShoppingSignOut,
  shoppingCacheEpoch,
  forgetShoppingDevice,
} from "./shopping-cache";
import type { Profile } from "./api";
import { createNativeTransport, type NativeBridge } from "./native-platform";
const originalWindow = globalThis.window;
afterEach(() => {
  setShoppingStorage(null);
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: originalWindow,
  });
});
const profile: Profile = {
  id: "1",
  accountInstance: "a".repeat(32),
  email: "synthetic@example.test",
  username: "synthetic",
  firstName: "Synthetic",
  lastName: "Cook",
  birthdate: null,
  sex: null,
  preferences: {
    diet: [],
    allergens: [],
    dislikes: [],
    technique: [],
    effort: "",
  },
};
function packet(week = "2026-11-09", privateImports = false) {
  return {
    base: {
      week,
      revision: 1,
      sourceVersion: "fixture",
      privateImports,
      meals: [],
      items: [
        {
          id: "line",
          name: privateImports ? "PRIVATE_IMPORT_CANARY" : "Carrots",
          quantity: "1",
          unit: "piece",
          category: "Vegetables",
          state: "buy" as const,
          updated: false,
          manual: false,
          contributions: [],
        },
      ],
    },
    queue: [
      {
        key: "same-offline-operation",
        edit: { kind: "state" as const, id: "line", state: "basket" as const },
        before: {
          quantity: "1",
          state: "buy" as const,
          name: "Carrots",
          unit: "piece",
          manual: false,
        },
      },
    ],
  };
}
function fixture() {
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: { dispatchEvent: () => true },
  });
  let stored: NativeShoppingSnapshot | null = null;
  let fail = false;
  let generation = crypto.randomUUID();
  const writes: (NativeShoppingSnapshot | null)[] = [];
  const bridge: NativeShoppingBridge = {
    async readShopping() {
      return { value: structuredClone(stored), generation };
    },
    async clearShopping() {
      stored = null;
      generation = crypto.randomUUID();
      return { generation };
    },
    async writeShopping({ value, generation: expected }) {
      if (fail) throw new Error("private OS details");
      if (expected !== generation) throw new Error("stale native write");
      stored = structuredClone(value);
      writes.push(structuredClone(value));
    },
  };
  return {
    bridge,
    writes,
    stored: () => stored,
    fail: (value: boolean) => {
      fail = value;
    },
  };
}

test("native ordinary shopping and exact pending edits survive relaunch across cached weeks", async () => {
  const os = fixture();
  const first = await openNativeShoppingStorage(os.bridge);
  setShoppingStorage(first);
  rememberShoppingOwner(profile, "2026-11-09");
  saveShoppingCache(profile.id, "2026-11-09", packet());
  saveShoppingCache(profile.id, "2026-11-16", packet("2026-11-16"));
  await flushShoppingCache();
  expect(os.stored()?.accountId).toBe(profile.id);
  expect(os.stored()?.accountInstance).toBe(profile.accountInstance!);
  const relaunched = await openNativeShoppingStorage(os.bridge);
  setShoppingStorage(relaunched);
  expect(pendingShoppingWeeks(profile.id)).toEqual([
    "2026-11-09",
    "2026-11-16",
  ]);
  expect(loadShoppingCache(profile.id, "2026-11-09")?.queue[0].key).toBe(
    "same-offline-operation",
  );
  const changes = loadShoppingCache(profile.id, "2026-11-09")!;
  changes.queue.push({ ...changes.queue[0], key: "second-offline-operation" });
  saveShoppingCache(profile.id, "2026-11-09", changes);
  await flushShoppingCache();
  setShoppingStorage(await openNativeShoppingStorage(os.bridge));
  expect(loadShoppingCache(profile.id, "2026-11-09")?.queue).toHaveLength(2);
});

test("imported shopping never enters the native snapshot and sign-out erases ordinary content", async () => {
  const os = fixture();
  setShoppingStorage(await openNativeShoppingStorage(os.bridge));
  rememberShoppingOwner(profile, "2026-11-09");
  saveShoppingCache(profile.id, "2026-11-09", packet());
  await flushShoppingCache();
  saveShoppingCache(profile.id, "2026-11-09", packet("2026-11-09", true));
  await flushShoppingCache();
  expect(JSON.stringify(os.writes)).not.toContain("PRIVATE_IMPORT_CANARY");
  expect(loadShoppingCache(profile.id, "2026-11-09")?.base.privateImports).toBe(
    true,
  );
  setShoppingStorage(await openNativeShoppingStorage(os.bridge));
  expect(loadShoppingCache(profile.id, "2026-11-09")).toBeNull();
  saveShoppingCache(profile.id, "2026-11-16", packet("2026-11-16"));
  await flushShoppingCache();
  localShoppingSignOut(true);
  await flushShoppingCache();
  expect(os.stored()).toBeNull();
  expect(loadShoppingCache(profile.id, "2026-11-16")).toBeNull();
});

test("storage failure keeps edits in memory, reports failure and retries without web fallback", async () => {
  const os = fixture();
  setShoppingStorage(await openNativeShoppingStorage(os.bridge));
  rememberShoppingOwner(profile, "2026-11-09");
  os.fail(true);
  saveShoppingCache(profile.id, "2026-11-09", packet());
  await expect(flushShoppingCache()).rejects.toThrow(
    "Shopping could not be saved securely on this device.",
  );
  expect(loadShoppingCache(profile.id, "2026-11-09")?.queue).toHaveLength(1);
  os.fail(false);
  await flushShoppingCache();
  setShoppingStorage(await openNativeShoppingStorage(os.bridge));
  expect(loadShoppingCache(profile.id, "2026-11-09")?.queue).toHaveLength(1);
});

test("a snapshot with a substituted account incarnation is rejected", async () => {
  const os = fixture();
  setShoppingStorage(await openNativeShoppingStorage(os.bridge));
  rememberShoppingOwner(profile, "2026-11-09");
  saveShoppingCache(profile.id, "2026-11-09", packet());
  await flushShoppingCache();
  const saved = os.stored()!;
  os.bridge.readShopping = async () => ({
    value: { ...saved, accountInstance: "b".repeat(32) },
    generation: crypto.randomUUID(),
  });
  await expect(openNativeShoppingStorage(os.bridge)).rejects.toThrow();
});

test("a late write from the old account incarnation cannot change the replacement cache", async () => {
  const os = fixture();
  setShoppingStorage(await openNativeShoppingStorage(os.bridge));
  rememberShoppingOwner(profile, "2026-11-09");
  const replacement = { ...profile, accountInstance: "b".repeat(32) };
  expect(() => rememberShoppingOwner(replacement, "2026-11-09")).toThrow();
  localShoppingSignOut();
  await flushShoppingCache();
  // A new incarnation can start after erasure, but old callers remain fenced.
  const { explicitShoppingSignIn } = await import("./shopping-cache");
  explicitShoppingSignIn();
  rememberShoppingOwner(replacement, "2026-11-09");
  saveShoppingCache(
    profile.id,
    "2026-11-09",
    packet(),
    profile.accountInstance,
  );
  await flushShoppingCache();
  expect(loadShoppingCache(profile.id, "2026-11-09")).toBeNull();
  expect(os.stored()?.accountInstance).toBe(replacement.accountInstance);
});

test("erasure fences an outstanding native write before the replacement account persists", async () => {
  const os = fixture();
  const storage = await openNativeShoppingStorage(os.bridge);
  setShoppingStorage(storage);
  let started!: () => void, release!: () => void;
  const ready = new Promise<void>((resolve) => {
    started = resolve;
  });
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  const original = os.bridge.writeShopping;
  let first = true;
  os.bridge.writeShopping = async (value) => {
    if (first) {
      first = false;
      started();
      await gate;
    }
    await original(value);
  };
  rememberShoppingOwner(profile, "2026-11-09");
  saveShoppingCache(profile.id, "2026-11-09", packet());
  const oldWrite = flushShoppingCache().catch(() => undefined);
  await ready;
  await storage.erase();
  const replacement = { ...profile, id: "2", accountInstance: "c".repeat(32) };
  rememberShoppingOwner(replacement, "2026-11-16");
  release();
  await oldWrite;
  await flushShoppingCache();
  expect(os.stored()?.accountId).toBe("2");
  expect(os.stored()?.payload).not.toContain("Carrots");
});

test("the real native factory erases the prior account before storing replacement credentials", async () => {
  const os = fixture();
  setShoppingStorage(await openNativeShoppingStorage(os.bridge));
  rememberShoppingOwner(profile, "2026-11-09");
  saveShoppingCache(profile.id, "2026-11-09", packet());
  await flushShoppingCache();
  const replacement = { ...profile, id: "2", accountInstance: "b".repeat(32) };
  let refresh: string | null = "e".repeat(64),
    checked = false;
  const native: NativeBridge = {
    async configuration() {
      return { platform: "ios", apiOrigin: "https://localhost:8977" };
    },
    async readRefresh() {
      return { value: refresh };
    },
    async writeRefresh({ value }) {
      if (value === "d".repeat(64)) {
        expect(os.stored()).toBeNull();
        expect(loadShoppingCache(profile.id, "2026-11-09")).toBeNull();
        checked = true;
      }
      refresh = value;
    },
    async cancelRequest() {},
    async request({ path }) {
      const login = path.endsWith("/login");
      return {
        status: 200,
        headers: { "Content-Type": "application/json" },
        body: btoa(
          JSON.stringify({
            user: login ? replacement : profile,
            csrfToken: "",
            native: {
              accessToken: (login ? "c" : "a").repeat(64),
              refreshToken: (login ? "d" : "e").repeat(64),
              accessExpiresUnix: Math.floor(Date.now() / 1000) + 300,
              sessionExpiresUnix: Math.floor(Date.now() / 1000) + 3600,
            },
          }),
        ),
      };
    },
  };
  const transport = await createNativeTransport(native, "ios");
  await transport.request("/api/v1/login", { method: "POST", body: "{}" });
  expect(checked).toBe(true);
});

test("a delayed background reader cannot remember the old account after erasure", async () => {
  const os = fixture();
  setShoppingStorage(await openNativeShoppingStorage(os.bridge));
  rememberShoppingOwner(profile, "2026-11-09");
  await flushShoppingCache();
  const started = shoppingCacheEpoch();
  await forgetShoppingDevice();
  rememberShoppingOwner(profile, "2026-11-09", started);
  saveShoppingCache(
    profile.id,
    "2026-11-09",
    packet(),
    profile.accountInstance,
    started,
  );
  await flushShoppingCache();
  expect(os.stored()).toBeNull();
});
