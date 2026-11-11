import { expect, test } from "@playwright/test";
import { fakeNative } from "./support/native";
import {
  shoppingAccount,
  shoppingEmail,
  shoppingPassword,
} from "./support/shopping";
test.use({
  serviceWorkers: "block",
  hasTouch: true,
  isMobile: true,
  viewport: { width: 320, height: 640 },
});
test("native preferences save through the native API and show confirmation", async ({
  page,
  context,
  playwright,
}) => {
  await shoppingAccount(page);
  await context.clearCookies();
  const server = await playwright.request.newContext({
    baseURL: "http://127.0.0.1:18137",
  });
  try {
    const os = await fakeNative(context, server);
    await page.goto("/app/");
    await page.getByLabel("Email", { exact: true }).fill(shoppingEmail);
    await page.getByLabel("Password", { exact: true }).fill(shoppingPassword);
    await page
      .getByRole("button", { name: "Sign in to MyChef", exact: true })
      .click();
    await page.getByRole("button", { name: "Profile", exact: true }).click();
    await page.getByLabel("Diet", { exact: true }).fill("Vegetarian");
    const save = page.getByRole("button", {
      name: "Save preferences",
      exact: true,
    });
    await page.getByLabel("Diet", { exact: true }).blur();
    await save.tap();
    await expect(
      page.getByText("Preferences saved.", { exact: true }),
    ).toBeVisible();
    const response = await server.get("/api/native/v1/profile", {
      headers: { Authorization: os.access() },
    });
    expect(response.status()).toBe(200);
    expect((await response.json()).preferences.diet).toEqual(["Vegetarian"]);
  } finally {
    await server.dispose();
  }
});

test("native links keep proofs out of navigation/storage and reject foreign origins", async ({
  page,
  context,
  playwright,
}) => {
  const server = await playwright.request.newContext({
    baseURL: "http://127.0.0.1:18137",
  });
  try {
    const os = await fakeNative(context, server);
    await page.goto("/app/");
    await expect(
      page.getByRole("heading", { name: "Welcome back.", exact: true }),
    ).toBeVisible();
    const proof = "NATIVE_ACCOUNT_PROOF_CANARY_".padEnd(43, "a");
    await os.runtime({
      link: `https://fixture-node.tailtest.ts.net:8977/app/reset-password#${proof}`,
    });
    await expect(
      page.getByRole("heading", { name: "Choose a new password", exact: true }),
    ).toBeVisible();
    expect(page.url()).toMatch(/\/app\/reset-password$/);
    const leaked = await page.evaluate(
      (canary) =>
        JSON.stringify([
          location.href,
          history.state,
          ...Object.keys(localStorage).map((k) => localStorage.getItem(k)),
          ...Object.keys(sessionStorage).map((k) => sessionStorage.getItem(k)),
        ]).includes(canary),
      proof,
    );
    expect(leaked).toBe(false);
    for (const field of ["New password", "Confirm new password"])
      expect(
        await page
          .getByLabel(field, { exact: true })
          .evaluate((el) => parseFloat(getComputedStyle(el).fontSize)),
      ).toBeGreaterThanOrEqual(16);
    await os.runtime({ link: `https://foreign.test/app/signup#${proof}` });
    expect(page.url()).toMatch(/\/app\/reset-password$/);
    await expect(
      page.getByRole("heading", { name: "Choose a new password", exact: true }),
    ).toBeVisible();
  } finally {
    await server.dispose();
  }
});
test("native back respects keyboard and foreground retry reconnects a cached account", async ({
  page,
  context,
  playwright,
}) => {
  await shoppingAccount(page);
  await context.clearCookies();
  const server = await playwright.request.newContext({
    baseURL: "http://127.0.0.1:18137",
  });
  try {
    const os = await fakeNative(context, server);
    await page.goto("/app/");
    await page.getByLabel("Email", { exact: true }).fill(shoppingEmail);
    await page.getByLabel("Password", { exact: true }).fill(shoppingPassword);
    await page
      .getByRole("button", { name: "Sign in to MyChef", exact: true })
      .click();
    await page
      .getByRole("button", { name: "Shopping list", exact: true })
      .click();
    await expect(page.getByText("Saved", { exact: true })).toBeVisible();
    os.network(false);
    await page.reload();
    await expect(
      page.getByText(/Offline · Your saved shopping list/),
    ).toBeVisible();
    os.network(true); // Wi-Fi stayed online; a foreground retry must still recover tailnet access.
    await page.evaluate(() =>
      window.dispatchEvent(new Event("mychef-native-resume")),
    );
    await expect(page.getByText("Saved", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Profile", exact: true }).click();
    await page.getByText("Account details", { exact: true }).click();
    const field = page.getByLabel("First name", { exact: true });
    await field.focus();
    await os.runtime({ back: true });
    await expect(field).not.toBeFocused();
    await os.runtime({ back: true });
    await expect(page.locator(".shopping-heading")).toBeVisible();
  } finally {
    await server.dispose();
  }
});

test("native back asks before discarding a recipe draft and preserves it when cancelled", async ({
  page,
  context,
  playwright,
}) => {
  const { personalFixture, openPersonalLibrary } =
    await import("./support/personal-recipes");
  await personalFixture(page, "nativeruntimedraft");
  await context.clearCookies();
  const server = await playwright.request.newContext({
    baseURL: "http://127.0.0.1:18137",
  });
  try {
    const os = await fakeNative(context, server);
    await page.goto("/app/");
    await page
      .getByLabel("Email", { exact: true })
      .fill("nativeruntimedraft@example.test");
    await page
      .getByLabel("Password", { exact: true })
      .fill("Garden personal recipe fixture 2026!");
    await page
      .getByRole("button", { name: "Sign in to MyChef", exact: true })
      .click();
    await page.getByRole("button", { name: "Profile", exact: true }).click();
    await openPersonalLibrary(page);
    await page
      .getByRole("button", { name: "Create a recipe", exact: true })
      .click();
    const title = page.getByLabel("Title", { exact: true });
    await title.fill("A private unsaved dish");
    await title.blur();
    const route = page.url();
    const prompt = page.getByRole("dialog", {
      name: "Discard unsaved recipe changes?",
      exact: true,
    });
    await os.runtime({ back: true });
    await expect(prompt).toBeVisible();
    await expect(page).toHaveURL(route);
    await prompt.getByRole("button", { name: "Keep editing" }).click();
    await expect(prompt).not.toBeVisible();
    await expect(title).toHaveValue("A private unsaved dish");
    await os.runtime({ back: true });
    await expect(prompt).toBeVisible();
    await prompt.getByRole("button", { name: "Discard changes" }).click();
    await expect(
      page.getByRole("heading", { name: "Your profile.", exact: true }),
    ).toBeVisible();
  } finally {
    await server.dispose();
  }
});
