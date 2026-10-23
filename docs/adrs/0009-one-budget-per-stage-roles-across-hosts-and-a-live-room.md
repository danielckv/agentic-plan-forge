# ADR 0009: Give a stage its own budget, spread the roles across the hosts, and say what each avatar is doing

- Status: accepted
- Date: 2026-10-23
- Supersedes: the "Rejected: raise `MaxGenerateTimeout`" decision in
  [ADR 0007](0007-the-architect-answers-in-parts.md)

## Context

The architect stage still failed on every run, with the same line ADR 0007 was written to fix:

```text
avatar stage failed  stage=architect partial=true  err="context deadline exceeded"
avatar stage failed  stage=architect partial=false err="context deadline exceeded"
```

ADR 0007 split the stage into three parts and rejected raising the cap, on the reasoning that "the cap is what makes a
stage input measurable (INV-5)". The split was right and it stays. The rejection was wrong about one part, and the
measurement says why.

### The design part does not fit 180 seconds, and cannot be split further

One architect design call was run against `claude -p --model opus` with a payload the size a real run produces:

| model | wall | input tokens | output tokens | of which thinking | result |
| :--- | ---: | ---: | ---: | ---: | :--- |
| `opus` | 110s | 94,184 | 12,075 | 5,960 | valid, 5 workstreams, 8 contracts |
| `sonnet` | 282s | 94,188 | 40,940 | 34,287 | valid, 6 workstreams, 8 contracts |

The payload was 206 KiB: a 200 KiB repository graph and a six-story Product Manager draft. It carried no project
context, no resolved decisions and no revision block. A real cycle adds all three, and cycles two and three add the
previous 16 KiB draft on top.

So the cheapest possible design call already spends 61% of the budget on the strongest model, and the run has nothing
left for the case the stage is actually for. The part cannot be split again: the specialists wait for the design and
for nothing else, so a half-decided DAG is not a thing that can be handed on.

The sonnet row is the second finding. It is 2.6x slower than opus for the same answer, because it spends 34,287
thinking tokens where opus spends 5,960. Moving a heavy reasoning stage to a cheaper model makes it slower, not
faster.

### A run used one host even when three were signed in

`Router` resolved one adapter for MAIN and one for JUDGE and cached them. Every drafting avatar then shared one host's
rate limit and one host's queue, on a machine signed in to `claude`, `omp` and `agy` alike.

### The room could not tell a slow stage from a hung one

`contracts.EventPublicSummary` is handled in `stage.go`, but no adapter has ever emitted it: every adapter emits
`EventTextDelta` and `EventFinal` only. The text deltas reached the orchestrator and were discarded after setting a
`sawOutput` flag. So a card showed one sentence — "Part 1 of 3: mapping the stories onto module boundaries" — and then
nothing until the stage ended. At 180 seconds that was tolerable. At a real budget it is not.

## Decision

### A stage call gets its own budget, separate from every other call

`contracts.MaxGenerateTimeout` is split in two:

- `DefaultGenerateTimeout` stays 180 seconds and keeps its old meaning: the budget a caller gets when it asks for
  none. `internal/ingestion` and `internal/impact` now name it explicitly, so ADR 0004's rule — size a section against
  the clock — measures against the same number it always did.
- `MaxGenerateTimeout` becomes 30 minutes: the ceiling a caller may raise its own budget to.

The deliberation stages ask for `stage_timeout_seconds`, default 600, bounded to 60..1800. This is what ADR 0007
rejected, and the reason the rejection does not hold: the cap it defended is still there for the callers whose inputs
it sizes. Only the stage that was measured to need more asks for more, and it asks for a bounded, configurable
amount rather than inheriting the ceiling.

`docBatchItems` goes back to 20. It had been raised to 30, which lengthens each documentation call by half and moves
it toward the wall rather than away from it. Batch size was never the cause.

### Roles are placed across every host that can serve

`Router.PlaceRole` resolves a host and model per avatar role, not per slot. Each role has a rotation
(`roleHostOrder`) covering all three adapters, so an installation signed in to more than one spreads its avatars
across them: one host's rate limit slows one role instead of the whole run, and the critic reviews on a different
host than the architect drafted on. A role walks its rotation until a host is ready, so one signed-in host still
serves everything.

Two things override the rotation, in this order: a `role_models` entry for that role, and a configured `main_host` or
`judge_host`. An owner who names a host still gets that host for every role on its slot; the spread is what happens
when nothing is named.

A configured slot model is only sent to a host that lists it. `opus` is a Claude Code alias and means nothing to OMP;
sending it there is the 404 ADR 0004 traced. A host with no match uses its own default.

**`agy` cannot serve a generation today, and this ADR does not change that.** `AGY.Preflight` fails closed because the
CLI exposes no verified isolated-settings mechanism, so the tool-free guarantee cannot be proven for it. It is in
every rotation and is tried first for the critic and the narrator; it falls through with a recorded skip reason, and
it will join the spread with no further change the day its preflight can pass. Until then a three-host machine
spreads across two.

### Every avatar says where it runs and how far it has got

- `Router` emits `EventPlacement` before delegating, so the host and model are known before any output.
- `AvatarThoughtPayload` carries `host`, `model`, `step`, `context_note`, `elapsed_ms` and `output_bytes`. All are
  optional; a thought emitted before a host is resolved carries none of them.
- A running call reports progress every 10 seconds: elapsed, bytes written, and the top-level JSON key it is filling,
  read from the stream by `jsonCursor` — a depth counter that tracks strings and escapes. That is what turns a byte
  count into "now on contracts".
- `context_note` states the evidence slice the call can see ("graph 184 files · context 12 excerpts · drift 20 items ·
  201 KiB · 1 evidence cut"), so a reader can tell a gap that was cut from one that was never there.
- The room keeps the newest heartbeat on the card and keeps all of them out of the discussion.

### A narrator avatar says what the run is doing

`internal/avatars/narrator.go` reads the step records every avatar already emits and publishes one or two sentences,
at most once every 25 seconds and only when a step arrived since the last one. It reads steps, never drafts, so it
adds no boundary to audit. It never blocks a stage: it runs in its own goroutine, its failures are dropped, and it
publishes outside `o.thought` so it never enters `o.seen` and can never be counted as an avatar still working. The
room shows it as a banner above the faces, not as a fifth card, so it gates nothing. `disable_narrator` turns it off.

## Consequences

A stage that needs ten minutes now gets ten minutes, and the owner can see it working the whole time. A hung host
holds a run for at most the configured budget rather than the ceiling.

Startup now places every role, which costs at most one preflight per host. In exchange, a host that cannot serve is
reported before the window opens instead of mid-cycle, and `/api/status` carries both the placements and the skip
reasons.

The narrator adds one cheap call every 25 seconds while a cycle runs. On a ten-minute run that is about 24 calls on
the cheapest model the rotation can reach.

**Rejected: the Message Batches API.** It is the real batch endpoint and it is 50% cheaper, but it is asynchronous and
does not stream: a batched stage would go dark for minutes with no per-step status, which is the opposite of what this
ADR is for. Batching is worth revisiting for an offline run that nobody is watching; it is the wrong tool for the
avatar room.

**Rejected: moving the architect design onto a cheaper model to spread load.** Measured above: sonnet is 2.6x slower
than opus on that call. Load is spread by role, and the design keeps the strongest model the rotation can reach.
