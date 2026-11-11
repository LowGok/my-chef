import { expect, test } from "@playwright/test";
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import {
  registerFixtureThroughBrowser,
  signInFixture,
} from "../tests/support/registration";
import type { Menu } from "../src/api";

const directory = process.env.MYCHEF_LIVE_ARTIFACTS!;
const state = process.env.MYCHEF_LIVE_STATE!;
test.beforeAll(() => {
  if (process.env.MYCHEF_RUN_REWRITE_E2E !== "1" || !directory || !state)
    throw new Error("Use the explicit isolated rewrite E2E runner");
});

test("prepare isolated provider account", async ({ page }) => {
  await registerFixtureThroughBrowser(page, {
    firstName: "Live",
    lastName: "Chef",
    username: "isolated-live",
    email: "isolated-live@example.test",
    password: process.env.MYCHEF_TEST_PASSWORD!,
  });
});

test("generate and inspect the actual menu", async ({ page }) => {
  const email = "isolated-live@example.test";
  const password = process.env.MYCHEF_TEST_PASSWORD!;
  if (process.env.MYCHEF_TEST_ACCOUNT_READY === "1") {
    await page.goto("/app/");
    await signInFixture(page, email, password);
  } else {
    await registerFixtureThroughBrowser(page, {
      firstName: "Live",
      lastName: "Chef",
      username: "isolated-live",
      email,
      password,
    });
  }
  await page.getByRole("button", { name: "Profile", exact: true }).click();
  if (process.env.MYCHEF_ARTIFACT_FAILURE === "1") {
    await page.context().storageState({ path: join(state, "session.json") });
    throw new Error("Injected failure after credential submission");
  }
  await page.getByLabel("Diet").fill("Vegetarian");
  await page.getByLabel("Disliked ingredients").fill("Mushrooms");
  await page.getByRole("button", { name: "Save preferences" }).click();
  await expect(
    page.getByText("Preferences saved.", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Meals", exact: true }).click();
  await page.getByRole("button", { name: "Plan a menu", exact: true }).click();
  await page.getByLabel("Number of meals", { exact: true }).fill("1");
  await page.getByRole("button", { name: "Review menu", exact: true }).click();
  const submitted = page.waitForResponse(
    (r) =>
      r.url().endsWith("/api/v1/generations") &&
      r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Plan my week", exact: true }).click();
  const post = await submitted;
  const operation = await post.json();
  expect(post.status()).toBe(202);
  await expect
    .poll(
      async () =>
        (
          await (
            await page.request.get(`/api/v1/generations/${operation.id}`)
          ).json()
        ).status,
      { timeout: 130_000 },
    )
    .toBe("succeeded");
  const completed = await (
    await page.request.get(`/api/v1/generations/${operation.id}`)
  ).json();
  await expect
    .poll(
      async () => {
        const menu = await (
          await page.request.get(`/api/v1/menus/${completed.menuId}`)
        ).json();
        return menu.recipes[0].imageStatus;
      },
      { timeout: 60_000 },
    )
    .not.toBe("pending");
  const menu: Menu = await (
    await page.request.get(`/api/v1/menus/${completed.menuId}`)
  ).json();
  expect(menu.recipes).toHaveLength(1);
  const recipe = menu.recipes[0];
  await page
    .getByRole("button", { name: `Open ${recipe.title}`, exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: recipe.title, exact: true }),
  ).toBeVisible();
  for (const step of recipe.instructions ?? [])
    await expect(page.getByText(step, { exact: true })).toBeVisible();
  for (const ingredient of recipe.ingredients) {
    const row = page
      .locator(".ingredients li")
      .filter({ has: page.getByText(ingredient.name, { exact: true }) });
    await expect(row).toHaveCount(1);
    const quantity =
      [ingredient.quantity, ingredient.unit]
        .filter((v) => v != null && v !== "")
        .join(" ") || "To taste";
    await expect(row.locator("strong")).toHaveText(quantity);
  }
  if (recipe.imageStatus === "unavailable")
    await expect(
      page.getByText("Image unavailable. Your saved recipe is ready to cook.", {
        exact: true,
      }),
    ).toBeVisible();
  await page.reload();
  await expect(
    page.getByRole("heading", { name: recipe.title, exact: true }),
  ).toBeVisible();
  writeFileSync(join(directory, "menu.json"), JSON.stringify(menu, null, 2), {
    mode: 0o600,
  });
  writeFileSync(join(directory, "operation.json"), JSON.stringify(operation), {
    mode: 0o600,
  });
  await page.context().storageState({ path: join(state, "session.json") });
  await page.screenshot({
    path: join(directory, "recipe.png"),
    fullPage: true,
  });
});

test("restart retains the same session and exact menu without a new generation", async ({
  browser,
}) => {
  const menu: Menu = JSON.parse(
    readFileSync(join(directory, "menu.json"), "utf8"),
  );
  const context = await browser.newContext({
    storageState: join(state, "session.json"),
    baseURL: "http://127.0.0.1:18138",
  });
  const page = await context.newPage();
  await page.goto(`/app/?recipe=${menu.recipes[0].id}&menu=${menu.id}`);
  await expect(
    page.getByRole("heading", { name: menu.recipes[0].title, exact: true }),
  ).toBeVisible();
  expect(
    await (await page.request.get(`/api/v1/menus/${menu.id}`)).json(),
  ).toEqual(menu);
  await page.screenshot({
    path: join(directory, "after-restart.png"),
    fullPage: true,
  });
  await context.close();
});
