# Install

Version notes live in [CHANGELOG.md](../CHANGELOG.md). This page describes the current release.

## Requirements

| Requirement | Notes |
| :--- | :--- |
| Git | Plan Forge grounds on a repository. Non-Git folders work with reduced status features. |
| A browser | The UI opens as a tab in your default browser, through the platform URL opener. Set `browser_path` to send it somewhere else. |
| Claude Code or OMP, signed in | Avatar generation runs through these CLIs. |
| Antigravity (optional) | Hosts `/plan-forge` and `/plan-config` as skills. With only Antigravity installed, avatar generation reports that Claude Code or OMP is required. |
| Poppler and Tesseract (optional) | Only needed to read scanned PDFs. Text from born-digital PDFs needs nothing extra. |

Plan Forge never asks for or stores provider API keys. It uses the credentials and configuration your host agent already
has.

## One command

macOS and Linux, installing the binary and registering the skills in every detected local agent:

```sh
curl -fsSL https://github.com/danielckv/agentic-plan-forge/releases/latest/download/install.sh | sh
```

Windows (PowerShell 5.1 or later):

```powershell
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12; irm https://github.com/danielckv/agentic-plan-forge/releases/latest/download/install.ps1 | iex
```

Restart your agent afterwards, then type `/plan-forge`. Restarting matters because agents scan their skill directories at
start-up. If the install directory is not on your `PATH`, the installer prints the exact line to add.

The one-command install fetches the latest published release. [Build from source](#build-from-source) is the path for
unreleased changes.

## What gets installed

The installer verifies the release checksum, places the binary, then runs `plan-forge install --host auto`, which detects
installed agents and writes the skills, commands and hooks:

| Host | Installed files | Slash commands |
| :--- | :--- | :--- |
| Claude Code | `~/.claude/skills/plan-forge/SKILL.md` and `~/.claude/skills/plan-config/SKILL.md` | `/plan-forge`, `/plan-config` |
| OMP | `~/.omp/agent/commands/plan-config.md` and the hook `~/.omp/agent/hooks/pre/plan-forge.ts`, which registers `/plan-forge`. No skill files | `/plan-forge`, `/plan-config` |
| Antigravity | `~/.gemini/antigravity-cli/skills/{plan-forge,plan-config}/SKILL.md` and `~/.gemini/config/skills/{plan-forge,plan-config}/SKILL.md` | `/plan-forge`, `/plan-config` |

**OMP installs no skills.** oh-my-pi reaches Plan Forge through built-in commands only: the hook registers `/plan-forge`
with `pi.registerCommand`, and `/plan-config` is `~/.omp/agent/commands/plan-config.md`. Because no `SKILL.md` is
written for oh-my-pi, a command can never be listed twice. Upgrading removes the `plan-forge` and `plan-config` skill
copies an earlier version installed, and leaves a copy you edited yourself untouched.

Plan Forge still marks the Claude copies `enabled: false`, which oh-my-pi's skill scanners skip. That marker carries the
whole guarantee on its own: oh-my-pi reads `<repo>/.claude/skills` by default and `~/.claude/skills` on opt-in, and an
unmarked copy would surface the Claude-rendered body — whose plan-mode step calls `EnterPlanMode`, a tool oh-my-pi does
not have — beside the command the hook registers. Claude Code ignores the key. `plan-forge doctor` lists the roots each
host reads and flags any skill that would shadow a built-in command, and `plan-forge install` prints the same warning;
suppress it with `ignoredSkills: ["plan-forge*"]` or by removing the foreign provider from `enabledProviders` (Plan Forge
never edits your settings).

One trade comes with this: a skill can be matched from its description, a registered command cannot. Asking oh-my-pi in
prose to "forge a plan" does not trigger Plan Forge on its own — type `/plan-forge`.

## Hooks

Hooks are registered where the host supports them:

- **Claude Code.** Three handlers, and none of them starts a process or opens a tab:
  - `SessionStart` reports a plan that changed since your last session.
  - `PreToolUse` (matcher `Edit|Write|MultiEdit|NotebookEdit`) refuses an edit once when the approved plan changed under
    a running session, and refuses an edit aimed at the approved plan itself.
  - `PostToolUse` (matcher `Bash`) moves the agent into Plan Mode after `plan-forge wait` approves.

  Every entry in `~/.claude/settings.json` carries a `plan-forge managed` marker, and uninstall removes only those.
  Reinstalling over 0.3.0 also removes its command center handler and its `UserPromptSubmit` entry.

  The guard blocks at most once per change: it records what the session was told, denies once when that changes, and
  immediately records the new state, so your next tool call proceeds. It never blocks in a repository with no approved
  plan, and it never runs a repository scan — it reads two files. `Bash` is deliberately outside the matcher, because
  blocking it would block the `plan-forge validate` run the refusal asks for.
- **OMP.** The hook reports plan status at session start, runs the forge from `/plan-forge`, and opens a plan-mode
  session once the plan is approved.
- **Antigravity.** User-scope installs merge a `SessionStart` plan-status entry, carrying the same `plan-forge managed`
  marker, into `~/.gemini/config/hooks.json`; whether Antigravity loads that layout is unverified.

Project-scope installs (`--scope project`) write into the repository instead and are opt-in, because those directories
are usually machine-local and should not be committed.

Installation is idempotent: re-running it skips unchanged files, refuses to overwrite files it does not own, and keeps a
manifest so removal is exact.

### Upgrading from 0.3.0

0.4.0 removed the command center, the plan board and swarm reporting. Run `plan-forge install` once after upgrading. It
rewrites the hooks and removes the `plan-forge-board` skill, unless you edited that skill yourself. Until you do, the old
hooks still call `plan-forge center`, `board` or `swarm`. Those commands now print one line on stderr and exit 0, so
nothing fails in your session. A command center daemon started by 0.3.0 keeps running until you stop it or log out.
`plan-forge uninstall --purge` removes its leftover state.

## Manual binary install

1. Download `plan-forge-<os>-<arch>.tar.gz` (Windows: `.zip`) and `checksums.txt` from the [releases page](https://github.com/danielckv/agentic-plan-forge/releases).
2. Verify the download with `shasum -a 256 -c checksums.txt --ignore-missing` on macOS and Linux, or `Get-FileHash -Algorithm SHA256` on Windows.
3. Extract the archive and move `plan-forge` onto your `PATH`.
4. Run `plan-forge install` to register the skills.

## Verify

```sh
plan-forge version
plan-forge doctor
```

`doctor` reports which agents were found, whether each is authenticated, which models are available, and whether the
browser and optional OCR tools are present. It redacts secrets and prints no paths beyond the skill directories it
manages.

## Update

Re-run the same install command. Skills and hooks are refreshed with the current absolute binary path.

## Uninstall

```sh
plan-forge uninstall --host all
```

This removes only files and hook entries that Plan Forge installed, restoring any settings file it modified. Add
`--purge` to also delete Plan Forge's own configuration, credentials, backups and caches. It refuses if the state
directory lacks Plan Forge's marker file, so it cannot delete a directory it does not own. Repository `.plan/` folders
are never touched.

## Build from source

Contributors need Go, Node.js and a C toolchain, because the Tree-sitter runtime links C grammars:

```sh
npm --prefix web ci
npm --prefix web run build
CGO_ENABLED=1 go build -tags client -o plan-forge ./cmd/client
```

`go install` is not a supported end-user path for the same reason: it requires a local C toolchain.
