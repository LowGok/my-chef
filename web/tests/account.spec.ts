import { expect, test } from "@playwright/test";
import { registerFixture } from "./support/registration";

test("account preferences survive logout, login and browser refresh", async ({
  page,
}) => {
  await registerFixture(page, {
    firstName: "Lina",
    lastName: "Cook",
    username: "lina-e2e",
    email: "lina-e2e@example.test",
    password: "ChefTest2026!",
  });
  await page.getByRole("button", { name: "Profile", exact: true }).click();
  await expect(page.getByLabel("Diet", { exact: true })).toBeVisible();
  await page
    .getByLabel("Cooking effort", { exact: true })
    .selectOption("Moderate Effort (Weekend)");
  await page.getByLabel("Diet", { exact: true }).fill("Vegetarian");
  await page.getByRole("button", { name: "Milk", exact: true }).click();
  await page.getByRole("button", { name: "Peanuts", exact: true }).click();
  await page.getByLabel("Other allergens", { exact: true }).fill("Kiwi");
  await page.getByRole("button", { name: "Save preferences" }).click();
  await expect(
    page.getByText("Preferences saved.", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await page.getByLabel("Email", { exact: true }).fill("lina-e2e@example.test");
  await page.getByLabel("Password", { exact: true }).fill("ChefTest2026!");
  await page
    .getByRole("button", { name: "Sign in to MyChef", exact: true })
    .click();
  await page.getByRole("button", { name: "Profile", exact: true }).click();
  await page.reload();
  await expect(page.getByLabel("Cooking effort", { exact: true })).toHaveValue(
    "Moderate Effort (Weekend)",
  );
  await expect(page.getByLabel("Diet", { exact: true })).toHaveValue(
    "Vegetarian",
  );
  await expect(
    page.getByRole("button", { name: "Milk", exact: true }),
  ).toHaveAttribute("aria-pressed", "true");
  await expect(
    page.getByRole("button", { name: "Peanuts", exact: true }),
  ).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByLabel("Other allergens", { exact: true })).toHaveValue(
    "Kiwi",
  );
  await page.getByRole("button", { name: "Milk", exact: true }).click();
  await page.getByRole("button", { name: "Save preferences" }).click();
  await expect(
    page.getByText("Preferences saved.", { exact: true }),
  ).toBeVisible();
  const session = await (await page.request.get("/api/v1/session")).json();
  expect(session.user.preferences.allergens).toEqual(["Peanuts", "Kiwi"]);
  expect(session.user.preferences.effort).toBe("Moderate Effort (Weekend)");
  await page.setViewportSize({ width: 390, height: 812 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true);
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({
    path: "../test-results/branding/allergens-mobile.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({
    path: "../test-results/rewrite/profile.png",
    fullPage: true,
  });
  await page.getByText("Preferences", { exact: true }).click();
  await page.screenshot({
    path: "../test-results/branding/profile-sections.png",
    fullPage: true,
  });
  await page.getByText("Help & support", { exact: true }).click();
  await expect(
    page.getByRole("link", { name: "Contact support" }),
  ).toHaveAttribute("href", "mailto:admin@example.invalid");
  await page.getByText("About", { exact: true }).click();
  await expect(
    page.getByText(
      "MyChef organizes recipes and shopping lists.",
      { exact: true },
    ),
  ).toBeVisible();

  const supportBounds = await page.locator(".profile-support").boundingBox();
  const signOutBounds = await page
    .getByRole("button", { name: "Sign out", exact: true })
    .boundingBox();
  expect(signOutBounds!.y).toBeGreaterThan(
    supportBounds!.y + supportBounds!.height,
  );
  expect(
    Math.abs(
      signOutBounds!.x +
        signOutBounds!.width / 2 -
        (supportBounds!.x + supportBounds!.width / 2),
    ),
  ).toBeLessThan(1);
  await page.setViewportSize({ width: 390, height: 812 });
  await page.evaluate(() => window.scrollTo(0, 0));
  const feedback = page.getByRole("button", {
    name: "Provide your feedback",
    exact: true,
  });
  const feedbackBounds = await feedback.boundingBox();
  const profileBounds = await page
    .locator(".profile-panel")
    .first()
    .boundingBox();
  const headingBounds = await page
    .getByRole("heading", { name: "Your profile.", exact: true })
    .boundingBox();
  expect(Math.abs(feedbackBounds!.width - profileBounds!.width)).toBeLessThan(
    1,
  );
  expect(feedbackBounds!.y + feedbackBounds!.height).toBeLessThan(
    headingBounds!.y,
  );
  await page.screenshot({
    path: "../test-results/branding/feedback-button-mobile.png",
  });
  await feedback.click();
  await expect(page).toHaveURL(/view=feedback/);
  await expect(
    page.getByRole("heading", { name: "Your feedback.", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Profile", exact: true }),
  ).toHaveAttribute("aria-current", "page");
  await page.reload();
  await expect(
    page.getByLabel("Contact reason", { exact: true }),
  ).toBeEnabled();
  await expect(page.getByLabel("Message", { exact: true })).toBeEditable();
  await page
    .getByRole("button", { name: "Back to profile", exact: true })
    .click();
  await expect(feedback).toBeVisible();
  await expect(page.getByLabel("Diet", { exact: true })).toHaveValue(
    "Vegetarian",
  );
});
