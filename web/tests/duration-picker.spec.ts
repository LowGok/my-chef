import { expect, test, type Locator, type Page } from "@playwright/test";
import {
  openPersonalLibrary,
  personalFixture,
  privateAction,
} from "./support/personal-recipes";

async function editor(page: Page, name: string) {
  await personalFixture(page, name);
  await openPersonalLibrary(page);
  await page
    .getByRole("button", { name: "Create a recipe", exact: true })
    .click();
}
async function setWheel(wheel: Locator, value: number) {
  await wheel.press("Home");
  const minimum = Number(await wheel.getAttribute("aria-valuemin"));
  const distance = value - minimum;
  for (let step = 0; step < Math.floor(distance / 10); step++)
    await wheel.press("PageUp");
  for (let step = 0; step < distance % 10; step++) await wheel.press("ArrowUp");
  await expect(wheel).toHaveAttribute("aria-valuenow", String(value));
}
async function duration(
  page: Page,
  label: string,
  hours: number,
  minutes: number,
) {
  await page.getByRole("button", { name: label, exact: true }).click();
  const dialog = page.getByRole("dialog", { name: label, exact: true });
  await setWheel(
    dialog.getByRole("spinbutton", { name: "Hours", exact: true }),
    hours,
  );
  await setWheel(
    dialog.getByRole("spinbutton", { name: "Minutes", exact: true }),
    minutes,
  );
  await dialog.getByRole("button", { name: "Done", exact: true }).click();
  await expect(dialog).not.toBeVisible();
}

test("duration wheels stage exact minutes, cancel cleanly and return focus", async ({
  page,
}) => {
  await editor(page, "durationcancel");
  const trigger = page.getByRole("button", {
    name: "Cooking time",
    exact: true,
  });
  await expect(trigger).toHaveText("0 min");
  await trigger.click();
  const dialog = page.getByRole("dialog", {
    name: "Cooking time",
    exact: true,
  });
  const hours = dialog.getByRole("spinbutton", { name: "Hours", exact: true });
  await expect(hours).toBeFocused();
  await setWheel(hours, 2);
  await setWheel(
    dialog.getByRole("spinbutton", { name: "Minutes", exact: true }),
    17,
  );
  await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(trigger).toBeFocused();
  await expect(trigger).toHaveText("0 min");
  await trigger.click();
  await hours.press("End");
  await expect(hours).toHaveAttribute("aria-valuenow", "24");
  await expect(
    dialog.getByRole("spinbutton", { name: "Minutes", exact: true }),
  ).toHaveAttribute("aria-valuemax", "0");
  await page.keyboard.press("Escape");
  await expect(trigger).toBeFocused();
  await duration(page, "Preparation time", 0, 0);
  let warnings = 0;
  page.on("dialog", async (prompt) => {
    warnings++;
    await prompt.dismiss();
  });
  await page.getByRole("button", { name: "Profile", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Your profile.", exact: true }),
  ).toBeVisible();
  expect(warnings).toBe(0);
});

test("exact duration payloads, total calculation and errors survive the picker", async ({
  page,
}) => {
  await editor(page, "durationvalues");
  await privateAction(async () => {
    await page.getByLabel("Title", { exact: true }).fill("A duration fixture");
    await page.getByLabel("Ingredient 1", { exact: true }).fill("Carrot");
    await page.getByLabel("Step 1", { exact: true }).fill("Cook the fixture.");
  });
  await duration(page, "Preparation time", 1, 17);
  await duration(page, "Cooking time", 0, 43);
  await expect(
    page.getByRole("button", { name: "Total time", exact: true }),
  ).toHaveText("2 h");
  await duration(page, "Total time", 0, 1);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  const total = page.getByRole("button", { name: "Total time", exact: true });
  await expect(total).toBeFocused();
  await expect(total).toHaveAttribute("aria-invalid", "true");
  await expect(page.getByLabel("Title", { exact: true })).toHaveValue(
    "A duration fixture",
  );
  await duration(page, "Total time", 2, 13);
  await duration(page, "Cooking time", 0, 44);
  await expect(total).toHaveText("2 h 13 min");
  const request = page.waitForRequest(
    (r) =>
      r.method() === "POST" &&
      new URL(r.url()).pathname === "/api/v1/personal-recipes",
  );
  await page.getByRole("button", { name: "Save", exact: true }).click();
  const body = (await request).postDataJSON();
  expect([
    body.content.prepMinutes,
    body.content.cookMinutes,
    body.content.totalMinutes,
  ]).toEqual([77, 44, 133]);
  await expect(page.locator(".personal-detail")).toBeVisible();
  await page.getByRole("button", { name: "Edit recipe", exact: true }).click();
  await page
    .getByRole("button", { name: "Preparation time", exact: true })
    .click();
  const dialog = page.getByRole("dialog", {
    name: "Preparation time",
    exact: true,
  });
  await expect(
    dialog.getByRole("spinbutton", { name: "Hours", exact: true }),
  ).toHaveAttribute("aria-valuenow", "1");
  await expect(
    dialog.getByRole("spinbutton", { name: "Minutes", exact: true }),
  ).toHaveAttribute("aria-valuenow", "17");
});

test("wheels handle scrolling, short phones, enlarged text and duration bounds", async ({
  page,
}) => {
  await editor(page, "durationtouch");
  for (const width of [320, 360, 390, 430]) {
    for (const scale of ["100%", "200%"]) {
      await page.setViewportSize({ width, height: 568 });
      await page.evaluate(
        (size) => (document.documentElement.style.fontSize = size),
        scale,
      );
      await page
        .getByRole("button", { name: "Total time", exact: true })
        .click();
      const dialog = page.getByRole("dialog", {
        name: "Total time",
        exact: true,
      });
      const bounds = await dialog.evaluate((el) => ({
        width: el.scrollWidth,
        client: el.clientWidth,
        left: el.getBoundingClientRect().left,
        right: el.getBoundingClientRect().right,
      }));
      expect(bounds.width).toBeLessThanOrEqual(bounds.client + 1);
      expect(bounds.left).toBeGreaterThanOrEqual(0);
      expect(bounds.right).toBeLessThanOrEqual(width);
      await expect(
        dialog.getByRole("button", { name: "Done", exact: true }),
      ).toBeInViewport();
      await dialog
        .getByRole("spinbutton", { name: "Hours", exact: true })
        .press("End");
      await expect(
        dialog.getByRole("spinbutton", { name: "Hours", exact: true }),
      ).toHaveAttribute("aria-valuenow", "48");
      await expect(
        dialog.getByRole("spinbutton", { name: "Minutes", exact: true }),
      ).toHaveAttribute("aria-valuemax", "0");
      await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
    }
  }
  await page.evaluate(() => (document.documentElement.style.fontSize = "100%"));
  await page.setViewportSize({ width: 568, height: 320 });
  await page.getByRole("button", { name: "Cooking time", exact: true }).click();
  const dialog = page.getByRole("dialog", {
    name: "Cooking time",
    exact: true,
  });
  await expect(
    dialog.getByRole("button", { name: "Done", exact: true }),
  ).toBeInViewport();
  const minutes = dialog.getByRole("spinbutton", {
    name: "Minutes",
    exact: true,
  });
  const originalURL = page.url();
  await minutes.evaluate((element) => {
    const touch = (x: number) =>
      new Touch({ identifier: 1, target: element, clientX: x, clientY: 180 });
    element.dispatchEvent(
      new TouchEvent("touchstart", {
        bubbles: true,
        touches: [touch(450)],
        changedTouches: [touch(450)],
      }),
    );
    element.dispatchEvent(
      new TouchEvent("touchend", {
        bubbles: true,
        touches: [],
        changedTouches: [touch(100)],
      }),
    );
  });
  await expect(dialog).toBeVisible();
  expect(page.url()).toBe(originalURL);
  expect(
    await minutes.evaluate((element) => getComputedStyle(element).touchAction),
  ).toContain("pinch-zoom");
  await minutes.evaluate((element) => {
    const row = element.querySelector<HTMLElement>("[data-wheel-value='23']")!;
    element.scrollTop =
      row.offsetTop - (element.clientHeight - row.offsetHeight) / 2;
    // Commit before a scroll event can update React state: Done must use the
    // visible wheel position rather than a stale staged value.
    element
      .closest("dialog")
      ?.querySelector<HTMLButtonElement>(".duration-footer button:last-child")
      ?.click();
  });
  await expect(
    page.getByRole("button", { name: "Cooking time", exact: true }),
  ).toHaveText("23 min");
  expect(new URL(page.url()).searchParams.get("view")).toBe("my-recipes");
});

test("cancel, backdrop and unchanged Done preserve the save key; non-drag controls stay operable", async ({
  page,
}) => {
  await editor(page, "durationretrykey");
  await privateAction(async () => {
    await page
      .getByLabel("Title", { exact: true })
      .fill("Duration retry fixture");
    await page.getByLabel("Ingredient 1", { exact: true }).fill("Carrot");
    await page.getByLabel("Step 1", { exact: true }).fill("Cook the fixture.");
  });
  await duration(page, "Cooking time", 0, 10);
  await duration(page, "Total time", 0, 1);
  const keys: string[] = [];
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      new URL(request.url()).pathname === "/api/v1/personal-recipes"
    )
      keys.push(request.postDataJSON().requestKey);
  });
  const invalidSave = async () => {
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(
      page.getByRole("button", { name: "Total time", exact: true }),
    ).toBeFocused();
  };
  await invalidSave();
  await page.getByRole("button", { name: "Cooking time", exact: true }).click();
  const dialog = page.getByRole("dialog", {
    name: "Cooking time",
    exact: true,
  });
  await dialog
    .getByRole("button", { name: "Increase minutes", exact: true })
    .click();
  await expect(
    dialog.getByRole("spinbutton", { name: "Minutes", exact: true }),
  ).toHaveAttribute("aria-valuenow", "11");
  const widths = await dialog
    .locator(".duration-footer button")
    .evaluateAll((buttons) =>
      buttons.map((button) => button.getBoundingClientRect().width),
    );
  expect(Math.abs(widths[0] - widths[1])).toBeLessThan(1);
  await page.mouse.click(2, 2);
  await expect(dialog).not.toBeVisible();
  await invalidSave();
  await duration(page, "Cooking time", 0, 10);
  await invalidSave();
  expect(keys.length).toBe(3);
  expect(new Set(keys).size).toBe(1);
  await page.emulateMedia({ reducedMotion: "reduce" });
  await duration(page, "Total time", 0, 20);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.locator(".personal-detail")).toBeVisible();
  expect(keys[3]).not.toBe(keys[0]);
});

test("maximum preparation and cooking times retain an exact forty-eight-hour total", async ({
  page,
}) => {
  await editor(page, "durationmaximum");
  await privateAction(async () => {
    await page
      .getByLabel("Title", { exact: true })
      .fill("Maximum duration fixture");
    await page.getByLabel("Ingredient 1", { exact: true }).fill("Carrot");
    await page
      .getByLabel("Step 1", { exact: true })
      .fill("A synthetic long-duration fixture.");
  });
  await duration(page, "Preparation time", 24, 0);
  await duration(page, "Cooking time", 24, 0);
  await expect(
    page.getByRole("button", { name: "Total time", exact: true }),
  ).toHaveText("48 h");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.locator(".personal-detail")).toBeVisible();
  await page.getByRole("button", { name: "Edit recipe", exact: true }).click();
  await page.getByRole("button", { name: "Total time", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Total time", exact: true });
  await expect(
    dialog.getByRole("spinbutton", { name: "Hours", exact: true }),
  ).toHaveAttribute("aria-valuenow", "48");
  await expect(
    dialog.getByRole("spinbutton", { name: "Minutes", exact: true }),
  ).toHaveAttribute("aria-valuenow", "0");
});
