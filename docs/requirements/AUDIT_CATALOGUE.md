# Requirements catalogue audit

## Scope and method

- Subject: `docs/requirements/PLAN_FORGE_REQUIREMENTS.md`, 71 requirements, as it stood on 2026-10-19.
- Reviewer: an independent reviewer, separate from the authors of the catalogue.
- Date: 2026-10-19.
- Standard: the 18 quality characteristics of the `make-requirements-great` skill. The characteristics are cited by number below.
- Status: this file records the review as performed. The catalogue has since been corrected for some of these findings. See the catalogue's `## Review log` for what changed. Findings below are not re-opened or removed when a correction lands.
- Superseded 2026-10-22 by `docs/adrs/0001-remove-live-task-views.md`: the BOARD, COORD and GRAPH areas, REQ-UI-008, REQ-PERF-004 and REQ-PERF-005 were retired with the features they described. Findings on them stay below as the record and carry a superseded note.
- This is a record of findings, not a second catalogue. It does not restate requirement text. Read the catalogue for the statements.

### Characteristic key

| No. | Characteristic | No. | Characteristic |
| :--- | :--- | :--- | :--- |
| 1 | Unambiguous | 10 | Unique |
| 2 | Clear | 11 | Cohesive |
| 3 | Concise | 12 | Consistent |
| 4 | Correct | 13 | Conformant |
| 5 | Testable | 14 | Current |
| 6 | Implementation-independent | 15 | Modifiable |
| 7 | Owned | 16 | Traceable |
| 8 | Relevant | 17 | Categorised |
| 9 | Feasible | 18 | Complete |

### Method

1. Per-requirement pass: each requirement was read against characteristics 1 to 9 and 11 to 16, with the evidence quoted or located in the catalogue.
2. Set-level pass: uniqueness (10), consistency (12), conformance (13), currency (14), modifiability (15), traceability (16), categorisation (17) and completeness (18) were judged across the whole catalogue. Duplicate pairs were compared by verb and object, not by wording.
3. Glossary pass: every glossary term was checked for use, and every statement for terms used outside the glossary.
4. Completeness was probed with named techniques, listed per gap group in the set-level findings.

Owned (7) is clean per item because every requirement carries a named handle. A single owner across all 71 requirements, with no confirmation field, is a Correct (4) risk. It is not an evidenced defect and is not counted.

## Defect log by requirement

Each entry gives the failed characteristics by number and name, the evidence, and the rewrite direction. Requirements not listed had no per-item defect recorded. A letter in brackets after an ID is the requirement's level: (B) business, (S) stakeholder. Every other logged requirement is (F), solution level. Each was audited at its own level, so a business-level entry is not criticised for lacking a metric and a solution-level entry is not criticised for naming a component.

### Handoff

- **REQ-HANDOFF-001** (S). Failed: 12 Consistent, 4 Correct, 1 Unambiguous.
  - Evidence: it claims automatic plan mode for every host, but `REQ-HANDOFF-006` gives Antigravity a hint only and `REQ-HANDOFF-003` falls back to prefill. Acceptance covers only Claude and oh-my-pi. The actor is a third-party harness; Plan-Forge only injects a directive.
  - Direction: scope the claim to the hosts that support it and set the status to `partly met`.
- **REQ-HANDOFF-003**. Failed: 11 Cohesive.
  - Evidence: session open, seed and fallback sit in one statement.
  - Direction: split into one requirement per behaviour.
- **REQ-HANDOFF-004**. Failed: 11 Cohesive, 10 Unique.
  - Evidence: the 20-second bound repeats in `REQ-PERF-003`.
  - Direction: keep the bound in one place and cross-reference it.
- **REQ-HANDOFF-005**. Failed: 11 Cohesive, 10 Unique, 2 Clear.
  - Evidence: five instructions in one statement; it duplicates `REQ-SEC-005`; AST is undefined.
  - Direction: split, merge the duplicate with `REQ-SEC-005`, and expand or glossary-define AST.
- **REQ-HANDOFF-006**. Failed: 5 Testable.
  - Evidence: the negative "no automatic mode switch" has no check. It also conflicts with `REQ-HANDOFF-001`.
  - Direction: add a check that observes the absence of a switch, and resolve the conflict with `REQ-HANDOFF-001`.

### UI

- **REQ-UI-009** (B). Failed: 1 Unambiguous, 5 Testable, 4 Correct, 12 Consistent.
  - Evidence: "fluent, flexible and polished" and "suitable" are subjective. Acceptance says subjective judgement remains with the owner. `Status: met` contradicts its own acceptance and open question 6. The title says "Executive-grade", the statement "fluent, flexible, polished", the source "nicer".
  - Direction: restate as a measurable usability outcome with a pending target, and set the status to `partly met`.
- **REQ-UI-001**. Failed: 16 Traceable.
  - Evidence: Traces-to cites no contract. The Traceability row cites C8, which defines `/board` routing, not the three views.
  - Direction: cite the contract that defines the three views.
- **REQ-UI-002**. Failed: 11 Cohesive, 5 Testable.
  - Evidence: open-the-window and end-on-close are joined. Exit 130 is absent from the statement.
  - Direction: split, and put exit 130 in the statement if it is required.
- **REQ-UI-003**. Failed: 11 Cohesive, 5 Testable, 1 Unambiguous.
  - Evidence: the sidebar has no acceptance. "navigation and status" is vague.
  - Direction: split, name what the sidebar must show, and add acceptance for it.
- **REQ-UI-005**. Failed: 1 Unambiguous, 5 Testable, 16 Traceable.
  - Evidence: "non-essential motion" is undefined and wider than its source. Acceptance only checks that a CSS block exists. Traceability cites a test the acceptance does not.
  - Direction: define the motion covered, test behaviour rather than block presence, and align the two test citations.
- **REQ-UI-006**. Failed: 11 Cohesive, 1 Unambiguous, 5 Testable.
  - Evidence: no shortcut is enumerated. "`focusTrap.ts` is used by `Dialog.tsx`" is an implementation fact, not a test.
  - Direction: enumerate the shortcuts and test focus behaviour.
- **REQ-UI-007**. Failed: 1 Unambiguous, 5 Testable, 16 Traceable.
  - Evidence: "usable" and "defined responsive breakpoints" are undefined. A media query being present is not usability. Traceability cites `web/e2e/app.spec.ts`, which the acceptance does not.
  - Direction: state the widths and the usable outcome, and align the citations.
- **REQ-UI-008**. Failed: 11 Cohesive, 10 Unique.
  - Evidence: the coordinator-panel clause is a superset of `REQ-COORD-007`.
  - Direction: remove the clause or cross-reference `REQ-COORD-007`.
  - Superseded 2026-10-22 by `docs/adrs/0001-remove-live-task-views.md`: REQ-UI-008 was retired with the board and Graph views.

### Board

Superseded 2026-10-22 by `docs/adrs/0001-remove-live-task-views.md`: the plan board was removed and every REQ-BOARD requirement was retired.

- **REQ-BOARD-002**. Failed: 14 Current, 4 Correct, 12 Consistent, 10 Unique.
  - Evidence: it describes the pre-Wave-4 column rule, while C11 makes `column` the coordinator's live column. Its blocked rule differs from `REQ-COORD-003`, and it overlaps that requirement.
  - Direction: restate against C11 and reconcile with `REQ-COORD-003`.
- **REQ-BOARD-003**. Failed: 11 Cohesive, 5 Testable.
  - Evidence: acceptance says "limits" but the statement states none. Modes 0600 and 0700 and manifest-untouched have no named test.
  - Direction: state the limits, split, and name the tests.
- **REQ-BOARD-004**. Failed: 5 Testable, 10 Unique.
  - Evidence: "with no external dependency" is untested. The 4-second timer repeats in `REQ-PERF-004`.
  - Direction: add a test for the dependency claim and keep the timer in one place.
- **REQ-BOARD-005**. Failed: 10 Unique.
  - Evidence: it is a subset of `REQ-PERF-001`.
  - Direction: merge into `REQ-PERF-001` or cross-reference it.
- **REQ-BOARD-007**. Failed: 11 Cohesive, 1 Unambiguous, 5 Testable.
  - Evidence: "single-action POST" is unclear. Acceptance tests 200, 400, 404 and 409, which the statement omits.
  - Direction: define the action and put the response codes in the statement.

### Coordinator

Superseded 2026-10-22 by `docs/adrs/0001-remove-live-task-views.md`: the coordinator was removed and every REQ-COORD requirement was retired.

- **REQ-COORD-001**. Failed: 1 Unambiguous, 3 Concise.
  - Evidence: the verification-receipts caveat has an unclear antecedent.
  - Direction: name what the caveat qualifies, or cut it.
- **REQ-COORD-002**. Failed: 1 Unambiguous, 4 Correct, 13 Conformant.
  - Evidence: it makes the coordinator the recorder, while `Coordinate` is pure and the glossary gives the coordinator no recording role. Acceptance carries design notes.
  - Direction: name the actual recorder and move design notes out of acceptance.
- **REQ-COORD-005**. Failed: 1 Unambiguous.
  - Evidence: "then manifest order, then accepted discoveries" reads as either a third sort key or a bucket. Inherited from C9.
  - Direction: state the ordering as one explicit sort key list.
- **REQ-COORD-007**. Failed: 10 Unique, 5 Testable.
  - Evidence: a subset of `REQ-UI-008`. Acceptance cites a `.tsx` file.
  - Direction: keep one of the pair and cite a behavioural test.

### Graph

Superseded 2026-10-22 by `docs/adrs/0001-remove-live-task-views.md`: the Graph view was removed and every REQ-GRAPH requirement was retired.

- **REQ-GRAPH-002**. Failed: 11 Cohesive, 12 Consistent, 5 Testable, 10 Unique.
  - Evidence: "task node" is used of a discovery the glossary does not call a task. Acceptance tests caps and `ErrDiscoveryLimit`, absent from the statement. It overlaps `REQ-GRAPH-004`.
  - Direction: use glossary terms, state the caps, and resolve the overlap.
- **REQ-GRAPH-003**. Failed: 4 Correct, 5 Testable, 16 Traceable.
  - Evidence: "every repository path touched" is false; `.git/` and `docs/plans/.plan/` are never recorded. Acceptance hedges between two test files. Traceability omits `wave4.test.ts`.
  - Direction: state the exclusions, name the one test, and add the missing citation.
- **REQ-GRAPH-004**. Failed: 11 Cohesive, 1 Unambiguous, 5 Testable.
  - Evidence: "distinctly" is unquantified.
  - Direction: say how discoveries must differ and test it.
- **REQ-GRAPH-005**. Failed: 1 Unambiguous, 5 Testable, 10 Unique, 11 Cohesive.
  - Evidence: "stay responsive" has no number, and the only threshold measures layout compute. It duplicates `REQ-PERF-005`.
  - Direction: give an interaction latency number and merge with `REQ-PERF-005`.

### Gate

- **REQ-GATE-003**. Failed: 5 Testable, 11 Cohesive.
  - Evidence: only two bound fixtures exist. `ErrUnsupportedFormat` and three bounds are uncased. Bounds and unsupported formats are joined by "or".
  - Direction: split and add a case per bound and per error.
- **REQ-GATE-004**. Failed: 5 Testable, 12 Consistent.
  - Evidence: acceptance tests sentinel leakage only, not the fresh tool-free process, the private directory, or the absence of skills and file tools. `ProductPRD` and `NormalizedPRD` are used without a glossary mapping.
  - Direction: test each isolation property and map both names in the glossary.
- **REQ-GATE-010**. Failed: 4 Correct, 16 Traceable, 5 Testable, 2 Clear, 12 Consistent.
  - Evidence: it fixes a 9-hour workday, but `ScheduleOptions.HoursPerDay` defaults to 9 and `/plan-config` exposes it. "schedule and Gantt cases" names no case. PERT and Gantt are unexpanded. "agent schedule" differs from "Coding Agent".
  - Direction: state 9 as the default, name the cases, expand the acronyms and use the glossary term.

### Install

- **REQ-INSTALL-001**. Failed: 16 Traceable.
  - Evidence: acceptance cites a "Checkpoint 6 layout test in the plan", which has no path.
  - Direction: cite a file path.
- **REQ-INSTALL-002**. Failed: 11 Cohesive, 16 Traceable.
  - Evidence: three hosts and about twenty artifacts in one sentence, so one `partly met` hides which parts are met. The source "this machine" is not reproducible.
  - Direction: split per host so each carries its own status, and give a reproducible source.
- **REQ-INSTALL-003**. Failed: 1 Unambiguous, 10 Unique.
  - Evidence: "where supported" is undefined. It overlaps `REQ-INSTALL-007`.
  - Direction: list the supported cases and resolve the overlap.
- **REQ-INSTALL-005**. Failed: 1 Unambiguous, 5 Testable, 11 Cohesive.
  - Evidence: it refers to "the enumerated state files" but enumerates none. Acceptance cites an implementation file.
  - Direction: enumerate the files and cite a test.
- **REQ-INSTALL-006**. Failed: 11 Cohesive, 2 Clear.
  - Evidence: TTY is unexpanded. The title covers only `--dry-run`.
  - Direction: split, expand TTY and align the title.
- **REQ-INSTALL-007**. Failed: 12 Consistent, 4 Correct, 5 Testable, 16 Traceable.
  - Evidence: the premise about which roots are marked does not match `REQ-INSTALL-002`. The title is wider than the statement. Acceptance relies on one manual A/B check.
  - Direction: correct the premise, narrow the title and add an automated check.

### Release

- **REQ-RELEASE-001**. Failed: 12 Consistent, 1 Unambiguous, 11 Cohesive.
  - Evidence: the title says CI gates the release, but the two workflows trigger independently. "the web gate" is two different jobs.
  - Direction: state which workflow gates which, and name the jobs.
- **REQ-RELEASE-002**. Failed: 12 Consistent, 16 Traceable.
  - Evidence: the title says CI, the statement says release pipeline. Traces-to omits `release.yml`.
  - Direction: align the title and statement and add the trace.
- **REQ-RELEASE-003**. Failed: 5 Testable, 1 Unambiguous, 2 Clear.
  - Evidence: eight archives covers five client tarballs, one zip and two relay tarballs. Some phrases are undefined.
  - Direction: list each archive and define the phrases.
- **REQ-RELEASE-004**. Failed: 11 Cohesive, 5 Testable, 18 Complete.
  - Evidence: two actors. Acceptance tests none of TLS, checksum, atomic install or "latest" resolution. Nothing governs relay image publication.
  - Direction: split by actor, add tests, and add a relay image requirement.
- **REQ-RELEASE-005**. Failed: 5 Testable.
  - Evidence: acceptance restates the statement and gives no command.
  - Direction: give the command and the expected result.

### Security

- **REQ-SEC-001**. Failed: 5 Testable, 2 Clear.
  - Evidence: acceptance names no file, and no ledger test exists. The fixed notice text is never given.
  - Direction: give the notice text and a test.
- **REQ-SEC-002**. Failed: 5 Testable, 11 Cohesive, 1 Unambiguous.
  - Evidence: it tests the function, not that every path calls it. The subject is data, not an actor.
  - Direction: name the actor and test every call path.
- **REQ-SEC-003**. Failed: 11 Cohesive, 1 Unambiguous.
  - Evidence: five behaviours in one statement. The CSP is never stated.
  - Direction: split and state the CSP.
- **REQ-SEC-004**. Failed: 1 Unambiguous, 11 Cohesive, 15 Modifiable.
  - Evidence: "allowlisted" has no list. "rejecting or redacting" is ambiguous. Acceptance cites implementation line numbers, which are brittle.
  - Direction: give the list, choose reject or redact, and cite a test instead of lines.
- **REQ-SEC-005**. Failed: 11 Cohesive, 1 Unambiguous, 10 Unique, 12 Consistent.
  - Evidence: two actors. "permission-bypassing flags" is unenumerated. "the Claude adapter" is not a glossary term. It overlaps `REQ-HANDOFF-005`.
  - Direction: split by actor, list the flags, use glossary terms and merge the duplicate.

### Performance

- **REQ-PERF-001**. Failed: 11 Cohesive, 10 Unique.
  - Evidence: the bound overlaps `REQ-BOARD-005`.
  - Direction: keep the bound here and cross-reference from the board requirement.
- **REQ-PERF-002**. Failed: 12 Consistent.
  - Evidence: 120 s, 30 s, 20 s and 10 s appear with no precedence rule. "budget", "bound" and "timeout" drift.
  - Direction: add a precedence rule and use one term.
- **REQ-PERF-003**. Failed: 10 Unique, 11 Cohesive.
  - Evidence: it duplicates the 20-second bound of `REQ-HANDOFF-004`.
  - Direction: keep one statement of the bound.
- **REQ-PERF-004**. Failed: 1 Unambiguous, 5 Testable, 11 Cohesive, 10 Unique, 16 Traceable, 12 Consistent.
  - Evidence: "within about 1 second" is vague. Acceptance cites source constants, not a test. The 4-second timer repeats. Source, trace and verification all point at its own implementation. The title says push latency, the statement defines polling.
  - Direction: give a number, a test and an independent source, and align title and statement.
  - Superseded 2026-10-22 by `docs/adrs/0001-remove-live-task-views.md`: REQ-PERF-004 was retired with the board.
- **REQ-PERF-005**. Failed: 10 Unique, 1 Unambiguous.
  - Evidence: it duplicates `REQ-GRAPH-005`, and its bound is vague.
  - Direction: merge with `REQ-GRAPH-005` and state a number.
  - Superseded 2026-10-22 by `docs/adrs/0001-remove-live-task-views.md`: REQ-PERF-005 was retired with the Graph view.
- **REQ-PERF-006**. Failed: 11 Cohesive, 2 Clear, 18 Complete.
  - Evidence: latency, zero loss and zero duplicates are joined. "control events" and "document pushes" are undefined. No functional relay requirement exists.
  - Direction: split, define the terms and add a functional relay requirement.

### Compatibility

- **REQ-COMPAT-002**. Failed: 5 Testable, 10 Unique, 16 Traceable.
  - Evidence: acceptance cites the `go-server` job, which has no `CGO_ENABLED=0` build. It overlaps `REQ-RELEASE-002`.
  - Direction: cite a job that builds with `CGO_ENABLED=0` and resolve the overlap.
- **REQ-COMPAT-003**. Failed: 11 Cohesive, 1 Unambiguous, 16 Traceable.
  - Evidence: it joins a Node floor and a TypeScript pin with an unexplained condition.
  - Direction: split and explain the condition.
- **REQ-COMPAT-004**. Failed: 15 Modifiable, 16 Traceable.
  - Evidence: source, trace and acceptance are all `go.mod`. "lines 3 and 5" is brittle.
  - Direction: state the version floor and cite an independent source.

Requirements logged above: 54 of 71.

## Set-level findings

### 10 Unique

Ten duplicate or overlapping pairs, compared by verb and object:

| Pair | Overlap |
| :--- | :--- |
| `REQ-UI-008` / `REQ-COORD-007` | Coordinator-panel behaviour |
| `REQ-HANDOFF-004` / `REQ-PERF-003` | The 20-second bound |
| `REQ-HANDOFF-005` / `REQ-SEC-005` | Host permission instructions |
| `REQ-BOARD-004` / `REQ-PERF-004` | The 4-second timer |
| `REQ-BOARD-005` / `REQ-PERF-001` | Board bound |
| `REQ-GRAPH-005` / `REQ-PERF-005` | Graph responsiveness |
| `REQ-RELEASE-002` / `REQ-COMPAT-002` | Static build |
| `REQ-BOARD-002` / `REQ-COORD-003` | Blocked rule |
| `REQ-INSTALL-003` / `REQ-INSTALL-007` | Install behaviour |
| `REQ-GRAPH-002` / `REQ-GRAPH-004` | Discovery behaviour |

Superseded 2026-10-22 by `docs/adrs/0001-remove-live-task-views.md`: six of these pairs name a retired requirement and no longer apply. They are the pairs with `REQ-UI-008`, `REQ-BOARD-004`, `REQ-BOARD-005`, `REQ-GRAPH-005`, `REQ-BOARD-002` and `REQ-GRAPH-002`.

### 11 Cohesive

31 requirements are compound. The worst are `REQ-INSTALL-002`, `REQ-SEC-003`, `REQ-GATE-007`, `REQ-PERF-004`, `REQ-HANDOFF-005` and `REQ-RELEASE-001`. The catalogue states no purpose or scope, so the orphan test cannot run.

### 12 Consistent

- 12a: `REQ-INSTALL-007` contradicts `REQ-INSTALL-002` on which roots are marked.
- 12b: conflicts between `REQ-HANDOFF-001` and `REQ-HANDOFF-006`; between `REQ-BOARD-002`, `REQ-COORD-003` and C11; and between the category note that says bridge auth is "not restated here" and `REQ-SEC-003`, which does restate it.

### 13 Conformant

- The catalogue claims every requirement is a single sentence, but many are not singular in behaviour.
- The Category coverage table miscounts: Functional is 37, not 40; usability/accessibility is 3, not 2; Constraint is 7, not 6. The listed counts sum to 72 against 71 requirements.
- "Covered across all areas" is false for SEC, PERF and COMPAT.
- Per-item labels differ from the table's merged label.

### 14 Current

- Every requirement carries the same review date, with no reviewer name and no per-requirement change log.
- `REQ-BOARD-002` describes superseded behaviour.
- There is no "released" status distinct from "in tree".

### 15 Modifiable

- The Traceability table duplicates `Traces to` and `Acceptance`, and already diverges on fourteen requirements: `REQ-GATE-002`, `-007`, `-009`; `REQ-UI-001`, `-005`, `-007`; `REQ-BOARD-004`; `REQ-COORD-002`; `REQ-GRAPH-003`; `REQ-RELEASE-003`, `-004`; `REQ-SEC-004`; `REQ-HANDOFF-006`; `REQ-INSTALL-002`.
- `Traces to` mixes bare contract IDs with file paths.
- Line numbers appear in acceptance text.
- Only `REQ-COMPAT-002` cross-references another requirement by ID.

### 16 Traceable

- Eight sources say "User request this session". A session is not durable, and `.agents/` is untracked (decision D-1).
- Circular source, trace and verification in `REQ-PERF-004`, `REQ-COMPAT-003`, `REQ-COMPAT-004` and `REQ-INSTALL-002`.
- No test for `REQ-RELEASE-001`, `-003`, `-004`, `-005`, `REQ-COMPAT-001` to `-004`, or `REQ-UI-009`.

### 17 Categorised

- Regulatory is genuinely empty, with a reason given.
- Reliability, Scalability, Maintainability, Portability, Documentation and Privacy are neither listed nor dismissed, though the content exists under other labels.
- Interface is overlooked rather than thin.
- Usability is genuinely thin at 3.

### 18 Complete

Seven gap groups, each with the technique that found it:

- (a) NFRs with no functional parent: the relay, the hook, `open`, `summary` and `verify`.
- (b) No requirement for `/plan-forge` invocation or for `/plan-config`.
- (c) The board's launch behaviour is missing: exit 3 without an approved plan, and Antigravity refused with exit 2. Superseded 2026-10-22 by `docs/adrs/0001-remove-live-task-views.md`: the board was removed.
- (d) CRUD gaps: note removal, priority lowering, the stated limits, the `409 plan changed` response, and overlay clean-up on re-approval. Superseded 2026-10-22 by `docs/adrs/0001-remove-live-task-views.md`: the board and its overlay were removed.
- (e) Lifecycle gaps: no resume or cancel after the deliberation pause, no re-approval or revocation, and no browser-absent or host-failure error states.
- (f) Not captured from project decision memory: the Jira-style design language, the distinct Critic model slot, host and model discovery, the Page 3 dual-baseline display, the README and Mermaid documentation requirement, the Cloud Run deploy job, and the relay image platforms and GHCR.
- (g) Completeness is not provable.

## Glossary integrity

All 16 terms appear in at least one statement, so there are no dead terms. Violations that remain:

- Bare "plan" for the plan manifest or the synthesized spec: `REQ-GATE-001`, `REQ-HANDOFF-001`, `-004`, `REQ-BOARD-001`, `-005`, `REQ-GRAPH-001`.
- Bare "manifest", ambiguous with `install-manifest.json`: `REQ-BOARD-003`, `REQ-SEC-001`, `REQ-COORD-005`.
- "the spec" and "master specification" for the same artefact.
- "Gemini" for Antigravity.
- Bare "Claude" for Claude Code: `REQ-COORD-002`, `REQ-INSTALL-007`, `REQ-SEC-005`.
- "window", "the UI", "GUI", "interface", "client", "binary" and "product" used for one thing.
- "graph" meaning both the AST graph and the task-graph view.
- "bridge" meaning both the loopback socket and the board API.
- "synthesizer" meaning both the Lead Synthesizer avatar and the compiler.
- "human" used in two senses.
- Terms used but undefined: avatar, Red-Team Critic, Technical Architect, Domain Specialists, Lead Synthesizer, decision cards, consumer, cursor, delta, live column, evidence, drifted, relay, managed, AST, PERT, DAG, TTY, PRD, CSP.
- Four glossary entries copy requirement text, creating a second source of truth: Product Manager isolation, overlay, coordinator, discovery.
- Minor drift: "Up next" versus "Up-next"; "awaiting-verification" versus "awaiting verification".
- Superseded 2026-10-22 by `docs/adrs/0001-remove-live-task-views.md`: the terms board, overlay, coordinator, discovery, consumer, cursor, delta and live column left the catalogue with their requirements, so the findings on them no longer apply.

## Three highest-impact defects

1. **`REQ-UI-009`** is marked `met` while its own acceptance defers to the owner's subjective judgement and open question 6 is unresolved. A shipped-status claim rests on an untestable statement.
2. **`REQ-HANDOFF-001`** promises automatic plan mode for every host. `REQ-HANDOFF-006` and `REQ-HANDOFF-003` show that Antigravity gets a hint and other paths fall back to prefill. The headline behaviour of the product is over-claimed and internally contradicted.
3. **Compound requirements and copied traceability** (11 and 15/16). 31 requirements bundle several behaviours, so a single status such as `partly met` on `REQ-INSTALL-002` hides which parts are met, while the Traceability table already diverges from the per-requirement fields on fourteen requirements.

## Verdict

**Yellow.** Counts are requirements affected for per-item characteristics, and findings for set-level characteristics.

| No. | Characteristic | Count |
| :--- | :--- | :--- |
| 1 | Unambiguous | 27 |
| 2 | Clear | 6 |
| 3 | Concise | 1 |
| 4 | Correct | 7 |
| 5 | Testable | 30 |
| 6 | Implementation-independent | 0 |
| 7 | Owned | 0 |
| 8 | Relevant | 0 |
| 9 | Feasible | 0 |
| 10 | Unique | 10 pairs |
| 11 | Cohesive | 31 |
| 12 | Consistent | 11 |
| 13 | Conformant | 3 |
| 14 | Current | 2 |
| 15 | Modifiable | 4 |
| 16 | Traceable | about 14, plus the set-level items |
| 17 | Categorised | 3 miscounts, plus 6 undeclared categories |
| 18 | Complete | 7 gap groups |

Owned is 0 per item because every requirement carries a named handle. The single owner for all 71 requirements, with no confirmation field, is a Correct (4) risk, not an evidenced defect.
