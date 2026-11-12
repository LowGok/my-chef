export function encodeNativeBytes(bytes: Uint8Array) {
  let text = "";
  for (let offset = 0; offset < bytes.length; offset += 8192)
    text += String.fromCharCode(...bytes.subarray(offset, offset + 8192));
  return btoa(text);
}
export function decodeNativeBytes(value: string, maximum: number) {
  if (
    typeof value !== "string" ||
    value.length > Math.ceil(maximum / 3) * 4 ||
    value.length % 4 !== 0 ||
    !/^[A-Za-z0-9+/]*={0,2}$/.test(value)
  )
    throw new TypeError("Invalid native bytes.");
  const bytes = Uint8Array.from(atob(value), (character) =>
    character.charCodeAt(0),
  );
  if (bytes.length > maximum) throw new TypeError("Invalid native bytes.");
  return bytes;
}
