import { expect, test } from "@playwright/test";
import { randomBytes } from "node:crypto";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { addDays, monday, today } from "../src/dates";
import { shoppingAccount, openExtras } from "./support/shopping";

test("shopping state survives an actual backend process restart", async ({
  browser,
}) => {
  const directory = mkdtempSync(join(tmpdir(), "mychef-shopping-restart-")),
    root = resolve(".."),
    tools = join(root, ".test-artifacts/rewrite-tools"),
    db = join(directory, "test.db");
  const migrated = Bun.spawn(
    [join(tools, "mychef-migrate"), "up", "--db", db],
    { stdout: "ignore", stderr: "pipe" },
  );
  expect(await migrated.exited).toBe(0);
  const origin = "http://127.0.0.1:18139";
  const capability = randomBytes(32).toString("base64url");
  const inboxFile = join(directory, "inbox.json"),
    keyFile = join(directory, "keys.json");
  writeFileSync(
    inboxFile,
    JSON.stringify({
      capability,
      expires: new Date(Date.now() + 15 * 60_000).toISOString(),
    }),
    { mode: 0o600 },
  );
  writeFileSync(
    keyFile,
    JSON.stringify({
      active: "fixture",
      keys: { fixture: randomBytes(32).toString("base64") },
      rate_key: randomBytes(32).toString("base64"),
    }),
    { mode: 0o600 },
  );
  function start(inbox = false) {
    return Bun.spawn([join(tools, "mychef")], {
      cwd: root,
      env: {
        ...process.env,
        MYCHEF_ADDR: "127.0.0.1:18139",
        MYCHEF_ORIGIN: origin,
        MYCHEF_DB: db,
        MYCHEF_DEV: "1",
        MYCHEF_PROVIDER: "fixture",
        MYCHEF_E2E: "1",
        MYCHEF_EMAIL_MODE: "fake",
        MYCHEF_FIXTURE_INBOX_FILE: inbox ? inboxFile : "",
        MYCHEF_EMAIL_PROOF_KEYS_FILE: keyFile,
      },
      stdout: "ignore",
      stderr: "pipe",
    });
  }
  let server = start(true);
  const context = await browser.newContext({
    baseURL: origin,
    locale: "en-GB",
  });
  try {
    await expect
      .poll(async () => {
        try {
          return (await context.request.get(origin + "/healthz")).status();
        } catch {
          return 0;
        }
      })
      .toBe(200);
    const page = await context.newPage();
    const signed = await shoppingAccount(page, capability);
    await page.goto("/app/?view=shopping&week=2026-11-02");
    await openExtras(page);
    await page.getByLabel("Extra item", { exact: true }).fill("Restart coffee");
    await page.getByRole("button", { name: "Add extra", exact: true }).click();
    await expect(page.getByText("Saved", { exact: true })).toBeVisible();
    await page.getByLabel("Mark Restart coffee as bought").click();
    await expect(page.getByText("Saved", { exact: true })).toBeVisible();
    const headers = { Origin: origin, "X-CSRF-Token": signed.csrfToken };
    const generated = await page.request.post("/api/v1/generations", {
      headers: { ...headers, "Idempotency-Key": crypto.randomUUID() },
      data: {
        dates: ["2026-10-01"],
        adults: 2,
        children: 0,
        maxMinutes: 30,
        budget: "low",
        region: "Belgium",
        creativity: 2,
      },
    });
    expect(generated.status()).toBe(202);
    const op = await generated.json();
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
    const recipe = (
      await (
        await page.request.get("/api/v1/menus?from=2026-10-01&to=2026-10-02")
      ).json()
    ).recipes[0];
    expect(
      (
        await page.request.put(`/api/v1/recipes/${recipe.id}/favorite`, {
          headers,
        })
      ).ok(),
    ).toBe(true);
    const next = addDays(monday(today()), 7);
    expect(
      (
        await page.request.post(`/api/v1/favorites/${recipe.id}/queue`, {
          headers,
          data: { week: next, today: today() },
        })
      ).ok(),
    ).toBe(true);
    server.kill("SIGTERM");
    expect(await server.exited).toBe(0);
    server = start();
    await expect
      .poll(async () => {
        try {
          return (await context.request.get(origin + "/readyz")).status();
        } catch {
          return 0;
        }
      })
      .toBe(200);
    await page.reload();
    await expect(
      page.getByLabel("Mark Restart coffee as needed"),
    ).toBeChecked();
    expect(
      (await (await page.request.get("/api/v1/favorites")).json()).recipes[0]
        .id,
    ).toBe(recipe.id);
    expect(
      (await (await page.request.get(`/api/v1/favorite-weeks/${next}`)).json())
        .recipes[0].id,
    ).toBe(recipe.id);
    expect(
      (await (await page.request.get("/api/v1/shopping/2026-11-02")).json())
        .items[0].state,
    ).toBe("basket");
  } finally {
    await context.close();
    server.kill("SIGTERM");
    await server.exited;
    rmSync(directory, { recursive: true, force: true });
  }
});
