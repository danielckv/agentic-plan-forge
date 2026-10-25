# Cloud mode: plan a GitHub repository from an RFC pull request

Status: design, not built. Decisions below were taken on 2026-10-25; everything else is open.

## What it is

A hosted service Plan Forge operates. Somebody opens a pull request containing an RFC. The service
reads the RFC, clones and reads the repository, runs the same four-avatar deliberation the desktop
product runs, and answers on the pull request with a verified specification.

The desktop product is unchanged. This is a second way to reach the same engine.

## Decisions taken

| Question | Answer |
| :--- | :--- |
| Where it runs | A hosted service we operate: a new binary, not the relay |
| How avatars reach a model | A provider API key, through a new adapter |
| Where the intent comes from | A pull request carrying an RFC |
| Relation to research batching | Separate modes; batching stays a local detached job |

## Why this is smaller than it looks

The deliberation engine is already a library with no host in it. Measured, not assumed:

```text
go list -tags client -deps ./internal/avatars | grep agentic-plan-forge
  internal/contracts  internal/ast  internal/gather  internal/config
  internal/relay      internal/sanitize             internal/synthesizer
```

`internal/host` is absent. `avatars.Run` takes a `contracts.Generator` by injection
(`internal/avatars/orchestrator.go:41`), so anything that satisfies one interface can drive the whole
deliberation. `internal/synthesizer` is purer still: contracts and sanitize, no filesystem, no model.

So cloud mode does not re-implement the pipeline. It supplies four things the desktop binary supplies
differently: a repository, a brief, a generator, and somewhere to put the answer.

| Desktop | Cloud |
| :--- | :--- |
| a local git worktree | a clone at the pull request's head |
| the Markdown composer | the RFC in the pull request |
| a local agent CLI subprocess | a provider API call |
| the browser, then `docs/plans/.plan/` | a comment, a check run, or a commit on the branch |

### Reused unchanged

`internal/ast` (scan, snapshot, drift), `internal/gather` (the repository's own ADRs, specs and notes),
`internal/ingestion` (the RFC is Markdown, which `Normalize` already takes), `internal/impact`,
`internal/avatars`, `internal/synthesizer`, `internal/contracts`.

### Not reused

`internal/window` (a browser), `internal/app` (session composition and the loopback bridge),
`internal/relay`'s client side, and the three CLI adapters in `internal/host`.

### Build tags need no surgery

`ast`, `gather`, `ingestion`, `impact` and `avatars` carry `//go:build client && !server`. A new
`cmd/cloud` built with `-tags client` compiles against all of them, because it imports neither
`internal/app` nor `web`. The relay's guard is untouched: CI asserts `cmd/relay` links none of this
(`.github/workflows/ci.yml:128`), and `cmd/cloud` is a third binary, not the relay.

The `client` tag now means "the analysis side" rather than "the desktop app". That is a misleading
name, not a blocker; renaming it is a separate change.

## The flow

```text
pull request opened or labelled
  -> GitHub App webhook
  -> mint an installation token
  -> clone at the PR head, shallow, into a per-run sandbox
  -> ingestion.Normalize(the RFC)            -> NormalizedPRD
  -> ast.Scan(clone)                         -> graph + documentation drift
  -> gather.Collect(clone)                   -> the repo's own ADRs, specs, notes
  -> impact.Build                            -> what the RFC touches
  -> avatars.Run + avatars.Synthesize        -> the deliberated design
  -> synthesizer.Compile                     -> spec Markdown + manifest
  -> answer on the pull request
```

Open: which of the three answers to write — a comment, a check run with the spec as its summary, or a
commit of `docs/plans/.plan/synthesized_spec.md` onto the PR branch. A check run is the most native and
the least noisy; a commit is the most useful to the next agent that picks the work up.

## What has to be built

1. **`cmd/cloud`** — the service: webhook receiver, job queue, per-run sandbox, result writer.
2. **`internal/host`: an API adapter** — `Kind() = "api"`, a `contracts.Generator` over HTTPS. The key
   comes from the environment or a secret manager, never from `config.json`, and never from a user's
   repository. The three CLI adapters stay exactly as they are.
3. **GitHub App** — installation tokens (the original build plan already sketched a broker), clone with
   a short-lived token in an `http.extraHeader`, and the permissions to read a repo and write a check
   run or a comment.
4. **Trigger and RFC discovery** — which pull requests qualify, and where the RFC is: the PR body, a
   changed file under a configured directory, or a label.
5. **Tenancy and auth** — who may install the App, which repositories a run may read, and how a run is
   attributed and billed.

## Three honest problems

**It breaks a documented promise.** The README states Plan Forge "never asks for or stores provider API
keys: it uses the credentials your host agent already has." A hosted service cannot keep that. The
sentence has to be rewritten to scope it to the desktop product, in the same change that adds the
adapter, with an ADR recording why. Leaving the old sentence standing while the service holds keys
would be the worst outcome.

**It reads untrusted repositories.** The mitigation is already structural and is worth stating plainly:
Plan Forge parses repository code, it never executes it. `internal/ast` is tree-sitter over bytes, git
access is an allowlist (`internal/ast/gitcmd.go:31`), symlinks are refused, and there is no build step,
no dependency install and no test run anywhere in the pipeline. That removes the whole class of
"malicious repo runs code on our box".

What remains is resource exhaustion and hostile input: a repository far larger than any `MaxFileBytes`
bound anticipated, a file that makes a grammar pathological, a documentation tree that produces the
3.1 MB drift list ADR 0006 already found once. Every bound in `internal/ingestion/bounds.go` and
`internal/ast` was sized for a developer's own repository, where the adversary is absent. They need
re-reading as a service's bounds, with a wall-clock and memory ceiling per run.

Also: `gather` opens a SQLite recall index when one exists. On a fresh clone it does not exist and the
file reader runs instead, which is the correct behaviour — but the service must never open a database
that arrived inside a cloned repository.

**A PR's head is not the main branch.** Planning against the PR head plans against the proposal's own
changes; planning against the base plans against what exists. For an RFC that adds only a document
these are nearly the same tree, but "nearly" needs a decision, and the answer should be recorded in the
manifest either way.

## Not in this

Research batching stays a separate, local, detached mode, as decided. The two share the impulse — move
slow work off the interactive path — and nothing else. Cloud mode does not host batches.

## Next

This is a design, not an approved plan. Before any code: decide the trigger and the answer surface,
re-read every bound as a service bound, and write the ADR for the credentials change. The engine reuse
above is verified; the service around it is not yet specified.
