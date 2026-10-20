import { expect, test } from "@playwright/test";

import { startCenterHarness } from "./center-harness.mjs";

/**
 * The command center, served by the real daemon under its production
 * Content-Security-Policy.
 *
 * It asserts the two properties nothing else can: that a tab completes the
 * ticket handshake and renders live telemetry recorded with no plan in sight,
 * and that one record posted by a hook reaches *two* open tabs. The window
 * bridge holds a single UI socket by construction, so the fanout is the whole
 * reason this surface exists and is worth a test that uses the real binary.
 */

const REPO = "/tmp/plan-forge-center-e2e-repo";

type Harness = Awaited<ReturnType<typeof startCenterHarness>>;

let harness: Harness;
let pageErrors: string[];

test.skip(!process.env.PLAN_FORGE_BIN, "set PLAN_FORGE_BIN to a client build");

test.beforeAll(async () => {
  harness = await startCenterHarness();
  await harness.ingest({
    repo: REPO,
    session_id: "claude:e2e",
    harness: "claude-code",
    developer: "e2e",
    branch: "feat/no-plan-here",
    kind: "session_start",
    detail: "session started",
  });
});

test.afterAll(async () => {
  await harness.close();
});

test.beforeEach(({ page }) => {
  pageErrors = [];
  page.on("pageerror", (err) => pageErrors.push(String(err)));
});

test("a launched tab renders a session recorded without a plan", async ({ page }) => {
  await page.goto(await harness.launchURL("/live"));

  await expect(page.locator("#root")).not.toBeEmpty();
  await expect(page.getByText("Plan Forge").first()).toBeVisible();
  // The point of the live store: a branch with no approved plan is still shown.
  await expect(page.getByText("feat/no-plan-here")).toBeVisible();
  await expect(page.getByText("ad hoc").first()).toBeVisible();
  expect(pageErrors).toEqual([]);
});

test("the tab reaches the stream and reports itself live", async ({ page }) => {
  await page.goto(await harness.launchURL("/live"));
  // "Live" renders only once the SSE response headers arrived, which means the
  // Bearer handshake and the Origin check both passed.
  await expect(page.getByText("Live", { exact: true }).first()).toBeVisible({ timeout: 15_000 });
});

test("one record reaches two open tabs", async ({ browser }) => {
  const first = await browser.newPage();
  const second = await browser.newPage();
  await first.goto(await harness.launchURL("/live"));
  await second.goto(await harness.launchURL("/live"));
  await expect(first.getByText("feat/no-plan-here")).toBeVisible();
  await expect(second.getByText("feat/no-plan-here")).toBeVisible();

  await harness.ingest({
    repo: REPO,
    session_id: "omp:second",
    harness: "omp",
    branch: "feat/arrived-live",
    kind: "tool",
    detail: "edited a file",
  });

  await expect(first.getByText("feat/arrived-live")).toBeVisible({ timeout: 15_000 });
  await expect(second.getByText("feat/arrived-live")).toBeVisible({ timeout: 15_000 });
  await first.close();
  await second.close();
});

test("a tab opened by hand parks instead of silently failing", async ({ page }) => {
  await page.goto(`${harness.origin}/live`);
  await expect(page.getByText("This tab was not opened by Plan Forge")).toBeVisible();
  await expect(page.getByText("plan-forge center open")).toBeVisible();
});

test("the command center makes no external request", async ({ page }) => {
  const external: string[] = [];
  page.on("request", (req) => {
    if (!req.url().startsWith(harness.origin) && !req.url().startsWith("data:")) {
      external.push(req.url());
    }
  });
  await page.goto(await harness.launchURL("/sprint"));
  await expect(page.locator("#root")).not.toBeEmpty();
  expect(external).toEqual([]);
});
