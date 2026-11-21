import { expect, test } from "@playwright/test";
import type { Session } from "../src/api";

test("Go serves health and the embedded production frontend", async ({
  page,
  request,
}) => {
  const health = await request.get("/healthz");
  expect(health.ok()).toBe(true);
  expect(await health.json()).toEqual({ status: "ok" });
  await page.goto("/app/");
  await expect(
    page.getByRole("button", { name: "Sign in to MyChef", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "MyChef", exact: true }),
  ).toBeVisible();
  const logo = page
    .locator(".brand")
    .getByRole("img", { name: "MyChef", exact: true });
  await expect(logo).toBeVisible();
  await expect(logo).toHaveJSProperty("naturalWidth", 1000);
  await expect(
    page.getByRole("button", { name: "Sign in to MyChef", exact: true }),
  ).toHaveCSS("background-color", "rgb(74, 105, 75)");
  await page.screenshot({
    path: "../test-results/branding/welcome.png",
    fullPage: true,
  });
});

test("entry and menu controls remain usable on narrow screens", async ({
  page,
}) => {
  await page.setViewportSize({ width: 320, height: 812 });
  await page.goto("/app/");
  await expect(
    page.getByRole("heading", { name: "Welcome back.", exact: true }),
  ).toBeVisible();
  await expect(
    page.locator(".brand"),
  ).toBeVisible();
  await page.screenshot({
    path: "../test-results/branding/welcome-mobile.png",
    fullPage: true,
  });
  const session: Session = {
    csrfToken: "branding-layout-only",
    user: {
      id: "10000",
      firstName: "Robin",
      lastName: "Cook",
      email: "branding@example.test",
      username: "branding",
      birthdate: null,
      sex: null,
      preferences: {
        diet: [],
        allergens: [],
        dislikes: [],
        technique: [],
        effort: "Quick weekday meals",
      },
    },
  };
  await page.route("**/api/v1/session", (route) =>
    route.fulfill({ json: session }),
  );
  await page.route("**/api/v1/favorite-weeks/*", (route) =>
    route.fulfill({
      headers: { "X-MyChef-User": session.user!.id },
      json: {
        week: new URL(route.request().url()).pathname.split("/").pop(),
        revision: "empty-layout-fixture",
        recipes: [],
        addedIds: [],
      },
    }),
  );
  await page.route("**/api/v1/favorites?*", (route) =>
    route.fulfill({
      headers: { "X-MyChef-User": session.user!.id },
      json: { recipes: [], nextOffset: null },
    }),
  );
  await page.route("**/api/v1/menus?*", (route) =>
    route.fulfill({
      headers: { "X-MyChef-User": session.user!.id },
      json: { recipes: [], nextOffset: null },
    }),
  );
  await page.reload();
  await page.getByRole("button", { name: "Plan a menu", exact: true }).click();
  await expect(page.getByLabel("Region", { exact: true })).toHaveCount(0);
  const effort = page.getByLabel("Cooking effort", { exact: true });
  await expect(effort.locator("option")).toHaveText([
    "Minimal Effort",
    "Quick & Easy",
    "Moderate Effort",
    "Cooking Project",
  ]);
  expect(
    await effort
      .locator("option")
      .evaluateAll((options) =>
        options.map((option) => (option as HTMLOptionElement).value),
      ),
  ).toEqual(["15", "30", "60", "120"]);
  await effort.selectOption("120");
  await expect(
    page.getByRole("button", { name: "Decrease children", exact: true }),
  ).toBeDisabled();
  await expect(
    page.getByRole("button", { name: "Decrease adults", exact: true }),
  ).toBeEnabled();
  await page
    .getByRole("button", { name: "Increase adults", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Increase children", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Increase number of meals", exact: true })
    .click();
  for (const width of [320, 390]) {
    await page.setViewportSize({ width, height: 812 });
    await expect(
      page.locator(".brand"),
    ).toBeVisible();
    const logoBounds = await page
      .locator(".brand")
      .boundingBox();
    expect(
      Math.abs(logoBounds!.x + logoBounds!.width / 2 - width / 2),
    ).toBeLessThan(1);
    const steps = await page.locator(".planner-steps li").all();
    const first = await steps[0].boundingBox();
    const second = await steps[1].boundingBox();
    expect(Math.abs(first!.width - second!.width)).toBeLessThan(1);
    expect(Math.abs(first!.y - second!.y)).toBeLessThan(1);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
    for (const name of ["Previous week", "Next week", "Review menu"]) {
      const bounds = await page
        .getByRole("button", { name, exact: true })
        .boundingBox();
      expect(bounds?.width).toBeGreaterThanOrEqual(44);
      expect(bounds?.height).toBeGreaterThanOrEqual(44);
    }
  }
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({
    path: "../test-results/branding/household-mobile.png",
    fullPage: true,
  });
  await page.getByLabel("First meal date").fill("2026-12-30");
  await page.getByRole("button", { name: "Review menu", exact: true }).click();
  await expect(
    page.getByRole("listitem").filter({ hasText: "Review your menu" }),
  ).toHaveAttribute("aria-current", "step");
  expect(
    await page
      .locator(".review-date-list time")
      .evaluateAll((dates) =>
        dates.map((date) => date.getAttribute("datetime")),
      ),
  ).toEqual(["2026-12-30", "2026-12-31", "2027-01-01", "2027-01-02"]);
  for (const width of [320, 390, 460, 640, 900, 1280]) {
    await page.setViewportSize({ width, height: 812 });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
    const cards = await page.locator(".review-summary > div").all();
    expect(cards).toHaveLength(4);
    const bounds = await Promise.all(cards.map((card) => card.boundingBox()));
    const overviewBounds = await page.locator(".review-overview").boundingBox();
    for (const [index, card] of bounds.entries()) {
      expect(Math.abs(card!.width - overviewBounds!.width)).toBeLessThan(1);
      if (index > 0)
        expect(card!.y).toBeGreaterThan(
          bounds[index - 1]!.y + bounds[index - 1]!.height,
        );
    }
    const plan = page.getByRole("button", {
      name: "Plan my week",
      exact: true,
    });
    const action = await plan.boundingBox();
    expect(action!.width).toBeGreaterThanOrEqual(44);
    expect(action!.height).toBeGreaterThanOrEqual(44);
    const lineCount = await plan.evaluate((button) => {
      const text = Array.from(button.childNodes).find(
        (node) =>
          node.nodeType === Node.TEXT_NODE &&
          node.textContent?.includes("Plan my week"),
      )!;
      const start = text.textContent!.indexOf("Plan my week");
      const range = document.createRange();
      range.setStart(text, start);
      range.setEnd(text, start + "Plan my week".length);
      return range.getClientRects().length;
    });
    expect(lineCount).toBe(1);
    if (width <= 390) {
      const editBounds = await page
        .getByRole("button", { name: "Edit request", exact: true })
        .boundingBox();
      const planBounds = await plan.boundingBox();
      expect(editBounds!.y).toBeGreaterThan(planBounds!.y);
    }
  }
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({
    path: "../test-results/branding/review-desktop.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 812 });
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({
    path: "../test-results/branding/planner-mobile.png",
    fullPage: true,
  });
  await page.getByRole("button", { name: "Edit request", exact: true }).click();
  await expect(page.getByLabel("First meal date")).toBeVisible();
  await expect(page.getByLabel("First meal date")).toHaveValue("2026-12-30");
  await expect(effort).toHaveValue("120");
  await expect(page.getByLabel("Adults", { exact: true })).toHaveValue("3");
  await expect(page.getByLabel("Children", { exact: true })).toHaveValue("1");
  await expect(page.getByLabel("Number of meals", { exact: true })).toHaveValue(
    "4",
  );
  await page.getByLabel("First meal date").fill("2027-01-02");
  await page.route("**/api/v1/menus?*", (route) => {
    if (new URL(route.request().url()).searchParams.get("limit") === "1")
      return route.fulfill({
        headers: { "X-MyChef-User": session.user!.id },
        json: { recipes: [{ id: "existing" }], nextOffset: null },
      });
    return route.fallback();
  });
  await page.getByRole("button", { name: "Review menu", exact: true }).click();
  const confirmation = page.getByRole("dialog", {
    name: "You have meals planned.",
    exact: true,
  });
  await expect(confirmation).toBeVisible();
  await expect(confirmation.locator(".replacement-weeks li")).toHaveCount(2);
  await page.keyboard.press("Escape");
  await expect(confirmation).not.toBeVisible();
  await expect(page.getByLabel("First meal date")).toHaveValue("2027-01-02");
});

test("startup loading gives way to minimal authentication", async ({
  page,
}) => {
  let release!: () => void;
  const loading = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route("**/api/v1/session", async (route) => {
    await loading;
    await route.continue();
  });
  await page.goto("/app/");
  await expect(
    page.getByRole("status", { name: "Loading MyChef", exact: true }),
  ).toBeVisible();
  await page.screenshot({
    path: "../test-results/branding/startup.png",
    fullPage: true,
  });
  release();
  await expect(
    page.getByRole("heading", { name: "Welcome back.", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("status", { name: "Loading MyChef", exact: true }),
  ).toHaveCount(0);
  await page.setViewportSize({ width: 390, height: 812 });
  await page
    .getByRole("button", { name: "Create an account", exact: true })
    .click();
  await expect(page.getByLabel("First name", { exact: true })).toBeVisible();
  await page.screenshot({
    path: "../test-results/branding/signup-mobile.png",
    fullPage: true,
  });
});
