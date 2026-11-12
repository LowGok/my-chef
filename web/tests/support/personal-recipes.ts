import { randomBytes } from "node:crypto";
import { expect, type Page } from "@playwright/test";
import { registerFixture } from "./registration";
import { privateAction } from "./private-action";
export { privateAction } from "./private-action";
export { personalPhoto } from "./personal-photo";

export async function personalFixture(
  page: Page,
  username: string,
  state = "premium",
) {
  await registerFixture(page, {
    firstName: "Library",
    lastName: "Fixture",
    username,
    email: `${username}@example.test`,
    password: "Garden personal recipe fixture 2026!",
  });
  await personalGrant(page, state);
  await page.goto("/app/?view=profile");
}
export async function personalGrant(page: Page, state: string) {
  try {
    const session = await (await page.request.get("/api/v1/session")).json();
    const path = "/api/v1/fixture/personal-recipes/entitlement";
    const headers = {
      Origin: new URL(page.url()).origin,
      Authorization: `Bearer ${process.env.MYCHEF_TEST_INBOX_CAPABILITY}`,
    };
    const lease = await page.request.get(path, { headers });
    expect(lease.status()).toBe(200);
    const { generation } = await lease.json();
    const response = await page.request.post(path, {
      headers,
      data: {
        accountId: session.user.id,
        accountInstance: session.user.accountInstance,
        generation,
        operationId: randomBytes(16).toString("hex"),
        state,
      },
    });
    expect(response.status()).toBe(204);
  } catch {
    throw new Error(
      "The synthetic recipe entitlement could not be configured.",
    );
  }
}
export async function openPersonalLibrary(page: Page) {
  await expect(
    page.locator("summary").filter({ hasText: /^My recipes$/ }),
  ).toBeVisible({ timeout: 1500 });
  await page
    .locator("summary")
    .filter({ hasText: /^My recipes$/ })
    .click();
  await page
    .getByRole("button", { name: "Open my recipes", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "My recipes", exact: true }),
  ).toBeVisible({ timeout: 1500 });
}
export async function choosePersonalDuration(
  page: Page,
  label: string,
  value: number,
) {
  return privateAction(async () => {
    await page.getByRole("button", { name: label, exact: true }).click();
    const dialog = page.getByRole("dialog", { name: label, exact: true });
    for (const [name, target] of [
      ["Hours", Math.floor(value / 60)],
      ["Minutes", value % 60],
    ] as const) {
      const wheel = dialog.getByRole("spinbutton", { name, exact: true });
      await wheel.press("Home");
      const distance =
        target - Number(await wheel.getAttribute("aria-valuemin"));
      for (let step = 0; step < Math.floor(distance / 10); step++)
        await wheel.press("PageUp");
      for (let step = 0; step < distance % 10; step++)
        await wheel.press("ArrowUp");
      await expect(wheel).toHaveAttribute("aria-valuenow", String(target));
    }
    await dialog.getByRole("button", { name: "Done", exact: true }).click();
    await expect(dialog).not.toBeVisible();
  });
}

export async function fillPersonalDraft(page: Page) {
  return privateAction(async () => {
    await page
      .getByLabel("Title", { exact: true })
      .fill("Private fixture supper");
    await page
      .getByLabel("Short description", { exact: true })
      .fill("A quiet home-cooked meal.");
    await choosePersonalDuration(page, "Preparation time", 5);
    await choosePersonalDuration(page, "Cooking time", 10);
    await page.getByLabel("Ingredient 1", { exact: true }).fill("Carrot");
    await page
      .getByLabel("Quantity 1", { exact: true })
      .fill("12345678901234567890.00000000001");
    await page.getByLabel("Unit 1", { exact: true }).fill("g");
    await page
      .getByLabel("Step 1", { exact: true })
      .fill("Gently cook the synthetic vegetables.");
  });
}

export async function personalRequest<T>(
  page: Page,
  method: string,
  path: string,
  data?: unknown,
): Promise<T> {
  try {
    const session = await (await page.request.get("/api/v1/session")).json();
    const response = await page.request.fetch("/api/v1" + path, {
      method,
      headers: {
        Origin: new URL(page.url()).origin,
        "X-CSRF-Token": session.csrfToken,
      },
      ...(data === undefined ? {} : { data }),
    });
    if (!response.ok()) throw new Error("Private fixture operation rejected.");
    return (await response.json()) as T;
  } catch {
    throw new Error("The private fixture operation failed.");
  }
}
