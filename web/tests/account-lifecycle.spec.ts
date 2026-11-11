import { expect, test, type Page } from "@playwright/test";
import { spawnSync } from "node:child_process";
import { registerFixture, fixtureLogin } from "./support/registration";

const password = "A synthetic lifecycle sentence 2026!";
async function account(page: Page, name: string) {
  await registerFixture(page, {
    firstName: "Lifecycle",
    lastName: "Fixture",
    username: name,
    email: name + "@example.test",
    password,
  });
  await page.goto("/app/?view=profile");
  await page.getByText("Account details", { exact: true }).click();
}
const downloadButton = (page: Page) =>
  page.getByRole("button", { name: "Download my data", exact: true });
const deleteButton = (page: Page) =>
  page.getByRole("button", { name: "Delete my account", exact: true });
const modal = (page: Page, name: string) =>
  page.getByRole("dialog", { name, exact: true });

test("account actions fit phones and enlarged text with keyboard focus", async ({
  page,
}) => {
  await account(page, "lifecyclegeometry");
  await expect(downloadButton(page)).toBeVisible({ timeout: 1500 });
  for (const width of [320, 360, 390, 430]) {
    await page.setViewportSize({ width, height: 780 });
    for (const scale of ["100%", "200%"]) {
      await page.evaluate((value) => {
        document.documentElement.style.fontSize = value;
      }, scale);
      for (const name of ["Download my data", "Delete my account"]) {
        const opener =
          name === "Download my data"
            ? downloadButton(page)
            : deleteButton(page);
        await opener.click();
        const dialog = modal(page, name);
        await expect(dialog).toBeVisible();
        await expect(
          dialog.getByLabel("Current password", { exact: true }),
        ).toBeFocused();
        for (const field of await dialog.locator("input").all())
          expect(
            await field.evaluate((e) =>
              parseFloat(getComputedStyle(e).fontSize),
            ),
          ).toBeGreaterThanOrEqual(18);
        expect(
          await dialog.evaluate((e) => e.scrollWidth <= e.clientWidth + 1),
        ).toBe(true);
        expect(
          await page.evaluate(
            () => document.documentElement.scrollWidth <= innerWidth,
          ),
        ).toBe(true);
        await page.keyboard.press("Escape");
        await expect(dialog).not.toBeVisible();
        await expect(opener).toBeFocused();
      }
    }
  }
});

test("account export downloads a complete ZIP without retaining its content", async ({
  page,
}) => {
  await account(page, "lifecycleportable");
  const before = await page.evaluate(() =>
    JSON.stringify({
      local: { ...localStorage },
      session: { ...sessionStorage },
    }),
  );
  await downloadButton(page).click();
  const dialog = modal(page, "Download my data");
  await dialog.getByLabel("Current password", { exact: true }).fill("wrong");
  await dialog
    .getByRole("button", { name: "Download ZIP", exact: true })
    .click();
  await expect(dialog.getByRole("alert")).toContainText("current password");
  await expect(
    dialog.getByLabel("Current password", { exact: true }),
  ).toBeFocused();
  await dialog.getByLabel("Current password", { exact: true }).fill(password);
  const downloading = page.waitForEvent("download");
  await dialog
    .getByRole("button", { name: "Download ZIP", exact: true })
    .click();
  const file = await downloading;
  try {
    expect(
      /^mychef-data-[0-9a-f]{32}\.zip$/.test(file.suggestedFilename()),
    ).toBe(true);
    const stream = await file.createReadStream();
    if (!stream)
      throw new Error("The private download stream was unavailable.");
    const chunks: Buffer[] = [];
    for await (const chunk of stream) chunks.push(Buffer.from(chunk));
    const check = spawnSync(
      "python3",
      [
        "-c",
        `
import io,json,sys,zipfile
with zipfile.ZipFile(io.BytesIO(sys.stdin.buffer.read())) as z:
 names=z.namelist()
 data=b"".join(z.read(n) for n in names)
 print(json.dumps({"version":json.loads(z.read("manifest.json"))["version"],"guide":"README.txt" in names,"files":len(names),"personal":all(n in names for n in ("personal-recipes.json","personal-versions.json","personal-queue.json","personal-photos.json")),"noCredential":b"A synthetic lifecycle sentence 2026!" not in data,"owner":b"lifecycleportable" in data}))
`,
      ],
      { input: Buffer.concat(chunks), encoding: "utf8" },
    );
    if (check.status !== 0)
      throw new Error("The account ZIP failed offline validation.");
    const flags = JSON.parse(check.stdout);
    expect(flags.version).toBe(2);
    expect(flags.personal).toBe(true);
    expect(flags.guide).toBe(true);
    expect(flags.files).toBeGreaterThan(10);
    expect(flags.noCredential).toBe(true);
    expect(flags.owner).toBe(true);
  } finally {
    await file.delete();
  }
  await expect(dialog).not.toBeVisible();
  await expect(
    page.getByRole("region", { name: "Your account data" }).getByRole("status"),
  ).toContainText("download is ready");
  const after = await page.evaluate(() =>
    JSON.stringify({
      local: { ...localStorage },
      session: { ...sessionStorage },
    }),
  );
  expect(before === after).toBe(true);
  const session = await (await page.request.get("/api/v1/session")).json();
  let unexpected = 0;
  page.on("download", () => unexpected++);
  await page.route("**/api/v1/account/export", (route) =>
    route.fulfill({
      status: 200,
      headers: {
        "Content-Type": "application/zip",
        "Content-Disposition":
          'attachment; filename="mychef-data-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.zip"',
        "X-MyChef-User": session.user.id,
      },
      body: "PK incomplete",
    }),
  );
  await downloadButton(page).click();
  await dialog.getByLabel("Current password", { exact: true }).fill(password);
  await dialog
    .getByRole("button", { name: "Download ZIP", exact: true })
    .click();
  await expect(dialog.getByRole("alert")).toContainText(
    /incomplete|completed/i,
  );
  expect(unexpected).toBe(0);
  await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(downloadButton(page)).toBeFocused();
});

test("account deletion confirms once, clears this device and permits empty signup", async ({
  page,
  browser,
}) => {
  await account(page, "lifecycledeleted");
  await page.evaluate(() =>
    sessionStorage.setItem(
      "mychef.feedback.synthetic",
      JSON.stringify({ requestKey: "a".repeat(32), operation: "feedback" }),
    ),
  );
  await deleteButton(page).click();
  const dialog = modal(page, "Delete my account");
  await expect(dialog).toContainText(/cannot be undone/i);
  await expect(
    dialog.getByRole("button", { name: "Download my data first", exact: true }),
  ).toBeVisible();
  await dialog.getByLabel("Current password", { exact: true }).fill(password);
  await dialog
    .getByLabel("Type DELETE to confirm", { exact: true })
    .fill("delete");
  await dialog
    .getByRole("button", { name: "Permanently delete account", exact: true })
    .click();
  await expect(dialog.getByRole("alert")).toContainText("DELETE");
  await expect(
    dialog.getByLabel("Type DELETE to confirm", { exact: true }),
  ).toBeFocused();
  await dialog
    .getByLabel("Type DELETE to confirm", { exact: true })
    .fill("DELETE");
  await dialog.getByLabel("Current password", { exact: true }).fill("wrong");
  await dialog
    .getByRole("button", { name: "Permanently delete account", exact: true })
    .click();
  await expect(dialog.getByRole("alert")).toContainText("current password");
  await dialog.getByLabel("Current password", { exact: true }).fill(password);
  let posts = 0;
  await page.route("**/api/v1/account/deletion", (route) => {
    posts++;
    return route.continue();
  });
  await dialog
    .getByRole("button", { name: "Permanently delete account", exact: true })
    .evaluate((e) => {
      (e as HTMLButtonElement).click();
      (e as HTMLButtonElement).click();
    });
  await expect(page).toHaveURL(/^https?:\/\/[^/]+\/$/);
  await expect(
    page.getByRole("heading", { name: "MyChef" }),
  ).toBeVisible();
  await page.goBack();
  await expect(
    page.getByRole("heading", { name: "Welcome back." }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Profile", exact: true }),
  ).toHaveCount(0);
  expect(posts).toBe(1);
  expect(
    await page.evaluate(() =>
      Object.keys(localStorage).some(
        (k) =>
          k.startsWith("mychef.shopping.list.") ||
          k === "mychef.shopping.owner",
      ),
    ),
  ).toBe(false);
  expect(
    await page.evaluate(() =>
      Object.keys(sessionStorage).some((k) => k.startsWith("mychef.feedback.")),
    ),
  ).toBe(false);
  const initial = await (await page.request.get("/api/v1/session")).json();
  const origin = new URL(page.url()).origin;
  const oldLogin = await fixtureLogin(
    page.request,
    origin,
    initial.csrfToken,
    "lifecycledeleted@example.test",
    password,
  );
  expect(oldLogin.status()).toBe(401);
  const fresh = await browser.newContext();
  try {
    await registerFixture(await fresh.newPage(), {
      email: "lifecycledeleted@example.test",
      username: "lifecyclefresh",
      firstName: "Fresh",
      lastName: "Account",
      password,
    });
  } finally {
    await fresh.close();
  }
  await page
    .getByLabel("Email", { exact: true })
    .fill("lifecycledeleted@example.test");
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page
    .getByRole("button", { name: "Sign in to MyChef", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "Profile", exact: true }),
  ).toBeVisible();
  const favorites = await page.request.get("/api/v1/favorites");
  expect(favorites.status()).toBe(200);
  const value = await favorites.json();
  expect(
    Array.isArray(value)
      ? value.length
      : (value.recipes?.length ?? value.favorites?.length),
  ).toBe(0);
});

test("account instances separate stale offline data even when numeric IDs match", async ({
  page,
}) => {
  await account(page, "lifecyclecache");
  const session = await (await page.request.get("/api/v1/session")).json();
  const old = { ...session.user, accountInstance: "b".repeat(32) };
  const current = {
    ...session,
    user: { ...session.user, accountInstance: "a".repeat(32) },
  };
  const cacheKey = "mychef.shopping.list." + session.user.id + ".2099-01-05";
  await page.evaluate(
    ({ profile, key }) => {
      localStorage.setItem(
        "mychef.shopping.owner",
        JSON.stringify({ profile, week: "2099-01-05", verified: true }),
      );
      localStorage.setItem(
        key,
        JSON.stringify({ base: { synthetic: "old-account-only" }, queue: [] }),
      );
    },
    { profile: old, key: cacheKey },
  );
  await page.route("**/api/v1/session", (route) =>
    route.fulfill({ json: current }),
  );
  await page.goto("/app/");
  await expect
    .poll(() =>
      page.evaluate((key) => localStorage.getItem(key) === null, cacheKey),
    )
    .toBe(true);
  await page.evaluate(
    ({ profile, key }) => {
      localStorage.setItem(
        "mychef.shopping.owner",
        JSON.stringify({ profile, week: "2099-01-05", verified: true }),
      );
      localStorage.setItem(
        key,
        JSON.stringify({
          base: { synthetic: "same-account-pending" },
          queue: [],
        }),
      );
    },
    { profile: current.user, key: cacheKey },
  );
  await page.reload();
  await expect(
    page.getByRole("button", { name: "Meals", exact: true }),
  ).toBeVisible();
  expect(
    await page.evaluate((key) => localStorage.getItem(key) !== null, cacheKey),
  ).toBe(true);
  // Simulate restoring an earlier document after another document signed out.
  await page.evaluate(() => {
    localStorage.setItem("mychef.shopping.signed-out", "1");
    dispatchEvent(new PageTransitionEvent("pageshow", { persisted: true }));
  });
  await expect(
    page.getByRole("heading", { name: "Welcome back." }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Profile", exact: true }),
  ).toHaveCount(0);
});

test("export cancellation discards a late response and leaves navigation usable", async ({
  page,
}) => {
  await account(page, "lifecyclecancel");
  let ready!: () => void, release!: () => void, finished!: () => void;
  const started = new Promise<void>((r) => {
    ready = r;
  });
  const resume = new Promise<void>((r) => {
    release = r;
  });
  const done = new Promise<void>((r) => {
    finished = r;
  });
  let downloads = 0;
  page.on("download", () => downloads++);
  await page.route("**/api/v1/account/export", async (route) => {
    try {
      const response = await route.fetch().catch(() => {
        throw new Error("The isolated export request failed.");
      });
      ready();
      await resume;
      await route.fulfill({ response }).catch(() => undefined);
    } finally {
      finished();
    }
  });
  try {
    await downloadButton(page).click();
    const dialog = modal(page, "Download my data");
    await dialog.getByLabel("Current password", { exact: true }).fill(password);
    await dialog
      .getByRole("button", { name: "Download ZIP", exact: true })
      .click();
    await started;
    const aborted = page.waitForEvent("requestfailed", {
      predicate: (r) => r.url().endsWith("/api/v1/account/export"),
    });
    await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
    await aborted;
    release();
    await done;
    await page.getByRole("button", { name: "Meals", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: /Let’s plan your week/ }),
    ).toBeVisible();
    expect(downloads).toBe(0);
  } finally {
    release();
  }
});

test("restricted accounts can reach only verification and their own data actions", async ({
  page,
}) => {
  await account(page, "lifecyclerestricted");
  const session = await (await page.request.get("/api/v1/session")).json();
  await page.route("**/api/v1/session", (route) =>
    route.fulfill({
      json: {
        user: null,
        csrfToken: session.csrfToken,
        verificationRequired: true,
      },
    }),
  );
  await page.goto("/app/");
  await expect(downloadButton(page)).toBeVisible();
  await expect(deleteButton(page)).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Meals", exact: true }),
  ).toHaveCount(0);
  await deleteButton(page).click();
  const dialog = modal(page, "Delete my account");
  await expect(
    dialog.getByLabel("Current password", { exact: true }),
  ).toBeFocused();
  await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(deleteButton(page)).toBeFocused();
});
