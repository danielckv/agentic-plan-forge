// Preview capture: serve web/dist statically and screenshot the three pages
// with a fake relay so each page renders with real seeded state.
// Throwaway harness for visual review, not part of the test suite.
//
// Run: npm run build && node scripts/preview.mjs
// Output: /tmp/plan-forge-preview/page{1,2,3}-*.png (+ scrolled detail shots)
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
//          <- SYNTHESIZE_REQUEST (click "Synthesize Master Spec")
//             -> PAGE_TRANSITION(SYNTHESIS), STATE_SNAPSHOT(SYNTHESIS),
//                AVATAR_DRAFT(synthesis.v1)
import { createServer } from "node:http";
import { readFileSync, existsSync, mkdirSync, rmSync, statSync } from "node:fs";
import { extname, join } from "node:path";
import { WebSocketServer } from "ws";
import { chromium } from "@playwright/test";

const dist = new URL("../dist", import.meta.url).pathname;
const port = 8791;
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
  page2Decisions: `${outDir}/page2-deliberation-decisions.png`,
  page3Matrix: `${outDir}/page3-synthesis-matrix.png`,
  page3Schedule: `${outDir}/page3-synthesis-schedule.png`,
};
const MIN_BYTES = 100 * 1024; // populated 1440x900 page

const runStart = Date.now();

// Hard upper bound for the whole run; every individual wait is bounded too.
setTimeout(() => {
  console.error("harness watchdog: run exceeded 120s");
  process.exit(2);
}, 120_000).unref();

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
// authority. It reacts to client commands; nothing is pushed on a timer.
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

function snapshot(phase) {
  send(
    "STATE_SNAPSHOT",
    { phase, doc_version: "12", role: "owner", members: MEMBERS, doc_text: PRD_TEXT },
    `STATE_SNAPSHOT(${phase})`,
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

// The owner picked an option: broadcast the resolution, then close the open
// HIGH finding so the Synthesize footer unlocks (as convergence would).
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
  send("AVATAR_THOUGHT", {
    run_id: RUN_ID, avatar_id: "architect", cycle: 3, status: "Synced",
    public_summary: "Idempotency key added to the retry path; streaming download chosen.",
  }, "AVATAR_THOUGHT(architect:Synced)");
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

let exitCode = 0;
let browser;
try {
  await new Promise((r) => server.listen(port, "127.0.0.1", r));
  mkdirSync(outDir, { recursive: true });
  // No stale PNG may survive a run: remove every output before capturing.
  for (const f of [...Object.values(SHOTS), ...Object.values(EXTRA_SHOTS)]) rmSync(f, { force: true });

  browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  page.setDefaultTimeout(15_000);
  page.setDefaultNavigationTimeout(15_000);
  await page.addInitScript((wsUrl) => {
    globalThis.PLAN_FORGE_WS_URL = wsUrl;
  }, `ws://127.0.0.1:${port}/ws`);
  page.on("pageerror", (e) => console.log("pageerror:", String(e).slice(0, 200)));
  page.on("console", (m) => {
    if (m.type() === "error" || m.type() === "warning") console.log("console:", m.type(), m.text().slice(0, 200));
  });

  // Page 1 — Ingestion: seeded PRD in the shared editor, healthy pills,
  // connected chrome (role + presence only render once the snapshot lands).
  currentPage = "page1";
  await page.goto(`http://127.0.0.1:${port}/`);
  await page.waitForSelector("text=Export invoices", { state: "visible" });
  await page.waitForSelector("text=Signed in as owner", { state: "visible" });
  await page.waitForSelector("text=AST OK", { state: "visible" }); // status pills poll
  await page.locator('input[type="checkbox"]').check();
  await page.screenshot({ path: SHOTS.page1 });
  console.log("captured page1");

  // Page 2 — Deliberation: the owner's real "Confirm" click sends
  // FREEZE_REQUEST; the relay answers with PAGE_TRANSITION and the seeded run.
  currentPage = "page2";
  await page.getByRole("button", { name: /Confirm Context/ }).click();
  await page.waitForSelector("h1:has-text('Avatar deliberation')", { state: "visible" });
  await page.waitForSelector("text=Awaiting decision", { state: "visible" });
  await page.getByText("Technical Architect", { exact: true }).click();
  await page.locator("figure.pf-mermaid svg").first().waitFor({ state: "visible" }); // architect diagram
  await page.screenshot({ path: SHOTS.page2 });
  console.log("captured page2");
  await scrollBodyTo(page, "bottom");
  await page.screenshot({ path: EXTRA_SHOTS.page2Decisions });
  console.log("captured page2 (decisions, scrolled)");

  // Page 3 — Synthesis: resolve the decision (relay closes ISS-2), then the
  // owner's "Synthesize Master Spec" click sends SYNTHESIZE_REQUEST and the
  // relay answers with PAGE_TRANSITION(SYNTHESIS) + the synthesis.v1 draft.
  await page.getByRole("button", { name: /Streaming download/ }).click();
  await page.waitForSelector("text=Resolved by owner:daniel", { state: "visible" });
  currentPage = "page3";
  await page.waitForSelector("button:not([disabled]):has-text('Synthesize Master Spec')", { state: "visible" });
  await page.getByRole("button", { name: /Synthesize Master Spec/ }).click();
  await page.waitForSelector("h1:has-text('Master specification')", { state: "visible" });
  await page.getByText("CSV export flow", { exact: true }).click();
  await page.getByText("As a finance analyst", { exact: false }).first().click();
  // Architecture, sequence, human gantt, agent gantt.
  await page.waitForFunction(() => document.querySelectorAll("figure.pf-mermaid svg").length >= 4);
  await scrollBodyTo(page, null);
  await page.screenshot({ path: SHOTS.page3 });
  console.log("captured page3");
  await scrollBodyTo(page, "#jira-matrix");
  await page.screenshot({ path: EXTRA_SHOTS.page3Matrix });
  await scrollBodyTo(page, "#dual-schedule");
  await page.screenshot({ path: EXTRA_SHOTS.page3Schedule });
  console.log("captured page3 (matrix + schedule, scrolled)");

  // Verification: every required PNG is newer than this run and populated.
  for (const [label, file] of Object.entries(SHOTS)) assertFresh(label, file, { minBytes: MIN_BYTES });
  for (const [label, file] of Object.entries(EXTRA_SHOTS)) assertFresh(label, file, {});

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
