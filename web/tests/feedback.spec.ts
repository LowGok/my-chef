import { expect, test, type Page } from "@playwright/test";
import { registerFixture } from "./support/registration";

const feedbackPath = "/api/v1/feedback";
const draft = "A synthetic 🫛 idea for the weekly meal list.";
const password = "Unrelated fixture sentence 24.";
async function account(page: Page, name: string) {
  return registerFixture(page, {
    email: `${name}@example.test`,
    username: name,
    firstName: "Test",
    lastName: "Cook",
    password,
  });
}
const send = (page: Page) =>
  page.getByRole("button", { name: "Send feedback", exact: true });

test("feedback preserves its receipt and guards uncertain submissions", async ({
  page,
  browser,
}) => {
  const other = await browser.newContext();
  try {
    await account(await other.newPage(), "feedbackother");
  } finally {
    await other.close();
  }
  await account(page, "feedbackowner");
  await page.goto("/app/?view=profile");
  await page.getByRole("button", { name: "Provide your feedback" }).click();
  await expect(
    page.getByRole("heading", { name: "Your feedback." }),
  ).toBeVisible();
  const topic = page.getByLabel("Contact reason");
  const field = page.getByLabel("Message", { exact: true });
  await expect(topic).toBeEnabled();
  await expect(page.getByText(/verified account email/)).toBeVisible();
  const feedback = page.getByRole("region", { name: "Feedback", exact: true });
  await expect(
    feedback.locator('input[type="file"], input[type="checkbox"]'),
  ).toHaveCount(0);
  await expect(
    feedback.locator('a[href^="mailto:"]'),
  ).toHaveCount(0);
  await expect(topic.locator("option")).toHaveText([
    "Choose a reason",
    "Technical issue",
    "Recipe issue",
    "My subscription",
    "Other",
  ]);

  await topic.selectOption("other");
  await field.fill("🫛".repeat(19));
  await expect(page.getByText("3981 characters remaining")).toBeVisible();
  await send(page).click();
  await expect(field).toBeFocused();
  await expect(field).toHaveAttribute("aria-invalid", "true");
  await expect(field).toHaveAccessibleDescription(
    /Enter a message of 20 to 4,000 characters/,
  );
  await expect(field).toHaveValue("🫛".repeat(19));
  await field.fill("🫛".repeat(4001));
  await send(page).click();
  await expect(field).toHaveValue("🫛".repeat(4001));
  await expect(page.getByText("1 character over the limit")).toBeVisible();
  await field.fill(draft);

  let posts = 0;
  let requestKey = "";
  await page.route(`**${feedbackPath}`, async (route) => {
    if (route.request().method() !== "POST") return route.continue();
    const key = route.request().postDataJSON().requestKey as string;
    posts++;
    if (posts === 1) {
      requestKey = key;
      await route.abort();
      return;
    }
    expect(key === requestKey).toBe(true);
    const response = await route.fetch();
    expect(response.status()).toBe(200);
    await route.abort(); // Committed response lost a second time.
  });
  await send(page).click();
  await expect(
    page.getByRole("button", { name: "Retry exact submission" }),
  ).toBeVisible();
  await expect(field).toBeDisabled();
  await page.getByRole("button", { name: "Check receipt" }).click();
  await expect(page.getByText(/No receipt yet/)).toBeVisible();
  await page.getByRole("button", { name: "Retry exact submission" }).click();
  await expect(
    page.getByRole("button", { name: "Check receipt" }),
  ).toBeEnabled();
  expect(posts).toBe(2);
  const storage = await page.evaluate(() =>
    JSON.stringify({ ...localStorage, ...sessionStorage }),
  );
  expect(storage.includes(draft)).toBe(false);
  expect(storage.includes("feedbackowner@example.test")).toBe(false);

  await page.reload();
  const heading = page.getByRole("heading", {
    name: "Thanks for your feedback.",
  });
  await expect(heading).toBeFocused();
  const receipt = page.getByTestId("feedback-receipt");
  await expect(
    page
      .getByRole("region", { name: "Feedback", exact: true })
      .locator('a[href^="mailto:"]'),
  ).toHaveCount(0);
  const original = await receipt.innerText();
  await expect(receipt.locator("time")).toHaveAttribute("datetime", /^\d{4}-/);
  await page.goBack();
  await page.goForward();
  await expect(receipt).toHaveText(original, { useInnerText: true });

  await page.unroute(`**${feedbackPath}`);
  let release = () => {};
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  let committed = () => {};
  const commit = new Promise<void>((resolve) => {
    committed = resolve;
  });
  await page.route(`**${feedbackPath}`, async (route) => {
    const response = await route.fetch();
    expect(response.status()).toBe(200);
    committed();
    await held;
    await route.fulfill({ response });
  });
  await page.getByRole("button", { name: "Send another report" }).click();
  await topic.selectOption("technical_issue");
  await field.fill(draft);
  await send(page).click();
  await commit;
  await page.getByRole("button", { name: "Back to profile" }).click();
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Welcome back." }),
  ).toBeVisible();
  expect(
    await page.evaluate(() =>
      Object.keys(sessionStorage).some((key) =>
        key.startsWith("mychef.feedback."),
      ),
    ),
  ).toBe(false);
  await page
    .getByLabel("Email", { exact: true })
    .fill("feedbackother@example.test");
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByRole("button", { name: "Sign in to MyChef" }).click();
  await expect(
    page.getByRole("button", { name: "Profile", exact: true }),
  ).toBeVisible();
  release();
  await page.getByRole("button", { name: "Profile", exact: true }).click();
  await page.getByRole("button", { name: "Provide your feedback" }).click();
  await expect(field).toBeEnabled();
  await expect(field).toHaveValue("");
  await expect(receipt).toHaveCount(0);
});

test("feedback controls fit phone widths and larger text", async ({ page }) => {
  await account(page, "feedbacklayout");
  await page.goto("/app/?view=feedback");
  const topic = page.getByLabel("Contact reason");
  const field = page.getByLabel("Message", { exact: true });
  await expect(topic).toBeEnabled();
  await expect(page.locator('meta[name="viewport"]')).not.toHaveAttribute(
    "content",
    /user-scalable=no|maximum-scale/,
  );
  const widths = [320, 360, 390, 430];
  for (const width of widths) {
    await page.setViewportSize({ width, height: 780 });
    for (const fontSize of ["", "200%"]) {
      await page.evaluate((value) => {
        document.documentElement.style.fontSize = value;
      }, fontSize);
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBe(true);
      for (const control of [topic, field]) {
        const bounds = await control.boundingBox();
        expect(
          bounds && bounds.x >= 0 && bounds.x + bounds.width <= width,
        ).toBe(true);
        expect(
          await control.evaluate((el) =>
            parseFloat(getComputedStyle(el).fontSize),
          ),
        ).toBeGreaterThanOrEqual(18);
      }
    }
  }
  await topic.focus();
  await page.keyboard.press("Tab");
  await expect(field).toBeFocused();
  await field.fill(draft);
  await topic.selectOption("technical_issue");
  await send(page).click();
  await expect(
    page.getByRole("heading", { name: "Thanks for your feedback." }),
  ).toBeFocused();
  const receipt = page.getByTestId("feedback-receipt");
  for (const width of widths) {
    await page.setViewportSize({ width, height: 780 });
    for (const fontSize of ["", "200%"]) {
      await page.evaluate((value) => {
        document.documentElement.style.fontSize = value;
      }, fontSize);
      const bounds = await receipt.boundingBox();
      expect(bounds && bounds.x >= 0 && bounds.x + bounds.width <= width).toBe(
        true,
      );
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBe(true);
    }
  }
  await page.getByRole("button", { name: "Send another report" }).click();
  await expect(field).toHaveValue("");
});

test("recipe issues and private ratings stay separate", async ({ page }) => {
  const signed = await account(page, "feedbackrecipe");
  const headers = {
    Origin: new URL(page.url()).origin,
    "X-CSRF-Token": signed.csrfToken,
    "Idempotency-Key": crypto.randomUUID(),
  };
  const generation = await page.request.post("/api/v1/generations", {
    headers,
    data: {
      dates: ["2026-11-02"],
      adults: 2,
      children: 0,
      maxMinutes: 30,
      budget: "medium",
      region: "Belgium",
      creativity: 2,
    },
  });
  expect(generation.status()).toBe(202);
  const op = await generation.json();
  await expect
    .poll(
      async () =>
        (await (await page.request.get(`/api/v1/generations/${op.id}`)).json())
          .status,
    )
    .toBe("succeeded");
  const list = await (
    await page.request.get("/api/v1/shopping/2026-11-02")
  ).json();
  const id = list.meals[0].recipeId;
  await page.goto(`/app/?recipe=${id}&week=2026-11-02`);
  const rating = page.getByLabel("Rating", { exact: true });
  await rating.selectOption("5");
  await page.getByRole("button", { name: "Save rating" }).click();
  await expect(page.getByText(/Rating saved/)).toBeVisible();
  await page.reload();
  await expect(rating).toHaveValue("5");
  await expect(
    page.getByRole("button", { name: "Save rating" }),
  ).toBeDisabled();

  await page.getByRole("button", { name: "Report an issue" }).click();
  const topic = page.getByLabel("Contact reason");
  await expect(topic).toBeEnabled();
  await expect(topic).toHaveValue("recipe_issue");
  await expect(page.getByLabel(/Include this recipe/)).toBeChecked();
  await topic.selectOption("technical_issue");
  await expect(page.getByLabel(/Include this recipe/)).toHaveCount(0);
  await topic.selectOption("recipe_issue");
  const field = page.getByLabel("Message", { exact: true });
  await field.fill("🫛".repeat(20));
  const received = page.waitForRequest(
    (request) =>
      new URL(request.url()).pathname === feedbackPath &&
      request.method() === "POST",
  );
  await send(page).click();
  expect((await received).postDataJSON().recipeId === id).toBe(true);
  await expect(
    page.getByRole("heading", { name: "Thanks for your feedback." }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Back to recipe" }).click();
  await expect(rating).toHaveValue("5");

  await page.getByRole("button", { name: "Profile", exact: true }).click();
  await page.getByRole("button", { name: "Provide your feedback" }).click();
  for (const [index, value] of [
    "recipe_issue",
    "subscription",
    "technical_issue",
    "other",
  ].entries()) {
    await page.getByRole("button", { name: "Send another report" }).click();
    await topic.selectOption(value);
    await expect(page.getByLabel(/Include this recipe/)).toHaveCount(0);
    await field.fill(index === 0 ? "🫛".repeat(4000) : draft);
    await send(page).click();
    await expect(
      page.getByRole("heading", { name: "Thanks for your feedback." }),
    ).toBeFocused();
  }
  await page.getByRole("button", { name: "Send another report" }).click();
  await field.fill(draft);
  await send(page).click();
  await expect(page.getByRole("alert")).toContainText(
    "Too many recent submissions",
  );
  await expect(field).toBeEditable();
  await expect(field).toHaveValue(draft);
  await page.route(`**${feedbackPath}`, (route) => route.abort());
  await send(page).click();
  await page
    .getByRole("button", { name: "Start a separate submission" })
    .click();
  await expect(page.getByText(/could create a duplicate/)).toBeVisible();
  await page.getByRole("button", { name: "Start anyway" }).click();
  await expect(field).toBeEditable();
});

test("unavailable feedback points back to Profile help without a form email link", async ({
  page,
}) => {
  await account(page, "feedbackunavailable");
  await page.route("**/api/v1/feedback/availability", async (route) => {
    const response = await route.fetch();
    await route.fulfill({ response, json: { enabled: false } });
  });
  await page.goto("/app/?view=feedback");
  await expect(page.getByRole("status")).toContainText("Help & support");
  await expect(page.getByLabel("Contact reason")).toBeDisabled();
  await expect(send(page)).toBeDisabled();
  await expect(
    page
      .getByRole("region", { name: "Feedback", exact: true })
      .locator('a[href^="mailto:"]'),
  ).toHaveCount(0);
  await page
    .getByRole("button", { name: "Back to profile", exact: true })
    .click();
  await page.getByText("Help & support", { exact: true }).click();
  await expect(
    page.getByRole("link", { name: "Contact support" }),
  ).toHaveAttribute("href", "mailto:admin@example.invalid");
});
