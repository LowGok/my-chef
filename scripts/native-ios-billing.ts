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
  const root = document.documentElement;
  const previous = root.style.fontSize;
  try {
    const normal = parseFloat(getComputedStyle(buy).fontSize);
    root.style.fontSize = "200%";
    buy.focus();
    if (!(parseFloat(getComputedStyle(buy).fontSize) > normal) ||
        panel.scrollWidth > panel.clientWidth + 1 || document.activeElement !== buy)
      throw new Error("The iOS fixture subscription controls failed the enlarged-text check.");
  } finally {
    root.style.fontSize = previous;
  }

  buy.click();
  await wait(() => completed() === 1 &&
    panel.querySelector('[role="status"]')?.textContent === "Purchase verified." &&
    panel.textContent?.includes("Premium · Monthly") &&
    panel.textContent.includes("Paid through") &&
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
  return { terms: true, checkout: true, restore: true };
}

export function iosBillingFixtureSource() {
  return `return await (${runIOSBillingFixture.toString()})(fixture, ${installBillingStoreFixture.toString()});\n`;
}
