# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- The Plan-Forge window is no longer blank. The UI compiled its JSON Schemas with Ajv at load time, and Ajv builds validators with `new Function`, which the bridge's `default-src 'self'` policy refuses; the resulting `EvalError` aborted the entry module before React mounted. Ajv now compiles the schemas at build time (`web/scripts/gen-validators.mjs`) into plain ES modules.
- The window completes the loopback handshake. It now redeems the launch nonce at `POST /api/session`, keeps the token in `sessionStorage`, sends it as the first WebSocket frame, and attaches it to every `/api/*` call. Without this the socket URL fell back to `ws://127.0.0.1:0/` and `plan-forge run` ended in `startup_timeout`.
- The bridge offers the `plan-forge.v1` subprotocol on `GET /ws`. The window always asks for it, and a handshake answered without it fails in the browser.
- The window supervisor no longer closes the session when a browser launcher hands off and exits before the page connects.

- The window consumes the bridge's `{"type":"auth.ok"}` acknowledgement instead of failing it as an envelope. Decoding it as one reported `unsupported wire version undefined`, closed the socket, and left the session reconnecting several times a second.
- Mermaid diagrams render again in the built bundle. `gen-validators.mjs` hoisted Ajv's CommonJS `ucs2length` helper as an ES default import. Rolldown resolved that import to the module object, so every validator with a string length bound threw `is not a function`, and drafts never reached the store. The generator now unwraps the CommonJS default, and `web/src/validators.bundle.test.ts` guards the built bundle.
- `plan-forge uninstall --dry-run` no longer deletes files, and a reinstall after uninstall no longer stops at exit 5 on the stripped `settings.json`. `--force` re-merges `settings.json` instead of overwriting it.
- The managed Claude hooks refresh a stale binary path: when the Plan-Forge binary moves, the merged hook entries in `~/.claude/settings.json` are updated to the current path instead of pointing at the old one.
- The managed hooks now carry an explicit 5 s timeout, so a slow or stuck hook call can no longer stall the host.

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
  host hooks, `plan-forge board report` calls, and verification receipts — not only from the periodic AST scan. The
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

[Unreleased]: https://github.com/danielckv/agentic-plan-forge/compare/v0.1.1...HEAD
[0.1.1]: https://github.com/danielckv/agentic-plan-forge/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/danielckv/agentic-plan-forge/releases/tag/v0.1.0
