import { openExtras } from "./support/shopping";
import { expect, test, type Page } from "@playwright/test";

import {
  shoppingAccount,
  shoppingEmail,
  shoppingPassword,
} from "./support/shopping";

test.use({ locale: "en-GB" });
test.setTimeout(100_000);
const shoppingPath = "/app/?view=shopping&week=2026-11-02";
async function openList(page: Page) {
  await shoppingAccount(page);
  await page.goto(shoppingPath);
  await expect(
    page.getByRole("heading", { name: "Shopping list.", exact: true }),
  ).toBeVisible();
  await expect(page.getByText("Saved", { exact: true })).toBeVisible();
  await expect
    .poll(() =>
      page.evaluate(async () => {
        const registration = await navigator.serviceWorker.getRegistration(
          location.href,
        );
        return registration?.active?.scriptURL;
      }),
    )
    .toBe(new URL("/app/sw.js", page.url()).href);
  await page.evaluate(async () => {
    await navigator.serviceWorker.ready;
    if (!navigator.serviceWorker.controller)
      await new Promise<void>((resolve) =>
        navigator.serviceWorker.addEventListener(
          "controllerchange",
          () => resolve(),
          { once: true },
        ),
      );
  });
}

test("cached app reloads offline, edits locally and converges after ordinary reconnection", async ({
  page,
  context,
}) => {
  await openList(page);
  await context.setOffline(true);
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "Shopping list.", exact: true }),
  ).toBeVisible();
  await expect(page.getByText(/Offline/).first()).toBeVisible();
  await openExtras(page);
  await page.getByLabel("Extra item", { exact: true }).fill("Offline coffee");
  await page.getByRole("button", { name: "Add extra", exact: true }).click();
  await expect(page.getByText(/Pending sync/).first()).toBeVisible();
  await page.reload();
  await expect(page.getByText("Offline coffee", { exact: true })).toBeVisible();
  await context.setOffline(false);
  await expect(page.getByText("Saved", { exact: true })).toBeVisible();
  const list = await (
    await page.request.get("/api/v1/shopping/2026-11-02")
  ).json();
  expect(
    list.items.filter((i: { name: string }) => i.name === "Offline coffee"),
  ).toHaveLength(1);
});

test("offline sign-out requires discard, blocks restoration and finishes server logout before explicit sign-in", async ({
  page,
  context,
}) => {
  await openList(page);
  await context.setOffline(true);
  await openExtras(page);
  await page.getByLabel("Extra item", { exact: true }).fill("Private extra");
  await page.getByRole("button", { name: "Add extra", exact: true }).click();
  await page.reload();
  await expect(
    page.getByRole("button", { name: "Meals", exact: true }),
  ).toBeDisabled();
  await page.getByRole("button", { name: "Profile", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Save preferences" }),
  ).toHaveCount(0);
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(
    page.getByRole("dialog", { name: "Unsaved shopping changes" }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Stay", exact: true }).click();
  await page
    .getByRole("button", { name: "Shopping list", exact: true })
    .click();
  await expect(page.getByText("Private extra", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Profile", exact: true }).click();
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await page
    .getByRole("button", { name: "Discard changes and sign out", exact: true })
    .click();
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "Welcome back.", exact: true }),
  ).toBeVisible();
  await expect(page.getByText("Private extra", { exact: true })).toHaveCount(0);
  await context.setOffline(false);
  await expect
    .poll(
      async () =>
        await page.evaluate(() =>
          localStorage.getItem("mychef.shopping.logout-pending"),
        ),
    )
    .toBe(null);
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "Welcome back.", exact: true }),
  ).toBeVisible();
  const session = await (await page.request.get("/api/v1/session")).json();
  expect(session.user).toBeNull();
});

async function extra(page: Page, name: string) {
  await openExtras(page);
  await page.getByLabel("Extra item", { exact: true }).fill(name);
  await page.getByRole("button", { name: "Add extra", exact: true }).click();
  await expect(page.getByText("Saved", { exact: true })).toBeVisible();
}

test("a server conflict stays pending until an explicit retry converges", async ({
  page,
  context,
}) => {
  await openList(page);
  await extra(page, "Conflict coffee");
  await context.setOffline(true);
  await page.getByLabel("Mark Conflict coffee as bought").click();
  const session = await (await page.request.get("/api/v1/session")).json();
  const remote = await (
    await page.request.get("/api/v1/shopping/2026-11-02")
  ).json();
  remote.items.find(
    (i: { name: string }) => i.name === "Conflict coffee",
  ).state = "home";
  expect(
    (
      await page.request.put("/api/v1/shopping/2026-11-02", {
        headers: {
          Origin: new URL(page.url()).origin,
          "X-CSRF-Token": session.csrfToken,
        },
        data: remote,
      })
    ).ok(),
  ).toBe(true);
  await context.setOffline(false);
  await expect(page.getByText(/changed on the server/)).toBeVisible();
  await expect(page.getByText(/Pending sync/).first()).toBeVisible();
  await page
    .getByRole("button", { name: "Reviewed latest list · Retry my changes" })
    .click();
  await expect(page.getByText("Saved", { exact: true })).toBeVisible();
  const saved = await (
    await page.request.get("/api/v1/shopping/2026-11-02")
  ).json();
  expect(
    saved.items.filter((i: { name: string }) => i.name === "Conflict coffee"),
  ).toHaveLength(1);
  expect(
    saved.items.find((i: { name: string }) => i.name === "Conflict coffee")
      .state,
  ).toBe("basket");
});

test("confirmed expiry hides cached data and same-account sign-in recovers queued edits", async ({
  page,
  context,
}) => {
  await openList(page);
  await context.setOffline(true);
  await openExtras(page);
  await page
    .getByLabel("Extra item", { exact: true })
    .fill("Recover this extra");
  await page.getByRole("button", { name: "Add extra", exact: true }).click();
  const session = await (await page.request.get("/api/v1/session")).json();
  expect(
    (
      await page.request.post("/api/v1/logout", {
        headers: {
          Origin: new URL(page.url()).origin,
          "X-CSRF-Token": session.csrfToken,
        },
        data: {},
      })
    ).status(),
  ).toBe(204);
  await context.setOffline(false);
  await expect(
    page.getByRole("heading", { name: "Welcome back.", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("Recover this extra", { exact: true }),
  ).toHaveCount(0);
  await context.setOffline(true);
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "Welcome back.", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("Recover this extra", { exact: true }),
  ).toHaveCount(0);
  await context.setOffline(false);
  await page.getByLabel("Email", { exact: true }).fill(shoppingEmail);
  await page.getByLabel("Password", { exact: true }).fill(shoppingPassword);
  await page
    .getByRole("button", { name: "Sign in to MyChef", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Shopping list", exact: true })
    .click();
  // Sign-in selects the current week. Reopen the list holding the queued edit.
  await page.goto(shoppingPath);
  await expect(
    page.getByText("Recover this extra", { exact: true }),
  ).toBeVisible();
  await expect(page.getByText("Saved", { exact: true })).toBeVisible();
});

test("failed server logout keeps the local sign-out marker until retry", async ({
  page,
  context,
}) => {
  await openList(page);
  await context.setOffline(true);
  await page.getByRole("button", { name: "Profile", exact: true }).click();
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await page.route("**/api/v1/logout", (route) => route.abort("failed"));
  await context.setOffline(false);
  await expect(
    page
      .getByText(/Server sign-out is pending|Server sign-out will finish/)
      .first(),
  ).toBeVisible();
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "Welcome back.", exact: true }),
  ).toBeVisible();
  expect(
    await page.evaluate(() =>
      localStorage.getItem("mychef.shopping.signed-out"),
    ),
  ).toBe("1");
  expect(
    await page.evaluate(() =>
      localStorage.getItem("mychef.shopping.logout-pending"),
    ),
  ).toMatch(/^[a-f0-9]{32}$/);
  await page.unroute("**/api/v1/logout");
  await page.getByRole("button", { name: "Retry server sign-out" }).click();
  await expect
    .poll(() =>
      page.evaluate(() =>
        localStorage.getItem("mychef.shopping.logout-pending"),
      ),
    )
    .toBe(null);
  expect(
    (await (await page.request.get("/api/v1/session")).json()).user,
  ).toBeNull();
});

test("an account mismatch suspends the original queue and never replays it for another owner", async ({
  page,
  context,
}) => {
  await openList(page);
  await context.setOffline(true);
  await openExtras(page);
  await page
    .getByLabel("Extra item", { exact: true })
    .fill("Original account only");
  await page.getByRole("button", { name: "Add extra", exact: true }).click();
  let shoppingWrites = 0;
  page.on("request", (r) => {
    if (r.method() === "PUT" && r.url().includes("/shopping/"))
      shoppingWrites++;
  });
  await page.route("**/api/v1/session", async (route) => {
    const response = await route.fetch(),
      data = await response.json();
    data.user = { ...data.user, id: "different-owner" };
    await route.fulfill({ response, json: data });
  });
  await context.setOffline(false);
  await expect(
    page.getByRole("heading", { name: "Welcome back.", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("Original account only", { exact: true }),
  ).toHaveCount(0);
  expect(shoppingWrites).toBe(0);
  expect(
    await page.evaluate(() =>
      Object.keys(localStorage)
        .filter((k) => k.startsWith("mychef.shopping.list."))
        .map((k) => JSON.parse(localStorage.getItem(k)!).queue.length),
    ),
  ).toEqual([1]);
  await page.unroute("**/api/v1/session");
});

test("offline reconnection follows real weekly replacement while preserving extras and selections", async ({
  page,
  context,
}) => {
  await openList(page);
  const session = await (await page.request.get("/api/v1/session")).json();
  const headers = {
    Origin: new URL(page.url()).origin,
    "X-CSRF-Token": session.csrfToken,
  };
  async function generate(dates: string[]) {
    const response = await page.request.post("/api/v1/generations", {
      headers: { ...headers, "Idempotency-Key": crypto.randomUUID() },
      data: {
        dates,
        adults: 2,
        children: 0,
        maxMinutes: 30,
        budget: "medium",
        region: "Belgium",
        creativity: 2,
        replaceExisting: true,
      },
    });
    expect(response.status()).toBe(202);
    const op = await response.json();
    await expect
      .poll(
        async () =>
          (
            await (
              await page.request.get(`/api/v1/generations/${op.id}`)
            ).json()
          ).status,
      )
      .toBe("succeeded");
  }
  await generate(["2026-11-02", "2026-11-03"]);
  await expect(page.getByLabel("Mark carrot as bought")).toBeVisible({
    timeout: 10_000,
  });
  const before = await (
    await page.request.get("/api/v1/shopping/2026-11-02")
  ).json();
  await extra(page, "Replacement soap");
  await context.setOffline(true);
  await page.getByLabel("Mark carrot as bought").click();
  await generate(["2026-11-02", "2026-11-04"]);
  await context.setOffline(false);
  await expect(page.getByText("Saved", { exact: true })).toBeVisible();
  await expect(page.getByText(/Your meals changed/)).toBeVisible();
  const after = await (
    await page.request.get("/api/v1/shopping/2026-11-02")
  ).json();
  expect(after.week).toBe(before.week);
  expect(after.meals[0].recipeId).not.toBe(before.meals[0].recipeId);
  expect(after.meals.map((m: { date: string }) => m.date)).toEqual([
    "2026-11-02",
    "2026-11-04",
  ]);
  expect(
    after.items.find((i: { name: string }) => i.name === "carrot").state,
  ).toBe("basket");
  expect(
    after.items.find((i: { name: string }) => i.name === "Replacement soap")
      .manual,
  ).toBe(true);
});

test("all cached weeks synchronize and removed-source recovery preserves later edits", async ({
  page,
  context,
}) => {
  await openList(page);
  await page.getByLabel("Next shopping week").click();
  await expect(page.getByText("Saved", { exact: true })).toBeVisible();
  await page.getByLabel("Previous shopping week").click();
  await expect(page.getByText("Saved", { exact: true })).toBeVisible();
  await context.setOffline(true);
  await openExtras(page);
  await page.getByLabel("Extra item", { exact: true }).fill("Week one coffee");
  await page.getByRole("button", { name: "Add extra", exact: true }).click();
  await page.getByLabel("Next shopping week").click();
  await openExtras(page);
  await page.getByLabel("Extra item", { exact: true }).fill("Week two tea");
  await page.getByRole("button", { name: "Add extra", exact: true }).click();
  await page.getByLabel("Previous shopping week").click();
  await page.getByRole("button", { name: "Profile", exact: true }).click();
  await context.setOffline(false);
  await expect
    .poll(
      async () =>
        (
          await (await page.request.get("/api/v1/shopping/2026-11-09")).json()
        ).items.some((i: { name: string }) => i.name === "Week two tea"),
      { timeout: 12_000 },
    )
    .toBe(true);
  await page
    .getByRole("button", { name: "Shopping list", exact: true })
    .click();
  await expect(page.getByText("Saved", { exact: true })).toBeVisible();
  expect(
    (
      await (await page.request.get("/api/v1/shopping/2026-11-02")).json()
    ).items.some((i: { name: string }) => i.name === "Week one coffee"),
  ).toBe(true);
  const session = await (await page.request.get("/api/v1/session")).json(),
    headers = {
      Origin: new URL(page.url()).origin,
      "X-CSRF-Token": session.csrfToken,
    };
  async function generate(dates: string[]) {
    const response = await page.request.post("/api/v1/generations", {
      headers: { ...headers, "Idempotency-Key": crypto.randomUUID() },
      data: {
        dates,
        adults: 2,
        children: 0,
        maxMinutes: 30,
        budget: "medium",
        region: "Belgium",
        creativity: 2,
        replaceExisting: true,
      },
    });
    expect(response.status()).toBe(202);
    const op = await response.json();
    await expect
      .poll(
        async () =>
          (
            await (
              await page.request.get(`/api/v1/generations/${op.id}`)
            ).json()
          ).status,
      )
      .toBe("succeeded");
  }
  await generate(["2026-11-02", "2026-11-03"]);
  await expect(page.getByLabel("Mark leek as bought")).toBeVisible({
    timeout: 10_000,
  });
  await context.setOffline(true);
  await page.getByLabel("Mark leek as bought").click();
  await openExtras(page);
  await page.getByLabel("Extra item", { exact: true }).fill("Keep this extra");
  await page.getByRole("button", { name: "Add extra", exact: true }).click();
  await generate(["2026-11-02"]);
  await context.setOffline(false);
  await page
    .getByRole("button", {
      name: "Keep removed items as extras and retry",
      exact: true,
    })
    .click();
  await expect(page.getByText("Saved", { exact: true })).toBeVisible();
  const recovered = await (
    await page.request.get("/api/v1/shopping/2026-11-02")
  ).json();
  expect(
    recovered.items.filter((i: { name: string }) => i.name === "leek"),
  ).toHaveLength(1);
  expect(
    recovered.items.find((i: { name: string }) => i.name === "leek"),
  ).toMatchObject({ manual: true, state: "basket" });
  expect(
    recovered.items.some((i: { name: string }) => i.name === "Keep this extra"),
  ).toBe(true);
  // A different week's conflict must identify the week that needs attention.
  await page.getByLabel("Next shopping week").click();
  await expect(page.getByText("Saved", { exact: true })).toBeVisible();
  await context.setOffline(true);
  await page.getByLabel("Mark Week two tea as bought").click();
  const remote = await (
    await page.request.get("/api/v1/shopping/2026-11-09")
  ).json();
  remote.items.find((i: { name: string }) => i.name === "Week two tea").state =
    "home";
  expect(
    (
      await page.request.put("/api/v1/shopping/2026-11-09", {
        headers,
        data: remote,
      })
    ).ok(),
  ).toBe(true);
  await page.getByLabel("Previous shopping week").click();
  await context.setOffline(false);
  await page
    .getByRole("button", { name: "Open week of 9 Nov 2026", exact: true })
    .click();
  await page
    .getByRole("button", {
      name: "Reviewed latest list · Retry my changes",
      exact: true,
    })
    .click();
  await expect(page.getByText("Saved", { exact: true })).toBeVisible();
  expect(
    (
      await (await page.request.get("/api/v1/shopping/2026-11-09")).json()
    ).items.find((i: { name: string }) => i.name === "Week two tea").state,
  ).toBe("basket");
});
