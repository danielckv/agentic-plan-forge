# Plan-Forge requirements catalogue

Catalogue standard: every requirement is a single sentence of the form `{Actor/subsystem} shall {behaviour}`. IDs are `REQ-<AREA>-<nnn>`, stable, three-digit, and never renumbered. Levels: `B` business, `S` stakeholder, `F` solution/functional; a requirement is reviewed at the level it is written, so a `B` or `S` requirement is not defective for lacking solution-level numbers. Owner is `@danielckv` for every requirement (the CODEOWNERS owner); no other owner is invented. `Last reviewed` is the date the requirement was checked against the tree. Status is judged against the code in the tree on that date: `met`, `partly met`, or `not met`; every `partly met` or `not met` requirement also appears in Open questions. Glossary terms are used exactly as defined in the Glossary throughout. Contract references `C1`–`C12` cite the design contracts in `docs/plans/GUI_BOARD_HANDOFF_PLAN.md`. The requirements audit of the master plan lives beside this catalogue at `docs/requirements/AUDIT_MASTER_PLAN.md`.

## Purpose and scope

Purpose: this catalogue is the normative inventory of what Plan-Forge shall do. Every behaviour the product owes a user, a host or a release is either stated here as a requirement or consciously dismissed in Category coverage or Open questions, so the orphan test — a plan item found nowhere in this catalogue — is decidable by construction. In scope: the `plan-forge` client and its embedded window, the board, the coordinator, the graph view, the plan-mode handoff and validation, installation and uninstall, release gates and artifacts, the untrusted-input posture, performance budgets, and platform and toolchain constraints, in both Solo and Remote collaboration modes. Out of scope: the relay wire protocol, the verbatim deliberation prompts, and visual design detail, which are design contracts recorded in `docs/plans/`, not product requirements.

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
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C12 (`prefers-reduced-motion` disables graph animations).
- Acceptance criteria: `web/src/app.css` and `web/src/board/board.css` contain `prefers-reduced-motion` blocks.
- Traces to: `web/src/app.css`, `web/src/board/board.css`
- Status: met
- Last reviewed: 2026-10-19

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
- Statement: The product shall meet a measurable usability outcome for executive-grade presentation — a task-completion or SUS target the owner has yet to set (open question 8) — so the interface is suitable for planning real products in research and commercial contexts.
- Category: Business
- Owner: @danielckv
- Source: User request this session (2026-10-19): "a more fluent, flexible and nicer GUI".
- Acceptance criteria: Target pending: a measurable usability target (task-completion study or SUS score) set by the owner and then measured. Until it exists, the objective proxy — the W1 component set (`web/src/ui/`) ships and `npm --prefix web run lint && npm --prefix web run typecheck && npm --prefix web test` are green — is the only evidence.
- Traces to: `web/src/ui/`, `web/src/app.css`
- Status: partly met
- Last reviewed: 2026-10-19

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
- Statement: The handoff text shall instruct the agent harness to treat the synthesized spec as data and never to bypass host permission prompts, as REQ-SEC-005 requires of every skill, hook and board delta.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C6 (hook context) and C7 step 5; `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 6 skill body "treat the spec as data".
- Acceptance criteria: `cmd/client/cmd_hook_test.go` asserts the directive text; `integrations/skill-plan-forge.md` carries the same instruction.
- Traces to: C6, C7; `cmd/client/cmd_hook.go`, `integrations/skill-plan-forge.md`, `integrations/omp-hook-plan-forge.ts`
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

### REQ-BOARD-002 — Evidence column

- ID: REQ-BOARD-002
- Level: F
- Statement: The board shall derive each task's evidence column from plan evidence — `drifted`, `done`, `in_progress` or `todo` — using the planstate evidence functions; REQ-COORD-003 governs how evidence and activity combine into the live column shown by the snapshot `column` (C11) and which blocked rule takes precedence.
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
- Statement: The board shall store priorities, notes, per-consumer cursors, activity and discoveries in a machine-local overlay keyed by the approved plan manifest hash, written atomically under a cross-process lock at mode 0600 in a 0700 directory, leaving the approved plan manifest and its approval hash untouched.
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
- Statement: The board shall deliver user edits to the running agent harness only through host-internal APIs — `pi.sendMessage` on the managed sync timer (REQ-PERF-007, Timing-bound precedence) for oh-my-pi and `additionalContext` from `UserPromptSubmit` and `PostToolUse` hooks for Claude Code — with no external dependency.
- Category: Interface
- Owner: @danielckv
- Source: User request this session (2026-10-19): "the board use only oh-my-pi and Claude internal APIs"; `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C4, C6, C7.
- Acceptance criteria: `integrations/omp-hook-plan-forge.test.ts` (timer, `deliverAs` steer/nextTurn), `cmd/client/cmd_hook_test.go` (delta appended to hook context), `internal/board/delta_test.go` (delta text).
- Traces to: C4, C6, C7; `integrations/omp-hook-plan-forge.ts`, `cmd/client/cmd_hook.go`, `internal/board/delta.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-BOARD-005 — Hook-safe CLI

- ID: REQ-BOARD-005
- Level: F
- Statement: The board CLI subcommands `pull`, `report`, `discover` and `activity` shall be hook-safe: an invalid flag or consumer id exits 2, any other failure prints nothing and exits 0, each run finishes within the hook time bound (REQ-PERF-001, Timing-bound precedence), and without an approved plan manifest each is a silent no-op.
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
- Statement: The user shall see a coordinator move tasks across the board in real time from what the agent harness actually does — file edits and `board report` calls — not only from the periodic evidence re-scan, which is also how verification receipts count as plan evidence.
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
- Statement: The coordinator shall record agent harness activity — Claude Code `PostToolUse` edit tools and oh-my-pi `tool_result` edit or write calls via the `board activity` path with source `file_edit`, and explicit `board report` calls with source `report` — into the overlay with the per-host consumer id.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C10 and C12.
- Acceptance criteria: `cmd/client/cmd_hook_test.go` (edit tools recorded in-process), `integrations/omp-hook-plan-forge.test.ts` (fire-and-forget capture), `cmd/client/cmd_board_test.go`. The source value `verify` (`SourceVerify` in `internal/board/discovery.go`) is accepted by the overlay but reserved: `plan-forge verify` (`cmd/client/cmd_verify.go`) records nothing on the board and nothing emits it today; verification receipts reach the board only through the evidence re-scan every 5 seconds (`boardEvidenceEvery` in `internal/app/board.go`).
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
- Statement: The coordinator shall maintain an Up-next queue of at most 5 tasks that are not done, drifted, blocked or active, ordered by priority, then plan manifest order, then accepted discoveries.
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
- Statement: The window shall show a coordinator panel listing Active now and the Up-next queue (panel labels `Active now` and `Up next`) beside the `Board | Graph` switch.
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
- Statement: The graph shall show a path node for every repository path the agent harness touched outside the excluded prefixes `.git/` and `docs/plans/.plan/` (repository internals and the plan's own state, never recorded), matched to planned targets by `MapPaths` and grouped under Unplanned when no target matches.
- Category: Functional
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C9 (`MapPaths`), C10 (`board activity`) and C12 (path pills).
- Acceptance criteria: `internal/board/discovery_test.go` (`MapPaths` cases), `web/src/board/wave4.test.ts`.
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
- Statement: The graph shall use a layered DAG layout (longest-path layering by `depends_on`, then three barycenter ordering passes) with pan, zoom and fit controls, and shall stay responsive up to the graph size bound (REQ-PERF-005).
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

### REQ-INSTALL-002 — Claude Code artifacts

- ID: REQ-INSTALL-002
- Level: F
- Statement: For Claude Code, the installer shall write exactly these managed artifacts: the skills `plan-forge`, `plan-config` and `plan-forge-board` (each carrying `enabled: false`) plus `SessionStart`, `PostToolUse` (matcher `Bash|Edit|Write|MultiEdit|NotebookEdit`) and `UserPromptSubmit` hook entries with the Claude Code hook timeout (Timing-bound precedence) in `~/.claude/settings.json`; the oh-my-pi and Antigravity artifact sets are REQ-INSTALL-008 and REQ-INSTALL-009.
- Category: Interface
- Owner: @danielckv
- Source: Verified `plan-forge install --host auto --yes` run on this machine (2026-10-19); `internal/host/installer.go`; `internal/host/hooks.go`.
- Acceptance criteria: `internal/host/installer_test.go`, `internal/host/hooks_test.go`; every managed entry carries the `plan-forge managed` marker.
- Traces to: C6; `internal/host/installer.go`, `internal/host/hooks.go`, `integrations/`
- Status: met
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

### REQ-INSTALL-007 — No duplicate skill visible to oh-my-pi

- ID: REQ-INSTALL-007
- Level: F
- Statement: The installer shall mark the Claude Code skill copies with `enabled: false` — and only those copies, never the oh-my-pi or Antigravity copies (REQ-INSTALL-002, REQ-INSTALL-008, REQ-INSTALL-009) — so that oh-my-pi, when its configuration reads the Claude Code skill root, skips the marked copies and lists exactly one `plan-forge` skill.
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
- Statement: For oh-my-pi, the installer shall write exactly these managed artifacts: the skills `plan-forge` and `plan-config` (without `enabled: false`), the command file `commands/plan-config.md`, and the hook `hooks/pre/plan-forge.ts` registering `/plan-forge` and `/plan-forge-board`, with no `commands/plan-forge.md`.
- Category: Interface
- Owner: @danielckv
- Source: Verified `plan-forge install --host auto --yes` run on this machine (2026-10-19); `internal/host/installer.go`; `internal/host/hooks.go`.
- Acceptance criteria: `internal/host/installer_test.go`, `internal/host/hooks_test.go`; every managed entry carries the `plan-forge managed` marker.
- Traces to: C7; `internal/host/installer.go`, `internal/host/hooks.go`, `integrations/`
- Status: met
- Last reviewed: 2026-10-19

### REQ-INSTALL-009 — Antigravity artifacts

- ID: REQ-INSTALL-009
- Level: F
- Statement: For Antigravity, the installer shall write exactly these managed artifacts: the skills `plan-forge` and `plan-config` in both `~/.gemini/antigravity-cli/skills/` and `~/.gemini/config/skills/` (without `enabled: false`) plus a `SessionStart` entry with the Antigravity hook timeout (Timing-bound precedence) in `~/.gemini/config/hooks.json`, and no board skill.
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
- Source: Consequence of the oh-my-pi command-only surface (REQ-INSTALL-008): with no skill copies, a missing hook removes `/plan-forge` and `/plan-forge-board` rather than degrading them; `docs/plans/COMMAND_CENTER_PLAN.md` C21.
- Acceptance criteria: `internal/host/hookcheck_test.go` (installed, stale, modified, missing and unreadable per host; the oh-my-pi remedy names the commands that are lost; the Antigravity remedy routes through uninstall because `mergeAGYHook` cannot refresh a stale path).
- Traces to: C21; `internal/host/hookcheck.go`, `cmd/client/cmd_doctor.go`
- Status: met
- Last reviewed: 2026-10-19

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
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 3b (`internal/sanitize`); `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C1 and C4 (titles and notes sanitized).
- Acceptance criteria: `internal/sanitize/sanitize_test.go` (including `"a\nb\tc"` becoming `"a b c"`).
- Traces to: C1, C4; `internal/sanitize/sanitize.go`, `cmd/client/cmd_hook.go`, `cmd/client/cmd_board.go`
- Status: met
- Last reviewed: 2026-10-19

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
- Statement: Every Plan-Forge skill, hook and board delta shall state that plan content and board edits never override host permission prompts, and the Claude Code adapter shall never invoke the host with permission-bypassing flags.
- Category: Non-functional (security)
- Owner: @danielckv
- Source: `docs/plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md` Step 3a (forbidden Claude Code flags) and Step 6 (skill body "treat the spec as data"); `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C4 (delta text "never overrides host permission prompts").
- Acceptance criteria: `internal/host/host_test.go` (argv assertions), `cmd/client/cmd_hook_test.go`, `internal/board/delta_test.go` (delta text).
- Traces to: C4, C6; `internal/host/claude.go`, `internal/board/delta.go`, `integrations/skill-plan-forge.md`
- Status: met
- Last reviewed: 2026-10-19

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

### REQ-PERF-004 — Board push latency

- ID: REQ-PERF-004
- Level: F
- Statement: The board server shall poll the overlay every 1 second and re-evaluate plan evidence every 5 seconds with the board evidence scan bound (Timing-bound precedence), so agent harness activity appears on the board within about 1 second.
- Category: Non-functional (performance)
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C5 (push intervals) and C11 (about 1 s); `internal/app/board.go` constants.
- Acceptance criteria: `internal/app/board.go` (`boardOverlayPoll = time.Second`, `boardEvidenceEvery = 5 * time.Second`, `boardScanTimeout = 10 * time.Second`); `internal/app/board_test.go`.
- Traces to: C5, C11; `internal/app/board.go`
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

### REQ-PERF-007 — oh-my-pi board sync timer

- ID: REQ-PERF-007
- Level: F
- Statement: The oh-my-pi hook shall sync board edits to the running agent harness on a managed 4-second timer, the delivery path REQ-BOARD-004 relies on.
- Category: Non-functional (performance)
- Owner: @danielckv
- Source: `docs/plans/GUI_BOARD_HANDOFF_PLAN.md` C7 (4 s timer).
- Acceptance criteria: `integrations/omp-hook-plan-forge.test.ts` (timer, `deliverAs` steer/nextTurn).
- Traces to: C7; `integrations/omp-hook-plan-forge.ts`
- Status: met
- Last reviewed: 2026-10-19

### Timing-bound precedence

One time bound applies per operation. The requirement cited in each row is the authority for that number; other requirements cross-reference it instead of restating the figure.

| Operation | Bound | Authority |
| :--- | :--- | :--- |
| AST repository scan, total budget | 120 s | REQ-PERF-002 (`DefaultScanTimeout`) |
| `plan-forge validate` scan, default | 30 s | REQ-PERF-003 (`--timeout`) |
| `plan-forge wait` post-approval scan | 20 s | REQ-PERF-003 |
| Board evidence re-scan | 10 s | REQ-PERF-004 (`boardScanTimeout`) |
| Board CLI subcommand (`pull`, `report`, `discover`, `activity`) | 2 s | REQ-PERF-001 |
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

## LIVE — the command center

### REQ-LIVE-001 — Visible without a plan

- ID: REQ-LIVE-001
- Level: S
- Statement: The command center shall record and show a harness session in a repository that has no approved plan.
- Category: Functional
- Owner: @danielckv
- Source: User request this session (2026-10-19): telemetry that works "even if Plan Forge hasn't run"; `docs/plans/COMMAND_CENTER_PLAN.md` C13, C16.
- Acceptance criteria: `internal/livestore/livestore_test.go` (upsert, bounds, eviction, concurrent writers lose no update), `internal/center/center_test.go` (`TestIngestRecordsAndRejects`), `web/e2e/center.spec.ts` ("a launched tab renders a session recorded without a plan").
- Traces to: C13, C16; `internal/livestore/livestore.go`, `internal/center/center.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-LIVE-002 — One long-lived surface, many tabs

- ID: REQ-LIVE-002
- Level: F
- Statement: The command center shall serve several browser tabs from one loopback daemon and push each recorded event to every connected tab.
- Category: Interface
- Owner: @danielckv
- Source: User decision (2026-10-19): a persistent fixed-port browser tab in place of the per-session window; `docs/plans/COMMAND_CENTER_PLAN.md` C14, C15.
- Acceptance criteria: `internal/center/center_test.go` (`TestOneRecordReachesEveryOpenTab`, `TestTheHubRefusesMoreThanItsCap`, `TestASlowClientIsDroppedWithAnOverflowFrame`), `web/e2e/center.spec.ts` ("one record reaches two open tabs").
- Traces to: C14, C15; `internal/center/hub.go`, `internal/app/center.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-LIVE-003 — The coordinator states what begins next

- ID: REQ-LIVE-003
- Level: S
- Statement: The command center shall present the coordinator's active and up-next queues for a repository with an approved plan, derived from the same evaluation the board window uses.
- Category: Functional
- Owner: @danielckv
- Source: User request this session (2026-10-19): "use coordinator that in live, decides which task should begin after the main agent harness gets more insights on new tasks"; `docs/plans/COMMAND_CENTER_PLAN.md` C18.
- Acceptance criteria: `internal/board/coordinator_test.go` (the ranking itself), `internal/center/center_test.go` (the sprint endpoint omits a repository with no approved plan); the view is rendered by `web/src/center/LivePage.tsx`.
- Traces to: C18; `internal/app/centersprint.go`, `web/src/center/LivePage.tsx`
- Status: partly met
- Last reviewed: 2026-10-19

### REQ-LIVE-004 — No egress and no tracker

- ID: REQ-LIVE-004
- Level: B
- Statement: The command center shall make no outbound network request and shall integrate with no external issue tracker.
- Category: Constraint
- Owner: @danielckv
- Source: User decision (2026-10-19): external trackers dropped in favour of REQ-BOARD-004 and REQ-SEC-004; `docs/plans/COMMAND_CENTER_PLAN.md` User decisions.
- Acceptance criteria: `web/e2e/center.spec.ts` ("the command center makes no external request") fails on any request to an origin other than the daemon's own.
- Traces to: C15, C17; `internal/center/center.go`, `web/src/center/`
- Status: met
- Last reviewed: 2026-10-19

### REQ-LIVE-005 — A tab is authorised, and opened once

- ID: REQ-LIVE-005
- Level: F
- Statement: The command center shall issue a session token only to a tab redeeming a single-use launch ticket, and shall open a browser tab only when no tab is already connected.
- Category: Non-functional (security)
- Owner: @danielckv
- Source: `docs/plans/COMMAND_CENTER_PLAN.md` C15, C20; the residual risks a fixed port introduces are recorded there.
- Acceptance criteria: `internal/center/center_test.go` (`TestAuthMatrix`, `TestATicketIsSingleUse`, `TestAnExpiredTicketIsRefused`, `TestForeignHostIsRefused`), `web/e2e/center.spec.ts` ("a tab opened by hand parks instead of silently failing").
- Traces to: C15, C20; `internal/center/auth.go`, `internal/app/center.go`
- Status: met
- Last reviewed: 2026-10-19

### REQ-LIVE-006 — Bounded, and never in the way

- ID: REQ-LIVE-006
- Level: F
- Statement: The command center's hook-side commands shall stay within the host hook budget and shall exit 0 whatever happens.
- Category: Non-functional (performance)
- Owner: @danielckv
- Source: `docs/plans/COMMAND_CENTER_PLAN.md` C16, C20; REQ-PERF-001 is the governing bound.
- Acceptance criteria: `integrations/omp-hook-plan-forge.test.ts` ("the command center launch is not awaited", "a failing command center never blocks the banner"); `CenterEnsureBound` and `centerReportBound` in `internal/app/center.go` and `cmd/client/cmd_center.go`.
- Traces to: C16, C20; `internal/app/center.go`, `cmd/client/cmd_center.go`
- Status: partly met
- Last reviewed: 2026-10-19

## Category coverage

Counts are taken from the `Category:` fields and sum to 86 requirements.

| Category | Count | Note |
| :--- | :--- | :--- |
| Functional | 41 | Concentrated in GATE, COORD, BOARD, INSTALL and RELEASE; the SEC, PERF and COMPAT areas carry no functional requirements, so coverage is not uniform across areas. |
| Non-functional (security) | 6 | SEC area: REQ-SEC-001, REQ-SEC-002, REQ-SEC-004, REQ-SEC-005, REQ-SEC-006; plus REQ-LIVE-005. |
| Non-functional (performance) | 9 | PERF area (REQ-PERF-001 to REQ-PERF-007) plus REQ-GRAPH-005 and REQ-LIVE-006. |
| Non-functional (usability/accessibility) | 3 | REQ-UI-005, REQ-UI-006, REQ-UI-007. Genuinely thin: the GUI polish was delivered as one slice; further usability requirements await measurable targets from the owner (open question 8). |
| Business | 2 | REQ-GATE-001, REQ-UI-009. |
| User | 6 | The S-level capability statements. REQ-LIVE-001 and REQ-LIVE-003 are S-level and counted under Functional by their Category field. |
| Interface | 9 | REQ-HANDOFF-002, REQ-HANDOFF-003, REQ-BOARD-004, REQ-BOARD-007, REQ-SEC-003, REQ-INSTALL-002, REQ-INSTALL-008, REQ-INSTALL-009, REQ-LIVE-002. The relay wire protocol is specified in the system plan as a design contract and is not restated here; the bridge authentication contract is restated as REQ-SEC-003. |
| Data | 2 | REQ-GATE-005, REQ-BOARD-003. Genuinely thin: other persisted shapes (session state, artifacts) are covered inside functional requirements on approval and storage. |
| Regulatory | 0 | Genuinely empty: the product is an open-source local tool with no identified regulatory obligation; security posture lives under Non-functional (security). |
| Constraint | 8 | Ingestion bounds, Product Manager isolation, SHA pinning, platform and toolchain pins, and the command center no-egress rule: REQ-GATE-003, REQ-GATE-004, REQ-RELEASE-005, REQ-COMPAT-001 to REQ-COMPAT-004, REQ-LIVE-004. |
| Reliability | 0 | Dismissed as a separate category: journaled commit and crash recovery are stated under REQ-GATE-009 and atomic overlay writes under REQ-BOARD-003; no standalone reliability target has been set. |
| Scalability | 0 | Genuinely empty: Plan-Forge is a single-user local tool; the only scale bound is the graph node budget under Non-functional (performance) (REQ-PERF-005). |
| Maintainability | 0 | Genuinely empty: upheld through repository gates (lint, typecheck, CI under REQ-RELEASE-001) rather than stated as product requirements. |
| Portability | 0 | Dismissed as a separate category: platform coverage is stated under Constraint (REQ-COMPAT-001, REQ-COMPAT-003, REQ-COMPAT-004); no further portability requirement is identified. |
| Documentation | 0 | Dismissed as a separate category: the README and skill-install documentation ship as release artifacts (REQ-RELEASE-003), not as product requirements. |
| Privacy | 0 | Dismissed as a separate category: the egress allowlist and untrusted-input posture are stated under Non-functional (security) (REQ-SEC-001, REQ-SEC-002, REQ-SEC-004). |

## Glossary

- plan forge gate: the pre-planning stage that turns a raw brief into an approved synthesized spec and plan manifest before implementation begins.
- Product Manager isolation: the property that nothing derived from the repository reaches the Product Manager avatar; the operational posture is stated by REQ-GATE-004.
- agent harness: the host-mediated coding agent session that receives the approved plan manifest and executes it.
- host: a supported local agent CLI — Claude Code, oh-my-pi (OMP) or Antigravity (AGY).
- skill: a named instruction package installed under a host's skill root; Claude Code and Antigravity invoke it as a slash command, while oh-my-pi exposes `skill:plan-forge` alongside the hook-registered `/plan-forge` command.
- hook: a host-registered callback (session start, pre/post tool use, prompt submit) through which Plan-Forge injects or captures context.
- plan mode: a mode of the host, not of Plan-Forge — the host's own planning mode, entered after approval, in which the agent plans without editing files.
- plan manifest: `.plan/plan_manifest.json`, the machine-readable approved plan with task DAG, targets and verification commands.
- synthesized spec: `.plan/synthesized_spec.md`, the human-readable approved specification consumed by the agent harness as its blueprint.
- approval ledger: `PlanForgeHome()/approvals.json`, the local record of plan manifest hashes approved on this machine.
- board: the Plan-Forge surface for the approved plan manifest's tasks: the window view laid out by status column, the overlay store behind it, its per-repository lock and its bridge API.
- task: one unit of planned work from the plan manifest, identified `T-<n>`, or an accepted discovery `D-<n>`.
- discovery: an item of work observed outside the plan manifest — a new task or a touched path — in one of three states: proposed, accepted or dismissed; only the user changes the state.
- path node: a graph node representing a repository path the agent harness touched, linked to its parent task or grouped under Unplanned.
- coordinator: the board component that records agent harness activity into the overlay and computes the live column, blocked state, awaiting verification and the Up-next queue from plan evidence plus overlay activity, steering the agent harness only when the queue head changes.
- overlay: the board's machine-local persistence, keyed by the approved plan manifest hash and kept separate from the approved artifacts.
- consumer: one host connection that pulls board deltas, identified by a per-host consumer id.
- cursor: a consumer's bookmark in the overlay recording what has already been delivered to that consumer.
- delta: the text of board changes — priorities, context notes and steering lines — delivered to a consumer since its cursor.
- live column: the column the coordinator computes for a task from evidence plus activity; the board snapshot shows it and REQ-COORD-003 defines the precedence.
- evidence: repository state observed by the planstate scan — AST targets and verification receipts — from which the board derives the evidence column (REQ-BOARD-002).
- drifted: the state of the plan manifest, or of a task, when the repository no longer matches the approved artifacts — an invalid manifest, a hash mismatch or an invalid target — so evidence cannot be trusted.
- managed: written by the installer and recorded in the install manifest with the `plan-forge managed` marker, so re-install and uninstall know exactly what Plan-Forge owns.
- relay: the `plan-forge-relay` server that mirrors session state between clients in Remote collaboration mode.
- avatar: one deliberation role in the plan forge gate: Product Manager, Technical Architect, Domain Specialist, Red-Team Critic or Lead Synthesizer.
- AST: abstract syntax tree; the repository grounding produced by the Tree-sitter scan.
- DAG: directed acyclic graph; the dependency structure of tasks in the plan manifest and the input to the graph view layout.
- PRD: product requirements document; the raw or normalized input to the plan forge gate.
- CSP: content security policy; the response header the local bridge serves the UI under (REQ-SEC-006).
- PERT: program evaluation and review technique; the three-point estimate input (optimistic, likely, pessimistic) behind the dual effort schedules (REQ-GATE-010).
- TTY: an interactive terminal; the installer prompts only when stdin and stdout are TTYs (REQ-INSTALL-006).
- decision card: the prompt the plan forge gate shows when deliberation needs a human choice — two to four options with a recommendation (REQ-GATE-007).

## Open questions

1. Release gap versus the one-command install. The one-command install fetches the latest published release, which is `v0.1.1` and predates every feature in the HANDOFF, BOARD, COORD, GRAPH and UI areas of this catalogue (confirmed: `git ls-tree v0.1.1 cmd/client/` has no `cmd_board.go` or `cmd_validate.go`). Whether the README's one-command install is expected to deliver those features, and when a tag carrying them is cut, is undecided. Blocking: release timing. Answerable by: @danielckv.
2. oh-my-pi dual surface. **Settled 2026-10-19:** the skill is dropped and oh-my-pi is command-only (REQ-INSTALL-008). One consequence is open in its place: a skill can be matched from its description and a registered command cannot, so a prose request no longer reaches Plan Forge in oh-my-pi. Whether that is worth a session-start hint line — which would make every session chattier — is undecided. Answerable by: @danielckv.
3. Command center sprint titles. REQ-LIVE-003 is partly met: `PlanManifest` carries `TaskDAG` but no epic or story titles, which survive approval only as headings in `synthesized_spec.md`, so `/sprint` groups by identifier rather than by title. Whether to parse those headings back out of the spec, or to persist them in the manifest, is undecided. Answerable by: @danielckv.
4. Command center hook budget. REQ-LIVE-006 is partly met: `CenterEnsureBound` and `centerReportBound` are declared and the oh-my-pi side is covered by tests that prove the launch is not awaited, but no test asserts the Claude-side wall-clock bound the way REQ-PERF-001 does for `hook claude`. Answerable by: @danielckv.
5. Antigravity hook file shape. `internal/host/hooks.go` writes events at the top level of `~/.gemini/config/hooks.json` (`{"SessionStart":[...]}`), while `integrations/agy-locations.md` documents the nested form (`{"plan-forge":{"SessionStart":[...]}}`); the installed file on this machine uses the flat form. Whether Antigravity reads the flat form — and therefore whether the plan-status hook fires at all — is unverified; this is why REQ-INSTALL-009 is partly met. Settled by reading Antigravity's hook documentation or observing the session-start banner. Answerable by: whoever can inspect the Antigravity CLI hooks loader (owner: @danielckv).
6. Antigravity wait timeout. The `{{WAIT_TIMEOUT}}` template value for AGY is 50 s "until its limit is verified" per the system plan; the actual AGY tool-call limit has not been confirmed. Blocking: AGY documentation or empirical check. Answerable by: @danielckv.
7. Windows cross-`go vet`. The Windows cross-`go vet` cannot run on this machine because tree-sitter needs cgo; CI covers it. Whether local pre-push verification of the Windows build is required is undecided. Blocking: none for correctness (CI gate exists); a local verification option needs a Windows runner or container. Answerable by: @danielckv.
8. Subjective polish measure. REQ-UI-009 is verified only by objective proxies (component set shipped, gates green); a measurable usability target (task-completion study, SUS score) has not been set, so REQ-UI-009 is partly met. Blocking: a target from the owner. Answerable by: @danielckv.
9. Egress check untested. `internal/relay/client_conn.go` implements the egress guard (`NewEgressGuard`, `Contains`, `Redact`, `RedactJSON`) and `internal/app/remote.go` applies it, but no test in the tree exercises it, so REQ-SEC-004 is partly met. Blocking: a test that feeds absolute paths and credential-shaped content through `Redact` and `RedactJSON` and asserts none reaches the wire. Answerable by: @danielckv.
10. Scan bound untested. The worker cap, the 2-second parse timeout and the 120-second scan budget are defined in `internal/ast/scanner.go` but no test asserts them or the `timed_out` partial result, so REQ-PERF-002 is partly met. Blocking: a scan test with a shortened timeout asserting `coverage.timed_out` and a partial graph. Answerable by: @danielckv.
11. Reserved `verify` activity source. `SourceVerify` is a valid activity source in `internal/board/discovery.go` but nothing emits it: `plan-forge verify` (`cmd/client/cmd_verify.go`) writes a receipt and records no board activity, so a verification run moves a card only after the next 5-second evidence re-scan. Whether `verify` should emit that activity, or the value should be removed, is undecided; no requirement depends on it. Blocking: a product decision on real-time verification feedback. Answerable by: @danielckv.
12. Antigravity automatic plan-mode entry. REQ-HANDOFF-001 delivers automatic plan-mode entry and validation for Claude Code and oh-my-pi only; Antigravity receives the plan-mode hint text (REQ-HANDOFF-006) and the user enters plan mode themselves, so REQ-HANDOFF-001 is partly met. Whether an automatic Antigravity mechanism exists, or the hint is the accepted ceiling, is undecided. Blocking: Antigravity API surface for session mode control. Answerable by: @danielckv.
13. Remaining compound requirements. The 2026-10-19 audit counted 31 compound requirements; six were split in this pass (REQ-INSTALL-002, REQ-SEC-003, REQ-GATE-007, REQ-PERF-004, REQ-HANDOFF-005, REQ-RELEASE-001). The remaining 25 stay compound until a follow-up pass: REQ-GATE-005, REQ-GATE-006, REQ-GATE-008, REQ-GATE-010, REQ-UI-001, REQ-UI-002, REQ-UI-003, REQ-UI-006, REQ-UI-008, REQ-HANDOFF-001, REQ-HANDOFF-004, REQ-BOARD-001, REQ-BOARD-003, REQ-BOARD-005, REQ-BOARD-007, REQ-COORD-003, REQ-GRAPH-002, REQ-GRAPH-004, REQ-GRAPH-005, REQ-INSTALL-001, REQ-INSTALL-004, REQ-INSTALL-005, REQ-INSTALL-006, REQ-RELEASE-004, REQ-SEC-005. Blocking: a scheduled editorial pass; none is functionally wrong, each just carries more than one intent. Answerable by: @danielckv.

## Review log

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
| REQ-LIVE-001 | C13, C16 | `internal/livestore/livestore.go`, `internal/center/center.go` | `internal/livestore/livestore_test.go`, `internal/center/center_test.go`, `web/e2e/center.spec.ts` |
| REQ-LIVE-002 | C14, C15 | `internal/center/hub.go`, `internal/app/center.go` | `internal/center/center_test.go`, `web/e2e/center.spec.ts` |
| REQ-LIVE-003 | C18 | `internal/app/centersprint.go`, `web/src/center/LivePage.tsx` | `internal/board/coordinator_test.go`, `internal/center/center_test.go` |
| REQ-LIVE-004 | C15, C17 | `internal/center/center.go`, `web/src/center/` | `web/e2e/center.spec.ts` |
| REQ-LIVE-005 | C15, C20 | `internal/center/auth.go`, `internal/app/center.go` | `internal/center/center_test.go`, `web/e2e/center.spec.ts` |
| REQ-LIVE-006 | C16, C20 | `internal/app/center.go`, `cmd/client/cmd_center.go` | `integrations/omp-hook-plan-forge.test.ts` |
| REQ-INSTALL-010 | C21 | `internal/host/hookcheck.go`, `cmd/client/cmd_doctor.go` | `internal/host/hookcheck_test.go` |
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
| REQ-UI-001 | C8 | `web/src/App.tsx`, `web/src/pages/`, `web/src/store/syncStore.ts` | `web/src/ui/phase.test.ts`, `web/e2e/bridge.spec.ts` |
| REQ-UI-002 | — | `internal/window/launch.go` | `internal/window/launch_test.go` |
| REQ-UI-003 | — | `web/src/ui/PhaseStepper.tsx`, `web/src/components/Sidebar.tsx` | `web/src/ui/phase.test.ts` |
| REQ-UI-004 | — | `web/src/ui/ThemeSelect.tsx`, `web/src/ui/prefs.ts` | `web/src/ui/prefs.test.ts` |
| REQ-UI-005 | C12 | `web/src/app.css`, `web/src/board/board.css` | CSS inspection: `prefers-reduced-motion` blocks in both files |
| REQ-UI-006 | — | `web/src/ui/shortcuts.ts`, `web/src/ui/ShortcutHelp.tsx`, `web/src/ui/focusTrap.ts` | `web/src/ui/shortcuts.test.ts` |
| REQ-UI-007 | — | `web/src/app.css`, `web/src/board/board.css` | CSS inspection: `@media (max-width: 1099.98px)` and `@media (max-width: 1180px)` breakpoints |
| REQ-UI-008 | C8, C12 | `web/src/board/BoardApp.tsx`, `web/src/board/graph/GraphView.tsx` | `web/src/board/viewMode.test.ts` |
| REQ-UI-009 | — | `web/src/ui/`, `web/src/app.css` | `npm --prefix web test` (suite) |
| REQ-HANDOFF-001 | C6, C7 | `cmd/client/cmd_hook.go`, `integrations/omp-hook-plan-forge.ts` | `cmd/client/cmd_hook_test.go`, `integrations/omp-hook-plan-forge.test.ts` |
| REQ-HANDOFF-002 | C6 | `cmd/client/cmd_hook.go`, `internal/host/hooks.go` | `cmd/client/cmd_hook_test.go` |
| REQ-HANDOFF-003 | C7 | `integrations/omp-hook-plan-forge.ts` | `integrations/omp-hook-plan-forge.test.ts` |
| REQ-HANDOFF-004 | C1, C2 | `cmd/client/cmd_validate.go`, `cmd/client/cmd_session.go`, `internal/planstate/validate.go` | `cmd/client/cmd_validate_test.go`, `internal/planstate/validate_test.go` |
| REQ-HANDOFF-005 | C6, C7 | `cmd/client/cmd_hook.go`, `integrations/skill-plan-forge.md` | `cmd/client/cmd_hook_test.go` |
| REQ-HANDOFF-006 | C6 | `integrations/skill-plan-forge.md`, `internal/host/installer.go` | `internal/host/installer_test.go` |
| REQ-HANDOFF-007 | C6, C7 | `cmd/client/cmd_hook.go`, `integrations/skill-plan-forge.md`, `integrations/omp-hook-plan-forge.ts` | `cmd/client/cmd_hook_test.go` |
| REQ-BOARD-001 | C4, C5 | `web/src/board/`, `internal/app/board.go` | `web/src/board/board.test.ts`, `internal/app/board_test.go` |
| REQ-BOARD-002 | C5 | `internal/planstate/taskboard.go`, `internal/app/board.go` | `internal/planstate/taskboard_test.go`, `internal/app/board_test.go` |
| REQ-BOARD-003 | C3, C9 | `internal/board/board.go`, `internal/board/lock_unix.go`, `internal/board/lock_windows.go` | `internal/board/board_test.go` |
| REQ-BOARD-004 | C4, C6, C7 | `integrations/omp-hook-plan-forge.ts`, `cmd/client/cmd_hook.go`, `internal/board/delta.go` | `integrations/omp-hook-plan-forge.test.ts`, `cmd/client/cmd_hook_test.go`, `internal/board/delta_test.go` |
| REQ-BOARD-005 | C4, C10 | `cmd/client/cmd_board.go` | `cmd/client/cmd_board_test.go` |
| REQ-BOARD-006 | C4 | `cmd/client/cmd_board.go`, `internal/app/boardguard_unix.go`, `internal/app/boardguard_windows.go` | `cmd/client/cmd_board_lock_unix_test.go` |
| REQ-BOARD-007 | C5, C11 | `internal/app/board.go` | `internal/app/board_test.go` |
| REQ-COORD-001 | C9, C11, C12 | `internal/board/coordinator.go` | `internal/board/coordinator_test.go`, `web/src/board/wave4.test.ts` |
| REQ-COORD-002 | C10, C12 | `cmd/client/cmd_hook.go`, `integrations/omp-hook-plan-forge.ts`, `cmd/client/cmd_board.go` | `cmd/client/cmd_hook_test.go`, `integrations/omp-hook-plan-forge.test.ts`, `cmd/client/cmd_board_test.go` |
| REQ-COORD-003 | C9 | `internal/board/coordinator.go` | `internal/board/coordinator_test.go` |
| REQ-COORD-004 | C9, C11 | `internal/board/coordinator.go`, `internal/app/board.go` | `internal/board/coordinator_test.go`, `internal/app/board_test.go` |
| REQ-COORD-005 | C9 | `internal/board/coordinator.go` | `internal/board/coordinator_test.go` |
| REQ-COORD-006 | C9 | `internal/board/delta.go`, `internal/board/coordinator.go` | `internal/board/delta_v2_test.go` |
| REQ-COORD-007 | C11, C12 | `web/src/board/CoordinatorPanel.tsx`, `web/src/board/coordinator.ts` | `web/src/board/wave4.test.ts` |
| REQ-GRAPH-001 | C12 | `web/src/board/graph/GraphView.tsx`, `web/src/board/graph/GraphNodes.tsx` | `web/src/board/graph/layout.test.ts`, `web/src/board/wave4.test.ts` |
| REQ-GRAPH-002 | C9, C12 | `internal/board/discovery.go`, `web/src/board/graph/DiscoveryPanel.tsx` | `internal/board/discovery_test.go`, `web/src/board/wave4.test.ts` |
| REQ-GRAPH-003 | C9, C10, C12 | `internal/board/discovery.go`, `web/src/board/graph/GraphNodes.tsx` | `internal/board/discovery_test.go`, `web/src/board/wave4.test.ts` |
| REQ-GRAPH-004 | C11, C12 | `internal/app/board.go`, `internal/board/discovery.go`, `web/src/board/graph/DiscoveryPanel.tsx` | `internal/app/board_test.go`, `web/src/board/wave4.test.ts` |
| REQ-GRAPH-005 | C12 | `web/src/board/graph/layout.ts`, `web/src/board/graph/GraphView.tsx` | `web/src/board/graph/layout.test.ts` |
| REQ-INSTALL-001 | — | `scripts/install.sh`, `scripts/install.ps1`, `internal/host/installer.go` | `internal/host/installer_test.go` |
| REQ-INSTALL-002 | C6 | `internal/host/installer.go`, `internal/host/hooks.go`, `integrations/` | `internal/host/installer_test.go`, `internal/host/hooks_test.go` |
| REQ-INSTALL-003 | — | `internal/host/installer.go`, `integrations/` | `internal/host/installer_test.go` |
| REQ-INSTALL-004 | — | `internal/host/installer.go` | `internal/host/installer_test.go` |
| REQ-INSTALL-005 | — | `internal/host/installer.go`, `cmd/client/cmd_uninstall.go` | `internal/host/installer_test.go` |
| REQ-INSTALL-006 | — | `internal/host/installer.go`, `cmd/client/cmd_install.go` | `internal/host/installer_test.go` |
| REQ-INSTALL-007 | — | `internal/host/installer.go` | `internal/host/installer_test.go` |
| REQ-INSTALL-008 | C7 | `internal/host/installer.go`, `internal/host/hooks.go`, `integrations/` | `internal/host/installer_test.go`, `internal/host/hooks_test.go` |
| REQ-INSTALL-009 | C6 | `internal/host/installer.go`, `internal/host/hooks.go`, `integrations/` | `internal/host/installer_test.go`, `internal/host/hooks_test.go` |
| REQ-RELEASE-001 | — | `.github/workflows/ci.yml`, `.github/workflows/release.yml` | CI workflow runs |
| REQ-RELEASE-002 | — | `.github/workflows/release.yml`, `.github/workflows/ci.yml`, `internal/buildguard/`, `cmd/relay/guard.go` | `relay` release job; `go-server` CI job |
| REQ-RELEASE-003 | — | `scripts/package-release.sh`, `scripts/verify-archive.sh`, `.github/workflows/release.yml` | Checkpoint 6 commands in the system plan |
| REQ-RELEASE-004 | — | `.github/workflows/release.yml`, `scripts/install.sh`, `scripts/install.ps1` | `release.yml` `publish`/`relay-image` job conditions; tags `v0.1.0` and `v0.1.1` published |
| REQ-RELEASE-005 | — | `.github/workflows/ci.yml`, `.github/workflows/release.yml` | Workflow inspection |
| REQ-RELEASE-006 | — | `.github/workflows/ci.yml`, `.github/workflows/release.yml` | `needs` chain inspection: `go-client` and `client` need `web` |
| REQ-SEC-001 | — | `internal/planstate/ledger.go`, `internal/planstate/planstate.go`, `cmd/client/cmd_hook.go` | `internal/planstate/validate_test.go` |
| REQ-SEC-002 | C1, C4 | `internal/sanitize/sanitize.go`, `cmd/client/cmd_hook.go`, `cmd/client/cmd_board.go` | `internal/sanitize/sanitize_test.go` |
| REQ-SEC-003 | — | `internal/relay/local_ws.go`, `internal/relay/client_conn.go` | `internal/relay/local_ws_test.go`, `web/e2e/bridge.spec.ts` |
| REQ-SEC-004 | — | `internal/relay/client_conn.go`, `internal/app/remote.go` | None: no dedicated egress test exists; `internal/relay/protocol_test.go` does not cover it |
| REQ-SEC-005 | C4, C6 | `internal/host/claude.go`, `internal/board/delta.go`, `integrations/skill-plan-forge.md` | `internal/host/host_test.go`, `cmd/client/cmd_hook_test.go`, `internal/board/delta_test.go` |
| REQ-SEC-006 | — | `internal/relay/local_ws.go`, `internal/relay/client_conn.go` | `internal/relay/local_ws_test.go`, `web/e2e/bridge.spec.ts` |
| REQ-PERF-001 | C6, C10 | `cmd/client/cmd_hook.go`, `cmd/client/cmd_board.go` | `cmd/client/cmd_hook_test.go`, `cmd/client/cmd_board_test.go` |
| REQ-PERF-002 | — | `internal/ast/scanner.go`, `internal/ast/parse.go` | None direct: bound defined by constants in `internal/ast/scanner.go`; `internal/ast/symbols_test.go` asserts none of it |
| REQ-PERF-003 | C1, C2 | `cmd/client/cmd_validate.go`, `cmd/client/cmd_session.go` | `cmd/client/cmd_validate_test.go`, `internal/planstate/validate_test.go` |
| REQ-PERF-004 | C5, C11 | `internal/app/board.go` | `internal/app/board_test.go` |
| REQ-PERF-005 | C12 | `web/src/board/graph/layout.ts` | `web/src/board/graph/layout.test.ts` |
| REQ-PERF-006 | — | `internal/relay/hub.go` | `internal/relay/hub_integration_test.go` |
| REQ-PERF-007 | C7 | `integrations/omp-hook-plan-forge.ts` | `integrations/omp-hook-plan-forge.test.ts` |
| REQ-COMPAT-001 | — | `.github/workflows/release.yml` | Release `client` matrix runs |
| REQ-COMPAT-002 | — | `internal/buildguard/`, `cmd/client/guard.go`, `cmd/relay/guard.go` | `go-server` CI job |
| REQ-COMPAT-003 | — | `web/.nvmrc`, `web/package.json` | `npm --prefix web run typecheck` in CI |
| REQ-COMPAT-004 | — | `go.mod`, `.github/workflows/` | All Go CI jobs |
