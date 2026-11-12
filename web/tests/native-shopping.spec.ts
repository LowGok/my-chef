import { expect, test } from "@playwright/test";
import {
  shoppingAccount,
  shoppingEmail,
  shoppingPassword,
  openExtras,
} from "./support/shopping";
import { fakeNative } from "./support/native";

test.use({ serviceWorkers: "block" });
test.setTimeout(150_000);
// Use a different current week so the fixture cannot inherit its target
// shopping dates from sign-in's default navigation.
test.beforeEach(async ({ page }) => {
  await page.clock.setFixedTime(new Date("2026-11-16T12:00:00Z"));
});

test("native ordinary shopping relaunches offline and synchronizes both weeks from Profile", async ({
  page,
  context,
  playwright,
}) => {
  // Android's API 33 image ships a WebView without this newer property.
  await context.addInitScript(() => {
    Reflect.deleteProperty(URLSearchParams.prototype, "size");
  });
  await shoppingAccount(page);
  await context.clearCookies();
  const server = await playwright.request.newContext({
    baseURL: "http://127.0.0.1:18137",
  });
  try {
    const os = await fakeNative(context, server);
    await page.goto("/app/?view=shopping&week=2026-11-02");
    await page.getByLabel("Email", { exact: true }).fill(shoppingEmail);
    await page.getByLabel("Password", { exact: true }).fill(shoppingPassword);
    await page
      .getByRole("button", { name: "Sign in to MyChef", exact: true })
      .click();
    await page
      .getByRole("button", { name: "Shopping list", exact: true })
      .click();
    await expect(page).toHaveURL(/\/app\/\?view=shopping&week=\d{4}-\d{2}-\d{2}$/);
    await page.goto("/app/?view=shopping&week=2026-11-02");
    await expect(page.getByText("Saved", { exact: true })).toBeVisible();
    await page
      .getByRole("button", { name: "Next shopping week", exact: true })
      .click();
    await expect(page).toHaveURL(/\/app\/\?view=shopping&week=2026-11-09$/);
    await expect(page.getByText("Saved", { exact: true })).toBeVisible();
    await page
      .getByRole("button", { name: "Previous shopping week", exact: true })
      .click();
    await expect(page.getByText("Saved", { exact: true })).toBeVisible();
    os.network(false);
    await context.setOffline(true);
    await openExtras(page);
    await page
      .getByLabel("Extra item", { exact: true })
      .fill("Native offline coffee");
    await page.getByRole("button", { name: "Add extra", exact: true }).click();
    await expect(page.getByText(/Pending sync/).first()).toBeVisible();
    await page
      .getByRole("button", { name: "Next shopping week", exact: true })
      .click();
    await openExtras(page);
    await page
      .getByLabel("Extra item", { exact: true })
      .fill("Native offline tea");
    await page.getByRole("button", { name: "Add extra", exact: true }).click();
    await expect
      .poll(() => os.snapshot()?.payload.includes("Native offline tea"))
      .toBe(true);
    await page.reload();
    await expect(page.locator(".shopping-heading")).toBeVisible();
    // Relaunch can restore either cached week. Verify both durable edits
    // through week navigation rather than assuming which week opens first.
    const restoredWeek = new URL(page.url()).searchParams.get("week");
    expect(["2026-11-02", "2026-11-09"]).toContain(restoredWeek);
    if (restoredWeek === "2026-11-09") {
      await expect(
        page.getByText("Native offline tea", { exact: true }),
      ).toBeVisible();
      await page
        .getByRole("button", { name: "Previous shopping week", exact: true })
        .click();
    }
    await expect(
      page.getByText("Native offline coffee", { exact: true }),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "Next shopping week", exact: true })
      .click();
    await expect(
      page.getByText("Native offline tea", { exact: true }),
    ).toBeVisible();
    await page.getByRole("button", { name: "Profile", exact: true }).click();
    os.network(true);
    await context.setOffline(false);
    for (const [week, name] of [
      ["2026-11-02", "Native offline coffee"],
      ["2026-11-09", "Native offline tea"],
    ]) {
      await expect
        .poll(async () => {
          const response = await server.get(`/api/native/v1/shopping/${week}`, {
            headers: { Authorization: os.access() },
          });
          if (!response.ok()) return 0;
          return (await response.json()).items.filter(
            (item: { name: string }) => item.name === name,
          ).length;
        })
        .toBe(1);
    }
    await expect(
      page.getByRole("heading", { name: "Your profile.", exact: true }),
    ).toBeVisible();
    await page.getByRole("button", { name: "Sign out", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "Welcome back.", exact: true }),
    ).toBeVisible();
    await expect.poll(() => os.snapshot()).toBeNull();
    await expect.poll(() => os.refresh()).toBeNull();
  } finally {
    await server.dispose();
  }
});

test("native storage failure keeps the exact draft for retry and never sends an unpersisted edit", async ({
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
    await page.goto("/app/?view=shopping&week=2026-11-02");
    await page.getByLabel("Email", { exact: true }).fill(shoppingEmail);
    await page.getByLabel("Password", { exact: true }).fill(shoppingPassword);
    await page
      .getByRole("button", { name: "Sign in to MyChef", exact: true })
      .click();
    await page
      .getByRole("button", { name: "Shopping list", exact: true })
      .click();
    await page.goto("/app/?view=shopping&week=2026-11-02");
    await expect(page.getByText("Saved", { exact: true })).toBeVisible();
    os.failStorage(true);
    await openExtras(page);
    await page
      .getByLabel("Extra item", { exact: true })
      .fill("Native storage retry");
    await page.getByRole("button", { name: "Add extra", exact: true }).click();
    await expect(
      page.getByText(/Shopping could not be saved securely/).first(),
    ).toBeVisible();
    const before = await server.get("/api/native/v1/shopping/2026-11-02", {
      headers: { Authorization: os.access() },
    });
    expect(
      (await before.json()).items.some(
        (item: { name: string }) => item.name === "Native storage retry",
      ),
    ).toBe(false);
    os.failStorage(false);
    await page
      .getByRole("button", { name: "Retry sync", exact: true })
      .first()
      .click();
    await expect(page.getByText("Saved", { exact: true })).toBeVisible();
    const after = await server.get("/api/native/v1/shopping/2026-11-02", {
      headers: { Authorization: os.access() },
    });
    expect(
      (await after.json()).items.filter(
        (item: { name: string }) => item.name === "Native storage retry",
      ),
    ).toHaveLength(1);
  } finally {
    await server.dispose();
  }
});
