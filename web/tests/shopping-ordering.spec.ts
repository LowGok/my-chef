import { expect, test } from "@playwright/test";
import { shoppingAccount, openExtras } from "./support/shopping";
test.use({ locale: "en-GB" });
test.setTimeout(100_000);
test("phone layout and shopping ordering keep completed items above closed extras", async ({
  page,
}) => {
  await shoppingAccount(page);
  for (const width of [320, 390, 440, 600, 760]) {
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/app/?week=2026-11-02");
    const picker = page.locator(".week-picker"),
      row = page.locator(".section-title").filter({ has: picker });
    const a = await picker.boundingBox(),
      b = await row.boundingBox();
    expect(
      a && b && Math.abs(a.x + a.width / 2 - b.x - b.width / 2),
    ).toBeLessThanOrEqual(2);
  }
  await page.goto("/app/?view=shopping&week=2026-11-02");
  await expect(page.getByText("Saved", { exact: true })).toBeVisible();
  await expect(page.locator(".shopping-heading > .icon")).toHaveCount(0);
  await expect(
    page
      .getByRole("button", { name: "Shopping list", exact: true })
      .locator(".icon"),
  ).toHaveCount(1);
  const tabs = await page.locator(".shopping-tabs").boundingBox(),
    first = await page.locator(".shopping-tabs button").first().boundingBox(),
    second = await page.locator(".shopping-tabs button").nth(1).boundingBox();
  expect(first!.x - tabs!.x).toBeLessThan(3);
  expect(second!.x - first!.x - first!.width).toBeLessThan(20);
  await expect(page.getByLabel("Extra item", { exact: true })).toBeHidden();
  for (const [name, amount] of [
    ["Zebra soap", "2"],
    ["Apple snack", "10"],
    ["Rice extra", "1"],
  ]) {
    await openExtras(page);
    await page.getByLabel("Extra item", { exact: true }).fill(name);
    await page.getByLabel("Amount", { exact: true }).fill(amount);
    await page.getByLabel("Unit", { exact: true }).fill("piece");
    await page.getByRole("button", { name: "Add extra", exact: true }).click();
  }
  await expect(page.getByText("Saved", { exact: true })).toBeVisible();
  await page.getByLabel("Mark Zebra soap as bought").click();
  for (const option of [
    "Category",
    "Fresh items first",
    "Name (A–Z)",
    "Quantity (high to low)",
  ]) {
    await page
      .getByLabel("Sort by", { exact: true })
      .selectOption({ label: option });
    const basket = page.getByRole("region", { name: "In basket", exact: true });
    await expect(basket.getByLabel("Mark Zebra soap as needed")).toBeChecked();
    expect(
      await basket.evaluate((element) =>
        element.nextElementSibling?.classList.contains("shopping-add"),
      ),
    ).toBe(true);
  }
  await page
    .getByLabel("Extra item", { exact: true })
    .fill("Unsubmitted coffee");
  await page
    .getByLabel("Sort by", { exact: true })
    .selectOption({ label: "Name (A–Z)" });
  await expect(page.getByLabel("Extra item", { exact: true })).toHaveValue(
    "Unsubmitted coffee",
  );
  await page.evaluate(() => window.scrollTo(0, document.body.scrollHeight));
  await page.waitForResponse(
    (response) =>
      response.url().includes("/api/v1/shopping/") &&
      response.request().method() === "GET",
  );
  expect(await page.evaluate(() => window.scrollY)).toBeGreaterThan(0);
  await page.reload();
  await expect(page.getByLabel("Extra item", { exact: true })).toBeHidden();
  await expect(
    page
      .getByRole("region", { name: "In basket" })
      .getByLabel("Mark Zebra soap as needed"),
  ).toBeChecked();
  await page
    .getByRole("region", { name: "In basket" })
    .getByLabel("Mark Zebra soap as needed")
    .click();
  await expect(page.getByRole("region", { name: "In basket" })).toHaveCount(0);
  await page.locator(".shopping-add > summary").focus();
  await page.keyboard.press("Enter");
  await expect(page.getByLabel("Extra item", { exact: true })).toBeVisible();
  await page.screenshot({
    path: "../test-results/shopping-ordering/phone.png",
    fullPage: true,
  });
});
