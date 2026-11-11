import { afterEach, expect, test } from "bun:test";
import { createNativeMedia, type NativeMediaBridge } from "./platform-media";

const originalCreate = URL.createObjectURL,
  originalRevoke = URL.revokeObjectURL;
afterEach(() => {
  URL.createObjectURL = originalCreate;
  URL.revokeObjectURL = originalRevoke;
});
const jpeg = new Uint8Array([255, 216, 255, 224]);
function fixture() {
  const calls: { method: string; value?: unknown }[] = [];
  const bridge: NativeMediaBridge = {
    async pickPhotos(value) {
      calls.push({ method: "pick", value });
      return {
        files: [
          {
            mimeType: "image/jpeg",
            data: Buffer.from(jpeg).toString("base64"),
          },
        ],
      };
    },
    async publicImage(value) {
      calls.push({ method: "publicImage", value });
      return {
        mimeType: "image/jpeg",
        data: Buffer.from(jpeg).toString("base64"),
      };
    },
    async saveExport(value) {
      calls.push({ method: "export", value: { filename: value.filename } });
      return { saved: true };
    },
    async openExternal(value) {
      calls.push({ method: "external", value });
    },
    async cancelMedia() {
      calls.push({ method: "cancel" });
    },
  };
  const media = createNativeMedia(bridge, async (path, options) => {
    calls.push({ method: "api", value: { path, options } });
    return new Response(jpeg, { headers: { "Content-Type": "image/jpeg" } });
  });
  return { media, bridge, calls };
}

test("system photo picks remain in memory, use generic names and keep source/dish limits separate", async () => {
  const { media, bridge } = fixture();
  const dish = await media.pickPhotos("dish");
  expect(dish[0].name).toBe("recipe-photo.jpg");
  expect(dish[0].type).toBe("image/jpeg");
  expect(await dish[0].arrayBuffer()).toEqual(jpeg.buffer);
  bridge.pickPhotos = async () => ({ files: [] });
  expect(await media.pickPhotos("recipe-pages")).toEqual([]);
  bridge.pickPhotos = async () => ({
    files: [{ mimeType: "image/heic", data: "AAAA" }],
  });
  await expect(media.pickPhotos("dish")).rejects.toThrow();
  expect((await media.pickPhotos("recipe-pages"))[0].name).toBe(
    "recipe-page-1.heic",
  );
  bridge.pickPhotos = async () => ({
    files: Array.from({ length: 4 }, () => ({
      mimeType: "image/jpeg",
      data: "AAAA",
    })),
  });
  await expect(media.pickPhotos("recipe-pages")).rejects.toThrow();
});

test("private images use the authorized API path and public image fetches receive no credentials", async () => {
  const { media, calls } = fixture();
  const own = await media.loadImage(
    "/api/v1/personal-photos/" + "a".repeat(32),
  );
  expect(calls[0].method).toBe("api");
  expect(JSON.stringify(calls[0].value)).toContain("/api/v1/personal-photos/");
  own.release();
  const publicImage = await media.loadImage(
    "https://res.cloudinary.com/fixture/image/upload/dish.jpg",
  );
  expect(calls.at(-1)).toEqual({
    method: "publicImage",
    value: { url: "https://res.cloudinary.com/fixture/image/upload/dish.jpg" },
  });
  publicImage.release();
  const count = calls.length;
  for (const url of [
    "https://foreign.test/photo.jpg",
    "/api/v1/personal-photos/" + "a".repeat(32) + "?token=secret",
    "javascript:alert(1)",
  ])
    await expect(media.loadImage(url)).rejects.toThrow();
  expect(calls.length).toBe(count);
});

test("logout revokes visible object URLs and rejects delayed photo/picker results", async () => {
  const revoked: string[] = [];
  URL.createObjectURL = () => "blob:synthetic-private-photo";
  URL.revokeObjectURL = (value) => {
    revoked.push(value);
  };
  const { media, bridge, calls } = fixture();
  await media.loadImage(
    "https://res.cloudinary.com/fixture/image/upload/dish.jpg",
  );
  let finish!: (
    value: Awaited<ReturnType<NativeMediaBridge["pickPhotos"]>>,
  ) => void;
  bridge.pickPhotos = () =>
    new Promise((resolve) => {
      finish = resolve;
    });
  const picked = media.pickPhotos("dish");
  media.clear();
  finish({ files: [{ mimeType: "image/jpeg", data: "AAAA" }] });
  await expect(picked).rejects.toMatchObject({ name: "AbortError" });
  expect(revoked).toEqual(["blob:synthetic-private-photo"]);
  expect(calls.at(-1)?.method).toBe("cancel");
});

test("account exports use a validated ZIP name and wait for the system save result", async () => {
  const { media, bridge, calls } = fixture();
  const bytes = new Uint8Array(22);
  new DataView(bytes.buffer).setUint32(0, 0x06054b50, true);
  const value = {
    blob: new Blob([bytes], { type: "application/zip" }),
    filename: `mychef-data-${"a".repeat(32)}.zip`,
  };
  bridge.saveExport = async () => ({ saved: false });
  expect(await media.saveExport(value)).toBe(false);
  await expect(
    media.saveExport({ ...value, filename: "../../private.zip" }),
  ).rejects.toThrow();
  await expect(
    media.saveExport({
      ...value,
      blob: new Blob(["broken"], { type: "application/zip" }),
    }),
  ).rejects.toThrow();
  expect(calls).toEqual([]);
});

test("external links use only safe system-browser/mail paths and never receive API authorization", async () => {
  const { media, calls } = fixture();
  await media.openExternal("https://recipes.example.test/carrot-soup");
  await media.openExternal("mailto:admin@example.invalid");
  for (const url of [
    "javascript:alert(1)",
    "file:///private",
    "intent://foreign",
    "https://user:secret@foreign.test/",
    "mailto:admin@example.invalid?bcc=foreign@example.test",
  ])
    await expect(media.openExternal(url)).rejects.toThrow();
  expect(calls).toEqual([
    {
      method: "external",
      value: { url: "https://recipes.example.test/carrot-soup" },
    },
    { method: "external", value: { url: "mailto:admin@example.invalid" } },
  ]);
});

test("packaged iOS internal links are recognized even when the custom URL has an opaque origin", async () => {
  const { embeddedAppLink } = await import("./platform-media");
  const base = "capacitor://localhost/app/";
  expect(new URL(base).origin).toBe("null");
  expect(
    embeddedAppLink("capacitor://localhost/app/?view=forgot-password", base),
  ).toBe(true);
  for (const value of [
    "file:///private",
    "capacitor://foreign/app/",
    "https://recipes.example.test/soup",
    "capacitor://user:secret@localhost/app/",
  ])
    expect(embeddedAppLink(value, base)).toBe(false);
});
