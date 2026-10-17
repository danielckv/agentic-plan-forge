# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
