import { expect, test } from "@playwright/test";
import {
  billingFixtureKey,
  installBillingStoreFixture,
} from "../../scripts/native-billing-webview";
import { iosBillingFixtureSource } from "../../scripts/native-ios-billing";
import { fakeNative } from "./support/native";
import { registerFixture } from "./support/registration";

test.skip(
  process.env.MYCHEF_BILLING_UI_E2E !== "1",
  "Billing uses a separately built loopback fixture server.",
);
test.use({ serviceWorkers: "block", hasTouch: true, isMobile: true });

for (const scenario of [
  {
    platform: "ios",
    store: "apple",
    width: 320,
    lostResult: false,
    manage: "Open Apple subscriptions",
    href: "https://apps.apple.com/account/subscriptions",
  },
  {
    platform: "android",
    store: "play",
    width: 430,
    lostResult: true,
    manage: "Open Google Play subscriptions",
    href: "https://play.google.com/store/account/subscriptions",
  },
] as const) {
  test(`${scenario.platform} Subscription screen completes verified purchases on a narrow phone`, async ({
    page,
    context,
    playwright,
    baseURL,
  }) => {
    await page.setViewportSize({ width: scenario.width, height: 680 });
    const server = await playwright.request.newContext({ baseURL });
    const account = {
      firstName: "Billing",
      lastName: "Fixture",
      username: "billing-" + scenario.store,
      email: "billing-" + scenario.store + "@example.test",
      password: "A disposable billing fixture phrase!",
    };
    try {
      await registerFixture(page, account);
      await context.clearCookies();
      const native = await fakeNative(context, server, {
        platform: scenario.platform,
        billingFixture: true,
      });
      await page.goto("/app/");
      await page.getByLabel("Email", { exact: true }).waitFor();
      await page.evaluate(installBillingStoreFixture, {
        platform: scenario.platform,
        proofKey: billingFixtureKey(
          process.env.MYCHEF_TEST_INBOX_CAPABILITY ?? "",
          scenario.platform,
        ),
        expiresAt: Date.now() + 10 * 60_000,
        loseResult: scenario.lostResult,
      });
      await page.getByLabel("Email", { exact: true }).fill(account.email);
      await page.getByLabel("Password", { exact: true }).fill(account.password);
      await page
        .getByRole("button", { name: "Sign in to MyChef", exact: true })
        .click();
      await page.getByRole("button", { name: "Profile", exact: true }).click();
      await page.getByText("Subscription", { exact: true }).tap();
      const panel = page.locator("details.subscription-details");
      await expect(
        panel.getByText("€6.99 per month.", { exact: false }),
      ).toBeVisible();
      await expect(
        panel.getByText("€69.90 per year.", { exact: false }),
      ).toBeVisible();
      await page.addStyleTag({
        content: "html { font-size: 200% !important; }",
      });
      const subscribe = page.getByRole("button", {
        name: "Subscribe monthly",
        exact: true,
      });
      await subscribe.focus();
      await expect(subscribe).toBeFocused();
      await expect(subscribe).toBeInViewport();
      expect(
        await subscribe.evaluate((el) => {
          const box = el.getBoundingClientRect();
          return (
            box.width > 0 &&
            box.right <= window.innerWidth + 1 &&
            el.scrollWidth <= el.clientWidth + 1
          );
        }),
      ).toBe(true);
      await subscribe.tap();
      if (scenario.lostResult) {
        await expect(
          page
            .getByRole("status")
            .filter({ hasText: "Your purchase is still being checked." }),
        ).toBeVisible();
        await expect(subscribe).toHaveCount(0);
        await expect(panel.getByRole("link", { name: scenario.manage })).toHaveAttribute("href", scenario.href);
        await expect(panel.getByText(/Premium ended on/)).toHaveCount(0);
        await expect(
          panel.getByText(
            "If the store shows a charge, restore purchases using the same store account.",
            { exact: true },
          ),
        ).toBeVisible();
        await panel
          .getByRole("button", { name: "Get help with this purchase" })
          .tap();
        await expect(page).toHaveURL(/\bview=feedback\b/);
        await expect(page.getByText("My subscription", { exact: true })).toHaveCount(1);
        await page.goBack();
        await expect(panel).toBeVisible();
        if ((await panel.getAttribute("open")) === null) {
          await panel.getByText("Subscription", { exact: true }).tap();
        }
        // The store fixture yields no unfinished proofs on foreground. A
        // separate verified restore resolves the purchase on the real server
        // without producing an event in this app view.
        const receipt = await page.evaluate(async () => {
          const cap = (window as unknown as {
            Capacitor: {
              nativePromise(
                plugin: string,
                method: string,
                input: unknown,
              ): Promise<{ receipts: { receipt: string }[] }>;
            };
          }).Capacitor;
          const reply = await cap.nativePromise("MyChefNative", "billingRestore", {
            _callNonce: crypto.randomUUID(),
          });
          return reply.receipts[0]?.receipt;
        });
        const response = await server.post("/api/native/v1/billing/restore", {
          headers: { Authorization: native.access() },
          data: { receipt },
        });
        expect(response.status()).toBe(200);
        expect((await response.json()).completeStoreTransaction).toBe(true);
        await native.runtime({ active: false });
        await native.runtime({ active: true });
        await expect(
          panel.getByText("Premium · Monthly", { exact: true }),
        ).toBeVisible();
        await expect(
          panel.getByText("A purchase is pending verification.", { exact: true }),
        ).toHaveCount(0);
        await expect(
          panel.getByText(/Your purchase is still being checked/),
        ).toHaveCount(0);
        await page
          .getByRole("button", { name: "Restore purchases", exact: true })
          .tap();
      }
      await expect(
        page.getByRole("status").filter({ hasText: "Purchase verified." }),
      ).toBeVisible();
      await expect(
        page.getByText("Premium · Monthly", { exact: true }),
      ).toBeVisible();
      await expect(page.getByText(/^Paid through /)).toBeVisible();
      await expect(
        page.getByRole("link", { name: scenario.manage }),
      ).toHaveAttribute("href", scenario.href);
      await expect(subscribe).toHaveCount(0);
      await page.getByText("Account details", { exact: true }).tap();
      await page
        .getByRole("button", { name: "Delete my account", exact: true })
        .tap();
      const deletion = page.getByRole("dialog", { name: "Delete my account" });
      await expect(
        deletion.getByText(/Deleting MyChef does not stop store charges/),
      ).toBeVisible();
      await expect(
        deletion.getByRole("link", { name: scenario.manage }),
      ).toHaveAttribute("href", scenario.href);
      await expect(
        deletion.getByRole("link", {
          name:
            scenario.store === "apple"
              ? "Open Google Play subscriptions"
              : "Open Apple subscriptions",
        }),
      ).toHaveCount(0);
      expect(await page.evaluate(() => /fixture\.[A-Za-z0-9_-]{20,}\./.test(
        JSON.stringify([location.href, history.state, ...Object.values(sessionStorage), ...Object.values(localStorage)]),
      ))).toBe(false);
    } finally {
      await server.dispose();
    }
  });
}


test("the packaged simulator fixture completes and restores the same Apple proof", async ({
  page, context, playwright, baseURL,
}) => {
  await page.setViewportSize({ width: 390, height: 720 });
  const server = await playwright.request.newContext({ baseURL });
  const account = {
    firstName: "Simulator", lastName: "Fixture", username: "simulator-billing",
    email: "simulator-billing@example.test", password: "Disposable simulator test phrase!",
  };
  try {
    await registerFixture(page, account);
    await context.clearCookies();
    await fakeNative(context, server, { platform: "ios", billingFixture: true });
    await page.goto("/app/");
    const result = await page.evaluate(({ source, input }) => {
      // Chromium runs the exact function body that WKWebView receives in CI.
      const AsyncFunction = Object.getPrototypeOf(async () => {}).constructor;
      return new AsyncFunction("fixture", source)(input);
    }, {
      source: iosBillingFixtureSource(),
      input: {
        email: account.email,
        password: account.password,
        proofKey: billingFixtureKey(process.env.MYCHEF_TEST_INBOX_CAPABILITY ?? "", "ios"),
        expiresAt: Date.now() + 10 * 60_000,
      },
    });
    expect(result).toEqual({ terms: true, checkout: true, restore: true });
  } finally {
    await server.dispose();
  }
});
