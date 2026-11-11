import { afterEach, expect, test } from "bun:test";
import {
  rememberShoppingOwner,
  saveShoppingCache,
  loadShoppingCache,
  pendingShoppingWeeks,
  hasPendingShopping,
  localShoppingSignOut,
} from "./shopping-cache";
const originalStorage = globalThis.localStorage,
  originalWindow = globalThis.window;
let values = new Map<string, string>();
let canary = "";
afterEach(() => {
  localShoppingSignOut();
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: originalStorage,
  });
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: originalWindow,
  });
});
function fixture() {
  values = new Map();
  canary = ["IMPORT", "CACHE", "PRIVATE", crypto.randomUUID()].join("-");
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: {
      get length() {
        return values.size;
      },
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => values.set(key, value),
      removeItem: (key: string) => values.delete(key),
      key: (index: number) => [...values.keys()][index] ?? null,
    },
  });
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: { dispatchEvent: () => true },
  });
  const profile = {
    id: "1",
    accountInstance: "a".repeat(32),
    email: "fixture@example.test",
    username: "fixture",
    firstName: "Fixture",
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
  rememberShoppingOwner(profile, "2026-11-09");
  return profile;
}
function packet(privateImports = true) {
  return {
    base: {
      week: "2026-11-09",
      revision: 1,
      sourceVersion: "fixture",
      privateImports,
      meals: [
        {
          position: "2026-11-09/0",
          recipeId: "1",
          date: "2026-11-09",
          title: canary,
          selected: true,
        },
      ],
      items: [
        {
          id: "line",
          name: canary,
          quantity: "1",
          unit: "piece",
          category: "Other ingredients",
          state: "buy" as const,
          updated: false,
          manual: false,
          contributions: [
            { recipeId: "1", title: canary, quantity: "1", unit: "piece" },
          ],
        },
      ],
    },
    queue: [
      {
        key: "queued",
        edit: { kind: "state" as const, id: "line", state: "basket" as const },
        before: {
          quantity: "1",
          state: "buy" as const,
          name: canary,
          unit: "piece",
          manual: false,
        },
      },
    ],
  };
}
test("imported shopping content and pending edits stay in memory and disappear on sign out", () => {
  const owner = fixture();
  saveShoppingCache(owner.id, "2026-11-09", packet());
  expect([...values.values()].join(" ").includes(canary)).toBe(false);
  expect(loadShoppingCache(owner.id, "2026-11-09")?.base.items.length).toBe(1);
  expect(hasPendingShopping(owner.id)).toBe(true);
  expect(pendingShoppingWeeks(owner.id)).toEqual(["2026-11-09"]);
  localShoppingSignOut(true);
  expect(loadShoppingCache(owner.id, "2026-11-09")).toBe(null);
});
test("a refreshed imported week removes its older durable cache before retaining new content", () => {
  const owner = fixture();
  saveShoppingCache(owner.id, "2026-11-09", packet(false));
  expect(values.has("mychef.shopping.list.1.2026-11-09")).toBe(true);
  saveShoppingCache(owner.id, "2026-11-09", packet());
  expect(values.has("mychef.shopping.list.1.2026-11-09")).toBe(false);
  expect([...values.values()].join(" ").includes(canary)).toBe(false);
});

test("pending edits from a removed imported meal keep their private source details out of storage", () => {
  const owner = fixture();
  const original = packet();
  saveShoppingCache(owner.id, "2026-11-09", original);
  const refreshed = packet(false);
  refreshed.base.meals = [];
  refreshed.base.items = [];
  saveShoppingCache(owner.id, "2026-11-09", refreshed);
  expect([...values.values()].join(" ").includes(canary)).toBe(false);
  expect(loadShoppingCache(owner.id, "2026-11-09")?.queue.length).toBe(1);
  expect(hasPendingShopping(owner.id)).toBe(true);
});
