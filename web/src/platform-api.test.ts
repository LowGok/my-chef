import { afterEach, expect, test } from "bun:test";
import {
  acceptSession,
  canChangePassword,
  changePassword,
  forgetSession,
  sessionEpoch,
} from "./api";
import { setPlatformTransport } from "./platform";

const originalStorage = globalThis.localStorage;
const originalNavigator = globalThis.navigator;
afterEach(() => {
  setPlatformTransport(null);
  forgetSession();
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: originalStorage,
  });
  Object.defineProperty(globalThis, "navigator", {
    configurable: true,
    value: originalNavigator,
  });
});

test("native password change advances the account epoch without browser locks or CSRF rotation", async () => {
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: { getItem: () => null },
  });
  Object.defineProperty(globalThis, "navigator", {
    configurable: true,
    value: {},
  });
  const session = {
    csrfToken: "",
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
  };
  setPlatformTransport({
    native: true,
    forget: async () => undefined,
    request: async () =>
      new Response(JSON.stringify(session), {
        headers: { "Content-Type": "application/json", "X-MyChef-User": "1" },
      }),
  });
  acceptSession(session);
  const started = sessionEpoch();
  expect(canChangePassword()).toBe(true);
  await changePassword({
    currentPassword: "old test password",
    newPassword: "replacement test password",
  });
  expect(sessionEpoch()).toBeGreaterThan(started);
});
