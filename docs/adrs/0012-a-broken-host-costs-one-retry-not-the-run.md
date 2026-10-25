# ADR 0012: A broken host costs one retry, not the run

- Status: accepted
- Date: 2026-10-25
- Follows the trap recorded in [ADR 0011](0011-the-documentation-batches-leave-the-critical-path.md)

## Context

ADR 0011 recorded the failure as a test problem: `roleHostOrder` sent the specialists to OMP, this
machine's OMP is stale, four calls died, so the live test now pins `MainHost` and `JudgeHost`. That
fixed the test. It did not fix the product, and the owner kept hitting the same wall in real runs —
the Product Manager and the Red Team critic failing on every attempt.

A live probe of all six avatar roles through the real router, against this installation's own
configuration, says why. The configuration asks for `main_host: agy`, `judge_host: omp`, and all
three hosts report ready:

```text
config main_host="agy" judge_host="omp"
pm           MAIN  placed on host=claude
architect    MAIN  placed on host=claude
specialist   MAIN  placed on host=omp   FAILED: capability missing for omp:
                                        host advertised a non-empty or invalid tools list
critic       JUDGE placed on host=omp   FAILED: capability missing for omp:
                                        host advertised a non-empty or invalid tools list
synthesizer  MAIN  placed on host=claude
```

Two separate faults, both invisible from anywhere above the live call.

**The configured host was dropped in silence.** `main_host: agy` is first in `roleCandidates` for
every MAIN role, and not one MAIN role went there. AGY passes `Detect` and passes `Auth` — `doctor`
printed `auth: ready (agy models)` and listed thirteen models — but fails `Preflight`:
`AGY exposes no verified isolated settings mechanism`. `selectCandidatesLocked` skips a candidate
that fails any probe and walks on, which is correct, except that nothing told the owner. They chose a
host, the chooser accepted it, `doctor` called it ready, and every avatar quietly went elsewhere.

**A host can pass every probe and still break the call.** OMP passes `Detect`, `Auth` and
`Preflight`. The tools list that trips `rejectAdvertisedTools` only arrives with the first streamed
event, which is to say after the placement is made, cached in `byRole`, and the call is open. There
was no path back from there: `PlaceRole` caches per role, so every retry of that stage returned the
same dead host, and the stage failed for good.

The two compose into the observed bug. With AGY unusable, `roleHostOrder` decides everything, and it
sends the specialist (`{omp, agy, claude}`) and — through `judge_host` — the critic straight to the
one host that breaks on contact. The Product Manager's rotation is `{claude, omp, agy}`, so it
survives until Claude is busy and it falls through to OMP, which is why it failed intermittently
while the critic failed every time.

## Decision

**A capability failure on an open call demotes the host and re-places the role.** `GenerateStream`
loops: when `adapter.Generate` returns a `*contracts.ErrCapability`, the router marks that host
unusable for the rest of the session, forgets every role placed on it, and asks `PlaceRole` again.
The retry is bounded by the number of registered adapters, and the last host's error is what the
caller sees, so a run with no working host still reports the real reason.

The demotion is deliberately for the whole session, not for one call. The tools list OMP advertises
is a property of the installed binary; the same call would break the same way a second later. One
role pays the cost once and every later role walks past it.

`ErrToolUseObserved` is **not** demoted on. It is already documented as non-retryable: the host is
working correctly and the model misbehaved.

**`doctor` reports whether a host can serve, not only whether it is signed in.** It now runs
`Preflight` per host, prints `serves: NO — <reason>` when that fails, and prints an explicit line when
`main_host` or `judge_host` names a host that cannot serve:

```text
agy        found 1.4.7
  auth:   ready (agy models)
  serves: NO — capability missing for agy: AGY exposes no verified isolated settings mechanism

main_host = agy IGNORED: capability missing for agy: AGY exposes no verified isolated settings mechanism
```

## Consequences

Measured on the same installation, same configuration, after the change — every role reaches a host:

```text
pm           MAIN  answered from claude
architect    MAIN  answered from claude
specialist   MAIN  answered from claude
critic       JUDGE answered from claude
synthesizer  MAIN  answered from claude
narrator     MAIN  answered from claude
skipped agy at preflight: AGY exposes no verified isolated settings mechanism
skipped omp at generate:  host advertised a non-empty or invalid tools list
```

The spread ADR 0009 wanted is gone from this machine, and that is the honest answer rather than a
regression: of three signed-in hosts exactly one can serve a tool-free call. The run completes on it
instead of dying on the other two. An installation with two working hosts still spreads across both.

A demoted host stays demoted even if the owner fixes it mid-session. Re-probing a host that broke the
contract would cost a stage its budget to re-learn what it already knows; restarting the session is
cheaper and is what the `doctor` line now tells the owner to do.

`TestLiveEveryRoleReachesAHost` covers this from the outside: one call per role, through the real
router and the installed hosts, failing if any role reaches nothing. It is skipped unless
`PLAN_FORGE_LIVE_ROLES=1`, because it spends host quota. Unit tests could not have caught either
fault — one lives in a `Preflight` that the test doubles pass, the other in a byte of a stream that
no double emits.
