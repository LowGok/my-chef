import { join } from "node:path";
import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./live-tests",
  workers: 1,
  retries: 0,
  timeout: 180_000,
  reporter: "list",
  preserveOutput: "never",
  outputDir: process.env.MYCHEF_LIVE_STATE
    ? join(process.env.MYCHEF_LIVE_STATE, "browser")
    : "../test-results/rewrite-live/browser",
  use: {
    baseURL: "http://127.0.0.1:18138",
    trace: "off",
    screenshot: "off",
    video: "off",
    launchOptions: { executablePath: process.env.MYCHEF_CHROMIUM },
  },
});
