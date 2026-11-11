import { afterEach, expect, test } from "bun:test";
import {
  APIError,
  api,
  acceptSession,
  forgetSession,
  personalAccountReady,
} from "./api";

const originalFetch = globalThis.fetch;
const originalStorage = globalThis.localStorage;
afterEach(() => {
  forgetSession();
  globalThis.fetch = originalFetch;
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: originalStorage,
  });
});

test("personal actions require a complete verified account incarnation", () => {
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: { getItem: () => null },
  });
  const user = {
    id: "1",
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
  acceptSession({ csrfToken: "fixture", user });
  expect(personalAccountReady()).toBe(false);
  acceptSession({
    csrfToken: "fixture",
    user: { ...user, accountInstance: "a".repeat(32) },
  });
  expect(personalAccountReady()).toBe(true);
  acceptSession({
    csrfToken: "fixture",
    user: null,
    verificationRequired: true,
  });
  expect(personalAccountReady()).toBe(false);
  forgetSession();
  expect(personalAccountReady()).toBe(false);
});
test("photo requests preserve the browser multipart boundary and structured conflicts", async () => {
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: { getItem: () => null },
  });
  let sent: RequestInit | undefined;
  globalThis.fetch = (async (_url: unknown, options: RequestInit) => {
    sent = options;
    return new Response(
      JSON.stringify({
        error: {
          code: "personal_version_conflict",
          message: "Review the newer version.",
          current: { versionId: "new" },
          changedFields: ["title"],
        },
      }),
      { status: 409 },
    );
  }) as typeof fetch;
  const body = new FormData();
  body.append("photo", new Blob(["synthetic bytes"]), "recipe.jpg");
  try {
    await api("/personal-recipes", { method: "POST", body });
    throw new Error("Expected conflict");
  } catch (error) {
    expect(error).toBeInstanceOf(APIError);
    expect(new Headers(sent?.headers).has("Content-Type")).toBe(false);
    expect((error as APIError).details?.changedFields).toEqual(["title"]);
    expect((error as APIError).details?.current?.versionId).toBe("new");
  }
});
