import { expect, test } from "bun:test";
import { disableReminderDevice, enableReminderDevice, forgetReminderDevice, parseReminderReference, setReminderBridge, syncReminderDevice } from "./reminder-device";
import { setPlatformTransport } from "./platform";

test("device registration follows account opt-in and OS permission", async () => {
  const events: string[] = [];
  let allowed = false;
  let token: string | null = null;
  const local = new Map<string, string>();
  const originalStorage = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
  Object.defineProperty(globalThis, "localStorage", { configurable: true,
    value: { getItem: (key: string) => local.get(key) ?? null } });
  setReminderBridge({
    reminderPermission: async ({ prompt }) => {
      events.push(prompt ? "prompt" : "sync");
      return { allowed };
    },
    reminderRegistration: async () => ({ allowed, token }),
    stopReminderRegistration: async () => {
      events.push("stop");
    },
    openReminderSettings: async () => undefined,
  });
  setPlatformTransport({ native: true, forget: async () => undefined,
    request: async (path, options) => {
      expect(path).toBe("/api/v1/planning-reminders/installation");
      events.push(options?.method ?? "GET");
      return new Response(null, { status: 204 });
    },
  });
  try {
    await syncReminderDevice();
    expect(events).toEqual([]);
    expect(await enableReminderDevice(true)).toBe(false);
    expect(events.splice(0)).toEqual(["prompt", "DELETE"]);
    allowed = true;
    expect(await enableReminderDevice(true)).toBe(true);
    expect(events.splice(0)).toEqual(["prompt"]);
    token = "a".repeat(64);
    await syncReminderDevice();
    await syncReminderDevice();
    token = "b".repeat(64);
    await syncReminderDevice();
    expect(events.splice(0)).toEqual(["PUT", "PUT"]);
    await disableReminderDevice();
    await syncReminderDevice();
    expect(events).toEqual(["stop", "DELETE"]);
  } finally {
    await forgetReminderDevice();
    setPlatformTransport(null);
    if (originalStorage) Object.defineProperty(globalThis, "localStorage", originalStorage);
    else Reflect.deleteProperty(globalThis, "localStorage");
  }
});

test("reminder taps accept opaque references only", () => {
  expect(parseReminderReference("a".repeat(32))).toBe("a".repeat(32));
  for (const wrong of [
    "2026-11-16",
    "https://mychef.dev/app/",
    "A".repeat(32),
    "a".repeat(33),
    undefined,
  ]) {
    expect(parseReminderReference(wrong)).toBeNull();
  }
});
