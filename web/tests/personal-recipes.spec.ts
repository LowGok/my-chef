import { expect, test } from "@playwright/test";
import {
  fillPersonalDraft,
  choosePersonalDuration,
  openPersonalLibrary,
  personalFixture,
  personalGrant,
  personalPhoto,
  personalRequest,
} from "./support/personal-recipes";
import type { PersonalRecipe } from "../src/api";
import { addDays, monday, today } from "../src/dates";

test("Profile library opens a private editor and saves only on explicit Save", async ({
  page,
}) => {
  await personalFixture(page, "personaleditor");
  await openPersonalLibrary(page);
  await expect(
    page.getByText("Your recipes stay private.", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Create a recipe", exact: true })
    .click();
  await fillPersonalDraft(page);
  let uploads = 0;
  page.on("request", (r) => {
    if (
      r.method() === "POST" &&
      new URL(r.url()).pathname === "/api/v1/personal-recipes"
    )
      uploads++;
  });
  await page.getByLabel("Choose recipe photo", { exact: true }).setInputFiles({
    name: "private-camera-original.png",
    mimeType: "image/png",
    buffer: personalPhoto,
  });
  await expect(page.locator(".personal-photo-preview img")).toBeVisible();
  expect(uploads).toBe(0);
  const invalid = await page
    .locator(".personal-editor input,.personal-editor textarea")
    .evaluateAll((elements) =>
      elements
        .filter((element) => !(element as HTMLInputElement).checkValidity())
        .map(
          (element) =>
            element.getAttribute("data-personal-field") ?? element.tagName,
        ),
    );
  expect(invalid).toEqual([]);
  const saving = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname === "/api/v1/personal-recipes" &&
      response.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Save", exact: true }).click();
  const saved = await saving;
  if (!saved.ok()) {
    const code = (await saved.json())?.error?.code;
    throw new Error(
      typeof code === "string" && /^[a-z_]{1,60}$/.test(code)
        ? `Recipe save rejected: ${code}`
        : "Recipe save failed.",
    );
  }
  await expect(
    page.getByRole("status").filter({ hasText: "Recipe saved." }),
  ).toBeVisible();
  expect(uploads).toBe(1);
  const photo = page.locator(".personal-detail img");
  await expect(photo).toBeVisible();
  expect(
    (await photo.getAttribute("src"))?.startsWith("/api/v1/personal-photos/"),
  ).toBe(true);
  expect(
    await page
      .locator(".personal-detail")
      .evaluate((e) =>
        e.textContent?.includes("12345678901234567890.00000000001"),
      ),
  ).toBe(true);
  expect(
    await page.evaluate(() =>
      document.activeElement?.matches(".personal-heading"),
    ),
  ).toBe(true);
  await page
    .getByRole("button", { name: "Back to my recipes", exact: true })
    .click();
  await expect(page.locator(".personal-row")).toHaveCount(1);
  const dimensions = await page
    .locator(".personal-row-image")
    .evaluate((e) => ({
      w: e.getBoundingClientRect().width,
      h: e.getBoundingClientRect().height,
    }));
  expect(Math.abs(dimensions.w - dimensions.h)).toBeLessThan(1);
});

test("Free accounts see their retained library and cannot open a new editor", async ({
  page,
}) => {
  await personalFixture(page, "personalfree", "free");
  await openPersonalLibrary(page);
  await expect(
    page.getByText("Premium is needed to create and edit recipes.", {
      exact: true,
    }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Create a recipe", exact: true }),
  ).toBeDisabled();
  await personalGrant(page, "premium");
  await page.reload();
  await expect(
    page.getByRole("button", { name: "Create a recipe", exact: true }),
  ).toBeEnabled();
});

test("editor rows, unsaved navigation and phone text sizes remain usable", async ({
  page,
}) => {
  await personalFixture(page, "personalphones");
  await openPersonalLibrary(page);
  await page
    .getByRole("button", { name: "Create a recipe", exact: true })
    .click();
  await fillPersonalDraft(page);
  await page
    .getByRole("button", { name: "Add ingredient", exact: true })
    .click();
  await page.getByLabel("Ingredient 2", { exact: true }).fill("Peas");
  await page
    .getByRole("button", { name: "Move ingredient 2 up", exact: true })
    .click();
  expect(
    (await page.getByLabel("Ingredient 1", { exact: true }).inputValue()) ===
      "Peas",
  ).toBe(true);
  await page
    .getByRole("button", { name: "Remove ingredient 2", exact: true })
    .click();
  for (const width of [320, 360, 390, 430]) {
    await page.setViewportSize({ width, height: 780 });
    for (const scale of ["100%", "200%"]) {
      await page.evaluate((value) => {
        document.documentElement.style.fontSize = value;
      }, scale);
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBe(true);
      for (const field of await page
        .locator(
          ".personal-editor input,.personal-editor textarea,.personal-editor select",
        )
        .all()) {
        expect(
          await field.evaluate((e) => parseFloat(getComputedStyle(e).fontSize)),
        ).toBeGreaterThanOrEqual(18);
      }
    }
  }
  page.once("dialog", (dialog) => dialog.dismiss());
  await page
    .getByRole("navigation", { name: "Main navigation" })
    .getByRole("button", { name: "Meals", exact: true })
    .click();
  await expect(page.locator(".personal-editor")).toBeVisible();
  page.once("dialog", (dialog) => dialog.accept());
  await page
    .getByRole("navigation", { name: "Main navigation" })
    .getByRole("button", { name: "Meals", exact: true })
    .click();
  await expect(page.locator(".personal-editor")).not.toBeVisible();
});

test("a conflicting save preserves the draft until the owner reviews a new version", async ({
  page,
}) => {
  await personalFixture(page, "personalconflict");
  await openPersonalLibrary(page);
  await page
    .getByRole("button", { name: "Create a recipe", exact: true })
    .click();
  await fillPersonalDraft(page);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.locator(".personal-detail")).toBeVisible();
  const list = await personalRequest<{ recipes: PersonalRecipe[] }>(
    page,
    "POST",
    "/personal-recipes/search",
    {},
  );
  const original = list.recipes[0];
  await page.getByRole("button", { name: "Edit recipe", exact: true }).click();
  await page
    .getByLabel("Title", { exact: true })
    .fill("A draft kept on this device");
  await personalRequest(page, "PUT", "/personal-recipes/" + original.id, {
    requestKey: crypto.randomUUID().replaceAll("-", ""),
    expectedVersion: original.versionId,
    photoAction: "keep",
    content: { ...original.content, title: "A newer saved supper" },
  });
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(
    page.getByRole("heading", {
      name: "A newer version is saved",
      exact: true,
    }),
  ).toBeFocused();
  expect(
    (await page.getByLabel("Title", { exact: true }).inputValue()) ===
      "A draft kept on this device",
  ).toBe(true);
  await page.getByText("Review the saved recipe", { exact: true }).click();
  expect(
    await page
      .locator(".personal-conflict")
      .evaluate((e) => e.textContent?.includes("A newer saved supper")),
  ).toBe(true);
  await page
    .getByRole("button", {
      name: "Save my draft as a new version",
      exact: true,
    })
    .click();
  await expect(page.locator(".personal-detail")).toBeVisible();
  expect(
    (await page.locator(".personal-heading").textContent()) ===
      "A draft kept on this device",
  ).toBe(true);
});

test("failed photo and field validation preserve the editor and focus the correction", async ({
  page,
}) => {
  await personalFixture(page, "personalcorrections");
  await openPersonalLibrary(page);
  await page
    .getByRole("button", { name: "Create a recipe", exact: true })
    .click();
  await fillPersonalDraft(page);
  await choosePersonalDuration(page, "Total time", 1);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Total time", exact: true }),
  ).toBeFocused();
  await choosePersonalDuration(page, "Total time", 20);
  await page.getByLabel("Choose recipe photo", { exact: true }).setInputFiles({
    name: "unsupported-private-photo.heic",
    mimeType: "image/heic",
    buffer: Buffer.from("not a decodable image"),
  });
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(
    page.getByLabel("Choose recipe photo", { exact: true }),
  ).toBeFocused();
  expect(
    (await page.getByLabel("Title", { exact: true }).inputValue()) ===
      "Private fixture supper",
  ).toBe(true);
  await page.getByLabel("Choose recipe photo", { exact: true }).setInputFiles({
    name: "private-camera-original.png",
    mimeType: "image/png",
    buffer: personalPhoto,
  });
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.locator(".personal-detail img")).toBeVisible();
});

test("expired personal queue entries are explained and removable before planning", async ({
  page,
}) => {
  await personalFixture(page, "personalblocked");
  await openPersonalLibrary(page);
  await page
    .getByRole("button", { name: "Create a recipe", exact: true })
    .click();
  await fillPersonalDraft(page);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.locator(".personal-detail")).toBeVisible();
  await page
    .getByRole("button", { name: "Add to next week", exact: true })
    .click();
  await expect(
    page.getByRole("status").filter({ hasText: "Added to next week." }),
  ).toBeVisible();
  await personalGrant(page, "expired");
  const week = addDays(monday(today()), 7);
  await page.goto("/app/?week=" + week);
  await page.getByRole("button", { name: "Plan a menu", exact: true }).click();
  await page.getByLabel("First meal date", { exact: true }).fill(week);
  await page.getByLabel("Number of meals", { exact: true }).fill("1");
  await page.getByRole("button", { name: "Review menu", exact: true }).click();
  await expect(
    page.getByRole("region", { name: "Blocked personal recipes", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Plan my week", exact: true }),
  ).toHaveCount(0);
  await page
    .getByRole("button", { name: "Remove blocked recipe", exact: true })
    .click();
  await expect(
    page.getByRole("region", { name: "Blocked personal recipes", exact: true }),
  ).not.toBeVisible();
  await page.getByRole("button", { name: "Review menu", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Plan my week", exact: true }),
  ).toBeVisible();
});

test("lost save response retries the same photo outcome without duplicate recipes", async ({
  page,
}) => {
  await personalFixture(page, "personalretry");
  await openPersonalLibrary(page);
  await page
    .getByRole("button", { name: "Create a recipe", exact: true })
    .click();
  await fillPersonalDraft(page);
  await page.getByLabel("Choose recipe photo", { exact: true }).setInputFiles({
    name: "synthetic-private.png",
    mimeType: "image/png",
    buffer: personalPhoto,
  });
  let intercepted = false;
  await page.route("**/api/v1/personal-recipes", async (route) => {
    if (!intercepted && route.request().method() === "POST") {
      intercepted = true;
      try {
        await route.fetch();
      } catch {
        throw new Error("The isolated save transport failed.");
      }
      await route.abort("failed");
    } else await route.continue();
  });
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Retry save", exact: true }),
  ).toBeVisible();
  await expect(page.getByLabel("Title", { exact: true })).toBeDisabled();
  await expect(
    page.getByRole("button", { name: "Cooking time", exact: true }),
  ).toBeDisabled();
  await page.getByRole("button", { name: "Retry save", exact: true }).click();
  await expect(page.locator(".personal-detail img")).toBeVisible();
  const list = await personalRequest<{ recipes: PersonalRecipe[] }>(
    page,
    "POST",
    "/personal-recipes/search",
    {},
  );
  expect(list.recipes.length).toBe(1);
});

test("copying a meal needs explicit Save, finds an existing copy and allows Free deletion", async ({
  page,
}) => {
  await personalFixture(page, "personalcopy");
  await page
    .getByRole("navigation", { name: "Main navigation" })
    .getByRole("button", { name: "Meals", exact: true })
    .click();
  await page.getByRole("button", { name: "Plan a menu", exact: true }).click();
  await page.getByLabel("Number of meals", { exact: true }).fill("1");
  await page.getByRole("button", { name: "Review menu", exact: true }).click();
  await page.getByRole("button", { name: "Plan my week", exact: true }).click();
  await page.locator(".recipe-card .recipe-open").first().click();
  const sourceURL = page.url();
  const sourceTitle = await page.locator(".page-heading h2").textContent();
  await page.route("**/api/v1/personal-recipes/copy", async (route) => {
    try {
      const response = await route.fetch();
      const value = await response.json();
      value.draft.content.servings = 0;
      value.draft.sourceYield = ["A family dish"];
      value.draft.needsServings = true;
      await route.fulfill({ response, json: value });
    } catch {
      throw new Error("The synthetic ambiguous-yield copy failed.");
    }
  });
  await page
    .getByRole("button", { name: "Save as my recipe", exact: true })
    .click();
  await expect(page.locator(".personal-editor")).toBeVisible();
  await expect(page.getByLabel("Servings", { exact: true })).toHaveValue("");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  expect(
    (await page.getByLabel("Title", { exact: true }).inputValue()) ===
      sourceTitle,
  ).toBe(true);
  const empty = await personalRequest<{ recipes: PersonalRecipe[] }>(
    page,
    "POST",
    "/personal-recipes/search",
    {},
  );
  expect(empty.recipes.length).toBe(0);
  await page.getByLabel("Servings", { exact: true }).fill("3");
  await page.unroute("**/api/v1/personal-recipes/copy");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.locator(".personal-detail")).toBeVisible();
  await page.goto(sourceURL);
  await page
    .getByRole("button", { name: "Save as my recipe", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "Already in My recipes", exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Open saved recipe", exact: true })
    .click();
  await expect(page.locator(".personal-detail")).toBeVisible();
  await personalGrant(page, "free");
  await page.reload();
  await page.locator(".personal-row").first().click();
  await expect(
    page.getByRole("button", { name: "Edit recipe", exact: true }),
  ).toBeDisabled();
  await page
    .getByRole("button", { name: "Delete recipe", exact: true })
    .click();
  const dialog = page.getByRole("dialog", {
    name: "Delete recipe",
    exact: true,
  });
  await dialog
    .getByRole("button", { name: "Delete recipe", exact: true })
    .click();
  await expect(page.locator(".personal-row")).toHaveCount(0);
  await page.goto(sourceURL);
  expect(
    (await page.locator(".page-heading h2").textContent()) === sourceTitle,
  ).toBe(true);
});

test("library pagination and literal search keep private title terms out of URLs", async ({
  page,
}) => {
  await personalFixture(page, "personalpages");
  await openPersonalLibrary(page);
  await page
    .getByRole("button", { name: "Create a recipe", exact: true })
    .click();
  await fillPersonalDraft(page);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.locator(".personal-detail")).toBeVisible();
  const current = await personalRequest<{ recipes: PersonalRecipe[] }>(
    page,
    "POST",
    "/personal-recipes/search",
    {},
  );
  for (let index = 0; index < 12; index++)
    await personalRequest(page, "POST", "/personal-recipes", {
      requestKey: crypto.randomUUID().replaceAll("-", ""),
      expectedVersion: "",
      photoAction: "keep",
      content: {
        ...current.recipes[0].content,
        title: index === 0 ? "A fixture % supper" : `Library fixture ${index}`,
      },
    });
  await page
    .getByRole("button", { name: "Back to my recipes", exact: true })
    .click();
  await expect(page.locator(".personal-row")).toHaveCount(12);
  await page
    .getByRole("button", { name: "Load more recipes", exact: true })
    .click();
  await expect(page.locator(".personal-row")).toHaveCount(13);
  await page.getByLabel("Search by title", { exact: true }).fill("%");
  await page.getByRole("button", { name: "Search", exact: true }).click();
  await expect(page.locator(".personal-row")).toHaveCount(1);
  expect(new URL(page.url()).search.includes("%")).toBe(false);
});

test("planned personal photos survive library deletion and favorites keep Premium assignment rules", async ({
  page,
}) => {
  await personalFixture(page, "personalplanned");
  await openPersonalLibrary(page);
  await page
    .getByRole("button", { name: "Create a recipe", exact: true })
    .click();
  await fillPersonalDraft(page);
  await page.getByLabel("Choose recipe photo", { exact: true }).setInputFiles({
    name: "synthetic-private.png",
    mimeType: "image/png",
    buffer: personalPhoto,
  });
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.locator(".personal-detail img")).toBeVisible();
  await page
    .getByRole("button", { name: "Add to next week", exact: true })
    .click();
  await expect(
    page.getByRole("status").filter({ hasText: "Added to next week." }),
  ).toBeVisible();
  const week = addDays(monday(today()), 7);
  await page.goto("/app/?week=" + week);
  await page.getByRole("button", { name: "Plan a menu", exact: true }).click();
  await page.getByLabel("First meal date", { exact: true }).fill(week);
  await page.getByLabel("Number of meals", { exact: true }).fill("1");
  await page.getByRole("button", { name: "Review menu", exact: true }).click();
  await expect(
    page.locator(".planner .personal-version").first(),
  ).toBeVisible();
  await page.getByRole("button", { name: "Plan my week", exact: true }).click();
  const photo = page.locator(".recipe-card .recipe-image img").first();
  await expect(photo).toBeVisible();
  const photoURL = await photo.getAttribute("src");
  expect(photoURL?.startsWith("/api/v1/personal-photos/")).toBe(true);
  await page.locator(".recipe-card .recipe-open").first().click();
  await page
    .getByRole("button", { name: "Add to favorites", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "Remove from favorites", exact: true }),
  ).toBeVisible();
  await personalGrant(page, "free");
  await page.goto("/app/?view=profile");
  await page.locator(".favorite-card").first().click();
  const favorite = page.getByRole("dialog", {
    name: "Favorite recipe",
    exact: true,
  });
  await expect(
    favorite.getByRole("button", { name: "Add to next week", exact: true }),
  ).toBeDisabled();
  await expect(
    favorite.getByRole("button", { name: "Swap this week", exact: true }),
  ).toBeDisabled();
  await favorite
    .getByRole("button", { name: "Close favorite", exact: true })
    .click();
  await openPersonalLibrary(page);
  await page.locator(".personal-row").first().click();
  await page
    .getByRole("button", { name: "Delete recipe", exact: true })
    .click();
  await page
    .getByRole("dialog", { name: "Delete recipe", exact: true })
    .getByRole("button", { name: "Delete recipe", exact: true })
    .click();
  await expect(page.locator(".personal-row")).toHaveCount(0);
  await page.goto("/app/?view=profile");
  await expect(page.locator(".favorite-card")).toHaveCount(0);
  await page.goto("/app/?week=" + week);
  const retained = page.locator(".recipe-card .recipe-image img").first();
  await expect(retained).toBeVisible();
  expect((await retained.getAttribute("src")) === photoURL).toBe(true);
  await expect
    .poll(() =>
      retained.evaluate(
        (image) => (image as HTMLImageElement).naturalWidth > 0,
      ),
    )
    .toBe(true);
});

test("swapping a personal recipe then queuing it uses distinct deliberate requests", async ({
  page,
}) => {
  await personalFixture(page, "personalswap");
  await page
    .getByRole("navigation", { name: "Main navigation" })
    .getByRole("button", { name: "Meals", exact: true })
    .click();
  await page.getByRole("button", { name: "Plan a menu", exact: true }).click();
  await page.getByLabel("Number of meals", { exact: true }).fill("1");
  await page.getByRole("button", { name: "Review menu", exact: true }).click();
  await page.getByRole("button", { name: "Plan my week", exact: true }).click();
  await expect(page.locator(".recipe-card").first()).toBeVisible();
  await page
    .getByRole("navigation", { name: "Main navigation" })
    .getByRole("button", { name: "Profile", exact: true })
    .click();
  await openPersonalLibrary(page);
  await page
    .getByRole("button", { name: "Create a recipe", exact: true })
    .click();
  await fillPersonalDraft(page);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.locator(".personal-detail")).toBeVisible();
  await page
    .getByRole("button", { name: "Swap this week’s meal", exact: true })
    .click();
  const dialog = page.getByRole("dialog", {
    name: "Swap this week’s meal",
    exact: true,
  });
  await expect(
    dialog.getByLabel("Meal to replace", { exact: true }),
  ).toBeEnabled();
  await dialog
    .getByLabel("Meal to replace", { exact: true })
    .selectOption({ index: 1 });
  await dialog
    .getByRole("button", { name: "Replace meal", exact: true })
    .click();
  await expect(
    page.getByRole("status").filter({ hasText: "Meal replaced." }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Add to next week", exact: true })
    .click();
  await expect(
    page.getByRole("status").filter({ hasText: "Added to next week." }),
  ).toBeVisible();
});
