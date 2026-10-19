// Preview capture: serve web/dist statically and screenshot the three pages
// with a fake relay so each page renders with real seeded state, then drive
// the window chrome (theme, sidebar, toasts, keyboard help, Mermaid modal,
// handoff) and assert the behavior it shows.
// Throwaway harness for visual review, not part of the test suite.
//
// Run: npm run build && node scripts/preview.mjs
// Output: /tmp/plan-forge-preview/*.png
//
// Navigation is never forced by URL: manual jumps are blocked by design
// (navigate() guard). The fake relay acts as the authority and moves the app
// between phases the same way the real relay does, in response to the
// commands the UI itself sends:
//
//   page 1  <- HELLO                  -> STATE_SNAPSHOT(INGESTION)
//   page 2  <- FREEZE_REQUEST (click "Confirm Context & Launch Avatars")
//             -> PAGE_TRANSITION(DELIBERATION), STATE_SNAPSHOT(DELIBERATION),
//                RUN_ASSIGNED, LEASE_GRANTED, AVATAR_THOUGHT x4 (Synced /
//                Revising / Critiquing / Idle), AVATAR_DRAFT x2 (pm, architect),
//                AVATAR_CRITIQUE x3, DECISION_PROMPT
//   page 3  <- DECISION_RESOLVE (click a decision option)
//             -> DECISION_RESOLVE, AVATAR_CRITIQUE(ISS-2 resolved), AVATAR_THOUGHT
//                x3 (architect / critic / synthesizer Synced)
//          <- SYNTHESIZE_REQUEST (click "Synthesize Master Spec")
//             -> PAGE_TRANSITION(SYNTHESIS), STATE_SNAPSHOT(SYNTHESIS),
//                AVATAR_DRAFT(synthesis.v1)
//   handoff <- APPROVE (click "Approve & open in Claude Code Plan Mode")
//             -> PLAN_APPROVED, as the relay broadcasts it once the artifacts
//                are saved
//
// The run fails (exit 1) on any `pageerror`, any request that leaves the
// origin, any horizontal page scroll at 1024x700, or a missing shot.
import { createServer } from "node:http";
import { readFileSync, existsSync, mkdirSync, rmSync, statSync } from "node:fs";
import { extname, join } from "node:path";
import { WebSocketServer } from "ws";
import { chromium } from "@playwright/test";

const dist = new URL("../dist", import.meta.url).pathname;
const port = 8791;
const origin = `http://127.0.0.1:${port}`;
const outDir = "/tmp/plan-forge-preview";
const mime = {
  ".html": "text/html", ".js": "text/javascript", ".css": "text/css",
  ".svg": "image/svg+xml", ".json": "application/json", ".woff2": "font/woff2",
};

const SHOTS = {
  page1: `${outDir}/page1-ingestion.png`,
  page2: `${outDir}/page2-deliberation.png`,
  page3: `${outDir}/page3-synthesis.png`,
};
const EXTRA_SHOTS = {
  page1Dark: `${outDir}/page1-dark.png`,
  page1Collapsed: `${outDir}/page1-sidebar-collapsed.png`,
  page1Narrow: `${outDir}/page1-1024-auto-rail.png`,
  page2Decisions: `${outDir}/page2-deliberation-decisions.png`,
  page2Toasts: `${outDir}/page2-toasts.png`,
  page2Help: `${outDir}/help-overlay.png`,
  page3Matrix: `${outDir}/page3-synthesis-matrix.png`,
  page3Filter: `${outDir}/page3-synthesis-filter.png`,
  page3Schedule: `${outDir}/page3-synthesis-schedule.png`,
  page3Modal: `${outDir}/page3-mermaid-modal.png`,
  page3Dark: `${outDir}/page3-synthesis-dark.png`,
  page3Handoff: `${outDir}/page3-handoff.png`,
  page2Narrow: `${outDir}/page2-1024.png`,
  page3Narrow: `${outDir}/page3-1024.png`,
};
const MIN_BYTES = 100 * 1024; // populated 1440x900 page
const MIN_EXTRA_BYTES = 8 * 1024;

const THEME_KEY = "plan-forge:ui:theme";
const SIDEBAR_KEY = "plan-forge:ui:sidebar";

const runStart = Date.now();

// Hard upper bound for the whole run; every individual wait is bounded too.
setTimeout(() => {
  console.error("harness watchdog: run exceeded 150s");
  process.exit(2);
}, 150_000).unref();

// ---------------------------------------------------------------------------
// Static file server + stubbed local-bridge endpoints
// ---------------------------------------------------------------------------

const server = createServer((req, res) => {
  const url = req.url ?? "/";
  if (url.startsWith("/api/session")) {
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify({ token: "preview-token" }));
    return;
  }
  if (url.startsWith("/api/status")) {
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify({
      ast: "ok",
      astDetail: "214 files parsed, 3,871 symbols indexed",
      docHealth: 0,
      ocr: "available",
      repo: "billing-service",
      repoPath: "/Users/dev/src/billing-service",
      // The host the invoking skill passed (`--host claude`): Page 3 words its
      // call to action and handoff panel for it.
      requestedHost: "claude",
    }));
    return;
  }
  let path = url.split("?")[0].split("#")[0];
  if (path === "/" || path === "/ingest" || path === "/deliberate" || path === "/synthesis") path = "/index.html";
  const file = join(dist, path);
  const target = existsSync(file) && !file.endsWith("/") ? file : join(dist, "index.html");
  res.setHeader("Content-Type", mime[extname(target)] || "application/octet-stream");
  res.end(readFileSync(target));
});

// ---------------------------------------------------------------------------
// Fake relay: completes the plan-forge.v1 subprotocol handshake and plays the
// authority. It reacts to client commands; the script itself pushes the few
// extra events (toasts, follower snapshot) that need a deliberate trigger.
// ---------------------------------------------------------------------------

const wss = new WebSocketServer({
  server,
  handleProtocols: (protocols) => (protocols.has("plan-forge.v1") ? "plan-forge.v1" : false),
});

const RUN_ID = "run-2026-10-17-01";
let relaySocket = null; // the single live fake-relay WS connection (latest wins)
let seq = 0;

// Wire log per page, printed at the end as the exact event sequence used.
let currentPage = "page1";
const wireLog = { page1: [], page2: [], page3: [] };

function envelope(event_type, payload) {
  seq += 1;
  return JSON.stringify({
    version: 1,
    session_id: "preview-session",
    sender_id: "authority",
    event_id: crypto.randomUUID(),
    event_type,
    seq: String(seq),
    revision: String(seq),
    epoch: "1",
    timestamp: new Date().toISOString(),
    payload,
  });
}

function send(eventType, payload, label = eventType) {
  if (relaySocket === null || relaySocket.readyState !== 1) {
    throw new Error(`relay cannot send ${eventType}: no open WS connection`);
  }
  relaySocket.send(envelope(eventType, payload));
  wireLog[currentPage].push(`-> ${label}`);
}

const MEMBERS = ["owner:daniel", "editor:priya"];

const PRD_TEXT = `# Export invoices — product brief

## Problem
Finance teams export invoice batches manually every Friday. Each export takes
~40 minutes of clicking through the reporting UI and fails silently for ranges
longer than a quarter.

## Requirements
- Export invoices as CSV for an arbitrary date range.
- Exports over 10k rows must complete without a UI timeout.
- Every export is auditable: who ran it, which range, when.

## Out of scope
- Scheduled recurring exports (v2).
- XLSX output.

## Technical appendix (not sent to the PM avatar)
Reuse the existing reporting query service (\`internal/reporting/query.go\`).
The CSV serializer is new; keep it streaming so memory stays flat.
`;

const ARCHITECT_DIAGRAM = `graph TD
    UI[Web UI] --> Bridge[Local bridge]
    Bridge --> Relay[Relay authority]
    Relay --> Pool[Avatar pool]
    Pool --> Synth[Lead Synthesizer]
    Synth --> Artifacts[.plan artifacts]`;

function snapshot(phase, role = "owner") {
  send(
    "STATE_SNAPSHOT",
    { phase, doc_version: "12", role, members: MEMBERS, doc_text: PRD_TEXT },
    `STATE_SNAPSHOT(${phase}:${role})`,
  );
}

function driveDeliberation() {
  send("PAGE_TRANSITION", { from: "INGESTION", to: "DELIBERATION" }, "PAGE_TRANSITION(DELIBERATION)");
  snapshot("DELIBERATION");
  send("RUN_ASSIGNED", { run_id: RUN_ID });
  send("LEASE_GRANTED", { holder_id: "owner:daniel" });
  const thought = (avatar, status, summary, cycle = 2) =>
    send(
      "AVATAR_THOUGHT",
      { run_id: RUN_ID, avatar_id: avatar, cycle, status, public_summary: summary },
      `AVATAR_THOUGHT(${avatar}:${status})`,
    );
  thought("pm", "Synced", "Drafted 2 epics covering the export flow and the audit trail; no open product questions remain.");
  thought("architect", "Revising", "Mapping the export flow onto the existing reporting module; streaming serializer keeps memory flat.");
  thought("critic", "Critiquing", "Probing failure states: partial exports after a query timeout, and audit gaps on retry.");
  thought("synthesizer", "Idle", "Standing by for convergence.");
  send("AVATAR_DRAFT", {
    run_id: RUN_ID, avatar_id: "pm", cycle: 2,
    draft_revision: "3", content_hash: "9f2b44ac1e07",
    content: "## Epics\n- E1 CSV export flow\n- E2 Audit trail\n\nEach story traces to a requirement in the frozen baseline.",
  }, "AVATAR_DRAFT(pm)");
  send("AVATAR_DRAFT", {
    run_id: RUN_ID, avatar_id: "architect", cycle: 2,
    draft_revision: "2", content_hash: "51cd8a02b3f9",
    content: ARCHITECT_DIAGRAM,
  }, "AVATAR_DRAFT(architect)");
  const critique = (issue_id, severity, summary, resolved) =>
    send(
      "AVATAR_CRITIQUE",
      { run_id: RUN_ID, avatar_id: "critic", issue_id, severity, summary, resolved },
      `AVATAR_CRITIQUE(${issue_id}:${severity}:${resolved ? "resolved" : "open"})`,
    );
  critique("ISS-1", "CRITICAL", "Partial exports after a query timeout are not rolled back — auditors would see phantom rows.", true);
  critique("ISS-2", "HIGH", "Retry path re-runs the full range without an idempotency key; duplicate CSV rows possible.", false);
  critique("ISS-3", "MEDIUM", "Audit record lacks the export filter hash, so two exports of the same range are indistinguishable.", false);
  send("DECISION_PROMPT", {
    decision_id: "d-1", run_id: RUN_ID, input_revision: "4",
    question: "Where should the CSV export write its output?",
    options: [
      { id: "a", label: "Streaming download", tradeoff: "No storage footprint, but exports over 10k rows risk a gateway timeout" },
      { id: "b", label: "Staged file + download link", tradeoff: "Reliable for large ranges; needs a cleanup job and temp storage" },
    ],
    recommended_option_id: "a",
  });
}

// The owner picked an option: broadcast the resolution, close the open HIGH
// finding, and let every avatar finish the cycle so the Synthesize footer
// unlocks (it stays locked while any avatar is still working).
function driveResolution(payload) {
  send(
    "DECISION_RESOLVE",
    { decision_id: payload.decision_id, option_id: payload.option_id, actor_id: "owner:daniel" },
    `DECISION_RESOLVE(${payload.decision_id}:${payload.option_id})`,
  );
  send("AVATAR_CRITIQUE", {
    run_id: RUN_ID, avatar_id: "critic", issue_id: "ISS-2", severity: "HIGH",
    summary: "Retry path re-runs the full range without an idempotency key; duplicate CSV rows possible.",
    resolved: true,
  }, "AVATAR_CRITIQUE(ISS-2:HIGH:resolved)");
  const synced = (avatar, cycle, summary) =>
    send("AVATAR_THOUGHT", {
      run_id: RUN_ID, avatar_id: avatar, cycle, status: "Synced", public_summary: summary,
    }, `AVATAR_THOUGHT(${avatar}:Synced)`);
  synced("architect", 3, "Idempotency key added to the retry path; streaming download chosen.");
  synced("critic", 3, "All blocking findings resolved; one MEDIUM note carried into the spec.");
  synced("synthesizer", 3, "Ready to compile the master specification.");
}

const SPEC_MARKDOWN = `run_id: ${RUN_ID}

## Executive summary

Invoice CSV export ships in one sprint. The export reuses the reporting query
service, streams rows to the client, and writes an audit record per run. The
red-team critique closed one CRITICAL (partial-export rollback) and one HIGH
(idempotent retries) before convergence.

## Immutable constraints

- Exports stream; memory stays flat regardless of row count.
- Every export writes exactly one audit record, including failures.
- No new external dependencies.

## AST targets

- \`internal/reporting/query.go\` — extend \`QueryInvoices\` with range filters
- \`internal/reporting/csv.go\` — new streaming serializer (\`WriteCSV\`)
- \`internal/audit/log.go\` — \`RecordExport\` with filter hash

## Documentation Sync

- [ ] README: document the export endpoint and limits
- [ ] docs/reporting.md: add the CSV contract and audit fields
- [ ] CHANGELOG: user-facing entry for the export flow
`;

const AGENT_GANTT = `gantt
    dateFormat HH:mm
    axisFormat %H:%M
    section Agent run
    Parse and ground :00:00, 8m
    Deliberation cycles :00:08, 19m
    Synthesis :00:27, 8m`;

const HUMAN_GANTT = `gantt
    dateFormat YYYY-MM-DD
    axisFormat %b %d
    section Sprint
    Query service range filters :2026-10-19, 1d
    Streaming CSV serializer :2026-10-20, 2d
    Audit record + tests :2026-10-22, 1d
    Docs sync and review :2026-10-23, 1d`;

const MANIFEST = {
  // Page 3 gives the diagram the full document width, so lay it out left-to-right.
  architecture_diagram: `graph LR
    UI[Web UI] --> Bridge[Local bridge]
    Bridge --> Relay[Relay authority]
    Relay --> Pool[Avatar pool]
    Pool --> Synth[Lead Synthesizer]
    Synth --> Artifacts[.plan artifacts]`,
  sequence_diagram: `sequenceDiagram
    participant O as Owner
    participant B as Local bridge
    participant A as Avatar pool
    O->>B: Confirm context
    B->>A: Frozen baseline
    A-->>B: Drafts and critiques
    B-->>O: Master spec`,
  human_gantt: HUMAN_GANTT,
  agent_gantt: AGENT_GANTT,
  gantt_chips: { human: "4.5 days (40.5 hrs)", agent: "~35 mins" },
  epics: [
    {
      id: "E1",
      title: "CSV export flow",
      goal: "Finance can export any invoice date range as CSV without timeouts.",
      stories: [
        {
          id: "E1-S1",
          need: "As a finance analyst, I export a date range so I can close the week.",
          subtasks: [
            {
              id: "E1-S1-T1",
              title: "Extend query service with range filters",
              severity: "HIGH",
              acceptance: ["Range start/end are inclusive and validated", "Empty ranges return an empty CSV, not an error"],
              targets: ["internal/reporting/query.go"],
              verification: ["go test ./internal/reporting/..."],
              humanEstimate: "1 day (9 hrs)",
              agentEstimate: "~9 mins",
            },
            {
              id: "E1-S1-T2",
              title: "Streaming CSV serializer",
              severity: "CRITICAL",
              acceptance: ["10k-row export streams without buffering", "Memory stays flat under load test"],
              targets: ["internal/reporting/csv.go"],
              verification: ["go test -run TestWriteCSV ./internal/reporting/", "go test -bench=. ./internal/reporting/"],
              humanEstimate: "2 days (18 hrs)",
              agentEstimate: "~14 mins",
            },
          ],
        },
      ],
    },
    {
      id: "E2",
      title: "Audit trail",
      goal: "Every export is attributable: actor, range, timestamp, outcome.",
      stories: [
        {
          id: "E2-S1",
          need: "As a compliance officer, I can list who exported what and when.",
          subtasks: [
            {
              id: "E2-S1-T1",
              title: "RecordExport with filter hash",
              severity: "MEDIUM",
              acceptance: ["Failures write an audit record too", "Filter hash distinguishes identical ranges"],
              targets: ["internal/audit/log.go"],
              verification: ["go test ./internal/audit/..."],
              humanEstimate: "1 day (9 hrs)",
              agentEstimate: "~7 mins",
            },
          ],
        },
      ],
    },
  ],
};

function driveSynthesis() {
  send("PAGE_TRANSITION", { from: "DELIBERATION", to: "SYNTHESIS" }, "PAGE_TRANSITION(SYNTHESIS)");
  snapshot("SYNTHESIS");
  send("AVATAR_DRAFT", {
    run_id: RUN_ID, avatar_id: "synthesizer", cycle: 3,
    draft_revision: "1", schema_id: "synthesis.v1",
    content: JSON.stringify({
      spec_markdown: SPEC_MARKDOWN,
      manifest_json: JSON.stringify(MANIFEST),
    }),
    content_hash: "e71d9f0a44c2",
    complete: true,
  }, "AVATAR_DRAFT(synthesizer:synthesis.v1)");
}

// The relay broadcasts PLAN_APPROVED only after the artifacts are saved; the
// window then sees phase APPROVED and shows the handoff panel.
function drivePlanApproved() {
  send("PLAN_APPROVED", {
    approval_id: "appr-0001",
    approved_revision: "1",
    spec_sha256: "a".repeat(64),
    manifest_sha256: "b".repeat(64),
    bundle_id: "bundle-0001",
    persisted_by: "owner:daniel",
  }, "PLAN_APPROVED");
}

wss.on("connection", (ws) => {
  relaySocket = ws;
  ws.on("message", (raw) => {
    let msg;
    try {
      msg = JSON.parse(String(raw));
    } catch {
      return;
    }
    const type = msg.event_type;
    wireLog[currentPage].push(`<- ${type}`);
    switch (type) {
      case "HELLO":
        snapshot("INGESTION");
        break;
      case "FREEZE_REQUEST":
        driveDeliberation();
        break;
      case "DECISION_RESOLVE":
        driveResolution(msg.payload ?? {});
        break;
      case "SYNTHESIZE_REQUEST":
        driveSynthesis();
        break;
      case "APPROVE":
        drivePlanApproved();
        break;
      default:
        // Other client commands (TEXT_CHECK, ...) need no authority reply.
        break;
    }
  });
});

// ---------------------------------------------------------------------------
// Playwright capture
// ---------------------------------------------------------------------------

function assertFresh(label, file, { minBytes }) {
  const st = statSync(file); // throws if the file was not written
  if (st.mtimeMs < runStart) throw new Error(`${label}: ${file} is stale (mtime before run start)`);
  if (minBytes !== undefined && st.size <= minBytes) {
    throw new Error(`${label}: ${file} is only ${st.size} bytes (need > ${minBytes})`);
  }
  console.log(`verified ${label}: ${file} (${st.size} bytes)`);
}

function check(cond, message) {
  if (!cond) throw new Error(`check failed: ${message}`);
  console.log(`ok: ${message}`);
}

async function scrollBodyTo(page, selector) {
  await page.evaluate((sel) => {
    const body = document.querySelector(".pf-page-body");
    if (body === null) throw new Error("no .pf-page-body scroll container");
    if (sel === null) {
      body.scrollTop = 0;
    } else if (sel === "bottom") {
      body.scrollTop = body.scrollHeight;
    } else {
      const el = document.querySelector(sel);
      if (el === null) throw new Error(`no element ${sel}`);
      el.scrollIntoView({ block: "start" });
    }
  }, selector);
}

/** True when neither the document nor the page's scroll regions scroll sideways. */
async function noHorizontalScroll(page) {
  return page.evaluate(() => {
    const root = document.documentElement;
    const regions = [".pf-page-body", ".pf-footer-bar", ".pf-topbar", ".pf-main"]
      .map((s) => document.querySelector(s))
      .filter((el) => el !== null);
    const offenders = regions
      .filter((el) => el.scrollWidth > el.clientWidth + 1)
      .map((el) => `${el.className}: ${el.scrollWidth}>${el.clientWidth}`);
    if (root.scrollWidth > globalThis.innerWidth + 1) offenders.push(`html: ${root.scrollWidth}>${globalThis.innerWidth}`);
    return offenders;
  });
}

let exitCode = 0;
let browser;
const pageErrors = [];
const foreignRequests = [];
try {
  await new Promise((r) => server.listen(port, "127.0.0.1", r));
  mkdirSync(outDir, { recursive: true });
  // No stale PNG may survive a run: remove every output before capturing.
  for (const f of [...Object.values(SHOTS), ...Object.values(EXTRA_SHOTS)]) rmSync(f, { force: true });

  browser = await chromium.launch();
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  await context.grantPermissions(["clipboard-read", "clipboard-write"], { origin });
  const page = await context.newPage();
  page.setDefaultTimeout(15_000);
  page.setDefaultNavigationTimeout(15_000);
  await page.addInitScript((wsUrl) => {
    globalThis.PLAN_FORGE_WS_URL = wsUrl;
  }, `ws://127.0.0.1:${port}/ws`);
  page.on("pageerror", (e) => {
    pageErrors.push(e.stack ?? String(e));
    console.log("pageerror:", (e.stack ?? String(e)).slice(0, 1200));
  });
  page.on("console", (m) => {
    if (m.type() === "error" || m.type() === "warning") console.log("console:", m.type(), m.text().slice(0, 200));
  });
  page.on("request", (req) => {
    const u = req.url();
    if (u.startsWith("data:") || u.startsWith("blob:")) return;
    if (new URL(u).origin !== origin) foreignRequests.push(u);
  });

  // ---- Page 1 — Ingestion: seeded PRD in the shared editor, healthy pills,
  // connected chrome (role + presence only render once the snapshot lands).
  currentPage = "page1";
  await page.goto(`${origin}/`);
  await page.waitForSelector("text=Export invoices", { state: "visible" });
  await page.waitForSelector("text=Signed in as owner", { state: "visible" });
  await page.waitForSelector("text=AST OK", { state: "visible" }); // status pills poll
  await page.locator('input[type="checkbox"]').check();
  await page.screenshot({ path: SHOTS.page1 });
  console.log("captured page1");

  // Phase stepper: Ingestion is the current step, the rest are upcoming.
  const stepper = await page.evaluate(() =>
    [...document.querySelectorAll(".pf-phase-step")].map((li) => ({
      label: li.querySelector(".pf-phase-label")?.textContent,
      state: li.getAttribute("data-state"),
      current: li.getAttribute("aria-current"),
    })),
  );
  check(
    JSON.stringify(stepper.map((s) => s.label)) === JSON.stringify(["Ingestion", "Deliberation", "Synthesis", "Approved"]),
    "phase stepper lists Ingestion -> Deliberation -> Synthesis -> Approved",
  );
  check(
    stepper[0].state === "current" && stepper[0].current === "step" && stepper.slice(1).every((s) => s.state === "upcoming" && s.current === null),
    "stepper marks Ingestion current (aria-current=step) and the rest upcoming",
  );
  check(
    (await page.evaluate(() => document.querySelector(".pf-sidebar")?.getAttribute("data-collapsed"))) === "false",
    "sidebar starts expanded at 1440px",
  );

  // Theme selector: Dark sets data-theme, persists, survives a reload.
  await page.locator(".pf-segmented").getByRole("button", { name: "Dark", exact: true }).click();
  check((await page.evaluate(() => document.documentElement.getAttribute("data-theme"))) === "dark", "Dark sets data-theme=dark on <html>");
  check((await page.evaluate((k) => globalThis.localStorage.getItem(k), THEME_KEY)) === "dark", `theme persists to ${THEME_KEY}`);
  await page.waitForTimeout(300); // theme repaint settles
  await page.screenshot({ path: EXTRA_SHOTS.page1Dark });
  console.log("captured page1 (dark)");
  await page.reload();
  await page.waitForSelector("text=Signed in as owner", { state: "visible" });
  check((await page.evaluate(() => document.documentElement.getAttribute("data-theme"))) === "dark", "dark theme survives a reload");
  await page.locator(".pf-segmented").getByRole("button", { name: "Light", exact: true }).click();
  check((await page.evaluate(() => document.documentElement.getAttribute("data-theme"))) === "light", "Light overrides the OS preference");

  // Sidebar: `[` collapses to the icon rail, persists, and expands again.
  await page.locator("body").press("[");
  await page.waitForSelector('.pf-sidebar[data-collapsed="true"]');
  check((await page.evaluate((k) => globalThis.localStorage.getItem(k), SIDEBAR_KEY)) === "collapsed", `sidebar choice persists to ${SIDEBAR_KEY}`);
  await page.waitForTimeout(350); // let the width transition settle
  await page.screenshot({ path: EXTRA_SHOTS.page1Collapsed });
  console.log("captured page1 (sidebar collapsed)");
  await page.getByRole("button", { name: "Expand sidebar" }).click();
  await page.waitForSelector('.pf-sidebar[data-collapsed="false"]');
  check((await page.evaluate((k) => globalThis.localStorage.getItem(k), SIDEBAR_KEY)) === "expanded", "toggle button expands the sidebar and persists");

  // ---- Page 2 — Deliberation: the owner's real "Confirm" click sends
  // FREEZE_REQUEST; the relay answers with PAGE_TRANSITION and the seeded run.
  currentPage = "page2";
  await page.getByRole("button", { name: /Confirm Context/ }).click();
  await page.waitForSelector("h1:has-text('Avatar deliberation')", { state: "visible" });
  await page.waitForSelector("text=Awaiting decision", { state: "visible" });
  await page.getByText("Technical Architect", { exact: true }).click();
  await page.locator("#root figure.pf-mermaid svg").first().waitFor({ state: "visible" }); // architect diagram
  await page.waitForTimeout(350); // accordion + route animations settle
  await page.screenshot({ path: SHOTS.page2 });
  console.log("captured page2");
  check(
    (await page.evaluate(() => document.querySelector('.pf-phase-step[aria-current="step"] .pf-phase-label')?.textContent)) === "Deliberation",
    "stepper follows the authority phase to Deliberation",
  );
  await scrollBodyTo(page, "bottom");
  await page.screenshot({ path: EXTRA_SHOTS.page2Decisions });
  console.log("captured page2 (decisions, scrolled)");

  // Toasts: an info notice auto-dismisses after ~6 s; an error stays until dismissed.
  await scrollBodyTo(page, null);
  // A decision the window does not hold: the notice appears, d-1 stays open.
  send("ALREADY_RESOLVED", { decision_id: "d-elsewhere", option_id: "a" });
  send("ERROR", { code: "ROLE_FORBIDDEN", message: "only the owner can cancel the run" });
  await page.waitForSelector('.pf-toast[data-kind="info"]', { state: "visible" });
  await page.waitForSelector('.pf-toast[data-kind="error"]', { state: "visible" });
  const toastShownAt = Date.now();
  await page.waitForTimeout(250);
  await page.screenshot({ path: EXTRA_SHOTS.page2Toasts });
  console.log("captured page2 (toasts)");
  // The info toast leaves on its own; the error toast is the one the user dismisses.
  await page.locator('.pf-toast[data-kind="info"]').waitFor({ state: "detached", timeout: 9_000 });
  const infoLifetime = Date.now() - toastShownAt;
  check(infoLifetime >= 5_400 && infoLifetime <= 8_400, `info toast auto-dismissed after ${infoLifetime} ms (~6 s)`);
  check((await page.locator('.pf-toast[data-kind="error"]').count()) === 1, "error toast outlives the info toast");
  await page.getByRole("button", { name: "Dismiss notification" }).click();
  await page.locator('.pf-toast[data-kind="error"]').waitFor({ state: "detached" });
  console.log("ok: error toast dismissed by its close button");

  // Keyboard help: `?` opens it, Esc closes it, focus returns.
  await page.locator("body").press("?");
  await page.getByRole("dialog", { name: "Keyboard shortcuts" }).waitFor({ state: "visible" });
  await page.waitForTimeout(300);
  await page.screenshot({ path: EXTRA_SHOTS.page2Help });
  console.log("captured help overlay");
  await page.keyboard.press("Escape");
  await page.getByRole("dialog").waitFor({ state: "detached" });
  console.log("ok: ? opens keyboard help and Esc closes it");

  // ---- Page 3 — Synthesis: resolve the decision (relay closes ISS-2 and
  // settles every avatar), then the owner's "Synthesize Master Spec" click
  // sends SYNTHESIZE_REQUEST and the relay answers with
  // PAGE_TRANSITION(SYNTHESIS) + the synthesis.v1 draft.
  await page.getByRole("button", { name: /Streaming download/ }).click();
  await page.waitForSelector("text=Resolved by owner:daniel", { state: "visible" });
  currentPage = "page3";
  await page.waitForSelector("button:not([disabled]):has-text('Synthesize Master Spec')", { state: "visible" });
  await page.getByRole("button", { name: /Synthesize Master Spec/ }).click();
  await page.waitForSelector("h1:has-text('Master specification')", { state: "visible" });
  // Architecture, sequence, human gantt, agent gantt.
  await page.waitForFunction(() => document.querySelectorAll("#root figure.pf-mermaid svg").length >= 4);
  check(
    (await page.evaluate(() => document.querySelector('.pf-phase-step[aria-current="step"] .pf-phase-label')?.textContent)) === "Synthesis",
    "stepper follows the authority phase to Synthesis",
  );

  // The host-aware call to action, from `requestedHost: "claude"` in /api/status.
  const cta = page.getByRole("button", { name: "Approve & open in Claude Code Plan Mode" });
  await cta.waitFor({ state: "visible" });
  check(await cta.isEnabled(), "owner sees the enabled host-aware CTA: Approve & open in Claude Code Plan Mode");

  // Followers keep "Owner approves".
  currentPage = "page3";
  snapshot("SYNTHESIS", "editor");
  await page.getByRole("button", { name: "Owner approves" }).waitFor({ state: "visible" });
  check(await page.getByRole("button", { name: "Owner approves" }).isDisabled(), 'follower sees a disabled "Owner approves"');
  snapshot("SYNTHESIS", "owner");
  await cta.waitFor({ state: "visible" });

  // Matrix: expand all, then collapse all.
  await scrollBodyTo(page, "#jira-matrix");
  await page.getByRole("button", { name: "Expand all" }).click();
  await page.waitForSelector("text=Streaming CSV serializer", { state: "visible" });
  await page.waitForTimeout(400);
  const openAfterExpand = await page.locator('#jira-matrix [aria-expanded="true"]').count();
  check(openAfterExpand === 4, `Expand all opens every epic and story (${openAfterExpand} open triggers)`);
  await scrollBodyTo(page, null);
  await page.waitForTimeout(200);
  await page.screenshot({ path: SHOTS.page3 });
  console.log("captured page3");
  await scrollBodyTo(page, "#jira-matrix");
  await page.waitForTimeout(300); // scroll-spy catches up
  await page.screenshot({ path: EXTRA_SHOTS.page3Matrix });

  // Filter: matches subtasks by ID, title, or target file.
  const filter = page.getByRole("searchbox", { name: /Filter subtasks/ });
  await filter.fill("csv.go");
  await page.waitForSelector("text=1 of 3 subtasks match", { state: "visible" });
  check((await page.locator("#jira-matrix .pf-subtask").count()) === 1, "filtering by target file (csv.go) leaves one subtask");
  await filter.fill("E2-S1-T1");
  await page.waitForSelector("text=1 of 3 subtasks match", { state: "visible" });
  check((await page.locator("#jira-matrix .pf-subtask").count()) === 1, "filtering by ID (E2-S1-T1) leaves one subtask");
  await filter.fill("streaming");
  await page.waitForSelector("text=1 of 3 subtasks match", { state: "visible" });
  await page.waitForTimeout(300);
  await page.screenshot({ path: EXTRA_SHOTS.page3Filter });
  console.log("captured page3 (filter)");
  await filter.fill("no-such-subtask");
  await page.waitForSelector("text=No subtasks match", { state: "visible" });
  await filter.press("Escape");
  await page.waitForSelector("text=2 epics", { state: "visible" });
  check((await filter.inputValue()) === "", "Esc clears the filter");
  // `/` focuses the filter from anywhere on the page.
  await page.locator("body").click({ position: { x: 700, y: 400 } });
  await page.locator("body").press("/");
  check(await filter.evaluate((el) => el === document.activeElement), "/ focuses the matrix filter");
  await filter.blur();

  // Copy button on a verification command.
  await page.getByRole("button", { name: "Collapse all" }).click();
  await page.getByRole("button", { name: "Expand all" }).click();
  await page.waitForTimeout(350);
  const copyBtn = page.getByRole("button", { name: "Copy command: go test ./internal/audit/..." });
  await copyBtn.scrollIntoViewIfNeeded();
  await copyBtn.click();
  await page.waitForSelector('.pf-copy[data-copied="true"]');
  check(
    (await page.evaluate(() => globalThis.navigator.clipboard.readText())) === "go test ./internal/audit/...",
    "Copy puts the verification command on the clipboard",
  );

  // Scroll-spy: the TOC highlights the section in view.
  await scrollBodyTo(page, "#dual-schedule");
  await page.waitForSelector('.pf-toc-link[data-active="true"]:has-text("Dual schedule")');
  await page.waitForTimeout(300);
  await page.screenshot({ path: EXTRA_SHOTS.page3Schedule });
  console.log("captured page3 (schedule, scrolled)");
  await scrollBodyTo(page, null);
  await page.waitForSelector('.pf-toc-link[data-active="true"]:has-text("Executive summary")');
  console.log("ok: scroll-spy highlights the active section (Dual schedule, then Executive summary)");

  // Mermaid: View source, then Expand into a modal (Esc closes, focus returns).
  await scrollBodyTo(page, "#architecture");
  const firstCard = page.locator("#architecture .pf-diagram-card").first();
  await firstCard.getByRole("button", { name: "View source" }).click();
  await firstCard.locator(".pf-diagram-source").waitFor({ state: "visible" });
  check((await firstCard.locator(".pf-diagram-source").innerText()).includes("graph LR"), "View source shows the diagram source");
  await firstCard.getByRole("button", { name: "Hide source" }).click();
  const expandBtn = firstCard.getByRole("button", { name: "Expand" });
  await expandBtn.click();
  const dialog = page.getByRole("dialog", { name: /Architecture/ });
  await dialog.waitFor({ state: "visible" });
  await dialog.locator("svg").first().waitFor({ state: "visible" });
  await page.waitForTimeout(350);
  await page.screenshot({ path: EXTRA_SHOTS.page3Modal });
  console.log("captured page3 (expanded Mermaid modal)");
  for (let i = 0; i < 6; i++) {
    await page.keyboard.press("Tab");
    check(await page.evaluate(() => document.activeElement?.closest('[role="dialog"]') !== null), `Tab ${i + 1} keeps focus inside the modal`);
  }
  await page.keyboard.press("Escape");
  await dialog.waitFor({ state: "detached" });
  check(await expandBtn.evaluate((el) => el === document.activeElement), "Esc closes the modal and focus returns to the Expand button");

  // Mermaid follows the effective theme: switching to Dark re-renders it.
  const svgBefore = await page.locator("#root figure.pf-mermaid svg").first().evaluate((el) => el.outerHTML.length + el.innerHTML.slice(0, 600));
  await page.locator(".pf-segmented").getByRole("button", { name: "Dark", exact: true }).click();
  await page.waitForFunction(
    (before) => {
      const el = document.querySelector("#root figure.pf-mermaid svg");
      return el !== null && el.outerHTML.length + el.innerHTML.slice(0, 600) !== before;
    },
    svgBefore,
  );
  await page.waitForFunction(() => document.querySelectorAll("#root figure.pf-mermaid svg").length >= 4);
  console.log("ok: Mermaid diagrams re-rendered after the theme change");
  await scrollBodyTo(page, "#architecture");
  await page.waitForTimeout(300);
  await page.screenshot({ path: EXTRA_SHOTS.page3Dark });
  console.log("captured page3 (dark, diagrams re-rendered)");
  await page.locator(".pf-segmented").getByRole("button", { name: "System", exact: true }).click();
  check((await page.evaluate(() => document.documentElement.hasAttribute("data-theme"))) === false, "System removes data-theme (the OS decides)");
  await page.waitForFunction(() => document.querySelectorAll("#root figure.pf-mermaid svg").length >= 4);

  // Reduced motion disables the route, accordion, and chip animations.
  await page.emulateMedia({ reducedMotion: "reduce" });
  const reduced = await page.evaluate(() => {
    const cs = (sel) => globalThis.getComputedStyle(document.querySelector(sel));
    return {
      route: cs(".pf-route").animationName,
      collapse: cs(".pf-collapse").transitionDuration,
      chip: cs(".pf-chip").transitionDuration,
    };
  });
  check(
    reduced.route === "none" && /^0s(, 0s)*$/.test(reduced.collapse) && /^0s(, 0s)*$/.test(reduced.chip),
    "prefers-reduced-motion: reduce disables route, accordion, and chip motion",
  );
  await page.emulateMedia({ reducedMotion: "no-preference" });

  // Layout at 1024x700: no horizontal page scroll, on every page, with the
  // sidebar expanded and collapsed (an explicit choice wins over the viewport,
  // so both states are reached through the real toggle).
  await page.setViewportSize({ width: 1024, height: 700 });
  await page.waitForTimeout(300);
  for (const collapsed of [false, true]) {
    const label = collapsed ? "collapsed" : "expanded";
    const collapsedNow = (await page.evaluate(() => document.querySelector(".pf-sidebar")?.getAttribute("data-collapsed"))) === "true";
    if (collapsed !== collapsedNow) await page.locator("body").press("[");
    await page.waitForSelector(`.pf-sidebar[data-collapsed="${String(collapsed)}"]`);
    await page.waitForTimeout(350);
    for (const [route, navLabel] of [["page3", "Synthesis"], ["page2", "Deliberation"], ["page1", "Ingestion"]]) {
      await page.locator(".pf-sidebar-nav").getByRole("button", { name: navLabel }).click();
      await page.waitForTimeout(350);
      const offenders = await noHorizontalScroll(page);
      check(offenders.length === 0, `no horizontal scroll at 1024x700 on ${route} with the sidebar ${label}${offenders.length ? `: ${offenders.join("; ")}` : ""}`);
      if (collapsed && route === "page2") await page.screenshot({ path: EXTRA_SHOTS.page2Narrow });
      if (collapsed && route === "page3") await page.screenshot({ path: EXTRA_SHOTS.page3Narrow });
    }
  }
  await page.locator(".pf-sidebar-nav").getByRole("button", { name: "Synthesis" }).click();
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.locator("body").press("["); // back to the expanded sidebar for the handoff shot
  await page.waitForSelector('.pf-sidebar[data-collapsed="false"]');
  await page.waitForTimeout(350);

  // ---- Handoff: the owner approves; the relay broadcasts PLAN_APPROVED; the
  // window shows the three-step handoff panel and the close countdown.
  await scrollBodyTo(page, null);
  await page.getByRole("button", { name: "Approve & open in Claude Code Plan Mode" }).click();
  await page.waitForSelector(".pf-handoff", { state: "visible" });
  await page.waitForSelector("text=This window closes in", { state: "visible" });
  await page.waitForTimeout(250);
  await page.screenshot({ path: EXTRA_SHOTS.page3Handoff });
  console.log("captured page3 (handoff panel)");
  const handoff = await page.evaluate(() => ({
    steps: [...document.querySelectorAll(".pf-handoff-step .pf-handoff-label")].map((el) => el.textContent?.replace(/\s+/g, " ").trim()),
    close: document.querySelector(".pf-handoff-close")?.textContent,
    stepper: document.querySelector('.pf-phase-step[aria-current="step"] .pf-phase-label')?.textContent,
  }));
  check(
    handoff.steps.length === 3 &&
      handoff.steps[0] === "Approved" &&
      handoff.steps[1] === "Claude Code enters Plan Mode" &&
      handoff.steps[2].startsWith("Validation runs automatically") &&
      handoff.steps[2].includes("plan-forge validate"),
    `handoff panel lists three steps: ${JSON.stringify(handoff.steps)}`,
  );
  check(/^This window closes in [1-5] s$/.test(handoff.close ?? ""), `countdown reads "${handoff.close}"`);
  check(handoff.stepper === "Approved", "stepper reaches Approved");
  await page.waitForSelector("text=This window closes in 3 s", { state: "visible", timeout: 6_000 });
  console.log("ok: the close countdown ticks down (5 -> 3 s observed)");

  // ---- Narrow viewport with no saved choice: the sidebar collapses by itself.
  await page.close();
  const narrowContext = await browser.newContext({ viewport: { width: 1024, height: 700 } });
  const narrow = await narrowContext.newPage();
  narrow.setDefaultTimeout(15_000);
  await narrow.addInitScript((wsUrl) => {
    globalThis.PLAN_FORGE_WS_URL = wsUrl;
  }, `ws://127.0.0.1:${port}/ws`);
  narrow.on("pageerror", (e) => {
    pageErrors.push(e.stack ?? String(e));
    console.log("pageerror:", (e.stack ?? String(e)).slice(0, 1200));
  });
  currentPage = "page1";
  await narrow.goto(`${origin}/`);
  await narrow.waitForSelector("text=Export invoices", { state: "visible" });
  await narrow.waitForSelector('.pf-sidebar[data-collapsed="true"]');
  await narrow.waitForTimeout(350);
  check((await noHorizontalScroll(narrow)).length === 0, "no horizontal scroll at 1024x700 on first load (sidebar auto-collapsed)");
  await narrow.screenshot({ path: EXTRA_SHOTS.page1Narrow });
  console.log("captured page1 (1024px, sidebar auto-collapsed)");
  await narrowContext.close();

  // Verification: every required PNG is newer than this run and populated.
  for (const [label, file] of Object.entries(SHOTS)) assertFresh(label, file, { minBytes: MIN_BYTES });
  for (const [label, file] of Object.entries(EXTRA_SHOTS)) assertFresh(label, file, { minBytes: MIN_EXTRA_BYTES });

  check(pageErrors.length === 0, `no pageerror during the run (${pageErrors.length})`);
  check(foreignRequests.length === 0, `no request left the origin${foreignRequests.length ? `: ${foreignRequests.join(", ")}` : ""}`);

  console.log("\nWS event sequence (-> relay to app, <- app to relay):");
  for (const [name, events] of Object.entries(wireLog)) {
    console.log(`  ${name}: ${events.filter((e) => !e.includes("TEXT_CHECK")).join(" | ")}`);
  }
  console.log(`done -> ${outDir}/`);
} catch (err) {
  console.error("preview harness failed:", err instanceof Error ? err.message : err);
  exitCode = 1;
} finally {
  await browser?.close();
  wss.close();
  server.close();
}
process.exit(exitCode);
