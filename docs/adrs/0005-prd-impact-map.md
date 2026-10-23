# ADR 0005: Map the requirements onto the project, and let the model judge candidates it was shown

- Status: accepted
- Date: 2026-10-23

## Context

Page 1 showed each draft epic with a "Related code" table. It was built by substring matching:
`rankComponents` gave a public declaration three points when its name contained one of the epic's
keywords and one point when only its path did. The keywords came from `ast.KeywordsFromText` —
lowercase words of four or more letters, minus a 36-word stop list.

That works when the author writes code names. It fails exactly where this product claims its value.
A PRD saying *"a customer can stop paying at any time"* yields the keywords `customer`, `stop`,
`paying`, and matches nothing in a repository whose declaration is `BillingTerminate`. The page then
printed *"No existing component matches this epic yet. It may need new code."* — which a
non-technical reader takes as *this is all new work*, the most expensive wrong conclusion a plan can
start from.

Two further gaps. Nothing noticed that a requirement contradicted a decision already recorded in
`docs/adrs/`, even though `gather.Collect` already reads that directory. And the table listed
identifiers with no statement of what would happen to them, so a reader who does not read code
learned nothing from it.

### Relation to ADR 0003

ADR 0003 is titled "no text scanning in the composer" and this one scans the requirements against the
repository. They do not conflict. ADR 0003 removed a scan that *refused* the user's text on suspicion
of secrets. This stage reads the text to *explain* it, and blocks nothing.

## Decision

**The map is a sibling of the normalized PRD, never an input to it.**
`ingestion.Normalize` and `avatars.BuildPMInput` keep their signatures. The map is derived from the
*output* of the interpretation, plus the repository graph and the project context. The Product
Manager receives product fields only, as its doc comment has always promised and as README states as
a product guarantee: requirements stay separated from technical material, so product decisions are
not made by code-level accident. `TestPMInputHasExactlyTheProductFields` now asserts the nine keys by
name and fails with a comment pointing here, so the boundary cannot be reopened by accident.

**Retrieve, then judge. The model never writes a path.**
A deterministic pass assembles an evidence pack per epic: the top 20 public declarations by the
keyword ranking, up to 16 whole directories, and up to 6 documentation, specification and decision
excerpts. Each candidate gets an identifier (`c1`…`cN`), unique across the pass. The model is shown
those candidates and answers with their identifiers; `resolve` reads the path, the line and the
declaration back out of the candidate. An entry naming an identifier that was not offered is dropped
and counted.

The declarations give precision and the directories give recall. Requirements written in business
language match no declaration name — the failure this ADR exists to fix — so without the directories
the model could only choose among the parts the keywords happened to reach. The first live run
against this repository proved the point: with declarations alone it returned documents and test
functions; with the directories it named `internal/impact`, `internal/gather` and
`ProjectContext` in `internal/app/projectcontext.go`.

Test files are not candidates. A test is how a change is checked, not a part of the project a plan
changes, and the same live run had reported `TestBuildFindsAConflictWithADecisionRecord` as something
the work touches. `IsTestPath` excludes them from both the declarations and the directories.

A fabricated file path is therefore structurally impossible rather than merely unlikely, and
`provenance` on every link is the exact text the model saw. `TestBuildRefusesAPathTheModelInvented`
covers it; breaking `resolve` fails it.

Three further entries are refused: an entry with no candidate whose verdict is not `add` (a claim
about code the model was not shown), a `conflicts_with` aimed at a code candidate (code states no
claim a requirement can contradict), and any verdict outside the schema enum.

**Documents are gathered per epic, not reused from the whole-PRD context.**
`gather.Collect` is capped at 12 excerpts for the entire document. On a six-epic PRD that is two per
epic — too thin for the decision record that contradicts one of them to be in the candidate set at
all. Each pack runs its own gather, bounded to 6 excerpts and 40 across the pass. The gather is
deterministic, read-only and 5 s-bounded; six of them cost nothing beside one model call.

**One call per batch of packs, halved on a deadline.**
Packs are batched up to `MaxEvidenceChars`. ADR 0004's rule applies: the bound is the 180 s call
timeout, not the context window. A batch that returns `context.DeadlineExceeded` is halved and both
halves retried; a single pack that overruns is shrunk to half its candidates and retried once; a pack
that still overruns keeps its keyword links and says so. A cancelled run is never mistaken for a
deadline, so a user who keeps typing does not trigger a retry storm.

`MaxEvidenceChars` is 18,000, measured rather than guessed, against `claude-opus-5` on Claude Code
scanning this repository (269 files, 50 modules):

| Evidence sent | Wall clock | Share of the 180 s cap |
| ---: | ---: | ---: |
| 10,068 characters | 18 s | 10% |
| 10,888 characters | 31 s | 17% |
| 10,691 characters | 32 s | 18% |
| 17,083 characters (two packs in one call) | 45 s | 25% |

Normalization needs 8,000 for the same 180 s because it restates every requirement; this stage
returns at most eight short entries per epic, so the same input costs about a third of the time. The
measurement is repeatable: `TestLiveMeasureEvidenceBudget`, skipped unless `PLAN_FORGE_LIVE_IMPACT=1`.

**Cached by content, in the gitignored repository cache.**
`docs/plans/.plan/cache/impact.json`, beside the parse cache. The key is a SHA-256 over six things:
the interpretation (with its revision zeroed, so two sessions reaching the same interpretation share
one entry), the worktree hash, the gathered project context, the assembled evidence digest, the
prompt version and the extractor version.

The context hash is separate from the worktree hash because the scanner never reads `.agents`: an
edited lane note or `STATE.md` leaves the worktree hash untouched, and only hashing the context
notices it. `docs/` *is* scanned, so an edited decision record moves both.

**The repository is re-read before a map pass.**
`ast.Scan` ran once per session and `e.graph` was never replaced, so the worktree hash was frozen for
the life of the window and the map would have followed the code only across sessions. A pass now
re-scans when the last scan is over ten seconds old. A warm scan is an lstat per file — the parse
cache matches on size and mtime without reading the contents — so this is cheap, and the interval
only stops a typing burst from repeating it. No file watcher is needed and none is added.

**The keyword ranking stays, as the floor.**
`impact.Lexical` is published before the model call starts and whenever the model cannot answer. It
carries no verdict and no reason, because a name match cannot know either, and every link records
`source: "lexical"` so the window can say which it is showing. Page 1 is never empty and never
silently stale. `internal/app/planfiles.go` now reads the same map the panel does, so the rail and
the panel cannot disagree.

## Consequences

- One more MAIN-slot call per interpretation, debounced by the existing two-second normalization
  gate, skipped entirely on a cache hit, and cancelled outright when a newer interpretation starts.
- A new cache file in `docs/plans/.plan/cache/`, already covered by the `.plan/.gitignore` that
  directory has always had. `ast.CacheDir` is exported and now owns writing that `.gitignore`;
  `ast.writeCache` calls it.
- `contracts.RoleImpact` is a new role rather than a second `RoleIngestion` call, so a receipt names
  the prompt that actually ran.
- `GET /api/impact` joins `/api/context`, `/api/components` and `/api/plan-files`. No relay frame was
  added: the map is local, read-only, derived Page 1 data, and the envelope's event enum, the
  reducer, the replay log and the web validators all stay as they were.
- `StatusView` carries `impact`, `impactDetail` and `impactRevision`. The interpretation revision does
  not move when the model's map replaces the keyword ranking, so a window keyed on that alone would
  show the first paint forever.
- The plan-file Markdown changed: "Related code" became "What this touches" with a verdict and a
  reason per row, and contradicted decisions are listed separately. The empty state no longer claims
  new work before the model has read the project.

### Rejected: feed the map to the Product Manager

It is the boundary this project exists to hold. See the first decision above.

### Rejected: feed the full map forward to the architect and the specialists

Those stages already receive strictly more than the map holds — the full graph snapshot, the
documentation drift and the project context — so the map would be a lower-fidelity, model-authored
restatement of material they can read directly, and anchoring them on a first-pass guess would
compound one model's error into the plan. It also introduces a second source of truth: a frozen run's
input would depend on a cache entry whose worktree hash need not match the graph that run snapshotted.

The one thing the map produces that nothing else in the pipeline does is the `conflicts_with`
judgement against decision records. Carrying only those forward, as a short `known_conflicts` list on
the architect and critic payloads, is worth measuring later. It is deliberately out of scope here.

### Rejected: a file watcher

The ten-second warm rescan above covers it at a fraction of the complexity, and `fsnotify` appears
nowhere in this repository today.

### Rejected: embeddings

There is no vector store here. The deterministic pass already has BM25 through the recall index when
one exists, and the model supplies the semantic judgement the keyword ranking cannot. Adding an
embedding index would be a second retrieval system to keep fresh for a gain this measurement does not
show a need for.
