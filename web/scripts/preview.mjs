// Preview capture: drive the real UI bundle through a fake planning session
// (scripts/lib/fake-session.mjs, Go-shaped frames, seeded with
// scripts/lib/scenario.mjs), screenshot every page, and assert the window's
// behavior along the way: the composer and its rail, the theme and sidebar,
// toasts, keyboard help, the epic and Mermaid modals, the avatar room, the
// matrix filter, the handoff, reduced motion, and layout at 1024 px.
// A visual-review harness, not part of the test suite.
//
// Run: npm run build && node scripts/preview.mjs
// Output: /tmp/plan-forge-preview/*.png
//
// The run fails (exit 1) on any `pageerror`, any request that leaves the
// origin, any horizontal page scroll at 1024x700, a failed check, or a
// missing shot.
import { mkdirSync, rmSync, statSync } from "node:fs";
import { chromium } from "@playwright/test";

import { startFakeSession } from "./lib/fake-session.mjs";

const dist = new URL("../dist", import.meta.url).pathname;
const port = 8791;
const outDir = "/tmp/plan-forge-preview";
const THEME_KEY = "plan-forge:ui:theme";
const SIDEBAR_KEY = "plan-forge:ui:sidebar";

const SHOTS = [
  "page1-composer", "page1-epic-modal", "page1-codebase", "page1-dark", "page1-sidebar-collapsed",
  "page2-room", "page2-toasts", "help-overlay",
  "page3-synthesis", "page3-matrix", "page3-filter", "page3-mermaid-modal", "page3-handoff",
  "page1-1024", "page2-1024", "page3-1024",
];
const MIN_BYTES = 8 * 1024;

const runStart = Date.now();
setTimeout(() => {
  console.error("harness watchdog: run exceeded 150s");
  process.exit(2);
}, 150_000).unref();

function check(cond, message) {
  if (!cond) throw new Error(`check failed: ${message}`);
  console.log(`ok: ${message}`);
}

async function shot(page, name) {
  await page.screenshot({ path: `${outDir}/${name}.png` });
  console.log(`captured ${name}`);
}

/** True when neither the document nor the page's scroll regions scroll sideways. */
async function horizontalOverflow(page) {
  return page.evaluate(() => {
    const regions = [".pf-page-body", ".pf-footer-bar", ".pf-topbar", ".pf-main"]
      .map((s) => document.querySelector(s))
      .filter((el) => el !== null);
    const out = regions.filter((el) => el.scrollWidth > el.clientWidth + 1).map((el) => `${el.className}: ${el.scrollWidth}>${el.clientWidth}`);
    const root = document.documentElement;
    if (root.scrollWidth > globalThis.innerWidth + 1) out.push(`html: ${root.scrollWidth}>${globalThis.innerWidth}`);
    return out;
  });
}

const session = await startFakeSession({ port, dist });
const pageErrors = [];
const foreign = [];
let browser;
let exitCode = 0;
try {
  mkdirSync(outDir, { recursive: true });
  for (const name of SHOTS) rmSync(`${outDir}/${name}.png`, { force: true });

  browser = await chromium.launch();
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  await context.grantPermissions(["clipboard-read", "clipboard-write"], { origin: session.origin });
  const page = await context.newPage();
  page.setDefaultTimeout(15_000);
  await page.addInitScript((ws) => {
    globalThis.PLAN_FORGE_WS_URL = ws;
  }, session.wsUrl);
  page.on("pageerror", (e) => {
    pageErrors.push(e.stack ?? String(e));
    console.log("pageerror:", (e.stack ?? String(e)).slice(0, 1200));
  });
  page.on("request", (req) => {
    const u = req.url();
    if (!u.startsWith("data:") && !u.startsWith("blob:") && new URL(u).origin !== session.origin) foreign.push(u);
  });

  // ---- Page 1: the composer, the epic rail, the codebase browser.
  await page.goto(`${session.origin}/`);
  await page.waitForSelector(".pf-composer .cm-content", { state: "visible" });
  await page.waitForSelector(".pf-epic-card", { state: "visible" });
  await page.waitForSelector("text=Code indexed", { state: "visible" });
  await shot(page, "page1-composer");

  const stepper = await page.evaluate(() =>
    [...document.querySelectorAll(".pf-phase-step")].map((li) => ({
      label: li.querySelector(".pf-phase-label")?.textContent,
      current: li.getAttribute("aria-current"),
    })),
  );
  check(
    JSON.stringify(stepper.map((s) => s.label)) === JSON.stringify(["Ingestion", "Deliberation", "Synthesis", "Approved"]) &&
      stepper[0].current === "step",
    "the phase stepper lists four steps with Ingestion current",
  );
  const borders = await page.evaluate(() => {
    const bottom = (sel) => document.querySelector(sel)?.getBoundingClientRect().bottom;
    return { brand: bottom(".pf-sidebar-brand"), top: bottom(".pf-topbar") };
  });
  check(borders.brand === borders.top, `the sidebar brand row and the top bar end on one line (${borders.brand} = ${borders.top})`);
  check((await page.locator(".pf-epic-card").count()) === 3, "the rail shows three epic files");

  // Typing reaches the authority and is acknowledged.
  const editor = page.locator(".pf-composer .cm-content");
  await editor.click();
  await page.keyboard.press("ControlOrMeta+End");
  await page.keyboard.type("\n\nAlso export credit notes.");
  await page.waitForFunction(() => document.querySelector(".pf-composer-status")?.textContent?.includes("Saved"));
  check(session.state.docText.includes("Also export credit notes."), "typed text reached the authority through TEXT_DELTA");

  // The epic modal: Markdown view, focus trap, Esc closes and focus returns.
  const firstCard = page.locator(".pf-epic-card").first();
  await firstCard.click();
  const epicDialog = page.getByRole("dialog", { name: /E1/ });
  await epicDialog.waitFor({ state: "visible" });
  await page.waitForTimeout(300);
  await shot(page, "page1-epic-modal");
  await epicDialog.getByRole("button", { name: "Markdown", exact: true }).click();
  check((await epicDialog.locator(".pf-epic-modal-source").innerText()).startsWith("# E1 · CSV invoice export"), "the Markdown tab shows the epic file");
  await epicDialog.getByRole("button", { name: /Next/ }).click();
  await page.getByRole("dialog", { name: /E2/ }).waitFor({ state: "visible" });
  for (let i = 0; i < 5; i++) {
    await page.keyboard.press("Tab");
    check(await page.evaluate(() => document.activeElement?.closest('[role="dialog"]') !== null), `Tab ${i + 1} stays inside the epic modal`);
  }
  await page.keyboard.press("Escape");
  await page.getByRole("dialog").waitFor({ state: "detached" });
  check(await firstCard.evaluate((el) => el === document.activeElement), "Esc closes the epic modal and focus returns to the card");

  // The codebase browser writes a component name into the text.
  await page.getByRole("tab", { name: /Codebase/ }).click();
  await page.locator(".pf-block-head", { hasText: "internal/audit" }).click();
  await page.locator(".pf-symbol", { hasText: "RecordEvent" }).waitFor({ state: "visible" });
  await shot(page, "page1-codebase");
  await editor.click();
  await page.keyboard.press("ControlOrMeta+End");
  await page.locator(".pf-symbol", { hasText: "RecordEvent" }).click();
  await page.waitForFunction(() => document.querySelector(".pf-composer-status")?.textContent?.includes("Saved"));
  check(session.state.docText.includes("`RecordEvent`"), "clicking a component writes it into the requirements as inline code");

  // Theme: Dark persists across a reload; Light overrides the OS.
  await page.locator(".pf-topbar").getByRole("button", { name: "Dark", exact: true }).click();
  check((await page.evaluate(() => document.documentElement.getAttribute("data-theme"))) === "dark", "Dark sets data-theme=dark");
  check((await page.evaluate((k) => globalThis.localStorage.getItem(k), THEME_KEY)) === "dark", `the theme persists to ${THEME_KEY}`);
  await page.waitForTimeout(250);
  await shot(page, "page1-dark");
  await page.reload();
  await page.waitForSelector(".pf-epic-card", { state: "visible" });
  check((await page.evaluate(() => document.documentElement.getAttribute("data-theme"))) === "dark", "dark survives a reload");
  await page.locator(".pf-topbar").getByRole("button", { name: "Light", exact: true }).click();

  // Sidebar: `[` collapses to the icon rail and the choice persists.
  await page.locator("body").press("[");
  await page.waitForSelector('.pf-sidebar[data-collapsed="true"]');
  check((await page.evaluate((k) => globalThis.localStorage.getItem(k), SIDEBAR_KEY)) === "collapsed", `the sidebar choice persists to ${SIDEBAR_KEY}`);
  await page.waitForTimeout(300);
  await shot(page, "page1-sidebar-collapsed");
  await page.getByRole("button", { name: "Expand sidebar" }).click();
  await page.waitForSelector('.pf-sidebar[data-collapsed="false"]');

  // ---- Page 2: the owner confirms; the avatars deliberate.
  await page.getByRole("button", { name: /Confirm Context/ }).click();
  await page.waitForSelector("h1:has-text('Avatar deliberation')", { state: "visible" });
  await page.waitForSelector("text=Where should a large export be written?", { state: "visible" });
  await page.waitForSelector(".pf-line", { state: "visible" });
  check((await page.locator(".pf-roster-card").count()) === 4, "the room shows the four avatars");
  check((await page.locator(".pf-room-plan .pf-epic-card").count()) === 3, "the PM's three epics render from its pm.v1 draft");
  await page.getByRole("tab", { name: /Findings/ }).click();
  check((await page.locator(".pf-finding").count()) === 3, "the critic's three findings render from AVATAR_CRITIQUE.issues");
  await page.getByRole("tab", { name: /Architecture/ }).click();
  await page.locator(".pf-room-plan figure.pf-mermaid svg").first().waitFor({ state: "visible" });
  await page.waitForTimeout(400);
  await shot(page, "page2-room");
  // Filtering by an avatar narrows the discussion.
  const allLines = await page.locator(".pf-line").count();
  await page.locator(".pf-roster-card", { hasText: "Red-Team Critic" }).click();
  const criticLines = await page.locator(".pf-line").count();
  check(criticLines > 0 && criticLines < allLines, `filtering by the critic narrows the discussion (${criticLines} of ${allLines})`);
  await page.locator(".pf-roster-card", { hasText: "Red-Team Critic" }).click();

  // Toasts: info auto-dismisses; an error stays until dismissed.
  session.send("ALREADY_RESOLVED", { decision_id: "d-elsewhere", option_id: "a" });
  session.send("ERROR", { code: "ROLE_FORBIDDEN", message: "only the owner can cancel the run" });
  await page.waitForSelector('.pf-toast[data-kind="info"]', { state: "visible" });
  await page.waitForSelector('.pf-toast[data-kind="error"]', { state: "visible" });
  const shownAt = Date.now();
  await shot(page, "page2-toasts");
  await page.locator('.pf-toast[data-kind="info"]').waitFor({ state: "detached", timeout: 9_000 });
  const lifetime = Date.now() - shownAt;
  check(lifetime >= 5_000 && lifetime <= 8_400, `the info toast dismissed itself after ${lifetime} ms (~6 s)`);
  await page.getByRole("button", { name: "Dismiss notification" }).click();
  await page.locator('.pf-toast[data-kind="error"]').waitFor({ state: "detached" });

  // Keyboard help: `?` opens it, Esc closes it.
  await page.locator("body").press("?");
  await page.getByRole("dialog", { name: "Keyboard shortcuts" }).waitFor({ state: "visible" });
  await page.waitForTimeout(250);
  await shot(page, "help-overlay");
  await page.keyboard.press("Escape");
  await page.getByRole("dialog").waitFor({ state: "detached" });

  // ---- Page 3: resolve the decision, synthesize.
  await page.getByRole("button", { name: /Stage the file/ }).click();
  await page.waitForSelector("text=Resolved by owner:dana", { state: "visible" });
  await page.waitForSelector("button:not([disabled]):has-text('Synthesize Master Spec')", { state: "visible" });
  await page.getByRole("button", { name: /Synthesize Master Spec/ }).click();
  await page.waitForSelector("h1:has-text('Master specification')", { state: "visible" });
  await page.waitForFunction(() => document.querySelectorAll("#root figure.pf-mermaid svg").length >= 4);
  const cta = page.getByRole("button", { name: "Approve & open in Claude Code Plan Mode" });
  await cta.waitFor({ state: "visible" });
  check(await cta.isEnabled(), "the owner sees the host-aware call to action");
  await shot(page, "page3-synthesis");

  await page.locator("#jira-matrix").scrollIntoViewIfNeeded();
  await page.getByRole("button", { name: "Expand all" }).click();
  await page.waitForTimeout(350);
  check((await page.locator('#jira-matrix [aria-expanded="true"]').count()) === 6, "Expand all opens three epics and three stories");
  await shot(page, "page3-matrix");
  const filter = page.getByRole("searchbox", { name: /Filter subtasks/ });
  await filter.fill("csv.go");
  await page.waitForSelector("text=1 of 3 subtasks match", { state: "visible" });
  await shot(page, "page3-filter");
  await filter.fill("no-such-subtask");
  await page.waitForSelector("text=No subtasks match", { state: "visible" });
  await filter.press("Escape");
  check((await filter.inputValue()) === "", "Esc clears the matrix filter");
  const copy = page.getByRole("button", { name: "Copy command: go test ./internal/audit/..." });
  await copy.scrollIntoViewIfNeeded();
  await copy.click();
  check((await page.evaluate(() => globalThis.navigator.clipboard.readText())) === "go test ./internal/audit/...", "Copy puts the verification command on the clipboard");

  // Mermaid: expand into a modal; Esc closes it.
  await page.locator("#architecture").scrollIntoViewIfNeeded();
  await page.locator("#architecture .pf-diagram-card").first().getByRole("button", { name: "Expand" }).click();
  const diagram = page.getByRole("dialog", { name: /Architecture/ });
  await diagram.locator("svg").first().waitFor({ state: "visible" });
  await page.waitForTimeout(300);
  await shot(page, "page3-mermaid-modal");
  await page.keyboard.press("Escape");
  await diagram.waitFor({ state: "detached" });

  // Reduced motion stops route, accordion and chip motion.
  await page.emulateMedia({ reducedMotion: "reduce" });
  const reduced = await page.evaluate(() => {
    const cs = (sel) => globalThis.getComputedStyle(document.querySelector(sel));
    return { route: cs(".pf-route").animationName, chip: cs(".pf-chip").transitionDuration };
  });
  check(reduced.route === "none" && /^0s(, 0s)*$/.test(reduced.chip), "prefers-reduced-motion disables route and chip motion");
  await page.emulateMedia({ reducedMotion: "no-preference" });

  // Layout at 1024x700: no sideways scroll on any page, rail expanded or not.
  await page.setViewportSize({ width: 1024, height: 700 });
  for (const collapsed of [false, true]) {
    const now = (await page.evaluate(() => document.querySelector(".pf-sidebar")?.getAttribute("data-collapsed"))) === "true";
    if (collapsed !== now) await page.locator("body").press("[");
    await page.waitForSelector(`.pf-sidebar[data-collapsed="${String(collapsed)}"]`);
    await page.waitForTimeout(300);
    for (const [name, nav] of [["page3", "Synthesis"], ["page2", "Deliberation"], ["page1", "Ingestion"]]) {
      await page.locator(".pf-sidebar-nav").getByRole("button", { name: nav }).click();
      await page.waitForTimeout(300);
      const overflow = await horizontalOverflow(page);
      check(overflow.length === 0, `no sideways scroll at 1024x700 on ${name}, sidebar ${collapsed ? "collapsed" : "expanded"}${overflow.length ? `: ${overflow.join("; ")}` : ""}`);
      if (collapsed) await shot(page, `${name}-1024`);
    }
  }
  await page.locator(".pf-sidebar-nav").getByRole("button", { name: "Synthesis" }).click();
  await page.setViewportSize({ width: 1440, height: 900 });

  // ---- Handoff: approve; the relay broadcasts PLAN_APPROVED.
  await page.getByRole("button", { name: "Approve & open in Claude Code Plan Mode" }).click();
  await page.waitForSelector(".pf-handoff", { state: "visible" });
  await page.waitForSelector("text=This window closes in", { state: "visible" });
  await shot(page, "page3-handoff");
  check(
    (await page.evaluate(() => document.querySelector('.pf-phase-step[aria-current="step"] .pf-phase-label')?.textContent)) === "Approved",
    "the stepper reaches Approved",
  );

  for (const name of SHOTS) {
    const st = statSync(`${outDir}/${name}.png`);
    if (st.mtimeMs < runStart || st.size < MIN_BYTES) throw new Error(`${name}.png is stale or empty`);
  }
  check(pageErrors.length === 0, `no page error during the run (${pageErrors.length})`);
  check(foreign.length === 0, `no request left the origin${foreign.length ? `: ${foreign.join(", ")}` : ""}`);
  console.log("\nWS event sequence (-> relay to app, <- app to relay):");
  for (const [name, events] of Object.entries(session.wireLog)) {
    console.log(`  ${name}: ${events.filter((e) => !e.includes("TEXT_CHECK")).join(" | ")}`);
  }
  console.log(`done -> ${outDir}/`);
} catch (err) {
  console.error("preview harness failed:", err instanceof Error ? err.message : err);
  exitCode = 1;
} finally {
  await browser?.close();
  session.close();
}
process.exit(exitCode);
