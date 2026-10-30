# ADR 0023: A run always produces a plan, and says what is still open

- Status: accepted
- Date: 2026-10-30
- Supersedes the cap behaviour in [ADR 0016](0016-five-minute-deliberation-budget.md)

## Context

A deliberation run could end with nothing to show for itself.

When the Red-Team Critic still had a `CRITICAL` or `HIGH` finding open at the three-cycle cap, the authority marked
the run capped (`internal/session/reducer.go:742`). From there `CYCLE_START` was rejected — *"run is paused at the
cycle cap"* (`:598`) — and `SYNTHESIZE_REQUEST` was rejected too, with `CodeBlockersOpen` (`:770`). The only command
that moved a capped run was `RETRY_STAGE`, which calls `resetRunLocked` and clears the run id, the drafts, the
blockers, the decisions and the failed stages (`:1308-1319`), then starts again at cycle one.

So the run could neither advance nor finish, and the single exit discarded every draft the avatars had produced. The
work was paid for — ADR 0011 timed one cycle at 43m46s of architect time across 15 calls, and ADR 0016 timed a whole
run at 1h16m31s across 93 calls — and the owner saw no plan. Starting over was likely to reach the same wall, because
nothing about the second attempt made the finding easier to resolve.

Separately, convergence required **zero open MEDIUM findings** as well as zero blockers
(`internal/avatars/orchestrator.go:267`). A MEDIUM has never blocked a plan, yet one of them bought a full extra
cycle: the architect re-drafted, up to four specialists re-drafted, and the critic barrier re-ran over the whole
repository graph. That could happen twice before the cap.

Google's Elephant-Goldfish Model, which this project's reviewer design follows, takes the opposite position on both
points. Its review step is explicitly advisory — *"This will provide a lot of feedback by design. Take what you think
is relevant. Ignore the rest."* — and its synthesizer is required to write the deliverable artifact before it reports
anything at all.

## Decision

**A run always produces a plan, and the plan says what is still open.**

At the cap with blockers open, `settleBlockers` (`internal/avatars/decisions.go`) raises one decision card: *"Record
them and produce the plan"* or *"Pause for manual revision"*, recommending the former. It mirrors `settleMediums`,
which has done exactly this for MEDIUM findings since ADR 0016, so this is an existing, tested shape applied to a
severity that previously had no such path. Choosing to pause still reaches the old `o.pause` behaviour, so nothing is
taken away.

**A recorded finding stays distinguishable from a fixed one.** `contracts.RecordedRefPrefix` and
`Critique.IsRecorded` mark it, and the compiled specification opens with a **Plan readiness** section — before the
phases and before the design — stating `complete` or `provisional` and listing every recorded finding with its
severity, target, evidence and the change it still asks for.

Every open critique is settled by that decision, not only the blocking ones. The plan is gated twice: the authority
counts CRITICAL/HIGH into `openBlockers`, and the compiler's `Validate` rejects any unresolved critique, requiring an
`accepted:` reference for a MEDIUM (`internal/synthesizer/compiler.go:204-216`). Settling only the blockers cleared
the first gate and failed the second.

**Only a blocker is worth another cycle.** The convergence switch gains a case: with no blockers open, the run offers
the MEDIUM choice immediately rather than spending a cycle first.

No change was needed in the session reducer. The `CodeBlockersOpen` gate is correct and stays; it was simply
unreachable, and now it is reachable.

## Consequences

- A capped run ends with a usable plan instead of nothing, and the avatars' work is never discarded to escape a cap.
- **A plan can now ship carrying a known CRITICAL.** This is a deliberate reversal of a safety gate and the reason
  this ADR exists. The mitigations are that the owner must choose it explicitly, the choice is recorded as a decision,
  and the specification states the verdict before any of the design it is judging. What is not acceptable is a plan
  that looks clean while carrying a waived blocker; the readiness section exists so that can never happen silently.
- A mediums-only run reaches its decision after one cycle instead of three, saving roughly two cycles of calls.
- A run can still end without a plan — by the owner choosing to pause, which is now a decision rather than a trap.
- Anything reading critiques must treat `IsRecorded` as "resolved for the gates, unresolved in engineering terms".
