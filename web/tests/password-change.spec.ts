import { expect, test, type BrowserContext, type Page } from "@playwright/test";
import { fixtureLogin, registerFixture } from "./support/registration";
import { openExtras } from "./support/shopping";

const shortNFC = "क़".repeat(5);
const longNFC = "e\u0301".repeat(128);
const field = {
  current: "Current password",
  next: "New password",
  confirmation: "Confirm new password",
};

test.setTimeout(120_000);

async function register(page: Page, name: string, password: string) {
  await registerFixture(page, {
    firstName: name,
    lastName: "Test",
    username: name,
    email: name + "@example.test",
    password,
  });
}

async function signIn(
  context: BrowserContext,
  origin: string,
  name: string,
  password: string,
) {
  const session = await (await context.request.get("/api/v1/session")).json();
  return fixtureLogin(
    context.request,
    origin,
    session.csrfToken,
    name + "@example.test",
    password,
  );
}

async function openPasswordForm(page: Page) {
  await page.getByRole("button", { name: "Profile", exact: true }).click();
  const details = page
    .locator(".profile-panel .account-details")
    .filter({ has: page.getByText("Account details", { exact: true }) });
  if ((await details.getAttribute("open")) === null)
    await page.getByText("Account details", { exact: true }).click();
  const summary = page.getByText("Change password", { exact: true });
  if (!(await page.getByLabel(field.current, { exact: true }).isVisible()))
    await summary.click();
}

async function fillChange(
  page: Page,
  current: string,
  next: string,
  confirmation = next,
) {
  await page.getByLabel(field.current, { exact: true }).fill(current);
  await page.getByLabel(field.next, { exact: true }).fill(next);
  await page.getByLabel(field.confirmation, { exact: true }).fill(confirmation);
}

const save = (page: Page) =>
  page.getByRole("button", { name: "Save password", exact: true });
const changed = (page: Page) =>
  page.getByText("Password changed.", { exact: false });
const cookie = async (context: BrowserContext) =>
  (await context.cookies()).find((c) => c.name === "mychef_session")?.value;

test("password form rotates the session, preserves offline edits and accepts NFC boundaries", async ({
  page,
  browser,
}) => {
  await page.setViewportSize({ width: 390, height: 650 });
  await register(page, "nfcshort", shortNFC);
  const origin = new URL(page.url()).origin;
  const current = await cookie(page.context());
  const before = await (await page.request.get("/api/v1/session")).json();
  const second = await browser.newContext({ baseURL: origin });
  try {
    expect((await signIn(second, origin, "nfcshort", shortNFC)).status()).toBe(
      200,
    );
    const shopping = await second.newPage();
    await shopping.goto("/app/?view=shopping&week=2026-11-02");
    await expect(shopping.getByText("Saved", { exact: true })).toBeVisible();
    await second.setOffline(true);
    await openExtras(shopping);
    await shopping
      .getByLabel("Extra item", { exact: true })
      .fill("Queued before password rotation");
    await shopping
      .getByRole("button", { name: "Add extra", exact: true })
      .click();
    await expect(shopping.getByText(/Pending sync/).first()).toBeVisible();

    await openPasswordForm(page);
    const profile = page.getByRole("heading", { name: "Your profile." });
    const identity = page.getByLabel("Sign-in email", { exact: true });
    await expect(identity).toBeVisible();
    await expect(identity).toHaveAttribute("autocomplete", "username");
    await expect(identity).toHaveJSProperty("readOnly", true);
    expect(await page.locator("form form").count()).toBe(0);
    const forms = await page.evaluate(() => {
      const user = document.querySelector<HTMLInputElement>(
        "input[name=firstName]",
      );
      const current = document.querySelector<HTMLInputElement>(
        "input[autocomplete='current-password']",
      );
      return !!user?.form && !!current?.form && user.form !== current.form;
    });
    expect(forms).toBe(true);
    await expect(
      page.getByLabel(field.current, { exact: true }),
    ).toHaveAttribute("autocomplete", "current-password");
    await expect(page.getByLabel(field.next, { exact: true })).toHaveAttribute(
      "autocomplete",
      "new-password",
    );
    await expect(
      page.getByLabel(field.confirmation, { exact: true }),
    ).toHaveAttribute("autocomplete", "new-password");
    let writes = 0;
    page.on("request", (r) => {
      if (r.url().endsWith("/api/v1/account/password")) writes++;
    });
    await fillChange(page, shortNFC, longNFC, "does not match");
    await save(page).click();
    await expect(page.getByRole("alert")).toHaveText(
      "The new passwords do not match.",
    );
    await expect(
      page.getByLabel(field.confirmation, { exact: true }),
    ).toHaveAttribute("aria-invalid", "true");
    await expect(
      page.getByLabel(field.confirmation, { exact: true }),
    ).toBeFocused();
    await expect(page.getByRole("alert")).toBeInViewport();
    await expect(profile).not.toBeInViewport();
    await fillChange(page, shortNFC, "too short");
    await save(page).click();
    await expect(page.getByRole("alert")).toHaveText(
      "Use 10 to 128 characters.",
    );
    await expect(page.getByLabel(field.next, { exact: true })).toBeFocused();
    await expect(page.getByRole("alert")).toBeInViewport();
    await expect(profile).not.toBeInViewport();
    expect(writes).toBe(0);

    await fillChange(page, shortNFC, longNFC);
    await save(page).click();
    await expect(changed(page)).toBeInViewport();
    await expect(profile).toBeFocused();
    await expect(profile).toBeInViewport();
    expect(writes).toBe(1);
    const hasPasswordInBrowserState = await page.evaluate(
      ([old, next]) => {
        const state = JSON.stringify({
          url: location.href,
          history: history.state,
          local: Object.entries(localStorage),
          session: Object.entries(sessionStorage),
        });
        return state.includes(old) || state.includes(next);
      },
      [shortNFC, longNFC],
    );
    expect(hasPasswordInBrowserState).toBe(false);
    await expect(page.locator(".password-change-form")).toHaveCount(0);
    for (const name of Object.values(field))
      await expect(page.getByLabel(name, { exact: true })).toHaveCount(0);
    const after = await (await page.request.get("/api/v1/session")).json();
    expect(
      after.user.id === before.user.id &&
        after.csrfToken !== before.csrfToken &&
        (await cookie(page.context())) !== current,
    ).toBe(true);
    await page.getByRole("button", { name: "Save preferences" }).click();
    await expect(
      page.getByText("Preferences saved.", { exact: true }),
    ).toBeVisible();

    await second.setOffline(false);
    await expect(
      shopping.getByRole("heading", { name: "Welcome back." }),
    ).toBeVisible();
    await expect(
      shopping.getByText("Queued before password rotation"),
    ).toHaveCount(0);
    const list = await (
      await page.request.get("/api/v1/shopping/2026-11-02")
    ).json();
    expect(
      list.items.some(
        (item: { name: string }) =>
          item.name === "Queued before password rotation",
      ),
    ).toBe(false);
    expect((await signIn(second, origin, "nfcshort", shortNFC)).status()).toBe(
      401,
    );
    expect((await signIn(second, origin, "nfcshort", longNFC)).status()).toBe(
      200,
    );
    await shopping.reload();
    await expect(
      shopping.getByText("Queued before password rotation"),
    ).toBeVisible();
    await expect(shopping.getByText("Saved", { exact: true })).toBeVisible();
  } finally {
    await second.close();
  }
});

const deferred = () => {
  let resolve!: () => void;
  const promise = new Promise<void>((done) => {
    resolve = done;
  });
  return { resolve, promise };
};

async function holdChangeResponse(page: Page) {
  const committed = deferred(),
    release = deferred();
  await page.route("**/api/v1/account/password", async (route) => {
    const r = route.request();
    // This request runs outside the browser's cookie jar. Only fulfill can
    // deliver Set-Cookie to the browser, so a held response really is late.
    const response = await fetch(r.url(), {
      method: r.method(),
      headers: await r.allHeaders(),
      body: r.postData(),
    });
    const body = await response.text();
    const headers = Object.fromEntries(response.headers);
    delete headers["content-length"];
    committed.resolve();
    await release.promise;
    await route.fulfill({ status: response.status, headers, body });
  });
  return { committed: committed.promise, release: release.resolve };
}

test("navigation and a later cross-tab sign-in never receive stale password-change success", async ({
  page,
  browser,
}) => {
  await register(page, "nfclong", longNFC);
  const origin = new URL(page.url()).origin;
  const spare = await browser.newContext({ baseURL: origin });
  try {
    await register(
      await spare.newPage(),
      "nextowner",
      "different test passphrase",
    );
  } finally {
    await spare.close();
  }
  await openPasswordForm(page);
  await fillChange(page, longNFC, shortNFC);
  let held = await holdChangeResponse(page);
  try {
    const before = await cookie(page.context());
    await save(page).click();
    await held.committed;
    expect((await cookie(page.context())) === before).toBe(true);
    await page.getByRole("button", { name: "Meals", exact: true }).click();
    const response = page.waitForResponse("**/api/v1/account/password");
    held.release();
    expect((await response).status()).toBe(200);
    await expect(
      page.getByRole("heading", { name: /Hello, nfclong/ }),
    ).toBeVisible();
    await openPasswordForm(page);
    await expect(changed(page)).toHaveCount(0);
    await expect(page.getByLabel(field.next, { exact: true })).toHaveValue("");
    await page.unroute("**/api/v1/account/password");
  } finally {
    held.release();
  }

  const second = await page.context().newPage();
  await second.goto("/app/?view=profile");
  await expect(
    second.getByRole("button", { name: "Sign out", exact: true }),
  ).toBeVisible();
  await fillChange(page, shortNFC, "new test passphrase");
  held = await holdChangeResponse(page);
  try {
    await save(page).click();
    await held.committed;
    await second.getByRole("button", { name: "Sign out", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "Welcome back." }),
    ).toBeVisible();
    await second
      .getByLabel("Email", { exact: true })
      .fill("nextowner@example.test");
    await second
      .getByLabel("Password", { exact: true })
      .fill("different test passphrase");
    await second
      .getByRole("button", { name: "Sign in to MyChef", exact: true })
      .click();
    const previous = await cookie(page.context());
    held.release();
    await expect(
      second.getByRole("heading", { name: /Hello, nextowner/ }),
    ).toBeVisible();
    await expect(changed(page)).toHaveCount(0);
    expect((await cookie(page.context())) !== previous).toBe(true);
    const session = await (await second.request.get("/api/v1/session")).json();
    expect(session.user.firstName).toBe("nextowner");

    // The same tab can also sign out before its own late rotation arrives.
    await openPasswordForm(second);
    await fillChange(
      second,
      "different test passphrase",
      "another test passphrase",
    );
    const sameTab = await holdChangeResponse(second);
    try {
      await save(second).click();
      await sameTab.committed;
      await second
        .getByRole("button", { name: "Sign out", exact: true })
        .click();
      await second
        .getByLabel("Email", { exact: true })
        .fill("nfclong@example.test");
      await second
        .getByLabel("Password", { exact: true })
        .fill("new test passphrase");
      await second
        .getByRole("button", { name: "Sign in to MyChef", exact: true })
        .click();
      sameTab.release();
      await expect(
        second.getByRole("heading", { name: /Hello, nfclong/ }),
      ).toBeVisible();
      await expect(changed(second)).toHaveCount(0);
      const current = await (
        await second.request.get("/api/v1/session")
      ).json();
      expect(current.user.firstName).toBe("nfclong");
    } finally {
      sameTab.release();
    }
  } finally {
    held.release();
    await second.close();
  }
});

test("password disclosure is keyboard usable at phone widths and requires a secure supported browser", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 800 });
  await page.goto("/app/");
  for (const name of ["Email", "Password"]) {
    expect(
      await page
        .getByLabel(name, { exact: true })
        .evaluate((input) => parseFloat(getComputedStyle(input).fontSize)),
    ).toBeGreaterThanOrEqual(18);
  }
  await register(page, "passwordlayout", "layout test passphrase");
  await page.getByRole("button", { name: "Profile", exact: true }).click();
  await page.getByText("Account details", { exact: true }).click();
  const summary = page.getByText("Change password", { exact: true });
  await summary.focus();
  await page.keyboard.press("Space");
  await page.keyboard.press("Tab");
  await expect(page.getByLabel(field.current, { exact: true })).toBeFocused();
  for (const width of [320, 360, 390, 430]) {
    await page.setViewportSize({ width, height: 800 });
    for (const size of ["100%", "200%"]) {
      await page.evaluate((size) => {
        document.documentElement.style.fontSize = size;
      }, size);
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBe(true);
      for (const name of [
        field.current,
        "Diet",
        "Other allergens",
        "Disliked ingredients",
        "Cooking techniques",
        "Cooking effort",
      ]) {
        expect(
          await page
            .getByLabel(name, { exact: true })
            .evaluate((input) => parseFloat(getComputedStyle(input).fontSize)),
          `${name} at ${width}px and ${size} text`,
        ).toBeGreaterThanOrEqual(18);
      }
    }
  }
  await page.addInitScript(() =>
    Object.defineProperty(navigator, "locks", { value: undefined }),
  );
  await page.reload();
  await page.getByText("Account details", { exact: true }).click();
  await page.getByText("Change password", { exact: true }).click();
  await expect(
    page.getByText(/supported secure browser to change your password/),
  ).toBeVisible();
  await expect(page.getByLabel(field.current, { exact: true })).toHaveCount(0);
});

test("credential artifact failure probe", async ({ page }) => {
  test.skip(
    process.env.MYCHEF_ARTIFACT_FAILURE !== "1",
    "explicit privacy probe",
  );
  const canary = process.env.MYCHEF_ARTIFACT_TEST_PASSWORD;
  if (!canary) throw new Error("Missing synthetic credential canary");
  await register(page, "artifactprobe", canary);
  throw new Error("Injected failure after credential submission");
});
