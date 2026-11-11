import { expect, test } from "@playwright/test";
import {
  shoppingAccount,
  shoppingEmail,
  shoppingPassword,
} from "./support/shopping";
import { addDays, monday, today } from "../src/dates";
test.setTimeout(150_000);
test.use({ viewport: { width: 390, height: 844 }, hasTouch: true });
async function fixture(page: import("@playwright/test").Page) {
  const signed = await shoppingAccount(page);
  let list = await (
    await page.request.get("/api/v1/shopping/2026-11-02")
  ).json();
  if (!list.meals.length) {
    const response = await page.request.post("/api/v1/generations", {
      headers: {
        Origin: new URL(page.url()).origin,
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
      },
    });
    expect(response.status()).toBe(202);
    const op = await response.json();
    await expect
      .poll(
        async () =>
          (
            await (
              await page.request.get(`/api/v1/generations/${op.id}`)
            ).json()
          ).status,
      )
      .toBe("succeeded");
    list = await (await page.request.get("/api/v1/shopping/2026-11-02")).json();
  }
  await page.request.delete(
    `/api/v1/recipes/${list.meals[0].recipeId}/favorite`,
    {
      headers: {
        Origin: new URL(page.url()).origin,
        "X-CSRF-Token": signed.csrfToken,
      },
    },
  );
  return { signed, list };
}
test("favorite cards recap and direct next-week add preserve preferences", async ({
  page,
}) => {
  const { signed, list } = await fixture(page);
  const id = list.meals[0].recipeId;
  await page.goto(`/app/?recipe=${id}&week=2026-11-02`);
  const heart = page.getByRole("button", {
    name: "Add to favorites",
    exact: true,
  });
  await heart.click({ timeout: 5000 });
  await expect(
    page.getByRole("button", { name: "Remove from favorites", exact: true }),
  ).toHaveAttribute("aria-pressed", "true");
  await page.getByRole("button", { name: "Profile", exact: true }).click();
  const favorites = page.getByRole("region", {
    name: "My favorites",
    exact: true,
  });
  await expect(favorites).toBeVisible();
  const settings = page.getByRole("region", {
    name: "Profile settings",
    exact: true,
  });
  await expect(
    settings.getByRole("region", { name: "My favorites", exact: true }),
  ).toBeVisible();
  await expect(settings.locator(".preferences-details")).toBeVisible();
  await expect(settings.locator(".panel")).toHaveCount(0);
  expect(await favorites.evaluate((e) => !!e.closest("form"))).toBe(false);
  const preferences = page.locator(".preferences-details");
  const save = preferences.getByRole("button", {
    name: "Save preferences",
    exact: true,
  });
  await expect(save).toBeVisible();
  await preferences.locator("summary").click();
  await expect(save).not.toBeVisible();
  await preferences.locator("summary").press("Enter");
  await expect(save).toBeVisible();
  await favorites.locator("summary").click();
  await expect(favorites.locator(".favorites-grid")).not.toBeVisible();
  await favorites.locator("summary").press("Enter");
  await expect(favorites.locator(".favorites-grid")).toBeVisible();
  await page
    .getByLabel("Disliked ingredients")
    .fill("Keep this unsaved preference");
  await favorites.locator(".favorite-card").first().click();
  const dialog = page.getByRole("dialog", { name: "Favorite recipe" });
  await expect(dialog).toBeVisible();
  const add = dialog.getByRole("button", {
      name: "Add to next week",
      exact: true,
    }),
    swap = dialog.getByRole("button", { name: "Swap this week", exact: true });
  for (const width of [320, 390, 760]) {
    await page.setViewportSize({ width, height: 844 });
    const a = await add.boundingBox(),
      b = await swap.boundingBox();
    expect(Math.abs(a!.width - b!.width)).toBeLessThan(1);
    expect(a!.y).toBe(b!.y);
    expect(a!.height).toBe(b!.height);
    expect(
      await dialog.evaluate((e) => e.scrollWidth <= e.clientWidth + 1),
    ).toBe(true);
  }
  await page.keyboard.press("Escape");
  await expect(favorites.locator(".favorite-card").first()).toBeFocused();
  await favorites.locator(".favorite-card").first().click();
  await page.setViewportSize({ width: 390, height: 844 });
  await dialog
    .getByRole("button", { name: "View full recipe", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "Back to favorites", exact: false }),
  ).toBeVisible();
  expect(await page.evaluate(() => scrollY)).toBe(0);
  await page
    .getByRole("button", { name: "Back to favorites", exact: false })
    .click();
  await expect(page.getByLabel("Disliked ingredients")).toHaveValue(
    "Keep this unsaved preference",
  );
  await favorites.locator(".favorite-card").first().click();
  await dialog.screenshot({
    path: "../test-results/shopping/favorites-dialog.png",
  });
  await add.click();
  await expect(dialog.getByRole("status")).toContainText("Added to next week.");
  await expect(dialog.locator('input[type="date"]')).toHaveCount(0);
  await dialog
    .getByRole("button", { name: "Close favorite", exact: true })
    .click();
  await expect(page.getByLabel("Disliked ingredients")).toHaveValue(
    "Keep this unsaved preference",
  );
  await page.getByRole("button", { name: "Go to Meals", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Meals", exact: true }),
  ).toHaveAttribute("aria-current", "page");
  const next = addDays(monday(today()), 7);
  await page.goto(`/app/?week=${next}`);
  await expect(
    page.getByRole("region", { name: "Saved for this week" }),
  ).toContainText(list.meals[0].title);
  // When next week arrives, planning must clear this same mounted week's pending list.
  await page.clock.setFixedTime(new Date(`${next}T12:00:00Z`));
  await page.getByRole("button", { name: "Plan a menu", exact: true }).click();
  await page.getByLabel("First meal date", { exact: true }).fill(next);
  await page.getByLabel("Number of meals", { exact: true }).fill("1");
  await page.getByRole("button", { name: "Review menu", exact: true }).click();
  await expect(
    page.getByText("Saved recipes included", { exact: true }),
  ).toBeVisible();
  const submitted = page.waitForResponse(
    (r) =>
      r.url().endsWith("/api/v1/generations") &&
      r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Plan my week", exact: true }).click();
  if ((await submitted).status() === 429) {
    await expect
      .poll(
        async () => {
          const retried = page.waitForResponse(
            (r) =>
              r.url().endsWith("/api/v1/generations") &&
              r.request().method() === "POST",
          );
          await page
            .getByRole("button", { name: "Check request status", exact: true })
            .click();
          return (await retried).status();
        },
        { timeout: 65_000, intervals: [1000, 5000, 10000] },
      )
      .toBe(202);
  }
  await expect(page.getByText("Menu ready", { exact: false })).toBeVisible();
  await expect(
    page.getByRole("region", { name: "Saved for this week", exact: true }),
  ).toHaveCount(0);
  await expect(page.getByLabel("Browse week", { exact: true })).toHaveValue(
    next,
  );
  const planned = await (
    await page.request.get(`/api/v1/menus?from=${next}&to=${addDays(next, 7)}`)
  ).json();
  expect(planned.recipes).toHaveLength(1);
  expect(planned.recipes[0].title).toBe(list.meals[0].title);
  expect(
    (await (await page.request.get(`/api/v1/favorite-weeks/${next}`)).json())
      .recipes,
  ).toHaveLength(0);
  // Clean the favorite without deleting the independently saved queue copy.
  expect(
    (
      await page.request.delete(`/api/v1/recipes/${id}/favorite`, {
        headers: {
          Origin: new URL(page.url()).origin,
          "X-CSRF-Token": signed.csrfToken,
        },
      })
    ).ok(),
  ).toBe(true);
});
test("logo keyboard navigation and favorite request failure remain honest", async ({
  page,
}) => {
  const { list } = await fixture(page);
  await page.goto(`/app/?recipe=${list.meals[0].recipeId}&week=2026-11-02`);
  await page.route("**/api/v1/recipes/*/favorite", (route) => route.abort());
  await page
    .getByRole("button", { name: "Add to favorites", exact: true })
    .click();
  await expect(page.getByRole("alert")).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Add to favorites", exact: true }),
  ).toHaveAttribute("aria-pressed", "false");
  await page.getByRole("button", { name: "Profile", exact: true }).click();
  const logo = page.getByRole("button", { name: "Go to Meals", exact: true });
  await logo.focus();
  await page.keyboard.press("Enter");
  await expect(
    page.getByRole("heading", { name: /Let’s plan your week/ }),
  ).toBeVisible();
  expect(await page.evaluate(() => scrollY)).toBe(0);
});

test("favorite collection distinguishes errors and paginates without resetting form input", async ({
  page,
}) => {
  const { signed, list } = await fixture(page);
  const source = await (
    await page.request.get(`/api/v1/recipes/${list.meals[0].recipeId}`)
  ).json();
  let fail = true;
  await page.route("**/api/v1/favorites?*", async (route) => {
    if (fail) {
      await route.fulfill({
        status: 503,
        headers: { "X-MyChef-User": signed.user.id },
        contentType: "application/json",
        body: JSON.stringify({
          error: { code: "unavailable", message: "Fixture unavailable" },
        }),
      });
      return;
    }
    const offset = new URL(route.request().url()).searchParams.get("offset");
    await route.fulfill({
      status: 200,
      headers: { "X-MyChef-User": signed.user.id },
      contentType: "application/json",
      body: JSON.stringify({
        recipes: [
          {
            ...source,
            id: offset === "0" ? "1000" : "1001",
            title:
              offset === "0" ? "First saved recipe" : "Second saved recipe",
          },
        ],
        nextOffset: offset === "0" ? 1 : null,
      }),
    });
  });
  await page.goto("/app/?view=profile");
  const favorites = page.getByRole("region", {
    name: "My favorites",
    exact: true,
  });
  await expect(favorites.getByRole("alert")).toBeVisible();
  await expect(favorites.getByText(/Save recipes you/)).toHaveCount(0);
  fail = false;
  await favorites.getByRole("button", { name: "Retry favorites" }).click();
  await expect(
    favorites.getByRole("button", { name: "First saved recipe", exact: true }),
  ).toBeVisible();
  await page.getByLabel("Disliked ingredients").fill("Keep draft");
  await favorites.getByRole("button", { name: "Load more favorites" }).click();
  await expect(favorites.locator(".favorite-card")).toHaveCount(2);
  await expect(page.getByLabel("Disliked ingredients")).toHaveValue(
    "Keep draft",
  );
});

test("favorite swap replaces just the selected current-week meal", async ({
  page,
}) => {
  const { signed, list } = await fixture(page);
  const headers = {
    Origin: new URL(page.url()).origin,
    "X-CSRF-Token": signed.csrfToken,
  };
  const source = list.meals[0].recipeId;
  expect(
    (
      await page.request.put(`/api/v1/recipes/${source}/favorite`, { headers })
    ).ok(),
  ).toBe(true);
  const week = monday(today());
  let before = await (
    await page.request.get(`/api/v1/menus?from=${week}&to=${addDays(week, 7)}`)
  ).json();
  if (!before.recipes.length) {
    const op = await (
      await page.request.post("/api/v1/generations", {
        headers: { ...headers, "Idempotency-Key": crypto.randomUUID() },
        data: {
          dates: [week],
          adults: 2,
          children: 0,
          maxMinutes: 30,
          budget: "low",
          region: "Belgium",
          creativity: 2,
        },
      })
    ).json();
    await expect
      .poll(
        async () =>
          (
            await (
              await page.request.get(`/api/v1/generations/${op.id}`)
            ).json()
          ).status,
      )
      .toBe("succeeded");
    before = await (
      await page.request.get(
        `/api/v1/menus?from=${week}&to=${addDays(week, 7)}`,
      )
    ).json();
  }
  await page.goto("/app/?view=profile");
  await page
    .getByRole("region", { name: "My favorites" })
    .locator(".favorite-card")
    .first()
    .click();
  const dialog = page.getByRole("dialog", { name: "Favorite recipe" });
  await dialog
    .getByRole("button", { name: "Swap this week", exact: true })
    .click();
  const target = before.recipes[before.recipes.length - 1];
  await dialog.locator(`input[value="${target.id}"]`).check();
  await dialog
    .getByRole("button", { name: "Confirm swap", exact: true })
    .click();
  await expect(dialog.getByRole("status")).toContainText("Meal swapped.");
  const after = await (
    await page.request.get(`/api/v1/menus?from=${week}&to=${addDays(week, 7)}`)
  ).json();
  expect(after.recipes).toHaveLength(before.recipes.length);
  expect(after.recipes.some((r: { id: string }) => r.id === target.id)).toBe(
    false,
  );
  for (const r of before.recipes.filter(
    (r: { id: string }) => r.id !== target.id,
  ))
    expect(after.recipes.some((v: { id: string }) => v.id === r.id)).toBe(true);
});

test("late favorite responses cannot restore signed-out private data", async ({
  page,
}) => {
  const { signed, list } = await fixture(page);
  const source = await (
    await page.request.get(`/api/v1/recipes/${list.meals[0].recipeId}`)
  ).json();
  expect(
    (
      await page.request.put(`/api/v1/recipes/${source.id}/favorite`, {
        headers: {
          Origin: new URL(page.url()).origin,
          "X-CSRF-Token": signed.csrfToken,
        },
      })
    ).ok(),
  ).toBe(true);
  let release!: () => void, seen!: () => void;
  const held = new Promise<void>((r) => (release = r)),
    arrived = new Promise<void>((r) => (seen = r));
  await page.route("**/api/v1/favorites?*", async (route) => {
    seen();
    await held;
    await route
      .fulfill({
        status: 200,
        headers: { "X-MyChef-User": signed.user.id },
        contentType: "application/json",
        body: JSON.stringify({
          recipes: [{ ...source, title: "PRIVATE LATE FAVORITE" }],
          nextOffset: null,
        }),
      })
      .catch(() => {});
  });
  await page.goto("/app/?view=profile");
  await arrived;
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Sign in to MyChef", exact: true }),
  ).toBeVisible();
  release();
  await page.evaluate(() => new Promise(requestAnimationFrame));
  await expect(
    page.getByText("PRIVATE LATE FAVORITE", { exact: true }),
  ).toHaveCount(0);
  await page.unroute("**/api/v1/favorites?*");
  await page.getByLabel("Email", { exact: true }).fill(shoppingEmail);
  await page.getByLabel("Password", { exact: true }).fill(shoppingPassword);
  await page
    .getByRole("button", { name: "Sign in to MyChef", exact: true })
    .click();
  await page.getByRole("button", { name: "Profile", exact: true }).click();
  await expect(
    page
      .getByRole("region", { name: "My favorites" })
      .locator(".favorite-card"),
  ).toContainText([source.title]);
});

test("full next-week queue and long recap stay usable without page swipes", async ({
  page,
}) => {
  const { signed, list } = await fixture(page);
  const source = await (
    await page.request.get(`/api/v1/recipes/${list.meals[0].recipeId}`)
  ).json();
  const upcoming = addDays(monday(today()), 7);
  const headers = { "X-MyChef-User": signed.user.id };
  await page.route("**/api/v1/favorites?*", (route) =>
    route.fulfill({
      headers,
      json: {
        recipes: [
          {
            ...source,
            title: "A long saved recipe ".repeat(10),
            allergenNote:
              "Review ingredient labels and cross-contamination. ".repeat(45),
            ingredients: Array.from({ length: 30 }, (_, i) => ({
              id: String(i),
              name: `Ingredient ${i}`,
              quantity: "1",
              unit: "g",
            })),
          },
        ],
        nextOffset: null,
      },
    }),
  );
  await page.route(`**/api/v1/favorite-weeks/${upcoming}`, (route) =>
    route.fulfill({
      headers,
      json: {
        week: upcoming,
        revision: "full-queue",
        recipes: Array.from({ length: 7 }, (_, i) => ({
          ...source,
          id: `queued-${i}`,
        })),
        addedIds: Array.from({ length: 7 }, (_, i) => `queued-${i}`),
      },
    }),
  );
  await page.goto("/app/?view=profile");
  await page
    .getByRole("region", { name: "My favorites" })
    .locator(".favorite-card")
    .first()
    .click();
  const dialog = page.getByRole("dialog", { name: "Favorite recipe" });
  await expect(
    dialog.getByText("Next week already has seven saved recipes.", {
      exact: false,
    }),
  ).toBeVisible();
  await expect(
    dialog.getByRole("button", { name: "Add to next week", exact: true }),
  ).toBeDisabled();
  for (let i = 0; i < 12; i++) {
    await page.keyboard.press("Tab");
    expect(
      await page.evaluate(
        () => !!document.activeElement?.closest("dialog[open]"),
      ),
    ).toBe(true);
  }
  await page.evaluate(() => {
    const target = document.querySelector(".workspace")!,
      point = (x: number) =>
        new Touch({ identifier: 1, target, clientX: x, clientY: 240 });
    target.dispatchEvent(
      new TouchEvent("touchstart", { bubbles: true, touches: [point(300)] }),
    );
    target.dispatchEvent(
      new TouchEvent("touchend", {
        bubbles: true,
        touches: [],
        changedTouches: [point(80)],
      }),
    );
  });
  await expect(
    page.getByRole("button", { name: "Profile", exact: true }),
  ).toHaveAttribute("aria-current", "page");
  expect(await dialog.evaluate((e) => e.scrollWidth <= e.clientWidth + 1)).toBe(
    true,
  );
  await page.keyboard.press("Escape");
  await expect(dialog).not.toBeVisible();
});
