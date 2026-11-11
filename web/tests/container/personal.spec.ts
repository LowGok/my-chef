import { expect, test, type Page } from "@playwright/test";
import { createHash, randomBytes } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import {
  fillPersonalDraft,
  openPersonalLibrary,
  personalFixture,
  personalPhoto,
  personalRequest,
  privateAction,
} from "../support/personal-recipes";
import type { PersonalRecipe } from "../../src/api";
const state = process.env.MYCHEF_LIVE_STATE!;
const filename = () => join(state, "personal-state.json");
const password = "Garden personal recipe fixture 2026!";
type Evidence = {
  id: string;
  first: string;
  second: string;
  photos: string[];
  digests: string[];
  canaries: string[];
};
const evidence = () => JSON.parse(readFileSync(filename(), "utf8")) as Evidence;
test.beforeAll(() => {
  if (
    process.env.MYCHEF_CONTAINER_FIXTURE !== "1" ||
    process.env.MYCHEF_RUN_REWRITE_E2E !== "1" ||
    !state
  )
    throw new Error("Use the isolated container runner.");
});
async function login(page: Page) {
  await privateAction(async () => {
    await page.goto("/app/");
    await page
      .getByLabel("Email", { exact: true })
      .fill("personalcontainer@example.test");
    await page.getByLabel("Password", { exact: true }).fill(password);
    await page
      .getByRole("button", { name: "Sign in to MyChef", exact: true })
      .click();
    await expect(
      page.getByRole("navigation", { name: "Main navigation" }),
    ).toBeVisible();
  });
}
async function verifyPhotos(page: Page, value: Evidence) {
  for (let index = 0; index < value.photos.length; index++) {
    const response = await privateAction(() =>
      page.request.get(value.photos[index]),
    );
    expect(response.status()).toBe(200);
    expect(response.headers()["cache-control"]).toBe("private, no-store");
    expect(
      createHash("sha256")
        .update(await privateAction(() => response.body()))
        .digest("hex") === value.digests[index],
    ).toBe(true);
  }
}

test("personal create versions photos queue and export in the container", async ({
  page,
}) => {
  const marker = "container-private-" + randomBytes(16).toString("hex"),
    canaries = [
      marker + "-method",
      marker + "-first",
      marker + "-second",
      marker + "-camera.png",
    ];
  writeFileSync(filename(), JSON.stringify({ canaries }), { mode: 0o600 });
  await personalFixture(page, "personalcontainer");
  await openPersonalLibrary(page);
  await page
    .getByRole("button", { name: "Create a recipe", exact: true })
    .click();
  await fillPersonalDraft(page);
  await privateAction(async () => {
    await page.getByLabel("Step 1", { exact: true }).fill(canaries[0]);
    await page
      .getByLabel("Short description", { exact: true })
      .fill(canaries[1]);
    await page
      .getByLabel("Choose recipe photo", { exact: true })
      .setInputFiles({
        name: canaries[3],
        mimeType: "image/png",
        buffer: personalPhoto,
      });
  });
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.locator(".personal-detail img")).toBeVisible();
  const first = (
    await personalRequest<{ recipes: PersonalRecipe[] }>(
      page,
      "POST",
      "/personal-recipes/search",
      {},
    )
  ).recipes[0];
  await page.getByRole("button", { name: "Edit recipe", exact: true }).click();
  await privateAction(async () => {
    await page
      .getByLabel("Short description", { exact: true })
      .fill(canaries[2]);
    await page
      .getByLabel("Choose recipe photo", { exact: true })
      .setInputFiles({
        name: canaries[3],
        mimeType: "image/png",
        buffer: personalPhoto,
      });
  });
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.locator(".personal-detail img")).toBeVisible();
  const second = (
    await personalRequest<{ recipes: PersonalRecipe[] }>(
      page,
      "POST",
      "/personal-recipes/search",
      {},
    )
  ).recipes[0];
  expect(
    first.id === second.id &&
      first.versionId !== second.versionId &&
      first.imageUrl !== second.imageUrl,
  ).toBe(true);
  const value: Evidence = {
    id: first.id,
    first: first.versionId,
    second: second.versionId,
    photos: [first.imageUrl!, second.imageUrl!],
    digests: [],
    canaries,
  };
  for (const url of value.photos)
    value.digests.push(
      createHash("sha256")
        .update(
          await privateAction(
            async () => await (await page.request.get(url)).body(),
          ),
        )
        .digest("hex"),
    );
  await verifyPhotos(page, value);
  await page
    .getByRole("button", { name: "Add to next week", exact: true })
    .click();
  await expect(
    page.getByRole("status").filter({ hasText: "Added to next week." }),
  ).toBeVisible();
  const session = await privateAction(
    async () => await (await page.request.get("/api/v1/session")).json(),
  );
  const archive = await privateAction(() =>
    page.request.post("/api/v1/account/export", {
      headers: {
        Origin: new URL(page.url()).origin,
        "X-CSRF-Token": session.csrfToken,
      },
      data: { currentPassword: password },
    }),
  );
  expect(archive.status()).toBe(200);
  const check = spawnSync(
    "python3",
    [
      "-c",
      `import io,json,sys,zipfile
with zipfile.ZipFile(io.BytesIO(sys.stdin.buffer.read())) as z:
 photos=[n for n in z.namelist() if n.startswith('photos/') and n.endswith('.jpg')]
 versions=json.loads(z.read('personal-versions.json'))
 print(json.dumps({'version':json.loads(z.read('manifest.json'))['version'],'photos':len(photos),'versions':len(versions),'jpeg':all(z.read(n).startswith(b'\\xff\\xd8') for n in photos)}))
`,
    ],
    { input: await privateAction(() => archive.body()), encoding: "utf8" },
  );
  if (check.status !== 0)
    throw new Error("The private container archive was invalid.");
  expect(JSON.parse(check.stdout)).toEqual({
    version: 2,
    photos: 2,
    versions: 2,
    jpeg: true,
  });
  writeFileSync(filename(), JSON.stringify(value), { mode: 0o600 });
});

test("personal restart or restore reads every version and normalized photo without Premium", async ({
  page,
}) => {
  const value = evidence();
  await login(page);
  const permission = await personalRequest<{ canWrite: boolean }>(
    page,
    "GET",
    "/personal-recipes/permissions",
  );
  expect(permission.canWrite).toBe(false);
  const first = await personalRequest<PersonalRecipe>(
      page,
      "GET",
      `/personal-recipes/${value.id}?version=${value.first}`,
    ),
    second = await personalRequest<PersonalRecipe>(
      page,
      "GET",
      `/personal-recipes/${value.id}`,
    );
  expect(
    first.versionId === value.first &&
      second.versionId === value.second &&
      first.content.description === value.canaries[1] &&
      second.content.description === value.canaries[2],
  ).toBe(true);
  await verifyPhotos(page, value);
  await page.goto("/app/?view=profile");
  await openPersonalLibrary(page);
  await page.locator(".personal-row").click();
  await expect(page.locator(".personal-detail img")).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Edit recipe", exact: true }),
  ).toBeDisabled();
});

test("personal account deletion revokes every photo", async ({ page }) => {
  const value = evidence();
  await login(page);
  const session = await privateAction(
    async () => await (await page.request.get("/api/v1/session")).json(),
  );
  const response = await privateAction(() =>
    page.request.post("/api/v1/account/deletion", {
      headers: {
        Origin: new URL(page.url()).origin,
        "X-CSRF-Token": session.csrfToken,
        "Idempotency-Key": randomBytes(16).toString("hex"),
      },
      data: { currentPassword: password, confirmation: "DELETE" },
    }),
  );
  expect(response.status()).toBe(200);
  for (const url of value.photos)
    expect((await privateAction(() => page.request.get(url))).status()).toBe(
      401,
    );
});

test("personal deletion replay rejects the old account", async ({ page }) => {
  await page.goto("/app/");
  const session = await privateAction(
    async () => await (await page.request.get("/api/v1/session")).json(),
  );
  const response = await privateAction(() =>
    page.request.post("/api/v1/login", {
      headers: {
        Origin: new URL(page.url()).origin,
        "X-CSRF-Token": session.csrfToken,
      },
      data: { email: "personalcontainer@example.test", password },
    }),
  );
  expect(response.status()).toBe(401);
});

test("normal runtime rejects fixture grants and personal creation", async ({
  browser,
}) => {
  const context = await browser.newContext({
    baseURL: "http://127.0.0.1:18138",
    storageState: join(state, "session.json"),
  });
  try {
    const session = await privateAction(
      async () => await (await context.request.get("/api/v1/session")).json(),
    );
    expect(session.user !== null).toBe(true);
    const headers = {
      Origin: "http://127.0.0.1:18138",
      "X-CSRF-Token": session.csrfToken,
    };
    const permission = await privateAction(() =>
      context.request.get("/api/v1/personal-recipes/permissions"),
    );
    expect((await permission.json()).canWrite).toBe(false);
    expect(
      (
        await privateAction(() =>
          context.request.post("/api/v1/fixture/personal-recipes/entitlement", {
            headers,
            data: {},
          }),
        )
      ).status(),
    ).toBe(404);
    const content = {
      schemaVersion: 1,
      title: "A synthetic recipe",
      description: "",
      servings: 2,
      equipment: "",
      prepMinutes: 5,
      cookMinutes: 10,
      totalMinutes: 15,
      ingredients: [{ name: "Carrot", quantity: "1", unit: "g" }],
      instructions: ["Cook gently."],
      tips: [],
      allergens: [],
      allergenNote: "",
    };
    expect(
      (
        await privateAction(() =>
          context.request.post("/api/v1/personal-recipes", {
            headers,
            data: {
              requestKey: randomBytes(16).toString("hex"),
              expectedVersion: "",
              photoAction: "keep",
              content,
            },
          }),
        )
      ).status(),
    ).toBe(403);
  } finally {
    await context.close();
  }
});
