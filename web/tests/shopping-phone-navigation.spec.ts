import { expect, test } from "@playwright/test";
import { shoppingAccount } from "./support/shopping";
test.use({ locale: "en-GB", hasTouch: true });
test.setTimeout(100_000);
test("date input remains between both arrows at phone widths", async ({
  page,
}) => {
  await shoppingAccount(page);
  for (const width of [320, 375, 390, 430, 600, 760]) {
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/app/?week=2026-11-02");
    const input = page.getByLabel("Browse week"),
      previous = page.getByLabel("Previous week", { exact: true }),
      next = page.getByLabel("Next week", { exact: true });
    const date = await input.boundingBox(),
      left = await previous.boundingBox(),
      right = await next.boundingBox();
    expect(date!.x).toBeGreaterThanOrEqual(left!.x + left!.width + 7);
    expect(date!.x + date!.width).toBeLessThanOrEqual(right!.x - 7);
    expect(
      await input.evaluate((e) => e.scrollWidth <= e.clientWidth + 1),
    ).toBe(true);
  }
});
test("swipe uses a visible incoming page and fading navigation with reduced-motion fallback", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await shoppingAccount(page);
  await page.goto("/app/?view=shopping&week=2026-11-02");
  expect(
    await page
      .locator(".workspace")
      .evaluate((e) => getComputedStyle(e).viewTransitionName),
  ).toBe("page-content");
  expect(
    await page
      .getByRole("button", { name: "Shopping list", exact: true })
      .evaluate((e) => getComputedStyle(e).transitionProperty),
  ).toContain("background-color");
  await page.evaluate(() => {
    (window as unknown as { swipeAnimations: string[] }).swipeAnimations = [];
    document.addEventListener("animationstart", (event) =>
      (window as unknown as { swipeAnimations: string[] }).swipeAnimations.push(
        (event as AnimationEvent).animationName,
      ),
    );
  });
  await page.evaluate(() => {
    const target = document.querySelector(".workspace")!;
    const point = (x: number) =>
      new Touch({ identifier: 1, target, clientX: x, clientY: 240 });
    target.dispatchEvent(
      new TouchEvent("touchstart", { bubbles: true, touches: [point(300)] }),
    );
    target.dispatchEvent(
      new TouchEvent("touchend", {
        bubbles: true,
        touches: [],
        changedTouches: [point(80)],
      }),
    );
  });
  await expect(
    page.getByRole("button", { name: "Profile", exact: true }),
  ).toHaveAttribute("aria-current", "page");
  await expect
    .poll(() =>
      page.evaluate(() =>
        (
          window as unknown as { swipeAnimations: string[] }
        ).swipeAnimations.some((name) => name.startsWith("page-enter")),
      ),
    )
    .toBe(true);
  await page
    .getByRole("button", { name: "Shopping list", exact: true })
    .click();
  await page.evaluate(() => {
    const target = document.querySelector(".workspace")!;
    const point = (x: number) =>
      new Touch({ identifier: 1, target, clientX: x, clientY: 240 });
    for (let attempt = 0; attempt < 2; attempt++) {
      target.dispatchEvent(
        new TouchEvent("touchstart", { bubbles: true, touches: [point(300)] }),
      );
      target.dispatchEvent(
        new TouchEvent("touchend", {
          bubbles: true,
          touches: [],
          changedTouches: [point(80)],
        }),
      );
    }
  });
  await expect(
    page.getByRole("button", { name: "Profile", exact: true }),
  ).toHaveAttribute("aria-current", "page");
  // Let skipped-transition promise rejections reach the browser error handler.
  await page.evaluate(() => new Promise(requestAnimationFrame));
  expect(errors).toEqual([]);
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page
    .getByRole("button", { name: "Shopping list", exact: true })
    .click();
  await page.evaluate(() => {
    const target = document.querySelector(".workspace")!,
      point = (x: number) =>
        new Touch({ identifier: 1, target, clientX: x, clientY: 240 });
    target.dispatchEvent(
      new TouchEvent("touchstart", { bubbles: true, touches: [point(300)] }),
    );
    target.dispatchEvent(
      new TouchEvent("touchend", {
        bubbles: true,
        touches: [],
        changedTouches: [point(80)],
      }),
    );
  });
  await expect(
    page.getByRole("button", { name: "Profile", exact: true }),
  ).toHaveAttribute("aria-current", "page");
  expect(
    await page.locator(".workspace").evaluate((e) => e.getAnimations().length),
  ).toBe(0);
  await page.emulateMedia({ reducedMotion: "no-preference" });
  await page
    .getByRole("button", { name: "Shopping list", exact: true })
    .click();
  await page.evaluate(() => {
    Object.defineProperty(document, "startViewTransition", {
      value: undefined,
      configurable: true,
    });
    const target = document.querySelector(".workspace")!,
      point = (x: number) =>
        new Touch({ identifier: 1, target, clientX: x, clientY: 240 });
    target.dispatchEvent(
      new TouchEvent("touchstart", { bubbles: true, touches: [point(300)] }),
    );
    target.dispatchEvent(
      new TouchEvent("touchend", {
        bubbles: true,
        touches: [],
        changedTouches: [point(80)],
      }),
    );
  });
  await expect(
    page.getByRole("button", { name: "Profile", exact: true }),
  ).toHaveAttribute("aria-current", "page");
  expect(
    await page.locator(".workspace").evaluate((e) => e.getAnimations().length),
  ).toBeGreaterThan(0);
});
