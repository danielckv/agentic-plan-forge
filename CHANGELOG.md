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

### Added

- `web/e2e/bridge.spec.ts` boots the built bundle behind the production Content-Security-Policy and the real handshake, and runs as the `web-e2e` CI job. The previous checks fetched the bundle without executing it, or drove it with the policy and the handshake removed.
- An automatic dark theme. The design system moves to a Cursor-style neutral palette with a light and a dark token block, selected by `prefers-color-scheme`; the editor, diagrams and every surface follow it. Nothing is hard-coded light any more.
- The window names the repository it is planning, in the sidebar and the breadcrumb. `GET /api/status` carries `repo` and `repoPath`, and `App` owns the poll so every page knows the project, not only Page 1.
- Deliberation shows that work is in flight: a run chip counting the working avatars, a spinner, shimmer placeholders where output will land, and a `Queued` state for avatars the authority has not reached yet.

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
