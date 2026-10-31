# <img src="docs/logo.svg" alt="" width="30" height="30" align="middle"> Agentic Plan Forge

[![CI](https://github.com/danielckv/agentic-plan-forge/actions/workflows/ci.yml/badge.svg)](https://github.com/danielckv/agentic-plan-forge/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/danielckv/agentic-plan-forge?sort=semver)](https://github.com/danielckv/agentic-plan-forge/releases/latest)

**A deliberate planning gate for coding agents.** A coding agent asked to "add invoice export" produces a plan in
seconds. It is plausible and usually wrong: it trusts stale documentation, forgets what the team decided last month,
and lets a technical constraint decide a product question. Plan Forge reads the repository first, argues with itself
about the plan, then hands you something to approve — as one Go binary with an embedded web UI, plus an optional
relay for team sessions.

One brief, from submission to the question drawer and the handoff, at 1× speed with no cuts:

![A brief submitted, the agents working, a fusion, and the question drawer answering one decision](docs/images/demo.gif)

## Quickstart

```sh
curl -fsSL https://raw.githubusercontent.com/danielckv/agentic-plan-forge/main/scripts/install.sh | sh
```

Restart your agent, then type `/plan-forge`. You need Git, a browser, and a model: either a coding agent you are
already signed in to (Claude Code, oh-my-pi) or an API key for Anthropic, OpenAI, Fireworks or Google, which Setup
asks for on a first run. The installer closes with a summary of the home, hosts and models it set up, and an
interactive run offers to edit `config.json` right there. `plan-forge run` opens the same workspace in a browser tab
without an agent. Re-running the
command updates the binary and re-registers the skills. Windows, version pinning, `uninstall` and the full host
matrix are in [docs/install.md](docs/install.md).

## How it works

Grounding is deterministic and runs before any model: the repository is parsed into exported symbols, signatures and
routes, documentation is checked against that graph, and the team's decision records, specs and recent commits are
matched against your requirements. Nothing is invented before that finishes.

The avatars — a Product Manager, a Technical Architect, one Domain Specialist per workstream, and a Red-Team Critic —
draft in cycles until the critic has nothing left that is CRITICAL or HIGH. Each avatar is a fresh, tool-free model
invocation: no repository access, no skills, no inherited conversation. The evidence it may see is assembled by Plan
Forge and passed in as data, and they deliberately see different things — the Product Manager never sees the code, so
product decisions stay product decisions. A Code Reviewer and a Technical Leader then read the assembled candidate at
once, one bounded repair pass answers the blocking findings, and a Lead Synthesizer compiles the specification.

A run always ends with a plan. If blocking findings remain when the cycle budget runs out, you choose whether to take
the plan with them recorded on it or pause and revise — and the specification opens with a readiness verdict naming
every one. A blocked plan is refused rather than written.

After approval, the agent implements the plan in an isolated worktree — never in your repository — with a diff tab
and a container preview. With a sandbox provider configured, a prepared VM runs the project's own recorded build,
test and run commands instead, with revision, cost and expiry stated before the launch. An acceptance binds one head
commit, one recipe and one bundle of evidence, and needs your sign-off plus a developer's GitHub approval of that
same commit.

## Documentation

| Guide | What it covers |
| :--- | :--- |
| [docs/install.md](docs/install.md) | Every host, Windows, version pinning, uninstall |
| [docs/usage.md](docs/usage.md) | The window, page by page |
| [docs/configuration.md](docs/configuration.md) | Hosts, models, `max_gap_agents`, storage, sandbox and repository settings |
| [docs/adrs/](docs/adrs/README.md) | Why each boundary sits where it does |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Build, test and pull request workflow |

## Privacy and security

- Requirements, repository contents and absolute paths stay on your machine. No telemetry, no account. The window
  talks to a loopback bridge in the same process, which checks the request origin and a session token.
- An API key is written to `~/.plan-forge/credentials.json` with owner-only permissions, outside `config.json`
  because that file ends up in bug reports. It is never echoed, logged or written into a run receipt; the window only
  receives a masked form. `plan-forge uninstall --purge` deletes it.
- Logs stay on your machine: `~/.plan-forge/logs/plan-forge-YYYY-MM-DD.log`, mode 0600, deleted after two days.
  They hold paths and model names, never API keys.
- Nothing is written to your repository without an approval. A prepared VM receives a packaged archive, never a
  clone, and never your GitHub write token.

## Project status

Pre-1.0; interfaces can still change between minor versions. Releases are on
[GitHub Releases](https://github.com/danielckv/agentic-plan-forge/releases); see [CHANGELOG.md](CHANGELOG.md).

Contributions are welcome — read [CONTRIBUTING.md](CONTRIBUTING.md). This project follows the
[Contributor Covenant Code of Conduct](CODE_OF_CONDUCT.md). For help, see [SUPPORT.md](SUPPORT.md).

## License

Licensed under the [Apache License, Version 2.0](LICENSE). Copyright 2026 Daniel Chernenkov
