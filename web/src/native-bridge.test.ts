import { expect, test } from "bun:test";
import { isolateNativeBridge } from "./native-bridge";

test("native replies must echo the unique call nonce before private values are accepted", async () => {
  const seen: string[] = [];
  const raw = {
    async readRefresh(input: Record<string, unknown>) {
      seen.push(input._callNonce as string);
      return { _callNonce: input._callNonce, value: "a".repeat(64) };
    },
  };
  const bridge = isolateNativeBridge<{
    readRefresh(): Promise<{ value: string }>;
  }>(raw);
  expect(await bridge.readRefresh()).toEqual({ value: "a".repeat(64) });
  await bridge.readRefresh();
  expect(seen[0]).toMatch(/^[a-f0-9-]{36}$/);
  expect(seen[0]).not.toBe(seen[1]);
});

test("a callback collision with an older view fails without exposing the old payload or OS error", async () => {
  const raw = {
    async readRefresh() {
      return { _callNonce: "old-view", value: "PRIVATE_PREVIOUS_VIEW" };
    },
  };
  const bridge = isolateNativeBridge<{ readRefresh(): Promise<unknown> }>(raw);
  await expect(bridge.readRefresh()).rejects.toThrow(
    "The native app response could not be verified.",
  );
  raw.readRefresh = async () => {
    throw new Error("PRIVATE_OS_ERROR");
  };
  await expect(bridge.readRefresh()).rejects.toThrow(
    "The native app response could not be verified.",
  );
});
