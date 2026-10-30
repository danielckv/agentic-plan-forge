import { defineConfig, devices } from "@playwright/test";

/**
 * E2E runs Chromium only, from `web/e2e/`, against the built bundle and the
 * bridge harness: `npm run build && npm run test:e2e`.
 *
 * The preview server below is started by Playwright itself, so `app.spec.ts`
 * runs without anyone remembering to set PLAN_FORGE_UI_URL. It used to be set
 * by hand, which meant those cases silently skipped in CI and locally — three
 * assertions that never ran. `reuseExistingServer` keeps a preview you already
 * have running instead of fighting it for the port.
 *
 * `bridge.spec.ts` ignores baseURL entirely: it boots its own server and
 * enforces the CSP and handshake there.
 */
const PORT = 4173;
const EXTERNAL_URL = process.env.PLAN_FORGE_UI_URL;
const BASE_URL = EXTERNAL_URL ?? `http://localhost:${PORT}`;

export default defineConfig({
  testDir: "./e2e",
  timeout: 120_000,
  retries: 0,
  workers: 1,
  reporter: "list",
  use: {
    ...devices["Desktop Chrome"],
    baseURL: BASE_URL,
  },
  // The property is always present. `exactOptionalPropertyTypes` is on, so an
  // explicit `undefined` is not assignable to webServer — assigning the whole
  // object conditionally is what the type accepts.
  ...(EXTERNAL_URL
    ? {}
    : {
        webServer: {
          command: `npx vite preview --port ${PORT} --strictPort`,
          url: BASE_URL,
          reuseExistingServer: !process.env.CI,
          timeout: 60_000,
        },
      }),
  outputDir: "./test-results",
});
