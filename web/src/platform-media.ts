import { platformRequest } from "./platform";
import { encodeNativeBytes, decodeNativeBytes } from "./native-bytes";

export type PhotoPurpose = "dish" | "recipe-pages";
type NativePhoto = { mimeType: string; data: string };
export type NativeMediaBridge = {
  pickPhotos(input: {
    purpose: PhotoPurpose;
  }): Promise<{ files: NativePhoto[] }>;
  publicImage(input: { url: string }): Promise<NativePhoto>;
  saveExport(input: {
    filename: string;
    data: string;
  }): Promise<{ saved: boolean }>;
  openExternal(input: { url: string }): Promise<void>;
  cancelMedia(): Promise<void>;
};
export class NativeMediaError extends Error {
  constructor(message = "The file could not be opened. Please try again.") {
    super(message);
  }
}
const stale = () => new DOMException("The account changed.", "AbortError");
const mb = 1024 * 1024;
const types: Record<string, string> = {
  "image/jpeg": "jpg",
  "image/png": "png",
  "image/webp": "webp",
  "image/heic": "heic",
};
function publicPhoto(value: string) {
  const url = new URL(value);
  if (
    url.origin !== "https://res.cloudinary.com" ||
    url.username ||
    url.password ||
    url.hash ||
    value.length > 4096
  )
    throw new NativeMediaError("This photo is unavailable.");
  return url.href;
}
function imageBytes(bytes: Uint8Array, mime: string) {
  if (!bytes.length || bytes.length > 8 * mb)
    throw new NativeMediaError("This photo is unavailable.");
  const prefix = String.fromCharCode(...bytes.slice(0, 16));
  if (
    (mime === "image/jpeg" &&
      bytes[0] === 255 &&
      bytes[1] === 216 &&
      bytes[2] === 255) ||
    (mime === "image/png" &&
      bytes[0] === 137 &&
      prefix.slice(1, 4) === "PNG") ||
    (mime === "image/webp" &&
      prefix.startsWith("RIFF") &&
      prefix.slice(8, 12) === "WEBP")
  )
    return;
  throw new NativeMediaError("This photo is unavailable.");
}

export function createNativeMedia(
  bridge: NativeMediaBridge,
  request = platformRequest,
) {
  let epoch = 0,
    picking = false,
    clearing: Promise<void> | null = null;
  const images = new Set<string>(),
    listeners = new Set<() => void>();
  function check(started: number, signal?: AbortSignal) {
    if (started !== epoch || signal?.aborted) throw stale();
  }
  return {
    subscribe(listener: () => void) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    clear() {
      epoch++;
      for (const url of images) URL.revokeObjectURL(url);
      images.clear();
      for (const listener of listeners) listener();
      if (!clearing) {
        const work = bridge.cancelMedia().catch(() => {
          throw new NativeMediaError(
            "Private files could not be cleared. Unlock the device and try again.",
          );
        });
        clearing = work.finally(() => {
          clearing = null;
        });
        void clearing.catch(() => undefined);
      }
      return clearing;
    },
    async pickPhotos(purpose: PhotoPurpose): Promise<File[]> {
      if (picking || !["dish", "recipe-pages"].includes(purpose))
        throw new NativeMediaError();
      picking = true;
      const started = epoch;
      try {
        const { files } = await bridge.pickPhotos({ purpose });
        check(started);
        if (
          !Array.isArray(files) ||
          files.length > (purpose === "dish" ? 1 : 3)
        )
          throw new NativeMediaError();
        let total = 0;
        return files.map((file, index) => {
          const allowed =
            purpose === "dish"
              ? ["image/jpeg", "image/png", "image/webp"]
              : ["image/jpeg", "image/png", "image/heic"];
          if (!allowed.includes(file.mimeType))
            throw new NativeMediaError(
              "Choose supported photos within the size limits.",
            );
          const bytes = decodeNativeBytes(
            file.data,
            (purpose === "dish" ? 12 : 10) * mb,
          );
          total += bytes.length;
          if (!bytes.length || total > 12 * mb)
            throw new NativeMediaError(
              "Choose supported photos within the size limits.",
            );
          return new File(
            [bytes],
            purpose === "dish"
              ? `recipe-photo.${types[file.mimeType]}`
              : `recipe-page-${index + 1}.${types[file.mimeType]}`,
            { type: file.mimeType },
          );
        });
      } catch (error) {
        check(started);
        if (error instanceof NativeMediaError) throw error;
        throw new NativeMediaError(
          "Choose supported photos within the size limits.",
        );
      } finally {
        picking = false;
      }
    },
    async loadImage(value: string, signal?: AbortSignal) {
      const started = epoch;
      check(started, signal);
      try {
        let bytes: Uint8Array<ArrayBuffer>, mime: string;
        if (/^\/api\/v1\/personal-photos\/[a-f0-9]{32}$/.test(value)) {
          const response = await request(value, {
            method: "GET",
            signal,
            cache: "no-store",
          });
          if (!response.ok) throw new NativeMediaError();
          mime =
            response.headers.get("Content-Type")?.split(";")[0].trim() ?? "";
          bytes = new Uint8Array(await response.arrayBuffer());
        } else {
          const response = await bridge.publicImage({
            url: publicPhoto(value),
          });
          mime = response.mimeType;
          bytes = decodeNativeBytes(response.data, 8 * mb);
        }
        check(started, signal);
        imageBytes(bytes, mime);
        const url = URL.createObjectURL(new Blob([bytes], { type: mime }));
        images.add(url);
        return {
          url,
          release() {
            if (images.delete(url)) URL.revokeObjectURL(url);
          },
        };
      } catch (error) {
        check(started, signal);
        throw new NativeMediaError("This photo is unavailable.");
      }
    },
    async saveExport(value: {
      blob: Blob;
      filename: string;
    }): Promise<boolean> {
      const started = epoch;
      try {
        if (
          !/^mychef-data-[a-f0-9]{32}\.zip$/.test(value.filename) ||
          value.blob.type !== "application/zip" ||
          value.blob.size < 22 ||
          value.blob.size > 32 * mb
        )
          throw new NativeMediaError();
        const bytes = new Uint8Array(await value.blob.arrayBuffer());
        check(started);
        if (
          new DataView(bytes.buffer).getUint32(bytes.length - 22, true) !==
          0x06054b50
        )
          throw new NativeMediaError();
        const result = await bridge.saveExport({
          filename: value.filename,
          data: encodeNativeBytes(bytes),
        });
        check(started);
        if (typeof result.saved !== "boolean") throw new NativeMediaError();
        return result.saved;
      } catch {
        check(started);
        throw new NativeMediaError(
          "The download could not be saved. Please try again.",
        );
      }
    },
    async openExternal(value: string) {
      try {
        const url = new URL(value);
        const mail =
          /^mailto:[A-Za-z0-9._+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$/.test(value);
        if (
          (!mail && url.protocol !== "https:") ||
          url.username ||
          url.password ||
          value.length > 4096 ||
          /[\r\n]/.test(value)
        )
          throw new NativeMediaError();
        await bridge.openExternal({ url: mail ? value : url.href });
      } catch {
        throw new NativeMediaError("This link could not be opened.");
      }
    },
  };
}
export type NativeMedia = ReturnType<typeof createNativeMedia>;
let selected: NativeMedia | null = null;
export const mediaPlatform = () => selected;
export function setMediaPlatform(value: NativeMedia | null) {
  selected?.clear();
  selected = value;
}
export function clearPlatformMedia() {
  return selected?.clear() ?? Promise.resolve();
}
export function embeddedAppLink(value: string, base: string) {
  try {
    const current = new URL(base),
      next = new URL(value, current);
    // WKWebView's custom scheme can have an opaque URL.origin. Compare the
    // actual scheme/authority, never two opaque "null" origins.
    return (
      !!current.host &&
      next.protocol === current.protocol &&
      next.host === current.host &&
      !next.username &&
      !next.password
    );
  } catch {
    return false;
  }
}
export function installNativeLinkHandling(onError: (message: string) => void) {
  const follow = (event: MouseEvent) => {
    const element =
      event.target instanceof Element ? event.target.closest("a[href]") : null;
    if (!(element instanceof HTMLAnchorElement) || !selected) return;
    const url = new URL(element.href);
    if (embeddedAppLink(url.href, location.href)) return;
    event.preventDefault();
    void selected
      .openExternal(url.href)
      .catch(() => onError("This link could not be opened."));
  };
  document.addEventListener("click", follow, true);
  return () => document.removeEventListener("click", follow, true);
}
