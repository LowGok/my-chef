import { expect, test, type Page } from "@playwright/test";
import { randomBytes } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import {
  personalFixture,
  personalRequest,
  personalPhoto,
  privateAction,
} from "../support/personal-recipes";
import type {
  PersonalRecipe,
  PersonalContent,
  RecipeImport,
} from "../../src/api";
const state = process.env.MYCHEF_LIVE_STATE!;
const filename = () => join(state, "import-state.json");
const password = "Garden personal recipe fixture 2026!";
type Evidence = {
  id: string;
  version: string;
  draft: string;
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
  await page.goto("/app/");
  await expect(page.getByRole("heading", { name: "Welcome back.", exact: true })).toBeVisible();
  const bootstrap = await page.request.get("/api/v1/session");
  expect(bootstrap.status()).toBe(200);
  const session = await bootstrap.json();
  const response = await page.request.post("/api/v1/login", {
    headers: {
      Origin: new URL(bootstrap.url()).origin,
      "X-CSRF-Token": session.csrfToken,
    },
    data: { email: "importcontainer@example.test", password },
  });
  expect(response.status()).toBe(200);
  await page.goto("/app/");
}
async function ready(page: Page, id: string): Promise<RecipeImport> {
  let op: RecipeImport | null = null;
  await expect
    .poll(async () => {
      op = await personalRequest<RecipeImport>(
        page,
        "GET",
        `/recipe-imports/${id}`,
      );
      return op.stage;
    })
    .toBe("ready");
  return op!;
}
test("imports create reviewed version and private photo draft in container", async ({
  page,
}) => {
  await privateAction(async () => {
    page.on("response", async (response) => {
      if (!response.url().includes("/api/v1/") || response.status() < 400)
        return;
      const body = await response.json().catch(() => ({}));
      const code = /^[a-z_]{1,40}$/.test(body?.error?.code ?? "")
        ? body.error.code
        : "unavailable";
      console.info(
        `Synthetic container import: HTTP ${response.status()} ${code}`,
      );
    });
    const canaries = ["container-import-" + randomBytes(16).toString("hex")];
    writeFileSync(filename(), JSON.stringify({ canaries }), { mode: 0o600 });
    await personalFixture(page, "importcontainer");
    console.info("Synthetic container import: account ready");
    const available = await personalRequest<{
      available: boolean;
      fixture: boolean;
      photosAvailable: boolean;
    }>(page, "GET", "/recipe-imports/availability");
    expect(
      available.available && available.fixture && available.photosAvailable,
    ).toBe(true);
    const started = await personalRequest<RecipeImport>(
      page,
      "POST",
      "/recipe-imports/url",
      {
        requestKey: randomBytes(16).toString("hex"),
        url: `https://recipes.example.test/carrot-soup?utm_source=${canaries[0]}`,
        personalUseConfirmed: true,
      },
    );
    const op = await ready(page, started.id);
    console.info("Synthetic container import: structured draft ready");
    expect(
      (
        await personalRequest<{ recipes: PersonalRecipe[] }>(
          page,
          "POST",
          "/personal-recipes/search",
          {},
        )
      ).recipes.length,
    ).toBe(0);
    console.info("Synthetic container import: library unchanged");
    const draft = op.draft!;
    const content = { ...draft.content, description: canaries[0] };
    const review = await personalRequest<{
      content: PersonalContent;
      contentRevision: string;
      requiredFields: string[];
    }>(page, "POST", `/recipe-imports/${op.id}/review`, { content });
    console.info("Synthetic container import: review complete");
    const saved = await personalRequest<PersonalRecipe>(
      page,
      "POST",
      "/personal-recipes",
      {
        requestKey: randomBytes(16).toString("hex"),
        content: review.content,
        photoAction: "keep",
        import: {
          operationId: op.id,
          confirmation: {
            draftRevision: draft.revision,
            contentRevision: review.contentRevision,
            reviewedFields: review.requiredFields,
            resolvedFields: [
              ...new Set(draft.issues.map((issue) => issue.field)),
            ],
          },
        },
      },
    );
    expect(
      saved.importSource?.url === "https://recipes.example.test/carrot-soup" &&
        saved.content.description === canaries[0] &&
        saved.imageUrl === null,
    ).toBe(true);
    console.info("Synthetic container import: reviewed Save complete");
    const session = await (await page.request.get("/api/v1/session")).json();
    const photo = await page.request.post("/api/v1/recipe-imports/photos", {
      headers: {
        Origin: new URL(page.url()).origin,
        "X-CSRF-Token": session.csrfToken,
      },
      multipart: {
        import: JSON.stringify({
          requestKey: randomBytes(16).toString("hex"),
          personalUseConfirmed: true,
          aiConsentConfirmed: true,
        }),
        photos: {
          name: canaries[0] + ".png",
          mimeType: "image/png",
          buffer: personalPhoto,
        },
      },
    });
    expect(photo.status()).toBe(202);
    const photoDraft = await ready(page, (await photo.json()).id);
    expect(photoDraft.draft?.source.kind === "photos").toBe(true);
    expect(
      (
        await personalRequest<{ recipes: PersonalRecipe[] }>(
          page,
          "POST",
          "/personal-recipes/search",
          {},
        )
      ).recipes.length,
    ).toBe(1);
    const value: Evidence = {
      id: saved.id,
      version: saved.versionId,
      draft: photoDraft.id,
      canaries,
    };
    writeFileSync(filename(), JSON.stringify(value), { mode: 0o600 });
  });
});
test("imports restart and restore retain private provenance and draft", async ({
  page,
}) => {
  await privateAction(async () => {
    const value = evidence();
    await login(page);
    const permission = await personalRequest<{ canWrite: boolean }>(
      page,
      "GET",
      "/personal-recipes/permissions",
    );
    expect(permission.canWrite).toBe(process.env.MYCHEF_EXPECT_FIXTURE_PREMIUM === "1");
    const saved = await personalRequest<PersonalRecipe>(
      page,
      "GET",
      `/personal-recipes/${value.id}`,
    );
    expect(
      saved.versionId === value.version &&
        saved.importSource?.url ===
          "https://recipes.example.test/carrot-soup" &&
        saved.content.description === value.canaries[0],
    ).toBe(true);
    const draft = await personalRequest<RecipeImport>(
      page,
      "GET",
      `/recipe-imports/${value.draft}`,
    );
    expect(
      draft.stage === "ready" && draft.draft?.source.kind === "photos",
    ).toBe(true);
  });
});
test("imports account deletion removes saved provenance and retained draft", async ({
  page,
}) => {
  await privateAction(async () => {
    const value = evidence();
    await login(page);
    const session = await (await page.request.get("/api/v1/session")).json();
    const response = await page.request.post("/api/v1/account/deletion", {
      headers: {
        Origin: new URL(page.url()).origin,
        "X-CSRF-Token": session.csrfToken,
        "Idempotency-Key": randomBytes(16).toString("hex"),
      },
      data: { currentPassword: password, confirmation: "DELETE" },
    });
    expect(response.status()).toBe(200);
    expect(
      (
        await page.request.get(`/api/v1/recipe-imports/${value.draft}`)
      ).status(),
    ).toBe(401);
  });
});
test("imports deletion replay cannot restore the deleted account", async ({
  request,
}) => {
  await privateAction(async () => {
    const bootstrap = await request.get("/api/v1/session");
    expect(bootstrap.status()).toBe(200);
    const session = await bootstrap.json();
    const response = await request.post("/api/v1/login", {
      headers: {
        Origin: new URL(bootstrap.url()).origin,
        "X-CSRF-Token": session.csrfToken,
      },
      data: { email: "importcontainer@example.test", password },
    });
    expect(response.status()).toBe(401);
  });
});
