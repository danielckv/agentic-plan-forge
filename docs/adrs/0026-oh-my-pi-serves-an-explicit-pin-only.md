# 0026. Oh-my-pi serves an explicit pin only

Date: 2026-10-31
Status: Accepted
Supersedes: ADR 0025 (oh-my-pi is the last host)

## Context

ADR 0025 kept oh-my-pi in every rotation and gated it behind a readiness probe: it served a run only when neither
Claude Code, the Google credential nor `agy` could. The gate was a compromise. An installation signed in to nothing
else still generated, at a measured cost of 2.83s wall per call against 1.10s of model time, with about 1.15s of
teardown and roughly 2,900 tokens of the CLI's own scaffolding, on a run that makes 25 to 60 calls.

The owner's rule changed: oh-my-pi is not supported by default at all, only when the owner asks for it. The default
placement is Claude Code, Antigravity/Gemini, and the provider APIs. A fallback that the owner never asked for is
still a silent second choice, and a silent second choice on a stage that shapes the architecture is a bad trade.

## Decision

- `main_host: omp`, `judge_host: omp` and a `role_models` entry naming `omp` are the whole opt-in. A pin is the owner
  saying they want it, so the router honours it even when Claude Code or a provider key is ready.
- Without a pin, `omp` never serves. `fallbackHosts()` lists claude and agy only, the role-rotation tail lists claude
  and agy only, and `Router.selectCandidatesLocked` skips `KindOMP` with a reason that names the pin.
- `plan-forge install --host auto` never selects oh-my-pi. `--host omp` installs its command and hook.
- `config.IsValidHost("omp")` is true again. Validation accepts the pin; it no longer rejects it with migration text.
- An installation holding nothing but oh-my-pi reports no usable generation host. The remedy is a pin, not a fallback.

## Consequences

- `TestOMPIsNotAFallbackWhenNothingElseCanServe` locks the default: an omp-only installation without a pin fails.
- `TestOMPServesWhenPinnedAsMainHost` and `TestOMPServesAPinnedRole` lock the opt-in in both directions.
- `TestAutoHostsNeverSelectsOMPAlone` locks the installer: naming the host is the way to want it.
- The skip reason in `doctor` and `/api/status` says the pin that would let it serve.

## Alternatives rejected

- **Keep ADR 0025's readiness gate.** It still serves the CLI's scaffolding to an owner who never asked, which is
  the complaint this ADR answers.
- **Remove the adapter.** `delivery` still drives oh-my-pi as a coding host, and an explicit pin is a supported
  choice. Deleting the adapter would break both.
