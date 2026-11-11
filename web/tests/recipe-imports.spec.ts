import { expect, test, type Page } from "@playwright/test";
import {
  personalFixture,
  openPersonalLibrary,
  personalPhoto,
} from "./support/personal-recipes";
import { privateAction } from "./support/private-action";

// Import content must never be copied into browser failure artifacts.
test.use({ trace: "off", screenshot: "off", video: "off" });
const fixtureContent = () => ({
  schemaVersion: 1 as const,
  title: "IMPORT-BROWSER-TITLE-CANARY",
  description: "A private imported recipe.",
  servings: 4,
  equipment: "",
  prepMinutes: 5,
  cookMinutes: 10,
  totalMinutes: 20,
  ingredients: [
    { name: "IMPORT-BROWSER-INGREDIENT-CANARY", quantity: null, unit: null },
  ],
  instructions: ["Cook gently."],
  tips: [],
  allergens: [],
  allergenNote: "",
});
async function fixtureImports(page: Page, mode = "structured") {
  const session = await (await page.request.get("/api/v1/session")).json();
  const headers = {
    "X-MyChef-User": session.user.id,
    "Cache-Control": "private, no-store",
  };
  const source =
    mode === "photos"
      ? { kind: "photos", importedAt: "2026-11-08T00:00:00Z" }
      : {
          kind: "url",
          url: "https://recipes.example.test/soup?recipe_id=7",
          importedAt: "2026-11-08T00:00:00Z",
        };
  const draft = {
    content: fixtureContent(),
    source,
    sourceYield: [],
    issues: [
      { field: "allergens", reason: "unverified" },
      {
        field: "ingredients.0.quantity",
        reason: "ambiguous",
        sourceText: "A handful",
      },
    ],
    revision: "1".repeat(64),
  };
  const base = {
    id: "a".repeat(32),
    kind: source.kind,
    stage: "ready",
    expiresAt: "2026-11-09T00:00:00Z",
    canReadWithAI: false,
    candidates: [],
    draft,
  };
  let state: Record<string, unknown> = { ...base };
  let starts = 0,
    reviews = 0,
    saves = 0,
    ai = 0,
    selections = 0,
    identical = true,
    cleanNames = true;
  let firstSave = "";
  await page.route("**/api/v1/recipe-imports/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/availability"))
      return route.fulfill({
        headers,
        json: {
          available: true,
          aiAvailable: true,
          photosAvailable: true,
          fixture: true,
        },
      });
    if (path.endsWith("/url") || path.endsWith("/photos")) {
      starts++;
      cleanNames =
        cleanNames &&
        !route
          .request()
          .postDataBuffer()
          ?.includes(Buffer.from("private-page-name-canary"));
      state =
        mode === "fallback"
          ? {
              ...base,
              stage: "awaiting-ai",
              canReadWithAI: true,
              draft: undefined,
            }
          : mode === "multiple"
            ? {
                ...base,
                stage: "selection",
                draft: undefined,
                candidates: [
                  { id: "d".repeat(64), complete: true, draft },
                  {
                    id: "e".repeat(64),
                    complete: true,
                    draft: {
                      ...draft,
                      content: {
                        ...draft.content,
                        title: "Second sample recipe",
                      },
                    },
                  },
                ],
              }
            : { ...base };
      return route.fulfill({ headers, status: 202, json: state });
    }
    if (path.endsWith("/read-with-ai")) {
      ai++;
      state = { ...base };
      return route.fulfill({ headers, status: 202, json: state });
    }
    if (path.endsWith("/select")) {
      selections++;
      state = {
        ...base,
        draft: {
          ...draft,
          content: { ...draft.content, title: "Second sample recipe" },
        },
      };
      return route.fulfill({ headers, json: state });
    }
    if (path.endsWith("/review")) {
      reviews++;
      const content = route.request().postDataJSON().content;
      return route.fulfill({
        headers,
        json: {
          content,
          contentRevision: "2".repeat(64),
          requiredFields: [
            "title",
            "servings",
            "prepMinutes",
            "cookMinutes",
            "totalMinutes",
            "allergens",
            "ingredients.0",
            "instructions.0",
          ],
        },
      });
    }
    if (path.endsWith("/cancel"))
      return route.fulfill({ headers, status: 204 });
    return route.fulfill({ headers, json: state });
  });
  await page.route("**/api/v1/personal-recipes", async (route) => {
    if (route.request().method() !== "POST") return route.continue();
    saves++;
    const raw = route.request().postData() ?? "";
    if (saves === 1) {
      firstSave = raw;
      return route.abort("failed");
    }
    identical = identical && raw === firstSave;
    const input = route.request().postDataJSON();
    return route.fulfill({
      headers,
      status: 201,
      json: {
        id: "b".repeat(32),
        versionId: "c".repeat(32),
        currentVersionId: "c".repeat(32),
        content: input.content,
        sourceYield: [],
        imageUrl: null,
        photoId: null,
        importSource: source,
        createdAt: source.importedAt,
        updatedAt: source.importedAt,
      },
    });
  });
  return () => ({
    starts,
    reviews,
    saves,
    ai,
    selections,
    identical,
    cleanNames,
  });
}

test("URL import stays unsaved until field review and exact Save retry keeps the reviewed request", async ({
  page,
}) => {
  await privateAction(async () => {
    await personalFixture(page, "importreview");
    const counts = await fixtureImports(page);
    await openPersonalLibrary(page);
    await page
      .getByRole("button", { name: "Import recipe", exact: true })
      .click({ timeout: 1500 });
    await page
      .getByLabel("Recipe URL", { exact: true })
      .fill("https://recipes.example.test/soup?recipe_id=7");
    await page
      .getByRole("checkbox", {
        name: "I have permission to import this recipe for my personal use.",
        exact: true,
      })
      .check();
    await page.getByRole("button", { name: "Check page", exact: true }).click();
    await expect(page.locator(".personal-editor")).toBeVisible();
    expect(
      counts().starts === 1 && counts().reviews === 0 && counts().saves === 0,
    ).toBe(true);
    await expect(page.locator(".personal-photo-preview img")).toHaveCount(0);
    const link = page.getByRole("link", {
      name: "Original recipe",
      exact: true,
    });
    expect((await link.getAttribute("rel"))?.includes("noreferrer")).toBe(true);
    await page.getByRole("button", { name: "Save", exact: true }).click();
    expect(counts().saves === 0).toBe(true);
    const checks = page
      .getByRole("group", { name: "Review imported recipe", exact: true })
      .getByRole("checkbox");
    expect((await checks.count()) >= 8).toBe(true);
    for (const check of await checks.all()) await check.check();
    await page
      .getByLabel("Title", { exact: true })
      .fill("Amended private recipe");
    expect(
      await checks.evaluateAll((inputs) =>
        inputs.every((input) => !(input as HTMLInputElement).checked),
      ),
    ).toBe(true);
    for (const check of await checks.all()) await check.check();
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(
      page.getByRole("button", { name: "Retry save", exact: true }),
    ).toBeVisible();
    await page.getByRole("button", { name: "Retry save", exact: true }).click();
    await expect(page.locator(".personal-detail")).toBeVisible();
    const result = counts();
    expect(
      result.starts === 1 &&
        result.reviews === 1 &&
        result.saves === 2 &&
        result.identical,
    ).toBe(true);
    const leaked = await page.evaluate(() =>
      [
        ...Object.keys(localStorage).map((k) => localStorage.getItem(k)),
        ...Object.keys(sessionStorage).map((k) => sessionStorage.getItem(k)),
      ]
        .join(" ")
        .includes("IMPORT-BROWSER-"),
    );
    expect(leaked).toBe(false);
  });
});

test("AI fallback needs a separate choice and multiple recipes need selection", async ({
  page,
}) => {
  await privateAction(async () => {
    await personalFixture(page, "importfallback");
    const counts = await fixtureImports(page, "fallback");
    await openPersonalLibrary(page);
    await page
      .getByRole("button", { name: "Import recipe", exact: true })
      .click();
    await page
      .getByLabel("Recipe URL", { exact: true })
      .fill("https://recipes.example.test/plain");
    await page
      .getByRole("checkbox", {
        name: "I have permission to import this recipe for my personal use.",
        exact: true,
      })
      .check();
    await page.getByRole("button", { name: "Check page", exact: true }).click();
    await expect(
      page.getByRole("button", { name: "Read with AI", exact: true }),
    ).toBeDisabled();
    expect(counts().ai === 0 && counts().saves === 0).toBe(true);
    await page
      .getByRole("checkbox", {
        name: "I agree to send the recipe text or pages to Gemini for reading.",
        exact: true,
      })
      .check();
    await page
      .getByRole("button", { name: "Read with AI", exact: true })
      .click();
    await expect(page.locator(".personal-editor")).toBeVisible();
    expect(
      counts().ai === 1 && counts().starts === 1 && counts().saves === 0,
    ).toBe(true);
  });
});

test("recipe-page uploads keep their illustration separate and work at phone text sizes", async ({
  page,
}) => {
  await privateAction(async () => {
    await personalFixture(page, "importphotos");
    const counts = await fixtureImports(page, "photos");
    await openPersonalLibrary(page);
    await page
      .getByRole("button", { name: "Import recipe", exact: true })
      .click();
    for (const width of [320, 360, 390, 430]) {
      await page.setViewportSize({ width, height: 780 });
      for (const scale of ["100%", "200%"]) {
        await page.evaluate(
          (value) => (document.documentElement.style.fontSize = value),
          scale,
        );
        expect(
          await page.evaluate(
            () => document.documentElement.scrollWidth <= innerWidth,
          ),
        ).toBe(true);
        expect(
          await page
            .getByLabel("Recipe URL", { exact: true })
            .evaluate((e) => parseFloat(getComputedStyle(e).fontSize) >= 18),
        ).toBe(true);
      }
    }
    await page
      .getByRole("button", { name: "Recipe pages", exact: true })
      .click();
    await page.getByLabel("Recipe-page photos", { exact: true }).setInputFiles({
      name: "private-page-name-canary.png",
      mimeType: "image/png",
      buffer: personalPhoto,
    });
    await page
      .getByRole("checkbox", {
        name: "I have permission to import this recipe for my personal use.",
        exact: true,
      })
      .check();
    await page
      .getByRole("button", { name: "Read recipe pages", exact: true })
      .click();
    expect(counts().starts).toBe(0);
    await page
      .getByRole("checkbox", {
        name: "I agree to send the recipe text or pages to Gemini for reading.",
        exact: true,
      })
      .check();
    await page.getByLabel("Recipe-page photos", { exact: true }).setInputFiles({
      name: "private-page-name-canary-replaced.png",
      mimeType: "image/png",
      buffer: personalPhoto,
    });
    await expect(
      page.getByRole("checkbox", {
        name: "I agree to send the recipe text or pages to Gemini for reading.",
        exact: true,
      }),
    ).not.toBeChecked();
    await page
      .getByRole("checkbox", {
        name: "I agree to send the recipe text or pages to Gemini for reading.",
        exact: true,
      })
      .check();
    await page
      .getByRole("button", { name: "Read recipe pages", exact: true })
      .click();
    await expect(page.locator(".personal-editor")).toBeVisible();
    await expect(page.locator(".personal-photo-preview img")).toHaveCount(0);
    for (const width of [320, 360, 390, 430]) {
      await page.setViewportSize({ width, height: 780 });
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBe(true);
    }
    expect(
      counts().starts === 1 && counts().saves === 0 && counts().cleanNames,
    ).toBe(true);
  });
});

test("multiple candidates cannot silently combine into an editor", async ({
  page,
}) => {
  await privateAction(async () => {
    await personalFixture(page, "importmultiple");
    const counts = await fixtureImports(page, "multiple");
    await openPersonalLibrary(page);
    await page
      .getByRole("button", { name: "Import recipe", exact: true })
      .click();
    await page
      .getByLabel("Recipe URL", { exact: true })
      .fill("https://recipes.example.test/multiple");
    await page
      .getByRole("checkbox", {
        name: "I have permission to import this recipe for my personal use.",
        exact: true,
      })
      .check();
    await page.getByRole("button", { name: "Check page", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "Choose one recipe", exact: true }),
    ).toBeVisible();
    await expect(page.locator(".personal-editor")).toHaveCount(0);
    await page
      .getByRole("button", { name: "Second sample recipe", exact: true })
      .click();
    await expect(page.locator(".personal-editor")).toBeVisible();
    expect(
      (await page.getByLabel("Title", { exact: true }).inputValue()) ===
        "Second sample recipe",
    ).toBe(true);
    expect(
      counts().selections === 1 && counts().ai === 0 && counts().saves === 0,
    ).toBe(true);
  });
});

test("Free accounts cannot start imports", async ({ page }) => {
  await privateAction(async () => {
    await personalFixture(page, "importfree", "free");
    let requests = 0;
    await page.route("**/api/v1/recipe-imports/**", async (route) => {
      requests++;
      return route.abort();
    });
    await openPersonalLibrary(page);
    await expect(
      page.getByRole("button", { name: "Import recipe", exact: true }),
    ).toBeDisabled();
    expect(requests).toBe(0);
  });
});

test("real private worker hands a structured draft to Save and survives a lost response", async ({
  page,
}) => {
  await privateAction(async () => {
    await personalFixture(page, "importworkerreal");
    const availability = await page.request.get(
      "/api/v1/recipe-imports/availability",
    );
    expect(availability.status()).toBe(200);
    const status = await availability.json();
    expect(status.available && status.fixture && status.aiAvailable).toBe(true);
    await openPersonalLibrary(page);
    let reviews = 0,
      saves = 0,
      sourceImages = 0;
    page.on("request", (request) => {
      const path = new URL(request.url()).pathname;
      if (path.endsWith("/review")) reviews++;
      if (request.url().includes("source-images.example.test")) sourceImages++;
    });
    await page.route("**/api/v1/personal-recipes", async (route) => {
      if (route.request().method() !== "POST") return route.continue();
      saves++;
      if (saves === 1) {
        const response = await route.fetch();
        if (!response.ok()) throw new Error("Synthetic committed Save failed.");
        return route.abort("failed");
      }
      return route.continue();
    });
    await page
      .getByRole("button", { name: "Import recipe", exact: true })
      .click();
    await page
      .getByLabel("Recipe URL", { exact: true })
      .fill("https://recipes.example.test/carrot-soup");
    await page
      .getByRole("checkbox", {
        name: "I have permission to import this recipe for my personal use.",
        exact: true,
      })
      .check();
    await page.getByRole("button", { name: "Check page", exact: true }).click();
    await expect(page.locator(".personal-editor")).toBeVisible();
    const checks = page
      .getByRole("group", { name: "Review imported recipe", exact: true })
      .getByRole("checkbox");
    for (const check of await checks.all()) await check.check();
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(
      page.getByRole("button", { name: "Retry save", exact: true }),
    ).toBeVisible();
    await page.getByRole("button", { name: "Retry save", exact: true }).click();
    await expect(page.locator(".personal-detail")).toBeVisible();
    expect(reviews === 1 && saves === 2 && sourceImages === 0).toBe(true);
    expect(
      (await page
        .getByRole("link", { name: "Original recipe", exact: true })
        .getAttribute("href")) === "https://recipes.example.test/carrot-soup",
    ).toBe(true);
  });
});

test("real private worker handles explicit fallback, recipe pages and finite quota without saving", async ({
  page,
}) => {
  await privateAction(async () => {
    await personalFixture(page, "importworkerai");
    const status = await (
      await page.request.get("/api/v1/recipe-imports/availability")
    ).json();
    expect(
      status.available &&
        status.fixture &&
        status.aiAvailable &&
        status.photosAvailable,
    ).toBe(true);
    const enter = async () => {
      await page.goto("/app/?view=profile");
      await openPersonalLibrary(page);
      await page
        .getByRole("button", { name: "Import recipe", exact: true })
        .click();
      await page
        .getByRole("checkbox", {
          name: "I have permission to import this recipe for my personal use.",
          exact: true,
        })
        .check();
    };
    let aiCalls = 0,
      photoCalls = 0,
      saves = 0;
    page.on("request", (request) => {
      const path = new URL(request.url()).pathname;
      if (path.endsWith("/read-with-ai")) aiCalls++;
      if (path.endsWith("/recipe-imports/photos")) photoCalls++;
      if (path.endsWith("/personal-recipes") && request.method() === "POST")
        saves++;
    });
    await enter();
    await page
      .getByLabel("Recipe URL", { exact: true })
      .fill("https://recipes.example.test/plain");
    await page.getByRole("button", { name: "Check page", exact: true }).click();
    const ai = page.getByRole("button", { name: "Read with AI", exact: true });
    await expect(ai).toBeDisabled();
    await page
      .getByRole("checkbox", {
        name: "I agree to send the recipe text or pages to Gemini for reading.",
        exact: true,
      })
      .check();
    await ai.click();
    await expect(page.locator(".personal-editor")).toBeVisible();
    expect(aiCalls === 1 && saves === 0).toBe(true);
    await enter();
    await page
      .getByRole("button", { name: "Recipe pages", exact: true })
      .click();
    await page
      .getByLabel("Recipe-page photos", { exact: true })
      .setInputFiles({
        name: "private-page.png",
        mimeType: "image/png",
        buffer: personalPhoto,
      });
    await page
      .getByRole("checkbox", {
        name: "I agree to send the recipe text or pages to Gemini for reading.",
        exact: true,
      })
      .check();
    await page
      .getByRole("button", { name: "Read recipe pages", exact: true })
      .click();
    await expect(page.locator(".personal-editor")).toBeVisible();
    await expect(
      page.getByRole("img", { name: "Your recipe photo", exact: true }),
    ).toHaveCount(0);
    expect(photoCalls === 1 && saves === 0).toBe(true);
    // This third deliberate attempt must stop at the fixture's two-call fleet limit.
    await enter();
    await page
      .getByLabel("Recipe URL", { exact: true })
      .fill("https://recipes.example.test/plain");
    await page.getByRole("button", { name: "Check page", exact: true }).click();
    await page
      .getByRole("checkbox", {
        name: "I agree to send the recipe text or pages to Gemini for reading.",
        exact: true,
      })
      .check();
    await page
      .getByRole("button", { name: "Read with AI", exact: true })
      .click();
    await expect(
      page.getByRole("heading", {
        name: "This recipe could not be imported",
        exact: true,
      }),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "Enter manually", exact: true })
      .click();
    await expect(page.locator(".personal-editor")).toBeVisible();
    expect(aiCalls === 2 && photoCalls === 1 && saves === 0).toBe(true);
  });
});
