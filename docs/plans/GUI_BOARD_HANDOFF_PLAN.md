# GUI polish, plan-mode handoff, and `/plan-forge-board`

Status: implemented (2026-10-19), with all gates green. Owner: main session. This is the cross-slice contract every worker coded against. The OMP plan-mode mechanism in C7 was corrected during implementation; see "Revision 2026-10-19".
Contracts in this file are fixed. A worker who needs to change a contract stops and reports the request back to the main session.

## Goals

1. **GUI.** Make the three-page window more fluent, flexible, and polished. Fix the Mermaid render crash in the built bundle (`TypeError: Hi is not a function` from `web/scripts/preview.mjs`).
2. **Plan-mode handoff.** After approval in the forge, the host agent enters its plan mode automatically and validation runs automatically:
   - Claude Code: the `EnterPlanMode` tool, driven by the skill and enforced by a managed `PostToolUse` hook.
   - oh-my-pi (OMP): the hook-registered `/plan-forge` command opens a new session in plan mode, seeded with the spec and the validation result.
   - Antigravity (AGY): no change beyond the hint text.
3. **Board.** `/plan-forge-board` is available for Claude Code and OMP only. It opens the Plan-Forge window on a live Board view that lays out the approved plan's tasks by status. The user sets task priority and adds context notes. Edits reach the running agent through host-internal APIs:
   - OMP: the managed hook pushes each edit with `pi.sendMessage`.
   - Claude Code: managed `UserPromptSubmit` and `PostToolUse` hooks return `additionalContext`.

## User decisions (2026-10-19)

- Board surface: Plan-Forge window, Board view.
- Edit delivery: OMP push plus Claude hook.
- Storage: machine-local overlay. The approved manifest and its approval hash MUST stay untouched.
- OMP plan mode: open a new plan-mode session automatically, with no keypress. The mechanism the user chose, the runtime `plan.defaultOnStartup` override, cannot work. C7 records the replacement.

## Revision 2026-10-19 — OMP plan-mode mechanism

Inspection of the OMP 18.12.2 binary showed two facts:

- `plan.defaultOnStartup` is checked only at interactive start-up and by `/new`, and only for a session with zero messages. The extension `ctx.newSession()` never runs that check.
- Session reconcile, which runs inside `newSession`/`switchSession`, restores plan mode from the session's last `mode_change` entry. `SessionManager.appendModeChange("plan", …)` records that entry.

C7 therefore uses this sequence: `newSession` with `appendModeChange("plan", {})` in `setup`, then `switchSession(<new file>)` to re-run reconcile, then `pi.sendUserMessage(prompt)`.

Any missing API, any throw, or `cancelled` falls back to prefilling `/plan <prompt>`. The prompt itself forbids edits ("Plan only"), so it stays safe even if plan mode did not engage.

## Slices and file ownership

| Slice | Owner | Writes | MUST NOT touch |
|:--|:--|:--|:--|
| W1 GUI polish | worker `WebPolish` | `web/src/App.tsx`, `web/src/app.css`, `web/src/pages/*`, `web/src/components/*`, `web/src/store/syncStore.ts`, new `web/src/ui/*`, `web/scripts/preview.mjs`, new tests under `web/src/` | `web/src/main.tsx`, `web/src/protocol/*`, `web/src/board/*` |
| W2 Board web | worker `BoardWeb` | new `web/src/board/*`, `web/src/main.tsx`, new `web/scripts/preview-board.mjs`, new tests under `web/src/board/` | `App.tsx`, `app.css`, `pages/*`, `components/*` (importing is fine), `store/*`, `protocol/*` |
| G Go core | worker `GoCore` | `internal/planstate/*`, new `internal/board/*`, new `internal/app/board*.go`, `internal/relay/local_ws.go` (launch path only), `cmd/client/cmd_validate.go`, `cmd/client/cmd_board.go`, `cmd/client/cmd_session.go` (`wait` only), `cmd/client/main.go` (exit constant only) | `internal/host/*`, `integrations/*`, `cmd/client/cmd_hook.go`, `web/*`, `.github/*` |
| H Host integrations | worker `HostIntegrations` | `integrations/*`, `internal/host/installer.go`, `internal/host/hooks.go`, `internal/host/*_test.go`, `cmd/client/cmd_hook.go` (+ test), `cmd/client/cmd_install.go`, `cmd/client/cmd_uninstall.go`, `scripts/package-release.sh`, `.github/workflows/ci.yml` | `internal/planstate/*`, `internal/board/*`, `internal/app/*`, `internal/relay/*`, `web/*` |
| Wave 2 | main session | Claude dispatcher board delta (`cmd_hook.go`), docs, integration fixes | — |

Build tags: every new client-side Go file starts with `//go:build client && !server`. `internal/board` MUST NOT enter the relay server's dependency graph.

## Contract C1 — Validation (`plan-forge validate`)

`plan-forge validate [--repo PATH] [--json] [--timeout 30s]`

- The command runs `planstate.Validate(ctx, repo, opts)`, a bounded `CheckStateWith` scan whose `ScanTimeout` comes from `--timeout`.
- Exit codes: `0` valid · `6` invalid (new `exitValidationFailed`) · `1` runtime error · `2` usage error.
- `valid` is `approved && status != "DRIFTED" && status != "UNKNOWN"`. A missing manifest is invalid, with the note `no plan manifest present`.
- Every manifest-derived string passes `sanitize.Terminal`.
- `--json` prints exactly one object:

```json
{"valid":true,"approved":true,"status":"UNSTARTED","title":"Export invoices","manifest_sha256":"<hex>",
 "verified":0,"total":9,"tasks":12,"pending":["T-3/V-1"],"drift":[{"code":"target_removed","detail":"..."}],
 "notes":["structure matched; behavior unverified"],
 "spec_path":"docs/plans/.plan/synthesized_spec.md","manifest_path":"docs/plans/.plan/plan_manifest.json"}
```

The text mode prints these lines:

```text
Plan validation: VALID — UNSTARTED · 0/9 AST targets present · 12 tasks
Spec: docs/plans/.plan/synthesized_spec.md
Drift: (none)
Pending verification: T-3/V-1
```

## Contract C2 — `plan-forge wait` enrichment

On exit 0, `wait` runs `planstate.Validate` with a 20 s scan bound and adds a `validation` field holding the C1 object. On a scan timeout it prints `{"valid":false,"status":"UNKNOWN","notes":["validation timed out; run plan-forge validate"]}` with the remaining fields zero-valued. The exit code stays 0, because approval already happened.

```json
{"session_id":"<uuid>","state":"approved","artifact_paths":["/abs/.../synthesized_spec.md","/abs/.../plan_manifest.json"],"validation":{...C1...}}
```

## Contract C3 — Board overlay store (`internal/board`)

- File: `config.EnsureCache()/board/<manifest_sha256>.json`. The directory mode is 0700 and the file mode is 0600. The store writes through an atomic same-directory temp file and a rename.
- Concurrency: the board server and many `board pull` processes write the same file. The store MUST serialize read-modify-write with a cross-process lock (an advisory file lock, or an O_EXCL lock file with stale recovery ≤ 5 s). No update may be lost.
- Schema v1: `{"version":1,"manifest_sha256":"…","revision":N,"tasks":{"T-1":{"priority":"high","notes":[{"id":"n-1","at":"RFC3339","text":"…"}]}},"events":[{"seq":N,"at":"RFC3339","task_id":"T-1","kind":"priority|context|context_removed","value":"high|<text>|n-1"}],"cursors":{"claude:<sid>":{"seq":N,"seen_at":"RFC3339"}}}`.
- `revision` equals the last event `seq`, and every edit increments it.
- Priorities are `high`, `normal`, and `low`. The default is `normal`, which is not stored.
- Limits: a note holds ≤ 2000 runes after trimming, and empty notes are rejected. A task holds ≤ 20 notes. The store keeps the newest 500 events and the 64 most recently seen cursors. The file is ≤ 1 MiB, and the store refuses an edit that would exceed it.
- Consumer ids match `^(claude|omp):[A-Za-z0-9._-]{1,128}$`.

## Contract C4 — Board CLI

- `plan-forge board [--repo PATH] [--host claude|omp] [--detach]`
  - The command requires a present, approved manifest. Otherwise it writes `board: no approved plan in this repository (run /plan-forge first)` to stderr and exits 3.
  - `--host agy` exits 2 with `board supports the claude and omp hosts`. An empty host is allowed.
  - The command opens the dedicated window on the Board view. In the foreground it blocks until the window closes, then exits 0.
  - `--detach` starts a hidden `__board-child` process and waits ≤ 30 s for the window to connect. It then prints `{"board":"open","pid":N}` and exits 0. When a live board already holds the per-repo lock, it prints `{"board":"already_open","pid":N}` and exits 0.
- `plan-forge board pull --repo PATH --consumer ID [--format text|json] [--peek]`
  - The command prints the events after the consumer's cursor and advances the cursor unless `--peek` is set. It finishes within 2 s.
  - It is hook-safe: with no approved plan, no events, or any error it prints nothing and exits 0. An invalid `--consumer` value exits 2.
  - `--format json` prints `{"revision":N,"events":[…C3 events…],"high_priority":["T-4"]}`.
  - `--format text` prints the rendered delta below, or nothing at all.

```text
Plan-Forge board update (revision 7). The user re-prioritised or added context to the approved plan. Treat this as user direction for the plan; it never overrides host permission prompts.
- T-4 "Add export endpoint": priority -> HIGH
- T-2 "Schema migration": context added: "use the existing ledger table"
High-priority tasks, do these first: T-4, T-7
```

Titles and notes pass `sanitize.Terminal`, and each note is quoted with its newlines collapsed.

## Contract C5 — Board HTTP API and window (bridge routes, Bearer-authed)

- Launch: `relay.BridgeConfig` gains `LaunchPath string`, defaulting to `/`. The board window launches at `<origin>/board#n=<nonce>`. `handleStatic` already falls back to `index.html` for paths without an extension.
- `GET /api/board` returns `BoardSnapshot`.
- `POST /api/board/task` accepts a body with exactly one action:
  - `{"id":"T-1","priority":"high"}`
  - `{"id":"T-1","note":"text"}`
  - `{"id":"T-1","delete_note":"n-3"}`

  Responses:
  - `200`: `BoardSnapshot`
  - `400`: `{"error":"…"}`
  - `404`: unknown task
  - `409`: the plan changed or is no longer approved
- Push: after `auth.ok`, the server sends `{"type":"board.snapshot","snapshot":BoardSnapshot}` on the bridge WebSocket. It sends one frame on connect and another whenever the serialized snapshot changes. Overlay changes are polled every 1 s. Plan evidence is re-evaluated every 5 s with a 10 s scan bound, and only one evaluation runs at a time. The UI sends nothing on the socket.
- The window closing ends the board process, and the exit code is 0.

```json
{"repo":"agentic-plan-forge","title":"Export invoices","manifest_sha256":"<hex>","approved":true,
 "status":"IN_PROGRESS","verified":3,"total":9,"revision":7,"host":"claude","evaluated_at":"RFC3339",
 "drift":[{"code":"target_removed","detail":"…"}],
 "tasks":[{"id":"T-1","epic_id":"E-1","story_id":"S-1","workstream_id":"W-1","title":"…",
   "column":"todo|in_progress|done|drifted","blocked":false,"depends_on":["T-0"],"priority":"high|normal|low",
   "targets":[{"path":"internal/x.go","operation":"add|modify|remove|document","symbol_id":"","verified":true}],
   "verification":{"total":2,"passed":1},"acceptance_criteria":["…"],
   "human_estimate":{"likely":4,"basis":"…"},"agent_estimate":{"likely":12,"basis":"…"},
   "notes":[{"id":"n-1","at":"RFC3339","text":"…"}]}],
 "consumers":[{"id":"claude:abc","delivered_revision":7,"seen_at":"RFC3339"}]}
```

Column derivation reuses the planstate evidence functions:

| Column | Condition |
|:--|:--|
| `drifted` | A drift reason concerns one of the task's targets or verification commands. |
| `done` | Every structural target is verified and every verification command has a current successful receipt. The task has at least one target or command. |
| `in_progress` | Some evidence exists, but not all. |
| `todo` | No evidence exists. |

`blocked` is true when any `depends_on` task is not `done`.

## Contract C6 — Claude Code integration

- Managed hooks merged into `~/.claude/settings.json`, each carrying the managed marker:
  - `SessionStart`: already exists.
  - `PostToolUse` with matcher `Bash`: `"<bin>" hook claude`
  - `UserPromptSubmit`: `"<bin>" hook claude`

  Uninstall removes only these entries.
- `plan-forge hook claude` reads ≤ 1 MiB of hook JSON from stdin and finishes within 2 s. It always exits 0 and prints nothing when it has nothing to add. It resolves the repository from `$CLAUDE_PROJECT_DIR`, then from the payload `cwd`, then from the process cwd. It emits `hookEventName` equal to the incoming event.
  - On `PostToolUse` it checks three conditions:
    - `tool_name` is `Bash`.
    - `tool_input.command` runs the Plan-Forge binary's `wait` subcommand.
    - `tool_response.stdout` (or a string response) parses as the C2 JSON with `state` `approved`.

    When all three hold, it emits `{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"…"}}`. The context tells the model to call `EnterPlanMode` now, before any edit. In Plan Mode the model reads `docs/plans/.plan/synthesized_spec.md` as the blueprint. It reviews the validation summary (valid, status, verified/total, drift) and verifies the AST targets. It runs `plan-forge validate --repo <root>` if `validation.valid` is false or absent. It presents the execution checklist and calls `ExitPlanMode` for the user's approval.
  - On `PostToolUse` and `UserPromptSubmit`, it appends the C4 text delta for consumer `claude:<session_id>` after the handoff text, separated by a blank line, and advances that consumer's cursor.
- The skill `plan-forge` step 5 uses the new template variable `{{PLAN_MODE_STEP}}`, which differs per host:
  - Claude: call `EnterPlanMode`.
  - OMP: the `/plan-forge` command opens the plan-mode session itself. On the skill path the hook prefills `/plan …` and the model asks the user to press Enter.
  - AGY: the existing hint.
- New Claude-only skill `plan-forge-board` at `~/.claude/skills/plan-forge-board/SKILL.md`:
  - It runs `"{{PLAN_FORGE_BIN}}" board --repo <root> --host claude --detach` and reports that the board is open.
  - Board edits arrive automatically as hook context, and the model treats HIGH-priority tasks first.
  - It handles exit 3 by telling the user to run `/plan-forge` first.

## Contract C7 — OMP integration (`integrations/omp-hook-plan-forge.ts`)

- Keep the `session_start` banner.
- `pi.registerCommand("plan-forge", …)` replaces the markdown command `commands/plan-forge.md`. The installer stops writing that file and removes a previously installed managed copy. The handler runs these steps:
  1. Run `pi.exec(bin, ["run","--repo",cwd,"--host","omp","--mode",mode,"--detach"])`. Mode is `remote` only when `config --show --json` reports `relay_server_url` and the user answers `ctx.ui.confirm` with yes.
  2. Loop `wait --session <id> --timeout 50s` while the exit code is 75, and show progress with `ctx.ui.setStatus`.
  3. On exit 0, wait with `ctx.waitForIdle()`. Then call `ctx.newSession({parentSession, setup: sm => sm.appendModeChange("plan", {})})`.
  4. Call `ctx.switchSession(<new session file>)`. It re-runs reconcile, which enters plan mode from the `mode_change` entry.
  5. Send `pi.sendUserMessage(prompt)`. The prompt carries the spec path and the validation summary. It says "Do not edit any file. Plan only", then asks the agent to validate, verify targets, and present the checklist.
  6. If any of those APIs is missing, throws, or returns `cancelled`, fall back to `ctx.ui.setEditorText("/plan " + prompt)` and `ctx.ui.notify`.
  7. On exit 130, notify that the session closed without approval. On any other exit code, notify the stderr summary.
- `pi.registerCommand("plan-forge-board", …)` runs `board --repo cwd --host omp --detach`, notifies the result, and ensures the sync timer is running.
- Board sync runs on a managed timer (`ctx.setInterval`, 4 s). It calls `board pull --repo cwd --consumer omp:<session-id> --format text`. Non-empty output goes through `pi.sendMessage({customType:"plan-forge-board",content,display:true},{deliverAs: ctx.isIdle() ? "nextTurn" : "steer"})`. A `ctx.ui.setStatus("plan-forge", …)` summary follows.
- `tool_result` (skill path): an approved `wait` result triggers `ctx.ui.setEditorText("/plan …")` and `ctx.ui.notify`.
- Testability: the module exports `createHook(pi, deps)` with injectable `deps` (timing knobs). The default export calls `createHook(pi, defaultDeps)`. `integrations/omp-hook-plan-forge.test.ts` runs under `node --test` with a fake `pi`. CI runs it.

## Contract C8 — Web

- Board mode: `main.tsx` renders `<BoardApp/>` when `location.pathname === "/board"` or when the preview global `PLAN_FORGE_VIEW === "board"`. Otherwise it renders `<App/>`. The board reuses `protocol/bridge.ts` for `bootstrap`, `apiFetch`, the auth frame, and the WS URL, without modifying it.
- W1 MUST NOT rename or remove existing `--pf-*` tokens or `pf-*` classes; it MAY add new ones. W2 styles only through existing tokens and classes plus its own `web/src/board/board.css`.
- W1 adds `requestedHost?: string` to the TS `StatusView`; the Go side already sends `requestedHost`. Page 3 uses it for the host-aware CTA and the post-approval handoff panel. The panel shows three steps: Approved → `<Host>` Plan Mode → Validation. It also shows a close countdown from `PostApprovalClose` = 5 s.

## Wave 4 — Coordinator and Graph Nodes (user request, 2026-10-19)

Goal: a **coordinator** moves tasks across the board in real time, from what the agent harness actually does, not only from 5 s AST evidence. A **Graph** view shows the plan as task nodes with dependency edges. It also shows nodes the harness **discovered**: tasks not in the plan, and repository paths touched outside every planned target. The user accepts or dismisses each discovery.

Defaults chosen by the main session:

- Discoveries enter as `proposed`. Only the user accepts them onto the board.
- The coordinator steers the agent only when the head of the "Up next" queue changes.

### Contract C9 — Overlay store v2 (`internal/board`)

- `version` becomes 2. Loading a v1 file upgrades it in memory, and the next write persists v2.
- New top-level fields:
  - `activity`: `{"<task_id>": {"state":"active|done|blocked","by":"<consumer>","source":"file_edit|report|verify","detail":"<≤200 runes>","at":"RFC3339"}}`. The latest activity per task wins.
  - `discoveries`: `[{"id":"D-<n>","kind":"task|path","title":"≤120 runes","paths":["repo/rel/path"],"parent_task_id":"T-1|","reason":"≤500 runes","by":"<consumer>","at":"RFC3339","status":"proposed|accepted|dismissed","priority":"high|normal|low"}]`.
- Discovery caps and rules:
  - The store holds at most 200 discoveries. When full, it drops the oldest `dismissed` first, then the oldest `accepted`, and never a `proposed` one. When all 200 are `proposed`, it refuses with `ErrDiscoveryLimit`.
  - A `path` discovery for a path already present (any status) is not duplicated; only its `at` is refreshed.
  - Paths are clean, repo-relative, and slash-separated, with no `..` and no absolute paths. Paths under `.git/` or `docs/plans/.plan/` are never recorded.
- New event kinds:
  - `activity`, with value `"<state>"`
  - `discovered`, with value `"D-n"`
  - `discovery_accepted`, with value `"D-n:<priority>"`
  - `discovery_dismissed`, with value `"D-n"`

  Each event carries `by` (the consumer id or `user`).
- New API:
  - `RecordActivity(consumer, taskID string, state ActivityState, source, detail string) (Overlay, error)`
  - `AddDiscovery(consumer string, d DiscoveryInput) (Discovery, Overlay, error)`
  - `ResolveDiscovery(id string, accept bool, p Priority) (Overlay, error)`
  - `MapPaths(m contracts.PlanManifest, paths []string) (byTask map[string][]string, unplanned []string)`: a pure function that matches the clean relative paths against task targets.
  - `Coordinate(tasks []planstate.TaskEvidence, ov Overlay) Coordination`: a pure function. This puts the `planstate` dependency inside `internal/board`, which is allowed because both are client-only.
- Coordination rules (live column per task, in order):
  1. Evidence `drifted` → `drifted`.
  2. Evidence `done` → `done`.
  3. Activity `done` → `done`, with `awaiting_verification:true`.
  4. Activity `active`, or evidence `in_progress` → `in_progress`.
  5. Otherwise → `todo`.

  `blocked` = any `depends_on` task whose live column is not `done`, or activity `blocked`. Accepted task discoveries become board tasks with id `D-n`, column `todo`, and their priority, and they depend on `parent_task_id` when it is set.
- `Coordination` = `{columns: map[id]string, awaiting_verification: map[id]bool, active: []id (activity active, newest first), next_up: []id, updated_at}`. `next_up` holds up to 5 ids: not done, not drifted, not blocked, not active, ordered by priority (high → low), then manifest order, then accepted discoveries.
- `RenderDelta`:
  - Skip `activity` and `discovered` events made by the same consumer that is pulling.
  - Render `discovery_accepted` as `- D-n "<title>": accepted into the plan, priority -> HIGH`.
  - When `next_up[0]` changed since the consumer's previous delivery, append `Coordinator: next up is T-4 "<title>" (HIGH; unblocked).` Persist `last_next_up` in the consumer's cursor.
  - All other C4 rules and bounds stay.

### Contract C10 — Board CLI additions (`cmd/client/cmd_board.go`)

All of these are hook-safe: an invalid flag exits 2; any other failure prints nothing and exits 0. Each runs within 2 s and requires an approved plan, otherwise it is a silent no-op.

- `board report --repo PATH --consumer ID --task ID --state started|done|blocked [--note TEXT]`. It records activity: `started` maps to `active`, and `source` is `report`. A note is also added as context with `by` = the consumer.
- `board discover --repo PATH --consumer ID --title TEXT [--parent ID] [--path P]... [--reason TEXT]`. It records a `task` discovery and prints `{"id":"D-n"}`.
- `board activity --repo PATH --consumer ID --path P...`. This is the harness capture path:
  - It runs `MapPaths`.
  - Each mapped task with no `done`/`active` activity is recorded `active` with source `file_edit` and the path as detail.
  - Each unplanned path becomes a `path` discovery, with `parent_task_id` set to the consumer's most recent `active` task.

### Contract C11 — HTTP and snapshot additions (`internal/app/board.go`)

- Snapshot task gains:
  - `"live":{"state":"active|done|blocked|","by":"…","source":"…","detail":"…","at":"…"}`
  - `"awaiting_verification":bool`
  - `"discovered":bool`, true for accepted discoveries
  - `"column"` becomes the coordinator's live column
- Snapshot gains:
  - `"discoveries":[…C9 discovery objects…]`, with every status, newest first
  - `"coordinator":{"active":[…],"next_up":[…],"updated_at":"RFC3339"}`
- `POST /api/board/discovery` takes `{"id":"D-1","action":"accept","priority":"high|normal|low"}` or `{"id":"D-1","action":"dismiss"}`.
  - `200`: a snapshot
  - `400`: bad input
  - `404`: unknown id
  - `409`: plan changed
- The board server pushes after any store change, including activity and discoveries written by other processes. It polls the overlay every 1 s, as today, so harness activity appears within about 1 s.

### Contract C12 — Harness capture and Graph view

- **Claude:** the managed `PostToolUse` matcher becomes `Bash|Edit|Write|MultiEdit|NotebookEdit`. `hook claude` calls the C10 `activity` logic in-process with consumer `claude:<session_id>`, for `tool_input.file_path` or `tool_input.notebook_path` on edit tools.
- **OMP:** `tool_result` for the `edit`/`write` tools (and any tool whose input carries `path`/`file_path`) runs `board activity` as fire-and-forget, bounded, with no message.
- **Skills and prompts:** the Claude `plan-forge` `{{PLAN_MODE_STEP}}`, the `plan-forge-board` skill, and the OMP handoff prompt tell the agent three things:
  - Run `board report --state started|done` around each task.
  - Run `board discover` for work outside the plan.
  - Follow `Coordinator:` lines.
- **Graph view** (web, `web/src/board/graph/*`):
  - **Header:** a `Board | Graph` switch, plus a right-side coordinator panel (Active now, Up next).
  - **Rendering:** custom SVG with no new dependency.
    - Layout: a layered DAG (longest-path layering by `depends_on`, then barycenter ordering, 3 passes).
    - Task nodes are colored by live column. An active task gets a pulsing ring, and high priority gets a badge.
    - Discovered task nodes are dashed and linked to their parent.
    - Path nodes are small pills, linked to their parent task or grouped under "Unplanned".
    - Proposed, accepted, and dismissed discoveries are visually distinct; dismissed is hidden behind a toggle.
  - **Interaction:**
    - Pan by drag, zoom by wheel or buttons, and Fit.
    - Click opens the task drawer, or a discovery panel offering Accept (with a priority choice) or Dismiss.
    - New nodes fade and scale in, and column changes animate color. `prefers-reduced-motion` disables both.
  - **Scale:** the graph stays responsive with 150 nodes.

## Implementation notes

- `internal/board` owns the overlay store v2, `MapPaths`, and `Coordinate`.
- `internal/app/board.go` owns the window server, the snapshot, and the discovery route.
- Web: `web/src/board/graph/` owns the layout and the SVG renderer.
- Delivery is at-least-once: `board pull` peeks, renders, and acknowledges only after a successful write.
- The Windows cross-`go vet` cannot run on this machine (tree-sitter needs cgo); CI covers it.

## Requirements traceability

The table maps each contract to the requirement areas of the catalogue in `docs/requirements/PLAN_FORGE_REQUIREMENTS.md`. The catalogue is the authority for the requirement text, and this file is the authority for the contracts.

| Contract | Subject | Requirement areas |
|:--|:--|:--|
| C1 | Validation (`plan-forge validate`) | `REQ-RELEASE`/`REQ-HANDOFF` |
| C2 | `plan-forge wait` enrichment | `REQ-HANDOFF` |
| C3 | Board overlay store (`internal/board`) | `REQ-BOARD` |
| C4 | Board CLI | `REQ-BOARD` |
| C5 | Board HTTP API and window | `REQ-BOARD` |
| C6 | Claude Code integration | `REQ-HANDOFF`/`REQ-INSTALL` |
| C7 | OMP integration (`integrations/omp-hook-plan-forge.ts`) | `REQ-HANDOFF`/`REQ-COORD` |
| C8 | Web | `REQ-UI` |
| C9 | Overlay store v2 (`internal/board`) | `REQ-COORD` |
| C10 | Board CLI additions (`cmd/client/cmd_board.go`) | `REQ-COORD`/`REQ-BOARD` |
| C11 | HTTP and snapshot additions (`internal/app/board.go`) | `REQ-COORD`/`REQ-BOARD` |
| C12 | Harness capture and Graph view | `REQ-COORD`/`REQ-GRAPH` |

## Verify (each slice runs its own; the main session runs all)

- Web: `npm --prefix web run lint && npm --prefix web run typecheck && npm --prefix web test && npm --prefix web run build`
- Go: `go vet -tags client ./... && go test -tags client ./...` and `go test -tags server ./cmd/relay/... ./internal/relay/...`
- OMP hook: `node --test integrations/omp-hook-plan-forge.test.ts`. A bare directory argument does not work.
