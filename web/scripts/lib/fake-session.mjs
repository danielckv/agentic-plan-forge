// A fake planning session for the preview and promo harnesses: a static
// server for web/dist, the bridge's read-only API, and a WebSocket "relay"
// that plays the authority. It answers the commands the window sends with
// the frames the Go side sends, in the Go shapes:
//
//   HELLO              -> STATE_SNAPSHOT(INGESTION) with the seeded brief
//   TEXT_DELTA         -> TEXT_ACK (the change is applied to the document),
//                         then a new interpretation revision 400 ms later
//   FREEZE_REQUEST     -> PAGE_TRANSITION(DELIBERATION) and the avatar run
//   DECISION_RESOLVE   -> the resolution, the closing critique, settled avatars
//   SYNTHESIZE_REQUEST -> PAGE_TRANSITION(SYNTHESIS) and the synthesis draft
//   APPROVE            -> PLAN_APPROVED
//
// Not part of the test suite: it exists so a person can look at the window.
import { createServer } from "node:http";
import { existsSync, readFileSync } from "node:fs";
import { extname, join } from "node:path";
import { WebSocketServer } from "ws";
import { ChangeSet, Text } from "@codemirror/state";

import * as S from "./scenario.mjs";

const mime = {
  ".html": "text/html", ".js": "text/javascript", ".css": "text/css",
  ".svg": "image/svg+xml", ".json": "application/json", ".woff2": "font/woff2", ".png": "image/png",
};

/**
 * Start the session. Returns { origin, wsUrl, send, snapshot, state, wireLog, close }.
 * `options.brief` replaces the seeded document; `options.interpreted` starts
 * with the plan files already present.
 */
export async function startFakeSession({ port, dist, brief = S.BRIEF, interpreted = true }) {
  const state = {
    phase: "INGESTION",
    docText: brief,
    docVersion: 1,
    interpretation: interpreted ? 1 : 0,
    seq: 0,
    page: "page1",
  };
  const wireLog = { page1: [], page2: [], page3: [] };

  const json = (res, body) => {
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify(body));
  };

  const server = createServer((req, res) => {
    const url = new URL(req.url ?? "/", "http://localhost");
    const p = url.pathname;
    if (p === "/api/session") return json(res, { token: "preview-token" });
    if (p === "/api/status") {
      return json(res, {
        ...S.STATUS,
        interpretation: state.interpretation,
        impactRevision: state.interpretation > 0 ? S.IMPACT.revision : 0,
      });
    }
    if (p === "/api/context") return json(res, S.CONTEXT);
    if (p === "/api/impact") return json(res, S.IMPACT);
    if (p === "/api/plan-files") {
      return json(res, { revision: state.interpretation, files: state.interpretation > 0 ? S.PLAN_FILES : [] });
    }
    if (p === "/api/components") {
      const module = url.searchParams.get("module");
      const q = (url.searchParams.get("q") ?? "").toLowerCase();
      const symbols = module
        ? S.COMPONENTS.filter((c) => c.module === module)
        : q
          ? S.COMPONENTS.filter((c) => c.name.toLowerCase().includes(q) || c.path.includes(q))
          : [];
      return json(res, { ready: true, stack: ["go", "typescript"], modules: S.MODULES, symbols, truncated: false });
    }
    let path = p;
    if (path === "/" || path === "/ingest" || path === "/deliberate" || path === "/synthesis") path = "/index.html";
    const file = join(dist, path);
    const target = existsSync(file) && !file.endsWith("/") ? file : join(dist, "index.html");
    res.setHeader("Content-Type", mime[extname(target)] || "application/octet-stream");
    res.end(readFileSync(target));
  });

  const wss = new WebSocketServer({
    server,
    handleProtocols: (protocols) => (protocols.has("plan-forge.v1") ? "plan-forge.v1" : false),
  });
  let socket = null;

  const send = (eventType, payload, label = eventType) => {
    if (socket === null || socket.readyState !== 1) throw new Error(`relay cannot send ${eventType}: no open socket`);
    state.seq += 1;
    socket.send(JSON.stringify({
      version: 1, session_id: "preview-session", sender_id: "authority", event_id: crypto.randomUUID(),
      event_type: eventType, seq: String(state.seq), revision: String(state.seq), epoch: "1",
      timestamp: new Date().toISOString(), payload,
    }));
    wireLog[state.page]?.push(`-> ${label}`);
  };

  const snapshot = (phase = state.phase, role = "owner") => {
    state.phase = phase;
    send("STATE_SNAPSHOT", {
      phase, doc_version: String(state.docVersion), role, members: S.MEMBERS, doc_text: state.docText,
    }, `STATE_SNAPSHOT(${phase}:${role})`);
  };

  const later = (ms, fn) => setTimeout(() => {
    try {
      fn();
    } catch {
      /* the window went away */
    }
  }, ms);

  // Where each avatar runs, as the router places it: a run spreads its roles
  // across every host the machine is signed in to.
  const PLACED = {
    pm: { host: "claude", model: "opus" },
    architect: { host: "claude", model: "opus" },
    "specialist:reporting": { host: "omp", model: "claude-sonnet-5-5" },
    "specialist:audit": { host: "omp", model: "claude-sonnet-5-5" },
    critic: { host: "omp", model: "claude-opus-5-5" },
    synthesizer: { host: "claude", model: "opus" },
    narrator: { host: "omp", model: "claude-haiku-5-5" },
  };

  const thought = (avatar, status, summary, cycle, extra = {}) =>
    send("AVATAR_THOUGHT", {
      run_id: S.RUN_ID, avatar_id: avatar, cycle, status, public_summary: summary,
      ...(PLACED[avatar] ?? {}), ...extra,
    }, `AVATAR_THOUGHT(${avatar}:${status})`);
  const draft = (avatar, schema, content, rev, cycle) =>
    send("AVATAR_DRAFT", {
      run_id: S.RUN_ID, avatar_id: avatar, cycle, draft_id: `${avatar}-draft`, draft_revision: String(rev),
      schema_id: schema, content, content_hash: `${avatar}-${rev}`, complete: true,
    }, `AVATAR_DRAFT(${avatar})`);

  // The run, as the orchestrator publishes it: thoughts as stages start and
  // finish, drafts once validated, one critique set per barrier.
  const deliberation = [
    () => send("PAGE_TRANSITION", { from: "INGESTION", to: "DELIBERATION" }, "PAGE_TRANSITION(DELIBERATION)"),
    () => snapshot("DELIBERATION"),
    () => send("RUN_ASSIGNED", { run_id: S.RUN_ID }),
    () => send("LEASE_GRANTED", { holder_id: "owner:dana" }),
    () => thought("pm", "Thinking", "Reading the frozen requirements: 4 requirements, 2 acceptance criteria.", 1),
    () => thought("pm", "Drafting", "Grouping the requirements into epics and user stories.", 1),
    () => draft("pm", "pm.v1", S.PM_DOC, 1, 1),
    () => thought("pm", "Synced", "Drafted 3 epics and 3 stories; every requirement traces to a story.", 1),
    () => thought("architect", "Thinking", "Mapping the epics onto internal/reporting, internal/audit and the web app.", 1),
    () => thought("architect", "Drafting", "ADR 7 says exports stream; designing the CSV writer around QueryInvoices.", 1, {
      step: "design 1/3", context_note: "graph 184 files · context 12 excerpts · drift 20 items · 201 KiB",
    }),
    // The heartbeat a running call emits: the card takes it, the discussion
    // does not.
    () => thought("architect", "Drafting", "Running for 40s, 7.2 KB written, now on contracts.", 1, {
      step: "design 1/3", context_note: "graph 184 files · context 12 excerpts · drift 20 items · 201 KiB",
      elapsed_ms: 40000, output_bytes: 7372,
    }),
    () => thought("narrator", "Narrating",
      "The architect is mapping the CSV export onto internal/reporting and is 40 seconds into its design call; both specialists are waiting on its contracts.",
      1),
    () => draft("architect", "architect.v1", S.ARCHITECT_DOC, 1, 1),
    () => thought("architect", "Synced", "3 workstreams: reporting, audit, web. The CSV writer streams from QueryInvoices.", 1),
    () => thought("specialist:reporting", "Drafting", "Writing tasks for the streaming CSV writer and its trailer row.", 1),
    () => thought("specialist:audit", "Drafting", "Keying audit events by export ID.", 1),
    () => thought("critic", "Critiquing", "Attacking failure states: timeouts mid-export, retries, role checks.", 2),
    () => send("AVATAR_CRITIQUE", { run_id: S.RUN_ID, cycle: 2, issues: S.CRITIQUES }, "AVATAR_CRITIQUE(3 issues)"),
    () => thought("critic", "Synced", "1 CRITICAL resolved; 1 HIGH and 1 MEDIUM open.", 2),
    () => thought("architect", "Revising", "Revising for C-2: audit events keyed by export ID.", 2),
    () => thought("synthesizer", "Thinking", "Waiting for the design to converge.", 2),
    () => send("DECISION_PROMPT", S.DECISION),
  ];

  const resolution = (payload) => [
    () => send("DECISION_RESOLVE", { decision_id: payload.decision_id, option_id: payload.option_id, actor_id: "owner:dana" }, "DECISION_RESOLVE"),
    () => send("AVATAR_CRITIQUE", {
      run_id: S.RUN_ID, cycle: 3,
      issues: S.CRITIQUES.map((c) => (c.id === "C-2" ? { ...c, status: "resolved", resolution_ref: "audit" } : c)),
    }, "AVATAR_CRITIQUE(C-2 resolved)"),
    () => thought("architect", "Synced", "Staged files with signed links; audit events keyed by export ID.", 3),
    () => thought("specialist:reporting", "Synced", "6 tasks, all targets in internal/reporting.", 3),
    () => thought("specialist:audit", "Synced", "2 tasks in internal/audit.", 3),
    () => thought("critic", "Synced", "No blocking finding left; C-3 carried into the spec as a note.", 3),
    () => thought("synthesizer", "Synced", "Ready to compile the master specification.", 3),
  ];

  const play = (steps, gap) => steps.forEach((step, i) => later(i * gap, step));

  wss.on("connection", (ws) => {
    socket = ws;
    ws.on("message", (raw) => {
      let msg;
      try {
        msg = JSON.parse(String(raw));
      } catch {
        return;
      }
      wireLog[state.page]?.push(`<- ${msg.event_type}`);
      switch (msg.event_type) {
        case "HELLO":
          snapshot(state.phase);
          break;
        case "TEXT_DELTA": {
          const p = msg.payload ?? {};
          if (Number(p.base_version) !== state.docVersion) {
            send("TEXT_REJECT", { document_id: "prd", head_version: String(state.docVersion), reason: "stale_base", updates: [] });
            break;
          }
          for (const u of p.updates ?? []) {
            state.docText = ChangeSet.fromJSON(u.changes).apply(Text.of(state.docText.split("\n"))).toString();
            state.docVersion += 1;
          }
          send("TEXT_ACK", { document_id: "prd", version: String(state.docVersion) });
          later(400, () => {
            state.interpretation += 1;
          });
          break;
        }
        case "FREEZE_REQUEST":
          state.page = "page2";
          play(deliberation, 90);
          break;
        case "DECISION_RESOLVE":
          play(resolution(msg.payload ?? {}), 90);
          break;
        case "SYNTHESIZE_REQUEST":
          state.page = "page3";
          send("PAGE_TRANSITION", { from: "DELIBERATION", to: "SYNTHESIS" }, "PAGE_TRANSITION(SYNTHESIS)");
          snapshot("SYNTHESIS");
          draft("synthesizer", "synthesis.v1", { spec_markdown: S.SPEC_MARKDOWN, manifest_json: JSON.stringify(S.MANIFEST) }, 1, 3);
          break;
        case "APPROVE":
          send("PLAN_APPROVED", {
            approval_id: "appr-0001", approved_revision: "1", spec_sha256: "a".repeat(64),
            manifest_sha256: "b".repeat(64), bundle_id: "bundle-0001", persisted_by: "owner:dana",
          });
          break;
        default:
          break;
      }
    });
  });

  await new Promise((resolve) => server.listen(port, "127.0.0.1", resolve));
  return {
    origin: `http://127.0.0.1:${port}`,
    wsUrl: `ws://127.0.0.1:${port}/ws`,
    send,
    snapshot,
    state,
    wireLog,
    close: () => {
      wss.close();
      server.close();
    },
  };
}
