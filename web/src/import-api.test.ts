import { afterEach, expect, test } from "bun:test";
import { acceptSession, forgetSession, importApi } from "./api";
const originalFetch = globalThis.fetch,
  originalStorage = globalThis.localStorage;
afterEach(() => {
  forgetSession();
  globalThis.fetch = originalFetch;
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: originalStorage,
  });
});
function account() {
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: { getItem: () => null },
  });
  acceptSession({
    csrfToken: "fixture",
    user: {
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
    },
  });
}
test("import sources stay in POST bodies and page uploads omit original filenames", async () => {
  account();
  const calls: { path: string; options: RequestInit }[] = [];
  globalThis.fetch = (async (path: unknown, options: RequestInit) => {
    calls.push({ path: String(path), options });
    return new Response("{}", {
      headers: { "X-MyChef-User": "1", "Content-Type": "application/json" },
    });
  }) as typeof fetch;
  await importApi.url({
    requestKey: "a".repeat(32),
    url: "https://recipes.example.test/private-source-canary",
    personalUseConfirmed: true,
  });
  await importApi.photos(
    {
      requestKey: "b".repeat(32),
      personalUseConfirmed: true,
      aiConsentConfirmed: true,
    },
    [
      new File(["synthetic bytes"], "private-source-filename-canary.png", {
        type: "image/png",
      }),
    ],
  );
  expect(calls.length).toBe(2);
  expect(
    calls.every((c) => !c.path.includes("canary") && !c.path.includes("?")),
  ).toBe(true);
  expect(
    JSON.parse(calls[0].options.body as string).url.includes(
      "private-source-canary",
    ),
  ).toBe(true);
  const form = calls[1].options.body as FormData;
  expect(form instanceof FormData).toBe(true);
  expect([...form.keys()]).toEqual(["import", "photos"]);
  expect((form.get("photos") as File).name.includes("canary")).toBe(false);
  expect(new Headers(calls[1].options.headers).has("Content-Type")).toBe(false);
  expect(new Headers(calls[1].options.headers).get("X-CSRF-Token")).toBe(
    "fixture",
  );
});
