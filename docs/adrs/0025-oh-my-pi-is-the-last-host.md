# 0025. Oh-my-pi is the last host, not the default

Date: 2026-10-31
Status: Accepted
Supersedes: the host ordering in ADR 0022 for the oh-my-pi adapter only

## Context

ADR 0022 moved generation onto provider APIs and kept the CLI hosts first-class. In that release the CLI hosts were
not reachable from the role rotation at all: `roleHostOrder` and the trailing candidates in `router.go` listed provider
adapters only, so an installation signed in solely to Claude Code or oh-my-pi reported "no usable generation host".

Oh-my-pi also charged for the privilege of being in the rotation. A measured tool-free `omp` call costs 2.83s wall
against 1.10s of model time, with about 1.15s of teardown and roughly 2,900 tokens of the CLI's own scaffolding added
to every call. A run makes 25 to 60 calls. That scaffolding buys nothing when a direct API call or Claude Code can
serve the same stage.

## Decision

Claude Code, the Google credential (`gemini` and `antigravity`) and `agy` outrank oh-my-pi for every role. Oh-my-pi
serves a run only when none of them is ready.

The rule is enforced in `Router.selectCandidatesLocked`, not in the candidate lists, so it holds even for an owner
who pinned `main_host: omp`: the pin decides the preference, the rule decides what is allowed to serve. A skipped OMP
records why, which `plan-forge doctor` prints.

`auto` in the installer follows the same rule. It selects Claude Code and Antigravity when they are present, and
oh-my-pi only when neither is.

## Consequences

- An installation with a Gemini key, or with Claude Code, never pays the CLI's startup cost per call.
- An installation with only oh-my-pi still works. The fallback is a real host, not a refusal.
- A CI runner with no hosts still reports every list as an array (`capabilities_test.go`), now with an emptied
  `PLAN_FORGE_HOME` as well as an emptied `PATH`; the API hosts answer from `credentials.json`, so PATH alone never
  made that test hermetic.
- `homeDir` honours `$HOME` when it is set. `os.UserHomeDir` reads the passwd entry on macOS, which reached the real
  home from a sandboxed run and made host detection untestable.

## Alternatives rejected

- **Drop oh-my-pi entirely.** It is the only host for an owner who has neither Claude Code nor a Google account, and
  removing the adapter would turn a working installation into "no usable generation host".
- **Order it last in every list instead of gating it.** A list order does not survive a `main_host` pin, a
  `role_models` binding or a lane rotation; the owner's own config pins `omp` today. The gate holds in all three.
