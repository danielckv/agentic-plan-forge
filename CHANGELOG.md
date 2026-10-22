# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Planning reads what the repository already says. Before deliberation, Plan Forge collects excerpts from the project's notes, specs, plans and decision records that match the requirements, plus the branch and recent commit subjects. It reads the project-memory recall index when one exists (`<git-common-dir>/agents-local/recall.db`, or the older `.agents/index/recall.db`) and the Markdown files directly otherwise. The Architect, Specialists and Critic receive it as `project_context`; the Product Manager never does. Read-only, bounded to 64 KiB, and repository-relative. See [ADR 0002](docs/adrs/0002-read-agents-recall-index.md).
- `plan-forge context --query "<topic>"` prints the same context. The `/plan-forge` skill runs it before interviewing you, so it skips questions the repository already answers.

### Changed

- The forge opens as a tab in your default browser. It launched a Chromium `--app` window in a throwaway profile, which meant a second browser with none of your extensions or sessions, and a hard requirement on Chrome, Edge, Brave or Chromium being installed. The URL now goes to the platform opener (`open`, `xdg-open`, the Windows protocol handler); `browser_path` still wins and now names a browser that takes a bare URL, so it opens a tab in the instance you already have running.
- Because the opener is not the browser — it exits milliseconds after handing the URL over — its exit is no longer treated as the window closing. Publishing it would have ended a session on the first page refresh. The UI WebSocket is now the only close signal, and nothing kills the tab: after an approval the page says it is safe to close, which is also why an explicit `browser_path` cannot be turned into a way to shut your browser down.
- Starting an agent session no longer starts anything. The Claude Code hooks are now `SessionStart` (plan status), `PreToolUse` (the stale-plan guard) and `PostToolUse` on `Bash` (the plan-mode handoff). The OMP hook reports plan status and registers `/plan-forge`. Run `plan-forge install` once after upgrading; it removes the old handlers and the `plan-forge-board` skill. See [ADR 0001](docs/adrs/0001-remove-live-task-views.md).

### Removed

- The command center (`plan-forge center`, its loopback daemon, the Live, Sprint and Setup pages, `center_port`, `center_auto_open` and `PLAN_FORGE_CENTER_PORT`).
- The plan board (`/plan-forge-board`, `plan-forge board`, the Board and Graph views, the Coordinator, discoveries and the board updates hooks injected into agent sessions).
- Swarm reporting (`plan-forge swarm` and the OMP `assistant:status` subscription).
- The secret scan on the requirements text. Confirming the context ran nine token patterns and an entropy rule over the document and refused on any hit; the entropy rule flagged repository paths, branch names and test names, and the window had no control to clear the refusal. Plan Forge no longer inspects or blocks what you write. Size, encoding and upload bounds stay. See [ADR 0003](docs/adrs/0003-no-text-scanning-in-composer.md).
- `center`, `board` and `swarm` stay for one release as retired commands. They print one line on stderr and exit 0, so a hook written by 0.3.0 never fails a session before you reinstall.

## [0.3.0] - 2026-10-20

### Added

- A brief intake in `/plan-forge`, run before the window opens. The forge deliberates on what it is given, so a vague brief buys a confident plan for the wrong work, and that cost is only visible after a full multi-avatar run. The skill now offers `skip` up front so the interview is never a toll gate, then asks one open question per message in the user's own vocabulary, and closes with a 200-300 word playback that prefixes every inference with `Guess:` and lists the agent's own choices under `My calls:`. Nothing launches until that playback has been answered once.
- `plan-forge run --brief <file>` seeds the shared PRD document, so the ingestion view opens on the agreed brief and `interpret()` normalizes it instead of a blank page. It is solo-only and says so: a remote session takes its document from the relay, and silently dropping the brief there would be worse than refusing it. `StartDetached` forwards the path rather than the text, because argv is readable by other local users. The file must be non-empty valid UTF-8 under 128 KiB; a requirements corpus still belongs in the ingestion view, which parses and normalizes documents.

### Fixed

- A planning run no longer ends seconds after the avatars start with "the Plan-Forge window was closed without an approval" and exit 130, with the window still open. Four defects chained. `Bridge.Send` destroys the UI socket when its write queue overflows, and every caller discarded the returned `ErrUIBackpressure`, so the room kept writing into a destroyed socket with nothing logged. A page reconnecting while the old socket ran its closing handshake was refused with HTTP 409, burning the supervisor's grace. The resync that repairs a reconnected window replayed up to `replayWindow` frames into a queue a fraction of that depth, destroying the socket it was catching up. A live second window is still refused; only a socket already closing is displaced, and a displaced socket no longer reports a late disconnect over its replacement.
- Stage payloads are bounded. Output always was, but the user payload was not, so a stage could hand the provider more than the model's context window and be killed mid-call with no diagnosable error — one observed run reached 807,320 tokens against a 922,000-token window. An oversized payload now reports `ErrBound` with the limit and the observed size instead of reaching the provider, and a repair drops the rejected output, whose problems the problem list already names, rather than doubling a payload that is merely large.

### Changed

- The README is a landing page. It had grown to 419 lines and carried version archaeology on the front page of a public repository: which features shipped in which release, and which failures an earlier release still had. That belongs in this file. Install, usage, the command center, the board and configuration moved into `docs/`, and the duplicated local-check block is gone because `CONTRIBUTING.md` already carries a fuller one and is named as the source of truth.
- `.agents/` is untracked. `.gitignore` had listed it all along, but an ignore rule does not untrack files already committed, so agent state stayed in the tree. The files remain in history; removing them from past commits is a separate, force-pushing operation.

## [0.2.0] - 2026-10-19

### Added

- A command center: one long-lived loopback server on `127.0.0.1:8420`, opened in a browser tab when `claude` or `omp` starts. **Live** shows every running harness with its branch, tool and touched files, and works with no approved plan at all — the board is keyed by manifest digest and refuses to exist without one, so a session in an unplanned repository previously left no trace anywhere. Where a plan does exist, Live leads with the coordinator's verdict: Active now and Begin next, the same ranking the board window computes, so the task to start is stated rather than inferred. **Sprint** renders the approved plan as epic, story and task. **Setup** explains the local surface.
- `plan-forge center serve|ensure|status|report`. `center ensure` starts the daemon when it is down and opens a tab only when none is connected, behind a cooldown so several terminals starting together still produce one tab. It is bounded and always exits 0, because a dashboard that failed to open must never be why an agent stops.
- `center_port` and `center_auto_open` settings, with `PLAN_FORGE_CENTER_PORT`. A port already in use is reported rather than worked around: a fixed port that moves breaks every bookmark.
- A Claude Code `PreToolUse` guard. When the approved plan changes under a running session, the next edit is refused once with the remedy, and the session's baseline is immediately updated so the following call proceeds — a guard that can block twice for one cause deadlocks the agent. It also refuses an edit aimed at the approved plan itself. It is silent in a repository with no approved plan, reads two files rather than scanning, and decides through the hook's JSON contract with exit 0: it never emits `allow`, which would skip your own permission prompt, never `defer`, and never exit 2.
- `plan-forge doctor` reports whether each host's managed hooks are installed and name the running binary, with a remedy per state. This matters most for oh-my-pi, where a missing hook now removes `/plan-forge` and `/plan-forge-board` rather than degrading them.
- A drawn brand mark — two bars narrowing into a struck diamond, the pre-planning gate itself — replacing the generic icon in a tinted box. It is inline SVG, so it needs no Content-Security-Policy exception and takes its colours from the theme; `docs/logo.svg` carries the same geometry for the README.

### Changed

- oh-my-pi is command-only. `plan-forge install` no longer writes `~/.omp/agent/skills/plan-forge/SKILL.md` or `plan-config/SKILL.md`: the hook registers `/plan-forge` and `/plan-forge-board` with `pi.registerCommand`, and `/plan-config` is a command file, so a command can never be listed twice. Upgrading removes the skill copies an earlier version installed, dropping their manifest rows and the emptied directories, while a copy whose bytes you changed keeps its row and stays on disk. The trade is that oh-my-pi can no longer match Plan Forge from a prose request the way a skill description allows; type `/plan-forge`.
- Claude Code's managed `SessionStart` carries a second handler that opens the command center, and both hosts now report tool calls to it alongside the board. `--purge` also removes the cache directories holding board overlays and live sessions; both are caches it already promised to delete, and the board one was missed before this change.
- `plan-forge install` and `plan-forge doctor` now warn when oh-my-pi would read a skill that shadows one of these built-in commands, instead of only when the same name appeared in two roots. The `enabled: false` marker on the Claude copies carries that guarantee alone now that oh-my-pi installs none of its own.

## [0.1.3] - 2026-10-19

### Fixed

- The installer header printed the version twice. The release pipeline injects the tag itself, so `plan-forge install` prefixed its own `v` and announced `Plan-Forge vv0.1.2`. `versionDisplay` normalizes the label to a single leading `v` and leaves a source build's `dev` alone.

## [0.1.2] - 2026-10-19

### Fixed

- The Plan-Forge window is no longer blank. The UI compiled its JSON Schemas with Ajv at load time, and Ajv builds validators with `new Function`, which the bridge's `default-src 'self'` policy refuses; the resulting `EvalError` aborted the entry module before React mounted. Ajv now compiles the schemas at build time (`web/scripts/gen-validators.mjs`) into plain ES modules.
- The window completes the loopback handshake. It now redeems the launch nonce at `POST /api/session`, keeps the token in `sessionStorage`, sends it as the first WebSocket frame, and attaches it to every `/api/*` call. Without this the socket URL fell back to `ws://127.0.0.1:0/` and `plan-forge run` ended in `startup_timeout`.
- The bridge offers the `plan-forge.v1` subprotocol on `GET /ws`. The window always asks for it, and a handshake answered without it fails in the browser.
- The window supervisor no longer closes the session when a browser launcher hands off and exits before the page connects.

- The window consumes the bridge's `{"type":"auth.ok"}` acknowledgement instead of failing it as an envelope. Decoding it as one reported `unsupported wire version undefined`, closed the socket, and left the session reconnecting several times a second.
- Mermaid diagrams render again in the built bundle. `gen-validators.mjs` hoisted Ajv's CommonJS `ucs2length` helper as an ES default import. Rolldown resolved that import to the module object, so every validator with a string length bound threw `is not a function`, and drafts never reached the store. The generator now unwraps the CommonJS default, and `web/src/validators.bundle.test.ts` guards the built bundle.
- `plan-forge uninstall --dry-run` no longer deletes files, and a reinstall after uninstall no longer stops at exit 5 on the stripped `settings.json`. `--force` re-merges `settings.json` instead of overwriting it.
- The managed Claude hooks refresh a stale binary path: when the Plan-Forge binary moves, the merged hook entries in `~/.claude/settings.json` are updated to the current path instead of pointing at the old one.
- The managed Claude hooks now carry an explicit 5 s timeout (the Antigravity `SessionStart` entry carries 2 s), so a slow or stuck hook call can no longer stall the host.

### Added

- `web/e2e/bridge.spec.ts` boots the built bundle behind the production Content-Security-Policy and the real handshake, and runs as the `web-e2e` CI job. The previous checks fetched the bundle without executing it, or drove it with the policy and the handshake removed.
- An automatic dark theme. The design system moves to a Cursor-style neutral palette with a light and a dark token block, selected by `prefers-color-scheme`; the editor, diagrams and every surface follow it. Nothing is hard-coded light any more.
- The window names the repository it is planning, in the sidebar and the breadcrumb. `GET /api/status` carries `repo` and `repoPath`, and `App` owns the poll so every page knows the project, not only Page 1.
- Deliberation shows that work is in flight: a run chip counting the working avatars, a spinner, shimmer placeholders where output will land, and a `Queued` state for avatars the authority has not reached yet.
- `plan-forge validate [--json]` checks the approved plan against the repository: approval on this machine, drift, AST targets present, and pending verification. It exits 0 when the plan is valid and 6 when it is invalid. `plan-forge wait` now includes the same result as `validation`.
- Plan Mode handoff after approval:
  - Claude Code calls `EnterPlanMode`, enforced by a managed `PostToolUse` hook that injects the spec path and the validation summary.
  - OMP's hook-registered `/plan-forge` opens a new plan-mode session seeded with the spec. It falls back to a prefilled `/plan …` when the session API is unavailable.
  - The window shows the handoff steps and a close countdown.
- `/plan-forge-board` for Claude Code and OMP, backed by `plan-forge board`:
  - A live board in the Plan-Forge window lays out the approved plan's tasks by evidence: To do, In progress, Done, Drifted.
  - Each task takes a priority and context notes.
  - Edits are stored in a machine-local overlay and never in the approved manifest.
  - Edits reach the agent through OMP `pi.sendMessage` and through Claude `UserPromptSubmit`/`PostToolUse` hook context. `plan-forge board pull` serves both.
- A coordinator moves tasks across the board in real time from agent-harness activity — file edits captured by the
  host hooks and `plan-forge board report` calls — not only from the periodic AST scan. Verification receipts count
  as plan evidence through that periodic evidence scan: `plan-forge verify` records nothing on the board. The
  snapshot exposes live columns and an `awaiting_verification` flag, an Up-next queue feeds a `Coordinator:` line
  delivered to the agent when its head changes, and the board window shows a coordinator panel with Active now and
  Up next.
- Discovered tasks and paths: work the agent did that no planned task covers is recorded as a proposed discovery.
  Each discovery is accepted (with a priority) or dismissed by the user; nothing enters the plan automatically.
- A Graph view on the board (`Board | Graph` switch) renders the plan as a node graph with dependency edges, nodes
  colored by live state and discovered nodes dashed, with pan, zoom and fit; it stays responsive at 150 nodes.
- Board CLI additions: `plan-forge board report` (task state from the agent), `board discover` (record out-of-plan
  work), and `board activity` (harness capture of edited paths). `board pull` now acknowledges delivery only after a
  successful write, so updates are delivered at-least-once.
- Window UX:
  - phase stepper, collapsible sidebar (`[`), and a System/Light/Dark theme switch
  - toasts for transient notices, a keyboard shortcut sheet (`?`), and route and accordion motion that honours `prefers-reduced-motion`
  - on Page 3: scroll-spy contents, a filterable task matrix with expand/collapse all, copy buttons on verification commands, and full-screen diagrams with a source view
  - a layout that holds from 1024×700 up
- The requirements catalogue and the master-plan audit, added under `docs/requirements/`.

### Changed

- Synthesis is blocked while any avatar is still working, and the footer names what it is waiting for. It was enabled mid-cycle, when the drafts it reads did not exist yet.
- The host acknowledgement on Page 1 starts checked, so confirming the context is one click. Unchecking it still blocks the launch.
- A dropped socket reads as `Reconnecting`, not `Disconnected`: the authority going away is routine. Only a refused handshake, which no retry can fix, reads as `Offline`.
- The product name is written `Plan Forge` in the UI.

## [0.1.1] - 2026-10-17

### Added

- `scripts/deploy-relay-cloudrun.sh` to deploy `plan-forge-relay` to Cloud Run: Cloud Build image built from the local tree, a git ref, or an existing image, digest-pinned deploy, Secret Manager token, and a dedicated service account; see `docs/deploy/relay-cloud-run.md`.
- `plan-forge-relay` honors the `PORT` environment variable for platforms such as Cloud Run.

### Changed

- Approved plan artifacts and the AST cache are written to `docs/plans/.plan/` instead of `.plan/`. The relay accepts both paths, so 0.1.0 clients keep syncing.

### Fixed

- Add the `run`, `wait`, `join`, and `invite` commands that the `/plan-forge` skill calls; 0.1.0 shipped without them, so the installed skill could not start a session.
- `plan-forge hook check-state` accepts the subcommand before or after its flags, as Claude Code and Oh My Pi invoke it.
- The Oh My Pi hook reads the banner from exec result objects.
- Follower sessions answer status, secrets, and approval requests without an owner executor.
- The installer preserves dotfiles symlinks inside the home directory.

## [0.1.0] - 2026-10-17

First public release.

### Added

- `plan-forge` client (CGO) with an embedded three-page web UI: ingestion, deliberation, and synthesis, styled after Jira's layout and theme colors.
- `plan-forge-relay` (CGO-free, same repository) for real-time collaboration, using a central operational-transform authority for the shared PRD document.
- Document ingestion for `.docx`, `.pdf`, and Markdown, with size and zip-bomb bounds and optional OCR for scanned PDFs.
- Tree-sitter codebase grounding for Go, JavaScript, TypeScript, TSX, Python, and Rust, plus documentation drift detection.
- Staged multi-avatar deliberation over Claude Code and Oh My Pi hosts with tool use disabled for generation.
- Spec compiler producing `.plan/synthesized_spec.md` and `.plan/plan_manifest.json`, with Mermaid diagrams and dual-baseline Gantt estimates (human workday versus coding agent).
- One-command installers (`install.sh`, `install.ps1`) that install the binary and register the `/plan-forge` and `/plan-config` skills in every detected agent.
- `plan-forge hook`, `doctor`, `models`, `verify`, `summary`, `open`, `wait`, `join`, and `invite` commands.
- Release workflow that builds client archives for macOS, Linux, and Windows, relay archives for Linux, and a multi-architecture relay container image.
- Repository scaffolding:
  - Apache License 2.0.
  - Community health files: `CODE_OF_CONDUCT.md`, `CONTRIBUTING.md`, `SECURITY.md`, `SUPPORT.md`.
  - Issue and pull request templates.
  - CI covering Go, web, workflow linting, Markdown linting, EditorConfig checks, shell linting, and link checking.
  - Dependabot configuration.

### Known limitations

- Antigravity generation fails closed because its CLI exposes no verified isolated-settings selector. Antigravity remains a skill host, and `/plan-forge` falls back to Claude or Oh My Pi.

[Unreleased]: https://github.com/danielckv/agentic-plan-forge/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/danielckv/agentic-plan-forge/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/danielckv/agentic-plan-forge/compare/v0.1.3...v0.2.0
[0.1.3]: https://github.com/danielckv/agentic-plan-forge/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/danielckv/agentic-plan-forge/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/danielckv/agentic-plan-forge/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/danielckv/agentic-plan-forge/releases/tag/v0.1.0
