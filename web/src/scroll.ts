/** Place committed content below navigation without letting focus scroll it again. */
export function focusContentTop(
  target: HTMLElement | null,
  heading: HTMLElement | null,
): (() => void) | undefined {
  if (
    !target?.isConnected ||
    !heading?.isConnected ||
    !target.getClientRects().length ||
    target.closest("[hidden]")
  )
    return;
  const modal = document.querySelector<HTMLDialogElement>("dialog:modal");
  if (modal && !modal.contains(heading)) {
    let cancelRetry: (() => void) | undefined;
    const retry = () => {
      cancelRetry = focusContentTop(target, heading);
    };
    modal.addEventListener("close", retry, { once: true });
    return () => {
      modal.removeEventListener("close", retry);
      cancelRetry?.();
    };
  }
  const box = target.getBoundingClientRect();
  const navigation = document.querySelector<HTMLElement>(".sidebar");
  const nav = navigation?.getBoundingClientRect();
  const position = navigation && getComputedStyle(navigation).position;
  const boundary =
    nav &&
    (position === "sticky" || position === "fixed") &&
    nav.right > box.left &&
    nav.left < box.right
      ? Math.max(0, nav.bottom)
      : 0;
  const maximum = Math.max(
    0,
    document.documentElement.scrollHeight - window.innerHeight,
  );
  const top = Math.min(
    maximum,
    Math.max(0, window.scrollY + box.top - boundary - 12),
  );
  heading.focus({ preventScroll: true });
  window.scrollTo({ top, left: 0, behavior: "instant" });
  return undefined;
}
