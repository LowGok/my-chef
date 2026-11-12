import { useEffect, useRef, useState, type ReactNode } from "react";
import { mediaPlatform } from "./platform-media";
import { FoodPlaceholder } from "./components";

export const recipeImage = (url: string | null | undefined) =>
  !!url &&
  (url.startsWith("https://") ||
    /^\/api\/v1\/personal-photos\/[0-9a-f]{32}$/.test(url));

export function RecipeImage({
  url,
  fallback = <FoodPlaceholder />,
}: {
  url: string | null | undefined;
  fallback?: ReactNode;
}) {
  const port = mediaPlatform();
  const [src, setSrc] = useState<string | null>(() =>
    !port && recipeImage(url) ? url! : null,
  );
  const invalidated = useRef(false);
  const pending = useRef<AbortController | null>(null);
  const owned = useRef<(() => void) | null>(null);
  useEffect(
    () =>
      port?.subscribe(() => {
        invalidated.current = true;
        pending.current?.abort();
        owned.current?.();
        owned.current = null;
        setSrc(null);
      }),
    [port],
  );
  useEffect(() => {
    const request = new AbortController();
    pending.current = request;
    invalidated.current = false;
    let active = true,
      release: (() => void) | undefined;
    setSrc(null);
    if (!recipeImage(url)) return;
    if (!port) {
      setSrc(url!);
      return;
    }
    void port
      .loadImage(url!, request.signal)
      .then((value) => {
        if (!active || invalidated.current) {
          value.release();
          return;
        }
        release = value.release;
        owned.current = value.release;
        setSrc(value.url);
      })
      .catch(() => {
        if (active) setSrc(null);
      });
    return () => {
      active = false;
      request.abort();
      if (pending.current === request) pending.current = null;
      release?.();
      if (owned.current === release) owned.current = null;
    };
  }, [url, port]);
  return src ? (
    <img
      src={src}
      alt=""
      loading="lazy"
      onError={() => {
        owned.current?.();
        owned.current = null;
        setSrc(null);
      }}
    />
  ) : (
    <>{fallback}</>
  );
}
