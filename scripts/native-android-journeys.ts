// Exercises the installed debug APK through its real native adapters.
// The separate disposable instrumentation APK enables ADB inspection only
// during this test. No debugging flag is enabled in the delivered app config.
import {
  _android,
  expect as playwrightExpect,
  type AndroidDevice,
  type Locator,
  type Page,
} from "../web/node_modules/@playwright/test";
import { join } from "node:path";
import { readFile, unlink, writeFile } from "node:fs/promises";
import { safeJourneyError } from "./native-android-report";
import { addDays, monday } from "../web/src/dates";
import {
  fillPersonalDraft,
  personalPhoto,
} from "../web/tests/support/personal-recipes";

const app = "com.gkldevelopment.mychef.preview";
// Native requests and emulator UI work have the same bounded wait budget.
const expect = playwrightExpect.configure({ timeout: 30_000 });
const interruptedConnection =
  "The connection was interrupted. Check the request status before starting another menu.";
const state = process.argv[2];
const group = process.argv[3];
if (
  process.env.GITHUB_ACTIONS !== "true" ||
  process.env.GITHUB_REPOSITORY !== "LowGok/my-chef" ||
  process.env.NATIVE_PLATFORM !== "android" ||
  (group !== "core" &&
    group !== "media" &&
    group !== "pages" &&
    group !== "account") ||
  !state ||
  !/^\/.*mychef-installed-[A-Za-z0-9_-]+$/.test(state)
)
  throw new Error("Installed Android journey context is invalid.");
const account = JSON.parse(
  await readFile(join(state, "native-android-account.json"), "utf8"),
);
if (
  !account.email.endsWith("@example.test") ||
  typeof account.password !== "string"
)
  throw new Error("Expected a disposable synthetic account.");
const privateValues = [account.email, account.password];
const adbBinary = join(
  process.env.ANDROID_HOME ?? "/missing",
  "platform-tools/adb",
);
const adb = (...args: string[]) => {
  const result = Bun.spawnSync([adbBinary, "-s", "emulator-5554", ...args], {
    stdout: "pipe",
    stderr: "pipe",
  });
  if (result.exitCode) throw new Error("Disposable Android control failed.");
  return result.stdout.toString().trim();
};
let device: AndroidDevice | undefined;
let page: Page;
let stage = "connect";
const passed: string[] = [];
const browserErrors: string[] = [];
async function connect() {
  stage = "wake disposable Android emulator";
  adb("shell", "input", "keyevent", "KEYCODE_WAKEUP");
  adb("shell", "wm", "dismiss-keyguard");
  stage = "start disposable instrumentation";
  const probe = Bun.spawn(
    [
      adbBinary,
      "-s",
      "emulator-5554",
      "shell",
      "am",
      "instrument",
      "-w",
      "com.gkldevelopment.mychef.preview.test/com.gkldevelopment.mychef.testing.NativeJourneyProbe",
    ],
    { stdout: Bun.file(join(state, "probe-private.log")), stderr: "ignore" },
  );
  await expect
    .poll(
      async () => {
        const log = await readFile(
          join(state, "probe-private.log"),
          "utf8",
        ).catch(() => "");
        if (/INSTRUMENTATION_STATUS: probe=ready/.test(log)) return true;
        if (probe.exitCode !== null)
          throw new Error("Disposable instrumentation stopped before ready.");
        return false;
      },
      { timeout: 30_000 },
    )
    .toBe(true);
  stage = "discover Android device";
  const devices = await _android.devices();
  device = devices.find((value) => value.serial() === "emulator-5554");
  if (!device) throw new Error("Disposable emulator is unavailable.");
  stage = "discover instrumented WebView";
  const webview = await device.webView({ pkg: app }, { timeout: 30_000 });
  stage = "attach Android WebView";
  page = await webview.page();
  page.on("pageerror", (error) => {
    browserErrors.push(
      safeJourneyError(error, "browser exception", privateValues),
    );
    if (browserErrors.length > 4) browserErrors.shift();
  });
  page.setDefaultTimeout(30_000);
  stage = "open embedded interface";
  await page.waitForLoadState("domcontentloaded");
}

async function check(name: string, run: () => Promise<void>) {
  stage = name;
  await run();
  passed.push(name);
  console.log("Installed Android check passed: " + name);
}
const button = (name: string) =>
  page.getByRole("button", { name, exact: true });
let controlSequence = 0;
function probeCommand(value: string) {
  if (
    !/^(pinch|activate:[1-9][0-9]{0,3}:[A-Za-z -]{1,50}|document:[1-9][0-9]{0,3}:(?:SAVE|native-(?:dish|page)\.png)|accessibility:[1-9][0-9]{0,3}:(?:nav|export-dialog))$/.test(
      value,
    )
  )
    throw new Error("Invalid disposable probe command.");
  const result = Bun.spawnSync(
    [
      adbBinary,
      "-s",
      "emulator-5554",
      "shell",
      "-T",
      "run-as",
      app,
      "tee",
      "cache/native-check-pending",
    ],
    { stdin: Buffer.from(value), stdout: "ignore", stderr: "ignore" },
  );
  if (result.exitCode)
    throw new Error("Disposable probe command could not be written.");
  adb(
    "shell",
    "run-as",
    app,
    "mv",
    "cache/native-check-pending",
    "cache/native-check-command",
  );
}
async function probeAction(
  kind: "activate" | "document" | "accessibility",
  name: string,
) {
  const sequence = String(++controlSequence);
  probeCommand(kind + ":" + sequence + ":" + name);
  await expect
    .poll(
      () => {
        try {
          return adb("shell", "run-as", app, "cat", "cache/native-check-done");
        } catch {
          return "";
        }
      },
      {
        timeout: 40_000,
        message: "Disposable Android probe: " + kind + " " + name,
      },
    )
    .toBe(sequence);
}
async function activateControl(control: Locator, name: string) {
  await expect(
    control,
    name + " must be visible for a native tap",
  ).toBeVisible();
  await expect(
    control,
    name + " must be enabled for a native tap",
  ).toBeEnabled();
  if (name !== "Current password") {
    await page.evaluate(() => {
      const field = document.activeElement;
      if (
        field instanceof HTMLInputElement ||
        field instanceof HTMLTextAreaElement ||
        field instanceof HTMLSelectElement
      )
        field.blur();
    });
    try {
      await expect.poll(keyboardShown, { timeout: 1_000 }).toBe(false);
    } catch {
      if (keyboardShown()) adb("shell", "input", "keyevent", "4");
      await expect.poll(keyboardShown).toBe(false);
    }
  }
  // Center the control after Android has dismissed the keyboard. Check the
  // WebView's hit target, then let instrumentation send the actual OS touch.
  await control.evaluate((node) =>
    node.scrollIntoView({
      block: "center",
      inline: "nearest",
      behavior: "instant",
    }),
  );
  await expect
    .poll(
      () =>
        control.evaluate((node) => {
          const rect = node.getBoundingClientRect();
          const target = document.elementFromPoint(
            rect.left + rect.width / 2,
            rect.top + rect.height / 2,
          );
          return Boolean(target && node.contains(target));
        }),
      { message: name + " must own its center point in the native WebView" },
    )
    .toBe(true);
  await probeAction("activate", name);
}

const activate = (name: string) => activateControl(button(name), name);
async function profile() {
  await button("Profile").click();
  await expect(
    page.getByRole("heading", { name: "Your profile.", exact: true }),
  ).toBeVisible();
}
async function openLibrary() {
  const summary = page.locator("summary").filter({ hasText: /^My recipes$/ });
  if (
    !(await summary.evaluate(
      (node) => (node.parentElement as HTMLDetailsElement).open,
    ))
  )
    await summary.click();
  await button("Open my recipes").click();
  await expect(
    page.getByRole("heading", { name: "My recipes", exact: true }),
  ).toBeVisible();
}
async function accountDetails() {
  const summary = page
    .locator("summary")
    .filter({ hasText: /^Account details$/ });
  if (
    !(await summary.evaluate(
      (node) => (node.parentElement as HTMLDetailsElement).open,
    ))
  )
    await summary.click();
}
const keyboardShown = () =>
  /mInputShown=true|isInputViewShown=true/.test(
    adb("shell", "dumpsys", "input_method"),
  );
async function login(password = account.password) {
  await page.getByLabel("Email", { exact: true }).fill(account.email);
  await page.getByLabel("Password", { exact: true }).fill(password);
  await button("Sign in to MyChef").click();
  await expect(button("Profile")).toBeVisible();
}
async function picker(
  target: "CANCEL" | "SAVE" | "native-dish.png" | "native-page.png",
) {
  if (target !== "CANCEL") {
    try {
      await probeAction("document", target);
    } catch (error) {
      const result = Bun.spawnSync(
        ["python3", "scripts/native_android_probe.py", "picker-focus"],
        { stdout: "pipe", stderr: "ignore" },
      );
      const focus = result.stdout.toString().trim();
      throw new Error(
        "Disposable Android picker did not complete; focus: " +
          (/^(app|documents|system-ui-anr|other|unknown)$/.test(focus)
            ? focus
            : "unknown"),
        { cause: error },
      );
    }
    return;
  }
  const result = Bun.spawnSync(
    ["python3", "scripts/native_android_probe.py", "cancel-picker"],
    { stdout: "pipe", stderr: "ignore" },
  );
  if (result.exitCode) {
    const details = result.stdout
      .toString()
      .trim()
      .match(
        /^(adb-missing|adb-timeout|adb-command-failed|picker-timeout):(app|documents|system-ui-anr|other|unknown)$/,
      );
    throw new Error(
      "Disposable picker cancellation failed" +
        (details ? "; " + details[1] + "; focus: " + details[2] : ""),
    );
  }
}
const choosePhoto = (label: "Choose recipe photo" | "Recipe-page photos") =>
  activateControl(page.getByLabel(label, { exact: true }), label);
async function stagePhoto(
  path: string,
  name: "native-dish.png" | "native-page.png",
) {
  // Seed through Android's Downloads provider as the emulator's shell user;
  // the app itself does not own the file or receive any extra permissions.
  const downloads = "content://media/external_primary/downloads";
  adb(
    "shell",
    "content",
    "insert",
    "--uri",
    downloads,
    "--bind",
    "_display_name:s:" + name,
    "--bind",
    "mime_type:s:image/png",
    "--bind",
    "relative_path:s:Download/",
  );
  const rows = adb(
    "shell",
    "content",
    "query",
    "--uri",
    downloads,
    "--projection",
    "_id:_display_name",
  )
    .split("\n")
    .map((line) =>
      line.match(
        /\b_id=([1-9]\d*),\s*_display_name=(native-(?:dish|page)\.png)\s*$/,
      ),
    )
    .filter((row) => row?.[2] === name);
  if (rows.length !== 1)
    throw new Error("Android Downloads did not register one synthetic photo.");
  const item = downloads + "/" + rows[0]![1];
  const bytes = Buffer.from(await Bun.file(path).arrayBuffer());
  const written = Bun.spawnSync(
    [
      adbBinary,
      "-s",
      "emulator-5554",
      "shell",
      "-T",
      "content",
      "write",
      "--uri",
      item,
    ],
    { stdin: bytes, stdout: "ignore", stderr: "ignore" },
  );
  if (written.exitCode)
    throw new Error(
      "Could not write the synthetic photo to Android Downloads.",
    );
  const received = Bun.spawnSync(
    [
      adbBinary,
      "-s",
      "emulator-5554",
      "exec-out",
      "content",
      "read",
      "--uri",
      item,
    ],
    { stdout: "pipe", stderr: "ignore" },
  );
  if (received.exitCode || !received.stdout.equals(bytes))
    throw new Error(
      "Android Downloads did not retain the exact synthetic photo bytes.",
    );
}
async function extras(name: string) {
  const disclosure = page.locator(".shopping-add");
  if ((await disclosure.getAttribute("open")) === null)
    await disclosure.locator(":scope > summary").click();
  await page.getByLabel("Extra item", { exact: true }).fill(name);
  await activate("Add extra");
}
async function reopenShopping(phase: string, first: string, second: string) {
  adb("shell", "am", "force-stop", app);
  await device?.close().catch(() => undefined);
  await connect();
  stage = phase;
  await button("Shopping list").click();
  await expect(page.locator(".shopping-heading")).toBeVisible();
  const restored = await shoppingWeek();
  expect([first, second]).toContain(restored);
  if (restored === second) await button("Previous shopping week").click();
  await expect.poll(shoppingWeek).toBe(first);
}

const shoppingWeek = () => page.locator(".shopping-week strong").innerText();
const shoppingWeekInUrl = () =>
  page.evaluate(
    () => new URLSearchParams(window.location.search).get("week") ?? "",
  );

async function coreJourneys() {
  await check("native sign-in and preferences", async () => {
    await login();
    await profile();
    await page.getByLabel("Diet", { exact: true }).fill("Vegetarian");
    expect(
      await button("Save preferences").evaluate(
        (node) => (node as HTMLButtonElement).form?.checkValidity() ?? false,
      ),
      "Preferences form is attached and valid before submission",
    ).toBe(true);
    const activation = await button("Save preferences").evaluateHandle(
      (node) => {
        const result = {
          clicked: false,
          submitted: false,
          touch: false,
          trusted: false,
          bubbles: false,
          prevented: false,
        };
        node.addEventListener(
          "pointerdown",
          (event) => {
            result.touch = (event as PointerEvent).pointerType === "touch";
            result.trusted = event.isTrusted;
          },
          { once: true },
        );
        node.addEventListener(
          "click",
          () => {
            result.clicked = true;
          },
          { once: true },
        );
        (node as HTMLButtonElement).form?.addEventListener(
          "submit",
          (event) => {
            result.submitted = true;
            result.bubbles = event.bubbles;
            // Read the final dispatch state after React's delegated listener.
            setTimeout(() => {
              result.prevented = event.defaultPrevented;
            }, 0);
          },
          { once: true },
        );
        return result;
      },
    );
    try {
      await activate("Save preferences");
      await expect(
        page.getByText("Preferences saved.", { exact: true }),
      ).toBeVisible();
      expect(await activation.jsonValue()).toEqual({
        clicked: true,
        submitted: true,
        touch: true,
        trusted: true,
        bubbles: true,
        prevented: true,
      });
    } catch (error) {
      const flags = await activation.jsonValue().catch(() => null);
      if (!flags) throw error;
      const problems = [];
      for (const [code, text] of [
        [
          "storage",
          "Secure storage is unavailable. Unlock your device and try again.",
        ],
        ["connection", interruptedConnection],
        ["request", "The request could not be completed. Please try again."],
        ["session", "Please sign in."],
      ]) {
        if (await page.getByText(text, { exact: true }).count())
          problems.push(code);
      }
      const busy = await page
        .locator(".preferences-details button[type=submit]")
        .isDisabled({ timeout: 1_000 })
        .catch(() => null);
      throw new Error(
        "Preferences confirmation missing: " +
          JSON.stringify({ ...flags, busy, problems }),
      );
    } finally {
      await activation.dispose().catch(() => undefined);
    }
  });
  await check("planning recipe detail rating and favorite", async () => {
    await button("Meals").click();
    await button("Plan a menu").click();
    await page.getByLabel("Number of meals", { exact: true }).fill("2");
    await activate("Review menu");
    try {
      await expect(button("Plan my week")).toBeVisible();
    } catch (error) {
      if (await page.getByText(interruptedConnection, { exact: true }).count())
        throw new Error("Planner review stopped on a connection interruption.");
      if (await page.getByRole("alert").count())
        throw new Error(
          "Planner review displayed a validation or request error.",
        );
      throw error;
    }
    await activate("Plan my week");
    const recipe = page
      .getByRole("button", { name: /^Open Lemon garden pasta$/ })
      .first();
    await expect(recipe).toBeVisible({ timeout: 60_000 });
    await recipe.click();
    await expect(
      page.getByText("Simmer the vegetables gently for ten minutes.", {
        exact: true,
      }),
    ).toBeVisible();
    await button("Add to favorites").click();
    await expect(button("Remove from favorites")).toBeVisible();
    const rating = page.getByLabel("Rating", { exact: true });
    await expect(rating).toBeEnabled();
    await rating.selectOption("5");
    await activate("Save rating");
    await expect(
      page.getByText(
        "Rating saved. Save this reference if you need it later.",
        {
          exact: true,
        },
      ),
    ).toBeVisible();
  });
  await check(
    "two shopping weeks survive offline process relaunch",
    async () => {
      await button("Shopping list").click();
      await expect(page.getByText("Saved", { exact: true })).toBeVisible();
      const first = await shoppingWeek();
      expect(first.trim()).not.toBe("");
      await expect.poll(shoppingWeekInUrl).toMatch(/^\d{4}-\d{2}-\d{2}$/);
      const firstWeek = await shoppingWeekInUrl();
      await button("Next shopping week").click();
      await expect(page.getByText("Saved", { exact: true })).toBeVisible();
      await expect.poll(shoppingWeek).not.toBe(first);
      const second = await shoppingWeek();
      expect(second.trim()).not.toBe("");
      const secondWeek = await shoppingWeekInUrl();
      expect(secondWeek).toMatch(/^\d{4}-\d{2}-\d{2}$/);
      expect(secondWeek).not.toBe(firstWeek);
      await button("Previous shopping week").click();
      await expect.poll(shoppingWeek).toBe(first);
      await expect.poll(shoppingWeekInUrl).toBe(firstWeek);
      await expect(page.getByText("Saved", { exact: true })).toBeVisible();
      adb("shell", "svc", "wifi", "disable");
      adb("shell", "svc", "data", "disable");
      await writeFile(join(state, "native-network-offline"), "offline");
      await reopenShopping("offline shopping before editing", first, second);
      await expect.poll(shoppingWeekInUrl).toBe(firstWeek);
      await extras("Native offline coffee");
      const status = page.locator(".shopping-progress [role=status]");
      await expect(status).toHaveText("Pending sync · 1 change");
      await button("Next shopping week").click();
      await expect.poll(shoppingWeekInUrl).toBe(secondWeek);
      await extras("Native offline tea");
      await expect(status).toHaveText("Pending sync · 2 changes");
      await reopenShopping(
        "offline shopping after durable edits",
        first,
        second,
      );
      const tea = page.getByText("Native offline tea", { exact: true });
      await expect(
        page.getByText("Native offline coffee", { exact: true }),
      ).toBeVisible();
      await button("Next shopping week").click();
      await expect(tea).toBeVisible();
      stage = "foreground reconnect synchronizes both shopping weeks";
      // Profile content is deliberately unavailable until we authenticate
      // again. Select the route while offline, then reconnect on that route.
      await button("Profile").click();
      await expect(button("Profile")).toHaveAttribute("aria-current", "page");
      await unlink(join(state, "native-network-offline"));
      adb("shell", "svc", "wifi", "enable");
      adb("shell", "svc", "data", "enable");
      adb("shell", "input", "keyevent", "3");
      adb("shell", "am", "start", "-n", app + "/.MainActivity");
      await expect(
        page.getByRole("heading", { name: "Your profile.", exact: true }),
      ).toBeVisible();
      await expect
        .poll(
          async () => {
            try {
              return (
                await readFile(
                  join(state, "native-shopping-synced.json"),
                  "utf8",
                )
              ).includes('"bothWeeks":true');
            } catch {
              return false;
            }
          },
          { timeout: 45_000 },
        )
        .toBe(true);
    },
  );
  await check(
    "shopping conflict preserves both writers without duplicates",
    async () => {
      await button("Shopping list").click();
      const status = page.locator(".shopping-progress [role=status]");
      await expect(status).toHaveText("Saved");
      await writeFile(
        join(state, "native-shopping-conflict"),
        "one concurrent revision",
      );
      await extras("Native conflict rice");
      await expect(page.locator(".shopping [role=alert]")).toContainText(
        "This list or its meals changed. Review the latest list and retry your pending changes.",
      );
      await expect(
        page.getByText("Native remote salt", { exact: true }),
      ).toBeVisible();
      await button("Reviewed latest list · Retry my changes").click();
      await expect(status).toHaveText("Saved");
      await expect(
        page.getByText("Native remote salt", { exact: true }),
      ).toHaveCount(1);
      await expect(
        page.getByText("Native conflict rice", { exact: true }),
      ).toHaveCount(1);
      await profile();
    },
  );
  await check("feedback receipt", async () => {
    await button("Provide your feedback").click();
    await page.getByLabel("Contact reason").selectOption("other");
    await page
      .getByLabel("Message", { exact: true })
      .fill("A synthetic native Android usability check.");
    await activate("Send feedback");
    await expect(
      page.getByRole("heading", { name: "Thanks for your feedback." }),
    ).toBeVisible();
    await profile();
  });
  await check("password change sign-out and sign-in", async () => {
    await accountDetails();
    await page
      .locator("summary")
      .filter({ hasText: /^Change password$/ })
      .click();
    const change = page.locator(".password-change-form");
    const nextPassword = account.password + " refreshed";
    privateValues.push(nextPassword);
    await change
      .getByLabel("Current password", { exact: true })
      .fill(account.password);
    await change.getByLabel("New password", { exact: true }).fill(nextPassword);
    await change
      .getByLabel("Confirm new password", { exact: true })
      .fill(nextPassword);
    await activate("Save password");
    await expect(
      page.getByText("Password changed. Other devices need to sign in again.", {
        exact: true,
      }),
    ).toBeVisible();
    account.password = nextPassword;
    await button("Sign out").click();
    await expect(
      page.getByRole("heading", { name: "Welcome back.", exact: true }),
    ).toBeVisible();
    await login();
    await profile();
  });
  await check("OS back confirms a dirty recipe draft", async () => {
    await openLibrary();
    await button("Create a recipe").click();
    const title = page.getByLabel("Title", { exact: true });
    await title.fill("Unsaved native dish");
    await title.blur();
    const draftURL = page.url();
    if (keyboardShown()) {
      adb("shell", "input", "keyevent", "4");
      await expect.poll(keyboardShown).toBe(false);
    }
    const prompt = page.getByRole("dialog", {
      name: "Discard unsaved recipe changes?",
      exact: true,
    });
    adb("shell", "input", "keyevent", "4");
    await expect(prompt).toBeVisible();
    await expect(page).toHaveURL(draftURL);
    await prompt.getByRole("button", { name: "Keep editing" }).click();
    await expect(prompt).not.toBeVisible();
    await expect(title).toHaveValue("Unsaved native dish");
    await title.blur();
    await expect.poll(keyboardShown).toBe(false);
    adb("shell", "input", "keyevent", "4");
    await expect(prompt).toBeVisible();
    await prompt.getByRole("button", { name: "Discard changes" }).click();
    await expect(
      page.getByRole("heading", { name: "Your profile.", exact: true }),
    ).toBeVisible();
  });
}

async function mediaJourneys() {
  await login();
  await profile();
  await check("OS photo picker cancellation retains the draft", async () => {
    await openLibrary();
    await button("Create a recipe").click();
    await fillPersonalDraft(page);
    await choosePhoto("Choose recipe photo");
    await picker("CANCEL");
    await expect(
      page.getByLabel("Choose recipe photo", { exact: true }),
    ).toHaveValue("Choose photos");
    await expect(page.getByLabel("Title", { exact: true })).toHaveValue(
      "Private fixture supper",
    );
    await expect(
      page.getByAltText("Selected recipe photo preview"),
    ).toHaveCount(0);
  });
  await check("OS photo picker attaches a dish photo", async () => {
    const image = join(state, "native-dish.png");
    await writeFile(image, personalPhoto);
    await stagePhoto(image, "native-dish.png");
    await choosePhoto("Choose recipe photo");
    await picker("native-dish.png");
    await expect(
      page.getByAltText("Selected recipe photo preview"),
    ).toBeVisible();
  });
  await check(
    "dish photo survives uncertain save retry and later edit",
    async () => {
      const droppedResponse = join(state, "native-personal-save-drop");
      await writeFile(droppedResponse, "one disposable committed response");
      await activate("Save");
      await expect(button("Retry save")).toBeVisible();
      expect(await Bun.file(droppedResponse).exists()).toBe(false);
      await expect(page.locator(".personal-photo-preview img")).toBeVisible();
      await activate("Retry save");
      await expect(page.locator(".personal-detail img")).toHaveAttribute(
        "src",
        /^blob:/,
      );
      await button("Edit recipe").click();
      await page
        .getByLabel("Title", { exact: true })
        .fill("Native edited supper");
      await activate("Save");
      await expect(page.locator(".personal-detail img")).toHaveAttribute(
        "src",
        /^blob:/,
      );
      await button("Back to my recipes").click();
      await expect(page.locator(".personal-row")).toHaveCount(1);
    },
  );
  await check("recipe URL import review and save", async () => {
    await button("Import recipe").click();
    await page
      .getByLabel("Recipe URL", { exact: true })
      .fill("https://recipes.example.test/carrot-soup");
    await page
      .getByRole("checkbox", {
        name: "I have permission to import this recipe for my personal use.",
        exact: true,
      })
      .check();
    await activate("Check page");
    await expect(page.getByLabel("Title", { exact: true })).toBeVisible({
      timeout: 60_000,
    });
    for (const check of await page
      .getByRole("group", { name: "Review imported recipe", exact: true })
      .getByRole("checkbox")
      .all())
      await check.check();
    await activate("Save");
    await expect(page.locator(".personal-detail")).toBeVisible();
  });
  await check(
    "imported shopping stays in memory across offline relaunch",
    async () => {
      const date = await page.evaluate(() => {
        const local = new Date();
        return [
          local.getFullYear(),
          String(local.getMonth() + 1).padStart(2, "0"),
          String(local.getDate()).padStart(2, "0"),
        ].join("-");
      });
      const importedWeek = addDays(monday(date), 7);
      await button("Add to next week").click();
      await expect(
        page.getByRole("status").filter({ hasText: "Added to next week." }),
      ).toBeVisible();
      await button("Meals").click();
      await button("Plan a menu").click();
      await page
        .getByLabel("First meal date", { exact: true })
        .fill(importedWeek);
      await page.getByLabel("Number of meals", { exact: true }).fill("1");
      await activate("Review menu");
      await expect(
        page
          .locator(".planner li")
          .filter({ hasText: "Carrot soup" })
          .locator(".personal-version")
          .first(),
      ).toBeVisible();
      await activate("Plan my week");
      await expect(button("Open Carrot soup")).toBeVisible({ timeout: 60_000 });
      await button("Shopping list").click();
      await expect.poll(shoppingWeekInUrl).toBe(importedWeek);
      const tabNotice = () =>
        page.getByText(
          "This list stays in this tab. Keep it open until changes are saved.",
          { exact: true },
        );
      const stock = () => page.getByText(/^vegetable stock$/i);
      const extra = "Temporary imported-list nutmeg";
      await expect(tabNotice()).toBeVisible();
      await expect(stock()).toBeVisible();
      const marker = join(state, "native-network-offline");
      await writeFile(marker, "offline");
      try {
        adb("shell", "svc", "wifi", "disable");
        adb("shell", "svc", "data", "disable");
        await expect(
          page.getByText(
            "Offline · Keep this tab open until your changes are saved.",
            { exact: true },
          ),
        ).toBeVisible();
        await extras(extra);
        await expect(page.getByText(extra, { exact: true })).toBeVisible();
        await expect(
          page.locator(".shopping-progress [role=status]"),
        ).toHaveText("Pending sync · 1 change");
        adb("shell", "am", "force-stop", app);
        await device?.close().catch(() => undefined);
        await connect();
        stage = "imported shopping after offline relaunch";
        await expect(
          page.getByText(
            "Connect to load your shopping list for the first time.",
            {
              exact: true,
            },
          ),
        ).toBeVisible();
        await expect(button("Shopping list")).toHaveCount(0);
        await expect(stock()).toHaveCount(0);
        await expect(page.getByText(extra, { exact: true })).toHaveCount(0);
      } finally {
        await unlink(marker);
        adb("shell", "svc", "wifi", "enable");
        adb("shell", "svc", "data", "enable");
      }
      stage = "imported shopping reloads only after reconnect";
      adb("shell", "input", "keyevent", "3");
      adb("shell", "am", "start", "-n", app + "/.MainActivity");
      await button("Shopping list").click();
      await expect(page.locator(".shopping-heading")).toBeVisible();
      for (let step = 0; step < 3; step++) {
        const shown = await shoppingWeekInUrl();
        expect(shown).toMatch(/^\d{4}-\d{2}-\d{2}$/);
        if (shown === importedWeek) break;
        await button(
          shown < importedWeek
            ? "Next shopping week"
            : "Previous shopping week",
        ).click();
        await expect.poll(shoppingWeekInUrl).not.toBe(shown);
      }
      await expect.poll(shoppingWeekInUrl).toBe(importedWeek);
      await expect(tabNotice()).toBeVisible();
      await expect(stock()).toBeVisible();
      await expect(page.getByText(extra, { exact: true })).toHaveCount(0);
      await profile();
    },
  );
}

async function pageImportJourney() {
  await login();
  await profile();
  await check(
    "system recipe-page picker consent and source/dish separation",
    async () => {
      await openLibrary();
      await button("Import recipe").click();
      await button("Recipe pages").click();
      const encoded = await page.evaluate(() => {
        const canvas = document.createElement("canvas");
        canvas.width = 640;
        canvas.height = 480;
        const context = canvas.getContext("2d")!;
        context.fillStyle = "white";
        context.fillRect(0, 0, 640, 480);
        context.fillStyle = "black";
        context.font = "28px sans-serif";
        [
          "Carrot soup",
          "2 portions",
          "2 carrots, water, olive oil",
          "Chop carrots. Simmer for 20 minutes.",
        ].forEach((text, index) => context.fillText(text, 24, 60 + index * 70));
        return canvas.toDataURL("image/png").split(",")[1];
      });
      const source = join(state, "native-page.png");
      await writeFile(source, Buffer.from(encoded, "base64"));
      await stagePhoto(source, "native-page.png");
      stage = "choose and confirm recipe page in Android picker";
      await choosePhoto("Recipe-page photos");
      await picker("native-page.png");
      stage = "check selected recipe page before consent";
      await expect(
        page.getByLabel("Recipe-page photos", { exact: true }),
      ).toHaveValue("1 photo selected · Change");
      stage = "review and save imported recipe page";
      await page
        .getByRole("checkbox", {
          name: "I have permission to import this recipe for my personal use.",
          exact: true,
        })
        .check();
      await page
        .getByRole("checkbox", {
          name: "I agree to send the recipe text or pages to Gemini for reading.",
          exact: true,
        })
        .check();
      await activate("Read recipe pages");
      await expect(page.getByLabel("Title", { exact: true })).toBeVisible({
        timeout: 60_000,
      });
      await expect(page.locator(".personal-photo-preview img")).toHaveCount(0);
      for (const field of await page
        .getByRole("group", { name: "Review imported recipe", exact: true })
        .getByRole("checkbox")
        .all())
        await field.check();
      await activate("Save");
      await expect(page.locator(".personal-detail")).toBeVisible();
      await expect(page.locator(".personal-detail img")).toHaveCount(0);
      await expect(
        page.getByText("Imported from recipe pages.", { exact: true }),
      ).toBeVisible();
      adb("shell", "test", "-f", "/sdcard/Download/native-page.png");
      await profile();
    },
  );
}

async function accountJourneys() {
  await login();
  await profile();
  await check("OS export cancel and save", async () => {
    await accountDetails();
    for (const save of [false, true]) {
      await button("Download my data").click();
      const dialog = page.getByRole("dialog", {
        name: "Download my data",
        exact: true,
      });
      await dialog
        .getByLabel("Current password", { exact: true })
        .fill(account.password);
      await activate("Download ZIP");
      if (save) await picker("SAVE");
      else await picker("CANCEL");
      await expect(
        page.getByText(
          save
            ? "Your download is ready. Keep the ZIP private."
            : "Download cancelled.",
          { exact: true },
        ),
      ).toBeVisible();
    }
  });
  await check("readable inputs large text and back gestures", async () => {
    await button("Download my data").click();
    const dialog = page.getByRole("dialog", {
      name: "Download my data",
      exact: true,
    });
    const input = dialog.getByLabel("Current password", { exact: true });
    await probeAction("accessibility", "export-dialog");
    const initialScale = await page.evaluate(() => visualViewport?.scale ?? 1);
    await activateControl(input, "Current password");
    await expect(input).toBeFocused();
    expect(
      await input.evaluate((node) =>
        parseFloat(getComputedStyle(node).fontSize),
      ),
    ).toBeGreaterThanOrEqual(16);
    expect(
      await page.evaluate(() => visualViewport?.scale ?? 1),
    ).toBeLessThanOrEqual(initialScale + 0.05);
    await expect.poll(keyboardShown).toBe(true);
    adb("shell", "input", "keyevent", "4");
    await expect.poll(keyboardShown).toBe(false);
    await expect(dialog).toBeVisible();
    adb("shell", "input", "keyevent", "4");
    await expect(dialog).not.toBeVisible();
    await page.evaluate(() => {
      document.documentElement.style.fontSize = "200%";
    });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.evaluate(() => {
      document.documentElement.style.fontSize = "";
      window.scrollTo(0, 0);
    });
    await probeAction("accessibility", "nav");
  });
  await check("permanent account deletion clears native access", async () => {
    await button("Delete my account").click();
    const dialog = page.getByRole("dialog", {
      name: "Delete my account",
      exact: true,
    });
    await dialog
      .getByLabel("Current password", { exact: true })
      .fill(account.password);
    await dialog
      .getByLabel("Type DELETE to confirm", { exact: true })
      .fill("DELETE");
    await activate("Permanently delete account");
    await expect(
      page.getByRole("heading", { name: "Welcome back.", exact: true }),
    ).toBeVisible();
    await expect(button("Profile")).toHaveCount(0);
  });
  await check("actual two-pointer pinch gesture", async () => {
    const before = await page.evaluate(() => visualViewport?.scale ?? 1);
    probeCommand("pinch");
    await expect
      .poll(() => page.evaluate(() => visualViewport?.scale ?? 1), {
        timeout: 10_000,
      })
      .toBeGreaterThan(before + 0.1);
  });
}

try {
  await connect();
  await {
    core: coreJourneys,
    media: mediaJourneys,
    pages: pageImportJourney,
    account: accountJourneys,
  }[group]();
  await writeFile(
    join(state, `native-android-journeys-${group}.json`),
    JSON.stringify(
      {
        group,
        passed,
      },
      null,
      2,
    ),
  );
} catch (error) {
  for (const browserError of browserErrors) console.error(browserError);
  throw new Error(safeJourneyError(error, stage, privateValues));
} finally {
  try {
    adb("shell", "run-as", app, "touch", "cache/native-check-finish");
  } catch {}
  await device?.close().catch(() => undefined);
}
