import { expect, test } from "bun:test";
import {
  createNativeTransport,
  runNativeStartupStep,
  NativeStartupError,
  type NativeBridge,
} from "./native-platform";

test("native startup failures carry a stable reference without bridge error data", async () => {
  const details = "synthetic private native diagnostic";
  const error = await runNativeStartupStep("storage", () => {
    throw new Error(details);
  }).catch((failure: unknown) => failure);
  expect(error).toBeInstanceOf(NativeStartupError);
  expect((error as NativeStartupError).reference).toBe("MYC-02");
  expect(String(error)).not.toContain(details);
  expect((error as Error).cause).toBeUndefined();
  await expect(runNativeStartupStep("device", async () => true)).resolves.toBe(
    true,
  );
});

function bridge() {
  let refresh: string | null = null;
  const calls: Parameters<NativeBridge["request"]>[0][] = [];
  const native: NativeBridge = {
    async configuration() {
      return {
        platform: "ios",
        apiOrigin: "https://fixture-node.tailtest.ts.net:8977",
      };
    },
    async readRefresh() {
      return { value: refresh };
    },
    async writeRefresh({ value }) {
      refresh = value;
    },
    async cancelRequest() {},
    async request(value) {
      calls.push(value);
      const json =
        value.path.endsWith("/session") && value.method === "POST"
          ? {
              native: {
                accessToken: "a".repeat(64),
                refreshToken: "b".repeat(64),
                accessExpiresUnix: Math.floor(Date.now() / 1000) + 300,
                sessionExpiresUnix: Math.floor(Date.now() / 1000) + 3600,
              },
            }
          : { ok: true };
      return {
        status: 200,
        headers: { "Content-Type": "application/json" },
        body: btoa(JSON.stringify(json)),
      };
    },
  };
  return { native, calls, stored: () => refresh };
}

test("native bootstrap uses only the protected bridge and marshals API bodies without cookies", async () => {
  const { native, calls, stored } = bridge();
  const transport = await createNativeTransport(native, "ios");
  const data = new FormData();
  data.set(
    "photo",
    new File(["synthetic photo"], "dish.jpg", { type: "image/jpeg" }),
  );
  await transport.request("/api/v1/personal-recipes/photo", {
    method: "POST",
    body: data,
    headers: {
      Cookie: "private",
      Origin: "foreign",
      "Idempotency-Key": "same-operation",
    },
  });
  expect(stored()).toBe("b".repeat(64));
  const last = calls.at(-1)!;
  expect(last.path).toBe("/api/native/v1/personal-recipes/photo");
  expect(last.headers.authorization).toBe("Bearer " + "a".repeat(64));
  expect(last.headers.cookie).toBeUndefined();
  expect(last.headers.origin).toBeUndefined();
  expect(last.headers["idempotency-key"]).toBe("same-operation");
  expect(last.headers["content-type"]).toContain(
    "multipart/form-data; boundary=",
  );
  expect(atob(last.body!)).toContain("synthetic photo");
  expect(
    JSON.stringify(await (await transport.request("/api/v1/menu")).json()),
  ).toBe('{"ok":true}');
});

test("a native session read never invokes WebView body serialization", async () => {
  const { native, calls } = bridge();
  const transport = await createNativeTransport(native, "ios");
  const read = Request.prototype.arrayBuffer;
  Request.prototype.arrayBuffer = function () {
    if (this.method === "GET")
      return Promise.reject(
        new Error("This WebView cannot read an absent body"),
      );
    return read.call(this);
  };
  try {
    expect((await transport.request("/api/v1/session")).ok).toBe(true);
    expect(calls.at(-1)).toMatchObject({
      path: "/api/native/v1/session",
      method: "GET",
      headers: { authorization: "Bearer " + "a".repeat(64) },
    });
    expect(calls.at(-1)?.body).toBeUndefined();
  } finally {
    Request.prototype.arrayBuffer = read;
  }
});

test("native bootstrap blocks foreign configuration, unavailable secure storage and redirects", async () => {
  for (const configuration of [
    { platform: "android", apiOrigin: "https://localhost" },
    { platform: "ios", apiOrigin: "https://foreign.test" },
    { platform: "ios", apiOrigin: "http://localhost" },
  ]) {
    const { native, calls } = bridge();
    native.configuration = async () => configuration;
    await expect(createNativeTransport(native, "ios")).rejects.toThrow();
    expect(calls).toHaveLength(0);
  }
  const { native, calls } = bridge();
  native.readRefresh = async () => {
    throw new Error("private OS detail");
  };
  const transport = await createNativeTransport(native, "ios");
  await expect(transport.request("/api/v1/session")).rejects.toThrow(
    "Protected device storage is unavailable.",
  );
  expect(calls).toHaveLength(0);
  const second = bridge();
  second.native.request = async () => ({
    status: 302,
    headers: { location: "https://foreign.test" },
    body: "",
  });
  const redirect = await createNativeTransport(second.native, "ios");
  await expect(redirect.request("/api/v1/session")).rejects.toThrow();
});

test("native cancellation stops the OS request and exposes no sensitive bridge error", async () => {
  const { native } = bridge();
  const transport = await createNativeTransport(native, "ios");
  // Refresh is shared with concurrent callers; cancel the individual request.
  await transport.request("/api/v1/session");
  const cancelled: string[] = [];
  let started!: () => void;
  const waiting = new Promise<void>((resolve) => {
    started = resolve;
  });
  native.cancelRequest = async ({ requestId }) => {
    cancelled.push(requestId);
  };
  native.request = async () => {
    started();
    return new Promise(() => {});
  };
  const controller = new AbortController();
  const result = transport.request("/api/v1/session", {
    signal: controller.signal,
  });
  await waiting;
  controller.abort();
  await expect(result).rejects.toMatchObject({ name: "AbortError" });
  expect(cancelled).toHaveLength(1);
  const other = bridge();
  other.native.request = async () => {
    throw new Error("Authorization: secret");
  };
  const failing = await createNativeTransport(other.native, "ios");
  await expect(failing.request("/api/v1/session")).rejects.toThrow(
    "The private API request failed.",
  );
});
