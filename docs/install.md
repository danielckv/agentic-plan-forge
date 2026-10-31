# Install

## What you need

- **Git.** Plan Forge grounds its planning in a repository.
- **A browser.** The workspace opens as a tab. Set `browser_path` to choose which browser.
- **A model.** Either a coding agent you are signed in to (Claude Code, Antigravity) or an API key for Anthropic,
  OpenAI, Fireworks or Google. Setup asks on the first run.
- **Poppler and Tesseract**, only to read scanned PDFs. Born-digital PDFs need nothing extra.

API keys are written to `~/.plan-forge/credentials.json`, mode `0600`, never into `config.json`.

## One command

macOS and Linux:

```sh
curl -fsSL https://github.com/danielckv/agentic-plan-forge/releases/latest/download/install.sh | sh
```

Windows, PowerShell 5.1 or later:

```powershell
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12; irm https://github.com/danielckv/agentic-plan-forge/releases/latest/download/install.ps1 | iex
```

Restart your agent, then type `/plan-forge`. The restart matters: agents read their skill directories at start-up.

On macOS and Linux the installer purges the old state first, so a new release starts from defaults. Your API keys are
copied aside and restored.

## What gets installed

`plan-forge install --host auto` finds every agent on the machine and writes the same two skills into each, plus one
managed hook. Installing an agent is not the same as choosing which model writes the plan: oh-my-pi gets the skills
and the hook, and never generates. See [configuration](configuration.md).

| Agent | Skills | Hook |
| :--- | :--- | :--- |
| Claude Code | `~/.claude/skills/{plan-forge,plan-config}/SKILL.md` | `~/.claude/settings.json` |
| Antigravity | `~/.gemini/antigravity-cli/skills/…` and `~/.gemini/config/skills/…` | `~/.gemini/config/hooks.json` |
| oh-my-pi | `~/.omp/agent/skills/{plan-forge,plan-config}/SKILL.md` | `~/.omp/agent/hooks/pre/plan-forge.ts` |

The skill runs the forge. The hook does the two things a skill cannot: report a plan that changed since your last
session, and move the agent into plan mode once a plan is approved. In Claude Code the hook also refuses an edit once
when the approved plan changed under a running session.

The Claude Code copies carry `enabled: false`, which oh-my-pi's skill scanner skips and Claude Code ignores. Without it
oh-my-pi would list the Claude-rendered body too. `plan-forge doctor` names any skill it would list twice.

`--scope project` writes into the repository instead. It is opt-in, because those directories are usually machine-local.

Re-running the installer is safe. It skips unchanged files, leaves files you edited alone and exits 5 so you can
decide, and keeps a manifest so removal is exact. `--force` overwrites, `--dry-run` prints the plan.

## Antigravity signs in separately

`agy` has no isolated-settings flag, so Plan Forge gives it one: `~/.plan-forge/profiles/agy` becomes the whole home of
every `agy` process it starts. Plan Forge owns the `settings.json` there and denies every tool family, which is what
makes plan generation tool-free; weaken it and generation stops rather than running with tools.

Sign in once, as the profile sees it:

```sh
HOME="$HOME/.plan-forge/profiles/agy" agy
```

A sign-in to your own `~/.gemini` does not count. `plan-forge doctor` prints the command with the right path.

## Check it

```sh
plan-forge version
plan-forge doctor
```

`doctor` reports which agents were found, whether each is signed in, which models are available, and whether the
hooks and skills are in place.

## Update

Re-run the same install command. It verifies the checksum, replaces the binary and re-registers every skill and hook.

Three environment variables are read: `PLAN_FORGE_VERSION` to pin one release, `PLAN_FORGE_INSTALL_DIR` (default
`$HOME/.local/bin`, absolute) and `PLAN_FORGE_NO_SKILLS=1` to install the binary only.

If an update looks like it did nothing:

- A planning session already open keeps the old build. Close the tab and run `/plan-forge` again.
- An older copy earlier on `PATH` shadows the new one. Check `which -a plan-forge`; `plan-forge version` prints the
  version and commit of the copy that actually runs.
- A file you edited is kept, not overwritten, and the command exits 5. Re-run with `--force`.

## Uninstall

```sh
plan-forge uninstall --host all            # the files Plan Forge installed
plan-forge uninstall --host all --purge    # and its own state
```

Only files Plan Forge wrote are removed, and a file that diverged is left in place. `--purge` deletes `config.json`,
the relay token, the manifest, approvals, sessions and caches — each path named, never a recursive wipe. Your
repository's `docs/plans/.plan/` is never touched.

## Build from source

You need Go, Node.js and a C toolchain, because the Tree-sitter runtime links C grammars.

```sh
npm --prefix web ci
npm --prefix web run build
CGO_ENABLED=1 go build -tags client -o plan-forge ./cmd/client
```

**Build the web bundle first, every time.** The Go binary embeds `web/dist` as it finds it. Skip the `npm run build`
and you get a binary carrying the previous bundle, which looks exactly like a browser cache that will not clear.

`go install` is not a supported path: it needs the same C toolchain and skips the web build.
