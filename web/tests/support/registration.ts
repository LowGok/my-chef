import {
  expect,
  type APIRequestContext,
  type APIResponse,
  type Page,
} from "@playwright/test";

type Account = {
  firstName: string;
  lastName: string;
  username: string;
  email: string;
  password: string;
};

// Playwright transport exceptions can include request headers or bodies.
async function fixtureRequest<T>(operation: () => Promise<T>): Promise<T> {
  try {
    return await operation();
  } catch {
    throw new Error("The isolated fixture request failed.");
  }
}

export function fixtureInboxRead(
  request: APIRequestContext,
  origin: string,
  recipient: string,
  capability = process.env.MYCHEF_TEST_INBOX_CAPABILITY,
): Promise<APIResponse> {
  return fixtureRequest(() =>
    request.post(origin + "/api/v1/fixture/inbox", {
      headers: capability
        ? { Origin: origin, Authorization: "Bearer " + capability }
        : { Origin: origin },
      data: { recipient },
    }),
  );
}

export async function fixtureProof(
  request: APIRequestContext,
  origin: string,
  recipient: string,
  kind: "signup" | "verify-email" | "change-email" | "reset-password",
  capability = process.env.MYCHEF_TEST_INBOX_CAPABILITY,
): Promise<string> {
  if (!capability)
    throw new Error("The isolated fixture inbox capability was not provided.");
  let proof = "";
  await expect
    .poll(
      async () => {
        const response = await fixtureInboxRead(
          request,
          origin,
          recipient,
          capability,
        );
        if (response.status() === 204) return false;
        if (response.status() !== 200)
          throw new Error("The isolated fixture inbox denied the test.");
        const { text } = await fixtureRequest(() => response.json());
        proof =
          new RegExp("/app/" + kind + "#([A-Za-z0-9_-]{43})").exec(text)?.[1] ??
          "";
        return !!proof;
      },
      { timeout: 15_000 },
    )
    .toBe(true);
  return proof;
}

export async function registerFixture(
  page: Page,
  { password, ...profile }: Account,
  capability?: string,
) {
  await page.goto("/app/");
  await expect(
    page.getByRole("heading", { name: "Welcome back." }),
  ).toBeVisible();
  const origin = new URL(page.url()).origin;
  const initial = await fixtureRequest(async () =>
    (await page.request.get("/api/v1/session")).json(),
  );
  const headers = { Origin: origin, "X-CSRF-Token": initial.csrfToken };
  const requested = await fixtureRequest(() =>
    page.request.post("/api/v1/register", { headers, data: profile }),
  );
  expect(requested.status()).toBe(202);
  const proof = await fixtureProof(
    page.request,
    origin,
    profile.email,
    "signup",
    capability,
  );
  const { firstName, lastName, username } = profile;
  const completed = await fixtureRequest(() =>
    page.request.post("/api/v1/signup/complete", {
      headers,
      data: { proof, firstName, lastName, username, password },
    }),
  );
  expect(completed.status()).toBe(201);
  return signInFixture(page, profile.email, password);
}

export async function fixtureLogin(
  request: APIRequestContext,
  origin: string,
  csrf: string,
  email: string,
  password: string,
): Promise<APIResponse> {
  const attempt = () =>
    fixtureRequest(() =>
      request.post(origin + "/api/v1/login", {
        headers: { Origin: origin, "X-CSRF-Token": csrf },
        data: { email, password },
      }),
    );
  let response = await attempt();
  if (response.status() === 429) {
    await expect
      .poll(
        async () => {
          response = await attempt();
          return response.status();
        },
        { timeout: 65_000, intervals: [1_000, 4_000, 6_000] },
      )
      .not.toBe(429);
  }
  return response;
}

export async function signInFixture(
  page: Page,
  email: string,
  password: string,
) {
  await expect(
    page.getByRole("heading", { name: "Welcome back." }),
  ).toBeVisible();
  const origin = new URL(page.url()).origin;
  const session = await fixtureRequest(async () =>
    (await page.request.get("/api/v1/session")).json(),
  );
  const response = await fixtureLogin(
    page.request,
    origin,
    session.csrfToken,
    email,
    password,
  );
  expect(response.status()).toBe(200);
  const value = await fixtureRequest(() => response.json());
  expect(!!value.user).toBe(true);
  await page.reload();
  await expect(
    page.getByRole("button", { name: "Profile", exact: true }),
  ).toBeVisible();
  return value;
}

export async function registerFixtureThroughBrowser(
  page: Page,
  account: Account,
) {
  await page.goto("/app/");
  await page
    .getByRole("button", { name: "Create an account", exact: true })
    .click();
  for (const [label, value] of Object.entries({
    "First name": account.firstName,
    "Last name": account.lastName,
    Username: account.username,
    Email: account.email,
  }))
    await page.getByLabel(label, { exact: true }).fill(value);
  await page
    .getByRole("button", { name: "Create my account", exact: true })
    .click();
  await expect(page.getByRole("status")).toContainText("Check your inbox");
  const origin = new URL(page.url()).origin;
  const proof = await fixtureProof(
    page.request,
    origin,
    account.email,
    "signup",
  );
  await page.goto("/app/signup");
  await page.evaluate((fragment) => {
    location.hash = fragment;
  }, proof);
  await page.getByLabel("Password", { exact: true }).fill(account.password);
  await page
    .getByLabel("Confirm password", { exact: true })
    .fill(account.password);
  await page
    .getByRole("button", { name: "Create account", exact: true })
    .click();
  await expect(page.getByRole("status")).toContainText("Account created");
  await page
    .getByRole("button", { name: "Back to MyChef", exact: true })
    .click();
  return signInFixture(page, account.email, account.password);
}
