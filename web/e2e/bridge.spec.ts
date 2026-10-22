import { expect, test } from "@playwright/test";

import { startBridgeHarness } from "./bridge-harness.mjs";
import type { BridgeHarness } from "./bridge-harness.mjs";

/**
 * The embedded bundle, served under the production Content-Security-Policy and
 * the real loopback handshake.
 *
 * This is the gate the suite was missing. `serve-ui --check` fetches bytes and
 * never runs them, and `scripts/preview.mjs` injects a relay URL, skips the
 * handshake and sends no CSP — so a bundle that cannot boot inside the
 * dedicated window passed every check while the window stayed blank.
 */

let harness: BridgeHarness;
let pageErrors: string[];

test.beforeAll(async () => {
  harness = await startBridgeHarness();
});

test.afterAll(async () => {
  await harness.close();
});

test.beforeEach(({ page }) => {
  pageErrors = [];
  page.on("pageerror", (err) => pageErrors.push(String(err)));
});

/** The URL the Go bridge hands to the window it launches. */
function launchURL(): string {
  return `${harness.origin}/#n=${harness.mintNonce()}`;
}

test("the bundle boots under the bridge CSP", async ({ page }) => {
  await page.goto(launchURL());

  // An uncaught error during module evaluation leaves #root empty and the
  // window blank; assert on the error too so a failure names the cause.
  await expect(page.locator("#root")).not.toBeEmpty();
  await expect(page.getByText("Plan Forge").first()).toBeVisible();
  expect(pageErrors).toEqual([]);
});

test("the window completes the handshake and reaches the authority", async ({ page }) => {
  await page.goto(launchURL());

  // "Connected" renders only once the socket negotiated plan-forge.v1 and the
  // bridge accepted the auth frame. Exact matching keeps "Disconnected" out.
  await expect(page.getByText("Connected", { exact: true }).first()).toBeVisible();
  // The authority's STATE_SNAPSHOT reached the store: the top bar names the
  // role it assigned.
  await expect(page.locator('.pf-conn[title="You are the owner"]')).toBeVisible();
  // HELLO followed the auth frame on the same socket.
  await expect
    .poll(() => harness.received.some((f) => f.includes('"event_type":"HELLO"')), { timeout: 10_000 })
    .toBe(true);
  expect(pageErrors).toEqual([]);
});

test("the launch nonce never stays in the URL", async ({ page }) => {
  await page.goto(launchURL());

  await expect(page.getByText("Connected", { exact: true }).first()).toBeVisible();
  // A reload that replayed the nonce would abort the session on the Go side.
  expect(new URL(page.url()).hash).toBe("");
});

test("authenticated API calls reach the bridge", async ({ page }) => {
  await page.goto(launchURL());

  const status = await page.waitForResponse((res) => res.url().endsWith("/api/status"), {
    timeout: 20_000,
  });
  expect(status.status()).toBe(200);
});

test("the window names the repository it is planning", async ({ page }) => {
  await page.goto(launchURL());

  // The sidebar and the breadcrumb both answer "which project is this?".
  await expect(page.getByText("billing-service").first()).toBeVisible({ timeout: 20_000 });
});

test.describe("dark theme", () => {
  test.use({ colorScheme: "dark" });

  test("follows the OS preference with no hard-coded light surfaces", async ({ page }) => {
    await page.goto(launchURL());
    await expect(page.locator("#root")).not.toBeEmpty();

    const theme = await page.evaluate(() => {
      const topBar = document.querySelector(".pf-topbar");
      return {
        body: getComputedStyle(document.body).backgroundColor,
        ink: getComputedStyle(document.body).color,
        topBar: topBar === null ? null : getComputedStyle(topBar).backgroundColor,
      };
    });

    // rgb(20, 20, 22): a light build reports rgb(255, 255, 255) here.
    expect(theme.body).toBe("rgb(20, 20, 22)");
    expect(theme.ink).toBe("rgb(236, 236, 239)");
    // Every surface is tokenised; none may stay hard-coded white.
    expect(theme.topBar).not.toBe("rgb(255, 255, 255)");
  });
});

test.describe("light theme", () => {
  test.use({ colorScheme: "light" });

  test("keeps the light token set when the OS asks for light", async ({ page }) => {
    await page.goto(launchURL());
    await expect(page.locator("#root")).not.toBeEmpty();

    const body = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
    expect(body).toBe("rgb(255, 255, 255)");
  });
});
