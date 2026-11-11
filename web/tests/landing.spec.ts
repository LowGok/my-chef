import { test, expect } from "@playwright/test";

test("minimal public entry opens the application without external requests", async ({ page }) => {
  const external: string[] = [];
  page.on("request", request => {
    if (new URL(request.url()).origin !== "http://127.0.0.1:18137") external.push(request.url());
  });
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "MyChef", exact: true })).toBeVisible();
  await page.getByRole("link", { name: "Open application", exact: true }).click();
  await expect(page).toHaveURL(/\/app\/$/);
  await expect(page.getByRole("button", { name: "Sign in to MyChef", exact: true })).toBeVisible();
  expect(external).toEqual([]);
});
