import { expect, test, type Page } from "@playwright/test";
import { shoppingAccount } from "./support/shopping";
import { dateLabel } from "../src/dates";

async function mealRows(page: Page) {
  const signed = await shoppingAccount(page);
  const recipes = [
    {
      id: "ux-1",
      title: "A very long lemon garden pasta title for the weekly dinner",
      note: "A bright, simple pasta with seasonal vegetables and a lemon dressing.",
      allergens: [
        "Milk",
        "Wheat",
        "Sesame",
        "A deliberately long recorded allergen name",
      ],
      totalMinutes: 35,
      prepMinutes: 10,
      imageUrl: "https://images.test/broken.png",
    },
    {
      id: "ux-2",
      title: "Vegetable rice",
      note: null,
      allergens: [],
      totalMinutes: null,
      prepMinutes: 5,
      imageUrl: null,
    },
    {
      id: "ux-3",
      title: "Saved soup",
      note: "A warming bowl",
      allergens: null,
      totalMinutes: 20,
      imageUrl: null,
    },
  ].map((r) => ({
    ...r,
    date: "2026-11-02",
    isFavorite: false,
    yield: [],
    instructions: ["Cook and serve."],
    ingredients: [],
    tips: [],
    imageStatus: "unavailable",
    allergenNote: null,
    menuId: null,
    equipment: null,
    cookMinutes: null,
    legacyRaw: null,
  }));
  const headers = { "X-MyChef-User": signed.user.id };
  await page.route("https://images.test/**", (r) => r.fulfill({ status: 404 }));
  await page.route("**/api/v1/menus?*", (r) =>
    r.fulfill({ headers, json: { recipes, nextOffset: null } }),
  );
  await page.route("**/api/v1/recipes/ux-*", (r) =>
    r.fulfill({
      headers,
      json: recipes.find((v) => r.request().url().endsWith(v.id)),
    }),
  );
  await page.goto("/app/");
  await expect(page.locator(".recipe-card")).toHaveCount(3);
  return recipes;
}

test("meal rows show the schedule and reserve allergens and time for recipe details", async ({
  page,
}) => {
  const recipes = await mealRows(page);
  for (const width of [320, 360, 390, 430, 1100]) {
    await page.setViewportSize({ width, height: 850 });
    const cards = page.locator(".recipe-card"),
      first = cards.first();
    const image = await first.locator(".recipe-image").boundingBox();
    const a = await first.boundingBox(),
      b = await cards.nth(1).boundingBox();
    expect(image!.y).toBeCloseTo(a!.y);
    expect(image!.height).toBeCloseTo(a!.height);
    expect(image!.height).toBeCloseTo(image!.width);
    const heightDifference = await first
      .locator(".recipe-note")
      .evaluate((note) => {
        const original = note.textContent;
        const card = note.closest(".recipe-card")!;
        note.textContent = "Lemon pasta ".repeat(30);
        const longHeight = card.getBoundingClientRect().height;
        note.textContent = "Lemon pasta\nFresh vegetables";
        (note as HTMLElement).style.whiteSpace = "pre-line";
        const twoLineHeight = card.getBoundingClientRect().height;
        note.textContent = original;
        (note as HTMLElement).style.whiteSpace = "";
        return longHeight - twoLineHeight;
      });
    expect(heightDifference).toBeCloseTo(0);
    expect(b!.y).toBeGreaterThanOrEqual(a!.y + a!.height);
    const schedule = first.locator("time");
    await expect(schedule).toHaveAttribute("datetime", recipes[0].date);
    await expect(schedule).toContainText(dateLabel(recipes[0].date));
    await expect(
      first.getByText(/listed allergens|total:|time unavailable/i),
    ).toHaveCount(0);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
  }
  await expect(
    page.locator(".recipe-card").first().locator(".food-placeholder"),
  ).toBeVisible();
  await expect(
    page.locator(".recipe-card").nth(1).locator(".recipe-note"),
  ).toHaveCount(0);
  const open = page.getByRole("button", {
    name: `Open ${recipes[0].title}`,
    exact: true,
  });
  await open.focus();
  await page.keyboard.press("Enter");
  await expect(
    page.getByRole("heading", { name: recipes[0].title, exact: true }),
  ).toBeVisible();
  const actions = page.locator(".recipe-detail-actions");
  const backBox = await actions
    .getByRole("button", { name: /Back to your menu/ })
    .boundingBox();
  const saveBox = await actions
    .getByRole("button", { name: "Add to favorites" })
    .boundingBox();
  expect(saveBox!.x).toBeGreaterThan(backBox!.x + backBox!.width);
  await expect(page.getByText("35 minutes", { exact: true })).toBeVisible();
  await expect(
    page.getByText(`Listed allergens: ${recipes[0].allergens!.join(", ")}`),
  ).toBeVisible();
  expect(await page.evaluate(() => scrollY)).toBeLessThan(2);
});

async function phoneFields(page: Page) {
  for (const field of await page
    .locator(
      'input:not([type="checkbox"]):not([type="radio"]):not([type="range"]):visible, select:visible, textarea:visible',
    )
    .all()) {
    expect(
      await field.evaluate((e) => parseFloat(getComputedStyle(e).fontSize)),
    ).toBeGreaterThanOrEqual(16);
  }
  const clippedOptions = await page
    .locator("select:visible")
    .evaluateAll(async (fields) => {
      await document.fonts.ready;
      const context = document.createElement("canvas").getContext("2d")!;
      return fields.flatMap((field) => {
        const style = getComputedStyle(field);
        context.font = `${style.fontWeight} ${style.fontSize} ${style.fontFamily}`;
        const textWidth =
          field.clientWidth -
          parseFloat(style.paddingLeft) -
          parseFloat(style.paddingRight) -
          20;
        return Array.from((field as HTMLSelectElement).options)
          .filter(
            (option) => context.measureText(option.label).width > textWidth,
          )
          .map((option) => option.label);
      });
    });
  expect(clippedOptions).toEqual([]);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
}
test("phone controls keep readable text and align with the week arrows", async ({
  page,
}) => {
  await page.setViewportSize({ width: 320, height: 850 });
  await page.goto("/app/");
  await phoneFields(page);
  await shoppingAccount(page);
  await page.goto("/app/");
  for (const width of [320, 360, 390, 430]) {
    await page.setViewportSize({ width, height: 850 });
    const input = page.getByLabel("Browse week", { exact: true });
    const picker = (await page.locator(".week-picker").boundingBox())!;
    const section = (await page
      .locator(".week-picker")
      .locator("..")
      .boundingBox())!;
    expect(Math.abs(picker.width - section.width)).toBeLessThanOrEqual(1);
    const box = await input.boundingBox(),
      arrow = await page
        .getByRole("button", { name: "Next week", exact: true })
        .boundingBox();
    expect(Math.abs(box!.height - arrow!.height)).toBeLessThanOrEqual(1);
    expect(Math.abs(box!.y - arrow!.y)).toBeLessThanOrEqual(1);
    await phoneFields(page);
  }
  await page.getByRole("button", { name: "Plan a menu", exact: true }).click();
  for (const width of [320, 360, 390, 430]) {
    await page.setViewportSize({ width, height: 850 });
    await phoneFields(page);
    for (const field of await page
      .locator('.planner input[type="date"], .planner select')
      .all())
      expect((await field.boundingBox())!.height).toBeCloseTo(44, 0);
  }
  await page.getByRole("button", { name: "Profile", exact: true }).click();
  await page.setViewportSize({ width: 320, height: 850 });
  await phoneFields(page);
  expect(
    (await page.getByLabel("Cooking effort", { exact: true }).boundingBox())!
      .height,
  ).toBeCloseTo(44, 0);
  await expect(page.locator('meta[name="viewport"]')).toHaveAttribute(
    "content",
    "width=device-width, initial-scale=1",
  );
});

async function visibleDestination(page: Page, selector: string) {
  await expect
    .poll(() =>
      page.locator(selector).evaluate((el) => {
        const box = el.getBoundingClientRect(),
          nav = document.querySelector(".sidebar")!.getBoundingClientRect();
        const boundary = nav.bottom;
        const desired = Math.min(
          Math.max(0, scrollY + box.top - boundary - 12),
          Math.max(0, document.documentElement.scrollHeight - innerHeight),
        );
        return Math.abs(scrollY - desired);
      }),
    )
    .toBeLessThanOrEqual(8);
}
test("review jumps to its visible panel after ordinary and replacement checks", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 650 });
  const signed = await shoppingAccount(page);
  await page.goto("/app/");
  await page.getByRole("button", { name: "Plan a menu", exact: true }).click();
  await page.getByRole("button", { name: "Review menu", exact: true }).click();
  const heading = page.getByRole("heading", {
    name: "Review planned dinners",
    exact: true,
  });
  await expect(heading).toBeFocused();
  await visibleDestination(page, ".planner");
  await page.getByRole("button", { name: "Edit request", exact: true }).click();
  await page.route("**/api/v1/menus?*", (r) =>
    r.fulfill({
      headers: { "X-MyChef-User": signed.user.id },
      json: { recipes: [{ id: "existing" }], nextOffset: null },
    }),
  );
  await page.getByRole("button", { name: "Review menu", exact: true }).click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await page
    .getByRole("button", { name: "Review replacement", exact: true })
    .click();
  await expect(heading).toBeFocused();
  await visibleDestination(page, ".planner");
});
test("preference success returns to Profile heading and failures keep context", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 650 });
  await shoppingAccount(page);
  await page.goto("/app/?view=profile");
  await page.getByText("Account details", { exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Save", exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Save preferences", exact: true })
    .click();
  const heading = page.getByRole("heading", {
    name: "Your profile.",
    exact: true,
  });
  await expect(heading).toBeFocused();
  await visibleDestination(page, ".profile-heading h2");
  await expect(
    page.getByText("Preferences saved.", { exact: true }),
  ).toBeInViewport();
  await page.route("**/api/v1/profile", (r) =>
    r.fulfill({
      status: 503,
      json: { error: { code: "unavailable", message: "Fixture save failed" } },
    }),
  );
  const save = page.getByRole("button", {
    name: "Save preferences",
    exact: true,
  });
  await save.scrollIntoViewIfNeeded();
  await save.click();
  await expect(page.getByRole("alert")).toBeVisible();
  await expect(save).toBeInViewport();
  await expect(heading).not.toBeInViewport();
  await expect(heading).not.toBeFocused();
});

test("late profile saves do not move another page and short pages clamp safely", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 650 });
  const signed = await shoppingAccount(page);
  await page.goto("/app/?view=profile");
  let release!: () => void, arrived!: () => void;
  const held = new Promise<void>((r) => (release = r)),
    seen = new Promise<void>((r) => (arrived = r));
  await page.route("**/api/v1/profile", async (r) => {
    if (r.request().method() !== "PUT") return r.continue();
    arrived();
    await held;
    await r.fulfill({
      headers: { "X-MyChef-User": signed.user.id },
      json: signed.user,
    });
  });
  await page
    .getByRole("button", { name: "Save preferences", exact: true })
    .click();
  await seen;
  await page
    .getByRole("button", { name: "Shopping list", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "Shopping list.", exact: true }),
  ).toBeVisible();
  const response = page.waitForResponse(
    (r) =>
      r.url().endsWith("/api/v1/profile") && r.request().method() === "PUT",
  );
  release();
  await response;
  await expect(
    page.getByRole("heading", { name: "Your profile.", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("heading", { name: "Shopping list.", exact: true }),
  ).toBeVisible();
  await page.unroute("**/api/v1/profile");
  await page.getByRole("button", { name: "Profile", exact: true }).click();
  await page.setViewportSize({ width: 390, height: 1600 });
  await page.evaluate(() =>
    document
      .querySelectorAll<HTMLElement>(
        ".profile-panel .field, .allergen-picker, .advisory, .profile-support, .profile-feedback",
      )
      .forEach((el) => (el.style.display = "none")),
  );
  await page
    .getByRole("button", { name: "Save preferences", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "Your profile.", exact: true }),
  ).toBeFocused();
  await visibleDestination(page, ".profile-heading h2");
  await expect(
    page.getByText("Preferences saved.", { exact: true }),
  ).toBeInViewport();
});

test("meal rows and review remain usable with enlarged text and delayed validation", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 700 });
  await mealRows(page);
  await page.screenshot({
    path: "../test-results/mobile-ux/meal-rows-phone.png",
    fullPage: true,
  });
  await page.evaluate(() => {
    document.documentElement.style.fontSize = "200%";
  });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  const first = page.locator(".recipe-card").first();
  await expect(first.locator("h3")).toHaveCSS("white-space", "normal");
  const image = await first.locator(".recipe-image").boundingBox();
  expect(image!.height).toBeCloseTo(image!.width);
  expect(image!.y).toBeCloseTo((await first.boundingBox())!.y);
  await expect(first.locator("time")).toContainText(dateLabel("2026-11-02"));
  await page.evaluate(() => {
    document.documentElement.style.fontSize = "";
    document
      .querySelectorAll<HTMLElement>(".sidebar nav button")
      .forEach((e) => (e.style.fontSize = "24px"));
  });
  await page.getByRole("button", { name: "Plan a menu", exact: true }).click();
  let release!: () => void, arrived!: () => void;
  const held = new Promise<void>((r) => (release = r)),
    seen = new Promise<void>((r) => (arrived = r));
  await page.route("**/api/v1/favorite-weeks/*", async (r) => {
    arrived();
    await held;
    await r.fulfill({
      status: 503,
      json: { error: { code: "unavailable", message: "Fixture check failed" } },
    });
  });
  await page.getByRole("button", { name: "Review menu", exact: true }).click();
  await seen;
  await expect(
    page.getByRole("heading", { name: "Plan dinners", exact: true }),
  ).not.toBeFocused();
  release();
  await expect(page.getByRole("alert")).toContainText("Fixture check failed");
  await expect(
    page.getByRole("heading", { name: "Plan dinners", exact: true }),
  ).not.toBeFocused();
  await page.unroute("**/api/v1/favorite-weeks/*");
  await page.getByRole("button", { name: "Review menu", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await page.getByRole("button", { name: "Keep my menu", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Plan dinners", exact: true }),
  ).not.toBeFocused();
  await page.getByRole("button", { name: "Review menu", exact: true }).click();
  await page
    .getByRole("button", { name: "Review replacement", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "Review planned dinners", exact: true }),
  ).toBeFocused();
  await visibleDestination(page, ".planner");
});

test("preference save waits for an open dialog before moving focus", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 650 });
  const signed = await shoppingAccount(page);
  await page.goto("/app/?view=profile");
  let release!: () => void, arrived!: () => void;
  const held = new Promise<void>((resolve) => (release = resolve));
  const seen = new Promise<void>((resolve) => (arrived = resolve));
  await page.route("**/api/v1/profile", async (route) => {
    if (route.request().method() !== "PUT") return route.continue();
    arrived();
    await held;
    await route.fulfill({
      headers: { "X-MyChef-User": signed.user.id },
      json: signed.user,
    });
  });
  await page
    .getByRole("button", { name: "Save preferences", exact: true })
    .click();
  await seen;
  await page.evaluate(() => {
    const dialog = document.createElement("dialog");
    const close = document.createElement("button");
    close.textContent = "Dismiss test dialog";
    close.onclick = () => dialog.close();
    dialog.append(close);
    document.body.append(dialog);
    dialog.showModal();
  });
  release();
  await expect(
    page.getByText("Preferences saved.", { exact: true }),
  ).toBeVisible();
  const dismiss = page.getByRole("button", { name: "Dismiss test dialog" });
  await expect(dismiss).toBeFocused();
  await dismiss.click();
  await expect(
    page.getByRole("heading", { name: "Your profile.", exact: true }),
  ).toBeFocused();
  await visibleDestination(page, ".profile-heading h2");
});
