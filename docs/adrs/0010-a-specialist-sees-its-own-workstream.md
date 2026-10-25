# ADR 0010: Give a specialist its own workstream's code, and measure a whole run

- Status: accepted
- Date: 2026-10-25

## Context

Nothing measured a complete deliberation. ADR 0004 measured a normalization section, ADR 0009 measured
the architect's design part at 110 seconds on a 206 KiB payload, and `TestLiveImpactEndToEnd` measures
the Page 1 chain. Between the brief and the locked specification there was no number at all: no record
of which stage is slow, how large each payload is, or how much of the pipeline already runs in
parallel. Every answer to "make it faster" was therefore an opinion.

Reading the code for one of those opinions found a concrete waste. `graphForPrompt` returns
`RunInput.Graph` unchanged, and three payloads carry it: the architect's, **every** specialist's, and
the critic's. The graph is bounded once, by `ast.SnapshotBudgetBytes` (200 KiB), against the Product
Manager's story keywords. So a specialist planning the billing workstream is handed the same 200 KiB
that the specialist planning the web workstream is handed, and most of it is code neither will touch.

The architect has already said which code each one owns. `contracts.Workstream.ModulePaths` is
declared in the design part and is the input to the deterministic interface comparison; it was never
used to decide what a specialist reads. Specialists run concurrently, up to `min(MaxParallel, 4)`, so
the waste is not paid once but once per concurrent call, in input tokens and in the latency of each.

## Decision

**A whole run is measured.** `TestLiveDeliberationEndToEnd` drives
`internal/app/testdata/long-brief.md` — 14 KiB, seven epics, a product that is not this one — through
`ingestion.Normalize`, `ast.Scan`, `gather.Collect`, `avatars.Run`, `avatars.Synthesize` and
`synthesizer.Compile` against real hosts. It asserts that each avatar produced the draft its stage
owes: the Product Manager draft decodes to epics and stories, the architect's to workstreams and frozen
contracts, every specialist's to tasks, the critic's to a critique, the synthesizer's to an executive
summary, and the locked design compiles.

A `callRecorder` wraps the generator and times every call. The report names, per role: the number of
calls, total and slowest duration, payload and output bytes, and the host and model each was placed on.
It then prints the run's wall clock against the sum of its call durations. That ratio is the measured
parallelism of the pipeline, not the configured one, and it is the number any later claim about speed
has to move. The test answers its own decision cards with the recommended option, so a conflict between
specialists cannot stall it until the context deadline.

It is skipped unless `PLAN_FORGE_LIVE_DELIBERATION=1` and runs one cycle unless
`PLAN_FORGE_LIVE_CYCLES` says otherwise, because a full run spends real money.

**A specialist's graph is scoped to its workstream.** `ast.SnapshotOptions.Paths` names the
repository-relative directories a caller owns. Those files rank above every keyword match: tier 0 for a
file under an owned path, 1 for a keyword hit, 2 for another file in a module with a hit, 3 for the
rest. A prefix matches only on a path boundary, so `internal/api` does not cover `internal/apikeys`,
and the `/**`, `/*` and trailing `/` forms a `ModulePaths` entry may carry are accepted.

`Paths` ranks; it does not filter. What falls outside an owned path is still included while the budget
allows, so a specialist keeps seeing the contracts it consumes elsewhere. The cutting is done by a
smaller budget: `avatars.SpecialistGraphBudgetBytes` is 64 KiB, against the 200 KiB the architect gets.
The architect designs across the repository and keeps the whole snapshot; the critic reviews drafts and
keeps it too, for now.

Measured on the sample fixture widened to 58 files of exported symbols: a specialist receives 18 files,
every one of them its own, where it previously received all 58. A repository whose graph already fits
64 KiB arrives whole, so a small project loses nothing.

## Consequences

- A specialist can now be wrong about code it was not shown. It was already wrong about code outside
  the 200 KiB bound; the bound simply moved closer to the work. The ranking is deterministic and the
  owned paths come from the architect, so what it loses is the least relevant quarter of the repository
  rather than an arbitrary one.
- The architect and the critic are unchanged. Scoping the critic needs a different rule — it reviews
  every draft, so it owns no single workstream — and that is not attempted here.
- The end-to-end number this ADR exists to produce is not in it. The live run was still going when this
  was written. The unit measurement (58 files to 18) is real; the latency and token effect on a live
  host is not yet recorded, and the next session records it.
- `rankUnits` gained a fourth tier. Callers that pass no `Paths` keep exactly the previous ordering,
  which a test asserts.

## Open

`runArchitect` waits for the diagrams and the documentation batches before `runSpecialists` begins,
although the stage comment states that the specialists "wait for this part and for nothing else"
(`internal/avatars/architect.go:19`). Parts 2 and 3 feed no specialist: the design part alone carries
the summary, the constraints, the workstreams and the contracts. Starting the specialists as soon as
the design lands would take the diagram and documentation calls off the critical path entirely.

It is not done here because it is surgery on the cycle's sequencing — the checkpoint identity, the
critic barrier's `setHash`, and the handling of a part that fails after specialists have already
started all have to be re-reasoned — and because the prize is unmeasured. The live run above reports
what the architect's parts 2 and 3 cost. If they are seconds, the risk is not worth it. If they are
minutes, this is the next change.

## Alternatives rejected

**Filter the graph to the workstream.** A specialist that cannot see the contract it consumes writes
against a signature it guessed. Ranking keeps the whole repository available and lets the budget
decide, which degrades instead of breaking.

**Shrink the budget for everyone.** The architect genuinely designs across the repository; it is the
one stage whose subject is the whole graph. Cutting its evidence to make specialists cheaper would pay
for the wrong stage.

**Derive the scope from the stories instead of the module paths.** Keyword ranking is what already
happens and it is why every specialist got the same graph: the stories describe the product, not the
division of labour. The architect's `ModulePaths` is the only statement in the run of who owns what.
