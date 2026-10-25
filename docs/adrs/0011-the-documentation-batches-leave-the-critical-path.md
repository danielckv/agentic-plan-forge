# ADR 0011: Take the documentation batches off the critical path, and bound them by count

- Status: accepted
- Date: 2026-10-25
- Closes the open item of [ADR 0010](0010-a-specialist-sees-its-own-workstream.md)

## Context

ADR 0010 added `TestLiveDeliberationEndToEnd` and left its own headline question open: the stage comment
in `internal/avatars/architect.go` says the specialists "wait for this part and for nothing else",
meaning the design, but `runArchitect` waited for all three parts before `runSpecialists` began. The
prize for overlapping them was unmeasured, so the surgery was not attempted.

The test then ran. A 12,472-byte brief for a product that is not this one, against `claude/opus`, one
cycle:

```text
role         calls     total   slowest   payload    output
architect       15    43m46s     8m16s      644K      355K
specialist       4    17m42s     6m14s     1030K        0K
ingestion        2     1m52s       56s       12K       22K
pm               1     1m32s     1m32s       14K       21K

wall clock 25m45s · provider call time 1h04m52s · peak in flight 4 · overlap 2.52
```

Three things in that table were not what anyone expected.

**The architect is the run.** 43m46s of 1h04m52s of provider time, 67% of it, and 15 calls where the
other three roles made seven between them.

**Thirteen of those fifteen calls write documentation tasks.** This repository produces 249
documentation-drift items. `planDocBatches` answers 20 confirmed items per call, so 249 items is 13
calls; the design and the diagrams are the other two. The deliberation was spending most of its money
describing stale documentation, and the plan it would have produced carried 249 documentation tasks.

**The pipeline was already 2.5 times parallel.** The overlap factor was 2.52 against a `max_parallel` of
3, so "it runs everything one at a time" was never the problem. The problem was one stage holding a
barrier in front of another.

The run also failed, at the specialists, on something unrelated and already known: `roleHostOrder` sends
specialists to OMP first whatever `PLAN_FORGE_LIVE_HOST` says, and this machine's OMP is stale. Four
specialist calls spent 17m42s and died with `host advertised a non-empty or invalid tools list`. The
test now pins `MainHost` and `JudgeHost`, which override the rotation.

## Decision

**The documentation batches and the diagrams run beside the specialists.** `runArchitect` splits into
`startArchitect`, which runs the design part, publishes it to the specialists and leaves the other two
parts running, and `finishArchitect`, which joins them, assembles the draft, validates it as a whole and
publishes it. `cycle` runs the specialists between the two.

This is safe for a reason that is worth stating, because it is the whole argument: the specialists read
the summary, the constraints, the workstreams and the contracts, and the design part carries all four.
The merge adds diagrams and documentation tasks and changes nothing a specialist reads. Both
`parseArchitectDesign` and `parseArchitect` call `synthesizer.NormalizeContracts`, so a stale specialist
draft is invalidated against the same contract identity as before the split.

The rest is joined even when the specialists fail, so no provider call outlives the cycle that started
it. An architect failure outranks a specialist one: those specialists were planning against a design
that turned out not to assemble.

**One semaphore bounds the whole cycle.** `architectRest` and `runSpecialists` each built their own
limiter of `par`. Overlapping two such stages would put `2*par` calls in flight — the precise harm the
setting exists to prevent. The orchestrator now owns one `sem`, and a test asserts the ceiling holds at
`max_parallel` 1, 2 and 3 across both stages.

**The drift list is bounded by item count, not only by bytes.** `ast.DriftItemBudget` is 60. A byte
budget does not bound the work a list causes: the item count decides how many provider calls the stage
makes, and 249 items fitted comfortably inside the 128 KiB `DriftBudgetBytes`. 60 is three batches at the
architect's batch size of 20. The ranking ADR 0006 established already puts confirmed drift first, then
keyword matches, so the cap keeps what a plan can act on and drops the long tail of undocumented
symbols. A negative `MaxItems` removes the bound for a caller that wants exhaustive coverage.

`DriftDigestInfo.ItemBound` records which bound cut, and the sentence handed to the model names that one
rather than always claiming a size limit.

## Consequences

- A plan now carries at most 60 drift-derived documentation tasks instead of as many as the repository
  has stale documents. That is a deliberate product change: a specification with 249 documentation
  chores is not one anybody reads to the end. The payload still states how many items were left out.
- The architect's draft reaches the room after the specialists have started, where before it arrived
  first. The board shows the specialists working against a design the room has not yet been shown. This
  is a real regression in what the room explains, and it is not addressed here.
- If the parts fail to assemble, the specialist calls that ran beside them were paid for against a
  design that was never valid. Each part passes its own validation, so this is the rare cross-part
  disagreement, and it already cost a full architect re-run before.
- The 0.5.0 changelog entry claimed the specialists "start as soon as the design is validated". That was
  true of the intent and not of the code. It is true now.

## Open

`TestEveryAvatarReportsTheStepItIsOn` failed once while this change was being made, reporting only
`design 1/3` and `diagrams 2/3`, and did not reproduce in six full-repository runs, five package runs or
three runs under the race detector. The skip path was examined and is guarded: a batch can only fail to
launch on a cancelled context or a sibling failure, and both surface an error that fails the stage, so a
documentation batch cannot be dropped silently. The one-off is recorded rather than called fixed.

Unchanged and still open from ADR 0010: the critic keeps the whole 200 KiB graph, because it reviews
every draft and owns no single workstream.

## Alternatives rejected

**Raise `docBatchItems` above 20.** ADR 0009 already tried 30 and measured that it lengthened each
documentation call rather than shortening the stage. The number of items is the lever, not the batch
size.

**Make the item cap configurable.** Offered and declined: a setting would let the noisy default survive
in exactly the repositories that most need the cap. One bound, stated in the payload, with a programmatic
escape for a caller that genuinely wants everything.

**Leave the batches in front and simply make them cheaper.** Capping the items alone would have cut the
13 calls to 3 and taken most of the time without touching the sequencing. It was the lower-risk half,
and it is also done — but a repository with 60 genuinely confirmed drift items would still hold its
specialists behind three calls for no reason.
