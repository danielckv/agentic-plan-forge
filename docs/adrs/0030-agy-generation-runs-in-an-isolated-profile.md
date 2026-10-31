# 0030. agy generation runs in an isolated profile

Date: 2026-10-31
Status: Accepted

## Context

`agy` (Antigravity CLI) exposes no flag that runs a single call with tools disabled. ADR 0004's rule is that a
generation host must pass a preflight proving it cannot touch the repository; `agy` failed that preflight for many
releases, so a configuration naming it always fell back and every rotation that reached it skipped it. Writing the
plan with a host that can edit files is the exact hazard the tool-free contract exists to stop.

Antigravity CLI reads its settings, credentials and state from `$HOME/.gemini`, so an isolated `HOME` is a complete
isolation boundary: the child process sees a directory Plan Forge owns and nothing else.

## Decision

- `internal/host/agy_profile.go` provisions `$PLAN_FORGE_HOME/profiles/agy` (0700) on first use. It writes a
  Plan-Forge-owned `settings.json` (0600) that denies every tool family, and rewrites `HOME` and `USERPROFILE` to that
  directory for every agy child; `XDG_*` derive from it.
- Existing files in the profile are never rewritten. Preflight validates the policy before a spawn and fails closed
  on drift, so a hand-edited profile that re-enables a tool stops generation instead of running with tools.
- `Auth` and `Models` probe the isolated profile, never the host's own agy sign-in, because a host sign-in says
  nothing about what a generation run can use.
- A fresh profile has no credentials. `plan-forge doctor` prints the one-time sign-in command,
  `HOME="$PLAN_FORGE_HOME/profiles/agy" agy`, and the auth detail names the profile path.

## Consequences

- A host-level agy sign-in does not carry over. This is a one-time re-sign-in for existing agy users, and `doctor`
  prints exactly what to run.
- `agy` joins the default rotation as a first-class host once signed in, beside Claude Code and the provider APIs.
- The profile directory is the only agy state Plan Forge reads or writes; the host's `~/.gemini` is never touched.
- Tests cover the profile path, the env rewrite, the probe writing only inside the profile, and the fail-closed
  preflight without a binary.

## Alternatives rejected

- **Set tools off per call.** No such flag exists in the installed CLI; guessing one would fail open or fail closed
  at random.
- **Accept the host sign-in and trust the settings.** The owner's own settings can enable tools, and a generation
  call would then have them. The isolated directory is the only boundary the CLI actually honours.
