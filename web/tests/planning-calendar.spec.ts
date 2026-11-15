import { expect, test } from "@playwright/test";
import { registerFixture } from "./support/registration";
import { personalGrant } from "./support/personal-recipes";
import { addDays } from "../src/dates";

test.use({
  viewport: { width: 375, height: 812 },
  timezoneId: "America/Los_Angeles",
});

test("a confirmed household week controls Free browsing and the current-day highlight", async ({
  page,
}) => {
  test.skip(
    process.env.MYCHEF_PLANNING_TIERS !== "1",
    "run with the disposable planning fixture enabled",
  );

  await registerFixture(page, {
    email: "planning-calendar@example.test",
    username: "planning-calendar",
    firstName: "Planning",
    lastName: "Fixture",
    password: "Pasta on the window sill 42.",
  });
  await expect(
    page.getByRole("heading", { name: "Confirm your household time zone" }),
  ).toBeVisible();
  await page.getByLabel("Household time zone").fill("Pacific/Kiritimati");
  const confirmed = page.waitForResponse(
    (res) =>
      new URL(res.url()).pathname === "/api/v1/planning/zone" &&
      res.request().method() === "PUT",
  );
  await page.getByRole("button", { name: "Save time zone" }).click();
  const status = await (await confirmed).json();
  expect(status.zone).toBe("Pacific/Kiritimati");
  expect(status.firstWeek).toBe(status.lastWeek);
  expect(status.premium).toBe(false);
  await expect(page.getByLabel("Browse week")).toHaveValue(status.firstWeek);
  await expect(
    page.getByRole("button", { name: "Previous week" }),
  ).toBeDisabled();
  await expect(page.getByRole("button", { name: "Next week" })).toBeDisabled();

  await page.getByRole("button", { name: "Plan a menu", exact: true }).click();
  await expect(page.getByRole("button", { name: /review/i })).toBeVisible();
  const firstSlots = page.getByRole("group", { name: "Dinner dates" });
  await expect(firstSlots.getByRole("checkbox")).toHaveCount(3);
  await expect(firstSlots.getByRole("checkbox", { checked: true })).toHaveCount(
    3,
  );
  await page.getByRole("button", { name: "Review menu" }).click();
  await page.getByRole("button", { name: "Plan my week", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Open Lemon garden pasta" }),
  ).toHaveCount(3, { timeout: 15_000 });
  await page.clock.setFixedTime(new Date(status.firstWeek + "T20:00:00Z"));
  await page.goto("/app/");
  await expect(
    page.getByRole("button", { name: "Open Lemon garden pasta" }),
  ).toHaveCount(3);
  await expect(page.locator(".meal-today,[aria-current=date]")).toHaveCount(0);
  await expect(page.getByLabel("Browse week")).toHaveValue(status.firstWeek);

  const history = async () =>
    (
      await page.request.get(
        `/api/v1/menus?from=${status.firstWeek}&to=${addDays(status.firstWeek, 7)}`,
      )
    ).json() as Promise<{ recipes: { id: string; date: string }[] }>;
  const beforeCorrection = (await history()).recipes;
  const target = beforeCorrection.find(
    (meal) => meal.date === addDays(status.firstWeek, 1),
  )!;
  await page
    .getByRole("button", { name: "Open Lemon garden pasta" })
    .nth(1)
    .click();
  await expect(
    page.getByRole("heading", { name: "Lemon garden pasta" }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Check for a correction" }).click();
  await expect(page.getByText(/no confirmed issue to correct/)).toBeVisible();
  expect((await history()).recipes).toEqual(beforeCorrection);
  const session = await (await page.request.get("/api/v1/session")).json();
  const profile = session.user;
  const setDislikes = async (dislikes: string[]) =>
    expect(
      (
        await page.request.put("/api/v1/profile", {
          headers: {
            Origin: new URL(page.url()).origin,
            "X-CSRF-Token": session.csrfToken,
          },
          data: {
            username: profile.username,
            firstName: profile.firstName,
            lastName: profile.lastName,
            preferences: { ...profile.preferences, dislikes },
          },
        })
      ).ok(),
    ).toBe(true);
  await setDislikes(["Leek"]);
  await page.getByRole("button", { name: "Check for a correction" }).click();
  await expect(
    page.getByRole("button", { name: "Open Lemon garden pasta" }),
  ).toHaveCount(3);
  const original = (await history()).recipes;
  expect(original.filter((r) => r.date !== target.date)).toEqual(
    beforeCorrection.filter((r) => r.date !== target.date),
  );
  expect(original.find((r) => r.date === target.date)?.id).not.toBe(target.id);
  await setDislikes([]);
  await personalGrant(page, "premium");
  await page.reload();
  await expect(page.getByRole("button", { name: "Next week" })).toBeEnabled();
  await page.getByRole("button", { name: "Plan a menu", exact: true }).click();
  const extended = page.getByRole("group", { name: "Dinner dates" });
  await expect(extended.getByRole("checkbox")).toHaveCount(4);
  await expect(extended.getByRole("checkbox", { checked: true })).toHaveCount(
    4,
  );
  await page.getByRole("button", { name: "Review menu" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page.getByRole("button", { name: "Plan my week", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Open Lemon garden pasta" }),
  ).toHaveCount(7, { timeout: 15_000 });
  const full = (await history()).recipes;
  expect(
    full.filter((r) => original.some((item) => item.date === r.date)),
  ).toEqual(original);

  await page.getByRole("button", { name: "Profile", exact: true }).click();
  await page.getByText("Planning calendar", { exact: true }).click();
  await expect(page.getByLabel("Household time zone")).toHaveValue(
    "Pacific/Kiritimati",
  );
});
