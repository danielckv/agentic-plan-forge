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

Before opening a pull request, run the following checks from the repository root:

```sh
npx --yes markdownlint-cli2 "**/*.md"
actionlint
npx --yes editorconfig-checker
lychee --no-progress --config lychee.toml "**/*.md"
```

Tooling installation notes:

- `markdownlint-cli2` and `editorconfig-checker` run through `npx` and require a recent Node.js installation.
- `actionlint` can be installed with `brew install actionlint` or by following the instructions at <https://github.com/rhysd/actionlint>.
- `lychee` can be installed with `brew install lychee` or by following the instructions at <https://github.com/lycheeverse/lychee>.

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
