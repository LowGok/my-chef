import {
  expect,
  test,
  type Page,
  type BrowserContext,
  type APIRequestContext,
} from "@playwright/test";
import { fakeNative } from "./support/native";
import {
  personalFixture,
  openPersonalLibrary,
  fillPersonalDraft,
} from "./support/personal-recipes";

test.use({ serviceWorkers: "block" });
test.setTimeout(150_000);
const password = "Garden personal recipe fixture 2026!";
async function signedIn(
  page: Page,
  context: BrowserContext,
  server: APIRequestContext,
  name: string,
) {
  await personalFixture(page, name);
  await context.clearCookies();
  const os = await fakeNative(context, server);
  await page.goto("/app/?view=profile");
  await page.getByLabel("Email", { exact: true }).fill(`${name}@example.test`);
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page
    .getByRole("button", { name: "Sign in to MyChef", exact: true })
    .click();
  await page.getByRole("button", { name: "Profile", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Your profile.", exact: true }),
  ).toBeVisible();
  return os;
}

test("native photo selection stays in memory and an uncertain save retries the same private picture", async ({
  page,
  context,
  playwright,
}) => {
  const server = await playwright.request.newContext({
    baseURL: "http://127.0.0.1:18137",
  });
  try {
    const os = await signedIn(page, context, server, "nativemediaphoto");
    await openPersonalLibrary(page);
    await page
      .getByRole("button", { name: "Create a recipe", exact: true })
      .click();
    await fillPersonalDraft(page);
    await page.getByLabel("Choose recipe photo", { exact: true }).click();
    await expect(page.locator(".personal-photo-preview img")).toBeVisible();
    expect(os.pickerCalls()).toBe(1);
    expect(os.recipeSaves()).toBe(0);
    os.failNextSave();
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(
      page.getByRole("button", { name: "Retry save", exact: true }),
    ).toBeVisible();
    await expect(page.locator(".personal-photo-preview img")).toBeVisible();
    await page.getByRole("button", { name: "Retry save", exact: true }).click();
    await expect(page.locator(".personal-detail img")).toHaveAttribute(
      "src",
      /^blob:/,
    );
    await expect.poll(() => os.photoReads()).toBeGreaterThan(0);
    expect(os.pickerCalls()).toBe(1);
    expect(os.recipeSaves()).toBe(2);
    const recipes = await server.post(
      "/api/native/v1/personal-recipes/search",
      { headers: { Authorization: os.access() }, data: {} },
    );
    expect((await recipes.json()).recipes.length).toBe(1);
  } finally {
    await server.dispose();
  }
});

test("native account export waits for system Save or Cancel and support uses the external handler", async ({
  page,
  context,
  playwright,
}) => {
  const server = await playwright.request.newContext({
    baseURL: "http://127.0.0.1:18137",
  });
  try {
    const os = await signedIn(page, context, server, "nativemediaexport");
    let browserDownloads = 0;
    page.on("download", () => {
      browserDownloads++;
    });
    await page.getByText("Account details", { exact: true }).click();
    async function download() {
      await page
        .getByRole("button", { name: "Download my data", exact: true })
        .click();
      const dialog = page.getByRole("dialog", {
        name: "Download my data",
        exact: true,
      });
      await dialog
        .getByLabel("Current password", { exact: true })
        .fill(password);
      await dialog
        .getByRole("button", { name: "Download ZIP", exact: true })
        .click();
    }
    await download();
    await expect(
      page.getByText("Your download is ready. Keep the ZIP private.", {
        exact: true,
      }),
    ).toBeVisible();
    expect(os.exports().length).toBe(1);
    expect(
      /^mychef-data-[a-f0-9]{32}\.zip$/.test(os.exports()[0].filename),
    ).toBe(true);
    expect(os.exports()[0].bytes.equals(os.exportResponse()!)).toBe(true);
    expect(browserDownloads).toBe(0);
    os.saveAllowed(false);
    await download();
    await expect(
      page.getByText("Download cancelled.", { exact: true }),
    ).toBeVisible();
    expect(os.exports().length).toBe(1);
    await page.locator("summary").filter({ hasText: "Help & support" }).click();
    await page
      .getByRole("link", { name: "Contact support", exact: true })
      .click();
    await expect
      .poll(() => os.links())
      .toEqual(["mailto:admin@example.invalid"]);
    expect(context.pages().length).toBe(1);
  } finally {
    await server.dispose();
  }
});

test("native recipe-page picking keeps AI consent explicit and never supplies a dish picture", async ({
  page,
  context,
  playwright,
}) => {
  const server = await playwright.request.newContext({
    baseURL: "http://127.0.0.1:18137",
  });
  try {
    const os = await signedIn(page, context, server, "nativemediapages");
    await openPersonalLibrary(page);
    await page
      .getByRole("button", { name: "Import recipe", exact: true })
      .click();
    await page
      .getByRole("button", { name: "Recipe pages", exact: true })
      .click();
    await page.getByLabel("Recipe-page photos", { exact: true }).click();
    await expect.poll(() => os.pickerCalls()).toBe(1);
    expect(os.pageImports()).toBe(0);
    const consent = page.getByRole("checkbox", {
      name: /I agree to send the recipe text or pages to Gemini/,
    });
    await expect(consent).not.toBeChecked();
    await page
      .getByRole("checkbox", { name: /I have permission to import/ })
      .check();
    await consent.check();
    await page
      .getByRole("button", { name: "Read recipe pages", exact: true })
      .click();
    await expect(page.locator(".personal-editor")).toBeVisible();
    expect(os.pageImports()).toBe(1);
    await expect(page.locator(".personal-photo-preview img")).toHaveCount(0);
    expect(os.recipeSaves()).toBe(0);
  } finally {
    await server.dispose();
  }
});
