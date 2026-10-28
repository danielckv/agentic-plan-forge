# ADR 0019: A malformed reply costs one repair, not the run

- Status: accepted
- Date: 2026-10-29
- Follows [ADR 0012](0012-a-broken-host-costs-one-retry-not-the-run.md), which gave the same
  treatment to a broken host

## Context

A real run died on this:

```text
normalize section 5 of 11: validate normalized_prd.v1 host output:
INVALID_PAYLOAD: schema normalized_prd.v1: decode instance:
invalid character 'A' after object key:value pair
```

Eleven sections, ten of them classified, and the run ended because one reply was not clean JSON.

Both model-calling loops that ask for a schema already carry a repair retry.
`internal/ingestion/normalizer.go` says so in its own comment — "one schema-repair retry is made
before the call fails" — and `internal/impact/model.go` has the same loop. Neither could ever run
it.

The reason is an ordering the two files could not see. `finalizeGeneration` in
`internal/host/stream.go` validates the terminal result against `req.SchemaID` inside the adapter
and returns a plain `error`. The caller's loop reads:

```go
res, err := gen.GenerateStream(ctx, ...)
if err != nil {
    return wireNormalized{}, err   // every schema failure leaves here
}
w, verr := decodeWire(res.Content) // only reached when the host already validated
```

So `decodeWire` only ever saw output the host had just validated, and the repair branch below it
was unreachable. The package tests did not catch it because their stub generator returned canned
content without validating it — the one path no real adapter takes.

A schema failure is also not an `ErrCapability`, so `Router.GenerateStream` does not demote the
host and retry elsewhere either. One malformed reply ended the run with nowhere to go.

The output itself was discarded. The error names a character position in text nobody can read
back, which is why the failure above cannot be diagnosed after the fact.

## Decision

Three changes, smallest first.

**The host unwraps a wrapped reply before validating.** `jsonCandidate` returns the first complete
JSON value in the output when the output is not already a single JSON value: a markdown fence, a
sentence before the object, a note after it. It decodes with `encoding/json`, so a brace inside a
string does not end the value. Output holding no JSON value is passed through unchanged, so the
schema error still quotes what the host actually said. This costs no model call.

**A schema failure is a typed, recoverable error.** `contracts.ErrSchemaInvalid` carries the
schema id, the output and the cause. Its message quotes the output, cut to 400 bytes on a rune
boundary.

**Both loops repair instead of returning.** `contracts.RepairEcho` reports whether an error is a
schema failure and returns the complaint and the output to quote back. The normalizer and the
impact map feed those into the repair request they already build, and take their second attempt.

The stubs in both packages now validate the schema the request names and answer with
`ErrSchemaInvalid`, so the tests exercise the path a real adapter takes. Before this change the two
existing repair tests fail: `TestNormalizeRepairsInvalidOutputOnce` reports the original error, and
`TestNormalizeFailsAfterOneFailedRepair` counts one model call where it wants two.

## Consequences

A section whose reply is wrapped in prose or a fence now costs zero extra calls. A section whose
reply is malformed costs one. A section that fails twice still ends the run, and now says what the
model wrote.

The repair is bounded at one attempt, unchanged. Nothing retries on another host: a model that
cannot produce the object will not produce it elsewhere, and demoting a host for a bad reply would
spend three hosts on one section.

`jsonCandidate` can pick the wrong object if the prose before the real payload contains a complete
JSON value of its own. The schema then rejects it and the repair attempt runs, which is the same
cost as the reply having been malformed.
