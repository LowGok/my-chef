import { expect, test, type Page } from "@playwright/test";

import { shoppingAccount, openExtras } from "./support/shopping";

test.use({ locale: "en-GB" });
test.setTimeout(100_000);

async function setup(page: Page) {
  const signed = await shoppingAccount(page);
  const headers = { Origin: new URL(page.url()).origin };
  const generated = await page.request.post("/api/v1/generations", {
    headers: {
      Origin: headers.Origin,
      "X-CSRF-Token": signed.csrfToken,
      "Idempotency-Key": crypto.randomUUID(),
    },
    data: {
      dates: ["2026-11-02", "2026-11-03"],
      adults: 2,
      children: 0,
      maxMinutes: 30,
      budget: "medium",
      region: "Belgium",
      creativity: 2,
      replaceExisting: true,
    },
  });
  expect(generated.status()).toBe(202);
  const op = await generated.json();
  await expect
    .poll(
      async () =>
        (await (await page.request.get(`/api/v1/generations/${op.id}`)).json())
          .status,
    )
    .toBe("succeeded");
  await page.goto("/app/?week=2026-11-02");
}

test("saved meals become a persistent, accessible shopping list", async ({
  page,
}) => {
  await setup(page);
  const sourceBefore = (
    await (
      await page.request.get("/api/v1/menus?from=2026-11-02&to=2026-11-09")
    ).json()
  ).recipes.map((r: { id: string; title: string; ingredients: unknown }) => ({
    id: r.id,
    title: r.title,
    ingredients: r.ingredients,
  }));
  const errors: string[] = [];
  page.on("pageerror", (err) => errors.push(err.message));
  let generations = 0;
  page.on("request", (r) => {
    if (r.url().includes("/generations") && r.method() === "POST")
      generations++;
  });
  await page
    .getByRole("button", { name: "Shopping list", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "Shopping list.", exact: true }),
  ).toBeVisible();
  await expect(page.locator(".shopping-row details")).toHaveCount(0);
  await expect(page.getByLabel("Mark carrot as bought")).toBeVisible();
  await page.getByLabel("Mark carrot as bought").click();
  await expect(page.getByLabel("Mark carrot as needed")).toBeChecked();
  await page.reload();
  await expect(page.getByLabel("Mark carrot as needed")).toBeChecked();
  await page.getByRole("button", { name: "Move carrot to At home" }).click();
  await page.getByRole("button", { name: /At home \(/ }).click();
  await expect(page.getByText("carrot", { exact: true })).toBeVisible();
  await openExtras(page);
  await page.getByLabel("Extra item", { exact: true }).fill("Dish soap");
  await page.getByRole("button", { name: "Add extra", exact: true }).click();
  await page.getByRole("button", { name: /To buy/ }).click();
  await expect(page.getByText("Dish soap", { exact: true })).toBeVisible();
  for (const width of [320, 390, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: `../test-results/shopping/${width}.png`,
      fullPage: true,
    });
  }
  const chooser = page.getByRole("button", { name: /Choose meals/ });
  if ((await chooser.getAttribute("aria-expanded")) === "false")
    await chooser.click();
  await page.getByLabel("Include Lemon garden pasta on 3 Nov 2026").uncheck();
  await expect(page.getByLabel("Mark leek as bought")).toHaveCount(0);
  const server = await (
    await page.request.get("/api/v1/shopping/2026-11-02")
  ).json();
  await expect
    .poll(
      async () =>
        (await (await page.request.get("/api/v1/shopping/2026-11-02")).json())
          .meals[1].selected,
    )
    .toBe(false);
  expect(
    server.items.some((i: { name: string }) => i.name === "Dish soap"),
  ).toBe(true);
  expect(generations).toBe(0);
  const sourceAfter = (
    await (
      await page.request.get("/api/v1/menus?from=2026-11-02&to=2026-11-09")
    ).json()
  ).recipes.map((r: { id: string; title: string; ingredients: unknown }) => ({
    id: r.id,
    title: r.title,
    ingredients: r.ingredients,
  }));
  expect(sourceAfter).toEqual(sourceBefore);
  expect(errors).toEqual([]);
  await openExtras(page);
  await page.getByLabel("Extra item", { exact: true }).focus();
  await expect(page.getByLabel("Extra item", { exact: true })).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(page.getByLabel("Amount", { exact: true })).toBeFocused();
});
