# Install

Version notes live in [CHANGELOG.md](../CHANGELOG.md). This page describes the current release.

## Requirements

| Requirement | Notes |
| :--- | :--- |
| Git | Plan Forge grounds on a repository. Non-Git folders work with reduced status features. |
| A browser | The UI opens as a tab in your default browser, through the platform URL opener. Set `browser_path` to send it somewhere else. |
| Claude Code or Antigravity (OMP only as a fallback) | Generation runs through Claude Code, the Google Gemini key, or Antigravity. Oh-my-pi serves only when none of them is present; each of its calls carries the CLI's own scaffolding. |
| Antigravity (optional) | Hosts `/plan-forge` and `/plan-config` as skills. |
| Poppler and Tesseract (optional) | Only needed to read scanned PDFs. Text from born-digital PDFs needs nothing extra. |

Plan Forge stores API keys in `~/.plan-forge/credentials.json` with mode `0600`, never in `config.json`.
`plan-forge uninstall --purge` deletes that file along with the rest of the state.

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

### A reinstall starts from defaults

On macOS and Linux the installer runs `plan-forge uninstall --purge --yes` before it registers anything. That deletes
`config.json`, the relay token, the approvals file, the sessions and the cache, so an installation that had pinned
`main_host`, shortened a timeout or enabled a host does not carry those settings into the new release.

`credentials.json` is copied aside first and restored afterwards: the keys are what make the Gemini and Claude hosts
work, and asking for them again is not a default value.

## What gets installed

The installer verifies the release checksum, places the binary, then runs `plan-forge install --host auto`, which detects
installed agents and writes the skills, commands and hooks. `auto` selects Claude Code and Antigravity when they are
present, and oh-my-pi only when neither is.

| Host | Installed files | Slash commands |
| :--- | :--- | :--- |
| Claude Code | `~/.claude/skills/plan-forge/SKILL.md` and `~/.claude/skills/plan-config/SKILL.md` | `/plan-forge`, `/plan-config` |
| Antigravity | `~/.gemini/antigravity-cli/skills/{plan-forge,plan-config}/SKILL.md` and `~/.gemini/config/skills/{plan-forge,plan-config}/SKILL.md` | `/plan-forge`, `/plan-config` |
| OMP (fallback only) | `~/.omp/agent/commands/plan-config.md` and the hook `~/.omp/agent/hooks/pre/plan-forge.ts`, which registers `/plan-forge`. No skill files | `/plan-forge`, `/plan-config` |

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

0.4.0 removed the command center, the plan board and swarm reporting. The one-command install runs `plan-forge install`
for you, which rewrites the hooks and removes the `plan-forge-board` skill, unless you edited that skill yourself. If you
upgraded another way, run `plan-forge install` once. Until then, the old hooks still call `plan-forge center`, `board`
or `swarm`; those commands print one line on stderr and exit 0, so nothing fails in your session. A command center
daemon started by 0.3.0 keeps running until you stop it or log out. `plan-forge uninstall --purge` removes its leftover
state.

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

Re-run the same install command. There is no separate update command, and nothing needs forcing: the installer
downloads the archive, verifies it against `checksums.txt`, and replaces the binary with an atomic `mv`, whether or not
one is already there. It then re-runs `plan-forge install`, which refreshes every skill and hook with the current
absolute binary path.

```sh
curl -fsSL https://github.com/danielckv/agentic-plan-forge/releases/latest/download/install.sh | sh
```

To install one exact release instead of the newest — to test a build, or to roll back — set `PLAN_FORGE_VERSION`:

```sh
curl -fsSL https://github.com/danielckv/agentic-plan-forge/releases/latest/download/install.sh | PLAN_FORGE_VERSION=v0.4.0 sh
```

Two other variables are read: `PLAN_FORGE_INSTALL_DIR` (default `$HOME/.local/bin`, must be absolute) and
`PLAN_FORGE_NO_SKILLS=1`, which installs the binary and stops before registering skills.

On Windows, re-run the same `irm … install.ps1 | iex` line. The three variables are the same, with
`PLAN_FORGE_INSTALL_DIR` defaulting to `%LOCALAPPDATA%\Programs\plan-forge`. Windows cannot overwrite a running
executable, so the installer renames the current one to `plan-forge.exe.old` and leaves it there; the next update
deletes it. A leftover `.old` file is normal and safe to remove once no session is using it.

### When an update looks like it did nothing

- **A planning session already open keeps the old build.** Replacing a file does not change a process already running
  from it. Approve or close the tab, then run `/plan-forge` again.
- **An older copy earlier on `PATH` shadows the new one.** Check with `which -a plan-forge`; `plan-forge version` prints
  the version, commit and platform of the copy that actually runs.
- **Your edits are kept, not overwritten.** `plan-forge install` compares each file it owns against the hash it recorded
  when it wrote it. A file that no longer matches is one you changed, so it is left alone and the command exits 5. Run
  `plan-forge install --force` to overwrite it, or `--dry-run` first to see what would change. The parse and impact
  caches under `docs/plans/.plan/cache/` need no attention: each records the version of the logic that filled it and is
  discarded when that changes.

### Re-registering without downloading

If the skills or hooks are missing or point at a moved binary, fix them without touching the binary itself:

```sh
plan-forge install --dry-run     # print the plan, write nothing
plan-forge install               # every host whose binary is found
plan-forge install --host claude # one host
plan-forge doctor                # what is registered now, and what is missing
```

Exit codes: 3 no supported agent found, 4 declined at the prompt, 5 a managed file diverged (see above).

## Uninstall

```sh
plan-forge uninstall --host all
```

This removes only files and hook entries that Plan Forge installed, restoring any settings file it modified. A file that
diverged from what Plan Forge wrote is left in place rather than deleted, for the same reason `install` refuses to
overwrite it. Use `--dry-run` to see what would go first, and `--host claude|omp|agy` to remove one host's artifacts.

```sh
plan-forge uninstall --host all --purge
```

`--purge` additionally deletes Plan Forge's own state: `config.json`, `relay-token.json`,
`install-manifest.json`, `approvals.json`, the backups and sessions under its home, and the `run`, `uploads`,
`sessions`, `recovery` and `notices` directories in its cache. Each path is enumerated, so a purge never removes a
directory recursively whatever it happens to contain, and it refuses outright if the state directory lacks Plan Forge's
marker file, so it cannot delete a directory it does not own.

Repository `.plan/` folders are never touched by either command: an approved `synthesized_spec.md` and
`plan_manifest.json` survive uninstalling Plan Forge itself.

## Build from source

Contributors need Go, Node.js and a C toolchain, because the Tree-sitter runtime links C grammars:

```sh
npm --prefix web ci
npm --prefix web run build
CGO_ENABLED=1 go build -tags client -o plan-forge ./cmd/client
```

`go install` is not a supported end-user path for the same reason: it requires a local C toolchain.
