import { api } from "./api";

export function parseReminderReference(value: unknown): string | null {
  return typeof value === "string" && /^[0-9a-f]{32}$/.test(value)
    ? value
    : null;
}

export type NativeReminderBridge = {
  reminderPermission(input: { prompt: boolean }): Promise<{ allowed: boolean }>;
  reminderRegistration(): Promise<{ token: string | null; allowed: boolean }>;
  stopReminderRegistration(): Promise<unknown>;
  openReminderSettings(): Promise<unknown>;
};
let bridge: NativeReminderBridge | null = null;
let activated = false;
let epoch = 0;
let writes = Promise.resolve();
let lastToken = "";

export function setReminderBridge(value: NativeReminderBridge) {
  bridge = value;
}

export const hasReminderBridge = () => bridge !== null;
export const reminderDeviceActive = () => activated;
const deviceChanged = () => globalThis.window?.dispatchEvent(new Event("mychef-reminder-device"));

const installation = "/planning-reminders/installation";

function queueUpdate(fn: () => Promise<void>) {
  const result = writes.then(fn);
  writes = result.catch(() => undefined);
  return result;
}

export function forgetReminderDevice() {
  epoch++;
  activated = false;
  lastToken = "";
  return bridge?.stopReminderRegistration().catch(() => undefined);
}

export async function syncReminderDevice() {
  if (!bridge || !activated) return;
  const requestEpoch = epoch;
  const result = await bridge.reminderRegistration();
  if (requestEpoch !== epoch || !activated) return;
  const { allowed, token } = result;
  await queueUpdate(async () => {
    if (requestEpoch !== epoch || !activated) return;
    if (!allowed) {
      await api(installation, { method: "DELETE" });
      if (requestEpoch !== epoch) return;
      lastToken = "";
      activated = false;
      deviceChanged();
      return;
    }
    if (
      typeof token !== "string" ||
      !/^[A-Za-z0-9_:-]{32,512}$/.test(token) ||
      token === lastToken
    )
      return;
    await api(installation, {
      method: "PUT",
      body: JSON.stringify({ token, osAllowed: true }),
    });
    if (requestEpoch === epoch) {
      lastToken = token;
      deviceChanged();
    }
  });
}

export async function enableReminderDevice(prompt: boolean) {
  if (!bridge) return false;
  const current = ++epoch;
  const result = await bridge.reminderPermission({ prompt });
  if (current !== epoch) return false;
  activated = result.allowed;
  deviceChanged();
  if (activated) await syncReminderDevice();
  else
    await queueUpdate(async () => {
      if (epoch !== current) return;
      await api(installation, { method: "DELETE" });
      lastToken = "";
    });
  return result.allowed;
}

export async function disableReminderDevice() {
  const stopped = forgetReminderDevice();
  const current = epoch;
  await queueUpdate(async () => {
    if (current !== epoch) return;
    await api(installation, { method: "DELETE" });
  });
  deviceChanged();
  await stopped;
}

export async function showReminderOSSettings() {
  await bridge?.openReminderSettings();
}

let pending: string | null = null;
export function captureReminderOpen(value: unknown) {
  const reference = parseReminderReference(value);
  if (!reference) return;
  pending = reference;
  globalThis.window?.dispatchEvent(new Event("mychef-reminder-open"));
}
export const peekReminderOpen = () => pending;
export function clearReminderOpen(reference: string) {
  if (pending === reference) pending = null;
}
