# Contributing to Agentic Plan Forge

This guide is for contributors to Agentic Plan Forge. It covers repository architecture, development workflows, local validation commands, and live integration testing.

Thank you for your interest in contributing. This document describes how to participate in the project.

## Ways to Contribute

- Report bugs by opening an issue.
- Propose features or improvements by opening an issue.
- Improve documentation.
- Submit pull requests with fixes or enhancements.

For large changes, open an issue first to discuss the proposal before writing code. This avoids duplicated effort and ensures the change fits the project's direction.

## Repository Layout

```text
cmd/client, cmd/relay       entrypoints, separated by build tags
internal/contracts          shared data shapes, schemas and error codes
internal/ingestion          document parsing, PRD normalization and draft epics
internal/ast                Tree-sitter grounding and documentation drift
internal/gather             project context: recall index, notes and specs, Git state
internal/impact             what the requirements touch, for the first page
internal/host               agent CLI adapters and skill installation
internal/avatars            staged deliberation and prompt contracts
internal/synthesizer        specification, manifest and effort schedules
internal/session, collab    authoritative state and shared-document editing
internal/relay              protocol, hub and local bridge
web                         embedded React UI
integrations                skill, command and hook templates
```

The `client` and `server` build tags are mutually exclusive. Most of `internal/` is client-tagged, so a plain `go build ./...` silently skips it; pass `-tags client`, and build `web/dist` first because the client embeds it.

## Development Workflow

1. Fork the repository and clone your fork.
2. Create a branch from `main` using one of these prefixes: `feat/<slug>` for features, `fix/<slug>` for bug fixes, or `docs/<slug>` for documentation changes.
3. Make your changes. Keep each pull request focused on a single change.
4. Commit using [Conventional Commits](https://www.conventionalcommits.org/), for example `feat:`, `fix:`, `docs:`, `chore:`, `ci:`, `refactor:`, or `test:`.
5. Update the `Unreleased` section of [CHANGELOG.md](CHANGELOG.md) with a brief description of your change.
6. Run the local checks below and make sure they pass.
7. Open a pull request against `main`.

## Local Checks

Prerequisites: Go 1.27.2, Node.js 24 (see `web/.nvmrc`), a C toolchain for CGO client builds, and `shellcheck`.

Before opening a pull request, run the following checks from the repository root:

```sh
# Go: shared packages and the relay (server build tag)
go test ./...
go test -tags server ./cmd/relay/... ./internal/relay/...

# Web UI; the client embeds web/dist, so build it before client-tag Go builds
npm --prefix web ci
npm --prefix web run typecheck
npm --prefix web test
npm --prefix web run build

# End to end: boots the built bundle behind the bridge CSP and handshake
npm --prefix web exec -- playwright install --with-deps chromium
npm --prefix web run test:e2e

# Go: client (client build tag, CGO)
go vet -tags client ./...
go test -tags client ./...

# Scripts, Markdown, EditorConfig, workflows and links
shellcheck scripts/*.sh tests/deploy/*.sh
bash tests/deploy/cloudrun_test.sh
npx --yes markdownlint-cli2 "**/*.md"
actionlint
npx --yes editorconfig-checker
lychee --no-progress --config lychee.toml "**/*.md"
```

The `client` and `server` build tags are mutually exclusive: never pass both.

## Live tests

A few tests call a real model. They are skipped unless you opt in, because each one spends real money, and they are never run in CI. Everything above passes without them.

Setup is whatever you already have: a signed-in host agent. No API key and no extra configuration.

```sh
# 1. One host, signed in. Check what Plan Forge can see:
plan-forge doctor

# 2. Pin the host and model explicitly. Both variables matter:
#    LIVE_HOST pins every role, because roles otherwise rotate across hosts
#    and a stale host fails a stage that has nothing to do with the test.
export PLAN_FORGE_LIVE_HOST=claude
export PLAN_FORGE_LIVE_MODEL=opus
```

| Test | Switch | What it costs | What it tells you |
| :--- | :--- | :--- | :--- |
| `TestLiveDeliberationEndToEnd` | `PLAN_FORGE_LIVE_DELIBERATION=1` | a whole run, 20–40 min | every avatar's calls, payload size, slowest call, host placement, and the run's wall clock against its total call time |
| `TestLiveImpactEndToEnd` | `PLAN_FORGE_LIVE_IMPACT=1` | a few calls | that the impact map names only files that exist, and that a second pass costs nothing |
| `TestLiveMeasureEvidenceBudget` | `PLAN_FORGE_LIVE_IMPACT=1` | one call | how long a given evidence size takes, which is how the input bounds are chosen |

```sh
# The whole gate, end to end, with the per-role cost table:
PLAN_FORGE_LIVE_DELIBERATION=1 go test -tags client \
    -run TestLiveDeliberation -timeout 60m -v ./internal/app/

# One cycle runs by default; PLAN_FORGE_LIVE_CYCLES raises it.
```

Use the deliberation test before and after any change that claims to make a run faster or an input smaller. The numbers it prints are what the decision records cite; a claim about speed without one of these tables behind it does not belong in an ADR.

To look at the window without a model, two harnesses drive the built bundle through a fake planning session (`web/scripts/lib/`, seeded with a sample brief and Go-shaped avatar frames):

```sh
npm --prefix web run build
node web/scripts/preview.mjs   # screenshots to /tmp/plan-forge-preview and behavior checks
node web/scripts/promo.mjs     # the README screenshots in docs/images
```

Re-run `promo.mjs` when a change alters what the README pictures show.

Tooling installation notes:

- `markdownlint-cli2` and `editorconfig-checker` run through `npx` and require a recent Node.js installation.
- `actionlint` can be installed with `brew install actionlint` or by following the instructions at <https://github.com/rhysd/actionlint>.
- `lychee` can be installed with `brew install lychee` or by following the instructions at <https://github.com/lycheeverse/lychee>.
- `shellcheck` can be installed with `brew install shellcheck` or your platform's package manager.

## Pull Request Expectations

- Keep pull requests small and focused; unrelated changes belong in separate pull requests.
- Fill in the pull request template completely.
- Ensure all CI checks pass. Reviewers may ask for changes before merging.
- CI waits 15 minutes before it starts. Pushing again inside that window cancels the waiting run and restarts the clock, so a burst of fixes costs one CI run instead of one per push. Run the checks above locally first; do not wait on CI to find what `go test` would have told you.
- Releases are cut by dispatching the Release workflow by hand. Dispatch it against a `v*.*.*` tag to publish the archives and push the relay image; dispatch it against a branch to build and package every platform without releasing, which is how to check a cross-compilation change.
- Maintainer review is expected before a pull request is merged. When no second reviewer is available, the maintainer self-merges.

## Licensing of Contributions

By submitting a contribution, you agree that your contributions are licensed under the Apache License, Version 2.0, the same license that covers the project (inbound = outbound), as described in Section 5 of the [Apache License, Version 2.0](LICENSE).

## Code of Conduct

All contributors are expected to follow the [Code of Conduct](CODE_OF_CONDUCT.md).

## Security

Do not open public issues for vulnerabilities. See [SECURITY.md](SECURITY.md) for how to report security issues privately.
