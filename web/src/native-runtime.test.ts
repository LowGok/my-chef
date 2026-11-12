import { expect, test } from "bun:test";
import {
  createNativeRuntime,
  validateNativeProof,
  type RuntimeSnapshot,
} from "./native-runtime";
const origin = "https://fixture-node.tailtest.ts.net:8977";
const proof = "a".repeat(43);
test("account links accept only exact private HTTPS proof routes without stored navigation", () => {
  for (const kind of [
    "signup",
    "verify-email",
    "change-email",
    "reset-password",
  ] as const)
    expect(
      validateNativeProof(`${origin}/app/${kind}#${proof}`, origin),
    ).toEqual({ kind, proof });
  for (const url of [
    `https://foreign.test/app/signup#${proof}`,
    `${origin}/app/signup?token=x#${proof}`,
    `${origin}/app/%73ignup#${proof}`,
    `${origin}/app/signup#short`,
    `${origin}/app/recipe#${proof}`,
    `https://user:pass@fixture-node.tailtest.ts.net:8977/app/signup#${proof}`,
  ])
    expect(validateNativeProof(url, origin)).toBeNull();
});
test("runtime wakeups reveal no payload and pull links once through a verified bridge", async () => {
  let snapshot: RuntimeSnapshot = {
    active: true,
    online: true,
    back: false,
    link: `${origin}/app/reset-password#${proof}`,
  };
  const events: unknown[] = [];
  const runtime = createNativeRuntime(
    {
      async readRuntime() {
        const result = snapshot;
        snapshot = { active: true, online: true, back: false };
        return result;
      },
    },
    origin,
    (event) => events.push(event),
  );
  await runtime.wake();
  expect(events).toEqual([
    { type: "connection", active: true, online: true },
    { type: "proof", entry: { kind: "reset-password", proof } },
  ]);
  await runtime.wake();
  expect(events).toHaveLength(2);
  snapshot = { active: false, online: false, back: false };
  await runtime.wake();
  snapshot = {
    active: true,
    online: true,
    back: true,
    link: `${origin}/app/signup#short`,
  };
  await runtime.wake();
  expect(events.slice(-3)).toEqual([
    { type: "connection", active: true, online: true },
    { type: "invalid-link" },
    { type: "back" },
  ]);
});
test("runtime sanitizes bridge failures and suppresses replies after shutdown", async () => {
  let done!: (value: RuntimeSnapshot) => void;
  const events: unknown[] = [];
  const runtime = createNativeRuntime(
    { readRuntime: () => new Promise((resolve) => (done = resolve)) },
    origin,
    (event) => events.push(event),
  );
  const wake = runtime.wake();
  runtime.stop();
  done({
    active: true,
    online: true,
    back: true,
    link: `${origin}/app/signup#${proof}`,
  });
  await wake;
  expect(events).toEqual([]);
  const broken = createNativeRuntime(
    {
      async readRuntime() {
        throw new Error("private canary");
      },
    },
    origin,
    (event) => events.push(event),
  );
  await broken.wake();
  expect(events).toEqual([{ type: "unavailable" }]);
});
