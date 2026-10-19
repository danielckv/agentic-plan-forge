// Board preview capture: serve web/dist with a fake C5 board API and push authority.
// Run after the production build. Outputs /tmp/plan-forge-preview/board-*.png.
import { createServer } from "node:http";
import { existsSync, mkdirSync, readFileSync, rmSync, statSync } from "node:fs";
import { extname, join } from "node:path";
import { chromium } from "@playwright/test";
import { WebSocketServer } from "ws";

const dist = new URL("../dist", import.meta.url).pathname;
const port = 8792;
const outDir = "/tmp/plan-forge-preview";
const shots = {
  light: `${outDir}/board-light.png`,
  dark: `${outDir}/board-dark.png`,
  drawer: `${outDir}/board-drawer.png`,
  moved: `${outDir}/board-moved.png`,
};
const mime = {
  ".html": "text/html",
  ".js": "text/javascript",
  ".css": "text/css",
  ".svg": "image/svg+xml",
  ".json": "application/json",
  ".woff2": "font/woff2",
};
const runStart = Date.now();
const capturedBodies = [];
let noteSequence = 4;
let clientFrames = 0;

setTimeout(() => {
  console.error("board harness watchdog: run exceeded 120s");
  process.exit(2);
}, 120_000).unref();

function task(id, title, column, priority, options = {}) {
  const numericID = Number(id.slice(2));
  const verifiedTargets = column === "done" ? 2 : column === "in_progress" ? 1 : 0;
  const passedChecks = column === "done" ? 2 : column === "in_progress" ? 1 : 0;
  return {
    id,
    epic_id: options.epic ?? `E-${String(((numericID - 1) % 3) + 1)}`,
    story_id: `S-${String(numericID)}`,
    workstream_id: options.workstream ?? ["W-platform", "W-web", "W-quality"][numericID % 3],
    title,
    column,
    blocked: options.blocked ?? false,
    depends_on: options.depends_on ?? [],
    priority,
    targets: [
      {
        path: options.path ?? `internal/board/task_${String(numericID)}.go`,
        operation: numericID % 4 === 0 ? "modify" : "add",
        symbol_id: `Task${String(numericID)}`,
        verified: verifiedTargets >= 1,
      },
      {
        path: numericID % 3 === 0 ? "web/src/board/BoardApp.tsx" : `internal/board/task_${String(numericID)}_test.go`,
        operation: numericID % 3 === 0 ? "modify" : "add",
        symbol_id: "",
        verified: verifiedTargets >= 2,
      },
    ],
    verification: { total: 2, passed: passedChecks },
    acceptance_criteria: [
      `The ${title.toLocaleLowerCase()} flow satisfies the approved plan.`,
      "Automated checks cover the user-visible behavior and failure path.",
    ],
    human_estimate: { likely: 4 + numericID, basis: "Implementation, review, and validation" },
    agent_estimate: { likely: 8 + numericID * 2, basis: "Grounded edit plus focused test pass" },
    notes: options.notes ?? [],
  };
}

let board = {
  repo: "agentic-plan-forge",
  title: "GUI polish, plan-mode handoff, and live board",
  manifest_sha256: "9c08e91eb8a74e12a531864a775b0616e61582ce3e2e97fc87f4aa74d79891d2",
  approved: true,
  status: "DRIFTED",
  verified: 14,
  total: 20,
  revision: 7,
  host: "claude",
  evaluated_at: "2026-10-19T14:42:00Z",
  drift: [
    { code: "target_modified", detail: "web/src/main.tsx changed after plan approval" },
    { code: "receipt_stale", detail: "T-10 verification receipt predates its target" },
  ],
  tasks: [
    task("T-1", "Persist board overlays with cross-process locking", "todo", "normal", {
      workstream: "W-platform",
      notes: [{ id: "n-1", at: "2026-10-19T12:04:00Z", text: "Keep the approved manifest immutable." }],
    }),
    task("T-2", "Expose the authenticated board snapshot endpoint", "todo", "high", {
      workstream: "W-platform",
      path: "internal/app/board_http.go",
    }),
    task("T-3", "Document board launch and recovery behavior", "todo", "low", {
      workstream: "W-quality",
      path: "README.md",
    }),
    task("T-4", "Deliver board deltas to the active Claude session", "in_progress", "high", {
      blocked: true,
      depends_on: ["T-2"],
      workstream: "W-hosts",
      path: "cmd/client/cmd_hook.go",
      notes: [{ id: "n-2", at: "2026-10-19T12:18:00Z", text: "Never bypass host permission prompts." }],
    }),
    task("T-5", "Build the task detail drawer and context controls", "in_progress", "normal", {
      workstream: "W-web",
      path: "web/src/board/BoardDrawer.tsx",
    }),
    task("T-6", "Reconnect board snapshots after bridge restarts", "in_progress", "low", {
      workstream: "W-web",
      path: "web/src/board/boardStore.ts",
    }),
    task("T-7", "Validate approved plan evidence", "done", "high", {
      workstream: "W-platform",
      path: "internal/planstate/validate.go",
      notes: [{ id: "n-3", at: "2026-10-19T13:03:00Z", text: "Validation must remain bounded by the CLI timeout." }],
    }),
    task("T-8", "Register the oh-my-pi board command", "done", "normal", {
      workstream: "W-hosts",
      path: "integrations/omp-hook-plan-forge.ts",
    }),
    task("T-9", "Render board filters and independently scrolling columns", "done", "low", {
      workstream: "W-web",
      path: "web/src/board/BoardApp.tsx",
    }),
    task("T-10", "Reconcile changed host integration tests", "drifted", "high", {
      workstream: "W-quality",
      path: "internal/host/hooks_test.go",
    }),
    task("T-11", "Refresh stale verification receipts", "drifted", "normal", {
      workstream: "W-quality",
      path: "internal/planstate/evidence.go",
    }),
    task("T-12", "Update the release packaging smoke check", "drifted", "low", {
      workstream: "W-hosts",
      path: "scripts/package-release.sh",
    }),
  ],
  consumers: [
    { id: "claude:preview-session", delivered_revision: 6, seen_at: "2026-10-19T14:41:50Z" },
  ],
};

function sendJSON(res, status, value) {
  res.statusCode = status;
  res.setHeader("Content-Type", "application/json");
  res.end(JSON.stringify(value));
}

async function readJSON(req) {
  const chunks = [];
  let length = 0;
  for await (const chunk of req) {
    length += chunk.length;
    if (length > 64 * 1024) throw new Error("request too large");
    chunks.push(chunk);
  }
  return JSON.parse(Buffer.concat(chunks).toString("utf8"));
}

function pushSnapshot() {
  const frame = JSON.stringify({ type: "board.snapshot", snapshot: board });
  for (const socket of wss.clients) {
    if (socket.readyState === 1) socket.send(frame);
  }
}

async function mutateBoard(req, res) {
  let body;
  try {
    body = await readJSON(req);
  } catch (error) {
    sendJSON(res, 400, { error: error instanceof Error ? error.message : "invalid JSON" });
    return;
  }
  if (typeof body !== "object" || body === null || Array.isArray(body) || typeof body.id !== "string") {
    sendJSON(res, 400, { error: "id is required" });
    return;
  }
  const actions = ["priority", "note", "delete_note"].filter((key) => Object.hasOwn(body, key));
  if (actions.length !== 1 || Object.keys(body).length !== 2) {
    sendJSON(res, 400, { error: "exactly one action is required" });
    return;
  }
  const boardTask = board.tasks.find((candidate) => candidate.id === body.id);
  if (boardTask === undefined) {
    sendJSON(res, 404, { error: "unknown task" });
    return;
  }
  capturedBodies.push(body);
  if (actions[0] === "priority") {
    if (!["high", "normal", "low"].includes(body.priority)) {
      sendJSON(res, 400, { error: "invalid priority" });
      return;
    }
    boardTask.priority = body.priority;
  } else if (actions[0] === "note") {
    if (typeof body.note !== "string" || body.note.trim() === "") {
      sendJSON(res, 400, { error: "note is required" });
      return;
    }
    noteSequence += 1;
    boardTask.notes.push({
      id: `n-${String(noteSequence)}`,
      at: "2026-10-19T14:45:00Z",
      text: body.note.trim(),
    });
  } else {
    boardTask.notes = boardTask.notes.filter((note) => note.id !== body.delete_note);
  }
  board.revision += 1;
  sendJSON(res, 200, board);
  pushSnapshot();
}

const server = createServer(async (req, res) => {
  const url = req.url ?? "/";
  if (req.method === "GET" && url.startsWith("/api/board")) {
    sendJSON(res, 200, board);
    return;
  }
  if (req.method === "POST" && url.startsWith("/api/board/task")) {
    await mutateBoard(req, res);
    return;
  }
  if (url.startsWith("/api/session")) {
    sendJSON(res, 200, { token: "preview-token" });
    return;
  }
  const pathname = url.split("?")[0].split("#")[0];
  const assetPath = pathname === "/" || pathname === "/board" ? "/index.html" : pathname;
  const file = join(dist, assetPath);
  const target = existsSync(file) && !file.endsWith("/") ? file : join(dist, "index.html");
  res.setHeader("Content-Type", mime[extname(target)] ?? "application/octet-stream");
  res.end(readFileSync(target));
});

const wss = new WebSocketServer({
  server,
  handleProtocols: (protocols) => (protocols.has("plan-forge.v1") ? "plan-forge.v1" : false),
});

wss.on("connection", (socket) => {
  socket.on("message", () => {
    clientFrames += 1;
  });
  socket.send(JSON.stringify({ type: "auth.ok" }));
  setTimeout(() => {
    if (socket.readyState === 1) {
      socket.send(JSON.stringify({ type: "board.snapshot", snapshot: board }));
    }
  }, 15);
});

function assertFresh(label, file) {
  const stats = statSync(file);
  if (stats.mtimeMs < runStart) throw new Error(`${label}: stale output ${file}`);
  if (stats.size < 45 * 1024) throw new Error(`${label}: ${file} is only ${String(stats.size)} bytes`);
  console.log(`verified ${label}: ${file} (${String(stats.size)} bytes)`);
}

let browser;
let exitCode = 0;
try {
  await new Promise((resolve) => server.listen(port, "127.0.0.1", resolve));
  mkdirSync(outDir, { recursive: true });
  for (const file of Object.values(shots)) rmSync(file, { force: true });

  browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  page.setDefaultTimeout(15_000);
  page.setDefaultNavigationTimeout(15_000);
  await page.addInitScript((wsURL) => {
    globalThis.PLAN_FORGE_WS_URL = wsURL;
    globalThis.PLAN_FORGE_VIEW = "board";
  }, `ws://127.0.0.1:${String(port)}/ws`);
  page.on("pageerror", (error) => console.log("pageerror:", String(error).slice(0, 300)));
  page.on("console", (message) => {
    if (message.type() === "error" || message.type() === "warning") {
      console.log("console:", message.type(), message.text().slice(0, 300));
    }
  });

  await page.emulateMedia({ colorScheme: "light", reducedMotion: "no-preference" });
  await page.goto(`http://127.0.0.1:${String(port)}/`);
  await page.locator('[data-task-id="T-1"]').waitFor({ state: "visible" });
  await page.waitForFunction(() => document.querySelectorAll("[data-task-id]").length === 12);
  await page.screenshot({ path: shots.light });

  await page.emulateMedia({ colorScheme: "dark", reducedMotion: "no-preference" });
  await page.waitForTimeout(120);
  await page.screenshot({ path: shots.dark });

  await page.emulateMedia({ colorScheme: "light", reducedMotion: "no-preference" });
  await page.getByRole("button", { name: /Open T-5:/ }).click();
  const noteText = "Reuse the existing focus-restoration path when the drawer closes.";
  const noteResponse = page.waitForResponse(
    (response) => response.url().endsWith("/api/board/task") && response.request().method() === "POST",
  );
  await page.getByLabel("Add context for the agent").fill(noteText);
  await page.getByLabel("Add context for the agent").press(
    process.platform === "darwin" ? "Meta+Enter" : "Control+Enter",
  );
  await noteResponse;
  await page.getByText(noteText, { exact: true }).waitFor({ state: "visible" });
  await page.waitForTimeout(220);
  await page.locator(".pf-board-drawer-scroll").evaluate((element) => {
    element.scrollTop = element.scrollHeight;
  });
  await page.waitForTimeout(60);
  await page.screenshot({ path: shots.drawer });

  await page.keyboard.press("Escape");
  const movingTask = board.tasks.find((candidate) => candidate.id === "T-2");
  if (movingTask === undefined) throw new Error("fixture task T-2 missing");
  movingTask.column = "in_progress";
  pushSnapshot();
  await page.locator('.pf-board-column--in_progress [data-task-id="T-2"]').waitFor({ state: "visible" });
  await page.waitForTimeout(340);
  await page.screenshot({ path: shots.moved });

  for (const [label, file] of Object.entries(shots)) assertFresh(label, file);
  if (capturedBodies.length === 0) throw new Error("drawer edit did not reach POST /api/board/task");
  if (clientFrames !== 0) throw new Error(`board sent ${String(clientFrames)} unexpected WebSocket frame(s)`);
  console.log(`captured board POST bodies: ${JSON.stringify(capturedBodies)}`);
  console.log(`done -> ${outDir}/board-{light,dark,drawer,moved}.png`);
} catch (error) {
  console.error("board preview harness failed:", error instanceof Error ? error.message : error);
  exitCode = 1;
} finally {
  await browser?.close();
  wss.close();
  server.close();
}
process.exit(exitCode);
