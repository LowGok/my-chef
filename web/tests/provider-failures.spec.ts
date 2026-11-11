import { expect, test, type Page } from "@playwright/test";
import { registerFixture } from "./support/registration";

test.setTimeout(90_000);

let cookies: Awaited<
  ReturnType<import("@playwright/test").BrowserContext["cookies"]>
>;
test.beforeAll(async ({ browser, baseURL }) => {
  const page = await browser.newPage({ baseURL });
  await registerFixture(page, {
    firstName: "Failure",
    lastName: "Chef",
    username: "provider-chef",
    email: "provider-chef@example.test",
    password: "ChefTest2026!",
  });
  await expect(
    page.getByRole("button", { name: "Plan a menu", exact: true }),
  ).toBeVisible();
  cookies = await page.context().cookies();
  await page.context().close();
});

async function plan(page: Page, region: string) {
  // Inject fixture failures at the network boundary; the product always requests Belgium.
  await page.route("**/api/v1/generations", async (route) => {
    if (route.request().method() !== "POST") return route.continue();
    const body = route.request().postDataJSON();
    expect(body.region).toBe("Belgium");
    await route.continue({ postData: JSON.stringify({ ...body, region }) });
  });
  await page.context().addCookies(cookies);
  await page.goto("/app/");
  await page.getByRole("button", { name: "Plan a menu", exact: true }).click();
  await page.getByLabel("Number of meals", { exact: true }).fill("1");
  await page.getByRole("button", { name: "Review menu", exact: true }).click();
  const submitted = page.waitForResponse(
    (r) =>
      r.url().endsWith("/api/v1/generations") &&
      r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Plan my week", exact: true }).click();
  const response = await submitted;
  expect(response.status()).toBe(202);
  return response.json();
}

for (const [region, code, guidance] of [
  [
    "__quota",
    "provider_quota",
    "The recipe provider has no quota available. Try again when its quota is restored.",
  ],
  [
    "__timeout",
    "provider_timeout",
    "The recipe provider timed out. Check this request before explicitly starting a new one.",
  ],
  [
    "__invalid",
    "provider_invalid",
    "The provider returned recipes that did not meet this request. Review your preferences before trying a new menu.",
  ],
]) {
  test(`${code} leaves no menu and requires an explicit new request`, async ({
    page,
  }) => {
    const operation = await plan(page, region);
    await expect(page.getByText(guidance, { exact: true })).toBeVisible();
    const saved = await (
      await page.request.get(`/api/v1/generations/${operation.id}`)
    ).json();
    expect(saved.status).toBe("failed");
    expect(saved.errorCode).toBe(code);
    expect(saved.menuId).toBeNull();
    await expect(page.locator(".recipe-card")).toHaveCount(0);
    let posts = 0;
    page.on("request", (r) => {
      if (r.url().endsWith("/api/v1/generations") && r.method() === "POST")
        posts++;
    });
    await page.reload();
    await expect(page.getByText(guidance, { exact: true })).toBeVisible();
    expect(posts).toBe(0);
    await page
      .getByRole("button", { name: "Plan a new request", exact: true })
      .click();
    await expect(
      page.getByRole("button", { name: "Review menu", exact: true }),
    ).toBeVisible();
    expect(posts).toBe(0);
  });
}

test("refresh of an active request resumes status without submitting another generation", async ({
  page,
}) => {
  const operation = await plan(page, "__slow");
  let posts = 0;
  page.on("request", (r) => {
    if (r.url().endsWith("/api/v1/generations") && r.method() === "POST")
      posts++;
  });
  await page.reload();
  await expect(
    page.getByRole("button", { name: "Open Lemon garden pasta", exact: true }),
  ).toBeVisible();
  expect(posts).toBe(0);
  const completed = await (
    await page.request.get(`/api/v1/generations/${operation.id}`)
  ).json();
  expect(completed.status).toBe("succeeded");
  await page
    .getByRole("button", { name: "Open Lemon garden pasta", exact: true })
    .click();
  await expect(
    page.getByText("Image unavailable. Your saved recipe is ready to cook.", {
      exact: true,
    }),
  ).toBeVisible();
});
