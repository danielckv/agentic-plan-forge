# ADR 0016: Bound ingestion, impact, narrator, workstreams and contract claims for a five-minute budget

- Status: accepted
- Date: 2026-10-27

## Context

A local session on a 650-file repository (`SmartCompanyAI`, run `29146c1c-7e6e-4c91-910e-e99aee5be6dc`)
spent 1h 16m 31s of provider time across 93 calls and then paused on a false-positive decision card
before the critic barrier could run:

| Role | Calls | Input tokens | Output tokens | Provider time |
| :--- | ---: | ---: | ---: | ---: |
| `specialist` | 10 | 229.7k | 107.7k | 32m 40s |
| `impact` | 33 | 120.3k | 6.2k | 16m 29s |
| `narrator` | 32 | 99.5k | 3.6k | 11m 45s |
| `architect` | 5 | 10 | 67.6k | 9m 18s |
| `ingestion` | 11 | 22 | 25.9k | 4m 09s |
| `critic` | 1 | 112.7k | 10.3k | 1m 40s |
| `pm` | 1 | 2 | 3.2k | 30.3s |

Four mechanisms caused the overrun:

- **Every ambiguity chip click re-ran the normalizer and the impact model.** `ingestion.Normalize`
  never sends `resolutions` to the model: `normalizeSection` sends only the Markdown section, and
  `finalizeNormalized` applies `resolutions` deterministically in memory. However, `executor.interpret`
  keyed its cache on `resolutionKey(text, rs)` (`text + rs`). Resolving 9 ambiguity chips on Page 1
  moved `PRDRevision` from 2 to 11, firing 10 redundant `ingestion` calls (4m 09s) and 33 `impact`
  calls (16m 29s, 3 to 8 sequential epic batches per click).
- **Ten-second heartbeats kept the narrator firing every 25 seconds.** `attempt` in `stage.go` emits a
  `"Running for Ns..."` thought every 10 seconds. `thoughtOfStep` passed every heartbeat into
  `o.tell.record`, which set `n.fresh = true`. The narrator ticker then spawned an `omp` subprocess
  every 25 seconds throughout the run (32 calls, 11m 45s).
- **Unbounded workstream fan-out plus an advisory in-stage critic.** The architect declared 8
  workstreams (`ws-dates`, `ws-search-cache`, `ws-perf-backend`, `ws-chat-latency`, `ws-explorer-api`,
  `ws-insights`, `ws-notifications`, `ws-frontend`). With `DefaultMaxParallel = 3`, 8 specialists and 2
  repair retries plus the advisory `earlyCritic` call ran across 4 serial waves (32m 40s, 107.7k
  output tokens).
- **Contract signature restatement opened a false-positive decision card.** On contract
  `search-suggest-api`, both `ws-frontend` and `ws-search-cache` echoed the exact `version_hash`
  (`d1447bd9...`). `ws-frontend` copied `k.InputSchema` verbatim, while `ws-search-cache` prepended
  `k.Signature + ". "` to `k.InputSchema`. `CompareInterfaces` required byte-for-byte string equality
  between claims even when both matched the architect's `version_hash` and contained the architect's
  `input_schema`, pausing the run on `dec-iface-search-suggest-api-c1-1`.

## Decision

**Cache raw normalization by document text and apply ambiguity resolutions in memory.**
`ingestion.ApplyResolutions(base, resolutions)` applies owner ambiguity choices to the raw
`contracts.NormalizedPRD` without calling the model. `executor.interpret` caches `normBase` by
`normText`; when only `resolutions` change, it calls `ingestion.ApplyResolutions` in memory and
triggers immediately without the 2-second edit debounce.

**Use the deterministic lexical impact map by default on Page 1; run the impact model on explicit
`Re-interpret`.** Background document edits and ambiguity resolutions call `refreshLexicalGrounding`,
which updates the repository scan, project context and `impact.Lexical` map with zero provider calls.
Clicking **Re-interpret** (`POST /api/reinterpret`) sets `modelImpact = true` and runs
`refreshGrounding` (`impact.Build`).

**Cap workstreams at 4, raise `DefaultMaxParallel` to 4, drop `earlyCritic`, and disable the narrator
by default.**

- `synthesizer.MaxWorkstreams = 4` is enforced in `designProblems` and stated in
  `ArchitectDesignSystemPrompt`, so all workstreams execute in a single parallel wave.
- `config.DefaultMaxParallel` is raised from 3 to 4 (matching `HardMaxParallel = 4`), and
  `config.Default()` sets `DisableNarrator: true`.
- `runSpecialists` no longer launches `earlyCritic`; only the mandatory `criticBarrier` runs once the
  specialists and interface decisions finish.
- `thoughtOfStep` skips `o.tell.record` for `"Running for "` heartbeat lines unless a test sets a
  custom `NarrateEvery`, so enabling `disable_narrator: false` in `config.json` still narrates only
  real step transitions.

**Auto-align specialist contract claims that match `version_hash` and restate or omit the architect's
schema.** `synthesizer.AlignSpecialistClaims` checks each `implements` and `consumes` claim whose
`version_hash` equals `ContractHash(k)`. When `cl.InputSchema` or `cl.OutputSchema` is blank, copies
`k.Signature`, or contains `k.InputSchema` / `k.OutputSchema` after whitespace and case normalization,
it snaps the field to the architect's schema. Genuine alternative schemas (such as `"application/json"`
against `"text/csv; header=present"`) do not contain the architect's schema and still open a human
decision card.

**Map OMP `modelRoles` to Plan Forge avatar roles and index `provider/id` selectors.** When an avatar
lands on `omp` with no explicit `role_models` or matching slot model in `config.json`, `OMP.RoleModel`
reads `modelRoles` from `~/.omp/agent/config.yml` and `.omp/config.yml` (skipping entries whose
provider is in `disabledProviders`) instead of sending every call to `modelRoles.default`. `architect`
uses `plan`, `critic` uses `judge`, `pm` uses `advisor`, `ingestion`/`impact`/`narrator` use `smol`,
and concurrent `specialist` lanes rotate across `task`, `worker`, `slow` and `explore`. `parseModelRefs`
filters `"kind": "chat"` and indexes both `id` and `selector` (`provider/id`).

## Consequences

Resolving N ambiguity chips on Page 1 now makes 0 `ingestion` calls and 0 `impact` calls instead of N
`ingestion` calls and 3N to 8N `impact` calls.

A single-cycle deliberation run makes 1 `pm` call, 1 `architect` design call, at most 4 `specialist`
calls in one parallel wave (overlapping the architect's diagram and documentation calls on the 4-slot
semaphore), 1 `critic` barrier call, and 0 `narrator` calls by default.

Specialists that echo the frozen `version_hash` and prepend `k.Signature` to `input_schema` or
`output_schema` converge without stalling on a false-positive `DECISION_PROMPT`.

Verified by `TestApplyResolutionsReusesBaseWithoutModelCalls`,
`TestAmbiguityResolutionsReuseNormalizedBaseWithoutModelCalls`,
`TestBackgroundInterpretPublishesLexicalImpactWhileReinterpretRunsModel`,
`TestArchitectValidateDesignCapsWorkstreamsAtFour`,
`TestCompareInterfacesAutoAlignsRestatedSchemaWithMatchingVersionHash`,
`TestSpecialistClaimPrependingSignatureAutoAlignsWithoutDecisionPrompt`,
`TestOMPParseModelRefsFiltersChatAndIndexesSelector`, and
`TestOMPModelRolesMapsRolesAndRotatesSpecialists`.
