# ADR 0004: Size a normalization section by the call timeout, and report what the host said

- Status: accepted
- Date: 2026-10-23

## Context

Interpreting the shared document is the first step of every run: `ingestion.Normalize` turns the Markdown into the
product view, the technical appendix, the epics and the open ambiguities. Nothing else in a run happens until it
succeeds. Two unrelated defects made it fail on ordinary documents, and a third made both hard to tell apart.

### A section was sized against the context window, not the clock

`MaxSectionChars` was 60,000 characters. One section is one model call, and one model call must finish inside
`contracts.MaxGenerateTimeout` (180 seconds) or the host adapter kills the child process and returns
`context.DeadlineExceeded`. Every streamed byte is discarded.

Measured on `claude-opus-5-5-high` through OMP:

| document | characters | time | result |
| :--- | ---: | ---: | :--- |
| a short brief | 1,600 | 18s | ok |
| `COMMAND_CENTER_PLAN.md` (0.3.0) | 18,039 | 2m31s | ok |
| `GUI_BOARD_HANDOFF_PLAN.md` (0.3.0) | 26,979 | >3m00s | killed at the cap |

The splitter was willing to build a section more than three times larger than the largest one that finishes. Any real
PRD failed with `interpretation failed: normalize section 1 of 1: context deadline exceeded`.

The epic grouping added in 0.4.0 moved the line. Rule 6 of the normalizer prompt makes the model restate every
requirement and acceptance criterion a second time inside `epics`, roughly doubling the output. The same
26,979-character document normalized in 2m22s before that rule and ran past the cap after it.

### A host refusal read the same whatever went wrong

Every provider refusal became `Claude generation failed` or `OMP stream reported an error`. The provider's own text was
discarded. The commonest refusal names its own cause — a model identifier the host does not have answers
`There's an issue with the selected model (claude-opus-5-5-high)` with HTTP 404 — and the owner never saw it.

This mattered because `Router` falls back when the configured host cannot serve, and the model identifier comes from the
configuration either way. A configuration of `{"main_host": "agy", "default_main_model": "claude-opus-5-5-high"}` sends
an agy identifier to whichever host wins, and agy never wins: it fails `Preflight` by design, because it exposes no
verified isolated settings mechanism. Invoked from Claude Code the run died instantly on a 404 it would not explain;
invoked from OMP the same configuration worked.

## Decision

**Size a section by the clock.** `MaxSectionChars` is 8,000 characters, chosen from the measurements above with room for
a slower host and for the epic grouping.

**Treat a call timeout as evidence the section is too large.** A section that returns `context.DeadlineExceeded` is
replaced by its halves and retried, down to a `MinSectionChars` floor of 1,000. This is not the same work billed twice:
the retry is strictly smaller, which is the rule the avatar stages already follow when they refuse to retry after partial
output. A caller that cancels the run is never mistaken for a timeout.

**Carry the host's own failure text into the error.** `failureDetail` copies the provider message, stripped of control
characters and bounded to 400 characters, into both adapters' errors.

### Rejected: validate the model against the host's listing

The obvious fix for the mismatched identifier is to check the configured model against the resolved host's `Models()`
and drop it when it is absent. It was implemented and then backed out, because the premise is false: a CLI accepts
identifiers its listing command does not print. `omp models` lists `claude-opus-5-5` and `claude-opus-5-5@default`, and
OMP runs `claude-opus-5-5-high` anyway — measured, a 26,979-character document normalized through it. The guard would
have replaced a working model with OMP's default on the path the owner actually uses.

A name is not evidence. The error text is, so the owner gets told which identifier the host rejected and changes it in
`plan-forge config`.

## Consequences

- A long document makes more, smaller model calls, run one after another. The 26,979-character document above is 4
  sections and normalizes in 6m24s where it previously failed after 3m00s. Interpretation is debounced and runs in the
  background, but it is now noticeably longer on a large document; normalizing sections concurrently is the obvious next
  step and is not part of this change.
- Per-section results merge deterministically, as they already did for documents over the old threshold. The 4-section
  run produced 84 requirements and 18 epics.
- A model identifier that does not exist on the host that ran reports which identifier and which host, in the window and
  in the terminal.
- `TestNormalizeSplitsLongDocumentsAtTopLevelHeadings` expects 3 sections rather than 2. The document it builds is sized
  relative to `MaxSectionChars`, so the assertion no longer moves when the constant does.
