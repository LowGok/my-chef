import { expect, test } from "@playwright/test";
import {
  fixtureInboxRead,
  fixtureProof,
  registerFixture,
  signInFixture,
} from "./support/registration";

const anonymous = { user: null, csrfToken: "test-csrf" };

test("sign-up collects profile fields before any password", async ({
  page,
}) => {
  await page.route("**/api/v1/session", (route) =>
    route.fulfill({ json: anonymous }),
  );
  await page.route("**/api/v1/register", async (route) => {
    expect(route.request().postDataJSON()).toEqual({
      firstName: "Test",
      lastName: "Cook",
      username: "fixture-cook",
      email: "cook@example.test",
    });
    await route.fulfill({ status: 202, json: { status: "accepted" } });
  });
  await page.goto("/app/");
  await page.getByRole("button", { name: "Create an account" }).click();
  await expect(page.getByLabel("Password")).toHaveCount(0);
  await page.getByLabel("First name").fill("Test");
  await page.getByLabel("Last name").fill("Cook");
  await page.getByLabel("Username", { exact: true }).fill("fixture-cook");
  await page.getByLabel("Email").fill("cook@example.test");
  await page.getByRole("button", { name: "Create my account" }).click();
  await expect(page.getByRole("status")).toContainText("Check your inbox");
});

test("proof view clears the fragment and waits for a decision", async ({
  page,
}) => {
  const proof = "a".repeat(43); // A mock only; no real link is used.
  await page.route("**/api/v1/session", (route) =>
    route.fulfill({ json: anonymous }),
  );
  await page.route("**/api/v1/email/change/inspect", async (route) => {
    expect(new URL(page.url()).hash).toBe("");
    expect(route.request().postDataJSON()).toEqual({ proof });
    await route.fulfill({ json: { action: "confirm" } });
  });
  let decisions = 0;
  await page.route("**/api/v1/email/change/decision", async (route) => {
    decisions++;
    await route.fulfill({ json: { status: "changed" } });
  });
  const shell = await page.goto("/app/change-email#" + proof);
  expect(shell?.headers()["cache-control"]).toBe("no-store");
  expect(shell?.headers()["referrer-policy"]).toBe("no-referrer");
  const confirm = page.getByRole("button", { name: "Confirm email address" });
  await expect(confirm).toBeVisible();
  expect(decisions).toBe(0);
  for (const width of [320, 360, 390, 430]) {
    await page.setViewportSize({ width, height: 700 });
    await page.evaluate(() => {
      document.documentElement.style.fontSize = "200%";
    });
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(width);
  }
  await confirm.focus();
  await expect(confirm).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("status")).toContainText("Email address updated");
  expect(decisions).toBe(1);
  expect(
    await page.evaluate(() =>
      JSON.stringify({
        local: { ...localStorage },
        session: { ...sessionStorage },
        history: history.state,
      }),
    ),
  ).not.toContain(proof);
  await page.reload();
  await expect(page.getByText("Open the link from your email")).toBeVisible();
});

test("an upgraded app never opens a pre-verification cache while offline", async ({
  page,
}) => {
  await page.addInitScript(() => {
    localStorage.setItem(
      "mychef.shopping.owner",
      JSON.stringify({
        profile: { id: "old-account", firstName: "Private" },
        week: "2026-11-02",
      }),
    );
    localStorage.setItem(
      "mychef.shopping.list.old-account.2026-11-02",
      JSON.stringify({
        base: { week: "2026-11-02", items: [] },
        queue: [{ id: "pending-edit" }],
      }),
    );
  });
  await page.route("**/api/v1/session", (route) => route.abort());
  await page.goto("/app/?view=shopping&week=2026-11-02");
  await expect(
    page.getByRole("heading", { name: "Welcome back." }),
  ).toBeVisible();
  await expect(page.getByRole("navigation")).toHaveCount(0);
  expect(
    await page.evaluate(() =>
      localStorage.getItem("mychef.shopping.list.old-account.2026-11-02"),
    ),
  ).toContain("pending-edit");
});

test("restricted account cannot open private UI and can request a new email", async ({
  page,
}) => {
  await page.route("**/api/v1/session", (route) =>
    route.fulfill({ json: { ...anonymous, verificationRequired: true } }),
  );
  const attempted: string[] = [];
  page.on("request", (request) => {
    const path = new URL(request.url()).pathname;
    if (path.startsWith("/api/v1/")) attempted.push(path);
  });
  await page.route("**/api/v1/email/verification/resend", (route) =>
    route.fulfill({ status: 202, json: { status: "accepted" } }),
  );
  await page.goto("/app/?view=profile");
  await expect(
    page.getByRole("heading", { name: "Verify your email" }),
  ).toBeVisible();
  await expect(page.getByRole("navigation")).toHaveCount(0);
  await page.getByRole("button", { name: "Send verification email" }).click();
  await expect(page.getByRole("status")).toContainText(
    "Verification email requested",
  );
  await page.getByText("Correct your email address", { exact: true }).click();
  await expect(page.getByLabel("Current password")).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Contact support" }),
  ).toBeVisible();
  expect(
    attempted.every((path) =>
      ["/api/v1/session", "/api/v1/email/verification/resend"].includes(path),
    ),
  ).toBe(true);
});

test("leaving a proof page drops an inspection response that arrives later", async ({
  page,
}) => {
  await page.route("**/api/v1/session", (route) =>
    route.fulfill({ json: anonymous }),
  );
  let finish!: () => void;
  const held = new Promise<void>((resolve) => {
    finish = resolve;
  });
  await page.route("**/api/v1/signup/inspect", async (route) => {
    await held;
    await route
      .fulfill({
        json: { firstName: "Old view", lastName: "Cook", username: "old-view" },
      })
      .catch(() => {});
  });
  await page.goto("/app/signup#" + "b".repeat(43));
  try {
    await expect(page.getByRole("status")).toContainText("Checking the link");
    await page.getByRole("button", { name: "Back to MyChef" }).click();
    finish();
    await expect(
      page.getByRole("heading", { name: "Welcome back." }),
    ).toBeVisible();
    await expect(page.getByText("Old view")).toHaveCount(0);
    await page.goBack();
    await expect(page.getByText("Open the link from your email")).toBeVisible();
  } finally {
    finish();
  }
});

test("mailbox owner creates and signs in to an account with a Unicode email", async ({
  page,
  browser,
  request,
  baseURL,
}) => {
  const email = "Straße+Test@Example.Test";
  const signIn = "a mailbox-selected passphrase";
  const context = await browser.newContext({ baseURL });
  try {
    await page.goto("/app/");
    await page.getByRole("button", { name: "Create an account" }).click();
    await page.getByLabel("First name").fill("Proposed");
    await page.getByLabel("Last name").fill("Cook");
    await page.getByLabel("Username").fill("proof-browser");
    await page.getByLabel("Email").fill(email);
    await page.getByRole("button", { name: "Create my account" }).click();
    await expect(page.getByRole("status")).toContainText("Check your inbox");

    const origin = new URL(page.url()).origin;
    const denied = await fixtureInboxRead(request, origin, email, "");
    expect(denied.status()).toBe(404);
    const fragment = await fixtureProof(request, origin, email, "signup");
    expect((await fixtureInboxRead(request, origin, email)).status()).toBe(204);
    const owner = await context.newPage();
    await owner.goto("/app/signup");
    await owner.evaluate((value) => {
      location.hash = value;
    }, fragment);
    await expect(owner.getByLabel("First name")).toHaveValue("Proposed");
    await owner.getByLabel("First name").fill("Mailbox owner");
    await owner.getByLabel("Password", { exact: true }).fill(signIn);
    await owner.getByLabel("Confirm password", { exact: true }).fill(signIn);
    await owner.getByRole("button", { name: "Create account" }).click();
    await expect(owner.getByRole("status")).toContainText("Account created");
    expect(new URL(owner.url()).hash).toBe("");
    await owner.getByRole("button", { name: "Back to MyChef" }).click();
    await owner.getByLabel("Email").fill(email.toLowerCase());
    await owner.getByLabel("Password", { exact: true }).fill(signIn);
    await owner.getByRole("button", { name: "Sign in to MyChef" }).click();
    await expect(
      owner.getByRole("heading", { name: /Hello, Mailbox owner/ }),
    ).toBeVisible();
  } finally {
    await context.close();
  }
});

test("the current address stays active until both test inboxes approve", async ({
  page,
  browser,
  baseURL,
}) => {
  const previous = "change-start@example.test";
  const next = "Forêt+Change@Example.Test";
  const password = "email change fixture passphrase";
  const owner = await registerFixture(page, {
    firstName: "Change",
    lastName: "Fixture",
    username: "change-fixture",
    email: previous,
    password,
  });
  const origin = new URL(page.url()).origin;
  await page.getByRole("button", { name: "Profile", exact: true }).click();
  await page.getByText("Account details", { exact: true }).click();
  await page.getByText("Change email address", { exact: true }).click();
  const form = page.locator(".email-change-form");
  await form.getByLabel("New email address").fill(next);
  await form.getByLabel("Current password").fill(password);
  await form.getByRole("button", { name: "Send confirmation" }).click();
  await expect(form.getByRole("status")).toContainText("Check both inboxes");

  const oldProof = await fixtureProof(
    page.request,
    origin,
    previous,
    "change-email",
  );
  const newProof = await fixtureProof(
    page.request,
    origin,
    next,
    "change-email",
  );
  const separate = await browser.newContext({ baseURL });
  try {
    const oldInbox = await separate.newPage();
    await oldInbox.goto("/app/change-email");
    await oldInbox.evaluate((value) => {
      location.hash = value;
    }, oldProof);
    await oldInbox
      .getByRole("button", { name: "Approve email change" })
      .click();
    await expect(oldInbox.getByRole("status")).toContainText(
      "other inbox still needs",
    );
    const before = await (await page.request.get("/api/v1/session")).json();
    expect(before.user.email === previous).toBe(true);

    await page.goto("/app/change-email");
    await page.evaluate((value) => {
      location.hash = value;
    }, newProof);
    await page.getByRole("button", { name: "Confirm email address" }).click();
    await expect(page.getByRole("status")).toContainText(
      "Email address updated",
    );
    const revoked = await (await page.request.get("/api/v1/session")).json();
    expect(revoked.user).toBeNull();
    await page.getByRole("button", { name: "Back to MyChef" }).click();
    const active = await signInFixture(page, next, password);
    expect(active.user.id).toBe(owner.user.id);
    expect(active.user.email === next).toBe(true);
  } finally {
    await separate.close();
  }
});

test("credential transport failure probe", async ({ request }) => {
  test.skip(
    process.env.MYCHEF_ARTIFACT_FAILURE !== "1",
    "explicit privacy probe",
  );
  await fixtureProof(
    request,
    "http://127.0.0.1:0",
    "unreachable@example.test",
    "signup",
  );
  throw new Error("The unreachable fixture transport unexpectedly succeeded.");
});
