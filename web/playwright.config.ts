import { randomBytes } from "node:crypto";
import { defineConfig } from "@playwright/test";

// The pinned runner otherwise writes an ARIA snapshot into error-context.md,
// even with screenshots, video and traces disabled. Private recipe tests must
// retain only their sanitized failure messages, never page content.
process.env.PLAYWRIGHT_NO_COPY_PROMPT = "1";
const containerFixture =
  process.env.MYCHEF_CONTAINER_FIXTURE === "1" &&
  process.env.MYCHEF_RUN_REWRITE_E2E === "1";

// All workers and the loopback fixture share a capability for this run only.
process.env.MYCHEF_TEST_INBOX_CAPABILITY ??=
  randomBytes(32).toString("base64url");

export default defineConfig({
  testDir: "./tests",
  testIgnore: containerFixture ? [] : ["**/container/**"],
  fullyParallel: false,
  workers: 1,
  timeout: 45_000,
  retries: 0,
  reporter: "list",
  preserveOutput: "never",
  outputDir: "../test-results/rewrite",
  use: {
    baseURL: containerFixture
      ? "http://127.0.0.1:18138"
      : "http://127.0.0.1:18137",
    trace: "off",
    screenshot: "off",
    video: "off",
    launchOptions: { executablePath: process.env.MYCHEF_CHROMIUM },
  },
  webServer: containerFixture
    ? undefined
    : {
        command: "python3 ../scripts/e2e_server.py",
        url: "http://127.0.0.1:18137/healthz",
        env: { MYCHEF_ADDR: "127.0.0.1:18137" },
        reuseExistingServer: false,
        gracefulShutdown: { signal: "SIGTERM", timeout: 20_000 },
        stderr: "pipe",
        stdout: "pipe",
      },
});
