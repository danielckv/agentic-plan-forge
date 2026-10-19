# Command center, live harness telemetry, and the command-only host surface

Status: implemented (2026-10-19), with all gates green. Owner: main session. This is the cross-slice contract every worker codes against.
Contracts in this file are fixed. A worker who needs to change a contract stops and reports the request back to the main session.
Contracts `C1`–`C12` live in [GUI_BOARD_HANDOFF_PLAN.md](GUI_BOARD_HANDOFF_PLAN.md); numbering continues here at `C13`.

## Goals

1. **Work before a plan exists.** Today the board refuses without an approved manifest (`ErrNoApprovedPlan`, exit 3) and its
  overlay is keyed by manifest digest, so an agent working in an unplanned repository is invisible. Record those sessions.
2. **One command center.** A long-lived local surface that shows every live harness session, the sprint hierarchy, and
  configuration, reachable from a browser tab rather than a per-session window.
3. **The coordinator decides what starts next.** Live is not a passive feed: it states the task to begin now and re-ranks
  as the harness reports new insight.
4. **One slash-command surface per host.** oh-my-pi through built-in commands, Claude Code and Antigravity through skills.

## User decisions (2026-10-19)

- Adapt into the existing Go binary. No second runtime, no SQLite, no separate web application.
- **No external trackers.** `REQ-BOARD-004` (host-internal delivery, no external dependency) and `REQ-SEC-004` (no egress)
  stand. The epic/story/task hierarchy is rendered locally; nothing is pushed to Jira, Confluence, Linear or Notion.
- **A persistent fixed-port browser tab**, chosen over the existing ephemeral-port Chromium `--app` window. The
  trade-off and its residual risks are recorded in C15; every mitigation that still applies is kept.
- oh-my-pi is **command-only**; Claude Code and Antigravity keep skills.
- The GUI must read as a finished product, not a debug panel (C17).

## Slices and file ownership

| Slice | Owner | Writes | MUST NOT touch |
|:--|:--|:--|:--|
| S1 Live store | `LiveStore` | new `internal/livestore/*` | `internal/host/*`, `integrations/*`, `web/*`, `internal/board/*` |
| S2 Daemon | `Center` | new `internal/center/*`, new `internal/applock/*`, new `cmd/client/cmd_center.go`, `internal/config/*` | `internal/host/*`, `integrations/*`, `web/src/{App.tsx,store,protocol,board}` |
| S3 Web | `CenterWeb` | new `web/src/center/*`, `web/src/main.tsx`, new `web/scripts/preview-center.mjs`, new `web/e2e/center.spec.ts` | `web/src/{App.tsx,app.css,pages,components,store,protocol,board}` |
| S4 Host surface | `HostIntegrations` | `internal/host/*`, `integrations/*`, `cmd/client/{cmd_hook*,cmd_install,cmd_uninstall,cmd_doctor,cmd_board}.go` | `internal/{center,livestore,applock,app,relay}/*`, `web/*` |

Every new client-side Go file starts with `//go:build client && !server`. No new package may enter the relay's dependency
closure; `.github/workflows/ci.yml` asserts it and its grep must be extended with the new package names.

## Contract C13 — Live store (`internal/livestore`)

Keyed by repository, not by manifest digest, so an unplanned repository has somewhere to write.

- File `<cache>/live/<sha256(realpath(repo))>.json`, 0600 in a 0700 directory; lock `<...>.lock`, a separate file so the
  atomic rename never replaces it. Cross-process advisory `flock` around every read-modify-write, then temp → fsync →
  rename, copying `internal/board`'s discipline exactly.
- Schema v1: `{version, repo_key, repo_name, seq, sessions{}, events[]}`. `Session{id, harness, developer, branch, mode,
  task_id, last_tool, last_command, started_at, last_heartbeat, seq}`; `mode ∈ PLANNED | UNPLANNED`.
  `LiveEvent{seq, at, session_id, kind, detail}`; `kind ∈ session_start | tool | command | untracked | idle | session_end`.
- `repo_path` is deliberately absent: an absolute machine path never reaches the wire, as the board already ensures.
- Session ids reuse `board.ValidConsumer` (`^(claude|omp|agy):[A-Za-z0-9._-]{1,128}$`). No second validator.
- Bounds as named constants, mirroring `internal/board`: 64 sessions, 500 events, 50 dirty paths, 1 MiB file ceiling,
  300-rune commands, 200-rune event detail. Eviction: stale sessions first, then oldest heartbeat, ties broken by greater
  id; then trim events; `ErrTooLarge` only at the floor.
- A session with no heartbeat for 90s renders `idle`; one with none for 24h is evicted on the next write.
- Retention is a privacy property, not hygiene: entries older than 7 days are pruned on every write.
- Every string is sanitized **on write** (`sanitize.Terminal` + rune bound), so no reader has to trust the file.

## Contract C14 — Daemon lifecycle (`plan-forge center`)

- `plan-forge center serve` runs in the foreground; `center ensure [--repo PATH] [--open] [--json]` is what hooks call;
  `center status [--json]` is a read-only probe. Hidden `__center-child` is the detached worker.
- The name is `center`, not `ui`: `serve-ui` is the hidden release smoke test (`cmd/client/cmd_serveui.go:16`) and `open`
  already means "print the approved spec" (`cmd/client/cmd_open.go:16`).
- Single instance via `internal/applock`, the pid-lock algorithm proven by the board (`internal/app/board.go:261-408`): publish by `os.Link`
  from a temp file, stale takeover serialized by a separate `.guard` flock. Extract it to `internal/applock` with the
  board's tests ported; refactoring the board to delegate is optional and last.
  Two corrections were made while extracting it, and the board keeps the originals until it
  delegates: the staging file is named per call rather than per process, so two concurrent
  acquisitions in one process cannot delete each other's temp file, and an acquisition that finds a
  lock naming its own process reports that it holds it rather than treating it as stale.
- `center ensure` prints `{"center":"open"|"already_open","pid":N,"clients":M}`, mirroring `board --detach`.
- Port precedence: `--port` → `PLAN_FORGE_UI_PORT` → config `ui_port` → `8420`. **Never silently fall back to another
  port**: a fixed port that moves breaks every bookmark. Port in use with no live lock holder exits 1 with the remedy.
- `center ensure` must work with no plan and no git repository. It never imports `planstate`.

## Contract C15 — Daemon HTTP, auth and streaming

Carried over verbatim from the bridge (`internal/relay/local_ws.go`): loopback-only bind, exact `Host` match
(DNS-rebinding), exact `Origin` match on every non-`GET` and every stream, full `BridgeCSP`, `Cache-Control: no-store`,
`COOP`, `Referrer-Policy: no-referrer`, constant-time token compare.

- **Streaming is SSE framed over `fetch()` + `ReadableStream`, not `EventSource` and not WebSocket.** `EventSource`
  cannot set `Authorization: Bearer`, and the alternatives — a token in the query string or a cookie — are both
  disqualified: the repo already forbids credentials in a URL, and a loopback cookie is port-blind. WebSocket is
  disqualified because the bridge allows exactly one socket by construction (`Bridge.ui` is a single field; a second
  connection gets 409), and the command center must serve several tabs.
- **Three credentials, strictly scoped.** A daemon secret at `<home>/center/daemon.token` (0600) accepted only on
  `/api/ingest`, `/api/center/ticket` and `/api/center/shutdown`; a 10-second single-use launch ticket redeemed from the
  URL fragment by `POST /api/session`; a session token held in `sessionStorage`.
- **A typed `http://127.0.0.1:8420` does not mint a token.** It renders a parked screen naming the command to run. A
  session token mintable without the 0600 token file is a token any web page on a guessable port could mint.
- `POST /api/ingest` requires **`Origin` absent**. A browser always sends it on POST, so its absence proves a CLI caller.
- Every data-returning `GET` requires the Bearer header; that is what forces a CORS preflight and makes the fixed port
  safe against a drive-by `fetch`. No bare data `GET` may be added.
- Backpressure: SSE has no close codes, so overflow writes a final `event: overflow` frame and closes; the client
  refetches the snapshot and resubscribes. 16 clients maximum, then 429.
- **Residual risks, recorded because the fixed port creates them.** A same-machine process can bind 8420 while the
  daemon is down and serve its own page on this origin. Any process running as the user can read the daemon token: the
  model is same-user ⇒ full trust, and these controls defend against the network and other browser origins, not against
  local code running as you.

## Contract C16 — Telemetry ingestion

- Hooks POST to the daemon with a 50 ms dial and a 150 ms total budget, through a transport whose dialer refuses any
  address but the pinned loopback one. Daemon down ⇒ `ECONNREFUSED` in about a millisecond, then a detached autostart
  behind a 10-second cooldown stamp. Always exit 0; never block the agent.
- Secrets are redacted **at the edge**, in the client, before the record crosses a process boundary.
- `board activity` falls back to the live store when `openApprovedBoard` returns `errNoApprovedBoard`, so the existing
  oh-my-pi hook needs no change for the file case. It gains `--tool` and `--command` so a tool call with no path is
  capturable.
- **`git status` and `git diff` stay banned.** `internal/ast/gitcmd.go` excludes them deliberately, and the OID
  comparison the scanner uses is a full-worktree walk that cannot fit a 2-second hook. The hook captures tool
  executions, commands, and one untracked-file listing per session via a narrowly allowlisted
  `git ls-files -z --others --exclude-standard`. Tracked-file drift is the daemon's job, on its own schedule.

## Contract C17 — Web command center (`web/src/center/`)

- A third entry app selected in `web/src/main.tsx` by path prefix, beside `BoardApp` and `App`. No `react-router`: the
  tree has deliberately avoided one and the server's catch-all already serves `index.html` for extensionless paths, so
  `/live`, `/sprint` and `/setup` need no server-side routing. `App`'s router stays phase-locked and untouched.
- `/sprint` renders the existing `JiraMatrix` unchanged through an adapter; every field `SubtaskRow` needs is already on
  the board task. An unknown epic groups under a synthetic unplanned epic, which is where discoveries belong.
- `/setup` covers local hooks, the workday baseline and host/model slots — the keys the terminal wizard already prompts
  for. No tracker credentials, no connection tests, no egress.
- **Presentation bar.** The command center must read as one deliberately designed product: a single layout grid, one
  type scale, aligned columns, colour used only to carry meaning (state, severity), generous whitespace, no raw JSON or
  stack traces on screen, designed empty and loading states, and full parity in light and dark. It reuses the existing
  `web/src/ui/` primitives and the `app.css` tokens so it is visually continuous with the forge and board windows.

## Contract C18 — Coordinator-driven "Up next" in live

`internal/board/coordinator.go` already computes `Active`, `NextUp` (at most five, priority then manifest order),
`Blocked` and `AwaitingVerification` deterministically and without reading a clock. This contract promotes that output
to the primary element of `/live` rather than adding a second ranking.

- "Active now" and "Up next" lead the page; the task to begin next is always explicit and never requires the reader to
  infer it from a list.
- When the harness reports fresh insight — a `board discover` call creating a proposed task, or new evidence landing —
  accepting it re-runs `Coordinate()` so `NextUp` re-ranks immediately. Accepted discoveries already enter the ranking
  through `Coordination.ExtraTasks`.
- **Nothing enters the plan automatically.** A discovery stays proposed until the user accepts or dismisses it, and the
  approved manifest and its approval hash stay untouched. Ranking is advisory; it never edits the plan.
- In an unplanned repository there is no manifest to rank, so live shows sessions and discoveries only, and offers the
  forge instead of a ranking.

## Contract C19 — Claude `PreToolUse` spec-drift guard

Verified against the current Claude Code hooks reference, because the contract has changed and a wrong choice here is
silently harmful:

- Output is `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"…"}}`
  on stdout with **exit 0**. Top-level `decision`/`reason` are deprecated for this event.
- **Never emit `allow`**: it *skips the user's permission prompt*, which would silently weaken their own settings. The
  allow path prints nothing and exits 0. **Never emit `defer`** (honoured only in non-interactive single-call turns).
  **Never exit 2**; every hook entry point here contracts "always exit 0".
- Matcher `Edit|Write|MultiEdit|NotebookEdit`. `Bash` is excluded deliberately: blocking it would block the agent's own
  `plan-forge validate`, the very remedy the deny reason names. Letters and `|` only, so the matcher is an exact-name
  list rather than an unanchored regex.
- Unplanned session ⇒ no-op, printing nothing, with no AST scan.
- **One block per drift event, never a loop.** The first `PreToolUse` of a session records a baseline; a changed digest
  denies exactly once and immediately rewrites the baseline so the next call proceeds. A guard that can block twice for
  one cause deadlocks the agent.
- It is a separate `hook claude-guard` subcommand, not a branch of `hook claude`: the latter compare-and-advances the
  board cursor, and firing that on the pre side would double-consume board events.

## Contract C20 — Session-start auto-launch

- A **second managed `SessionStart` handler** running `center ensure --repo … --open`, not a flag on `hook check-state`.
  `check-state` returns early when a plan is unchanged or absent, and auto-launch must fire in both cases; a separate
  handler makes that moot by construction and is independently timeout-bounded by the host.
- oh-my-pi fires the same command from `session_start`, not awaited, so a slow status check cannot delay the tab.
- A tab opens only when the daemon reports zero connected clients, and a check-and-set cooldown under the guard lock
  stops three terminals starting at once from opening three tabs.
- Bounded at 1500 ms total, always exit 0. Until the daemon ships, the spawn fails and `center ensure` exits 0 silently.

## Contract C21 — Doctor hook verification

Dropping oh-my-pi's skills removes the fallback that made `/plan-forge` work when the hook failed to load, so a missing
oh-my-pi hook now means the command does not exist at all. `doctor` must say so.

- Per host: is the managed hook present, and does it point at **this** binary? Claude parses `settings.json` for the
  marker across every event in `claudeHookEvents`; oh-my-pi re-renders the hook template and compares bytes; Antigravity
  checks `hooks.json`. States: `installed | stale | missing | modified | unreadable`, each with a remedy.
- The host set is the union of detected hosts and hosts holding manifest rows, so a host whose hook is installed but
  whose binary is absent is never skipped.
- `mergeAGYHook` returns early on a marker match and so never refreshes a stale binary path. Fix it to self-heal through
  `replaceManagedHandlers`, as the Claude path already does, rather than printing a second kind of remedy.
- `doctor` stays diagnostic and keeps exiting 0.

## Requirements traceability

The catalogue in [../requirements/PLAN_FORGE_REQUIREMENTS.md](../requirements/PLAN_FORGE_REQUIREMENTS.md) is the
authority for requirement text; this file is the authority for the contracts.

| Contract | Subject | Requirement areas |
|:--|:--|:--|
| C13 | Live store | `REQ-LIVE` |
| C14 | Daemon lifecycle | `REQ-LIVE`/`REQ-UI` |
| C15 | Daemon HTTP, auth, streaming | `REQ-SEC`/`REQ-PERF` |
| C16 | Telemetry ingestion | `REQ-LIVE`/`REQ-COORD`/`REQ-SEC` |
| C17 | Web command center | `REQ-UI` |
| C18 | Coordinator-driven Up next | `REQ-COORD` |
| C19 | PreToolUse spec-drift guard | `REQ-HANDOFF`/`REQ-SEC` |
| C20 | Session-start auto-launch | `REQ-UI`/`REQ-INSTALL` |
| C21 | Doctor hook verification | `REQ-INSTALL` |

## What is not finished

Two things are deliberately short of the contract rather than silently absent, and both are open questions
in the catalogue:

- **C17, `/sprint` titles.** `PlanManifest` carries `TaskDAG` but no epic or story titles: those survive approval only
  as headings in `synthesized_spec.md`. The view therefore groups by identifier. Parsing the headings back out, or
  persisting them in the manifest, is undecided.
- **C16, the Claude-side budget.** `CenterEnsureBound` and `centerReportBound` are declared and the oh-my-pi side is
  covered by tests that prove the launch is never awaited, but no test asserts the Claude-side wall clock the way
  `REQ-PERF-001` does for `hook claude`.

Delegating the board's own pid lock to `internal/applock` also remains optional and last, as recorded in C14.

## Verify (each slice runs its own; the main session runs all)

- Go: `go vet -tags client ./... && go test -tags client ./...`, `go test ./...`, and
  `go test -tags server ./cmd/relay/... ./internal/relay/...`
- Relay closure: `! go list -deps -tags server ./cmd/relay | grep -Eq "internal/(…|livestore|center|applock)|/web$|tree-sitter"`
- Cross-compile the daemon packages without cgo: `CGO_ENABLED=0 GOOS=windows go vet -tags client ./internal/livestore/...`
- Web: `npm --prefix web run typecheck && npm --prefix web test && npm --prefix web run build && npm --prefix web run test:e2e`
- oh-my-pi hook: `node --test integrations/omp-hook-plan-forge.test.ts`. A bare directory argument does not work.
- The e2e case asserts what the single-socket bridge structurally cannot do: a record posted by a harness reaches **two**
  open tabs, under the production CSP, with no external network request.
