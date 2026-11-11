import { expect, type Page } from "@playwright/test";
import { registerFixture } from "./registration";
export const shoppingEmail = "shopping-suite@test.invalid";
export const shoppingPassword = "ChefTest2026!";
export async function shoppingAccount(page: Page, capability?: string) {
  await page.goto("/app/");
  await expect(
    page.getByRole("heading", { name: "Welcome back.", exact: true }),
  ).toBeVisible();
  const session = await (await page.request.get("/api/v1/session")).json();
  const headers = {
    Origin: new URL(page.url()).origin,
    "X-CSRF-Token": session.csrfToken,
  };
  let response = await page.request.post("/api/v1/login", {
    headers,
    data: { email: shoppingEmail, password: shoppingPassword },
  });
  if (response.status() === 429) {
    await expect
      .poll(
        async () => {
          response = await page.request.post("/api/v1/login", {
            headers,
            data: { email: shoppingEmail, password: shoppingPassword },
          });
          return response.status();
        },
        { timeout: 65_000, intervals: [1000, 5000, 10000] },
      )
      .toBe(200);
  }
  const signed =
    response.status() === 401
      ? await registerFixture(
          page,
          {
            email: shoppingEmail,
            password: shoppingPassword,
            username: "shopping-suite",
            firstName: "Shopping",
            lastName: "Chef",
          },
          capability,
        )
      : await response.json();
  expect(!!signed.user).toBe(true);
  const list = await (
    await page.request.get("/api/v1/shopping/2026-11-02")
  ).json();
  list.items = list.items
    .filter((i: { manual: boolean }) => !i.manual)
    .map((i: object) => ({ ...i, state: "buy" }));
  list.meals = list.meals.map((m: object) => ({ ...m, selected: true }));
  expect(
    (
      await page.request.put("/api/v1/shopping/2026-11-02", {
        headers: { ...headers, "X-CSRF-Token": signed.csrfToken },
        data: list,
      })
    ).ok(),
  ).toBe(true);
  return signed;
}

export async function openExtras(page: Page) {
  const disclosure = page.locator(".shopping-add");
  if ((await disclosure.getAttribute("open")) === null)
    await disclosure.locator(":scope > summary").click();
}
