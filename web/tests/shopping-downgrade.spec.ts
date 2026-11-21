import { randomUUID } from "node:crypto";
import { expect, test, type Page } from "@playwright/test";
import { addDays } from "../src/dates";
import type { ShoppingList } from "../src/shopping-state";
import { personalGrant, personalRequest } from "./support/personal-recipes";
import { registerFixture, signInFixture } from "./support/registration";
import { openExtras } from "./support/shopping";

test.use({ viewport: { width: 375, height: 812 } });

test("offline check-offs sync after downgrade while Premium changes are reported", async ({
  page,
  context,
  browser,
}) => {
  test.skip(
    process.env.MYCHEF_PLANNING_TIERS !== "1",
    "run with the disposable planning fixture enabled",
  );
  const email = "shopping-downgrade@example.test";
  const password = "Pasta after going offline 42.";
  await registerFixture(page, {
    email,
    password,
    firstName: "Offline",
    lastName: "Fixture",
    username: "shopping-downgrade",
  });
  await personalGrant(page, "premium");
  const { firstWeek: week } = await personalRequest<{ firstWeek: string }>(
    page,
    "PUT",
    "/planning/zone",
    { zone: "Europe/Brussels" },
  );
  const session = await (await page.request.get("/api/v1/session")).json();
  const response = await page.request.post("/api/v1/generations", {
    headers: {
      Origin: new URL(page.url()).origin,
      "X-CSRF-Token": session.csrfToken,
      "Idempotency-Key": randomUUID(),
    },
    data: {
      dates: [week, addDays(week, 1)],
      adults: 2,
      children: 0,
      maxMinutes: 30,
      budget: "medium",
      region: "Belgium",
      creativity: 5,
    },
  });
  expect(response.status()).toBe(202);
  const { id } = await response.json();
  await expect
    .poll(
      async () =>
        (
          await personalRequest<{ status: string }>(
            page,
            "GET",
            `/generations/${id}`,
          )
        ).status,
    )
    .toBe("succeeded");
  const path = `/shopping/${week}`;
  const read = (tab: Page) => personalRequest<ShoppingList>(tab, "GET", path);
  const save = (tab: Page, list: ShoppingList) =>
    personalRequest<ShoppingList>(tab, "PUT", path, list);
  const custom = await read(page);
  custom.meals[1].selected = false;
  custom.items.push({
    id: "retained-soap",
    name: "Dish soap",
    quantity: "1",
    unit: "bottle",
    category: "Household extras",
    state: "buy",
    manual: true,
    contributions: [],
    updated: false,
  });
  await save(page, custom);
  await page.goto(`/app/?view=shopping&week=${week}`);
  await expect(
    page.getByRole("checkbox", { name: "Mark Dish soap as bought" }),
  ).toBeEnabled();

  const other = await browser.newContext({
    baseURL: new URL(page.url()).origin,
  });
  try {
    const second = await other.newPage();
    await second.goto("/app/");
    await signInFixture(second, email, password);
    await context.setOffline(true);
    await page
      .getByRole("checkbox", { name: "Mark Dish soap as bought" })
      .click();
    await openExtras(page);
    await page.getByLabel("Extra item", { exact: true }).fill("Offline coffee");
    await page.getByRole("button", { name: "Add extra", exact: true }).click();
    await expect(page.getByText(/Pending sync/).first()).toBeVisible();

    const concurrent = await read(second);
    concurrent.items.find(
      (item) => item.name.toLowerCase() === "carrot",
    )!.state = "home";
    await save(second, concurrent);
    await personalGrant(second, "free");
    await context.setOffline(false);
    await expect(
      page.getByText(/weren’t synced on Free: edits to extras/),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Choose meals" }),
    ).toHaveCount(0);
    await expect(page.getByLabel("Sort by")).toHaveCount(0);
    await expect(page.getByText("Offline coffee", { exact: true })).toHaveCount(
      0,
    );
    const settled = await read(page);
    expect(settled.customizationLocked).toBe(true);
    expect(settled.meals.every((meal) => meal.selected)).toBe(true);
    expect(
      settled.items.find((item) => item.id === "retained-soap")?.state,
    ).toBe("basket");
    expect(
      settled.items.find((item) => item.name.toLowerCase() === "carrot")?.state,
    ).toBe("home");
    expect(
      settled.items.some((item) => item.name.toLowerCase() === "leek"),
    ).toBe(true);
    await page.reload();
    await expect(
      page.getByText(/weren’t synced on Free: edits to extras/),
    ).toBeVisible();
    await page.getByRole("button", { name: "Dismiss", exact: true }).click();
    await expect(page.getByText(/weren’t synced on Free/)).toHaveCount(0);
  } finally {
    await other.close();
  }
});
