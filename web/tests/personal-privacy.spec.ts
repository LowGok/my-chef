import { expect, test } from "@playwright/test";
import { randomBytes } from "node:crypto";
import {
  mkdtempSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { spawnSync } from "node:child_process";
import {
  fillPersonalDraft,
  openPersonalLibrary,
  personalFixture,
  personalPhoto,
  personalRequest,
  privateAction,
} from "./support/personal-recipes";
import type { PersonalRecipe } from "../src/api";
import { addDays, monday, today } from "../src/dates";

test("controlled success and failure artifacts exclude private bodies, filenames and photos", async () => {
  const directory = mkdtempSync(join(tmpdir(), "mychef-personal-artifacts-"));
  const canary = "private-" + randomBytes(24).toString("hex"),
    photo = personalPhoto.toString("base64");
  try {
    const cases = join(directory, "cases");
    mkdirSync(cases, { mode: 0o700 });
    const module = resolve("node_modules/@playwright/test/index.mjs");
    writeFileSync(
      join(cases, "controlled.spec.ts"),
      `import {test} from ${JSON.stringify(module)};
import {privateAction,personalRequest} from ${JSON.stringify(resolve("tests/support/personal-recipes.ts"))};
for (const failure of [false,"action","request"]) test(failure ? "controlled failure "+failure : "controlled success", async({page})=>{
  const canary=process.env.MYCHEF_PRIVATE_ARTIFACT_CANARY;
  const photo=process.env.MYCHEF_PRIVATE_ARTIFACT_IMAGE;
  await page.setContent('<main><p id="body"></p><img id="photo"><input id="filename" aria-label="Selected file"></main>');
  await page.evaluate(({canary,photo})=>{
    document.querySelector('#body').textContent=canary;
    const image=document.querySelector('#photo');image.alt=canary;image.src='data:image/png;base64,'+photo;
    document.querySelector('#filename').value=canary+'.png';
  },{canary,photo});
  if(failure==='action')await privateAction(()=>page.locator('#missing-private-field').fill(canary,{timeout:100}));
  if(failure==='request')await personalRequest(page,'GET','/personal-recipes/search');
});`,
      { mode: 0o600 },
    );
    writeFileSync(
      join(directory, "config.ts"),
      `import base from ${JSON.stringify(resolve("playwright.config.ts"))};
export default {...base,testDir:${JSON.stringify(cases)},outputDir:${JSON.stringify(join(directory, "output"))},webServer:undefined,preserveOutput:'always',reporter:'list',retries:0,workers:1};`,
      { mode: 0o600 },
    );
    const child = spawnSync(
      process.execPath,
      [
        "--bun",
        resolve("node_modules/@playwright/test/cli.js"),
        "test",
        "--config",
        join(directory, "config.ts"),
      ],
      {
        cwd: process.cwd(),
        encoding: "utf8",
        timeout: 25000,
        env: {
          ...process.env,
          MYCHEF_PRIVATE_ARTIFACT_CANARY: canary,
          MYCHEF_PRIVATE_ARTIFACT_IMAGE: photo,
        },
      },
    );
    const output = (child.stdout ?? "") + (child.stderr ?? "");
    expect(child.status).toBe(1);
    expect(output.includes("1 passed") && output.includes("2 failed")).toBe(
      true,
    );
    const leaks: string[] = [];
    function scan(path: string) {
      for (const file of readdirSync(path, { withFileTypes: true })) {
        const next = join(path, file.name);
        if (file.isDirectory()) scan(next);
        else {
          const data = readFileSync(next);
          if (
            data.includes(Buffer.from(canary)) ||
            data.includes(Buffer.from(photo)) ||
            data.includes(personalPhoto) ||
            /\.(png|jpe?g|webm|zip)$/i.test(file.name)
          )
            leaks.push(file.name);
        }
      }
    }
    scan(directory);
    if (output.includes(canary) || output.includes(photo))
      leaks.push("runner output");
    expect(leaks).toEqual([]);
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});

test("photo previews are revoked on replace, removal, discard and save", async ({
  page,
}) => {
  await page.addInitScript(() => {
    const create = URL.createObjectURL.bind(URL),
      revoke = URL.revokeObjectURL.bind(URL),
      active = new Set<string>();
    (
      window as unknown as { privatePreviewCount: () => number }
    ).privatePreviewCount = () => active.size;
    URL.createObjectURL = (blob) => {
      const value = create(blob);
      active.add(value);
      return value;
    };
    URL.revokeObjectURL = (value) => {
      active.delete(value);
      revoke(value);
    };
  });
  const count = () =>
    page.evaluate(() =>
      (
        window as unknown as { privatePreviewCount: () => number }
      ).privatePreviewCount(),
    );
  await personalFixture(page, "personalpreviews");
  await openPersonalLibrary(page);
  await page
    .getByRole("button", { name: "Create a recipe", exact: true })
    .click();
  await fillPersonalDraft(page);
  const choose = page.getByLabel("Choose recipe photo", { exact: true });
  await choose.setInputFiles({
    name: "synthetic-first.png",
    mimeType: "image/png",
    buffer: personalPhoto,
  });
  await expect.poll(count).toBe(1);
  await choose.setInputFiles({
    name: "synthetic-replacement.png",
    mimeType: "image/png",
    buffer: personalPhoto,
  });
  await expect.poll(count).toBe(1);
  await page.getByRole("button", { name: "Remove photo", exact: true }).click();
  await expect.poll(count).toBe(0);
  await choose.setInputFiles({
    name: "synthetic-discard.png",
    mimeType: "image/png",
    buffer: personalPhoto,
  });
  await expect.poll(count).toBe(1);
  await page
    .getByRole("button", { name: "Discard changes", exact: true })
    .click();
  await expect.poll(count).toBe(0);
  await page
    .getByRole("button", { name: "Create a recipe", exact: true })
    .click();
  await fillPersonalDraft(page);
  await choose.setInputFiles({
    name: "synthetic-save.png",
    mimeType: "image/png",
    buffer: personalPhoto,
  });
  await expect.poll(count).toBe(1);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.locator(".personal-detail img")).toBeVisible();
  await expect.poll(count).toBe(0);
  await page.getByRole("button", { name: "Edit recipe", exact: true }).click();
  await choose.setInputFiles({
    name: "synthetic-signout.png",
    mimeType: "image/png",
    buffer: personalPhoto,
  });
  await expect.poll(count).toBe(1);
  const otherTab = await page.context().newPage();
  try {
    await otherTab.goto("/app/?view=profile");
    await otherTab
      .getByRole("button", { name: "Sign out", exact: true })
      .click();
    await expect(
      page.getByRole("heading", { name: "Welcome back.", exact: true }),
    ).toBeVisible();
    await expect.poll(count).toBe(0);
  } finally {
    await otherTab.close();
  }
});

test("only ordinary shopping fields persist and another account cannot read personal photos", async ({
  page,
}) => {
  const suffix = randomBytes(12).toString("hex"),
    allowed = {
      title: "basket-title-" + suffix,
      ingredient: "basket-ingredient-" + suffix,
    };
  const forbidden = {
    description: "private-description-" + suffix,
    instruction: "private-method-" + suffix,
    equipment: "private-equipment-" + suffix,
    tip: "private-tip-" + suffix,
    note: "private-declaration-" + suffix,
    filename: "private-camera-" + suffix + ".png",
  };
  const forbiddenValues = Object.values(forbidden);
  async function storageFlags() {
    return await page.evaluate(
      async ({ allowed, forbidden }) => {
        const stored = JSON.stringify({
          local: { ...localStorage },
          session: { ...sessionStorage },
        });
        let cached = "",
          cachedImage = false;
        for (const name of await caches.keys()) {
          const cache = await caches.open(name);
          for (const request of await cache.keys()) {
            cachedImage ||= new URL(request.url).pathname.startsWith(
              "/api/v1/personal-photos/",
            );
            cached += request.url;
            const response = await cache.match(request);
            if (response) cached += await response.text();
          }
        }
        return {
          privateLeak: forbidden.some(
            (value) => stored.includes(value) || cached.includes(value),
          ),
          shopping:
            stored.includes(allowed.title) &&
            stored.includes(allowed.ingredient),
          cachedImage,
        };
      },
      { allowed, forbidden: forbiddenValues },
    );
  }
  await personalFixture(page, "personalstorage");
  await expect
    .poll(() => page.evaluate(() => !!navigator.serviceWorker.controller))
    .toBe(true);
  await openPersonalLibrary(page);
  await page
    .getByRole("button", { name: "Create a recipe", exact: true })
    .click();
  await fillPersonalDraft(page);
  await privateAction(async () => {
    await page.getByLabel("Title", { exact: true }).fill(allowed.title);
    await page
      .getByLabel("Ingredient 1", { exact: true })
      .fill(allowed.ingredient);
    await page
      .getByLabel("Short description", { exact: true })
      .fill(forbidden.description);
    await page
      .getByLabel("Equipment", { exact: true })
      .fill(forbidden.equipment);
    await page
      .getByLabel("Step 1", { exact: true })
      .fill(forbidden.instruction);
    await page.getByRole("button", { name: "Add tip", exact: true }).click();
    await page.getByLabel("Tip 1", { exact: true }).fill(forbidden.tip);
    await page
      .getByLabel("Allergen note", { exact: true })
      .fill(forbidden.note);
    await page
      .getByLabel("Choose recipe photo", { exact: true })
      .setInputFiles({
        name: forbidden.filename,
        mimeType: "image/png",
        buffer: personalPhoto,
      });
  });
  expect(await storageFlags()).toEqual({
    privateLeak: false,
    shopping: false,
    cachedImage: false,
  });
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.locator(".personal-detail img")).toBeVisible();
  const photoURL = await page
    .locator(".personal-detail img")
    .getAttribute("src");
  const saved = await personalRequest<{ recipes: PersonalRecipe[] }>(
    page,
    "POST",
    "/personal-recipes/search",
    {},
  );
  const id = saved.recipes[0].id;
  expect(await storageFlags()).toEqual({
    privateLeak: false,
    shopping: false,
    cachedImage: false,
  });
  const photoResponse = await page.request.get(photoURL!);
  expect(photoResponse.headers()["cache-control"]).toBe("private, no-store");
  expect(photoResponse.headers()["location"]).toBeUndefined();
  await page.context().setOffline(true);
  expect(
    await page.evaluate(async (url) => {
      try {
        return (await fetch(url!, { credentials: "same-origin" })).ok;
      } catch {
        return false;
      }
    }, photoURL),
  ).toBe(false);
  await page.context().setOffline(false);
  await page
    .getByRole("button", { name: "Add to next week", exact: true })
    .click();
  await expect(
    page.getByRole("status").filter({ hasText: "Added to next week." }),
  ).toBeVisible();
  const week = addDays(monday(today()), 7);
  await page.goto("/app/?week=" + week);
  await page.getByRole("button", { name: "Plan a menu", exact: true }).click();
  await page.getByLabel("First meal date", { exact: true }).fill(week);
  await page.getByLabel("Number of meals", { exact: true }).fill("1");
  await page.getByRole("button", { name: "Review menu", exact: true }).click();
  await page.getByRole("button", { name: "Plan my week", exact: true }).click();
  await expect(page.locator(".recipe-card")).toHaveCount(1);
  await page.goto("/app/?view=shopping&week=" + week);
  await expect.poll(async () => (await storageFlags()).shopping).toBe(true);
  expect((await storageFlags()).privateLeak).toBe(false);
  expect((await storageFlags()).cachedImage).toBe(false);
  await page.goto("/app/?view=profile");
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Welcome back.", exact: true }),
  ).toBeVisible();
  expect((await storageFlags()).shopping).toBe(false);
  await expect
    .poll(async () => (await page.request.get(photoURL!)).status())
    .toBe(401);
  const other = await page
    .context()
    .browser()!
    .newContext({ baseURL: new URL(page.url()).origin });
  try {
    await personalFixture(await other.newPage(), "personalforeign");
  } finally {
    await other.close();
  }
  await page
    .getByLabel("Email", { exact: true })
    .fill("personalforeign@example.test");
  await page
    .getByLabel("Password", { exact: true })
    .fill("Garden personal recipe fixture 2026!");
  await page
    .getByRole("button", { name: "Sign in to MyChef", exact: true })
    .click();
  await expect(
    page.getByRole("navigation", { name: "Main navigation" }),
  ).toBeVisible();
  expect(
    (await page.request.get("/api/v1/personal-recipes/" + id)).status(),
  ).toBe(404);
  expect((await page.request.get(photoURL!)).status()).toBe(404);
  expect((await storageFlags()).privateLeak).toBe(false);
});

test("late reads and saves cannot restore a recipe after leaving the page", async ({
  page,
}) => {
  await personalFixture(page, "personallate");
  await openPersonalLibrary(page);
  await page
    .getByRole("button", { name: "Create a recipe", exact: true })
    .click();
  await fillPersonalDraft(page);
  let release!: () => void, started!: () => void;
  const held = new Promise<void>((resolve) => {
      release = resolve;
    }),
    began = new Promise<void>((resolve) => {
      started = resolve;
    });
  await page.route("**/api/v1/personal-recipes", async (route) => {
    if (route.request().method() !== "POST") {
      await route.continue();
      return;
    }
    try {
      const response = await route.fetch();
      started();
      await held;
      await route.fulfill({ response }).catch(() => {});
    } catch {
      started();
    }
  });
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await began;
  page.once("dialog", (dialog) => dialog.accept());
  await page
    .getByRole("navigation", { name: "Main navigation" })
    .getByRole("button", { name: "Meals", exact: true })
    .click();
  release();
  await expect(page.locator(".personal-editor,.personal-detail")).toHaveCount(
    0,
  );
  await page.unroute("**/api/v1/personal-recipes");
  await page
    .getByRole("navigation", { name: "Main navigation" })
    .getByRole("button", { name: "Profile", exact: true })
    .click();
  await openPersonalLibrary(page);
  await expect(page.locator(".personal-row")).toHaveCount(1);
  let releaseRead!: () => void, readStarted!: () => void;
  const heldRead = new Promise<void>((resolve) => {
      releaseRead = resolve;
    }),
    reading = new Promise<void>((resolve) => {
      readStarted = resolve;
    });
  await page.route(
    /\/api\/v1\/personal-recipes\/[0-9a-f]{32}$/,
    async (route) => {
      try {
        const response = await route.fetch();
        readStarted();
        await heldRead;
        await route.fulfill({ response }).catch(() => {});
      } catch {
        readStarted();
      }
    },
  );
  await page.locator(".personal-row").click();
  await reading;
  await page
    .getByRole("navigation", { name: "Main navigation" })
    .getByRole("button", { name: "Meals", exact: true })
    .click();
  releaseRead();
  await expect(
    page.locator(".personal-library,.personal-editor,.personal-detail"),
  ).toHaveCount(0);
});
