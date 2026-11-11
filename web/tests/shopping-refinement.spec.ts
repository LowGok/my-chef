import { expect, test, type Page } from "@playwright/test";
import { shoppingAccount, openExtras } from "./support/shopping";

test.use({
  locale: "en-GB",
  hasTouch: true,
  viewport: { width: 390, height: 844 },
});
test.setTimeout(100_000);
let cookies: Awaited<
  ReturnType<import("@playwright/test").BrowserContext["cookies"]>
>;
test.beforeAll(async ({ browser, baseURL }) => {
  const context = await browser.newContext({ baseURL });
  const page = await context.newPage();
  await shoppingAccount(page);
  cookies = await context.cookies();
  await context.close();
});
test.beforeEach(async ({ page, context }) => {
  await context.addCookies(cookies);
  await page.goto("/app/?view=shopping&week=2026-11-02");
  await expect(page.getByText("Saved", { exact: true })).toBeVisible();
});

test("shopping stays minimal and purchases remain in place with strikethrough", async ({
  page,
}) => {
  const views = page.getByRole("group", { name: "Shopping views" });
  await expect(views.getByRole("button")).toHaveCount(2);
  await expect(page.getByRole("button", { name: /In basket/ })).toHaveCount(0);
  await expect(
    page
      .locator(".shopping")
      .getByRole("button", { name: "Sign out", exact: true }),
  ).toHaveCount(0);
  await expect(page.locator(".shopping-row details")).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: /Choose meals/ }),
  ).toBeVisible();
  await openExtras(page);
  await page.getByLabel("Extra item", { exact: true }).fill("Sample coffee");
  await page.getByLabel("Amount", { exact: true }).fill("1");
  await page.getByLabel("Unit", { exact: true }).fill("bag");
  await page.getByRole("button", { name: "Add extra", exact: true }).click();
  await page
    .getByLabel("Mark Sample coffee as bought", { exact: true })
    .click();
  const row = page
    .locator(".shopping-row")
    .filter({ hasText: "Sample coffee" });
  await expect(row).toBeVisible();
  await expect(row.locator(".shopping-item-copy strong")).toHaveCSS(
    "text-decoration-line",
    "line-through",
  );
  await expect(row.locator(".shopping-quantity")).toHaveCSS(
    "text-decoration-line",
    "line-through",
  );
  await page.reload();
  await expect(
    page.getByLabel("Mark Sample coffee as needed", { exact: true }),
  ).toBeChecked();
  await page
    .getByLabel("Mark Sample coffee as needed", { exact: true })
    .click();
  await expect(row.locator(".shopping-item-copy strong")).toHaveCSS(
    "text-decoration-line",
    "none",
  );
  await page.getByRole("button", { name: "Meals", exact: true }).click();
  const colors = await page
    .getByLabel("Previous week", { exact: true })
    .evaluate((element) => {
      const css = getComputedStyle(element);
      return [
        css.color,
        css.backgroundColor,
        css.borderTopColor,
        css.borderTopWidth,
      ];
    });
  await page
    .getByRole("button", { name: "Shopping list", exact: true })
    .click();
  expect(
    await page
      .getByLabel("Previous shopping week", { exact: true })
      .evaluate((element) => {
        const css = getComputedStyle(element);
        return [
          css.color,
          css.backgroundColor,
          css.borderTopColor,
          css.borderTopWidth,
        ];
      }),
  ).toEqual(colors);
  for (const width of [320, 390, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: `../test-results/shopping-refinement/${width}.png`,
      fullPage: true,
    });
  }
});

async function swipe(
  page: Page,
  from: { x: number; y: number },
  to: { x: number; y: number },
  selector = ".workspace",
  multi = false,
) {
  await page.evaluate(
    ({ from, to, selector, multi }) => {
      const target = document.querySelector(selector)!;
      const point = (p: { x: number; y: number }, identifier: number) =>
        new Touch({
          identifier,
          target,
          clientX: p.x,
          clientY: p.y,
          radiusX: 1,
          radiusY: 1,
          force: 1,
        });
      const touches = [point(from, 1)];
      if (multi) touches.push(point({ x: from.x + 30, y: from.y + 30 }, 2));
      target.dispatchEvent(
        new TouchEvent("touchstart", {
          bubbles: true,
          touches,
          changedTouches: touches,
        }),
      );
      target.dispatchEvent(
        new TouchEvent("touchend", {
          bubbles: true,
          touches: [],
          changedTouches: [point(to, 1)],
        }),
      );
    },
    { from, to, selector, multi },
  );
}

test("horizontal swipes navigate main pages while guarded gestures leave the page alone", async ({
  page,
}) => {
  const url = page.url(),
    left = { x: 300, y: 350 },
    right = { x: 80, y: 350 };
  for (const [from, to, selector, multi] of [
    [left, { x: 290, y: 120 }, ".workspace", false],
    [left, { x: 280, y: 350 }, ".workspace", false],
    [{ x: 5, y: 350 }, left, ".workspace", false],
    [left, right, 'input[name="name"]', false],
    [left, right, ".workspace", true],
  ] as const) {
    await swipe(page, from, to, selector, multi);
    expect(page.url()).toBe(url);
  }
  await page.evaluate(() => {
    const dialog = document.createElement("dialog");
    dialog.id = "swipe-test-modal";
    document.body.append(dialog);
    dialog.showModal();
  });
  await swipe(page, left, right);
  expect(page.url()).toBe(url);
  await page.evaluate(() =>
    document.getElementById("swipe-test-modal")?.remove(),
  );
  const touch = await page.context().newCDPSession(page);
  await touch.send("Input.dispatchTouchEvent", {
    type: "touchStart",
    touchPoints: [{ x: 300, y: 240 }],
  });
  await touch.send("Input.dispatchTouchEvent", {
    type: "touchMove",
    touchPoints: [{ x: 190, y: 240 }],
  });
  await touch.send("Input.dispatchTouchEvent", {
    type: "touchMove",
    touchPoints: [{ x: 80, y: 240 }],
  });
  await touch.send("Input.dispatchTouchEvent", {
    type: "touchEnd",
    touchPoints: [],
  });
  await touch.detach();
  await expect(
    page.getByRole("button", { name: "Profile", exact: true }),
  ).toHaveAttribute("aria-current", "page");
  expect(new URL(page.url()).searchParams.get("week")).toBe("2026-11-02");
  const profileUrl = page.url();
  await swipe(page, left, right);
  expect(page.url()).toBe(profileUrl);
  await swipe(page, right, left);
  await expect(
    page.getByRole("button", { name: "Shopping list", exact: true }),
  ).toHaveAttribute("aria-current", "page");
  await swipe(page, right, left);
  await expect(
    page.getByRole("button", { name: "Meals", exact: true }),
  ).toHaveAttribute("aria-current", "page");
  const mealsUrl = page.url();
  await swipe(page, right, left);
  expect(page.url()).toBe(mealsUrl);
  await page
    .getByRole("button", { name: "Shopping list", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "Shopping list.", exact: true }),
  ).toBeVisible();
});
