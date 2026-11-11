import { writeFileSync } from "node:fs";
import { expect, test, type Page } from "@playwright/test";
import {
  fixtureInboxRead,
  fixtureProof,
  registerFixture,
} from "./support/registration";
import { openExtras } from "./support/shopping";

const anonymous = { user: null, csrfToken: "mock-csrf" };
const proof = "a".repeat(43);
const password = "a fixture replacement password only";
const invalidGuidance =
  "This reset link is invalid or has expired. Request a new one.";

async function quietly<T>(action: () => Promise<T>): Promise<T> {
  try {
    return await action();
  } catch {
    throw new Error("A recovery fixture action failed.");
  }
}

async function openLink(page: Page, fragment = proof) {
  return quietly(() => page.goto("/app/reset-password#" + fragment));
}

async function fillReset(page: Page, confirm = password, value = password) {
  await quietly(async () => {
    await page.getByLabel("New password", { exact: true }).fill(value);
    await page
      .getByLabel("Confirm new password", { exact: true })
      .fill(confirm);
  });
}

test("sign-in offers a private request form with a generic acknowledgement", async ({
  page,
}) => {
  await page.route("**/api/v1/session", (route) =>
    route.fulfill({ json: anonymous }),
  );
  let submitted = false;
  await page.route("**/api/v1/password-recovery/request", async (route) => {
    submitted =
      route.request().headers()["x-csrf-token"] === anonymous.csrfToken &&
      Object.keys(route.request().postDataJSON()).join() === "email";
    await route.fulfill({ status: 202, json: { status: "accepted" } });
  });
  await page.goto("/app/");
  await page.getByRole("link", { name: "Forgot password?" }).click();
  await page.getByRole("heading", { name: "Reset your password" }).waitFor();
  await quietly(() =>
    page.getByLabel("Email", { exact: true }).fill("sample@example.test"),
  );
  await page.getByRole("button", { name: "Send reset link" }).click();
  await expect(page.getByRole("status")).toContainText("Check your inbox");
  expect(submitted).toBe(true);
  await page.getByRole("link", { name: "Back to sign in" }).click();
  await expect(
    page.getByRole("heading", { name: "Welcome back." }),
  ).toBeVisible();
});

test("reset scrubs before bootstrap, supports keyboard, and returns to sign-in", async ({
  page,
}) => {
  let sessionLookups = 0,
    completions = 0;
  let scrubbedAtBootstrap = false,
    contractOK = false;
  let entryNetworkSafe = false;
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (request.isNavigationRequest() && url.pathname === "/app/reset-password")
      entryNetworkSafe = !url.search && !url.hash;
  });
  await page.route("**/api/v1/session", async (route) => {
    sessionLookups++;
    scrubbedAtBootstrap ||=
      new URL(page.url()).pathname === "/app/reset-password" &&
      new URL(page.url()).hash === "";
    await route.fulfill({ json: anonymous });
  });
  await page.route("**/api/v1/password-recovery/complete", async (route) => {
    completions++;
    const body = route.request().postDataJSON();
    contractOK =
      body.proof === proof &&
      body.password === password &&
      route.request().headers()["x-csrf-token"] === anonymous.csrfToken;
    await route.fulfill({ json: { status: "reset" } });
  });
  const entry = await openLink(page);
  expect(entry?.headers()["cache-control"]).toBe("no-store");
  expect(entry?.headers()["referrer-policy"]).toBe("no-referrer");
  const reset = page.getByRole("button", { name: "Reset password" });
  await expect(reset).toBeVisible();
  expect(scrubbedAtBootstrap && entryNetworkSafe).toBe(true);
  const fields = page.locator("input[type=password]");
  for (const width of [320, 360, 390, 430]) {
    await page.setViewportSize({ width, height: 700 });
    await page.evaluate(() => {
      document.documentElement.style.fontSize = "200%";
    });
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(width);
    expect(
      await fields.evaluateAll((inputs) =>
        inputs.every(
          (field) =>
            parseFloat(getComputedStyle(field).fontSize) >= 18 &&
            field.getAttribute("autocomplete") === "new-password",
        ),
      ),
    ).toBe(true);
  }
  await fillReset(page, "a different confirmation");
  await reset.focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("alert")).toContainText("Passwords don’t match");
  expect(completions).toBe(0);
  await fillReset(page, "short", "short");
  await page.keyboard.press("Enter");
  await expect(page.getByRole("alert")).toContainText(
    "Use 10 to 128 characters",
  );
  expect(completions).toBe(0);
  await fillReset(page);
  await page.keyboard.press("Enter");
  await expect(page.getByRole("status")).toContainText("Password reset");
  await expect(fields).toHaveCount(0);
  expect(completions).toBe(1);
  expect(contractOK).toBe(true);
  expect(sessionLookups).toBe(1);
  const stored = await page.evaluate(() =>
    JSON.stringify({
      local: { ...localStorage },
      session: { ...sessionStorage },
      history: history.state,
    }),
  );
  expect(stored.includes(proof) || stored.includes(password)).toBe(false);
  await page.getByRole("link", { name: "Back to sign in" }).click();
  await expect(
    page.getByRole("heading", { name: "Welcome back." }),
  ).toBeVisible();
  await page.goBack();
  await expect(page.getByText(invalidGuidance)).toBeVisible();
  await page.goForward();
  await expect(
    page.getByRole("heading", { name: "Welcome back." }),
  ).toBeVisible();
  await page.goBack();
  await page.reload();
  await expect(page.getByText(invalidGuidance)).toBeVisible();
  expect(new URL(page.url()).hash).toBe("");
  expect(completions).toBe(1);
});

test("malformed links are scrubbed and a delayed reset cannot restore a closed view", async ({
  page,
}) => {
  await page.route("**/api/v1/session", (route) =>
    route.fulfill({ json: anonymous }),
  );
  await openLink(page, "malformed-fragment");
  await expect(page.getByText(invalidGuidance)).toBeVisible();
  expect(new URL(page.url()).hash).toBe("");
  let release!: () => void, reached!: () => void;
  const hold = new Promise<void>((resolve) => {
    release = resolve;
  });
  const submitted = new Promise<void>((resolve) => {
    reached = resolve;
  });
  await page.route("**/api/v1/password-recovery/complete", async (route) => {
    reached();
    await hold;
    await route.fulfill({ json: { status: "reset" } }).catch(() => {});
  });
  await openLink(page);
  try {
    await fillReset(page);
    await page.getByRole("button", { name: "Reset password" }).click();
    await submitted;
    await page.getByRole("link", { name: "Back to sign in" }).click();
    await expect(
      page.getByRole("heading", { name: "Welcome back." }),
    ).toBeVisible();
  } finally {
    release();
  }
  await page.goBack();
  await expect(page.getByText(invalidGuidance)).toBeVisible();
  await expect(page.getByRole("status")).toHaveCount(0);
});

test("a real reset signs out a second device and keeps its unsent shopping edits", async ({
  browser,
  page,
  context,
  baseURL,
}) => {
  const account = {
    email: "récupération@example.test",
    username: "recovery-browser",
    firstName: "Test",
    lastName: "Cook",
    password: "the fixture password before recovery",
  };
  const week = "2026-11-02";
  const listPath = "/api/v1/shopping/" + week;
  const shoppingPath = "/app/?view=shopping&week=" + week;
  const session = await registerFixture(page, account);
  await page.goto(shoppingPath);
  await expect(page.getByText("Saved", { exact: true })).toBeVisible();
  await expect
    .poll(() => page.evaluate(() => !!navigator.serviceWorker.controller))
    .toBe(true);

  const resetDevice = await browser.newContext({ baseURL });
  try {
    const resetPage = await resetDevice.newPage();
    await resetPage.goto("/app/");
    await resetPage.getByRole("link", { name: "Forgot password?" }).click();
    await quietly(() =>
      resetPage.getByLabel("Email", { exact: true }).fill(account.email),
    );
    await resetPage.getByRole("button", { name: "Send reset link" }).click();
    await expect(resetPage.getByRole("status")).toContainText(
      "Check your inbox",
    );
    const origin = new URL(page.url()).origin;
    const inbox = await resetDevice.newPage();
    const capability = process.env.MYCHEF_TEST_INBOX_CAPABILITY;
    if (!capability) throw new Error("Missing isolated inbox capability");
    await inbox.goto("/fixture/inbox");
    let recoveryProof = "";
    await expect
      .poll(async () => {
        await quietly(async () => {
          await inbox.getByLabel("Test inbox code").fill(capability);
          await inbox.getByLabel("Synthetic email").fill(account.email);
        });
        const posted = inbox.waitForResponse(
          (response) =>
            response.request().method() === "POST" &&
            new URL(response.url()).pathname === "/fixture/inbox",
        );
        await inbox
          .getByRole("button", { name: "Read the next message" })
          .click();
        const response = await posted;
        if (response.status() !== 200)
          throw new Error("The same-origin inbox form rejected the fixture.");
        const message = (await inbox.locator("pre").textContent()) ?? "";
        recoveryProof =
          /\/app\/reset-password#([A-Za-z0-9_-]{43})/.exec(message)?.[1] ?? "";
        return !!recoveryProof;
      })
      .toBe(true);
    await inbox.close();

    const stillSignedIn = await context.newPage();
    await openLink(stillSignedIn, recoveryProof);
    await expect(
      stillSignedIn.getByText("Sign out and reopen the link from your email."),
    ).toBeVisible();
    await expect(stillSignedIn.locator("input[type=password]")).toHaveCount(0);
    await stillSignedIn.close();

    await context.setOffline(true);
    await openExtras(page);
    await page
      .getByLabel("Extra item", { exact: true })
      .fill("Pending fixture oats");
    await page.getByRole("button", { name: "Add extra", exact: true }).click();
    await expect(
      page.getByText("Pending fixture oats", { exact: true }),
    ).toBeVisible();
    await expect(page.getByText(/Pending sync/).first()).toBeVisible();

    await openLink(resetPage, recoveryProof);
    await fillReset(resetPage);
    await resetPage.getByRole("button", { name: "Reset password" }).click();
    await expect(resetPage.getByRole("status")).toContainText(
      "Password reset.",
    );
    const after = await quietly(async () =>
      (await resetPage.request.get("/api/v1/session")).json(),
    );
    expect(after.user === null).toBe(true);
    await openLink(resetPage, recoveryProof);
    await fillReset(resetPage);
    await resetPage.getByRole("button", { name: "Reset password" }).click();
    await expect(resetPage.getByText(invalidGuidance)).toBeVisible();
    await expect
      .poll(async () => {
        const notice = await fixtureInboxRead(
          resetPage.request,
          origin,
          account.email,
        );
        if (notice.status() !== 200) return false;
        const { text } = await quietly(() => notice.json());
        return (
          /password( was| has been)? changed/i.test(text) &&
          !text.includes(recoveryProof)
        );
      })
      .toBe(true);

    const oldRead = await quietly(() => page.request.get(listPath));
    const oldWrite = await quietly(() =>
      page.request.put(listPath, {
        headers: { Origin: origin, "X-CSRF-Token": session.csrfToken },
        data: {},
      }),
    );
    expect(oldRead.status()).toBe(401);
    expect(oldWrite.status()).toBe(401);
    await expect(
      page.getByText("Pending fixture oats", { exact: true }),
    ).toBeVisible();
    await context.setOffline(false);
    await expect(
      page.getByRole("heading", { name: "Welcome back." }),
    ).toBeVisible();
    await expect(
      page.getByText("Pending fixture oats", { exact: true }),
    ).toHaveCount(0);

    const signIn = async (pass: string) => {
      await quietly(async () => {
        await page.getByLabel("Email", { exact: true }).fill(account.email);
        await page.getByLabel("Password", { exact: true }).fill(pass);
      });
      await page
        .getByRole("button", { name: "Sign in to MyChef", exact: true })
        .click();
    };
    await signIn(account.password);
    await expect(
      page.getByText("The email or password is incorrect."),
    ).toBeVisible();
    await signIn(password);
    await page
      .getByRole("button", { name: "Shopping list", exact: true })
      .click();
    // Sign-in selects the current week. Inspect the fixture's original list.
    await page.goto(shoppingPath);
    await expect(
      page.getByText("Pending fixture oats", { exact: true }),
    ).toBeVisible();
    await expect(page.getByText("Saved", { exact: true })).toBeVisible();
  } finally {
    await context.setOffline(false);
    await resetDevice.close();
  }
});

test("credential recovery failure probe", async ({
  page,
  browser,
  baseURL,
}) => {
  test.skip(
    process.env.MYCHEF_ARTIFACT_FAILURE !== "1",
    "explicit privacy probe",
  );
  const canary = process.env.MYCHEF_ARTIFACT_TEST_PASSWORD;
  const email = process.env.MYCHEF_ARTIFACT_TEST_EMAIL;
  const manifest = process.env.MYCHEF_ARTIFACT_PROOF_FILE;
  if (!canary || !email || !manifest)
    throw new Error("Missing recovery fixture canaries");
  await registerFixture(page, {
    email,
    username: "recovery-artifact",
    firstName: "Test",
    lastName: "Cook",
    password: "a prior password used only by the fixture",
  });
  const device = await browser.newContext({ baseURL });
  try {
    const other = await device.newPage();
    await other.goto("/app/");
    await other.getByRole("link", { name: "Forgot password?" }).click();
    await quietly(() => other.getByLabel("Email", { exact: true }).fill(email));
    await other.getByRole("button", { name: "Send reset link" }).click();
    await expect(other.getByRole("status")).toContainText("Check your inbox");
    const origin = new URL(other.url()).origin;
    const secret = await fixtureProof(
      other.request,
      origin,
      email,
      "reset-password",
    );
    // The scanner receives the exact canary over a private, one-run handoff
    // outside browser artifacts. It never needs to log the proof.
    writeFileSync(manifest, secret, { flag: "wx", mode: 0o600 });
    await openLink(other, secret);
    await fillReset(other, canary, canary);
    await other.getByRole("button", { name: "Reset password" }).click();
    await expect(other.getByRole("status")).toContainText("Password reset.");
    const keys = await other.evaluate(async () => {
      const items: string[] = [];
      for (const key of await caches.keys())
        for (const request of await (await caches.open(key)).keys())
          items.push(request.url);
      return JSON.stringify({
        items,
        local: { ...localStorage },
        session: { ...sessionStorage },
        history: history.state,
      });
    });
    if (
      keys.includes(secret) ||
      keys.includes(canary) ||
      keys.includes(email) ||
      keys.includes("/app/reset-password")
    )
      throw new Error(
        "Recovery left private data or an entry route in browser storage",
      );
    throw new Error("Injected recovery failure after reset");
  } finally {
    await device.close();
  }
});

test("a delayed reset response cannot show success in another signed-in session", async ({
  page,
  context,
  browser,
  baseURL,
}) => {
  const account = {
    email: "recovery-second-owner@example.test",
    username: "recovery-second-owner",
    firstName: "Other",
    lastName: "Cook",
    password: "an unrelated fixture account password",
  };
  const isolated = await browser.newContext({ baseURL });
  try {
    await registerFixture(await isolated.newPage(), account);
  } finally {
    await isolated.close();
  }
  let release!: () => void, reached!: () => void;
  const hold = new Promise<void>((resolve) => {
    release = resolve;
  });
  const submitted = new Promise<void>((resolve) => {
    reached = resolve;
  });
  await page.route("**/api/v1/password-recovery/complete", async (route) => {
    reached();
    await hold;
    await route.fulfill({ json: { status: "reset" } }).catch(() => {});
  });
  const other = await context.newPage();
  try {
    await openLink(page);
    await fillReset(page);
    await page.getByRole("button", { name: "Reset password" }).click();
    await submitted;
    await other.goto("/app/");
    await quietly(async () => {
      await other.getByLabel("Email", { exact: true }).fill(account.email);
      await other
        .getByLabel("Password", { exact: true })
        .fill(account.password);
    });
    await other.getByRole("button", { name: "Sign in to MyChef" }).click();
    await expect(
      other.getByRole("heading", { name: /Hello, Other/ }),
    ).toBeVisible();
    release();
    await expect(
      page.getByRole("heading", { name: /Hello, Other/ }),
    ).toBeVisible();
    await expect(
      page.getByText("Password reset.", { exact: true }),
    ).toHaveCount(0);
    const who = await quietly(async () =>
      (await other.request.get("/api/v1/session")).json(),
    );
    expect(who.user?.firstName).toBe("Other");
  } finally {
    release();
    await other.close();
  }
});
