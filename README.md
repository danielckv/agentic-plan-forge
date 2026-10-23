# <img src="docs/logo.svg" alt="" width="30" height="30" align="top"> Agentic Plan Forge

[![CI](https://github.com/danielckv/agentic-plan-forge/actions/workflows/ci.yml/badge.svg)](https://github.com/danielckv/agentic-plan-forge/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/danielckv/agentic-plan-forge?sort=semver)](https://github.com/danielckv/agentic-plan-forge/releases/latest)

**A blocking pre-planning gate for coding agents.** Instead of letting an agent jump from a brief straight to a linear
plan, Plan Forge deliberates first. You write what you want in a Markdown composer. Plan Forge reads the repository's
real code and the decisions your team already wrote down, runs a staged review by four avatars, and returns a verified
specification that the agent then executes.

It ships as one Go binary with an embedded web UI, plus an optional relay server for team planning sessions.

![Agentic Plan Forge: your requirements, the real code and the team's recorded decisions go into a deliberation by four avatars and a red-team critic, and a verified specification comes out into Plan Mode](docs/images/overview.png)

## Quickstart

```sh
curl -fsSL https://github.com/danielckv/agentic-plan-forge/releases/latest/download/install.sh | sh
```

Restart your agent, then type `/plan-forge`. You need Git, a browser, and Claude Code or OMP signed in. The planning
window opens as a tab in your default browser when you run `/plan-forge`, and only then. Plan Forge never asks for or
stores provider API keys: it uses the credentials your host agent already has.

Re-run the same command to update; it replaces the binary and re-registers the skills, so there is nothing to force.
Windows, pinning a version, `plan-forge uninstall` and the full host matrix are in [docs/install.md](docs/install.md).

## A look inside

**Write the requirements.** One large editor with headings, checklists and tables. Type `/` for blocks such as an epic
or a diagram, and `@` to use the exact name of a component in your code. On the right, the requirements become epic files
as you write, each linked to the code it touches.

**See what it touches.** The Impact tab names the parts of your project the work affects and what happens to each:
changes, new, already there, or clashes with a decision the team already recorded. One sentence apiece, in language that
does not assume you read code.

![The composer: a Markdown editor with the requirements on the left and three epic files on the right, each listing the components it touches](docs/images/composer.png)

| Open an epic as a document | Browse the codebase while you write |
| :---: | :---: |
| ![An epic file open in a modal: goal, requirements, acceptance criteria and a table of related code](docs/images/epic-modal.png) | ![The Codebase tab lists the repository's blocks and components, and @ completion suggests QueryInvoices in the editor](docs/images/codebase-palette.png) |

**Watch the avatars plan.** After you confirm, the Product Manager, Technical Architect, Red-Team Critic and Lead
Synthesizer work in the open: every status, draft, finding and decision lands in one discussion, and the plan takes shape
beside it.

![The avatar room: four avatars with their status, a discussion timeline with a pinned decision, and the Product Manager's epics forming on the right](docs/images/avatars.png)

| Review the specification | Light or dark |
| :---: | :---: |
| ![The master specification with an executive summary and the architecture diagram](docs/images/synthesis.png) | ![The composer in the dark theme with the project notes tab open](docs/images/composer-dark.png) |

The screenshots come from the UI driving a seeded sample session (`web/scripts/promo.mjs`), not from a live model run.

## Why

Agent plans often fail before implementation starts. A single-pass planner converges on the first plausible design,
trusts documentation that no longer matches the code, forgets what the team decided last month, and mixes product
requirements with low-level technical constraints. Plan Forge inserts a deliberation step that is slower and more
sceptical on purpose:

- Requirements are separated from technical constraints, so product decisions are not made by code-level accident.
- Proposals are grounded in the repository's real structure, and documentation drift is reported instead of trusted.
- The avatars read the team's decision records, specs and notes, so a plan builds on earlier decisions instead of
  reopening them.
- Drafts are attacked by a dedicated critic that runs on a different model slot than the authors.
- The result is a contract-verified specification with explicit targets, verification commands and effort estimates.

## How it works

Each avatar runs as a fresh, tool-free model invocation through the host agent CLI you already have installed and
authenticated. The Product Manager receives product requirements only: no file trees, no symbol graphs, no code, no
project notes. The Red-Team Critic runs on a separate model slot so it does not simply agree with its own drafting.

Repository grounding is deterministic. Files are parsed with Tree-sitter and reduced to exported symbols, signatures and
route registrations; documentation is cross-referenced against that graph so stale references become explicit
documentation-sync tasks instead of silent assumptions. Parse coverage gaps are reported as gaps, and structural matches
are never treated as proof that behaviour is complete.

Before the avatars start, Plan Forge gathers what the repository already says about the work: decision records, specs,
notes and recent commit subjects, matched against your requirements and bounded to 12 excerpts. The Impact tab then asks
the model which of those parts the requirements actually affect. It answers by picking from a numbered shortlist rather
than writing paths, so every file it names exists. That map is for you; the avatars never receive it.

Effort is estimated twice from the same task graph: a human schedule based on a 9-hour productive day, and an agent
schedule based on declared parallelism. Estimates are labelled as estimates unless they come from recorded runs.

The whole flow, page by page, is in [docs/usage.md](docs/usage.md).

## Documentation

| Guide | What it covers |
| :--- | :--- |
| [Install](docs/install.md) | Requirements, one-command and manual install, what gets written per host, hooks, update and uninstall |
| [Usage](docs/usage.md) | The composer, the avatar room, the specification, Plan Mode handoff, `plan-forge validate` and `context` |
| [Configuration](docs/configuration.md) | Hosts, models, workday baseline and self-hosting the relay |
| [Decision records](docs/adrs/) | Why the live task views were removed, how project context is read, why the text is not scanned, how the impact map is built |
| [Requirements catalogue](docs/requirements/) | Owners, acceptance criteria and traceability for every requirement |
| [Contributing](CONTRIBUTING.md) | Repository layout, development workflow and the full local check suite |

## Privacy and security

- Requirements documents, repository contents, project notes and absolute paths stay on your machine unless you
  explicitly start a remote session. The relay receives session state: the shared document, avatar summaries and drafts,
  critiques, decisions and the approved plan.
- Plan Forge does not scan, redact or block what you write. Your text goes to your own agent CLI, exactly as it would in
  a chat with that agent. See [ADR 0003](docs/adrs/0003-no-text-scanning-in-composer.md).
- Avatar invocations are tool-free. Every generation runs with repository access, skills and MCP servers disabled, and a
  tripwire aborts a run if a tool call appears anyway.
- A relay never accepts requests from a browser, requires TLS off loopback, and stores only hashes of credentials.
- Security reports are welcome, and should not be filed as public issues. See [SECURITY.md](SECURITY.md).

## Project status

Plan Forge is **pre-1.0**. Releases are published on
[GitHub Releases](https://github.com/danielckv/agentic-plan-forge/releases) with client archives for macOS, Linux and
Windows, relay archives for Linux, and a multi-architecture relay image at `ghcr.io/danielckv/plan-forge-relay`.
Interfaces can still change between minor versions; see [CHANGELOG.md](CHANGELOG.md).

Next on the roadmap: signing and notarisation for macOS and Windows builds, and Antigravity as a generation host once
its CLI exposes an isolated-settings selector.

## Contributing

Contributions are welcome. Please read [CONTRIBUTING.md](CONTRIBUTING.md) for the development workflow, local checks and
pull request expectations. This project follows the
[Contributor Covenant Code of Conduct](CODE_OF_CONDUCT.md). For help, see [SUPPORT.md](SUPPORT.md).

## License

Licensed under the [Apache License, Version 2.0](LICENSE).

Copyright 2026 Daniel Chernenkov
