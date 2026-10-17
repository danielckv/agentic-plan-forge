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
  await expect(page.getByText("Planning session")).toBeVisible();
  expect(pageErrors).toEqual([]);
});

test("the window completes the handshake and reaches the authority", async ({ page }) => {
  await page.goto(launchURL());

  // "Connected" renders only once the socket negotiated plan-forge.v1 and the
  // bridge accepted the auth frame. Exact matching keeps "Disconnected" out.
  await expect(page.getByText("Connected", { exact: true }).first()).toBeVisible();
  // The authority's STATE_SNAPSHOT reached the store.
  await expect(page.getByText("Signed in as owner")).toBeVisible();
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
