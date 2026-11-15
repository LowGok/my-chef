import { installBillingStoreFixture } from "./native-billing-webview";

type SimulatorInput = { email: string; password: string; proofKey: string; expiresAt: number };

// This static program runs only in the explicitly compiled iOS simulator
// fixture. The test input is passed separately by WebKit as structured data.
async function runIOSBillingFixture(
  input: SimulatorInput,
  install: typeof installBillingStoreFixture,
) {
  const deadline = Math.min(Date.now() + 75_000, input.expiresAt);
  async function wait<T>(read: () => T | undefined): Promise<T> {
    while (Date.now() < deadline) {
      const result = read();
      if (result !== undefined) return result;
      await new Promise((resolve) => setTimeout(resolve, 150));
    }
    throw new Error("The disposable iOS subscription fixture did not finish.");
  }
  const exact = <T extends HTMLElement>(selector: string, text: string, root: ParentNode = document) =>
    [...root.querySelectorAll<T>(selector)].find((node) => node.textContent?.trim() === text);
  const form = await wait(() =>
    document.querySelector<HTMLInputElement>("#sign-in-email")?.form ?? undefined,
  );
  const completed = await install({
    platform: "ios", proofKey: input.proofKey,
    expiresAt: input.expiresAt, loseResult: false,
  });
  const email = form.querySelector<HTMLInputElement>("#sign-in-email");
  const password = form.querySelector<HTMLInputElement>("[autocomplete=current-password]");
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
  if (!email || !password || !setter || !input.email.endsWith("@example.test"))
    throw new Error("The disposable iOS sign-in form was unavailable.");
  for (const [field, value] of [[email, input.email], [password, input.password]] as const) {
    setter.call(field, value);
    field.dispatchEvent(new Event("input", { bubbles: true }));
  }
  await new Promise((resolve) => setTimeout(resolve, 150));
  form.requestSubmit();

  const profile = await wait(() => exact<HTMLButtonElement>("nav button", "Profile"));
  profile.click();
  const summary = await wait(() => exact("summary", "Subscription"));
  const panel = summary.parentElement as HTMLDetailsElement;
  panel.open = true;
  const buy = await wait(() => {
    const button = exact<HTMLButtonElement>("button", "Subscribe monthly", panel);
    return button && !button.disabled && panel.textContent?.includes("€6.99 per month.") &&
      panel.textContent.includes("€69.90 per year.") ? button : undefined;
  });
  const controls = await wait(() => {
    const annual = exact<HTMLButtonElement>("button", "Subscribe annually", panel);
    const restore = exact<HTMLButtonElement>("button", "Restore purchases", panel);
    if (annual && !annual.disabled && restore && !restore.disabled) return [buy, annual, restore];
  });
  const root = document.documentElement;
  await wait(() => parseFloat(getComputedStyle(root).fontSize) >= 24 ? true : undefined);
  const fits = () =>
    root.scrollWidth <= innerWidth + 1 && panel.scrollWidth <= panel.clientWidth + 1 &&
    controls.every((button) => {
      button.focus();
      const box = button.getBoundingClientRect();
      return document.activeElement === button && box.width > 0 && box.height > 0 &&
        box.left >= 0 && box.right <= innerWidth + 1 && button.scrollWidth <= button.clientWidth + 1;
    });
  const fonts = () => controls.map((button) => parseFloat(getComputedStyle(button).fontSize));
  const systemFonts = fonts();
  if (!fits()) throw new Error("The subscription controls do not fit with iOS system large text.");
  const previous = root.style.fontSize;
  try {
    root.style.fontSize = "100%";
    const normal = fonts();
    if (!normal.every((size, index) => size > 0 && systemFonts[index] >= size * 1.45))
      throw new Error("The subscription controls did not enlarge with iOS system text.");
    root.style.fontSize = "200%";
    if (!fits() || !fonts().every((size, index) => size >= normal[index] * 1.95))
      throw new Error("The iOS fixture subscription controls failed the enlarged-text check.");
  } finally {
    root.style.fontSize = previous;
  }

  buy.click();
  await wait(() => completed() === 1 &&
    panel.querySelector('[role="status"]')?.textContent === "Purchase verified." &&
    panel.textContent?.includes("Premium · Monthly") &&
    panel.textContent.includes("Renews on") &&
    [...panel.querySelectorAll<HTMLAnchorElement>("a")].some(
      (link) => link.href === "https://apps.apple.com/account/subscriptions",
    ) ? true : undefined);
  const restore = await wait(() => {
    const button = exact<HTMLButtonElement>("button", "Restore purchases", panel);
    return button && !button.disabled ? button : undefined;
  });
  restore.click();
  await wait(() => completed() === 2 &&
    panel.querySelector('[role="status"]')?.textContent === "Purchase verified." &&
    !exact("button", "Subscribe monthly", panel) ? true : undefined);
  return { terms: true, systemTextSize: true, checkout: true, restore: true };
}

export function iosBillingFixtureSource() {
  return `return await (${runIOSBillingFixture.toString()})(fixture, ${installBillingStoreFixture.toString()});\n`;
}
