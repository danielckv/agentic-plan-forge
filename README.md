# <img src="docs/logo.svg" alt="" width="30" height="30" align="middle"> Agentic Plan Forge

[![CI](https://github.com/danielckv/agentic-plan-forge/actions/workflows/ci.yml/badge.svg)](https://github.com/danielckv/agentic-plan-forge/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/danielckv/agentic-plan-forge?sort=semver)](https://github.com/danielckv/agentic-plan-forge/releases/latest)

**A planning gate for coding agents.** Catch the wrong architecture before a file changes.

Ask a coding agent to build a feature and it returns a confident plan in seconds. The plan is often wrong: it trusts
stale documentation, misses decisions the team already made, and lets a technical detail decide a product question.
Plan Forge stops the loop before that. It reads your actual codebase, argues the approach out between several model
avatars, and hands you one specification to sign off.

![A brief submitted, the agents working, a fusion, and the question drawer answering one decision](docs/images/demo.gif)

## Quickstart

```sh
curl -fsSL https://raw.githubusercontent.com/danielckv/agentic-plan-forge/main/scripts/install.sh | sh
```

Restart your agent, then type `/plan-forge`. A tab opens; write what you want built there.

You need Git, a browser, and a model — either a coding agent you are already signed in to (Claude Code,
Antigravity) or an API key, which Setup asks for on the first run. Windows, version pinning and uninstall are in
[docs/install.md](docs/install.md).

## How it works

Grounding runs first and makes no model call: the repository is parsed into symbols, signatures and routes, the
documentation is checked against that, and the team's decision records, specs and recent commits are matched against
your requirements. Nothing is invented before that finishes.

Then the avatars draft — a Product Manager, a Technical Architect, one Domain Specialist per workstream, and a
Red-Team Critic — in cycles, until the critic has nothing CRITICAL or HIGH left. Each avatar is a fresh, tool-free
model call: no repository access, no skills, no inherited conversation. Everything it sees is assembled by Plan Forge
and passed in as data, and they deliberately see different things — the Product Manager never sees code, so product
decisions stay product decisions. A Code Reviewer and a Technical Leader then read the assembled plan, one repair pass
answers the blocking findings, and a Lead Synthesizer compiles the specification.

A run always ends with a plan. If blocking findings are still open when the cycle budget runs out, you choose whether
to take the plan with them recorded on it or pause and revise, and the specification opens with a readiness verdict
naming every one.

After approval the agent can implement the plan in an isolated worktree — never your repository — with a diff tab and
a container preview.

## Documentation

| Guide | What it covers |
| :--- | :--- |
| [docs/install.md](docs/install.md) | Every agent, Windows, updating, uninstall |
| [docs/usage.md](docs/usage.md) | The window and the terminal, page by page |
| [docs/configuration.md](docs/configuration.md) | Hosts, models, timeouts, logs |
| [docs/decisions.md](docs/decisions.md) | Why each boundary sits where it does |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Build, test and pull request workflow |

## Privacy and security

- Requirements, repository contents and absolute paths stay on your machine. No telemetry, no account. The window
  talks to a loopback bridge in the same process, which checks the request origin and a session token.
- An API key is written to `~/.plan-forge/credentials.json` with owner-only permissions, outside `config.json`
  because that file ends up in bug reports. It is never echoed, logged or written into a run receipt.
- Logs stay on your machine, mode 0600, deleted after two days. They hold paths and model names, never API keys.
- Nothing is written to your repository without an approval.

## Project status

Pre-1.0; interfaces can still change between minor versions. Releases are on
[GitHub Releases](https://github.com/danielckv/agentic-plan-forge/releases); see [CHANGELOG.md](CHANGELOG.md).

Contributions are welcome — read [CONTRIBUTING.md](CONTRIBUTING.md). This project follows the
[Contributor Covenant Code of Conduct](CODE_OF_CONDUCT.md). For help, see [SUPPORT.md](SUPPORT.md).

## License

Licensed under the [Apache License, Version 2.0](LICENSE). Copyright 2026 Daniel Chernenkov
