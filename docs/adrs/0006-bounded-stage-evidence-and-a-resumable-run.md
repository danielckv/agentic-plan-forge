# ADR 0006: Bound every piece of stage evidence, and keep an unfinished run on disk

- Status: accepted
- Date: 2026-10-23

## Context

A real run died at the first technical stage:

```text
architect stage failed: bound exceeded: architect payload limit 2097152, observed 3107404
```

Three defects met there.

**The drift list was never bounded.** The architect payload carries five pieces of evidence. Two are
sized: the repository graph is cut to 200 KiB by `ast.Snapshot`, the project context to 64 KiB by
`gather.Collect`. The documentation drift list is capped *per document* — 50 `MISSING_DOC` and 20
`UNVERIFIABLE` each — and not at all across documents. A repository with a few hundred Markdown files
produces thousands of items, each carrying an `expected` and an `observed` string. The executor
passed that whole list to every technical stage (`DocDrift: g.DocDriftReports`). Graph and context
account for at most 270 KiB of the 3,107,404 bytes observed; the rest was drift, the Product Manager
draft and the PRD's technical appendix.

**A payload over the bound failed the stage.** The check in `generate` is correct — it stops a call
that would die inside the host with no diagnosable error — but it was the only response. The run had
already paid for the Product Manager draft and threw the whole cycle away over evidence the stage
could have done without. The error named the limit and the observed size, and nothing about what
filled the payload.

**A failed stage left the board waiting.** `avatars.Run` returned a `*StageError`; the executor
logged a warning and emitted nothing. Page 2 derives an avatar's status as *the newest thought, else
Synced if it has a draft, else Queued while a run is assigned*, so Red-Team Critic and Lead
Synthesizer — which never started — showed `QUEUED` forever, with no retry offered. The same rule had
a second victim: the Lead Synthesizer never takes part in a cycle, so its permanent `Queued` counted
as *working* and the Synthesize button could not enable even when a run converged.

Separately, checkpoints lived only in `CheckpointStore`, in memory, for at most 16 runs. Closing the
window threw away drafts that had already been billed.

## Decision

**One budget per piece of evidence, measured, with the cut stated.** `ast.DriftDigest` bounds the
drift list to `DriftBudgetBytes` (128 KiB), ranked: confirmed drift (`STALE_SIGNATURE`,
`DEPRECATED_FLOW`) first, then `MISSING_DOC`, then `UNVERIFIABLE`; within a tier, items whose
document, code file or symbol matches the story keywords, then by confidence, then in detection
order. It sits beside `SnapshotBudgetBytes` and follows INV-5: both are sized by the 180 s call
timeout, not by a context window. A list that fits is returned unchanged, in detection order.

**A stage trims rather than fails.** `fitPayload` marshals a payload and, while it is over
`MaxStagePayloadBytes`, gives up evidence in a fixed order, cheapest first:

| payload | order |
| --- | --- |
| architect | drift, project context, graph, technical appendix |
| specialist | project context, graph |
| critic | project context, graph |

Each cut halves that part and is named in a new `evidence_notes` field, in the words the model reads:
*"the documentation drift was cut to fit the stage budget: 180 of 13,904 items are included, most
relevant first. Treat what the rest covered as unchecked, not as correct."* The stage's subject is
never cut: the stories and the Product Manager draft for the architect, the architecture and
contracts for a specialist, the drafts under review and every prior critique for the critic — the
critic must re-list all of them, so dropping one would break its contract. When nothing droppable is
left, the `ErrBound` carries a `Detail` naming the three largest top-level fields with their sizes,
so the next step is to shrink that part rather than to guess.

**A run that stops leaves no avatar waiting.** The orchestrator records the last status it emitted
per avatar. A `*StageError` ends the run through `endRunOnFailure`, which gives every avatar without
a terminal status (`Synced`, `Failed`, `Cancelled`, `Stale`) a `Cancelled` one naming the stage that
failed. A failure before the run starts — an interpretation error — does the same from the executor.
Page 2's rules moved into `web/src/ui/room.ts` where they are tested: a queued Lead Synthesizer no
longer counts as working, and a failed stage shows a notice with *Retry stage* and *Revise baseline*
instead of a board that waits.

**An unfinished run is kept in the plan directory.** `storage.RunStore` writes the newest checkpoint
to `docs/plans/.plan/run.json` (mode 0600, inside a directory whose `.gitignore` denies everything by
default). `CheckpointStore.Persist` attaches it as a `CheckpointSink`. The file holds one run per
repository: the next run reads it whatever run id it was assigned, because a restart assigns a new
one, and `runState.compatible` then revalidates the PRD revision, the Product Manager input, the
graph hash, the project context hash and both model slots before adopting it. A checkpoint that
fails that check is dropped from disk and the window is told the saved drafts no longer match. An
adopted checkpoint is republished as drafts, so the epics and the architecture come back onto the
board instead of sitting invisible in memory. Approving a plan clears the file: the drafts it was
built from are no longer work in progress.

## Consequences

- A stage can now run with less evidence than the repository holds. The model is told, every time, in
  the payload. An unstated cut would be worse than a failed stage; a stated one is a bounded input.
- `DriftBudgetBytes` is 128 KiB by argument, not by measurement: it leaves the architect payload
  under half the bound with a 200 KiB graph and a 64 KiB context. It has not been measured against a
  live host the way `MaxEvidenceChars` was in ADR 0004. That measurement is open work.
- `run.json` holds model drafts on disk, which `state.json` deliberately does not. It is inside the
  default-deny plan directory, 0600, removed on approval. A repository that is shared by copying the
  working tree would carry an unfinished plan with it.
- The trim order is a judgement about what a plan can be built without. Drift goes first because it
  describes documentation, not behaviour; the appendix goes last because the user wrote it.

## Alternatives rejected

**Raise `MaxStagePayloadBytes`.** The bound exists because a payload that fits the file does not
necessarily fit the call. The observed payload was 1.5 times the limit; the next repository would be
ten times.

**Summarise the drift with a model call.** Deterministic beats a model call: ranking by drift type
and keyword costs nothing, repeats exactly and cannot invent an item.

**Carry the drift forward as `known_conflicts` only.** Still the v2 idea ADR 0005 recorded, still
unmeasured, still out of scope here.
