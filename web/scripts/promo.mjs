// Promotional screenshots for the README, captured from the real UI bundle
// driving a fake planning session (scripts/lib/fake-session.mjs) seeded with
// the sample in scripts/lib/scenario.mjs. No model runs: the avatars' frames
// are the scenario's, in the shapes the Go side sends.
//
// Run: npm run build && node scripts/promo.mjs [--out DIR] [--scale N]
// Default output: ../docs/images (the README's images).
//
// The run fails (exit 1) on any page error, any request that leaves the
// origin, or a missing shot.
import { mkdirSync, statSync } from "node:fs";
import { resolve } from "node:path";
import { parseArgs } from "node:util";
import { chromium } from "@playwright/test";

import { startFakeSession } from "./lib/fake-session.mjs";

const { values: args } = parseArgs({
  options: {
    out: { type: "string", default: new URL("../../docs/images", import.meta.url).pathname },
    scale: { type: "string", default: "2" },
    only: { type: "string" },
  },
});
const outDir = resolve(args.out);
const scale = Number(args.scale);
const dist = new URL("../dist", import.meta.url).pathname;
const port = 8792;

setTimeout(() => {
  console.error("promo harness watchdog: run exceeded 120s");
  process.exit(2);
}, 120_000).unref();

const shots = [];
/**
 * Wait for a selector, and on failure report what the page actually holds. A
 * bare 15-second timeout hides whether the rail was empty, on another tab, or
 * never rendered at all.
 */
async function need(page, selector, what) {
  try {
    await page.waitForSelector(selector, { state: "visible" });
  } catch {
    const tabs = await page.locator(".pf-rail-tab").allTextContents();
    const rail = await page.locator(".pf-rail-body").innerText().catch(() => "(no rail body)");
    throw new Error(
      `${what}: ${selector} never appeared.\n  rail tabs: ${tabs.join(" | ") || "(none)"}\n` +
        `  rail body: ${rail.slice(0, 300).replaceAll("\n", " / ")}`,
    );
  }
}

async function shot(page, name) {
  if (args.only !== undefined && !args.only.split(",").includes(name)) return;
  const file = `${outDir}/${name}.png`;
  await page.screenshot({ path: file });
  shots.push(file);
  console.log(`captured ${name} (${Math.round(statSync(file).size / 1024)} KB)`);
}

const session = await startFakeSession({ port, dist });
const errors = [];
const foreign = [];
let browser;
let code = 0;
try {
  mkdirSync(outDir, { recursive: true });
  browser = await chromium.launch();
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: scale });
  const page = await context.newPage();
  page.setDefaultTimeout(15_000);
  await page.addInitScript((ws) => {
    globalThis.PLAN_FORGE_WS_URL = ws;
    // Light unless a step chose otherwise; a reload keeps the choice.
    if (globalThis.localStorage.getItem("plan-forge:ui:theme") === null) {
      globalThis.localStorage.setItem("plan-forge:ui:theme", "light");
    }
  }, session.wsUrl);
  page.on("pageerror", (e) => errors.push(e.stack ?? String(e)));
  page.on("request", (req) => {
    const u = req.url();
    if (!u.startsWith("data:") && !u.startsWith("blob:") && new URL(u).origin !== session.origin) foreign.push(u);
  });

  // Page 1: the composer with the brief and its epic files.
  await page.goto(`${session.origin}/`);
  await page.waitForSelector(".pf-composer .cm-content", { state: "visible" });
  // The rail opens on the Brief tab: an outline of the deliberation's topics.
  // The epic files are one tab over, so the composer shot shows the rail the
  // way a writer finds it after writing.
  await page.getByRole("tab", { name: /Epics/ }).click();
  await need(page, ".pf-epic-card", "the composer");
  await need(page, "text=Project notes found", "the composer footer");
  await page.waitForTimeout(400);
  await shot(page, "composer");

  // The epic modal.
  await page.locator(".pf-epic-card").first().click();
  await page.getByRole("dialog").waitFor({ state: "visible" });
  await page.waitForTimeout(350);
  await shot(page, "epic-modal");
  await page.keyboard.press("Escape");
  await page.getByRole("dialog").waitFor({ state: "detached" });

  // The codebase browser with one block open, and `@` completion in the editor.
  await page.getByRole("tab", { name: /Codebase/ }).click();
  await page.locator(".pf-block-head", { hasText: "internal/reporting" }).click();
  await page.waitForSelector(".pf-symbol", { state: "visible" });
  await page.locator(".pf-composer .cm-line", { hasText: "Exports over 10,000 rows" }).click();
  await page.keyboard.press("End");
  await page.keyboard.type(" Stream rows from @Query");
  await page.waitForSelector(".cm-tooltip-autocomplete", { state: "visible" });
  await page.waitForTimeout(300);
  await shot(page, "codebase-palette");
  await page.keyboard.press("Escape");

  // Dark theme.
  await page.evaluate(() => {
    globalThis.localStorage.setItem("plan-forge:ui:theme", "dark");
  });
  await page.reload();
  // A reload puts the rail back on Brief, so the epic cards need the tab again
  // before the dark shot can show them beside the notes.
  await page.getByRole("tab", { name: /Epics/ }).click();
  await need(page, ".pf-epic-card", "the dark composer");
  await page.getByRole("tab", { name: /Notes/ }).click();
  await need(page, ".pf-note", "the notes tab");
  await page.waitForTimeout(400);
  await shot(page, "composer-dark");
  await page.evaluate(() => {
    globalThis.localStorage.setItem("plan-forge:ui:theme", "light");
  });
  await page.reload();
  await page.waitForSelector(".pf-epic-card", { state: "visible" });

  // Page 2: the avatars deliberate.
  await page.getByRole("button", { name: /Confirm Context/ }).click();
  await page.waitForSelector("text=Where should a large export be written?", { state: "visible" });
  await page.waitForTimeout(800);
  await shot(page, "avatars");

  // Page 3: the specification.
  await page.getByRole("button", { name: /Stage the file/ }).click();
  await page.waitForSelector("button:not([disabled]):has-text('Synthesize Master Spec')", { state: "visible" });
  await page.getByRole("button", { name: /Synthesize Master Spec/ }).click();
  await page.waitForSelector("h1:has-text('Master specification')", { state: "visible" });
  await page.waitForFunction(() => document.querySelectorAll("#root figure.pf-mermaid svg").length >= 2);
  await page.waitForTimeout(600);
  await shot(page, "synthesis");

  // The overview diagram, from its HTML template.
  const overview = await context.newPage();
  await overview.setViewportSize({ width: 1280, height: 580 });
  await overview.goto(new URL("./promo/overview.html", import.meta.url).href);
  await overview.waitForTimeout(200);
  await shot(overview, "overview");
  await overview.close();

  if (errors.length > 0) throw new Error(`page errors:\n${errors.join("\n")}`);
  if (foreign.length > 0) throw new Error(`requests left the origin: ${foreign.join(", ")}`);
  console.log(`done -> ${outDir}`);
} catch (err) {
  console.error("promo harness failed:", err instanceof Error ? err.message : err);
  code = 1;
} finally {
  await browser?.close();
  session.close();
}
process.exit(code);
