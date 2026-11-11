import { expect, request as playwrightRequest, test } from "@playwright/test";
import { registerFixture } from "./support/registration";

test("preferences → generated menu → recipe → refresh preserves the provider result", async ({
  page,
}) => {
  await page.clock.setFixedTime(new Date("2026-12-31T12:00:00Z"));
  await registerFixture(page, {
    firstName: "Ada",
    lastName: "Chef",
    username: "ada-e2e",
    email: "ada-e2e@example.test",
    password: "ChefTest2026!",
  });
  await page.getByRole("button", { name: "Profile", exact: true }).click();
  await page.getByLabel("Diet").fill("Vegetarian");
  await page
    .getByLabel("Cooking effort", { exact: true })
    .selectOption("Moderate Effort (Weekend)");
  await page.getByRole("button", { name: "Peanuts", exact: true }).click();
  await page.getByLabel("Disliked ingredients").fill("Mushrooms");
  await page.getByRole("button", { name: "Save preferences" }).click();
  await expect(
    page.getByText("Preferences saved.", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Meals", exact: true }).click();
  await page.getByRole("button", { name: "Plan a menu", exact: true }).click();
  await page.getByLabel("First meal date").fill("2026-12-31");
  await page.getByLabel("Number of meals", { exact: true }).fill("2");
  await expect(page.getByLabel("Region", { exact: true })).toHaveCount(0);
  await expect(page.getByLabel("Cooking effort", { exact: true })).toHaveValue(
    "60",
  );
  await page.getByRole("button", { name: "Review menu", exact: true }).click();
  const submitted = page.waitForResponse(
    (r) =>
      r.url().endsWith("/api/v1/generations") &&
      r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Plan my week", exact: true }).click();
  const response = await submitted;
  expect(response.request().postDataJSON()).toMatchObject({
    region: "Belgium",
    maxMinutes: 60,
  });
  const operation = await response.json();
  await expect(
    page.getByRole("button", { name: "Open Lemon garden pasta", exact: true }),
  ).toHaveCount(2);
  await expect(page.locator(".recipe-card").nth(0)).toHaveAttribute(
    "aria-current",
    "date",
  );
  await expect(page.locator(".recipe-card").nth(1)).not.toHaveAttribute(
    "aria-current",
    "date",
  );
  await page.screenshot({
    path: "../test-results/branding/menu.png",
    fullPage: true,
  });
  await page.route("**/api/v1/recipes/*", async (route) => {
    const response = await route.fetch();
    await new Promise((resolve) => setTimeout(resolve, 200));
    await route.fulfill({ response });
  });
  await page.evaluate(() => window.scrollTo(0, document.body.scrollHeight));
  await page
    .getByRole("button", { name: "Open Lemon garden pasta", exact: true })
    .first()
    .click();
  await expect.poll(() => page.evaluate(() => window.scrollY)).toBe(0);
  await page.unroute("**/api/v1/recipes/*");
  await expect(
    page.getByText("Simmer the vegetables gently for ten minutes.", {
      exact: true,
    }),
  ).toBeVisible();
  await expect(
    page.getByText("0.12345678901234567890123456789 g", { exact: true }),
  ).toBeVisible();
  await page.screenshot({
    path: "../test-results/branding/recipe.png",
    fullPage: true,
  });
  await page.reload();
  await expect(
    page.getByText("Simmer the vegetables gently for ten minutes.", {
      exact: true,
    }),
  ).toBeVisible();
  const session = await (await page.request.get("/api/v1/session")).json();
  expect(session.user.preferences.diet).toEqual(["Vegetarian"]);
  expect(session.user.preferences.allergens).toEqual(["Peanuts"]);
  const complete = await (
    await page.request.get(`/api/v1/generations/${operation.id}`)
  ).json();
  expect(complete.status).toBe("succeeded");
  const menu = await (
    await page.request.get(`/api/v1/menus/${complete.menuId}`)
  ).json();
  expect(menu.recipes).toHaveLength(2);
  expect(menu.recipes[0].id).not.toBe(menu.recipes[1].id);
  expect(menu.recipes[0].ingredients[0].name).toBe("Carrot");
  expect(menu.recipes[1].ingredients[0].name).toBe("Leek");
  expect(menu.recipes[0].ingredients[0].quantity).toBe(
    "0.12345678901234567890123456789",
  );
  const stranger = await playwrightRequest.newContext({
    baseURL: new URL(page.url()).origin,
  });
  expect(
    (await stranger.get(`/api/v1/recipes/${menu.recipes[0].id}`)).status(),
  ).toBe(401);
  await stranger.dispose();

  const originalIDs = menu.recipes.map((recipe: { id: string }) => recipe.id);
  const requestBody = {
    ...response.request().postDataJSON(),
    dates: ["2026-12-30"],
  };
  const refused = await page.request.post("/api/v1/generations", {
    headers: {
      "X-CSRF-Token": session.csrfToken,
      Origin: new URL(page.url()).origin,
      "Idempotency-Key": "no-replacement-consent",
    },
    data: requestBody,
  });
  expect(refused.status()).toBe(409);
  expect((await refused.json()).error.code).toBe("menu_exists");
  await page.getByRole("button", { name: "Meals", exact: true }).click();
  await page.getByRole("button", { name: "Plan a menu", exact: true }).click();
  await page.getByLabel("First meal date").fill("2026-12-30");
  await page.getByLabel("Number of meals", { exact: true }).fill("1");
  let posts = 0;
  page.on("request", (request) => {
    if (
      request.url().endsWith("/api/v1/generations") &&
      request.method() === "POST"
    )
      posts++;
  });
  await page.getByRole("button", { name: "Review menu", exact: true }).click();
  const dialog = page.getByRole("dialog", {
    name: "You have meals planned.",
    exact: true,
  });
  await expect(dialog).toBeVisible();
  const keep = dialog.getByRole("button", {
    name: "Keep my menu",
    exact: true,
  });
  const proceed = dialog.getByRole("button", {
    name: "Review replacement",
    exact: true,
  });
  await expect(keep).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(proceed).toBeFocused();
  await page
    .getByRole("button", { name: "Review menu", exact: true })
    .evaluate((button) => button.focus());
  await expect(proceed).toBeFocused();
  await page.keyboard.press("Shift+Tab");
  await expect(keep).toBeFocused();
  for (const width of [320, 390]) {
    await page.setViewportSize({ width, height: 812 });
    const bounds = await dialog.boundingBox();
    expect(bounds!.x).toBeGreaterThanOrEqual(0);
    expect(bounds!.width).toBeLessThanOrEqual(width);
  }
  await page.screenshot({
    path: "../test-results/branding/replace-week-mobile.png",
  });
  await keep.click();
  await expect(dialog).not.toBeVisible();
  await expect(page.getByLabel("First meal date")).toHaveValue("2026-12-30");
  await page.getByRole("button", { name: "Review menu", exact: true }).click();
  await expect(dialog).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(dialog).not.toBeVisible();
  expect(posts).toBe(0);
  const unchanged = await (
    await page.request.get("/api/v1/menus?from=2026-12-28&to=2027-01-04")
  ).json();
  expect(unchanged.recipes.map((recipe: { id: string }) => recipe.id)).toEqual(
    originalIDs,
  );
  await page.getByRole("button", { name: "Review menu", exact: true }).click();
  await proceed.click();
  const replaced = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/v1/generations") &&
      response.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Plan my week", exact: true }).click();
  const replacementResponse = await replaced;
  expect(replacementResponse.status()).toBe(202);
  expect(replacementResponse.request().postDataJSON().replaceExisting).toBe(
    true,
  );
  await expect(page.locator(".recipe-card")).toHaveCount(1);
  const replacementHistory = await (
    await page.request.get("/api/v1/menus?from=2026-12-28&to=2027-01-04")
  ).json();
  expect(replacementHistory.recipes).toHaveLength(1);
  expect(replacementHistory.recipes[0].date).toBe("2026-12-30");
  for (const id of originalIDs)
    expect((await page.request.get(`/api/v1/recipes/${id}`)).status()).toBe(
      404,
    );
  await page.reload();
  await expect(page.locator(".recipe-card")).toHaveCount(1);

  // Simulate a stale review in another tab; the backend still requires consent.
  await page.route("**/api/v1/menus?*", async (route) => {
    if (new URL(route.request().url()).searchParams.get("limit") === "1") {
      await route.fulfill({
        headers: { "X-MyChef-User": session.user.id },
        json: { recipes: [], nextOffset: null },
      });
    } else await route.continue();
  });
  await page.getByRole("button", { name: "Plan a menu", exact: true }).click();
  await page.getByLabel("First meal date").fill("2026-12-30");
  await page.getByLabel("Number of meals", { exact: true }).fill("1");
  await page.getByLabel("Adults", { exact: true }).fill("3");
  await page.getByLabel("Children", { exact: true }).fill("1");
  await page.getByRole("button", { name: "Review menu", exact: true }).click();
  await page.getByRole("button", { name: "Plan my week", exact: true }).click();
  await expect(
    page.getByText(
      "Your week has changed. Review your menu again to choose whether to replace it.",
      { exact: true },
    ),
  ).toBeVisible();
  await expect(page.getByLabel("First meal date")).toHaveValue("2026-12-30");
  await expect(page.getByLabel("Adults", { exact: true })).toHaveValue("3");
  await expect(page.getByLabel("Children", { exact: true })).toHaveValue("1");
  await page.unroute("**/api/v1/menus?*");
  await page.getByRole("button", { name: "Review menu", exact: true }).click();
  await expect(dialog).toBeVisible();
  await proceed.click();
  // Keep this replacement in flight so the following conflict proof cannot
  // accidentally pass by snapshotting the previous menu before this one saves.
  await page.route(
    "**/api/v1/generations",
    async (route) => {
      const input = route.request().postDataJSON();
      await route.continue({
        postData: JSON.stringify({ ...input, region: "__slow" }),
      });
    },
    { times: 1 },
  );
  const recovered = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/v1/generations") &&
      response.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Plan my week", exact: true }).click();
  const recoveredResponse = await recovered;
  expect(recoveredResponse.status()).toBe(202);
  const recoveredOperation = await recoveredResponse.json();
  await expect
    .poll(
      async () =>
        (
          await (
            await page.request.get(
              `/api/v1/generations/${recoveredOperation.id}`,
            )
          ).json()
        ).status,
    )
    .toBe("succeeded");
  await expect(page.locator(".recipe-card")).toHaveCount(1);

  // A conflict can also be discovered after POST was accepted, while saving.
  // Generation succeeds before its independent image worker finishes.
  // Compare complete snapshots only after the fixture image has settled.
  await expect
    .poll(async () => {
      const history = await (
        await page.request.get("/api/v1/menus?from=2026-12-28&to=2027-01-04")
      ).json();
      return history.recipes.map(
        (recipe: { imageStatus: string }) => recipe.imageStatus,
      );
    })
    .toEqual(["unavailable"]);
  const beforeConflict = await (
    await page.request.get("/api/v1/menus?from=2026-12-28&to=2027-01-04")
  ).json();
  await page.route("**/api/v1/menus?*", async (route) => {
    if (new URL(route.request().url()).searchParams.get("limit") === "1")
      await route.fulfill({
        headers: { "X-MyChef-User": session.user.id },
        json: { recipes: [], nextOffset: null },
      });
    else await route.continue();
  });
  const conflictOperation = {
    id: "90001",
    status: "running",
    stage: "generating",
    menuId: null,
    errorCode: null,
    errorMessage: null,
  };
  let conflictPosts = 0;
  await page.route("**/api/v1/generations", async (route) => {
    expect(route.request().method()).toBe("POST");
    expect(route.request().postDataJSON().replaceExisting).toBeUndefined();
    conflictPosts++;
    await route.fulfill({
      status: 202,
      headers: { "X-MyChef-User": session.user.id },
      json: conflictOperation,
    });
  });
  await page.route("**/api/v1/generations/90001", (route) =>
    route.fulfill({
      headers: { "X-MyChef-User": session.user.id },
      json: {
        ...conflictOperation,
        status: "failed",
        stage: "failed",
        errorCode: "menu_exists",
        errorMessage: "A menu now exists for this week.",
      },
    }),
  );
  await page.getByRole("button", { name: "Plan a menu", exact: true }).click();
  await page.getByLabel("First meal date").fill("2026-12-30");
  await page.getByLabel("Number of meals", { exact: true }).fill("2");
  await page.getByLabel("Adults", { exact: true }).fill("4");
  await page.getByLabel("Children", { exact: true }).fill("2");
  await page.getByLabel("Cooking effort", { exact: true }).selectOption("120");
  await page.getByLabel("Budget", { exact: true }).selectOption("high");
  await page.getByRole("button", { name: "Review menu", exact: true }).click();
  await page.getByRole("button", { name: "Plan my week", exact: true }).click();
  await expect(
    page.getByText(
      "Your week has changed. Review your menu again to choose whether to replace it.",
      { exact: true },
    ),
  ).toBeVisible();
  await expect(page.getByLabel("First meal date")).toHaveValue("2026-12-30");
  await expect(page.getByLabel("Number of meals", { exact: true })).toHaveValue(
    "2",
  );
  await expect(page.getByLabel("Adults", { exact: true })).toHaveValue("4");
  await expect(page.getByLabel("Children", { exact: true })).toHaveValue("2");
  await expect(page.getByLabel("Cooking effort", { exact: true })).toHaveValue(
    "120",
  );
  await expect(page.getByLabel("Budget", { exact: true })).toHaveValue("high");
  expect(
    await page.evaluate(
      (id) => sessionStorage.getItem(`mychef.pending.${id}`),
      session.user.id,
    ),
  ).toBeNull();
  const afterConflict = await (
    await page.request.get("/api/v1/menus?from=2026-12-28&to=2027-01-04")
  ).json();
  expect(afterConflict).toEqual(beforeConflict);
  await page.unroute("**/api/v1/menus?*");
  await page.getByRole("button", { name: "Review menu", exact: true }).click();
  await expect(dialog).toBeVisible();
  await proceed.click();
  await expect(
    page.getByRole("button", { name: "Plan my week", exact: true }),
  ).toBeVisible();
  expect(conflictPosts).toBe(1);
});
