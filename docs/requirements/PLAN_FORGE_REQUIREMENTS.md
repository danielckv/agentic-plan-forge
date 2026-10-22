# Plan-Forge requirements catalogue

Catalogue standard: every requirement is a single sentence of the form `{Actor/subsystem} shall {behaviour}`. IDs are `REQ-<AREA>-<nnn>`, stable, three-digit, and never renumbered. Levels: `B` business, `S` stakeholder, `F` solution/functional; a requirement is reviewed at the level it is written, so a `B` or `S` requirement is not defective for lacking solution-level numbers. Owner is `@danielckv` for every requirement (the CODEOWNERS owner); no other owner is invented. `Last reviewed` is the date the requirement was checked against the tree. Status is judged against the code in the tree on that date: `met`, `partly met`, or `not met`; every `partly met` or `not met` requirement also appears in Open questions. Glossary terms are used exactly as defined in the Glossary throughout. Contract references `C1`–`C12` cite the design contracts in `docs/plans/GUI_BOARD_HANDOFF_PLAN.md`, and `C13`–`C21` cite `docs/plans/COMMAND_CENTER_PLAN.md`. Both plans left the tree on 2026-10-22 with the features they designed (`docs/adrs/0001-remove-live-task-views.md`). They stay readable in git history at commit `d4cbb43`. The requirements audit of the master plan lives beside this catalogue at `docs/requirements/AUDIT_MASTER_PLAN.md`.

## Purpose and scope

Purpose: this catalogue is the normative inventory of what Plan-Forge shall do. Every behaviour the product owes a user, a host or a release is either stated here as a requirement or consciously dismissed in Category coverage or Open questions, so the orphan test — a plan item found nowhere in this catalogue — is decidable by construction. In scope: the `plan-forge` client and its embedded window, the pre-planning context gatherer, the plan-mode handoff and validation, installation and uninstall, release gates and artifacts, the untrusted-input posture, performance budgets, and platform and toolchain constraints, in both Solo and Remote collaboration modes. Out of scope: the relay wire protocol, the verbatim deliberation prompts, and visual design detail, which are design contracts recorded in `docs/plans/`, not product requirements. Removed features are listed under Retired requirements; their IDs are never reused.

## GATE — pre-planning gate

### REQ-GATE-001 — Gate before implementation

- ID: REQ-GATE-001
- Level: B
- Statement: The product shall provide a plan forge gate that turns a raw brief into an approved, implementation-ready plan before any code is written.
- Category: Business
- Owner: @danielckv
- Source: User request this session (2026-10-19); `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Context.
- Acceptance criteria: `web/e2e/bridge.spec.ts` drives the full flow from Page 1 input to approval and artifact write.
- Traces to: `internal/app/`, `web/src/pages/`
- Status: met
- Last reviewed: 2026-10-19

### REQ-GATE-002 — Ingestion to typed PRD

- ID: REQ-GATE-002
- Level: F
- Statement: The plan forge gate shall ingest `.docx`, `.pdf` and `.md` uploads plus free composer text and normalize them into a typed `NormalizedPRD` before deliberation.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 2 (Document parsers) and Step 3b (stage 1).
- Acceptance criteria: `internal/ingestion/ingest_test.go`, `internal/ingestion/normalizer_test.go`, `internal/ingestion/fixtures_test.go`.
- Traces to: `internal/ingestion/ingest.go`, `internal/ingestion/normalizer.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-GATE-003 — Ingestion bounds

- ID: REQ-GATE-003
- Level: F
- Statement: The ingestion router shall reject inputs outside the stated bounds (25 MiB UI and 50 MiB CLI per file, 500 PDF pages, 10,000 DOCX ZIP entries, 100:1 compression ratio) with typed `ErrBound` or `ErrUnsupportedFormat` errors.
- Category: Constraint
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 2 (Bounds).
- Acceptance criteria: `internal/ingestion/fixtures_test.go` (`zip-bomb.docx`, `oversized.pdf` yield `ErrBound`).
- Traces to: `internal/ingestion/bounds.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-GATE-004 — Product Manager isolation

- ID: REQ-GATE-004
- Level: F
- Statement: The plan forge gate shall run the Product Manager Avatar in Product Manager isolation — a fresh tool-free process in a private empty directory receiving only `BuildPMInput(ProductPRD)`, with no repository graph, paths, environment, conversation history, skills or file tools.
- Category: Constraint
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 3b (stage 1; exact mandatory role preambles).
- Acceptance criteria: `internal/avatars/avatars_test.go` and `internal/avatars/prompts_test.go` seed sentinel strings into graph/files/env and assert none appear in the PM request or system prompt.
- Traces to: `internal/avatars/prompts.go`, `internal/avatars/orchestrator.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-GATE-005 — Typed epics and stories

- ID: REQ-GATE-005
- Level: F
- Statement: The Product Manager Avatar shall return typed epics and stories in which every story carries acceptance criteria and a `requirement_ids` trace to a PRD requirement, and invalid output is repaired at most once before the stage fails.
- Category: Data
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 3b (stage 2); `internal/contracts/types.go` (`Epic`, `Story`).
- Acceptance criteria: `internal/contracts/schema_test.go` (schema fixtures), `internal/avatars/avatars_test.go` (repair-once behaviour).
- Traces to: `internal/contracts/types.go`, `internal/contracts/schema/`, `internal/avatars/stage.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-GATE-006 — Staged deliberation

- ID: REQ-GATE-006
- Level: F
- Statement: The plan forge gate shall run deliberation as ordered stages — Product Manager, Technical Architect, selected Domain Specialists, a mandatory Red-Team Critic barrier, then the Lead Synthesizer — with at most 3 automatic cycles and a pause with open blockers when the cap is reached.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 3b (Stage ordering and revision rules).
- Acceptance criteria: `internal/avatars/avatars_test.go` (an open HIGH critique blocks synthesis; after 3 cycles the run pauses with no fourth automatic cycle).
- Traces to: `internal/avatars/orchestrator.go`, `internal/avatars/stage.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-GATE-007 — Decision capture

- ID: REQ-GATE-007
- Level: F
- Statement: The plan forge gate shall present human decision cards with two to four distinct options and a recommendation and shall resolve them first-valid-write-wins at the expected revision.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 3b (rule 7) and Step 4 (`DECISION_RESOLVE` ordering).
- Acceptance criteria: `internal/session/reducer_test.go` (`ALREADY_RESOLVED` on late conflicting resolutions), `internal/avatars/avatars_test.go`.
- Traces to: `internal/avatars/decisions.go`, `internal/session/reducer.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-GATE-008 — Deterministic compilation

- ID: REQ-GATE-008
- Level: F
- Statement: The synthesizer shall compile the approved inputs into a synthesized spec and plan manifest that are byte-identical for identical input, with `ContentHash` independent of approval metadata.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 3b (Compiler, diagrams and dual schedules).
- Acceptance criteria: `internal/synthesizer/synthesizer_test.go` (compiling twice is byte-identical; `ContentHash` unchanged by `ApprovalMeta`).
- Traces to: `internal/synthesizer/compiler.go`, `internal/synthesizer/mermaid.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-GATE-009 — Approval ordering

- ID: REQ-GATE-009
- Level: F
- Statement: The plan forge gate shall persist approval through the ordered APPROVE, journaled commit, `ARTIFACT_SAVED` and `PLAN_APPROVED` sequence, and shall never emit `PLAN_APPROVED` before `ARTIFACT_SAVED ok`.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 4 (Approval ordering).
- Acceptance criteria: `internal/storage/storage_test.go` (journaled commit and crash recovery), `internal/session/reducer_test.go`, `web/e2e/bridge.spec.ts` (approval writes both artifacts).
- Traces to: `internal/storage/artifacts.go`, `internal/session/reducer.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-GATE-010 — Dual effort estimates

- ID: REQ-GATE-010
- Level: F
- Statement: The synthesizer shall compute dual effort estimates — a human schedule on a 9-hour productive workday and an agent schedule in elapsed minutes — from PERT inputs with an explicit basis, labelled as model estimates unless measured.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 3b (`internal/synthesizer/gantt.go`); Page 3 dual-baseline requirement.
- Acceptance criteria: `internal/synthesizer/synthesizer_test.go` (schedule and Gantt cases).
- Traces to: `internal/synthesizer/gantt.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-GATE-011 — Decision limits

- ID: REQ-GATE-011
- Level: F
- Statement: The plan forge gate shall never let a human choice waive CRITICAL or HIGH defects, invalid contracts or safety invariants.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 3b (rule 7) and Step 4 (`DECISION_RESOLVE` ordering).
- Acceptance criteria: `internal/avatars/avatars_test.go` — only MEDIUM findings become decision cards (`TestMediumFindingsAtTheCapBecomeADecision`) and an open CRITICAL or HIGH critique pauses the run with blockers instead of reaching synthesis or a waivable decision.
- Traces to: `internal/avatars/orchestrator.go`, `internal/avatars/decisions.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-GATE-012 — Epics from the interpretation call

- ID: REQ-GATE-012
- Level: F
- Statement: The interpretation call shall return `epics[]` beside the `NormalizedPRD`, each epic carrying `id`, `title`, `goal`, `requirements` and `acceptance_criteria`.
- Category: Data
- Owner: @danielckv
- Source: Maintainer decision (2026-10-22): Page 1 shows the brief as epics before deliberation.
- Acceptance criteria: `internal/ingestion/normalizer_test.go` (to be added: a normalized brief returns `epics[]` with all five fields set).
- Traces to: `internal/ingestion/normalizer.go`, `internal/contracts/types.go`
- Status: not met
- Last reviewed: 2026-10-22

### REQ-GATE-013 — Epics invent nothing

- ID: REQ-GATE-013
- Level: F
- Statement: The normalizer shall never place in an epic a requirement or acceptance criterion that is absent from the `NormalizedPRD`.
- Category: Functional
- Owner: @danielckv
- Source: Maintainer decision (2026-10-22): epics group existing requirements and never add content.
- Acceptance criteria: `internal/ingestion/normalizer_test.go` (to be added: a model reply that puts an unknown requirement in an epic never yields an epic containing it).
- Traces to: `internal/ingestion/normalizer.go`
- Status: not met
- Last reviewed: 2026-10-22

## CONTEXT — pre-planning context gatherer

The context gatherer is the `internal/gather` package. It collects what the repository already records about itself before deliberation starts, with no model call. `docs/adrs/0002-read-agents-recall-index.md` records the decision.

### REQ-CONTEXT-001 — Avatars start from recorded project memory

- ID: REQ-CONTEXT-001
- Level: S
- Statement: The plan forge gate shall give the Technical Architect, the Domain Specialists and the Red-Team Critic the repository's recorded decisions, specs, plans and git state before deliberation, gathered without a model call.
- Category: User
- Owner: @danielckv
- Source: Maintainer decision (2026-10-22); `docs/adrs/0002-read-agents-recall-index.md`.
- Acceptance criteria: `internal/gather/gather_test.go` (to be added: a fixture repository with `.agents/` and `docs/` content yields a context that names those files).
- Traces to: `internal/gather/`
- Status: not met
- Last reviewed: 2026-10-22

### REQ-CONTEXT-002 — Read the recall index read-only

- ID: REQ-CONTEXT-002
- Level: F
- Statement: The context gatherer shall open the project-memory recall index (SQLite FTS5) read-only through the pure-Go `modernc.org/sqlite` driver, trying `<git-common-dir>/agents-local/recall.db` first and the legacy `.agents/index/recall.db` second.
- Category: Interface
- Owner: @danielckv
- Source: `docs/adrs/0002-read-agents-recall-index.md`.
- Acceptance criteria: `internal/gather/gather_test.go` (to be added: an index at each location is read, the first location wins when both exist, and the index file is byte-identical after the read).
- Traces to: `internal/gather/`
- Status: not met
- Last reviewed: 2026-10-22

### REQ-CONTEXT-003 — Fall back to the files

- ID: REQ-CONTEXT-003
- Level: F
- Statement: When no recall index can be opened, the context gatherer shall read `.agents/STATE.md`, `.agents/lanes/`, `.agents/sessions/`, `docs/specs/`, `docs/plans/`, `docs/adrs/` and `specs/` directly, read-only.
- Category: Functional
- Owner: @danielckv
- Source: `docs/adrs/0002-read-agents-recall-index.md`.
- Acceptance criteria: `internal/gather/gather_test.go` (to be added: with no index, the context names files from each listed path that exists; a missing path is skipped without error).
- Traces to: `internal/gather/`
- Status: not met
- Last reviewed: 2026-10-22

### REQ-CONTEXT-004 — Git state

- ID: REQ-CONTEXT-004
- Level: F
- Statement: The context gatherer shall record the current branch, the HEAD commit, the number of dirty paths and the subjects of the most recent commits.
- Category: Functional
- Owner: @danielckv
- Source: Maintainer decision (2026-10-22).
- Acceptance criteria: `internal/gather/gather_test.go` (to be added: a fixture repository with one dirty file reports its branch, HEAD, a dirty count of 1 and its commit subjects). The number of recent subjects is not yet fixed here (open question 14).
- Traces to: `internal/gather/`
- Status: not met
- Last reviewed: 2026-10-22

### REQ-CONTEXT-005 — Output bound

- ID: REQ-CONTEXT-005
- Level: F
- Statement: The context gatherer shall cap its output at 64 KiB.
- Category: Constraint
- Owner: @danielckv
- Source: Maintainer decision (2026-10-22).
- Acceptance criteria: `internal/gather/gather_test.go` (to be added: a fixture larger than 64 KiB yields output of at most 65,536 bytes).
- Traces to: `internal/gather/`
- Status: not met
- Last reviewed: 2026-10-22

### REQ-CONTEXT-006 — Repository-relative paths only

- ID: REQ-CONTEXT-006
- Level: F
- Statement: The context gatherer shall write every path in its output relative to the repository root and never as an absolute path.
- Category: Non-functional (security)
- Owner: @danielckv
- Source: Maintainer decision (2026-10-22); the no-absolute-path rule of REQ-SEC-004.
- Acceptance criteria: `internal/gather/gather_test.go` (to be added: the output never contains the fixture's absolute root).
- Traces to: `internal/gather/`
- Status: not met
- Last reviewed: 2026-10-22

### REQ-CONTEXT-007 — Never to the Product Manager

- ID: REQ-CONTEXT-007
- Level: F
- Statement: The plan forge gate shall put the gathered context in the Technical Architect, Domain Specialist and Red-Team Critic payloads only, and never in the Product Manager payload.
- Category: Non-functional (security)
- Owner: @danielckv
- Source: Maintainer decision (2026-10-22); Product Manager isolation (REQ-GATE-004).
- Acceptance criteria: Pending: no test is named yet (open question 14). The REQ-GATE-004 sentinel tests in `internal/avatars/prompts_test.go` are where a context sentinel would be asserted absent from the Product Manager request.
- Traces to: `internal/avatars/prompts.go`, `internal/gather/`
- Status: not met
- Last reviewed: 2026-10-22

### REQ-CONTEXT-008 — The user sees what the avatars see

- ID: REQ-CONTEXT-008
- Level: F
- Statement: The window shall show the user the gathered context that the avatars receive.
- Category: Functional
- Owner: @danielckv
- Source: Maintainer decision (2026-10-22).
- Acceptance criteria: Pending: no test is named yet (open question 14).
- Traces to: `web/src/pages/Page1Ingestion.tsx`, `internal/app/`
- Status: not met
- Last reviewed: 2026-10-22

### REQ-CONTEXT-009 — `plan-forge context`

- ID: REQ-CONTEXT-009
- Level: F
- Statement: `plan-forge context` shall print the gathered context for the current repository to stdout.
- Category: Functional
- Owner: @danielckv
- Source: Maintainer decision (2026-10-22).
- Acceptance criteria: `internal/gather/gather_test.go` (to be added) covers the content; no command-level test is named yet (open question 14).
- Traces to: `cmd/client/`, `internal/gather/`
- Status: not met
- Last reviewed: 2026-10-22

## UI — the window

### REQ-UI-001 — Three sequential views

- ID: REQ-UI-001
- Level: S
- Statement: The user shall move through three distinct, sequential views — ingestion, deliberation, master specification — with explicit phase-driven transitions and never a single scrolling page.
- Category: User
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 5 (three-page embedded UI).
- Acceptance criteria: `web/src/ui/phase.test.ts`; `web/e2e/bridge.spec.ts` (a manual URL jump to `/synthesis` during Page 1 is redirected).
- Traces to: C8 (`docs/plans/GUI_BOARD_HANDOFF_PLAN.md`); `web/src/App.tsx`, `web/src/pages/`, `web/src/store/syncStore.ts`
- Status: met
- Last reviewed: 2026-10-19

### REQ-UI-002 — Dedicated window

- ID: REQ-UI-002
- Level: F
- Statement: The client shall open the UI in a dedicated Chromium-family application window launched with `--app` and a temporary profile, never in a default browser tab, and shall end the session when that window closes.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 4 (Dedicated application window).
- Acceptance criteria: `internal/window/launch_test.go`; close detection exits 130 per the plan.
- Traces to: `internal/window/launch.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-UI-003 — Phase stepper and sidebar

- ID: REQ-UI-003
- Level: F
- Statement: The window shall show a phase stepper marking the current phase with `aria-current="step"` and a sidebar for navigation and status.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` Goal 1 (GUI polish); W1 slice.
- Acceptance criteria: `web/src/ui/phase.test.ts`; `web/src/ui/PhaseStepper.tsx` carries `aria-current` and `aria-label`.
- Traces to: `web/src/ui/PhaseStepper.tsx`, `web/src/components/Sidebar.tsx`
- Status: met
- Last reviewed: 2026-10-19

### REQ-UI-004 — Theme

- ID: REQ-UI-004
- Level: F
- Statement: The window shall offer a persisted light, dark and system theme selection.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` Goal 1; W1 slice.
- Acceptance criteria: `web/src/ui/prefs.test.ts`; `web/src/app.css` carries the `prefers-color-scheme: dark` block.
- Traces to: `web/src/ui/ThemeSelect.tsx`, `web/src/ui/prefs.ts`
- Status: met
- Last reviewed: 2026-10-19

### REQ-UI-005 — Reduced motion

- ID: REQ-UI-005
- Level: F
- Statement: The window shall disable non-essential motion when the user agent reports `prefers-reduced-motion: reduce`.
- Category: Non-functional (usability/accessibility)
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C12 (the `prefers-reduced-motion` rule, first written for the retired Graph view).
- Acceptance criteria: `web/src/app.css` contains a `prefers-reduced-motion: reduce` block.
- Traces to: `web/src/app.css`
- Status: met
- Last reviewed: 2026-10-22

### REQ-UI-006 — Keyboard operation

- ID: REQ-UI-006
- Level: F
- Statement: The window shall provide keyboard shortcuts with an in-app shortcut reference and shall trap focus inside modal dialogs.
- Category: Non-functional (usability/accessibility)
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` Goal 1; W1 slice.
- Acceptance criteria: `web/src/ui/shortcuts.test.ts`; `web/src/ui/focusTrap.ts` is used by `web/src/ui/Dialog.tsx`.
- Traces to: `web/src/ui/shortcuts.ts`, `web/src/ui/useShortcuts.ts`, `web/src/ui/ShortcutHelp.tsx`
- Status: met
- Last reviewed: 2026-10-19

### REQ-UI-007 — Responsive layout

- ID: REQ-UI-007
- Level: F
- Statement: The window shall remain usable across viewport widths through defined responsive breakpoints.
- Category: Non-functional (usability/accessibility)
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` Goal 1.
- Acceptance criteria: `web/src/app.css` (`@media (max-width: 1099.98px)`) breakpoint.
- Traces to: `web/src/app.css`
- Status: met
- Last reviewed: 2026-10-22

### REQ-UI-009 — Executive-grade presentation

- ID: REQ-UI-009
- Level: B
- Statement: The product shall meet a measurable usability outcome for executive-grade presentation — a task-completion or SUS target the owner has yet to set (open question 8) — so the interface is suitable for planning real products in research and commercial contexts.
- Category: Business
- Owner: @danielckv
- Source: User request this session (2026-10-19): "a more fluent, flexible and nicer GUI".
- Acceptance criteria: Target pending: a measurable usability target (task-completion study or SUS score) set by the owner and then measured. Until it exists, the objective proxy — the W1 component set (`web/src/ui/`) ships and `npm --prefix web run lint && npm --prefix web run typecheck && npm --prefix web test` are green — is the only evidence.
- Traces to: `web/src/ui/`, `web/src/app.css`
- Status: partly met
- Last reviewed: 2026-10-19

### REQ-UI-010 — Markdown composer

- ID: REQ-UI-010
- Level: F
- Statement: The Page 1 composer shall provide a full markdown editor for the requirements text.
- Category: Functional
- Owner: @danielckv
- Source: Maintainer decision (2026-10-22): the Page 1 composer gains a markdown editor, a code component browser and an epic modal.
- Acceptance criteria: `web/src/ui/docSync.test.ts` (to be added: an edit made in the editor reaches the shared PRD document unchanged).
- Traces to: `web/src/pages/Page1Ingestion.tsx`, `web/src/codemirror/`
- Status: not met
- Last reviewed: 2026-10-22

### REQ-UI-011 — Code component browser

- ID: REQ-UI-011
- Level: F
- Statement: The Page 1 composer shall let the user browse the code components of the repository under review.
- Category: Functional
- Owner: @danielckv
- Source: Maintainer decision (2026-10-22), as for REQ-UI-010.
- Acceptance criteria: `internal/app/planfiles_test.go` (to be added: the component list holds repository-relative paths only).
- Traces to: `web/src/pages/Page1Ingestion.tsx`, `internal/app/`
- Status: not met
- Last reviewed: 2026-10-22

### REQ-UI-012 — Epic files

- ID: REQ-UI-012
- Level: F
- Statement: Page 1 shall show each epic returned by the interpretation call (REQ-GATE-012) as a file the user can open.
- Category: Functional
- Owner: @danielckv
- Source: Maintainer decision (2026-10-22), as for REQ-UI-010.
- Acceptance criteria: `internal/app/planfiles_test.go` (to be added: one epic file per returned epic).
- Traces to: `web/src/pages/Page1Ingestion.tsx`, `internal/app/`
- Status: not met
- Last reviewed: 2026-10-22

### REQ-UI-013 — Epics link real code components

- ID: REQ-UI-013
- Level: F
- Statement: Page 1 shall link an epic only to code components whose repository-relative path exists in the repository.
- Category: Functional
- Owner: @danielckv
- Source: Maintainer decision (2026-10-22): epic files show linked real code components.
- Acceptance criteria: `internal/app/planfiles_test.go` (to be added: a component path that does not exist is never linked).
- Traces to: `internal/app/`
- Status: not met
- Last reviewed: 2026-10-22

### REQ-UI-014 — Epic modal

- ID: REQ-UI-014
- Level: F
- Statement: Page 1 shall open an epic in a modal dialog that shows its goal, requirements, acceptance criteria and linked code components.
- Category: Functional
- Owner: @danielckv
- Source: Maintainer decision (2026-10-22), as for REQ-UI-010.
- Acceptance criteria: Pending: no test is named yet (open question 14). The dialog traps focus as REQ-UI-006 requires of every modal.
- Traces to: `web/src/pages/Page1Ingestion.tsx`, `web/src/ui/Dialog.tsx`
- Status: not met
- Last reviewed: 2026-10-22

## HANDOFF — plan-mode handoff and validation

### REQ-HANDOFF-001 — Automatic handoff

- ID: REQ-HANDOFF-001
- Level: S
- Statement: Once the plan manifest is approved in the plan forge gate, the agent harness on a host with a supported mechanism shall enter its own plan mode automatically and validation shall run without manual intervention — Claude Code via REQ-HANDOFF-002 and oh-my-pi via REQ-HANDOFF-003, which falls back to prefilling `/plan`; Antigravity receives a plan-mode hint only (REQ-HANDOFF-006).
- Category: User
- Owner: @danielckv
- Source: User request this session (2026-10-19): "once the plan is ready from the forge the agent harness switches to its `/plan` mode automatically and validation is triggered".
- Acceptance criteria: `cmd/client/cmd_hook_test.go` (Claude Code path), `integrations/omp-hook-plan-forge.test.ts` (oh-my-pi path, including the `/plan` prefill fallback); no automatic mechanism exists for Antigravity (open question 12).
- Traces to: C6, C7; `cmd/client/cmd_hook.go`, `integrations/omp-hook-plan-forge.ts`
- Status: partly met
- Last reviewed: 2026-10-19

### REQ-HANDOFF-002 — Claude Code mechanism

- ID: REQ-HANDOFF-002
- Level: F
- Statement: For Claude Code, a managed `PostToolUse` hook shall detect an approved `plan-forge wait` result and inject an `EnterPlanMode` directive together with the sanitized validation summary as `additionalContext`.
- Category: Interface
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C6.
- Acceptance criteria: `cmd/client/cmd_hook_test.go`; `cmd/client/cmd_hook.go` emits the `EnterPlanMode` directive.
- Traces to: C6; `cmd/client/cmd_hook.go`, `internal/host/hooks.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-HANDOFF-003 — oh-my-pi mechanism

- ID: REQ-HANDOFF-003
- Level: F
- Statement: For oh-my-pi, the hook-registered `/plan-forge` command shall open a new session in plan mode via `newSession` with `appendModeChange("plan", {})` followed by `switchSession`, seed it with the synthesized spec path and validation summary via `sendUserMessage`, and fall back to prefilling `/plan` when any API is missing, throws or is cancelled.
- Category: Interface
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C7 and the 2026-10-19 revision of the OMP plan-mode mechanism.
- Acceptance criteria: `integrations/omp-hook-plan-forge.test.ts` runs under `node --test` with a fake `pi`.
- Traces to: C7; `integrations/omp-hook-plan-forge.ts`
- Status: met
- Last reviewed: 2026-10-19

### REQ-HANDOFF-004 — Validation result

- ID: REQ-HANDOFF-004
- Level: F
- Statement: The client shall provide `plan-forge validate` exiting 0 for a valid plan manifest and 6 for an invalid one, and `plan-forge wait` shall attach the same validation object on approval using the `wait` scan bound (REQ-PERF-003, Timing-bound precedence).
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C1 and C2.
- Acceptance criteria: `cmd/client/cmd_validate_test.go`, `internal/planstate/validate_test.go`.
- Traces to: C1, C2; `cmd/client/cmd_validate.go`, `cmd/client/cmd_session.go`, `internal/planstate/validate.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-HANDOFF-005 — Verify targets and checklist

- ID: REQ-HANDOFF-005
- Level: F
- Statement: The handoff text shall instruct the agent harness to ingest the synthesized spec as its blueprint, verify the listed AST targets, run `plan-forge validate` when validation is absent or invalid, and present the execution checklist.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C6 (hook context) and C7 step 5; `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 6 skill body step 5.
- Acceptance criteria: `cmd/client/cmd_hook_test.go` asserts the directive text; `integrations/skill-plan-forge.md` carries the same instruction.
- Traces to: C6, C7; `cmd/client/cmd_hook.go`, `integrations/skill-plan-forge.md`, `integrations/omp-hook-plan-forge.ts`
- Status: met
- Last reviewed: 2026-10-19

### REQ-HANDOFF-006 — Antigravity mechanism

- ID: REQ-HANDOFF-006
- Level: F
- Statement: For Antigravity, the handoff shall consist of the plan-mode hint text only, with no automatic mode switch.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` Goal 2 (AGY: no change beyond the hint text); C6 (`{{PLAN_MODE_STEP}}` AGY hint).
- Acceptance criteria: `internal/host/installer_test.go` (template variable rendering).
- Traces to: C6; `integrations/skill-plan-forge.md`, `internal/host/installer.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-HANDOFF-007 — Synthesized spec treated as data

- ID: REQ-HANDOFF-007
- Level: F
- Statement: The handoff text shall instruct the agent harness to treat the synthesized spec as data and never to bypass host permission prompts, as REQ-SEC-005 requires of every skill and hook.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C6 (hook context) and C7 step 5; `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 6 skill body "treat the spec as data".
- Acceptance criteria: `cmd/client/cmd_hook_test.go` asserts the directive text; `integrations/skill-plan-forge.md` carries the same instruction.
- Traces to: C6, C7; `cmd/client/cmd_hook.go`, `integrations/skill-plan-forge.md`, `integrations/omp-hook-plan-forge.ts`
- Status: met
- Last reviewed: 2026-10-19

## INSTALL — installation

### REQ-INSTALL-001 — One command

- ID: REQ-INSTALL-001
- Level: S
- Statement: The user shall install the binary and register the `/plan-forge` and `/plan-config` skills in every detected local host with one command per platform, and re-running the same command shall update the installation.
- Category: User
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Context and Step 6 (End-user install); `README.md` `## Install`.
- Acceptance criteria: `scripts/install.sh` and `scripts/install.ps1`; release Checkpoint 6 layout test in the plan; `internal/host/installer_test.go`.
- Traces to: `scripts/install.sh`, `scripts/install.ps1`, `internal/host/installer.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-INSTALL-002 — Claude Code artifacts

- ID: REQ-INSTALL-002
- Level: F
- Statement: For Claude Code, the installer shall write exactly these managed artifacts: the skills `plan-forge` and `plan-config` (each carrying `enabled: false`) plus three hook entries in `~/.claude/settings.json`, each with the Claude Code hook timeout (Timing-bound precedence) — `SessionStart` running `hook check-state`, `PreToolUse` with matcher `Edit|Write|MultiEdit|NotebookEdit` running `hook claude-guard`, and `PostToolUse` with matcher `Bash` running `hook claude` for the plan-mode handoff only; the oh-my-pi and Antigravity artifact sets are REQ-INSTALL-008 and REQ-INSTALL-009.
- Category: Interface
- Owner: @danielckv
- Source: `internal/host/installer.go`; `internal/host/hooks.go` (`claudeHookEvents`, `claudePreToolUseMatcher`, `claudePostToolUseMatcher`); `docs/adrs/0001-remove-live-task-views.md`: no managed hook starts a daemon or opens a browser tab at session start.
- Acceptance criteria: `internal/host/installer_test.go`, `internal/host/hooks_test.go` (`TestManagedHooksStartNothingAtSessionStart`: neither the merged settings nor the manual snippet carries `--open`, `center` or `UserPromptSubmit`); every managed entry carries the `plan-forge managed` marker.
- Traces to: C6; `internal/host/installer.go`, `internal/host/hooks.go`, `integrations/`
- Status: met
- Last reviewed: 2026-10-22

### REQ-INSTALL-003 — One skill name per host root

- ID: REQ-INSTALL-003
- Level: F
- Statement: The installer shall install each feature under one skill name per host root — `plan-forge` and `plan-config` — with no duplicate name in the same root.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 6 (per-host template paths).
- Acceptance criteria: `internal/host/installer_test.go` (install-manifest rows per path; `TestInstallWritesEachSkillIntoOneRootPerHost`).
- Traces to: `internal/host/installer.go`, `integrations/`
- Status: met
- Last reviewed: 2026-10-22

### REQ-INSTALL-004 — Idempotent re-install

- ID: REQ-INSTALL-004
- Level: F
- Statement: The installer shall be idempotent through the `install-manifest.json` record of managed paths and hashes, skipping identical content and refusing diverged managed files with exit 5 unless `--force` is given.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 6 (Safety and idempotency).
- Acceptance criteria: `internal/host/installer_test.go` (skip, divergence, `--force` cases).
- Traces to: `internal/host/installer.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-INSTALL-005 — Exact uninstall

- ID: REQ-INSTALL-005
- Level: F
- Statement: The uninstaller shall remove exactly the managed entries recorded in the install manifest and nothing else, and `--purge` shall delete the enumerated Plan-Forge state files only when the `.plan-forge-home` marker exists, never touching a repository `.plan/` directory.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 6 (`uninstall --purge` bullet).
- Acceptance criteria: `internal/host/installer_test.go`; `cmd/client/cmd_uninstall.go`.
- Traces to: `internal/host/installer.go`, `cmd/client/cmd_uninstall.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-INSTALL-006 — Dry-run safety

- ID: REQ-INSTALL-006
- Level: F
- Statement: The installer shall support `--dry-run` reporting planned writes without touching the filesystem, and shall never prompt when stdin and stdout are not TTYs unless `--yes` is given.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 6 (installer flags).
- Acceptance criteria: `internal/host/installer_test.go` (dry-run and non-TTY cases).
- Traces to: `internal/host/installer.go`, `cmd/client/cmd_install.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-INSTALL-007 — No duplicate skill visible to oh-my-pi

- ID: REQ-INSTALL-007
- Level: F
- Statement: The installer shall mark the Claude Code skill copies with `enabled: false` — and only those copies, never the Antigravity copies (REQ-INSTALL-002, REQ-INSTALL-009); oh-my-pi receives no skill copy (REQ-INSTALL-008) — so that oh-my-pi, when its configuration reads the Claude Code skill root, skips the marked copies and lists exactly one `plan-forge` skill.
- Category: Functional
- Owner: @danielckv
- Source: User request this session (2026-10-19): "no duplicate skill in oh-my-pi, which also reads skills from the Claude and Gemini roots"; `internal/host/installer.go` (the `enabled: false` comment block: the oh-my-pi and Antigravity copies are deliberately not marked).
- Acceptance criteria: `internal/host/installer_test.go`; A/B verification on this machine (unmarked copies list two entries, marked copies list one).
- Traces to: `internal/host/installer.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-INSTALL-008 — oh-my-pi artifacts

- ID: REQ-INSTALL-008
- Level: F
- Statement: For oh-my-pi, the installer shall write exactly these managed artifacts: the command file `commands/plan-config.md` and the hook `hooks/pre/plan-forge.ts` registering `/plan-forge`, with no skill copy and no `commands/plan-forge.md`.
- Category: Interface
- Owner: @danielckv
- Source: `planOMP` in `internal/host/installer.go`; `integrations/omp-hook-plan-forge.ts` (one `registerCommand` call, for `plan-forge`); `docs/adrs/0001-remove-live-task-views.md` (`/plan-forge-board` removed).
- Acceptance criteria: `internal/host/installer_test.go` (`TestOMPPlansCommandsAndHookButNoSkills`, `TestOMPDoesNotPlanForgeCommand`), `internal/host/hooks_test.go`, `integrations/omp-hook-plan-forge.test.ts`; every managed entry carries the `plan-forge managed` marker.
- Traces to: C7; `internal/host/installer.go`, `internal/host/hooks.go`, `integrations/`
- Status: met
- Last reviewed: 2026-10-22

### REQ-INSTALL-009 — Antigravity artifacts

- ID: REQ-INSTALL-009
- Level: F
- Statement: For Antigravity, the installer shall write exactly these managed artifacts: the skills `plan-forge` and `plan-config` in both `~/.gemini/antigravity-cli/skills/` and `~/.gemini/config/skills/` (without `enabled: false`) plus a `SessionStart` entry with the Antigravity hook timeout (Timing-bound precedence) in `~/.gemini/config/hooks.json`.
- Category: Interface
- Owner: @danielckv
- Source: Verified `plan-forge install --host auto --yes` run on this machine (2026-10-19); `internal/host/installer.go`; `internal/host/hooks.go`.
- Acceptance criteria: `internal/host/installer_test.go`, `internal/host/hooks_test.go`; every managed entry carries the `plan-forge managed` marker. The Antigravity hook file shape is unverified (open question 5).
- Traces to: C6; `internal/host/installer.go`, `internal/host/hooks.go`, `integrations/`
- Status: partly met
- Last reviewed: 2026-10-19

### REQ-INSTALL-010 — Managed hook verification

- ID: REQ-INSTALL-010
- Level: F
- Statement: `plan-forge doctor` shall report, per host, whether the managed hooks are installed and whether they name the running binary, with a remedy for every state that is not installed.
- Category: Functional
- Owner: @danielckv
- Source: Consequence of the oh-my-pi command-only surface (REQ-INSTALL-008): with no skill copies, a missing hook removes `/plan-forge` rather than degrading it; `docs/plans/COMMAND_CENTER_PLAN.md` C21.
- Acceptance criteria: `internal/host/hookcheck_test.go` (installed, stale, modified, missing and unreadable per host; a 0.3.0 install that still launches the command center or carries a `UserPromptSubmit` entry reports stale; the oh-my-pi remedy names the command that is lost; the Antigravity remedy routes through uninstall because `mergeAGYHook` cannot refresh a stale path).
- Traces to: C21; `internal/host/hookcheck.go`, `cmd/client/cmd_doctor.go`
- Status: met
- Last reviewed: 2026-10-22

### REQ-INSTALL-011 — Upgrade strips retired hook handlers

- ID: REQ-INSTALL-011
- Level: F
- Statement: On install and on uninstall, the installer shall remove from `~/.claude/settings.json` every managed handler that 0.3.0 wrote under `UserPromptSubmit` or that launched the command center, and shall keep every foreign handler.
- Category: Functional
- Owner: @danielckv
- Source: `docs/adrs/0001-remove-live-task-views.md`; `claudeRetiredHookEvents` in `internal/host/hooks.go`.
- Acceptance criteria: `internal/host/hooks_test.go` (`TestMergeClaudeHookUpgradesAPreviousInstall`: a reinstall over 0.3.0 settings removes both handlers, keeps foreign ones and is idempotent; `TestSplitManagedHookRemovesRetiredHandlers`: uninstall leaves no managed residue and keeps a foreign `UserPromptSubmit` handler).
- Traces to: `internal/host/hooks.go`, `internal/host/installer.go`
- Status: met
- Last reviewed: 2026-10-22

### REQ-INSTALL-012 — Upgrade removes the retired board skill

- ID: REQ-INSTALL-012
- Level: F
- Statement: For Claude Code, an install shall delete the `plan-forge-board` skill copy an earlier version wrote only while its bytes match the hash recorded in `install-manifest.json`, and shall keep a copy the user edited.
- Category: Functional
- Owner: @danielckv
- Source: `docs/adrs/0001-remove-live-task-views.md`; `retiredSkills` in `internal/host/installer.go`; REQ-INSTALL-004 (hash-checked ownership).
- Acceptance criteria: `internal/host/installer_test.go` (`TestClaudeInstallRemovesRetiredBoardSkill`: the owned copy is deleted, the edited copy stays, and no install plans the retired skill).
- Traces to: `internal/host/installer.go`
- Status: met
- Last reviewed: 2026-10-22

### REQ-INSTALL-013 — Retired subcommands stay quiet

- ID: REQ-INSTALL-013
- Level: F
- Statement: Until 0.5.0, `plan-forge center`, `plan-forge board` and `plan-forge swarm` shall each print one line to stderr that names `plan-forge install` and exit 0, so a hook written by 0.3.0 never reports an error in the user's session.
- Category: Functional
- Owner: @danielckv
- Source: `docs/adrs/0001-remove-live-task-views.md`; `retiredCommands` in `cmd/client/main.go` ("Remove this table in 0.5.0").
- Acceptance criteria: `cmd/client/retired_test.go` (`TestRetiredCommandsExitCleanly`: exit 0, exactly one line, the line names `plan-forge install`, and none of the three is a registered command).
- Traces to: `cmd/client/main.go`
- Status: met
- Last reviewed: 2026-10-22

## RELEASE — gates and artifacts

### REQ-RELEASE-001 — CI gates before release

- ID: REQ-RELEASE-001
- Level: F
- Statement: CI shall prove the lint, shared, server, client, web, end-to-end and script gates — `actionlint`, `markdownlint`, `editorconfig`, `links`, `go-shared`, `go-server`, `web`, `web-e2e`, `go-client` and `deploy-script` (which runs `shellcheck` as a step) — on every push to `main` and every pull request.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 6 (Release pipeline); repository invariant (web gates go-client and release).
- Acceptance criteria: `.github/workflows/ci.yml` job graph.
- Traces to: `.github/workflows/ci.yml`, `.github/workflows/release.yml`
- Status: met
- Last reviewed: 2026-10-19

### REQ-RELEASE-002 — Relay dependency closure in CI

- ID: REQ-RELEASE-002
- Level: F
- Statement: The release pipeline shall prove the relay dependency closure defined by REQ-COMPAT-002 — via `go list -deps -tags server ./cmd/relay` — before a relay image is published.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 1 (dependency proof) and Step 6 (`go-server` job).
- Acceptance criteria: `.github/workflows/release.yml` job `relay` runs `CGO_ENABLED=0 go build -tags server ./cmd/relay` and the closure assertion, and the `package` and `relay-image` jobs depend on it; `.github/workflows/ci.yml` job `go-server` runs vet, test and the same closure assertion.
- Traces to: `.github/workflows/ci.yml`, `internal/buildguard/`, `cmd/relay/guard.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-RELEASE-003 — Archive contents

- ID: REQ-RELEASE-003
- Level: F
- Statement: Each published client archive shall contain the `plan-forge` binary, `LICENSE`, `README.md`, the platform installer, the generic `commands/` skill copies, the guarded `hooks/post-merge.sh` block and the `integrations/` templates identical to the embedded copies.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 6 (Client archive layout).
- Acceptance criteria: `scripts/package-release.sh --snapshot` produces exactly 8 archives plus both installers and `checksums.txt`; `scripts/verify-archive.sh` exits 0 on a produced archive.
- Traces to: `scripts/package-release.sh`, `scripts/verify-archive.sh`, `.github/workflows/release.yml` `package` job
- Status: met
- Last reviewed: 2026-10-19

### REQ-RELEASE-004 — Installing from a published release

- ID: REQ-RELEASE-004
- Level: F
- Statement: The one-command installers shall resolve the latest published `v*.*.*` release, download the archive and `checksums.txt` over TLS, verify the checksum, and install atomically, and release archives shall be published only from tags and never from pull requests.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 6 (triggers; end-user install).
- Acceptance criteria: `.github/workflows/release.yml` job `publish` runs only on `v*.*.*` tags (`if: startsWith(github.ref, 'refs/tags/v')`); job `relay-image` runs on every push event (`if: github.event_name == 'push'`) and publishes `:main` and `:sha-<7>` for branch pushes and the version tag and `:latest` for tags; tags `v0.1.0` and `v0.1.1` are published.
- Traces to: `.github/workflows/release.yml`, `scripts/install.sh`, `scripts/install.ps1`
- Status: met
- Last reviewed: 2026-10-19

### REQ-RELEASE-005 — Pinned action SHAs

- ID: REQ-RELEASE-005
- Level: F
- Statement: Every `uses:` reference in the workflows shall be pinned to a 40-character commit SHA with a version comment.
- Category: Constraint
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 6 (existing repository invariant).
- Acceptance criteria: `.github/workflows/ci.yml` and `.github/workflows/release.yml` pin every `uses:` to a 40-character SHA.
- Traces to: `.github/workflows/`
- Status: met
- Last reviewed: 2026-10-19

### REQ-RELEASE-006 — Web gate blocks client and release

- ID: REQ-RELEASE-006
- Level: F
- Statement: The `web` CI gate shall block the client and release jobs when it fails — `go-client` in CI and `client` in the release workflow directly, `package`, `relay-image` and `publish` transitively through `client`.
- Category: Functional
- Owner: @danielckv
- Source: Repository invariant (web gates go-client and release); `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 6 (Release pipeline).
- Acceptance criteria: `.github/workflows/ci.yml` `go-client` and `.github/workflows/release.yml` `client` declare `needs: web`; `package` declares `needs: [client, relay]` and `publish` and `relay-image` declare `needs: package`.
- Traces to: `.github/workflows/ci.yml`, `.github/workflows/release.yml`
- Status: met
- Last reviewed: 2026-10-19

## SEC — untrusted-input posture

### REQ-SEC-001 — Manifest and spec are untrusted

- ID: REQ-SEC-001
- Level: F
- Statement: The client shall treat `.plan/plan_manifest.json` and `.plan/synthesized_spec.md` as untrusted input in `hook`, `open`, `summary` and `verify`, printing plan detail only when the manifest hash is present in the local approval ledger and otherwise printing the fixed unverified-manifest notice.
- Category: Non-functional (security)
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 3b (Plan-status hook evidence).
- Acceptance criteria: `internal/planstate/validate_test.go` and ledger cases in `internal/planstate/`; the approval ledger lives at `PlanForgeHome()/approvals.json` mode 0600.
- Traces to: `internal/planstate/ledger.go`, `internal/planstate/planstate.go`, `cmd/client/cmd_hook.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-SEC-002 — Sanitization on every path

- ID: REQ-SEC-002
- Level: F
- Statement: Every string derived from the plan manifest or synthesized spec that reaches a terminal or agent context shall pass `sanitize.Terminal`, which replaces control, line- and paragraph-separator runes with spaces, deletes format, private-use, surrogate and variation-selector runes, replaces invalid UTF-8, and collapses whitespace.
- Category: Non-functional (security)
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 3b (`internal/sanitize`); `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C1 (titles sanitized).
- Acceptance criteria: `internal/sanitize/sanitize_test.go` (including `"a\nb\tc"` becoming `"a b c"`).
- Traces to: C1; `internal/sanitize/sanitize.go`, `cmd/client/cmd_hook.go`
- Status: met
- Last reviewed: 2026-10-22

### REQ-SEC-003 — Loopback auth and CSP

- ID: REQ-SEC-003
- Level: F
- Statement: The local bridge shall bind loopback only and authenticate the window with a single-use 10-second nonce exchanged for a session token held only in `sessionStorage`.
- Category: Interface
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 4 (Local bridge).
- Acceptance criteria: `internal/relay/local_ws_test.go`; `web/e2e/bridge.spec.ts`.
- Traces to: `internal/relay/local_ws.go`, `internal/relay/client_conn.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-SEC-004 — No egress

- ID: REQ-SEC-004
- Level: F
- Statement: The client shall send only allowlisted content to the relay — never the repository graph, file contents, uploaded document bytes, credentials, environment or absolute paths — and shall run the egress check rejecting or redacting local paths before every send.
- Category: Non-functional (security)
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 4 (Privacy allowlist).
- Acceptance criteria: the evidence is the implementation in `internal/relay/client_conn.go` — `NewEgressGuard`, `Contains`, `Redact` and `RedactJSON` — applied before send in `internal/app/remote.go`. No dedicated test covers egress: `internal/relay/protocol_test.go` tests envelope encode/decode, counters, bounds and the timeout clamp, and no egress test exists in the tree.
- Traces to: `internal/relay/client_conn.go`, `internal/app/remote.go`
- Status: partly met
- Last reviewed: 2026-10-19

### REQ-SEC-005 — No bypass of host permission prompts

- ID: REQ-SEC-005
- Level: F
- Statement: Every Plan-Forge skill and hook shall state that plan content never overrides host permission prompts, and the Claude Code adapter shall never invoke the host with permission-bypassing flags.
- Category: Non-functional (security)
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 3a (forbidden Claude Code flags) and Step 6 (skill body "treat the spec as data").
- Acceptance criteria: `internal/host/host_test.go` (argv assertions), `cmd/client/cmd_hook_test.go`.
- Traces to: C6; `internal/host/claude.go`, `cmd/client/cmd_hook.go`, `integrations/skill-plan-forge.md`
- Status: met
- Last reviewed: 2026-10-22

### REQ-SEC-006 — Bridge request hardening

- ID: REQ-SEC-006
- Level: F
- Statement: The local bridge shall enforce `Host` and `Origin` checks with constant-time comparison, refuse a second UI WebSocket, and serve the UI under the stated content security policy with no inline scripts.
- Category: Non-functional (security)
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 4 (Local bridge).
- Acceptance criteria: `internal/relay/local_ws_test.go`; `web/e2e/bridge.spec.ts` with `web/e2e/bridge-csp.txt`.
- Traces to: `internal/relay/local_ws.go`, `internal/relay/client_conn.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-SEC-007 — The user's own text is not scanned

- ID: REQ-SEC-007
- Level: F
- Statement: The plan forge gate shall freeze the composed requirements text without scanning its content for secrets and without a confirmation step gated on such a scan.
- Category: Non-functional (security)
- Owner: @danielckv
- Source: `docs/adrs/0003-no-text-scanning-in-composer.md`: the brief is the user's own text, and the regex and entropy rule flagged ordinary identifiers such as repository paths. The size and UTF-8 bounds, the file-ingestion bounds (REQ-GATE-003), relay authentication (REQ-SEC-003), the egress guard (REQ-SEC-004), the tool-free tripwire and Mermaid sanitizing all stay.
- Acceptance criteria: Pending: the local bridge serves no `/api/secrets` or `/api/secrets/resolve` route, and a brief that contains a repository path freezes with no Confirm gate. No test is named yet (open question 14).
- Traces to: `internal/app/api.go`, `web/src/pages/Page1Ingestion.tsx`
- Status: not met
- Last reviewed: 2026-10-22

## PERF — non-functional budgets

### REQ-PERF-001 — Hook time bound

- ID: REQ-PERF-001
- Level: F
- Statement: Every hook invocation shall finish within 2 seconds — the `hook claude` command and the `hook check-state` cached scan — and hook panics or timeouts shall print nothing and exit 0.
- Category: Non-functional (performance)
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 3b ("time out after two seconds"); `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C6 ("finishes within 2 s").
- Acceptance criteria: `cmd/client/cmd_hook_test.go`.
- Traces to: C6; `cmd/client/cmd_hook.go`
- Status: met
- Last reviewed: 2026-10-22

### REQ-PERF-002 — Scan bounds

- ID: REQ-PERF-002
- Level: F
- Statement: The AST scan shall run with at most `min(4, GOMAXPROCS)` workers, a 2-second per-file parse timeout and a 120-second total budget that degrades to a partial graph marked `timed_out`.
- Category: Non-functional (performance)
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 2 (Budget) and Tree-sitter scanner bounds.
- Acceptance criteria: the bound is defined by `internal/ast/scanner.go` — `DefaultParseTimeout` (`2 * time.Second`), `DefaultScanTimeout` (`120 * time.Second`) and the worker cap `min(4, runtime.GOMAXPROCS(0))` in `withDefaults`; partial-scan coverage is reported in `coverage.timed_out`. The bound is defined but not directly tested: `internal/ast/symbols_test.go` has no worker-count, parse-timeout, scan-timeout or `timed_out` assertion.
- Traces to: `internal/ast/scanner.go`, `internal/ast/parse.go`
- Status: partly met
- Last reviewed: 2026-10-19

### REQ-PERF-003 — Validation bounds

- ID: REQ-PERF-003
- Level: F
- Statement: `plan-forge validate` shall bound its scan by `--timeout` defaulting to 30 seconds, and `plan-forge wait` shall run its post-approval validation with a 20-second scan bound that degrades to `status: UNKNOWN` with a timeout note.
- Category: Non-functional (performance)
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C1 and C2.
- Acceptance criteria: `cmd/client/cmd_validate_test.go`, `internal/planstate/validate_test.go`.
- Traces to: C1, C2; `cmd/client/cmd_validate.go`, `cmd/client/cmd_session.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-PERF-006 — Relay messaging latency

- ID: REQ-PERF-006
- Level: F
- Statement: The relay shall deliver control and document events with p99 send-to-receive latency of at most 20 ms on loopback and 50 ms in CI under the integration load of 50 control events and 20 document pushes per second for 60 seconds with zero loss and zero duplicates.
- Category: Non-functional (performance)
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Checkpoint 4.
- Acceptance criteria: `internal/relay/hub_integration_test.go` (reports p50/p95/p99).
- Traces to: `internal/relay/hub.go`
- Status: met
- Last reviewed: 2026-10-19

### Timing-bound precedence

One time bound applies per operation. The requirement cited in each row is the authority for that number; other requirements cross-reference it instead of restating the figure.

| Operation | Bound | Authority |
| :--- | :--- | :--- |
| AST repository scan, total budget | 120 s | REQ-PERF-002 (`DefaultScanTimeout`) |
| `plan-forge validate` scan, default | 30 s | REQ-PERF-003 (`--timeout`) |
| `plan-forge wait` post-approval scan | 20 s | REQ-PERF-003 |
| Claude Code hook, host-enforced timeout written to `~/.claude/settings.json` | 5 s | REQ-INSTALL-002 (`claudeHookTimeout`) |
| Antigravity `SessionStart` hook, host-enforced timeout | 2 s | REQ-INSTALL-009 |

The 5-second and 2-second hook timeouts are limits the host enforces on the process; the 2-second hook time bound in REQ-PERF-001 is Plan-Forge's own budget for finishing inside them.

## COMPAT — platforms and toolchains

### REQ-COMPAT-001 — Client platforms

- ID: REQ-COMPAT-001
- Level: F
- Statement: The client shall build and ship for darwin/arm64, darwin/amd64, linux/amd64, linux/arm64 and windows/amd64 from the release matrix.
- Category: Constraint
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 6 (client matrix).
- Acceptance criteria: `.github/workflows/release.yml` `client` matrix covers the five targets with native runners.
- Traces to: `.github/workflows/release.yml`
- Status: met
- Last reviewed: 2026-10-19

### REQ-COMPAT-002 — Relay toolchain independence

- ID: REQ-COMPAT-002
- Level: F
- Statement: The relay server shall build with `CGO_ENABLED=0` under the `server` build tag with no client-only package, tree-sitter or PDF dependency in its closure — the dependency-closure rule REQ-RELEASE-002 proves in CI — enforced by the build-tag guard that fails a `client server` build naming `BOTH_BUILD_TAGS_client_and_server_are_mutually_exclusive`.
- Category: Constraint
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 1 (Build-tag policy; dependency proof); cross-reference REQ-RELEASE-002 for the CI gate.
- Acceptance criteria: `go build -tags "client server" ./cmd/client` fails naming the guard identifier; `.github/workflows/ci.yml` `go-server` job.
- Traces to: `internal/buildguard/`, `cmd/client/guard.go`, `cmd/relay/guard.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-COMPAT-003 — Node and TypeScript pins

- ID: REQ-COMPAT-003
- Level: F
- Statement: The web toolchain shall pin Node 24 LTS via `web/.nvmrc` and the workflow `node-version`, and TypeScript at 5.9.3, staying below 6.1.0 while `typescript-eslint` caps its peer range.
- Category: Constraint
- Owner: @danielckv
- Source: `web/.nvmrc` (`24`); `web/package.json` (`"node": ">=24"`, `"typescript": "5.9.3"`); repository invariant on the `typescript-eslint` peer cap.
- Acceptance criteria: `web/package.json` and `web/package-lock.json` pins; `.github/workflows/ci.yml` and `.github/workflows/release.yml` use `node-version: 24`.
- Traces to: `web/.nvmrc`, `web/package.json`, `.github/workflows/ci.yml`
- Status: met
- Last reviewed: 2026-10-19

### REQ-COMPAT-004 — Go toolchain pin

- ID: REQ-COMPAT-004
- Level: F
- Statement: The Go module shall pin `go 1.27.0` with `toolchain go1.27.2`, and every workflow shall select Go 1.27.2.
- Category: Constraint
- Owner: @danielckv
- Source: `go.mod`; `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 1 and Verification prerequisites.
- Acceptance criteria: `go.mod` lines 3 and 5; `go-version: "1.27.2"` in `.github/workflows/ci.yml` and `.github/workflows/release.yml`.
- Traces to: `go.mod`, `.github/workflows/`
- Status: met
- Last reviewed: 2026-10-19

### REQ-HANDOFF-008 — Spec-drift guard

- ID: REQ-HANDOFF-008
- Level: F
- Statement: When the approved plan changes under a running Claude Code session, the managed `PreToolUse` hook shall refuse the next edit exactly once and state the remedy.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/COMMAND_CENTER_PLAN.md` C19; the Claude Code hooks reference for the `PreToolUse` decision contract.
- Acceptance criteria: `cmd/client/cmd_hook_guard_test.go` (`TestGuardDeniesOncePerDrift` proves one refusal per change and that the next call proceeds; `TestGuardIsSilentWithoutAnApprovedPlan`; `TestGuardAlwaysExitsZero`; `TestGuardDecisionIsDenyAndNeverAllowOrDefer`; `TestGuardBaselineIsPerSession`), `internal/host/hooks_test.go` (the managed entry and its matcher).
- Traces to: C19; `cmd/client/cmd_hook_guard.go`, `internal/host/hooks.go`
- Status: met
- Last reviewed: 2026-10-19

## Retired requirements

These requirements left the catalogue on 2026-10-22 because the features they described were removed. Their IDs are never reused. The full text stays readable in git history at commit `d4cbb43`.

| IDs | Feature removed | Decision |
| :--- | :--- | :--- |
| REQ-BOARD-001 to REQ-BOARD-007 | The plan board: `/plan-forge-board`, `plan-forge board`, the overlay store and the board delta injected through hooks | `docs/adrs/0001-remove-live-task-views.md` |
| REQ-COORD-001 to REQ-COORD-007 | The coordinator and its Up-next queue | `docs/adrs/0001-remove-live-task-views.md` |
| REQ-GRAPH-001 to REQ-GRAPH-005 | The Graph view and discoveries | `docs/adrs/0001-remove-live-task-views.md` |
| REQ-LIVE-001 to REQ-LIVE-006 | The command center: the loopback dashboard daemon, `plan-forge center`, the Live, Sprint and Setup pages, `center_port`, `center_auto_open` and `PLAN_FORGE_CENTER_PORT`; swarm reporting (`plan-forge swarm`) | `docs/adrs/0001-remove-live-task-views.md` |
| REQ-UI-008 | The `Board \| Graph` view switch and the coordinator panel | `docs/adrs/0001-remove-live-task-views.md` |
| REQ-PERF-004, REQ-PERF-005, REQ-PERF-007 | Board push latency, graph size and the oh-my-pi board sync timer | `docs/adrs/0001-remove-live-task-views.md` |

REQ-INSTALL-011, REQ-INSTALL-012 and REQ-INSTALL-013 state how an install from 0.3.0 sheds the retired hooks, skill and subcommands.

## Category coverage

Counts are taken from the `Category:` fields and sum to 77 requirements.

| Category | Count | Note |
| :--- | :--- | :--- |
| Functional | 39 | Concentrated in GATE, UI, CONTEXT, INSTALL and RELEASE; the SEC, PERF and COMPAT areas carry no functional requirements, so coverage is not uniform across areas. |
| Non-functional (security) | 8 | SEC area: REQ-SEC-001, REQ-SEC-002, REQ-SEC-004, REQ-SEC-005, REQ-SEC-006, REQ-SEC-007; plus REQ-CONTEXT-006 and REQ-CONTEXT-007. |
| Non-functional (performance) | 4 | REQ-PERF-001, REQ-PERF-002, REQ-PERF-003, REQ-PERF-006. |
| Non-functional (usability/accessibility) | 3 | REQ-UI-005, REQ-UI-006, REQ-UI-007. Genuinely thin: the GUI polish was delivered as one slice; further usability requirements await measurable targets from the owner (open question 8). |
| Business | 2 | REQ-GATE-001, REQ-UI-009. |
| User | 4 | The S-level capability statements: REQ-UI-001, REQ-HANDOFF-001, REQ-INSTALL-001, REQ-CONTEXT-001. |
| Interface | 7 | REQ-HANDOFF-002, REQ-HANDOFF-003, REQ-SEC-003, REQ-INSTALL-002, REQ-INSTALL-008, REQ-INSTALL-009, REQ-CONTEXT-002. The relay wire protocol is specified in the system plan as a design contract and is not restated here; the bridge authentication contract is restated as REQ-SEC-003. |
| Data | 2 | REQ-GATE-005, REQ-GATE-012. Genuinely thin: other persisted shapes (session state, artifacts) are covered inside functional requirements on approval and storage. |
| Regulatory | 0 | Genuinely empty: the product is an open-source local tool with no identified regulatory obligation; security posture lives under Non-functional (security). |
| Constraint | 8 | Ingestion bounds, Product Manager isolation, SHA pinning, platform and toolchain pins, and the 64 KiB context bound: REQ-GATE-003, REQ-GATE-004, REQ-RELEASE-005, REQ-COMPAT-001 to REQ-COMPAT-004, REQ-CONTEXT-005. |
| Reliability | 0 | Dismissed as a separate category: journaled commit and crash recovery are stated under REQ-GATE-009; no standalone reliability target has been set. |
| Scalability | 0 | Genuinely empty: Plan-Forge is a single-user local tool; the only scale bounds are the ingestion bounds (REQ-GATE-003) and the 64 KiB context bound (REQ-CONTEXT-005). |
| Maintainability | 0 | Genuinely empty: upheld through repository gates (lint, typecheck, CI under REQ-RELEASE-001) rather than stated as product requirements. |
| Portability | 0 | Dismissed as a separate category: platform coverage is stated under Constraint (REQ-COMPAT-001, REQ-COMPAT-003, REQ-COMPAT-004); no further portability requirement is identified. |
| Documentation | 0 | Dismissed as a separate category: the README and skill-install documentation ship as release artifacts (REQ-RELEASE-003), not as product requirements. |
| Privacy | 0 | Dismissed as a separate category: the egress allowlist, the untrusted-input posture, the no-scan rule for the user's own text and the context recipients are stated under Non-functional (security) (REQ-SEC-001, REQ-SEC-002, REQ-SEC-004, REQ-SEC-007, REQ-CONTEXT-006, REQ-CONTEXT-007). |

## Glossary

- plan forge gate: the pre-planning stage that turns a raw brief into an approved synthesized spec and plan manifest before implementation begins.
- Product Manager isolation: the property that nothing derived from the repository reaches the Product Manager avatar; the operational posture is stated by REQ-GATE-004.
- agent harness: the host-mediated coding agent session that receives the approved plan manifest and executes it.
- host: a supported local agent CLI — Claude Code, oh-my-pi (OMP) or Antigravity (AGY).
- skill: a named instruction package installed under a host's skill root; Claude Code and Antigravity invoke it as a slash command, while oh-my-pi receives no skill and reaches Plan-Forge through the hook-registered `/plan-forge` command.
- hook: a host-registered callback (session start, pre tool use or post tool use) through which Plan-Forge reports plan status, guards edits against a changed plan or hands off to plan mode.
- plan mode: a mode of the host, not of Plan-Forge — the host's own planning mode, entered after approval, in which the agent plans without editing files.
- plan manifest: `.plan/plan_manifest.json`, the machine-readable approved plan with task DAG, targets and verification commands.
- synthesized spec: `.plan/synthesized_spec.md`, the human-readable approved specification consumed by the agent harness as its blueprint.
- approval ledger: `PlanForgeHome()/approvals.json`, the local record of plan manifest hashes approved on this machine.
- task: one unit of planned work from the plan manifest, identified `T-<n>`.
- evidence: repository state observed by the planstate scan — AST targets and verification receipts — from which plan status is derived.
- drifted: the state of the plan manifest, or of a task, when the repository no longer matches the approved artifacts — an invalid manifest, a hash mismatch or an invalid target — so evidence cannot be trusted.
- managed: written by the installer and recorded in the install manifest with the `plan-forge managed` marker, so re-install and uninstall know exactly what Plan-Forge owns.
- relay: the `plan-forge-relay` server that mirrors session state between clients in Remote collaboration mode.
- avatar: one deliberation role in the plan forge gate: Product Manager, Technical Architect, Domain Specialist, Red-Team Critic or Lead Synthesizer.
- AST: abstract syntax tree; the repository grounding produced by the Tree-sitter scan.
- DAG: directed acyclic graph; the dependency structure of tasks in the plan manifest.
- PRD: product requirements document; the raw or normalized input to the plan forge gate.
- CSP: content security policy; the response header the local bridge serves the UI under (REQ-SEC-006).
- PERT: program evaluation and review technique; the three-point estimate input (optimistic, likely, pessimistic) behind the dual effort schedules (REQ-GATE-010).
- TTY: an interactive terminal; the installer prompts only when stdin and stdout are TTYs (REQ-INSTALL-006).
- decision card: the prompt the plan forge gate shows when deliberation needs a human choice — two to four options with a recommendation (REQ-GATE-007).
- composer: the Page 1 editor where the user writes the brief; its text, plus ingested files, becomes the PRD.
- interpretation call: the normalization model call that turns the composed text and ingested files into a `NormalizedPRD` and its epics (REQ-GATE-002, REQ-GATE-012).
- epic file: one epic returned by the interpretation call, shown on Page 1 as a file the user can open (REQ-UI-012). It is not the Product Manager's epic of REQ-GATE-005, which deliberation produces later.
- code component: a repository path, written relative to the repository root, that the component browser lists and an epic file can link (REQ-UI-011, REQ-UI-013).
- context gatherer: the `internal/gather` package, which reads project memory and git state without a model call and hands the result to the Technical Architect, the Domain Specialists and the Red-Team Critic (REQ-CONTEXT-001).
- recall index: a SQLite FTS5 database of project memory written by the `agents` tool, at `<git-common-dir>/agents-local/recall.db` or the legacy `.agents/index/recall.db` (REQ-CONTEXT-002).

## Open questions

1. Release gap versus the one-command install. The one-command install fetches the latest published release, which is `v0.1.1` and predates every feature in the HANDOFF and UI areas of this catalogue (confirmed: `git ls-tree v0.1.1 cmd/client/` has no `cmd_validate.go`). Whether the README's one-command install is expected to deliver those features, and when a tag carrying them is cut, is undecided. Blocking: release timing. Answerable by: @danielckv.
2. oh-my-pi dual surface. **Settled 2026-10-19:** the skill is dropped and oh-my-pi is command-only (REQ-INSTALL-008). One consequence is open in its place: a skill can be matched from its description and a registered command cannot, so a prose request no longer reaches Plan Forge in oh-my-pi. Whether that is worth a session-start hint line — which would make every session chattier — is undecided. Answerable by: @danielckv.
3. Command center sprint titles. **Closed 2026-10-22:** the command center was removed, so REQ-LIVE-003 is retired (`docs/adrs/0001-remove-live-task-views.md`).
4. Command center hook budget. **Closed 2026-10-22:** the command center was removed, so REQ-LIVE-006 is retired (`docs/adrs/0001-remove-live-task-views.md`).
5. Antigravity hook file shape. `internal/host/hooks.go` writes events at the top level of `~/.gemini/config/hooks.json` (`{"SessionStart":[...]}`), while `integrations/agy-locations.md` documents the nested form (`{"plan-forge":{"SessionStart":[...]}}`); the installed file on this machine uses the flat form. Whether Antigravity reads the flat form — and therefore whether the plan-status hook fires at all — is unverified; this is why REQ-INSTALL-009 is partly met. Settled by reading Antigravity's hook documentation or observing the session-start banner. Answerable by: whoever can inspect the Antigravity CLI hooks loader (owner: @danielckv).
6. Antigravity wait timeout. The `{{WAIT_TIMEOUT}}` template value for AGY is 50 s "until its limit is verified" per the system plan; the actual AGY tool-call limit has not been confirmed. Blocking: AGY documentation or empirical check. Answerable by: @danielckv.
7. Windows cross-`go vet`. The Windows cross-`go vet` cannot run on this machine because tree-sitter needs cgo; CI covers it. Whether local pre-push verification of the Windows build is required is undecided. Blocking: none for correctness (CI gate exists); a local verification option needs a Windows runner or container. Answerable by: @danielckv.
8. Subjective polish measure. REQ-UI-009 is verified only by objective proxies (component set shipped, gates green); a measurable usability target (task-completion study, SUS score) has not been set, so REQ-UI-009 is partly met. Blocking: a target from the owner. Answerable by: @danielckv.
9. Egress check untested. `internal/relay/client_conn.go` implements the egress guard (`NewEgressGuard`, `Contains`, `Redact`, `RedactJSON`) and `internal/app/remote.go` applies it, but no test in the tree exercises it, so REQ-SEC-004 is partly met. Blocking: a test that feeds absolute paths and credential-shaped content through `Redact` and `RedactJSON` and asserts none reaches the wire. Answerable by: @danielckv.
10. Scan bound untested. The worker cap, the 2-second parse timeout and the 120-second scan budget are defined in `internal/ast/scanner.go` but no test asserts them or the `timed_out` partial result, so REQ-PERF-002 is partly met. Blocking: a scan test with a shortened timeout asserting `coverage.timed_out` and a partial graph. Answerable by: @danielckv.
11. Reserved `verify` activity source. **Closed 2026-10-22:** the board and its activity sources were removed (`docs/adrs/0001-remove-live-task-views.md`).
12. Antigravity automatic plan-mode entry. REQ-HANDOFF-001 delivers automatic plan-mode entry and validation for Claude Code and oh-my-pi only; Antigravity receives the plan-mode hint text (REQ-HANDOFF-006) and the user enters plan mode themselves, so REQ-HANDOFF-001 is partly met. Whether an automatic Antigravity mechanism exists, or the hint is the accepted ceiling, is undecided. Blocking: Antigravity API surface for session mode control. Answerable by: @danielckv.
13. Remaining compound requirements. The 2026-10-19 audit counted 31 compound requirements; six were split in this pass (REQ-INSTALL-002, REQ-SEC-003, REQ-GATE-007, REQ-PERF-004, REQ-HANDOFF-005, REQ-RELEASE-001). Of the remaining 25, nine left with the retired areas on 2026-10-22 (REQ-UI-008, REQ-BOARD-001, REQ-BOARD-003, REQ-BOARD-005, REQ-BOARD-007, REQ-COORD-003, REQ-GRAPH-002, REQ-GRAPH-004, REQ-GRAPH-005). The other 16 stay compound until a follow-up pass: REQ-GATE-005, REQ-GATE-006, REQ-GATE-008, REQ-GATE-010, REQ-UI-001, REQ-UI-002, REQ-UI-003, REQ-UI-006, REQ-HANDOFF-001, REQ-HANDOFF-004, REQ-INSTALL-001, REQ-INSTALL-004, REQ-INSTALL-005, REQ-INSTALL-006, REQ-RELEASE-004, REQ-SEC-005. Blocking: a scheduled editorial pass; none is functionally wrong, each just carries more than one intent. Answerable by: @danielckv.
14. Context, epics and composer in flight. On 2026-10-22 the maintainer approved the context gatherer, interpretation epics, the Page 1 composer and the removal of the composer secret scan. The code is being written now, so 17 requirements are `not met`: REQ-CONTEXT-001 to REQ-CONTEXT-009, REQ-GATE-012, REQ-GATE-013, REQ-UI-010 to REQ-UI-014 and REQ-SEC-007. Each flips to `met` when its named test lands and passes. Four still name no test: REQ-UI-014 (epic modal), REQ-CONTEXT-007 (never to the Product Manager), REQ-CONTEXT-008 (context shown in the window) and REQ-SEC-007 (no scan, no Confirm gate). REQ-CONTEXT-009 has no command-level test. REQ-CONTEXT-004 does not yet fix how many recent commit subjects the gatherer records. Blocking: the implementation and its tests. Answerable by: @danielckv.

## Review log

- 2026-10-22 · REQ-BOARD-001..007, REQ-COORD-001..007, REQ-GRAPH-001..005, REQ-LIVE-001..006, REQ-UI-008, REQ-PERF-004, REQ-PERF-005, REQ-PERF-007 · retired: the plan board, the coordinator, the Graph view, discoveries, the command center and swarm reporting were removed; session start no longer starts a daemon or opens a tab; the IDs are listed under Retired requirements and are never reused · evidence: `docs/adrs/0001-remove-live-task-views.md`; `internal/board/`, `internal/center/`, `internal/livestore/`, `internal/planstate/taskboard.go`, `web/src/board/` and `web/src/center/` are deleted from the tree.
- 2026-10-22 · REQ-INSTALL-002 · the Claude Code hook set is now `SessionStart` (`hook check-state`), `PreToolUse` with matcher `Edit|Write|MultiEdit|NotebookEdit` (`hook claude-guard`) and `PostToolUse` with matcher `Bash` (`hook claude`, plan-mode handoff only); `UserPromptSubmit`, the command center handler and the `plan-forge-board` skill are no longer installed · evidence: `claudeHookEvents`, `claudePreToolUseMatcher` and `claudePostToolUseMatcher` in `internal/host/hooks.go`; `TestManagedHooksStartNothingAtSessionStart`.
- 2026-10-22 · REQ-INSTALL-003, REQ-INSTALL-007, REQ-INSTALL-008, REQ-INSTALL-009, REQ-INSTALL-010 · `plan-forge-board` and `/plan-forge-board` removed from the statements; REQ-INSTALL-007 and REQ-INSTALL-008 now say oh-my-pi receives no skill copy, which the 2026-10-19 REQ-INSTALL-008 entry below already recorded but the statement still contradicted · evidence: `planOMP` in `internal/host/installer.go`; one `registerCommand` call in `integrations/omp-hook-plan-forge.ts`.
- 2026-10-22 · REQ-INSTALL-011, REQ-INSTALL-012, REQ-INSTALL-013 · new: an install or uninstall strips the managed handlers 0.3.0 wrote under `UserPromptSubmit` or for the command center; an install deletes an owned `plan-forge-board` skill copy only while its hash matches; `center`, `board` and `swarm` print one stderr line and exit 0 until 0.5.0 · evidence: `TestMergeClaudeHookUpgradesAPreviousInstall`, `TestSplitManagedHookRemovesRetiredHandlers`, `TestClaudeInstallRemovesRetiredBoardSkill`, `TestRetiredCommandsExitCleanly`.
- 2026-10-22 · REQ-HANDOFF-007, REQ-SEC-002, REQ-SEC-005, REQ-PERF-001, REQ-UI-005, REQ-UI-007, Timing-bound precedence · references to the board delta, the board CLI, `web/src/board/board.css` and the board timing rows removed · evidence: `docs/adrs/0001-remove-live-task-views.md`; `cmd/client/cmd_board.go` and `web/src/board/board.css` are deleted.
- 2026-10-22 · REQ-SEC-007 · new: the composed requirements text is not scanned for secrets; the regex and entropy scan, `/api/secrets`, `/api/secrets/resolve` and the Confirm gate are removed because the brief is the user's own text and the entropy rule flagged repository paths; size, UTF-8 and file-ingestion bounds, relay authentication, the egress guard, the tool-free tripwire and Mermaid sanitizing stay; status `not met` until the removal lands (open question 14) · evidence: `docs/adrs/0003-no-text-scanning-in-composer.md`.
- 2026-10-22 · REQ-CONTEXT-001..009 · new area: the context gatherer reads the recall index read-only or falls back to the memory and docs files, adds git state, caps output at 64 KiB with repository-relative paths, feeds the Technical Architect, Domain Specialist and Red-Team Critic payloads only, is shown in the window and is printed by `plan-forge context`; status `not met` until the code and tests land (open question 14) · evidence: `docs/adrs/0002-read-agents-recall-index.md`.
- 2026-10-22 · REQ-GATE-012, REQ-GATE-013, REQ-UI-010..014 · new: the interpretation call returns `epics[]` grouped from existing requirements; Page 1 gains a markdown editor, a code component browser, epic files linked to real code components and an epic modal; status `not met` until the code and tests land (open question 14) · evidence: maintainer decision 2026-10-22.
- 2026-10-22 · Purpose and scope, Category coverage, Glossary, Open questions, Traceability · scope names the context gatherer; counts recomputed from the `Category:` fields to 77; glossary drops board, discovery, path node, coordinator, overlay, consumer, cursor, delta and live column, rewrites skill, hook, task, evidence and DAG, and adds composer, interpretation call, epic file, code component, context gatherer and recall index; open questions 3, 4 and 11 closed, 13 trimmed to 16 compound requirements, 14 added; traceability rows follow the same removals and additions · evidence: this change.
- 2026-10-19 · REQ-LIVE-001..006 · new area added for the command center: a loopback daemon that records harness sessions with or without an approved plan, serves several tabs, and surfaces the coordinator's up-next queue · evidence: `internal/livestore/`, `internal/center/`, `internal/app/center.go`, `web/src/center/`, `web/e2e/center.spec.ts`.
- 2026-10-19 · REQ-INSTALL-008 · the oh-my-pi artifact set drops both `SKILL.md` copies; oh-my-pi is reached through built-in commands only, and an install removes copies an earlier version wrote · evidence: `planOMP` in `internal/host/installer.go` plans `commands/plan-config.md` and the hook factory only; `TestOMPPlansCommandsAndHookButNoSkills`, `TestApplyRemovesSkillsFromAnEarlierOMPInstall`.
- 2026-10-19 · REQ-INSTALL-002 · Claude Code's managed `SessionStart` carries two handlers, the plan-status banner and the command center launch · evidence: `claudeCenterCommand` in `internal/host/hooks.go`; `TestMergeClaudeHookSessionStartCarriesBothHandlers`, `TestMergeClaudeHookUpgradesASingleHandlerInstall`.
- 2026-10-19 · REQ-INSTALL-007 · the premise flips: oh-my-pi should list no plan-forge skill at all, so any visible one shadows a built-in command and is reported as a conflict rather than only a cross-root duplicate · evidence: `SkillConflict` and `inspectSurface` in `internal/host/installer.go`; `TestOMPDuplicateWarningRequiresOptIn`.
- 2026-10-19 · REQ-HANDOFF-008 · new: a managed `PreToolUse` hook refuses an edit once when the approved plan changed under a running session, and refuses an edit aimed at the approved plan itself; it decides through the JSON contract with exit 0 and never emits `allow`, `defer` or a top-level decision · evidence: `cmd/client/cmd_hook_guard.go`, `cmd/client/cmd_hook_guard_test.go`, `claudePreToolUseMatcher` in `internal/host/hooks.go`.
- 2026-10-19 · REQ-INSTALL-010 · new: `doctor` verifies the managed hooks are installed and point at this binary, which matters because dropping oh-my-pi's skills removed the fallback for `/plan-forge` · evidence: `internal/host/hookcheck.go`, `internal/host/hookcheck_test.go`, `printHookStatuses` in `cmd/client/cmd_doctor.go`.
- 2026-10-19 · REQ-SEC-004 · acceptance criteria no longer cite `internal/relay/protocol_test.go`; they cite the implementation (`NewEgressGuard`, `Contains`, `Redact`, `RedactJSON`) and state that no egress test exists; status `met` → `partly met`; open question 9 added · evidence: traceability verification — `protocol_test.go` covers envelope encode/decode, counters, bounds and the timeout clamp, and no test in the tree references the egress guard.
- 2026-10-19 · REQ-PERF-002 · acceptance criteria no longer cite `internal/ast/symbols_test.go`; they cite `DefaultParseTimeout`, `DefaultScanTimeout` and the `min(4, runtime.GOMAXPROCS(0))` worker cap in `internal/ast/scanner.go` and state the bound is defined but not directly tested; status `met` → `partly met`; open question 10 added · evidence: traceability verification — `symbols_test.go` has no worker-count, parse-timeout, scan-timeout or `timed_out` assertion.
- 2026-10-19 · REQ-RELEASE-002 · the `CGO_ENABLED=0 go build -tags server ./cmd/relay` build moved from CI job `go-server` to `release.yml` job `relay`, which also asserts the closure; `go-server` is cited only for vet, test and the closure assertion · evidence: `ci.yml` `go-server` runs vet, test and the closure assertion only; the build is in `release.yml` job `relay`.
- 2026-10-19 · REQ-RELEASE-004 · the claim that `relay-image` runs only on version tags is replaced: `relay-image` runs on every push event and publishes `:main` and `:sha-<7>` for branch pushes; only `publish` is tag-gated; statement narrowed to release archives · evidence: `release.yml` `relay-image` has `if: github.event_name == 'push'`, `publish` has `if: startsWith(github.ref, 'refs/tags/v')`.
- 2026-10-19 · REQ-RELEASE-001 · job names corrected to `actionlint`, `markdownlint`, `editorconfig`, `links`, `go-shared`, `go-server`, `web`, `web-e2e`, `go-client` and `deploy-script`; `shellcheck` is a step inside `deploy-script`, not a job · evidence: job keys in `.github/workflows/ci.yml`.
- 2026-10-19 · REQ-COORD-001 · statement now names the coordinator's real-time inputs — harness file edits and `board report` calls — and says verification receipts count as plan evidence only through the periodic evidence re-scan · evidence: `cmd/client/cmd_verify.go` records nothing on the board, and receipts reach the board through `boardEvidenceEvery = 5 * time.Second` in `internal/app/board.go`.
- 2026-10-19 · REQ-COORD-002 · statement and acceptance criteria now cover `board report` calls (source `report`) and document `SourceVerify` as a reserved source with no producer today; open question 11 added · evidence: `SourceVerify` is defined in `internal/board/discovery.go` and referenced only by `validSource` and a test, and nothing in `cmd/client` or `internal/app` emits it.
- 2026-10-19 · REQ-HANDOFF-001 · statement scoped to the hosts with a supported mechanism (Claude Code via REQ-HANDOFF-002; oh-my-pi via REQ-HANDOFF-003 with the `/plan` prefill fallback); Antigravity named as hint-only per REQ-HANDOFF-006; status `met` → `partly met`; open question 12 added · evidence: REQ-HANDOFF-006 states the Antigravity handoff is the hint text only, and REQ-HANDOFF-003 carries the prefill fallback.
- 2026-10-19 · REQ-UI-009 · restated as a measurable usability outcome with the target pending; status `met` → `partly met`; open question 8 updated · evidence: the previous acceptance named no measurable target and left the judgement with the owner, contradicting `met`.
- 2026-10-19 · REQ-BOARD-002 · renamed Evidence column; the pre-Wave-4 blocked rule removed; REQ-COORD-003 now governs live-column and blocked precedence and the snapshot `column` (C11) · evidence: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C11 and `internal/board/coordinator.go`.
- 2026-10-19 · REQ-BOARD-004 · the 4-second timer clause replaced by a cross-reference (authority: REQ-PERF-007); acceptance gained `internal/board/delta_test.go` · evidence: the timer figure was duplicated in REQ-PERF-004; `delta_test.go` covers the delta text.
- 2026-10-19 · REQ-BOARD-005 · the 2-second clause replaced by a cross-reference to REQ-PERF-001 · evidence: REQ-PERF-001 owns the hook time bound (C6, C10).
- 2026-10-19 · REQ-HANDOFF-004 · the 20-second scan bound replaced by a cross-reference to REQ-PERF-003 · evidence: REQ-PERF-003 owns the `wait` scan bound (C1, C2).
- 2026-10-19 · REQ-HANDOFF-005 · split: the treat-as-data and permission-prompt intent moved to REQ-HANDOFF-007 · evidence: the catalogue standard of one intent per requirement; REQ-SEC-005 requires the same statement of every skill, hook and delta.
- 2026-10-19 · REQ-GATE-007 · split: the no-waiver rule moved to REQ-GATE-011 · evidence: the catalogue standard of one intent per requirement; `internal/avatars/avatars_test.go` shows an open HIGH critique blocks synthesis.
- 2026-10-19 · REQ-GRAPH-003 · claim narrowed: `.git/` and `docs/plans/.plan/` are never recorded as path nodes; acceptance cites `internal/board/discovery_test.go` where `MapPaths` is actually tested · evidence: `forbiddenPrefixes` in `internal/board/discovery.go`; `TestMapPaths` in `internal/board/discovery_test.go`.
- 2026-10-19 · REQ-GRAPH-005 · the 150-node figure replaced by a cross-reference (authority: REQ-PERF-005) · evidence: the figure was duplicated in both requirements.
- 2026-10-19 · REQ-INSTALL-002 · split into per-host artifact sets: REQ-INSTALL-002 (Claude Code), REQ-INSTALL-008 (oh-my-pi) and REQ-INSTALL-009 (Antigravity); category `Functional` → `Interface`; status `partly met` → `met` for the Claude Code set while REQ-INSTALL-009 carries the partly-met Antigravity hook question · evidence: the verified install run covers the Claude Code and oh-my-pi sets; open question 5 concerns only the Antigravity hook file shape.
- 2026-10-19 · REQ-INSTALL-007 · retitled "No duplicate skill visible to oh-my-pi"; premise corrected — only the Claude Code copies carry `enabled: false`, the oh-my-pi and Antigravity copies stay unmarked · evidence: the `enabled: false` comment block in `internal/host/installer.go` ("OMP's native copies and the AGY copies are NOT marked").
- 2026-10-19 · REQ-RELEASE-001 · split: the web-gate blocking intent moved to REQ-RELEASE-006, which states the actual `needs` chain · evidence: `.github/workflows/ci.yml` `go-client` and `.github/workflows/release.yml` `client` declare `needs: web`; `package` declares `needs: [client, relay]` and `publish`/`relay-image` declare `needs: package`.
- 2026-10-19 · REQ-RELEASE-002 and REQ-COMPAT-002 · the dependency-closure enumeration moved to REQ-COMPAT-002 as the single authority; REQ-RELEASE-002 cross-references it · evidence: both requirements stated the closure rule.
- 2026-10-19 · REQ-SEC-003 · split: loopback binding and nonce authentication stay (category `Non-functional (security)` → `Interface`); `Host`/`Origin` checks, the single UI WebSocket and the CSP moved to REQ-SEC-006 · evidence: the catalogue standard of one intent per requirement.
- 2026-10-19 · REQ-SEC-004 and REQ-PERF-002 · line numbers removed from the acceptance text; file and constant names kept · evidence: modifiability — line numbers rot under unrelated edits.
- 2026-10-19 · REQ-PERF-004 · split: the oh-my-pi 4-second sync timer moved to REQ-PERF-007; new subsection Timing-bound precedence assigns one authority per bound (120 s AST scan, 30 s `validate` default, 20 s `wait` scan, 10 s board evidence scan, 2 s board CLI, 5 s Claude Code hook, 2 s Antigravity hook) · evidence: `internal/app/board.go` constants, `claudeHookTimeout` in `internal/host/hooks.go`, the Antigravity `timeout: 2` hook entry.
- 2026-10-19 · Category coverage · recounted from the `Category:` fields after the splits and interface reclassifications — Functional 37, Interface 8, Constraint 7, usability/accessibility 3, summing to 78; "Covered across all areas" removed; dispositions added for Reliability, Scalability, Maintainability, Portability, Documentation and Privacy · evidence: the `Category:` field of every entry; the bridge-auth note conflicted with REQ-SEC-003 and now cites it.
- 2026-10-19 · Glossary · Product Manager isolation, overlay, coordinator and discovery rewritten as definitions rather than restatements; board, skill, plan mode and coordinator reconciled with their usage; entries added for consumer, cursor, delta, live column, evidence, drifted, managed, relay, avatar, AST, DAG, PRD, CSP, PERT, TTY and decision card; "PTT" from the review's term list appears nowhere in the catalogue or its cited sources, so no definition was invented · evidence: term usage across this catalogue and `docs/plans/GUI_BOARD_HANDOFF_PLAN.md`; PERT fields in `internal/synthesizer/gantt.go`; the TTY rule in REQ-INSTALL-006.
- 2026-10-19 · Purpose and scope · section added stating the catalogue's purpose and scope so the orphan test is decidable · evidence: the review finding that no purpose/scope statement existed.
- 2026-10-19 · Traceability · fourteen rows reconciled with the per-requirement `Traces to` and `Acceptance` fields (REQ-GATE-002, REQ-GATE-007, REQ-GATE-009, REQ-UI-001, REQ-UI-005, REQ-UI-007, REQ-BOARD-004, REQ-COORD-002, REQ-GRAPH-003, REQ-RELEASE-003, REQ-RELEASE-004, REQ-SEC-004, REQ-HANDOFF-006, REQ-INSTALL-002) and rows added for the seven split IDs; the first contract use names `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` · evidence: file-by-file check of the cited tests — `web/e2e/app.spec.ts` has no breakpoint case, `web/src/ui/prefs.test.ts` has no reduced-motion case, and `MapPaths` is tested only in `internal/board/discovery_test.go`.

## Traceability

| REQ | Contract | Implementing files | Tests |
| :--- | :--- | :--- | :--- |
| REQ-HANDOFF-008 | C19 | `cmd/client/cmd_hook_guard.go`, `internal/host/hooks.go` | `cmd/client/cmd_hook_guard_test.go`, `internal/host/hooks_test.go` |
| REQ-INSTALL-010 | C21 | `internal/host/hookcheck.go`, `cmd/client/cmd_doctor.go` | `internal/host/hookcheck_test.go` |
| REQ-INSTALL-011 | — | `internal/host/hooks.go`, `internal/host/installer.go` | `internal/host/hooks_test.go` |
| REQ-INSTALL-012 | — | `internal/host/installer.go` | `internal/host/installer_test.go` |
| REQ-INSTALL-013 | — | `cmd/client/main.go` | `cmd/client/retired_test.go` |
| REQ-GATE-001 | — | `internal/app/`, `web/src/pages/` | `web/e2e/bridge.spec.ts` |
| REQ-GATE-002 | — | `internal/ingestion/ingest.go`, `internal/ingestion/normalizer.go` | `internal/ingestion/ingest_test.go`, `internal/ingestion/normalizer_test.go`, `internal/ingestion/fixtures_test.go` |
| REQ-GATE-003 | — | `internal/ingestion/bounds.go` | `internal/ingestion/fixtures_test.go` |
| REQ-GATE-004 | — | `internal/avatars/prompts.go`, `internal/avatars/orchestrator.go` | `internal/avatars/avatars_test.go`, `internal/avatars/prompts_test.go` |
| REQ-GATE-005 | — | `internal/contracts/types.go`, `internal/avatars/stage.go` | `internal/contracts/schema_test.go`, `internal/avatars/avatars_test.go` |
| REQ-GATE-006 | — | `internal/avatars/orchestrator.go`, `internal/avatars/stage.go` | `internal/avatars/avatars_test.go` |
| REQ-GATE-007 | — | `internal/avatars/decisions.go`, `internal/session/reducer.go` | `internal/session/reducer_test.go`, `internal/avatars/avatars_test.go` |
| REQ-GATE-008 | — | `internal/synthesizer/compiler.go`, `internal/synthesizer/mermaid.go` | `internal/synthesizer/synthesizer_test.go` |
| REQ-GATE-009 | — | `internal/storage/artifacts.go`, `internal/session/reducer.go` | `internal/storage/storage_test.go`, `internal/session/reducer_test.go`, `web/e2e/bridge.spec.ts` |
| REQ-GATE-010 | — | `internal/synthesizer/gantt.go` | `internal/synthesizer/synthesizer_test.go` |
| REQ-GATE-011 | — | `internal/avatars/orchestrator.go`, `internal/avatars/decisions.go` | `internal/avatars/avatars_test.go` |
| REQ-GATE-012 | — | `internal/ingestion/normalizer.go`, `internal/contracts/types.go` | `internal/ingestion/normalizer_test.go` (to be added) |
| REQ-GATE-013 | — | `internal/ingestion/normalizer.go` | `internal/ingestion/normalizer_test.go` (to be added) |
| REQ-CONTEXT-001 | — | `internal/gather/` | `internal/gather/gather_test.go` (to be added) |
| REQ-CONTEXT-002 | — | `internal/gather/` | `internal/gather/gather_test.go` (to be added) |
| REQ-CONTEXT-003 | — | `internal/gather/` | `internal/gather/gather_test.go` (to be added) |
| REQ-CONTEXT-004 | — | `internal/gather/` | `internal/gather/gather_test.go` (to be added) |
| REQ-CONTEXT-005 | — | `internal/gather/` | `internal/gather/gather_test.go` (to be added) |
| REQ-CONTEXT-006 | — | `internal/gather/` | `internal/gather/gather_test.go` (to be added) |
| REQ-CONTEXT-007 | — | `internal/avatars/prompts.go`, `internal/gather/` | None named yet (open question 14) |
| REQ-CONTEXT-008 | — | `web/src/pages/Page1Ingestion.tsx`, `internal/app/` | None named yet (open question 14) |
| REQ-CONTEXT-009 | — | `cmd/client/`, `internal/gather/` | `internal/gather/gather_test.go` (to be added); no command-level test named |
| REQ-UI-001 | C8 | `web/src/App.tsx`, `web/src/pages/`, `web/src/store/syncStore.ts` | `web/src/ui/phase.test.ts`, `web/e2e/bridge.spec.ts` |
| REQ-UI-002 | — | `internal/window/launch.go` | `internal/window/launch_test.go` |
| REQ-UI-003 | — | `web/src/ui/PhaseStepper.tsx`, `web/src/components/Sidebar.tsx` | `web/src/ui/phase.test.ts` |
| REQ-UI-004 | — | `web/src/ui/ThemeSelect.tsx`, `web/src/ui/prefs.ts` | `web/src/ui/prefs.test.ts` |
| REQ-UI-005 | — | `web/src/app.css` | CSS inspection: a `prefers-reduced-motion: reduce` block |
| REQ-UI-006 | — | `web/src/ui/shortcuts.ts`, `web/src/ui/ShortcutHelp.tsx`, `web/src/ui/focusTrap.ts` | `web/src/ui/shortcuts.test.ts` |
| REQ-UI-007 | — | `web/src/app.css` | CSS inspection: `@media (max-width: 1099.98px)` breakpoint |
| REQ-UI-009 | — | `web/src/ui/`, `web/src/app.css` | `npm --prefix web test` (suite) |
| REQ-UI-010 | — | `web/src/pages/Page1Ingestion.tsx`, `web/src/codemirror/` | `web/src/ui/docSync.test.ts` (to be added) |
| REQ-UI-011 | — | `web/src/pages/Page1Ingestion.tsx`, `internal/app/` | `internal/app/planfiles_test.go` (to be added) |
| REQ-UI-012 | — | `web/src/pages/Page1Ingestion.tsx`, `internal/app/` | `internal/app/planfiles_test.go` (to be added) |
| REQ-UI-013 | — | `internal/app/` | `internal/app/planfiles_test.go` (to be added) |
| REQ-UI-014 | — | `web/src/pages/Page1Ingestion.tsx`, `web/src/ui/Dialog.tsx` | None named yet (open question 14) |
| REQ-HANDOFF-001 | C6, C7 | `cmd/client/cmd_hook.go`, `integrations/omp-hook-plan-forge.ts` | `cmd/client/cmd_hook_test.go`, `integrations/omp-hook-plan-forge.test.ts` |
| REQ-HANDOFF-002 | C6 | `cmd/client/cmd_hook.go`, `internal/host/hooks.go` | `cmd/client/cmd_hook_test.go` |
| REQ-HANDOFF-003 | C7 | `integrations/omp-hook-plan-forge.ts` | `integrations/omp-hook-plan-forge.test.ts` |
| REQ-HANDOFF-004 | C1, C2 | `cmd/client/cmd_validate.go`, `cmd/client/cmd_session.go`, `internal/planstate/validate.go` | `cmd/client/cmd_validate_test.go`, `internal/planstate/validate_test.go` |
| REQ-HANDOFF-005 | C6, C7 | `cmd/client/cmd_hook.go`, `integrations/skill-plan-forge.md` | `cmd/client/cmd_hook_test.go` |
| REQ-HANDOFF-006 | C6 | `integrations/skill-plan-forge.md`, `internal/host/installer.go` | `internal/host/installer_test.go` |
| REQ-HANDOFF-007 | C6, C7 | `cmd/client/cmd_hook.go`, `integrations/skill-plan-forge.md`, `integrations/omp-hook-plan-forge.ts` | `cmd/client/cmd_hook_test.go` |
| REQ-INSTALL-001 | — | `scripts/install.sh`, `scripts/install.ps1`, `internal/host/installer.go` | `internal/host/installer_test.go` |
| REQ-INSTALL-002 | C6 | `internal/host/installer.go`, `internal/host/hooks.go`, `integrations/` | `internal/host/installer_test.go`, `internal/host/hooks_test.go` |
| REQ-INSTALL-003 | — | `internal/host/installer.go`, `integrations/` | `internal/host/installer_test.go` |
| REQ-INSTALL-004 | — | `internal/host/installer.go` | `internal/host/installer_test.go` |
| REQ-INSTALL-005 | — | `internal/host/installer.go`, `cmd/client/cmd_uninstall.go` | `internal/host/installer_test.go` |
| REQ-INSTALL-006 | — | `internal/host/installer.go`, `cmd/client/cmd_install.go` | `internal/host/installer_test.go` |
| REQ-INSTALL-007 | — | `internal/host/installer.go` | `internal/host/installer_test.go` |
| REQ-INSTALL-008 | C7 | `internal/host/installer.go`, `internal/host/hooks.go`, `integrations/` | `internal/host/installer_test.go`, `internal/host/hooks_test.go`, `integrations/omp-hook-plan-forge.test.ts` |
| REQ-INSTALL-009 | C6 | `internal/host/installer.go`, `internal/host/hooks.go`, `integrations/` | `internal/host/installer_test.go`, `internal/host/hooks_test.go` |
| REQ-RELEASE-001 | — | `.github/workflows/ci.yml`, `.github/workflows/release.yml` | CI workflow runs |
| REQ-RELEASE-002 | — | `.github/workflows/release.yml`, `.github/workflows/ci.yml`, `internal/buildguard/`, `cmd/relay/guard.go` | `relay` release job; `go-server` CI job |
| REQ-RELEASE-003 | — | `scripts/package-release.sh`, `scripts/verify-archive.sh`, `.github/workflows/release.yml` | Checkpoint 6 commands in the system plan |
| REQ-RELEASE-004 | — | `.github/workflows/release.yml`, `scripts/install.sh`, `scripts/install.ps1` | `release.yml` `publish`/`relay-image` job conditions; tags `v0.1.0` and `v0.1.1` published |
| REQ-RELEASE-005 | — | `.github/workflows/ci.yml`, `.github/workflows/release.yml` | Workflow inspection |
| REQ-RELEASE-006 | — | `.github/workflows/ci.yml`, `.github/workflows/release.yml` | `needs` chain inspection: `go-client` and `client` need `web` |
| REQ-SEC-001 | — | `internal/planstate/ledger.go`, `internal/planstate/planstate.go`, `cmd/client/cmd_hook.go` | `internal/planstate/validate_test.go` |
| REQ-SEC-002 | C1 | `internal/sanitize/sanitize.go`, `cmd/client/cmd_hook.go` | `internal/sanitize/sanitize_test.go` |
| REQ-SEC-003 | — | `internal/relay/local_ws.go`, `internal/relay/client_conn.go` | `internal/relay/local_ws_test.go`, `web/e2e/bridge.spec.ts` |
| REQ-SEC-004 | — | `internal/relay/client_conn.go`, `internal/app/remote.go` | None: no dedicated egress test exists; `internal/relay/protocol_test.go` does not cover it |
| REQ-SEC-005 | C6 | `internal/host/claude.go`, `cmd/client/cmd_hook.go`, `integrations/skill-plan-forge.md` | `internal/host/host_test.go`, `cmd/client/cmd_hook_test.go` |
| REQ-SEC-006 | — | `internal/relay/local_ws.go`, `internal/relay/client_conn.go` | `internal/relay/local_ws_test.go`, `web/e2e/bridge.spec.ts` |
| REQ-SEC-007 | — | `internal/app/api.go`, `web/src/pages/Page1Ingestion.tsx` | None named yet (open question 14) |
| REQ-PERF-001 | C6 | `cmd/client/cmd_hook.go` | `cmd/client/cmd_hook_test.go` |
| REQ-PERF-002 | — | `internal/ast/scanner.go`, `internal/ast/parse.go` | None direct: bound defined by constants in `internal/ast/scanner.go`; `internal/ast/symbols_test.go` asserts none of it |
| REQ-PERF-003 | C1, C2 | `cmd/client/cmd_validate.go`, `cmd/client/cmd_session.go` | `cmd/client/cmd_validate_test.go`, `internal/planstate/validate_test.go` |
| REQ-PERF-006 | — | `internal/relay/hub.go` | `internal/relay/hub_integration_test.go` |
| REQ-COMPAT-001 | — | `.github/workflows/release.yml` | Release `client` matrix runs |
| REQ-COMPAT-002 | — | `internal/buildguard/`, `cmd/client/guard.go`, `cmd/relay/guard.go` | `go-server` CI job |
| REQ-COMPAT-003 | — | `web/.nvmrc`, `web/package.json` | `npm --prefix web run typecheck` in CI |
| REQ-COMPAT-004 | — | `go.mod`, `.github/workflows/` | All Go CI jobs |
