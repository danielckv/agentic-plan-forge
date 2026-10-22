# Contributing to Agentic Plan Forge

Thank you for your interest in contributing. This document describes how to participate in the project.

## Ways to Contribute

- Report bugs by opening an issue.
- Propose features or improvements by opening an issue.
- Improve documentation.
- Submit pull requests with fixes or enhancements.

For large changes, open an issue first to discuss the proposal before writing code. This avoids duplicated effort and ensures the change fits the project's direction.

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

To look at the window without a model, two harnesses drive the built bundle through a fake planning session
(`web/scripts/lib/`, seeded with a sample brief and Go-shaped avatar frames):

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
- Maintainer review is expected before a pull request is merged. When no second reviewer is available, the maintainer self-merges.

## Licensing of Contributions

By submitting a contribution, you agree that your contributions are licensed under the Apache License, Version 2.0, the same license that covers the project (inbound = outbound), as described in Section 5 of the [Apache License, Version 2.0](LICENSE).

## Code of Conduct

All contributors are expected to follow the [Code of Conduct](CODE_OF_CONDUCT.md).

## Security

Do not open public issues for vulnerabilities. See [SECURITY.md](SECURITY.md) for how to report security issues privately.
