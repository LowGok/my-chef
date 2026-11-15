import { expect, test } from "@playwright/test";
import { fakeNative } from "./support/native";
import { personalGrant } from "./support/personal-recipes";
import { registerFixture } from "./support/registration";

test("Profile explains the fixed nudge and separates Premium advance settings", async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 720 });
  await registerFixture(page, {
    firstName: "Push", lastName: "Fixture", username: "pushfixture",
    email: "pushfixture@example.test", password: "Garden private fixture 2026!",
  });
  await page.goto("/app/?view=profile");
  await page.getByText("Planning reminders", { exact: true }).click();
  const group = page.locator(".planning-reminders");
  await expect(group.getByText(/Monday at 17:00/)).toBeVisible();
  await expect(group.getByLabel("Reminder for next week")).toHaveCount(0);
  await group.getByLabel("Time zone").fill("Europe/Brussels");
  await group.getByLabel("Monday reminder").check();
  await group.getByRole("button", { name: "Save reminder preferences" }).click();
  await expect(group.getByRole("status")).toHaveText("Reminder preferences saved.");
  await expect(group.getByText(/separately on each device/)).toBeVisible();
  await personalGrant(page, "premium");
  await page.reload();
  await page.getByText("Planning reminders", { exact: true }).click();
  await expect(group.getByLabel("Reminder for next week")).toBeVisible();
  await group.getByLabel("Reminder for next week").check();
  await group.getByRole("combobox", { name: "Day", exact: true }).selectOption("0");
  await group.getByLabel("Time", { exact: true }).fill("17:30");
  await group.getByRole("button", { name: "Save reminder preferences" }).click();
  await expect(group.getByRole("status")).toHaveText("Reminder preferences saved.");
  const textAndControls = group.locator("summary, p, label, input:not([type=checkbox]), select, button");
  const fontSizes = () => textAndControls.evaluateAll((nodes) =>
    nodes.map((node) => Number.parseFloat(getComputedStyle(node).fontSize)),
  );
  for (const width of [320, 430]) {
    await page.setViewportSize({ width, height: 720 });
    for (const native of [false, true]) {
      await page.evaluate((native) => {
        document.documentElement.style.fontSize = "100%";
        if (native) document.documentElement.dataset.native = "true";
        else delete document.documentElement.dataset.native;
      }, native);
      const normal = await fontSizes();
      await page.evaluate(() => { document.documentElement.style.fontSize = "200%"; });
      expect(await fontSizes(), `text at ${width}px, native=${native}`).toEqual(normal.map((size) => size * 2));
      expect(await group.evaluate((node) => node.scrollWidth <= node.clientWidth)).toBe(true);
      await expect(group.getByRole("button", { name: "Save reminder preferences" })).toBeEnabled();
    }
  }
  const day = group.getByRole("combobox", { name: "Day", exact: true });
  await day.focus();
  await expect(day).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(group.getByLabel("Time", { exact: true })).toBeFocused();
  await personalGrant(page, "free");
  await page.reload();
  await page.getByText("Planning reminders", { exact: true }).click();
  await expect(group.getByLabel("Reminder for next week")).toHaveCount(0);
  await expect(group.getByLabel("Monday reminder")).toBeChecked();
});


test("iOS native text preference scales reminder controls without changing the default", async ({
  page,
  context,
  playwright,
  baseURL,
}) => {
  const account = {
    firstName: "Text", lastName: "Fixture", username: "ios-reminder-text",
    email: "ios-reminder-text@example.test", password: "Private fixture reminder 2026!",
  };
  const server = await playwright.request.newContext({ baseURL });
  try {
    await page.setViewportSize({ width: 320, height: 720 });
    await registerFixture(page, account);
    await context.clearCookies();
    const os = await fakeNative(context, server, { platform: "ios" });
    await page.goto("/app/");
    await page.getByLabel("Email", { exact: true }).fill(account.email);
    await page.getByLabel("Password", { exact: true }).fill(account.password);
    await page.getByRole("button", { name: "Sign in to MyChef", exact: true }).click();
    await page.getByRole("button", { name: "Profile", exact: true }).click();
    await page.getByText("Planning reminders", { exact: true }).click();
    const panel = page.locator(".planning-reminders");
    const save = panel.getByRole("button", { name: "Save reminder preferences" });
    const fontSize = () => save.evaluate((node) => parseFloat(getComputedStyle(node).fontSize));
    const normal = await fontSize();
    for (const width of [320, 430]) {
      await page.setViewportSize({ width, height: 720 });
      await os.runtime({ textScale: 1.75 });
      await expect.poll(fontSize).toBe(normal * 1.75);
      await expect(save).toBeEnabled();
      expect(await panel.evaluate((node) => node.scrollWidth <= node.clientWidth + 1)).toBe(true);
    }
    await os.runtime({ textScale: 1 });
    await expect.poll(fontSize).toBe(normal);
  } finally {
    await server.dispose();
  }
});
