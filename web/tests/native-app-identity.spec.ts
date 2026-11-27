import { expect, test } from "@playwright/test";
import { fakeNative } from "./support/native";
import {personalFixture,openPersonalLibrary} from "./support/personal-recipes";
import {
  shoppingAccount,
  shoppingEmail,
  shoppingPassword,
} from "./support/shopping";

test.use({
  serviceWorkers: "block",
  hasTouch: true,
  isMobile: true,
  viewport: { width: 320, height: 640 },
});

test("app links honor the unsaved editor guard and a warm pause keeps its draft",async({page,context,playwright})=>{
 await personalFixture(page,"identity-draft");await context.clearCookies();
 const server=await playwright.request.newContext({baseURL:"http://127.0.0.1:18137"});
 try{
  const os=await fakeNative(context,server,{identityLane:"app",identityActive:true});
  await page.goto("/app/");
  await page.getByLabel("Email",{exact:true}).fill("identity-draft@example.test");
  await page.getByLabel("Password",{exact:true}).fill("Garden personal recipe fixture 2026!");
  await page.getByRole("button",{name:"Sign in to MyChef",exact:true}).click();
  await page.getByRole("button",{name:"Profile",exact:true}).click();
  await openPersonalLibrary(page);
  await page.getByRole("button",{name:"Create a recipe",exact:true}).click();
  await page.getByLabel("Title",{exact:true}).fill("Unsent identity draft");
  const dialog=page.waitForEvent("dialog");
  const handoff=os.runtime({link:"https://mychef.dev/app/open"});
  const prompt=await dialog;await prompt.dismiss();await handoff;
  await expect(page.getByLabel("Title",{exact:true})).toHaveValue("Unsent identity draft");
  os.identityPolicy(false);await page.evaluate(()=>window.dispatchEvent(new Event("mychef-native-resume")));
  await expect(page.getByRole("heading",{name:"This MyChef app is paused.",exact:true})).toBeVisible();
  os.identityPolicy(true);await page.getByRole("button",{name:"Check again",exact:true}).click();
  await expect(page.getByLabel("Title",{exact:true})).toHaveValue("Unsent identity draft");
 }finally{await server.dispose();}
});

test("candidate starts paused, offers the fixed browser route and rechecks authorization", async ({
  page,
  context,
  playwright,
}) => {
  const server = await playwright.request.newContext({
    baseURL: "http://127.0.0.1:18137",
  });
  try {
    const os = await fakeNative(context, server, { identityLane: "app" });
    await page.goto("/app/");
    await expect(
      page.getByRole("heading", {
        name: "This MyChef app is paused.",
        exact: true,
      }),
    ).toBeVisible();
    await expect(page.getByLabel("Email", { exact: true })).toHaveCount(0);
    await page
      .getByRole("button", { name: "Open in browser", exact: true })
      .click();
    expect(os.links()).toEqual(["https://mychef.dev/app/"]);
    os.identityPolicy(true);
    await page
      .getByRole("button", { name: "Check again", exact: true })
      .click();
    await expect(
      page.getByRole("heading", { name: "Welcome back.", exact: true }),
    ).toBeVisible();
  } finally {
    await server.dispose();
  }
});

test("exact app link opens Meals after sign-in and cannot replace an active private proof", async ({
  page,
  context,
  playwright,
}) => {
  await shoppingAccount(page);
  await context.clearCookies();
  const server = await playwright.request.newContext({
    baseURL: "http://127.0.0.1:18137",
  });
  try {
    const os = await fakeNative(context, server, {
      identityLane: "app",
      identityActive: true,
    });
    await os.runtime({ link: "https://mychef.dev/app/open" });
    await page.goto("/app/");
    await page.getByLabel("Email", { exact: true }).fill(shoppingEmail);
    await page.getByLabel("Password", { exact: true }).fill(shoppingPassword);
    await page
      .getByRole("button", { name: "Sign in to MyChef", exact: true })
      .click();
    await page.getByRole("button", { name: "Profile", exact: true }).click();
    await os.runtime({ link: "https://mychef.dev/app/open" });
    await expect(page).toHaveURL(/\/app\/$/);
    const proof = "IDENTITY_PROOF_CANARY_".padEnd(43, "a");
    await os.runtime({
      link: `https://fixture-node.tailtest.ts.net:8977/app/reset-password#${proof}`,
    });
    await expect(
      page.getByRole("heading", { name: "Choose a new password", exact: true }),
    ).toBeVisible();
    await os.runtime({ link: "https://mychef.dev/app/open" });
    await expect(
      page.getByRole("heading", { name: "Choose a new password", exact: true }),
    ).toBeVisible();
    await os.runtime({ link: "https://mychef.dev/app/open?wrong=1" });
    await expect(
      page.getByRole("button", { name: "Open in browser", exact: true }),
    ).toBeVisible();
    expect(page.url()).not.toContain(proof);
  } finally {
    await server.dispose();
  }
});
