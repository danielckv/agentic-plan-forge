# ADR 0021: Independent sections and batches run together

- Status: accepted
- Date: 2026-10-29
- Follows [ADR 0004](0004-section-size-and-host-failure-detail.md) and
  [ADR 0016](0016-five-minute-deliberation-budget.md)

## Context

A long document was already split well. `splitSections` cuts at top-level headings into sections
of at most `MaxSectionChars` (8,000), `halveSection` replaces a section that outran the call
timeout with its halves down to a 1,000-character floor, and `mergeWire` folds the results back in
document order. The attachment limits above that are set too: 25 MB from the UI, 50 MB from the
command line, 500 PDF pages, OCR for scanned pages.

What the split did not do was overlap. `Normalize` ran one section at a time:

```go
for i := 0; i < len(sections); i++ {
    w, err := normalizeSection(ctx, gen, sections[i], i+1, len(sections))
    ...
}
```

Eleven sections at roughly 20s each is about 3m40s of wall clock for ingestion alone, against a
five-minute budget for the whole deliberation (ADR 0016). `runBatches` in `internal/impact` had
the same shape: one call per batch, in sequence, while `config.DefaultMaxParallel` is 4 and the
Domain Specialists already ask one host to carry four concurrent calls.

The sections are independent by construction — each is classified on its own and merged by index
— and so are the impact batches, each covering its own epics.

## Decision

Both loops dispatch concurrently, bounded at 4, and fold their results back in index order.

`normalizeAll` replaces the sequential loop. It runs the pending sections through a semaphore of
`maxSectionParallel`, writes each result into `parts` at the section's own index, and then handles
failures in one pass: the earliest section that failed for a reason halving cannot fix is
reported, and every section that outran the call timeout is replaced by its halves, spliced from
the back so earlier indices stay valid. A section that already answered is never called again.

`runBatches` keeps each batch's answer in a `batchResult` at the batch's index and folds the
links and notes afterwards, in batch order.

Order is preserved everywhere it is observed: the merged product lists, the `section N of M` tag
on each ambiguity, the source refs, and the impact notes all read the same whatever order the
hosts answer in. Only the order the requests *arrive* at the host changed, which nothing depends
on.

## Consequences

Ingestion of an 11-section document goes from 11 serial calls to 3 waves of 4. On the 20s-per-call
figure above that is about 1m from about 3m40s. The saving scales with the document: a 500-page
PDF that splits into 125 sections drops from roughly 42 minutes to roughly 10.

Four concurrent calls land on one host, because an avatar is placed once (ADR 0015) and every
section of one document is the same avatar. That is the load the specialists already place.

Two test stubs were unsafe under concurrent calls and had to be guarded. `timeoutGen` lost
`append`s to a data race, which the test then read as the normalizer dropping 2,000 characters of
the document — a false accusation produced by the test, not the code. Any future stub in these
packages must be safe for concurrent calls.

The concurrency test sets its barrier to a literal 2 rather than to `maxSectionParallel`. An
expectation that tracked the constant passed against a one-at-a-time dispatcher as soon as the
constant was 1, which is the failure it exists to catch.

No cap was added on the number of sections. A 50 MB document is bounded by the file limit, not by
a call count, and capping calls would silently drop part of a document the owner supplied.
