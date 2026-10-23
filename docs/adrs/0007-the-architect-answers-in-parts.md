# ADR 0007: The architect answers in parts, a timed-out call shrinks, and the session shows what it spent

- Status: accepted
- Date: 2026-10-23

## Context

A real run died at the first technical stage, for the second time in a day:

```text
architect stage failed: context deadline exceeded
```

The Product Manager finished; the Technical Architect never did. ADR 0006 had already bounded the stage *input*. This
failure was the stage *clock*, and three separate defects met in it.

### One call had to read everything and write everything

The architect call received, by construction:

| part | budget | source |
| :--- | ---: | :--- |
| repository graph | 200 KiB | `ast.SnapshotBudgetBytes` |
| documentation drift | 128 KiB | `ast.DriftBudgetBytes` |
| project context | 64 KiB | `gather.DefaultBudget` |
| Product Manager draft + technical appendix | unbounded in practice | the stage's subject |

That is about 400 KiB of input, roughly 100,000 tokens, before the stage writes a word. What it then had to write was
worse: `ArchitectDoc.Validate` requires one documentation task for **every** confirmed drift item
(`internal/synthesizer/docs.go`), and one task carries steps, targets, an argv verification command and two three-point
estimates — some 500 bytes of JSON each. A repository with 200 confirmed drift items owes 100 KB of output from one
call.

`contracts.MaxGenerateTimeout` is 180 seconds. ADR 0004 measured a 27,000-character document running past that cap on
`claude-opus-5-5-high`. The architect was being asked for several times that, every run, by design.

### A timed-out call was repeated unchanged

`transient` treated `context.DeadlineExceeded` as retryable, so the identical call ran again with the identical payload
and the identical timeout. The user waited 180 s + 1 s backoff + 180 s ≈ 361 s to be told the stage failed. ADR 0004
had already established the opposite rule for normalization, and `internal/impact` followed it: **a call that outran the
clock was too large, so it is replaced by a smaller one, never repeated.** The avatar stages were the one place that
still repeated.

### A second click threw away a paid call

`(*executor).retryStage` called `newRunContext`, which cancels the run in flight and starts another. A double click on
**Retry stage** therefore cancelled up to 180 seconds of billed work and paid for it again. Nothing refused it, and the
window gave no sign that the first click had been received.

### Nothing said what the run had cost

Every adapter already produces `contracts.Usage` from the host's own result event (`internal/host/stream.go`), and
every one of those values was discarded. There was no way to see which host served which stage, how many calls it took
or how many tokens it spent.

## Decision

### The architect answers in parts

The stage runs as three parts, assembled and validated as one draft (`internal/avatars/architect.go`):

1. **Design** — summary, constraints, workstreams, contracts, accepted tradeoffs. It reads the graph, the project
   context, the PM draft and the technical appendix. It does **not** read the documentation drift, which it does not
   answer. The specialists wait for this part and for nothing else.
2. **Diagrams** — the architecture and sequence diagrams, drawn from the design the first part decided. No graph, no
   drift, no context.
3. **Documentation tasks** — one call per batch of at most 20 confirmed drift items (`docBatchItems`), bounded also by
   24 KiB of drift evidence per call (`docBatchBytes`). No graph: a documentation target is a documentation path, which
   the drift item already carries.

Parts 2 and 3 do not depend on each other and run concurrently under the same `MaxParallel` limit as the specialists.
Each part is checkpointed (`runState.ArchDesign`, `ArchDiagrams`, `ArchDocs`), so a failure costs its own call and a
retry re-runs the part that failed. Every part that finished is written to the checkpoint **before** any failure is
reported.

Task ids cannot collide across batches: each batch is given an `id_prefix` (`T-DOC-<batch>-`) and a task whose id does
not carry it fails that batch. Coverage is checked per batch — a batch answers every item it was given — and again over
the assembled draft, which is the same check as before.

**Rejected: raise `MaxGenerateTimeout`.** The cap is what makes a stage input measurable (INV-5). Raising it hides the
growth instead of bounding it, and a host that kills the child process at its own limit would still kill it.

**Rejected: drop the one-task-per-drift-item rule.** The documentation tasks are the reason the drift scan exists. The
work is real; it just does not fit one call.

### A call that outran the clock shrinks

`generate` now replaces a timed-out call with a strictly smaller one: the payload gives up evidence until it is at most
half its size, the cut is named in `evidence_notes`, and the call runs again. At most two shrinks
(`maxTimeoutShrinks`), so a payload falls to a quarter before the stage fails. `transient` no longer retries
`context.DeadlineExceeded`: a payload with nothing left to give (the Product Manager input, a repair input) fails after
one call instead of spending a second timeout to fail the same way.

This is the third application of ADR 0004's rule, after `internal/ingestion` and `internal/impact`.

### One run per agent

The executor holds one claim (`claimRun`/`releaseRun`). A `RETRY_STAGE` for work already in flight is refused, and the
window is told the stage is still running rather than left silent. `stopRun` frees the claim, so cancelling and
restarting still works. Page 2 shows the button as `Retrying…` until the board moves, which closes the window between
the click and the first status event.

### The session reports what it spent

`host.Meter` wraps the generator, so one handle sees ingestion, the impact map and every avatar. It counts requests,
failures, reported input and output tokens and wall time per **host × model × role**, attributing each call to the host
named in the receipt the adapter fills before the call runs — a failed call is still attributed. `GET /api/usage`
serves the report; the Usage tab in the Page 2 rail reads it, per host and per avatar, polling while a cycle is
running.

## Consequences

- The architect's calls are bounded by construction: the design call no longer carries 128 KiB of drift, and the
  documentation work is split into calls of at most 20 items. The specialists start after the design part rather than
  after the whole stage.
- A failed stage costs one part, not the stage. A timed-out call costs one timeout, not two.
- The documentation part sees its own batch and the workstreams, not the whole design. Cross-batch consistency rests on
  the id prefix and on the assembled validation, both tested.
- Evidence notes now name cuts made for the clock as well as cuts made for the byte bound. The wording says which.
- `ArchitectDoc.Validate` is unchanged in what it demands; it is now composed of `ValidateDesign`, `ValidateDiagrams`
  and `ValidateDocTasks`, which the parts reuse so one rule has one implementation.
- Token counts are only as good as what a host reports. A host that reports none shows zero, and the panel says calls
  and time regardless.

## Status of the measurement

The part boundaries are argued from the budgets above and from ADR 0004's measurements, not yet measured end to end on
a live host. `docBatchItems = 20` is the first number to check against a real run.
