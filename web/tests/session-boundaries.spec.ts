import {
  expect,
  test,
  type Page,
  type Route,
  type APIResponse,
} from "@playwright/test";
import { registerFixture } from "./support/registration";

test.setTimeout(120_000);

async function register(page: Page, name: string) {
  await registerFixture(page, {
    firstName: name,
    lastName: "Chef",
    username: "race-" + name,
    email: "race-" + name + "@example.test",
    password: "ChefTest2026!",
  });
}
async function login(page: Page, name: string) {
  await page
    .getByLabel("Email", { exact: true })
    .fill(`race-${name}@example.test`);
  await page.getByLabel("Password", { exact: true }).fill("ChefTest2026!");
  const button = page.getByRole("button", {
    name: "Sign in to MyChef",
    exact: true,
  });
  const deadline = Date.now() + 65_000;
  for (;;) {
    const submitted = page.waitForResponse(
      (response) =>
        new URL(response.url()).pathname === "/api/v1/login" &&
        response.request().method() === "POST",
    );
    await button.click();
    const response = await submitted;
    if (response.status() !== 429) {
      expect(response.status()).toBe(200);
      break;
    }
    const seconds = Number(response.headers()["retry-after"]);
    const delay = seconds * 1_000;
    expect(
      Number.isFinite(seconds) &&
        seconds >= 1 &&
        Date.now() + delay <= deadline,
      "The shared fixture login limiter must provide a bounded Retry-After.",
    ).toBe(true);
    await new Promise<void>((resolve) => setTimeout(resolve, delay));
  }
  await expect(
    page.getByRole("button", { name: "Profile", exact: true }),
  ).toBeVisible();
}
async function reply(route: Route, response: APIResponse, body: unknown) {
  const headers = { ...response.headers() };
  delete headers["content-length"];
  await route.fulfill({
    status: response.status(),
    headers,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
}
const deferred = () => {
  let resolve!: () => void;
  const promise = new Promise<void>((done) => {
    resolve = done;
  });
  return { promise, resolve };
};

test("late private responses and revoked sessions cannot restore the previous account", async ({
  page,
  browser,
}) => {
  await register(page, "Alice");
  const alice = (await (await page.request.get("/api/v1/session")).json()).user
    .id;
  const other = await browser.newContext({
    baseURL: new URL(page.url()).origin,
  });
  const bob = await other.newPage();
  await register(bob, "Bob");
  await other.close();
  await page.getByRole("button", { name: "Profile", exact: true }).click();
  const profileSaved = deferred(),
    releaseProfile = deferred(),
    profileDelivered = deferred();
  await page.route("**/api/v1/profile", async (route) => {
    if (route.request().method() !== "PUT") {
      await route.continue();
      return;
    }
    const response = await route.fetch();
    profileSaved.resolve();
    await releaseProfile.promise;
    await route.fulfill({ response });
    profileDelivered.resolve();
  });
  await page.getByText("Account details", { exact: true }).click();
  await page.getByLabel("First name", { exact: true }).fill("Private Alice");
  await page.getByRole("button", { name: "Save preferences" }).click();
  await profileSaved.promise;
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await login(page, "Bob");
  const lateProfile = page.waitForResponse(
    (r) =>
      r.url().endsWith("/api/v1/profile") && r.request().method() === "PUT",
  );
  releaseProfile.resolve();
  expect((await lateProfile).headers()["x-mychef-user"]).toBe(alice);
  await profileDelivered.promise;
  await page.evaluate(
    () =>
      new Promise<void>((done) =>
        requestAnimationFrame(() => requestAnimationFrame(() => done())),
      ),
  );
  await page.unroute("**/api/v1/profile");
  await page.getByRole("button", { name: "Profile", exact: true }).click();
  await page.getByText("Account details", { exact: true }).click();
  await expect(page.getByLabel("First name", { exact: true })).toHaveValue(
    "Bob",
  );
  await expect(page.getByText("Private Alice", { exact: true })).toHaveCount(0);
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await login(page, "Alice");
  const shoppingReceived = deferred(),
    releaseShopping = deferred(),
    shoppingDelivered = deferred();
  await page.route("**/api/v1/shopping/*", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    shoppingReceived.resolve();
    await releaseShopping.promise;
    body.items.push({
      id: "late-private-extra",
      name: "Delayed Alice shopping extra",
      quantity: null,
      unit: "",
      category: "Household extras",
      state: "buy",
      manual: true,
      updated: false,
      contributions: [],
    });
    await reply(route, response, body);
    shoppingDelivered.resolve();
  });
  await page
    .getByRole("button", { name: "Shopping list", exact: true })
    .click();
  await shoppingReceived.promise;
  await page.getByRole("button", { name: "Profile", exact: true }).click();
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await login(page, "Bob");
  releaseShopping.resolve();
  await shoppingDelivered.promise;
  await page.unroute("**/api/v1/shopping/*");
  await expect(
    page.getByText("Delayed Alice shopping extra", { exact: true }),
  ).toHaveCount(0);
  expect(await page.evaluate(() => JSON.stringify(localStorage))).not.toContain(
    "Delayed Alice shopping extra",
  );
  await page.getByRole("button", { name: "Profile", exact: true }).click();
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await login(page, "Alice");
  const paginationReceived = deferred(),
    releasePagination = deferred(),
    paginationDelivered = deferred();
  const privateRecipe = {
    id: "9007199254740995",
    menuId: null,
    date: "2026-10-27",
    title: "Private Alice row",
    note: null,
    yield: null,
    equipment: null,
    instructions: null,
    tips: null,
    allergens: null,
    allergenNote: null,
    prepMinutes: null,
    cookMinutes: null,
    totalMinutes: null,
    ingredients: [],
    imageUrl: null,
    imageStatus: "unavailable",
    legacyRaw: null,
  };
  await page.route("**/api/v1/menus?*", async (route) => {
    const response = await route.fetch();
    if (response.headers()["x-mychef-user"] !== alice) {
      await route.fulfill({ response });
      return;
    }
    if (new URL(route.request().url()).searchParams.has("offset")) {
      paginationReceived.resolve();
      await releasePagination.promise;
      await reply(route, response, {
        recipes: [
          {
            ...privateRecipe,
            id: "9007199254740996",
            title: "Delayed private Alice row",
          },
        ],
        nextOffset: null,
      });
      paginationDelivered.resolve();
    } else
      await reply(route, response, {
        recipes: [privateRecipe],
        nextOffset: 25,
      });
  });
  await page.getByRole("button", { name: "Meals", exact: true }).click();
  await page.getByRole("button", { name: "Load more recipes" }).click();
  await paginationReceived.promise;
  await page.getByRole("button", { name: "Profile", exact: true }).click();
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await page.route(
    "**/api/v1/login",
    (route) =>
      route.fulfill({
        status: 429,
        headers: { "Retry-After": "1" },
        json: {
          error: { code: "rate_limited", message: "Too many attempts." },
        },
      }),
    { times: 1 },
  );
  await login(page, "Bob");
  const latePage = page.waitForResponse(
    (r) =>
      new URL(r.url()).pathname === "/api/v1/menus" &&
      new URL(r.url()).searchParams.has("offset"),
  );
  releasePagination.resolve();
  expect((await latePage).headers()["x-mychef-user"]).toBe(alice);
  await paginationDelivered.promise;
  await page.evaluate(
    () =>
      new Promise<void>((done) =>
        requestAnimationFrame(() => requestAnimationFrame(() => done())),
      ),
  );
  await page.unroute("**/api/v1/menus?*");
  await expect(page.getByRole("heading", { name: /Hello, Bob/ })).toBeVisible();
  await expect(
    page.getByText("Delayed private Alice row", { exact: true }),
  ).toHaveCount(0);
  await page.getByRole("button", { name: "Profile", exact: true }).click();
  async function revoke() {
    const session = await (await page.request.get("/api/v1/session")).json();
    const response = await page.request.post("/api/v1/logout", {
      headers: {
        Origin: new URL(page.url()).origin,
        "X-CSRF-Token": session.csrfToken,
      },
      data: {},
    });
    expect(response.status()).toBe(204);
  }
  await revoke();
  await page.getByRole("button", { name: "Save preferences" }).click();
  await expect(
    page.getByRole("button", { name: "Sign in to MyChef", exact: true }),
  ).toBeVisible();
  await login(page, "Bob");
  await page.getByRole("button", { name: "Profile", exact: true }).click();
  await revoke();
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Sign in to MyChef", exact: true }),
  ).toBeVisible();
});

test("a duplicate sign-out in another tab cannot revoke a newer sign-in", async ({
  page,
  browser,
}) => {
  await register(page, "FirstTab");
  const other = await browser.newContext({
    baseURL: new URL(page.url()).origin,
  });
  await register(await other.newPage(), "NextTab");
  await other.close();

  const second = await page.context().newPage();
  await second.goto("/app/");
  await expect(
    second.getByRole("heading", { name: /Hello, FirstTab/ }),
  ).toBeVisible();
  await second.evaluate(() => {
    const locks = navigator.locks;
    const acquire = locks.request.bind(locks);
    let held = false;
    Object.defineProperty(locks, "request", {
      configurable: true,
      async value(...args: unknown[]) {
        if (!held) {
          held = true;
          await new Promise<void>((resolve) => {
            window.addEventListener("release-test-lock", () => resolve(), {
              once: true,
            });
            document.documentElement.dataset.logoutLock = "pending";
          });
          const result = await Reflect.apply(acquire, undefined, args);
          document.documentElement.dataset.logoutLock = "settled";
          return result;
        }
        return Reflect.apply(acquire, undefined, args);
      },
    });
  });
  let lateRequests = 0;
  await second.route("**/api/v1/logout", async (route) => {
    lateRequests++;
    await route.continue();
  });
  const received = deferred(),
    release = deferred();
  await page.route("**/api/v1/logout", async (route) => {
    received.resolve();
    await release.promise;
    await route.continue();
  });
  try {
    await page.getByRole("button", { name: "Profile", exact: true }).click();
    await page.getByRole("button", { name: "Sign out", exact: true }).click();
    await received.promise;
    await expect(
      second.getByRole("button", { name: "Sign in to MyChef" }),
    ).toBeVisible();
    await expect(second.locator("html")).toHaveAttribute(
      "data-logout-lock",
      "pending",
    );
    release.resolve();
    await login(page, "NextTab");
    const before = await page.request.get("/api/v1/session");
    expect((await before.json()).user.firstName).toBe("NextTab");

    await second.evaluate(() =>
      window.dispatchEvent(new Event("release-test-lock")),
    );
    await expect(second.locator("html")).toHaveAttribute(
      "data-logout-lock",
      "settled",
    );
    expect(lateRequests).toBe(0);
    const after = await page.request.get("/api/v1/session");
    expect((await after.json()).user.firstName).toBe("NextTab");
  } finally {
    release.resolve();
    await second
      .evaluate(() => window.dispatchEvent(new Event("release-test-lock")))
      .catch(() => {});
    await second.close();
  }
});
