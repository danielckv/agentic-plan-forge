import { defineConfig, devices } from "@playwright/test";

/**
 * E2E runs Chromium only, from `web/e2e/`, against the built bundle and the
 * bridge harness: `npm run build && npm run test:e2e`.
 */

export default defineConfig({
  testDir: "./e2e",
  timeout: 120_000,
  retries: 0,
  workers: 1,
  reporter: "list",
  use: {
    ...devices["Desktop Chrome"],
    baseURL: process.env.PLAN_FORGE_UI_URL,
  },
  outputDir: "./test-results",
});
