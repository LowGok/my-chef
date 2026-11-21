import { expect, test } from "bun:test";
import {
  appOpeningLink,
  createIdentityAuthorization,
  validAppOpeningLink,
} from "./native-app-identity";

test("the public opening link is exact and belongs only to the app lane", () => {
  expect(validAppOpeningLink(appOpeningLink, "app")).toBe(true);
  for (const lane of ["preview", "unknown", undefined])
    expect(validAppOpeningLink(appOpeningLink, lane)).toBe(false);
  for (const value of [
    appOpeningLink + "?x=1",
    appOpeningLink + "#x",
    appOpeningLink + "/",
    "https://user@mychef.dev/app/open",
    "http://mychef.dev/app/open",
    "https://mychef.dev:443/app/open",
    "https://mychef.dev/app/%6fpen",
    "https://mychef.dev/other/../app/open",
    "https://foreign.test/app/open",
  ])
    expect(validAppOpeningLink(value, "app")).toBe(false);
});

test("candidate authorization starts closed and never falls back to an old online result", async () => {
  let reply: unknown = { active: true };
  const seen: boolean[] = [];
  const authorization = createIdentityAuthorization(
    "app",
    async () => reply,
    (value) => seen.push(value),
  );
  expect(authorization.allowed()).toBe(false);
  expect(await authorization.refresh()).toBe(true);
  expect(authorization.allowed()).toBe(true);
  reply = { active: false };
  expect(await authorization.refresh()).toBe(false);
  reply = { active: true };
  expect(await authorization.refresh()).toBe(true);
  reply = null;
  expect(await authorization.refresh()).toBe(false);
  expect(seen).toEqual([true, false, true, false]);
});

test("an old status response cannot reopen the candidate after an offline cutoff", async () => {
  let resolve!: (value: unknown) => void;
  const authorization = createIdentityAuthorization(
    "app",
    () => new Promise((done) => (resolve = done)),
    () => {},
  );
  const pending = authorization.refresh();
  authorization.suspend();
  resolve({ active: true });
  await pending;
  expect(authorization.allowed()).toBe(false);
});

test("Preview needs no candidate status and keeps its offline behavior", async () => {
  let reads = 0;
  const authorization = createIdentityAuthorization(
    "preview",
    async () => {
      reads++;
      throw new Error();
    },
    () => {},
  );
  expect(authorization.allowed()).toBe(true);
  expect(await authorization.refresh()).toBe(true);
  authorization.suspend();
  expect(authorization.allowed()).toBe(true);
  expect(reads).toBe(0);
});
