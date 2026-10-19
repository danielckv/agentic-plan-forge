# Plan-Forge requirements catalogue

Catalogue standard: every requirement is a single sentence of the form `{Actor/subsystem} shall {behaviour}`. IDs are `REQ-<AREA>-<nnn>`, stable, three-digit, and never renumbered. Levels: `B` business, `S` stakeholder, `F` solution/functional; a requirement is reviewed at the level it is written, so a `B` or `S` requirement is not defective for lacking solution-level numbers. Owner is `@danielckv` for every requirement (the CODEOWNERS owner); no other owner is invented. `Last reviewed` is the date the requirement was checked against the tree. Status is judged against the code in the tree on that date: `met`, `partly met`, or `not met`; every `partly met` or `not met` requirement also appears in Open questions. Glossary terms are used exactly as defined in the Glossary throughout.

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
- Statement: The plan forge gate shall present human decision cards with two to four distinct options and a recommendation, resolve them first-valid-write-wins at the expected revision, and never let a human choice waive CRITICAL or HIGH defects, invalid contracts or safety invariants.
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

## UI — the window

### REQ-UI-001 — Three sequential views

- ID: REQ-UI-001
- Level: S
- Statement: The user shall move through three distinct, sequential views — ingestion, deliberation, master specification — with explicit phase-driven transitions and never a single scrolling page.
- Category: User
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 5 (three-page embedded UI).
- Acceptance criteria: `web/src/ui/phase.test.ts`; `web/e2e/bridge.spec.ts` (a manual URL jump to `/synthesis` during Page 1 is redirected).
- Traces to: `web/src/App.tsx`, `web/src/pages/`, `web/src/store/syncStore.ts`
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
- Category: Non-functional (accessibility)
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C12 (`prefers-reduced-motion` disables graph animations).
- Acceptance criteria: `web/src/app.css` and `web/src/board/board.css` contain `prefers-reduced-motion` blocks.
- Traces to: `web/src/app.css`, `web/src/board/board.css`, `web/src/ui/prefs.ts`
- Status: met
- Last reviewed: 2026-10-19

### REQ-UI-006 — Keyboard operation

- ID: REQ-UI-006
- Level: F
- Statement: The window shall provide keyboard shortcuts with an in-app shortcut reference and shall trap focus inside modal dialogs.
- Category: Non-functional (usability)
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
- Category: Non-functional (usability)
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` Goal 1.
- Acceptance criteria: `web/src/app.css` (`@media (max-width: 1099.98px)`) and `web/src/board/board.css` (`@media (max-width: 1180px)`) breakpoints.
- Traces to: `web/src/app.css`, `web/src/board/board.css`
- Status: met
- Last reviewed: 2026-10-19

### REQ-UI-008 — Board and Graph as views

- ID: REQ-UI-008
- Level: F
- Statement: The window shall provide a board view and a graph view as switchable views with a `Board | Graph` header switch and a coordinator panel.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C8 and C12 (Graph view header).
- Acceptance criteria: `web/src/board/viewMode.test.ts`; `web/src/board/BoardApp.tsx` renders `GraphView`.
- Traces to: C8, C12; `web/src/board/BoardApp.tsx`, `web/src/board/graph/GraphView.tsx`, `web/src/board/CoordinatorPanel.tsx`
- Status: met
- Last reviewed: 2026-10-19

### REQ-UI-009 — Executive-grade presentation

- ID: REQ-UI-009
- Level: B
- Statement: The product shall present a fluent, flexible and polished interface suitable for planning real products in research and commercial contexts.
- Category: Business
- Owner: @danielckv
- Source: User request this session (2026-10-19): "a more fluent, flexible and nicer GUI".
- Acceptance criteria: Objective proxy — the W1 component set (`web/src/ui/`) ships and `npm --prefix web run lint && npm --prefix web run typecheck && npm --prefix web test` are green; subjective judgement remains with the owner.
- Traces to: `web/src/ui/`, `web/src/app.css`
- Status: met
- Last reviewed: 2026-10-19

## HANDOFF — plan-mode handoff and validation

### REQ-HANDOFF-001 — Automatic handoff

- ID: REQ-HANDOFF-001
- Level: S
- Statement: Once the plan is approved in the plan forge gate, the agent harness shall enter its own plan mode automatically and validation shall run without manual intervention.
- Category: User
- Owner: @danielckv
- Source: User request this session (2026-10-19): "once the plan is ready from the forge the agent harness switches to its `/plan` mode automatically and validation is triggered".
- Acceptance criteria: `cmd/client/cmd_hook_test.go` (Claude path), `integrations/omp-hook-plan-forge.test.ts` (OMP path).
- Traces to: C6, C7; `cmd/client/cmd_hook.go`, `integrations/omp-hook-plan-forge.ts`
- Status: met
- Last reviewed: 2026-10-19

### REQ-HANDOFF-002 — Claude mechanism

- ID: REQ-HANDOFF-002
- Level: F
- Statement: For Claude Code, a managed `PostToolUse` hook shall detect an approved `plan-forge wait` result and inject an `EnterPlanMode` directive together with the sanitized validation summary as `additionalContext`.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C6.
- Acceptance criteria: `cmd/client/cmd_hook_test.go`; `cmd/client/cmd_hook.go` emits the `EnterPlanMode` directive.
- Traces to: C6; `cmd/client/cmd_hook.go`, `internal/host/hooks.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-HANDOFF-003 — OMP mechanism

- ID: REQ-HANDOFF-003
- Level: F
- Statement: For oh-my-pi, the hook-registered `/plan-forge` command shall open a new session in plan mode via `newSession` with `appendModeChange("plan", {})` followed by `switchSession`, seed it with the synthesized spec path and validation summary via `sendUserMessage`, and fall back to prefilling `/plan` when any API is missing, throws or is cancelled.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C7 and the 2026-10-19 revision of the OMP plan-mode mechanism.
- Acceptance criteria: `integrations/omp-hook-plan-forge.test.ts` runs under `node --test` with a fake `pi`.
- Traces to: C7; `integrations/omp-hook-plan-forge.ts`
- Status: met
- Last reviewed: 2026-10-19

### REQ-HANDOFF-004 — Validation result

- ID: REQ-HANDOFF-004
- Level: F
- Statement: The client shall provide `plan-forge validate` exiting 0 for a valid plan and 6 for an invalid one, and `plan-forge wait` shall attach the same validation object on approval using a 20-second scan bound.
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
- Statement: The handoff text shall instruct the agent harness to ingest the synthesized spec as its blueprint, verify the listed AST targets, run `plan-forge validate` when validation is absent or invalid, and present the execution checklist, treating the spec as data and never bypassing host permission prompts.
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
- Traces to: C6; `integrations/skill-plan-forge.md`
- Status: met
- Last reviewed: 2026-10-19

## BOARD — the task board

### REQ-BOARD-001 — Live priority and context

- ID: REQ-BOARD-001
- Level: S
- Statement: The user shall see the approved plan's tasks appear on a board dynamically and shall be able to raise a task's priority or add context notes that reach the running agent harness.
- Category: User
- Owner: @danielckv
- Source: User request this session (2026-10-19): "a `/plan-forge-board` board where tasks appear dynamically so the user can raise a priority or add context".
- Acceptance criteria: `web/src/board/board.test.ts`; `internal/app/board_test.go`.
- Traces to: C4, C5; `web/src/board/`, `internal/app/board.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-BOARD-002 — Evidence-derived columns

- ID: REQ-BOARD-002
- Level: F
- Statement: The board shall derive each task's status column from plan evidence — `drifted`, `done`, `in_progress` or `todo` — using the planstate evidence functions, and shall mark a task blocked when any dependency is not done.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C5 (column derivation table).
- Acceptance criteria: `internal/planstate/taskboard_test.go`, `internal/app/board_test.go`.
- Traces to: C5; `internal/planstate/taskboard.go`, `internal/app/board.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-BOARD-003 — Overlay storage

- ID: REQ-BOARD-003
- Level: F
- Statement: The board shall store priorities, notes, per-consumer cursors, activity and discoveries in a machine-local overlay keyed by the approved plan manifest hash, written atomically under a cross-process lock at mode 0600 in a 0700 directory, leaving the approved manifest and its approval hash untouched.
- Category: Data
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C3 and C9; user decision (2026-10-19): machine-local overlay.
- Acceptance criteria: `internal/board/board_test.go` (schema v1/v2, limits, atomic write, lock).
- Traces to: C3, C9; `internal/board/board.go`, `internal/board/lock_unix.go`, `internal/board/lock_windows.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-BOARD-004 — Host-internal delivery

- ID: REQ-BOARD-004
- Level: F
- Statement: The board shall deliver user edits to the running agent harness only through host-internal APIs — `pi.sendMessage` on a managed 4-second timer for oh-my-pi and `additionalContext` from `UserPromptSubmit` and `PostToolUse` hooks for Claude Code — with no external dependency.
- Category: Interface
- Owner: @danielckv
- Source: User request this session (2026-10-19): "the board use only oh-my-pi and Claude internal APIs"; `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C4, C6, C7.
- Acceptance criteria: `integrations/omp-hook-plan-forge.test.ts` (timer, `deliverAs` steer/nextTurn), `cmd/client/cmd_hook_test.go` (delta appended to hook context).
- Traces to: C4, C6, C7; `integrations/omp-hook-plan-forge.ts`, `cmd/client/cmd_hook.go`, `internal/board/delta.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-BOARD-005 — Hook-safe CLI

- ID: REQ-BOARD-005
- Level: F
- Statement: The board CLI subcommands `pull`, `report`, `discover` and `activity` shall be hook-safe: an invalid flag or consumer id exits 2, any other failure prints nothing and exits 0, each run finishes within 2 seconds, and without an approved plan each is a silent no-op.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C4 and C10.
- Acceptance criteria: `cmd/client/cmd_board_test.go`.
- Traces to: C4, C10; `cmd/client/cmd_board.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-BOARD-006 — Single instance per repository

- ID: REQ-BOARD-006
- Level: F
- Statement: The board shall hold a per-repository lock so that a second `plan-forge board --detach` against the same repository reports `already_open` instead of starting a second instance.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C4.
- Acceptance criteria: `cmd/client/cmd_board_lock_unix_test.go`.
- Traces to: C4; `cmd/client/cmd_board.go`, `internal/app/boardguard_unix.go`, `internal/app/boardguard_windows.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-BOARD-007 — Board bridge API

- ID: REQ-BOARD-007
- Level: F
- Statement: The board server shall expose a Bearer-authenticated `GET /api/board` snapshot, single-action `POST /api/board/task`, and `POST /api/board/discovery` routes, and shall push one snapshot frame on connect and one whenever the serialized snapshot changes.
- Category: Interface
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C5 and C11.
- Acceptance criteria: `internal/app/board_test.go` (200/400/404/409 cases, snapshot push).
- Traces to: C5, C11; `internal/app/board.go`
- Status: met
- Last reviewed: 2026-10-19

## COORD — the coordinator

### REQ-COORD-001 — Real-time movement from evidence of work

- ID: REQ-COORD-001
- Level: S
- Statement: The user shall see a coordinator move tasks across the board in real time from what the agent harness actually does, not only from periodic AST evidence.
- Category: User
- Owner: @danielckv
- Source: User request this session (2026-10-19): "a real-time coordinator that moves tasks"; `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` Wave 4 goal.
- Acceptance criteria: `internal/board/coordinator_test.go`; `web/src/board/wave4.test.ts`.
- Traces to: C9, C11, C12; `internal/board/coordinator.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-COORD-002 — Harness activity capture

- ID: REQ-COORD-002
- Level: F
- Statement: The coordinator shall record agent harness activity — Claude `PostToolUse` edit tools and oh-my-pi `tool_result` edit or write calls — into the overlay via the `board activity` path with the per-host consumer id.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C10 and C12.
- Acceptance criteria: `cmd/client/cmd_hook_test.go` (edit tools recorded in-process), `integrations/omp-hook-plan-forge.test.ts` (fire-and-forget capture), `cmd/client/cmd_board_test.go`.
- Traces to: C10, C12; `cmd/client/cmd_hook.go`, `integrations/omp-hook-plan-forge.ts`, `cmd/client/cmd_board.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-COORD-003 — Live columns and awaiting verification

- ID: REQ-COORD-003
- Level: F
- Statement: The coordinator shall compute each task's live column by the ordered rules — evidence drifted, evidence done, activity done with `awaiting_verification: true`, activity active or evidence in progress, otherwise todo — and mark a task blocked when any dependency's live column is not done or its activity is blocked.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C9 (coordination rules).
- Acceptance criteria: `internal/board/coordinator_test.go`.
- Traces to: C9; `internal/board/coordinator.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-COORD-004 — Blocked recomputation

- ID: REQ-COORD-004
- Level: F
- Statement: The coordinator shall recompute blocked state and live columns whenever overlay activity or plan evidence changes, so a completed dependency unblocks its dependents without user action.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C9 (`Coordinate` is a pure function over evidence plus overlay) and C11 (push after any store change).
- Acceptance criteria: `internal/board/coordinator_test.go`; `internal/app/board_test.go` (snapshot reflects store changes).
- Traces to: C9, C11; `internal/board/coordinator.go`, `internal/app/board.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-COORD-005 — Up-next queue

- ID: REQ-COORD-005
- Level: F
- Statement: The coordinator shall maintain an Up-next queue of at most 5 tasks that are not done, drifted, blocked or active, ordered by priority, then manifest order, then accepted discoveries.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C9 (`next_up` rules).
- Acceptance criteria: `internal/board/coordinator_test.go`.
- Traces to: C9; `internal/board/coordinator.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-COORD-006 — Steering only on queue-head change

- ID: REQ-COORD-006
- Level: F
- Statement: The coordinator shall append a `Coordinator: next up is ...` line to a consumer's board delta only when the head of the Up-next queue changed since that consumer's previous delivery, persisting `last_next_up` in the consumer's cursor.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` Wave 4 default and C9 (`RenderDelta`).
- Acceptance criteria: `internal/board/delta_v2_test.go`.
- Traces to: C9; `internal/board/delta.go`, `internal/board/coordinator.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-COORD-007 — Coordinator panel

- ID: REQ-COORD-007
- Level: F
- Statement: The window shall show a coordinator panel listing Active now and Up next beside the `Board | Graph` switch.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C12 (Graph view header panel).
- Acceptance criteria: `web/src/board/wave4.test.ts`; `web/src/board/CoordinatorPanel.tsx`.
- Traces to: C11, C12; `web/src/board/CoordinatorPanel.tsx`, `web/src/board/coordinator.ts`
- Status: met
- Last reviewed: 2026-10-19

## GRAPH — the Graph view

### REQ-GRAPH-001 — Plan as a graph

- ID: REQ-GRAPH-001
- Level: S
- Statement: The user shall see the plan as a graph of task nodes with dependency edges, including work the agent harness discovered beyond the plan.
- Category: User
- Owner: @danielckv
- Source: User request this session (2026-10-19): "a graph of task nodes so the user can see tasks or paths the harness discovered beyond the plan"; `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` Wave 4.
- Acceptance criteria: `web/src/board/graph/layout.test.ts`; `web/src/board/wave4.test.ts`.
- Traces to: C12; `web/src/board/graph/GraphView.tsx`, `web/src/board/graph/GraphNodes.tsx`
- Status: met
- Last reviewed: 2026-10-19

### REQ-GRAPH-002 — Discovered task nodes

- ID: REQ-GRAPH-002
- Level: F
- Statement: The graph shall show each discovery of work outside the plan as a proposed, dashed task node linked to its parent, and only the user shall accept a discovery onto the board.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` Wave 4 defaults and C9 (discovery rules and caps).
- Acceptance criteria: `internal/board/discovery_test.go` (caps, de-duplication, `ErrDiscoveryLimit`), `web/src/board/wave4.test.ts`.
- Traces to: C9, C12; `internal/board/discovery.go`, `web/src/board/graph/DiscoveryPanel.tsx`
- Status: met
- Last reviewed: 2026-10-19

### REQ-GRAPH-003 — Path nodes

- ID: REQ-GRAPH-003
- Level: F
- Statement: The graph shall show a path node for every repository path the agent harness touched, matched to planned targets by `MapPaths` and grouped under Unplanned when no target matches.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C9 (`MapPaths`), C10 (`board activity`) and C12 (path pills).
- Acceptance criteria: `internal/board/coordinator_test.go` or `internal/board/discovery_test.go` (`MapPaths` cases), `web/src/board/wave4.test.ts`.
- Traces to: C9, C10, C12; `internal/board/discovery.go`, `web/src/board/graph/GraphNodes.tsx`
- Status: met
- Last reviewed: 2026-10-19

### REQ-GRAPH-004 — Accept or dismiss

- ID: REQ-GRAPH-004
- Level: F
- Statement: The graph shall let the user accept a proposed discovery with a chosen priority or dismiss it, shall render proposed, accepted and dismissed discoveries distinctly, and shall hide dismissed discoveries behind a toggle.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C11 (`POST /api/board/discovery`) and C12 (interaction).
- Acceptance criteria: `internal/app/board_test.go` (discovery route 200/400/404/409), `web/src/board/wave4.test.ts`.
- Traces to: C11, C12; `internal/app/board.go`, `internal/board/discovery.go`, `web/src/board/graph/DiscoveryPanel.tsx`
- Status: met
- Last reviewed: 2026-10-19

### REQ-GRAPH-005 — Readable and operable at scale

- ID: REQ-GRAPH-005
- Level: F
- Statement: The graph shall use a layered DAG layout (longest-path layering by `depends_on`, then three barycenter ordering passes) with pan, zoom and fit controls, and shall stay responsive with 150 nodes.
- Category: Non-functional (performance)
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C12 (rendering, interaction, scale).
- Acceptance criteria: `web/src/board/graph/layout.test.ts`.
- Traces to: C12; `web/src/board/graph/layout.ts`, `web/src/board/graph/GraphView.tsx`
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

### REQ-INSTALL-002 — Per-host artifacts

- ID: REQ-INSTALL-002
- Level: F
- Statement: The installer shall write exactly these managed artifacts: for Claude Code the skills `plan-forge`, `plan-config` and `plan-forge-board` (each carrying `enabled: false`) plus `SessionStart`, `PostToolUse` (matcher `Bash|Edit|Write|MultiEdit|NotebookEdit`) and `UserPromptSubmit` hook entries with `timeout: 5` in `~/.claude/settings.json`; for oh-my-pi the skills `plan-forge` and `plan-config` (without `enabled: false`), the command file `commands/plan-config.md`, and the hook `hooks/pre/plan-forge.ts` registering `/plan-forge` and `/plan-forge-board`, with no `commands/plan-forge.md`; for Antigravity the skills `plan-forge` and `plan-config` in both `~/.gemini/antigravity-cli/skills/` and `~/.gemini/config/skills/` plus a `SessionStart` entry with `timeout: 2` in `~/.gemini/config/hooks.json`, and no board skill.
- Category: Functional
- Owner: @danielckv
- Source: Verified `plan-forge install --host auto --yes` run on this machine (2026-10-19); `internal/host/installer.go`; `internal/host/hooks.go`.
- Acceptance criteria: `internal/host/installer_test.go`, `internal/host/hooks_test.go`; every managed entry carries the `plan-forge managed` marker.
- Traces to: C6, C7; `internal/host/installer.go`, `internal/host/hooks.go`, `integrations/`
- Status: partly met
- Last reviewed: 2026-10-19

### REQ-INSTALL-003 — One skill name per host root

- ID: REQ-INSTALL-003
- Level: F
- Statement: The installer shall install each feature under one skill name per host root — `plan-forge`, `plan-config`, and where supported `plan-forge-board` — with no duplicate name in the same root.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 6 (per-host template paths).
- Acceptance criteria: `internal/host/installer_test.go` (install-manifest rows per path).
- Traces to: `internal/host/installer.go`, `integrations/`
- Status: met
- Last reviewed: 2026-10-19

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

### REQ-INSTALL-007 — No duplicate skill visible to a host

- ID: REQ-INSTALL-007
- Level: F
- Statement: The installer shall mark the Claude skill copies with `enabled: false` so that oh-my-pi, which also reads the Claude and Gemini skill roots, lists exactly one `plan-forge` skill.
- Category: Functional
- Owner: @danielckv
- Source: User request this session (2026-10-19): "no duplicate skill in oh-my-pi, which also reads skills from the Claude and Gemini roots"; `internal/host/installer.go` (the `enabled: false` comment block).
- Acceptance criteria: `internal/host/installer_test.go`; A/B verification on this machine (unmarked copies list two entries, marked copies list one).
- Traces to: `internal/host/installer.go`
- Status: met
- Last reviewed: 2026-10-19

## RELEASE — gates and artifacts

### REQ-RELEASE-001 — CI gates before release

- ID: REQ-RELEASE-001
- Level: F
- Statement: CI shall prove the shared, server, client, web, end-to-end and script gates — `go-shared`, `go-server`, `go-client`, `web`, `e2e` and `shellcheck` — on every push and pull request, and the web gate shall block the client and release jobs when it fails.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 6 (Release pipeline); repository invariant (web gates go-client and release).
- Acceptance criteria: `.github/workflows/ci.yml` job graph; `.github/workflows/release.yml` depends on the web job.
- Traces to: `.github/workflows/ci.yml`, `.github/workflows/release.yml`
- Status: met
- Last reviewed: 2026-10-19

### REQ-RELEASE-002 — Relay dependency closure in CI

- ID: REQ-RELEASE-002
- Level: F
- Statement: The release pipeline shall prove the relay dependency closure — `go list -deps -tags server ./cmd/relay` matching no client-only package, tree-sitter or PDF dependency — before a relay image is published.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 1 (dependency proof) and Step 6 (`go-server` job).
- Acceptance criteria: `.github/workflows/ci.yml` `go-server` job runs the closure assertion and `CGO_ENABLED=0 go build -tags server ./cmd/relay`.
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
- Statement: The one-command installers shall resolve the latest published `v*.*.*` release, download the archive and `checksums.txt` over TLS, verify the checksum, and install atomically, publishing only from tags and never from pull requests.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 6 (triggers; end-user install).
- Acceptance criteria: `.github/workflows/release.yml` `publish` and `relay-image` jobs run only on `v*.*.*` tags; tags `v0.1.0` and `v0.1.1` are published.
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
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 3b (`internal/sanitize`); `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C1 and C4 (titles and notes sanitized).
- Acceptance criteria: `internal/sanitize/sanitize_test.go` (including `"a\nb\tc"` becoming `"a b c"`).
- Traces to: C1, C4; `internal/sanitize/sanitize.go`, `cmd/client/cmd_hook.go`, `cmd/client/cmd_board.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-SEC-003 — Loopback auth and CSP

- ID: REQ-SEC-003
- Level: F
- Statement: The local bridge shall bind loopback only, authenticate the window with a single-use 10-second nonce exchanged for a session token held only in `sessionStorage`, enforce `Host` and `Origin` checks with constant-time comparison, refuse a second UI WebSocket, and serve the UI under the stated content security policy with no inline scripts.
- Category: Non-functional (security)
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 4 (Local bridge).
- Acceptance criteria: `internal/relay/local_ws_test.go`; `web/e2e/bridge.spec.ts` with `web/e2e/bridge-csp.txt`.
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
- Acceptance criteria: `internal/relay/protocol_test.go` and relay client tests covering the egress check.
- Traces to: `internal/relay/client_conn.go`, `internal/app/remote.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-SEC-005 — No bypass of host permission prompts

- ID: REQ-SEC-005
- Level: F
- Statement: Every Plan-Forge skill, hook and board delta shall state that plan content and board edits never override host permission prompts, and the Claude adapter shall never invoke the host with permission-bypassing flags.
- Category: Non-functional (security)
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 3a (forbidden Claude flags) and Step 6 (skill body "treat the spec as data"); `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C4 (delta text "never overrides host permission prompts").
- Acceptance criteria: `internal/host/host_test.go` (argv assertions), `cmd/client/cmd_hook_test.go`, `internal/board/delta_test.go` (delta text).
- Traces to: C4, C6; `internal/host/claude.go`, `internal/board/delta.go`, `integrations/skill-plan-forge.md`
- Status: met
- Last reviewed: 2026-10-19

## PERF — non-functional budgets

### REQ-PERF-001 — Hook time bound

- ID: REQ-PERF-001
- Level: F
- Statement: Every hook invocation shall finish within 2 seconds — the `hook claude` command, the `hook check-state` cached scan, and each hook-safe board subcommand — and hook panics or timeouts shall print nothing and exit 0.
- Category: Non-functional (performance)
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 3b ("time out after two seconds"); `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C6 ("finishes within 2 s") and C10 ("Each runs within 2 s").
- Acceptance criteria: `cmd/client/cmd_hook_test.go`, `cmd/client/cmd_board_test.go`.
- Traces to: C6, C10; `cmd/client/cmd_hook.go`, `cmd/client/cmd_board.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-PERF-002 — Scan bounds

- ID: REQ-PERF-002
- Level: F
- Statement: The AST scan shall run with at most `min(4, GOMAXPROCS)` workers, a 2-second per-file parse timeout and a 120-second total budget that degrades to a partial graph marked `timed_out`.
- Category: Non-functional (performance)
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 2 (Budget) and Tree-sitter scanner bounds.
- Acceptance criteria: `internal/ast/symbols_test.go`; partial-scan coverage reported in `coverage.timed_out`.
- Traces to: `internal/ast/scanner.go`, `internal/ast/parse.go`
- Status: met
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

### REQ-PERF-004 — Board push latency

- ID: REQ-PERF-004
- Level: F
- Statement: The board server shall poll the overlay every 1 second and re-evaluate plan evidence every 5 seconds with a 10-second scan bound, so agent harness activity appears on the board within about 1 second, and oh-my-pi shall sync board edits on a 4-second timer.
- Category: Non-functional (performance)
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C5 (push intervals), C11 (about 1 s) and C7 (4 s timer); `internal/app/board.go` constants.
- Acceptance criteria: `internal/app/board.go` (`boardOverlayPoll = time.Second`, `boardEvidenceEvery = 5 * time.Second`, `boardScanTimeout = 10 * time.Second`); `internal/app/board_test.go`.
- Traces to: C5, C7, C11; `internal/app/board.go`, `integrations/omp-hook-plan-forge.ts`
- Status: met
- Last reviewed: 2026-10-19

### REQ-PERF-005 — Graph size

- ID: REQ-PERF-005
- Level: F
- Statement: The graph view shall stay responsive with 150 nodes.
- Category: Non-functional (performance)
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C12 (Scale).
- Acceptance criteria: `web/src/board/graph/layout.test.ts`.
- Traces to: C12; `web/src/board/graph/layout.ts`
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
- Statement: The relay server shall build with `CGO_ENABLED=0` under the `server` build tag without any client-only package, enforced by the build-tag guard that fails a `client server` build naming `BOTH_BUILD_TAGS_client_and_server_are_mutually_exclusive`.
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

## Category coverage

| Category | Count | Note |
| :--- | :--- | :--- |
| Functional | 40 | Covered across all areas. |
| Non-functional (security) | 5 | SEC area. |
| Non-functional (performance) | 7 | PERF area plus REQ-GRAPH-005. |
| Non-functional (usability/accessibility) | 2 | REQ-UI-005, REQ-UI-006. Genuinely thin: the GUI polish was delivered as one slice; further usability requirements await measurable targets from the owner. |
| Business | 2 | REQ-GATE-001, REQ-UI-009. |
| User | 6 | The S-level capability statements. |
| Interface | 2 | REQ-BOARD-004, REQ-BOARD-007. Genuinely thin: the relay wire protocol and bridge auth are specified in the system plan as design contracts, not restated here as requirements. |
| Data | 2 | REQ-GATE-005, REQ-BOARD-003. Genuinely thin: other persisted shapes (session state, artifacts) are covered inside functional requirements on approval and storage. |
| Regulatory | 0 | Genuinely empty: the product is an open-source local tool with no identified regulatory obligation; security posture lives under Non-functional (security). |
| Constraint | 6 | Ingestion bounds, PM isolation, SHA pinning, platform and toolchain pins. |

## Glossary

- plan forge gate: the pre-planning stage that turns a raw brief into an approved synthesized spec and plan manifest before implementation begins.
- Product Manager isolation: the Product Manager Avatar's posture — a fresh tool-free process in a private empty directory with no repository-derived context.
- agent harness: the host-mediated coding agent session that receives the approved plan and executes it.
- host: a supported local agent CLI — Claude Code, oh-my-pi (OMP) or Antigravity (AGY).
- skill: a named instruction package installed under a host's skill root and invoked as a slash command.
- hook: a host-registered callback (session start, pre/post tool use, prompt submit) through which Plan-Forge injects or captures context.
- plan mode: the host's own planning mode, entered after approval, in which the agent plans without editing files.
- plan manifest: `.plan/plan_manifest.json`, the machine-readable approved plan with task DAG, targets and verification commands.
- synthesized spec: `.plan/synthesized_spec.md`, the human-readable approved specification consumed by the agent harness as its blueprint.
- approval ledger: `PlanForgeHome()/approvals.json`, the local record of manifest hashes approved on this machine.
- board: the Plan-Forge window view that lays out the approved plan's tasks by status column.
- task: one unit of planned work from the plan manifest, identified `T-<n>`, or an accepted discovery `D-<n>`.
- discovery: a proposed item of work observed outside the plan — a new task or a touched path — that only the user accepts or dismisses.
- path node: a graph node representing a repository path the agent harness touched, linked to its parent task or grouped under Unplanned.
- coordinator: the component that computes live columns, blocked state, awaiting-verification and the Up-next queue from evidence plus overlay, and steers the agent harness only when the queue head changes.
- overlay: the machine-local store keyed by the approved plan manifest hash holding priorities, notes, cursors, activity and discoveries, leaving the approved artifacts untouched.

## Open questions

1. Release gap versus the one-command install. The one-command install fetches the latest published release, which is `v0.1.1` and predates every feature in the HANDOFF, BOARD, COORD, GRAPH and UI areas of this catalogue (confirmed: `git ls-tree v0.1.1 cmd/client/` has no `cmd_board.go` or `cmd_validate.go`). Whether the README's one-command install is expected to deliver those features, and when a tag carrying them is cut, is undecided. Blocking: release timing. Answerable by: @danielckv.
2. oh-my-pi dual surface. OMP exposes both `skill:plan-forge` and the hook-registered `/plan-forge` command for the same feature (the registered command replaced the markdown command per C7, but the skill remains installed). Whether both should exist, or the skill should be dropped, is undecided. Blocking: product decision on the OMP surface. Answerable by: @danielckv.
3. Antigravity hook file shape. `internal/host/hooks.go` writes events at the top level of `~/.gemini/config/hooks.json` (`{"SessionStart":[...]}`), while `integrations/agy-locations.md` documents the nested form (`{"plan-forge":{"SessionStart":[...]}}`); the installed file on this machine uses the flat form. Whether Antigravity reads the flat form — and therefore whether the plan-status hook fires at all — is unverified; this is why REQ-INSTALL-002 is partly met. Settled by reading Antigravity's hook documentation or observing the session-start banner. Answerable by: whoever can inspect the Antigravity CLI hooks loader (owner: @danielckv).
4. Antigravity wait timeout. The `{{WAIT_TIMEOUT}}` template value for AGY is 50 s "until its limit is verified" per the system plan; the actual AGY tool-call limit has not been confirmed. Blocking: AGY documentation or empirical check. Answerable by: @danielckv.
5. Windows cross-`go vet`. The Windows cross-`go vet` cannot run on this machine because tree-sitter needs cgo; CI covers it. Whether local pre-push verification of the Windows build is required is undecided. Blocking: none for correctness (CI gate exists); a local verification option needs a Windows runner or container. Answerable by: @danielckv.
6. Subjective polish measure. REQ-UI-009 is verified only by objective proxies (component set shipped, gates green); a measurable usability target (task-completion study, SUS score) has not been set. Blocking: a target from the owner. Answerable by: @danielckv.

## Traceability

| REQ | Contract | Implementing files | Tests |
| :--- | :--- | :--- | :--- |
| REQ-GATE-001 | — | `internal/app/`, `web/src/pages/` | `web/e2e/bridge.spec.ts` |
| REQ-GATE-002 | — | `internal/ingestion/ingest.go`, `internal/ingestion/normalizer.go` | `internal/ingestion/ingest_test.go`, `internal/ingestion/normalizer_test.go` |
| REQ-GATE-003 | — | `internal/ingestion/bounds.go` | `internal/ingestion/fixtures_test.go` |
| REQ-GATE-004 | — | `internal/avatars/prompts.go`, `internal/avatars/orchestrator.go` | `internal/avatars/avatars_test.go`, `internal/avatars/prompts_test.go` |
| REQ-GATE-005 | — | `internal/contracts/types.go`, `internal/avatars/stage.go` | `internal/contracts/schema_test.go`, `internal/avatars/avatars_test.go` |
| REQ-GATE-006 | — | `internal/avatars/orchestrator.go`, `internal/avatars/stage.go` | `internal/avatars/avatars_test.go` |
| REQ-GATE-007 | — | `internal/avatars/decisions.go`, `internal/session/reducer.go` | `internal/session/reducer_test.go` |
| REQ-GATE-008 | — | `internal/synthesizer/compiler.go`, `internal/synthesizer/mermaid.go` | `internal/synthesizer/synthesizer_test.go` |
| REQ-GATE-009 | — | `internal/storage/artifacts.go`, `internal/session/reducer.go` | `internal/storage/storage_test.go`, `web/e2e/bridge.spec.ts` |
| REQ-GATE-010 | — | `internal/synthesizer/gantt.go` | `internal/synthesizer/synthesizer_test.go` |
| REQ-UI-001 | C8 | `web/src/App.tsx`, `web/src/pages/`, `web/src/store/syncStore.ts` | `web/src/ui/phase.test.ts`, `web/e2e/bridge.spec.ts` |
| REQ-UI-002 | — | `internal/window/launch.go` | `internal/window/launch_test.go` |
| REQ-UI-003 | — | `web/src/ui/PhaseStepper.tsx`, `web/src/components/Sidebar.tsx` | `web/src/ui/phase.test.ts` |
| REQ-UI-004 | — | `web/src/ui/ThemeSelect.tsx`, `web/src/ui/prefs.ts` | `web/src/ui/prefs.test.ts` |
| REQ-UI-005 | C12 | `web/src/app.css`, `web/src/board/board.css` | `web/src/ui/prefs.test.ts` |
| REQ-UI-006 | — | `web/src/ui/shortcuts.ts`, `web/src/ui/ShortcutHelp.tsx`, `web/src/ui/focusTrap.ts` | `web/src/ui/shortcuts.test.ts` |
| REQ-UI-007 | — | `web/src/app.css`, `web/src/board/board.css` | `web/e2e/app.spec.ts` |
| REQ-UI-008 | C8, C12 | `web/src/board/BoardApp.tsx`, `web/src/board/graph/GraphView.tsx` | `web/src/board/viewMode.test.ts` |
| REQ-UI-009 | — | `web/src/ui/`, `web/src/app.css` | `npm --prefix web test` (suite) |
| REQ-HANDOFF-001 | C6, C7 | `cmd/client/cmd_hook.go`, `integrations/omp-hook-plan-forge.ts` | `cmd/client/cmd_hook_test.go`, `integrations/omp-hook-plan-forge.test.ts` |
| REQ-HANDOFF-002 | C6 | `cmd/client/cmd_hook.go`, `internal/host/hooks.go` | `cmd/client/cmd_hook_test.go` |
| REQ-HANDOFF-003 | C7 | `integrations/omp-hook-plan-forge.ts` | `integrations/omp-hook-plan-forge.test.ts` |
| REQ-HANDOFF-004 | C1, C2 | `cmd/client/cmd_validate.go`, `cmd/client/cmd_session.go`, `internal/planstate/validate.go` | `cmd/client/cmd_validate_test.go`, `internal/planstate/validate_test.go` |
| REQ-HANDOFF-005 | C6, C7 | `cmd/client/cmd_hook.go`, `integrations/skill-plan-forge.md` | `cmd/client/cmd_hook_test.go` |
| REQ-HANDOFF-006 | C6 | `integrations/skill-plan-forge.md`, `internal/host/installer.go` | `internal/host/installer_test.go` |
| REQ-BOARD-001 | C4, C5 | `web/src/board/`, `internal/app/board.go` | `web/src/board/board.test.ts`, `internal/app/board_test.go` |
| REQ-BOARD-002 | C5 | `internal/planstate/taskboard.go`, `internal/app/board.go` | `internal/planstate/taskboard_test.go`, `internal/app/board_test.go` |
| REQ-BOARD-003 | C3, C9 | `internal/board/board.go`, `internal/board/lock_unix.go`, `internal/board/lock_windows.go` | `internal/board/board_test.go` |
| REQ-BOARD-004 | C4, C6, C7 | `integrations/omp-hook-plan-forge.ts`, `cmd/client/cmd_hook.go`, `internal/board/delta.go` | `integrations/omp-hook-plan-forge.test.ts`, `cmd/client/cmd_hook_test.go`, `internal/board/delta_test.go` |
| REQ-BOARD-005 | C4, C10 | `cmd/client/cmd_board.go` | `cmd/client/cmd_board_test.go` |
| REQ-BOARD-006 | C4 | `cmd/client/cmd_board.go`, `internal/app/boardguard_unix.go`, `internal/app/boardguard_windows.go` | `cmd/client/cmd_board_lock_unix_test.go` |
| REQ-BOARD-007 | C5, C11 | `internal/app/board.go` | `internal/app/board_test.go` |
| REQ-COORD-001 | C9, C11, C12 | `internal/board/coordinator.go` | `internal/board/coordinator_test.go`, `web/src/board/wave4.test.ts` |
| REQ-COORD-002 | C10, C12 | `cmd/client/cmd_hook.go`, `integrations/omp-hook-plan-forge.ts`, `cmd/client/cmd_board.go` | `cmd/client/cmd_hook_test.go`, `integrations/omp-hook-plan-forge.test.ts` |
| REQ-COORD-003 | C9 | `internal/board/coordinator.go` | `internal/board/coordinator_test.go` |
| REQ-COORD-004 | C9, C11 | `internal/board/coordinator.go`, `internal/app/board.go` | `internal/board/coordinator_test.go`, `internal/app/board_test.go` |
| REQ-COORD-005 | C9 | `internal/board/coordinator.go` | `internal/board/coordinator_test.go` |
| REQ-COORD-006 | C9 | `internal/board/delta.go`, `internal/board/coordinator.go` | `internal/board/delta_v2_test.go` |
| REQ-COORD-007 | C11, C12 | `web/src/board/CoordinatorPanel.tsx`, `web/src/board/coordinator.ts` | `web/src/board/wave4.test.ts` |
| REQ-GRAPH-001 | C12 | `web/src/board/graph/GraphView.tsx`, `web/src/board/graph/GraphNodes.tsx` | `web/src/board/graph/layout.test.ts`, `web/src/board/wave4.test.ts` |
| REQ-GRAPH-002 | C9, C12 | `internal/board/discovery.go`, `web/src/board/graph/DiscoveryPanel.tsx` | `internal/board/discovery_test.go`, `web/src/board/wave4.test.ts` |
| REQ-GRAPH-003 | C9, C10, C12 | `internal/board/discovery.go`, `web/src/board/graph/GraphNodes.tsx` | `internal/board/discovery_test.go` |
| REQ-GRAPH-004 | C11, C12 | `internal/app/board.go`, `internal/board/discovery.go`, `web/src/board/graph/DiscoveryPanel.tsx` | `internal/app/board_test.go`, `web/src/board/wave4.test.ts` |
| REQ-GRAPH-005 | C12 | `web/src/board/graph/layout.ts`, `web/src/board/graph/GraphView.tsx` | `web/src/board/graph/layout.test.ts` |
| REQ-INSTALL-001 | — | `scripts/install.sh`, `scripts/install.ps1`, `internal/host/installer.go` | `internal/host/installer_test.go` |
| REQ-INSTALL-002 | C6, C7 | `internal/host/installer.go`, `internal/host/hooks.go`, `integrations/` | `internal/host/installer_test.go`, `internal/host/hooks_test.go` |
| REQ-INSTALL-003 | — | `internal/host/installer.go`, `integrations/` | `internal/host/installer_test.go` |
| REQ-INSTALL-004 | — | `internal/host/installer.go` | `internal/host/installer_test.go` |
| REQ-INSTALL-005 | — | `internal/host/installer.go`, `cmd/client/cmd_uninstall.go` | `internal/host/installer_test.go` |
| REQ-INSTALL-006 | — | `internal/host/installer.go`, `cmd/client/cmd_install.go` | `internal/host/installer_test.go` |
| REQ-INSTALL-007 | — | `internal/host/installer.go` | `internal/host/installer_test.go` |
| REQ-RELEASE-001 | — | `.github/workflows/ci.yml`, `.github/workflows/release.yml` | CI workflow runs |
| REQ-RELEASE-002 | — | `.github/workflows/ci.yml`, `internal/buildguard/`, `cmd/relay/guard.go` | `go-server` CI job |
| REQ-RELEASE-003 | — | `scripts/package-release.sh`, `scripts/verify-archive.sh` | Checkpoint 6 commands in the system plan |
| REQ-RELEASE-004 | — | `.github/workflows/release.yml`, `scripts/install.sh`, `scripts/install.ps1` | Checkpoint 6 install test in the system plan |
| REQ-RELEASE-005 | — | `.github/workflows/ci.yml`, `.github/workflows/release.yml` | Workflow inspection |
| REQ-SEC-001 | — | `internal/planstate/ledger.go`, `internal/planstate/planstate.go`, `cmd/client/cmd_hook.go` | `internal/planstate/validate_test.go` |
| REQ-SEC-002 | C1, C4 | `internal/sanitize/sanitize.go`, `cmd/client/cmd_hook.go`, `cmd/client/cmd_board.go` | `internal/sanitize/sanitize_test.go` |
| REQ-SEC-003 | — | `internal/relay/local_ws.go`, `internal/relay/client_conn.go` | `internal/relay/local_ws_test.go`, `web/e2e/bridge.spec.ts` |
| REQ-SEC-004 | — | `internal/relay/client_conn.go`, `internal/app/remote.go` | `internal/relay/protocol_test.go` |
| REQ-SEC-005 | C4, C6 | `internal/host/claude.go`, `internal/board/delta.go`, `integrations/skill-plan-forge.md` | `internal/host/host_test.go`, `cmd/client/cmd_hook_test.go`, `internal/board/delta_test.go` |
| REQ-PERF-001 | C6, C10 | `cmd/client/cmd_hook.go`, `cmd/client/cmd_board.go` | `cmd/client/cmd_hook_test.go`, `cmd/client/cmd_board_test.go` |
| REQ-PERF-002 | — | `internal/ast/scanner.go`, `internal/ast/parse.go` | `internal/ast/symbols_test.go` |
| REQ-PERF-003 | C1, C2 | `cmd/client/cmd_validate.go`, `cmd/client/cmd_session.go` | `cmd/client/cmd_validate_test.go`, `internal/planstate/validate_test.go` |
| REQ-PERF-004 | C5, C7, C11 | `internal/app/board.go`, `integrations/omp-hook-plan-forge.ts` | `internal/app/board_test.go` |
| REQ-PERF-005 | C12 | `web/src/board/graph/layout.ts` | `web/src/board/graph/layout.test.ts` |
| REQ-PERF-006 | — | `internal/relay/hub.go` | `internal/relay/hub_integration_test.go` |
| REQ-COMPAT-001 | — | `.github/workflows/release.yml` | Release `client` matrix runs |
| REQ-COMPAT-002 | — | `internal/buildguard/`, `cmd/client/guard.go`, `cmd/relay/guard.go` | `go-server` CI job |
| REQ-COMPAT-003 | — | `web/.nvmrc`, `web/package.json` | `npm --prefix web run typecheck` in CI |
| REQ-COMPAT-004 | — | `go.mod`, `.github/workflows/` | All Go CI jobs |
