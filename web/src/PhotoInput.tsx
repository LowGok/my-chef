import { useEffect, useRef, useState, type RefObject } from "react";
import {
  mediaPlatform,
  NativeMediaError,
  type PhotoPurpose,
} from "./platform-media";

export function PhotoInput({
  purpose,
  inputRef,
  disabled,
  onFiles,
  onError,
  id,
  "aria-describedby": hint,
  field,
}: {
  purpose: PhotoPurpose;
  inputRef?: RefObject<HTMLInputElement | null>;
  disabled?: boolean;
  onFiles(files: File[]): boolean | void;
  onError(message: string): void;
  id?: string;
  "aria-describedby"?: string;
  field?: string;
}) {
  const port = mediaPlatform(),
    active = useRef(true),
    pending = useRef(false);
  const [busy, setBusy] = useState(false),
    [count, setCount] = useState(0);
  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
    };
  }, []);
  async function pick() {
    if (!port || pending.current || disabled) return;
    pending.current = true;
    setBusy(true);
    try {
      const files = await port.pickPhotos(purpose);
      if (active.current && files.length && onFiles(files) !== false)
        setCount(files.length);
    } catch (error) {
      if (
        active.current &&
        !(error instanceof DOMException && error.name === "AbortError")
      )
        onError(
          error instanceof NativeMediaError
            ? error.message
            : "The photo could not be opened. Please try again.",
        );
    } finally {
      pending.current = false;
      if (active.current) setBusy(false);
    }
  }
  return (
    <input
      ref={inputRef}
      id={id}
      aria-describedby={hint}
      data-personal-field={field}
      type={port ? "button" : "file"}
      multiple={purpose === "recipe-pages"}
      accept={
        purpose === "dish"
          ? "image/jpeg,image/png,image/webp"
          : "image/jpeg,image/png,image/heic,.heic"
      }
      disabled={disabled || busy}
      value={
        port
          ? busy
            ? "Opening photos…"
            : count
              ? `${count} photo${count === 1 ? "" : "s"} selected · Change`
              : "Choose photos"
          : undefined
      }
      onClick={
        port
          ? (event) => {
              event.preventDefault();
              void pick();
            }
          : undefined
      }
      onChange={(event) => {
        if (
          !port &&
          onFiles(Array.from(event.currentTarget.files ?? [])) === false
        )
          event.currentTarget.value = "";
      }}
    />
  );
}
