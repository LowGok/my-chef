import { expect, test } from "@playwright/test";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
const notice = {
  available: true,
  defaultEnabled: true,
  version: "core-v1",
  hash: "fictional",
  basis: "legitimate_interest",
  text: "Fictional fixture: 90 days of events, 13 monthly buckets, isolated backups up to seven days. Turn off in Profile.",
};
test("native text scale enlarges the analytics notice and labelled control without clipping", async ({
  page,
}) => {
  await page.setViewportSize({ width: 320, height: 812 });
  await page.goto("/app/");
  await page
    .getByRole("button", { name: "Create an account", exact: true })
    .click();
  await page.evaluate(() =>
    document.documentElement.style.setProperty("--native-text-size", "200%"),
  );
  const sizes = await page.locator(".analytics-control").evaluate((panel) => ({
    label: parseFloat(getComputedStyle(panel.querySelector("label")!).fontSize),
    notice: parseFloat(getComputedStyle(panel.querySelector("p")!).fontSize),
    fits: document.documentElement.scrollWidth <= innerWidth + 1,
  }));
  expect(sizes.label).toBeGreaterThanOrEqual(30);
  expect(sizes.notice).toBeGreaterThanOrEqual(24);
  expect(sizes.fits).toBe(true);
});
test("signup offers a visible On switch and preserves Off in the request", async ({
  page,
}) => {
  await page.route("**/api/v1/analytics/notice", (route) =>
    route.fulfill({ json: notice }),
  );
  await page.goto("/app/");
  await page
    .getByRole("button", { name: "Create an account", exact: true })
    .click();
  const toggle = page.getByRole("checkbox", {
    name: "Help improve MyChef",
    exact: true,
  });
  await expect(toggle).toBeChecked();
  await toggle.uncheck();
  await page.getByLabel("First name", { exact: true }).fill("Fictional");
  await page.getByLabel("Last name", { exact: true }).fill("Signup");
  await page.getByLabel("Username", { exact: true }).fill("analyticsfixture");
  await page
    .getByLabel("Email", { exact: true })
    .fill("analyticsfixture@example.test");
  const request = page.waitForRequest((r) =>
    r.url().endsWith("/api/v1/register"),
  );
  await page
    .getByRole("button", { name: "Create my account", exact: true })
    .click();
  expect((await request).postDataJSON()).toMatchObject({
    analyticsEnabled: false,
    analyticsNoticeVersion: "core-v1",
  });
});

test("iOS installed disabled-state probe reaches signup through its accessible label", async ({
  page,
}) => {
  const swift = readFileSync(
    resolve(
      import.meta.dir,
      "../../mobile/ios/App/App/MyChefNativePlugin.swift",
    ),
    "utf8",
  );
  const body = swift.split("private func checkDisabledAnalytics")[1];
  const program = body?.match(
    /evaluateJavaScript\("""\n([\s\S]*?)\n\s*"""\)/,
  )?.[1];
  expect(program).toBeTruthy();
  await page.goto("/app/");
  await expect
    .poll(() => page.evaluate(program!), { timeout: 5000 })
    .toEqual({
      disabled: true,
      unchecked: true,
      labelled: true,
      fits: true,
    });
});

test("Profile reports an unconfirmed offline opt-out and the later server fence truthfully", async ({
  page,
}) => {
  const { registerFixture } = await import("./support/registration");
  let failOff = true;
  let preference = {
    enabled: true,
    revision: 1,
    noticeVersion: "core-v1",
    noticeHash: "fixture",
    basis: "legitimate_interest",
    basisVersion: "fixture",
    source: "explicit_enable",
    state: "on",
    deletionStatus: "none",
    available: true,
  };
  await page.route("**/api/v1/analytics/preferences", async (route) => {
    const response = await route.fetch();
    if (route.request().method() === "PUT") {
      if (failOff) {
        await route.fulfill({
          response,
          status: 503,
          json: {
            error: {
              code: "unavailable",
              message: "Fictional interrupted update",
            },
          },
        });
        return;
      }
      preference = {
        ...preference,
        enabled: false,
        revision: 2,
        source: "objection",
        state: "deletion_in_progress",
        deletionStatus: "backup_pending",
      };
    }
    await route.fulfill({ response, json: preference });
  });
  await registerFixture(page, {
    firstName: "Fictional",
    lastName: "Profile",
    username: "analyticsprofile",
    email: "analyticsprofile@example.test",
    password: "ChefTest2026!",
  });
  await page.getByRole("button", { name: "Profile", exact: true }).click();
  await page.getByText("Account details", { exact: true }).click();
  const toggle = page.getByRole("checkbox", {
    name: "Help improve MyChef",
    exact: true,
  });
  await expect(toggle).toBeChecked();
  await toggle.click();
  await expect(page.getByRole("alert")).toContainText(
    "Turning off was not confirmed",
  );
  await expect(toggle).toBeChecked();
  failOff = false;
  await toggle.click();
  await expect(toggle).not.toBeChecked();
  await expect(
    page.getByRole("status").filter({ hasText: "isolated backup cleanup" }),
  ).toBeVisible();
  await page.setViewportSize({ width: 320, height: 812 });
  await page.evaluate(() => {
    document.documentElement.style.fontSize = "200%";
  });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true);
});
