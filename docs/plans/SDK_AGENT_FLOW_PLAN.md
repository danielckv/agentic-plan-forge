# SDK-backed planning workspace

## Context
Replace local CLI generation with direct SDK-backed providers. Deliver one coherent planning workspace across browser and terminal, including model selectors, skills settings, permanent reviewers, justified temporary agents, and questions after fusion. Favor one complete planning pass with bounded targeted repair rather than repeated whole-plan cycles. Repository graph evidence MUST contain only code, Markdown, and SVG.

## Decisions and provider placement

Anthropic already uses the official Go SDK in `internal/host/anthropicapi.go`. Extend it rather than introduce a second Claude runtime. Google needs a new adapter using `google.golang.org/genai`.

Google has two different interfaces. The local `google-antigravity` Python SDK manages a local binary. Managed Antigravity runs through Google's Interactions API. Use remote APIs, not the local Python runtime, because eliminating local CLI prerequisites is the requested outcome.

Expose Google model calls and managed Antigravity as distinct runtime selections under one Google credential. Use direct Argon calls for PRD drafting: they expose medium thinking explicitly and avoid provisioning an agent sandbox. Managed Antigravity remains selectable for bounded specialist work only after the capability gate below passes. Never label a direct Gemini request as an Antigravity agent request.

Default placements:

| Responsibility | Preferred provider/model | Reason |
| --- | --- | --- |
| Initial PRD and large-context requirements | Google, `gemini-4-argon`, medium | Large context; explicit thinking control |
| Product planning | Anthropic, Claude Sonnet 5.5 | Product-only reasoning without repeated architecture calls |
| Architecture | Anthropic, Claude Opus 5.5 | Interfaces, constraints, and implementation design |
| Technical leader and final integration | Anthropic, Claude Opus 5.5 | Resolve technical dependencies and certify readiness |
| Code reviewer | Anthropic, Claude Sonnet 5.5 | Separate review of proposed code changes and repository evidence |
| Independent judgment | Google, Argon medium | Independent provider review of the fused candidate |
| Temporary specialist | Sonnet or Argon medium, selected by capability | Narrow expertise only when coverage is missing |
| Status narration | Deterministic status messages | No model call on the critical path |

Resolve Anthropic IDs against the live catalog; reuse the existing Opus/Sonnet/Haiku constants as labeled fallback candidates, not proof of access. Never silently replace an unavailable model with an arbitrary alphabetically selected model.

With only Anthropic available, spread work across Opus and Sonnet; Haiku may handle bounded summaries, never architectural judgment. With only Google available, use Argon for critical reasoning and another verified eligible Google model for inexpensive work. Different roles MUST use isolated contexts even when only one eligible model exists. Show `same-model review` when genuine model diversity is unavailable.

## Approach

### 1. Provider contracts, onboarding, and settings

Reuse `host.Adapter`, `contracts.Generator`, `Router.PlaceRole`, and the credential store. Add Google runtime adapters in `internal/host/`; register generation kinds `gemini` and `antigravity`. Add credential provider `google`, sourced from `GEMINI_API_KEY` or the existing owner-only credential file. Both runtimes share that credential but expose separate readiness results.

Keep Claude and Antigravity integration identities separate from SDK generation. Remove `claude`, `omp`, and `agy` from automatic generation candidates in `router.go`. Remove OMP from supported generation adapters, onboarding choices, diagnostics recommendations, hook installation, and skill installation for this release. Existing OMP configuration MUST produce migration guidance, not silent fallback. Preserve user-owned OMP files; removal of previously managed files requires explicit confirmation and matching ownership hashes. Do not uninstall the user's OMP application. Existing OpenAI/Fireworks API configurations remain explicitly selectable; the default recommendation uses Anthropic and Google.

At first launch, run existing CLI detection for diagnostics only. Display separate states: installed application, credential present, authentication verified, model accessible, runtime compatible. A Claude installation MUST NOT imply Anthropic API access. Never extract Claude subscription tokens or Google application session credentials. No verified provider opens onboarding instead of aborting before settings can appear.

Preflight MUST use a bounded authenticated request. Distinguish invalid credentials, missing model entitlement, network failure, quota failure, and unsupported runtime capabilities. Deduplicate concurrent probes and cache results for five minutes. Credential changes invalidate the cache. Catalog failure preserves the last list marked stale; it does not certify models as available.

For managed Antigravity, use Interactions API agent `antigravity-preview-09-2026` with `agent_config.type=antigravity` and the selected underlying model. Before enabling it for planning, prove that an explicit empty tools list and omitted environment prohibit tool execution. Do not mount repositories, skills containing executables, or credentials into a managed sandbox. Verify that request serialization preserves the empty list rather than omitting it. If the API cannot enforce this contract, mark this runtime unavailable for planning; keep direct Google calls available and state the reason. A prompt saying “do not use tools” is not isolation. Expose medium effort only for runtimes that actually accept it; managed-runtime defaults MUST be labeled as defaults.

Use existing cancellation, receipts, metering, output bounds, schema validation, and repair machinery. Cancel remote background work when a local run is cancelled. Never restart a paid interaction merely because a stream reconnects. Unsupported structured-output capabilities MUST be surfaced; existing bounded JSON repair remains available.

Extend `config.RoleBinding` with `effort` and `fallback_policy`; legal policies are `auto` and `strict`. Default auto placement uses the table above. Explicit pins default to strict. Resolve and freeze a placement snapshot at run start; include provider, runtime, model, effort, skills hashes, and routing version in checkpoint compatibility. Mid-run settings apply to the next run. Any permitted fallback emits its reason and actual placement before execution.

Migrate old CLI generation pins through a visible settings review. Preserve their original values until the user saves replacement selections. Do not map an `agy` model string onto Anthropic. Unknown model IDs remain visible as unavailable selections, not editable free text.

Reuse `/api/providers` for credential submission. Add authenticated loopback settings/catalog endpoints through the same bridge guards: `GET /api/models`, `GET /api/settings`, and `PUT /api/settings`. Settings writes carry a revision; return 409 for stale writes. Validate provider/model membership server-side, not only in controls. Provider keys never appear in settings responses, logs, or collaborative events. API-key entry remains masked text; provider, runtime, model, effort, and policy are selections.

### 2. One shared planning lifecycle

Reuse `internal/avatars/orchestrator.go`, the existing checkpoints, `contracts.Decision`, and the session authority. Do not create a second orchestrator for the UI or terminal.

Retain `pm`, `architect`, `critic`, `specialist`, and `synthesizer`. Add role IDs `tech_lead` and `code_reviewer`. Label the critic as Judgment in the interface. Product planning remains the existing PM rather than a duplicate agent. Add role definitions to contracts, config validation, routing, stage IDs, prompts, schemas, checkpoint records, and UI descriptors together. Locate all enumerations with `rg 'RoleProductManager|RoleArchitect|RoleCritic|StageArchitect|StageCritic|roleSlots|validRoles' internal web/src` before editing.

The normal dependency order is:

1. Normalize the brief with Argon medium.
2. Run product planning and repository evidence preparation concurrently where their inputs are independent.
3. Produce architecture and a coverage map once.
4. Execute required workstream drafts and approved expertise gaps under the shared concurrency limit.
5. Assemble one candidate. Run code review and independent judgment concurrently against that same immutable candidate.
6. Have the technical leader integrate findings. Repair only affected sections once, then rerun affected validators/reviewers.
7. Publish a fused candidate with readiness and unresolved choices. Open one question drawer when human decisions remain; otherwise continue to the existing approval/handoff flow.

Preserve the PM's product-only input boundary. Technical leaders and reviewers receive repository evidence, not fabricated code execution results. A code review covers current code and proposed changes; it MUST NOT claim an unimplemented change passed tests.

Keep `HardMaxParallel=4` initially. All model calls share the semaphore, including repairs and reviews. Keep the existing per-call safety ceiling rather than shortening timeouts to simulate speed. Add a separate run budget; do not claim a fixed end-to-end latency before measurement. A deadline produces a resumable candidate with pending work, never a false ready result.

Replace automatic full-cycle retries in the default flow with at most one targeted repair pass. User answers invalidate only dependent artifacts. A new whole-plan pass requires a material baseline revision or explicit user action. Preserve checkpoint resume and ADR 0023's record-and-produce path. A provisional plan MUST remain visibly provisional.

Define a versioned run snapshot with run ID, input revision, phase, candidate revision, placements, agent instances, findings, decisions, artifact references, and readiness. New phases are `preparing`, `drafting`, `reviewing`, `repairing`, `fused`, `awaiting_decisions`, `ready`, `provisional`, `failed`, and `cancelled`. Integrate these as presentation state over the existing authority; do not bypass existing approval transitions. Send snapshots and incremental agent state through the existing WebSocket transport. Reconnect restores current state without replaying generation.

### 3. Temporary agents with a real purpose

Reuse workstream specialist lanes rather than create an unrestricted agent factory. Each temporary agent is a run-scoped descriptor, not generated executable code or a persisted global persona.

A descriptor MUST contain `id`, `specialty`, `purpose`, `requirement_ids`, `evidence_refs`, `deliverable`, `parent_id`, `placement`, and `status`. Architecture proposes gaps alongside its normal output, avoiding an extra discovery model call. The server verifies that referenced requirements exist, the task has a concrete deliverable, and existing role ownership does not already cover it. Reject duplicate normalized scopes.

Allow at most three additional expertise-gap agents per run, beyond required workstream owners. Never allow agents to recursively create agents. The limit is a bounded selection in settings, from zero through three. Typical eligible gaps include security boundaries, data migration, and accessibility when those concerns actually exist in the brief.

Lifecycle: proposed → admitted → queued → running → completed → fused → retired. Failure, cancellation, and stale states are explicit alternatives. Emit admission before scheduling so the workspace displays the new agent. Completion means its validated deliverable exists; fusion means the deliverable is incorporated or rejected with a recorded reason. Retired agents remain inspectable in the run history but do not become permanent settings entries.

### 4. Browser workspace and question drawer

Extend `Page2AvatarCanvas.tsx`, not a parallel planning page. Reuse the lazy-loading and disposal pattern in `PlanGraph3D.tsx`, the WebGL capability check, and the existing plan tree fallback. Add an agent-room renderer because the existing renderer shows plan nodes, not agent lifecycle.

Use one workspace with Brief, Workspace, Plan, and Settings views. Settings contains Providers, Role Models, Skills, and Runtime Limits. The Workspace shows a fixed permanent-agent ring, temporary agents near their owning workstream, dependency links, and a central fused-candidate node. The Plan view retains the existing plan graph and inspectable tasks.

Clicking an agent opens a details panel showing responsibility, provider/model, evidence scope, stage, elapsed time, validated output, and findings. Render status and progress from server state only. Never fabricate percentage completion. Provide orbit, zoom, reset-view, selection, keyboard navigation, and a synchronized accessible list. Respect reduced motion. Disable animation while hidden or idle. Dispose Three.js resources and listeners on unmount. Cap device pixel ratio at two.

Provide clear empty, loading, missing-provider, failed, cancelled, reconnecting, and completed states. Retry acts on the failed stage only. Cancellation requires a clear action and leaves completed artifacts available. Local provider settings are owner-only; collaborator visibility does not grant credential or settings access.

Reuse `contracts.Decision` and `DECISION_PROMPT`/`DECISION_RESOLVE`. Preserve option impact in `syncStore.ts`, which currently drops it. Batch the current candidate's unresolved decisions into a right-side drawer occupying half the viewport on desktop and the full viewport on small screens. Each question shows context, options, recommendation, tradeoff, and affected tasks.

Open the drawer only after fusion completes for that candidate revision. Never wait on hidden questions while the UI claims agents are still working. Closing the drawer does not answer questions. Show a persistent unresolved-count action to reopen it. Validate answers against the authority's current revision; stale choices require refresh. Preserve focus trapping, Escape behavior, focus return, and owner permissions through the existing `Dialog` utilities.

After answers, update only affected work and recheck readiness. No questions means automatic continuation to approval, not automatic approval or implementation. Existing manual approval remains mandatory.

### 5. Interactive terminal and skills settings

The current terminal configuration is a line-oriented wizard in `cmd/client/cmd_config.go`; it has no full-screen planning UI. Add a real terminal frontend using Bubble Tea and its standard widgets rather than hand-written terminal escape handling. This dependency is new because no equivalent terminal framework exists in the inspected paths.

Add `plan-forge run --ui=tui`; default remains `--ui=web`. Add `plan-forge config --ui=tui` for the same settings screens. Reject full-screen mode when stdin/stdout are not terminals, with actionable usage guidance. Keep existing noninteractive commands for automation.

Refactor session presentation from browser launch without changing the executor. Extend `app.Options` with an explicit frontend selection and frontend runner callback. Both frontends receive the existing loopback session connection, authenticate through the same bridge handshake, and send the same authorized commands. Browser availability MUST NOT be required in TUI mode. Before implementing this seam, trace the concrete window-launch and handshake callers in `internal/app` and `internal/relay/local_ws.go`; their exact call signatures remain unverified.

The TUI provides Brief, Agents, Plan, Questions, and Settings tabs. Use Tab/Shift-Tab for panes, arrows for selections, Enter for actions, Escape for returning, and a visible help key. Show provider/model dropdown lists, a dependency-ordered agent list, findings, pending questions, and artifact destinations. Terminal rendering is a state-equivalent list/tree, not an attempted 3D terminal renderer. Handle resize, Unicode, long content, keyboard-only input, reconnection, and terminal restoration after errors.

Use the same settings service and validation for browser, TUI, and CLI flags. Replace open-string host/model prompts in `wizard` with catalog-backed selections. Noninteractive model flags MUST also validate IDs against the provider and explain stale/unavailable choices.

Skills settings MUST distinguish integration skills from planning guidance. Reuse `host.InspectSkillRoots`, installer ownership tracking, and existing capabilities data to show installation scope, active roots, duplicates, and modified managed files. Install/update/remove actions require an explicit confirmation and use the existing installer, never a second file writer. Never delete user-modified skills.

Add planning-skill selection by existing discovered skill identity and content hash. Skills are bounded Markdown instructions, not scripts. Users can enable them and bind them to roles. Show the exact loaded source and effective role assignments. Snapshot selected content per run; later edits apply to the next run. Reject symlinks escaping configured roots, invalid UTF-8, oversized content, duplicate identities, and unreadable files with visible errors. Limit each skill to 32 KiB and total loaded planning skills to 128 KiB. Treat skill text as untrusted context below application instructions. No automatic execution of skill tools or scripts.

### 6. Restrict AST evidence without breaking build packaging

Place one content classifier beside `LanguageForPath` and `otherSourceExts` in `internal/ast/languages.go`. Classify supported code, recognized unsupported code, Markdown (`.md`, `.markdown`), SVG (`.svg`), and excluded content. Reuse existing extension tables; unsupported source remains a coverage gap, not a falsely parsed AST.

In `scanner.go`, classify before opening or hashing content. Excluded files MUST NOT enter graph evidence, document evidence, or WorktreeHash. Keep path-only skip accounting. Supported code retains Tree-sitter parsing. Recognized unsupported code may contribute bounded textual evidence and its hash, with explicit unsupported-language coverage. Never claim symbols were extracted from it.

Read Markdown throughout the selected repository, not only README and docs paths, under existing total bounds. Treat SVG as bounded UTF-8 XML source, never image input. Do not execute scripts, resolve external entities, fetch linked resources, or render uploaded SVG markup. A malformed or binary-disguised text file is skipped with a reason. Keep existing ignore, symlink, size, count, and cancellation protections.

`detectStack` currently reads manifests outside this policy. Remove those content reads from the AST path; derive stack hints from allowed code and file basenames only. Mark hints inferred rather than manifest-verified. Bump graph/cache extractor version because hashing semantics change. Include accepted Markdown/SVG changes in invalidation; binary-only changes MUST NOT invalidate this graph.

This restriction applies to repository graph/evidence reads. It MUST NOT silently remove document-upload formats or remove assets/manifests needed by the separate build sandbox.

### 7. Implementation-ready plan output

Extend existing synthesis and validation rather than introduce a second compiler. Every generated task MUST include purpose, requirement references, existing file/symbol targets, concrete change, dependencies, acceptance criteria, and executable verification commands. New files MUST be labeled new. References to existing symbols MUST resolve against available evidence or be labeled unverified blockers.

The technical leader checks requirement coverage, interface consistency, dependency order, missing decisions, and verification completeness. Deterministic validators enforce structural checks; models explain tradeoffs rather than substitute for those checks. Include a first executable task and a dependency-ordered implementation sequence in the handoff.

Use embedded JSON schemas for all avatar outputs, including the new reviewer and gap descriptors. Populate `GenerateRequest.OutputSchema` instead of relying only on prompt prose. Retain semantic validation after schema validation. A schema-valid plan can still have inconsistent dependencies.

Readiness values are `ready`, `provisional`, and `blocked`. Ready requires resolved material decisions, valid interfaces, complete requirement coverage, and no unresolved blocking findings. Human-waived findings stay in the readiness section. Unsupported evidence or unrun implementation tests MUST NOT be portrayed as verified. The product can minimize iteration; it cannot promise zero future implementation changes.

### 8. Final README rewrite and visual demonstration

After implementation and end-to-end verification, rewrite `README.md` to describe the shipped product clearly. Explain its purpose, prerequisites, quick start, supported providers, browser/TUI workflow, agent responsibilities, checkpoint recovery, output readiness, and implementation handoff. Link detailed configuration and architecture documentation rather than duplicate it. Remove obsolete OMP instructions and unsupported claims.

Embed exactly one product screenshot and one demo GIF. Capture the implemented interface, not generated mockups. Reuse `web/scripts/preview.mjs`, `web/scripts/promo.mjs`, and their shared capture helpers; extend them only where needed. Capture a representative, non-sensitive demonstration showing brief submission, agent progress, fused plan, question drawer, and final handoff. Clearly label any seeded demonstration or time-compressed playback.

Use a crisp screenshot captured at a 1440-pixel-wide viewport with device scale factor two. Frame the main workspace so text remains readable at README display width. Produce a palette-optimized GIF at least 960 pixels wide, targeting 12–15 frames per second, 15–25 seconds, and at most 10 MiB. Prefer readable framing and deliberate pauses over decorative motion. Show only states the application actually reaches. Remove credentials, personal paths, and private repository details from captures.

Store assets under `docs/images/` with descriptive filenames and useful alt text. The screenshot MUST provide a static alternative to the animation. Inspect both assets at normal README width and on a narrow viewport. Verify GIF playback, legibility, transitions, file size, relative image paths, and documentation links. Re-run documented quick-start commands against the finished build before claiming they work.

## Execution order and ownership

Implement steps 1 and the shared lifecycle contracts first. Step 6 can run independently in an isolated worktree. Implement the orchestrator and temporary agents next. Browser and TUI work may run concurrently only after the shared snapshot, settings, and command contracts are committed. One integration owner controls contracts and the authority. Add the output-readiness changes before end-to-end acceptance.

Record the provider replacement and lifecycle decision in a new ADR during implementation. Existing ADRs remain historical. Update the affected usage/configuration documentation with behavior changes. Work occurs on a session branch, not the main checkout.

## Verification

Planning status: repository and vendor documentation inspected; no production files changed; no implementation tests or live provider probes run.

Prerequisites follow `CONTRIBUTING.md`: Go 1.27.2, Node 24, CGO compiler, browser test dependencies. Build `web/dist` before client-tagged tests. Run the full documented project gate after integration, including:

- `go test ./...`
- `go test -tags server ./cmd/relay/... ./internal/relay/...`
- `npm --prefix web ci`
- `npm --prefix web run typecheck`
- `npm --prefix web test`
- `npm --prefix web run build`
- `npm --prefix web run test:e2e`
- `go vet -tags client ./...`
- `go test -tags client ./...`

Add behavior tests alongside each change:

1. Installed Claude with no API key opens onboarding and reports API access unverified. No subprocess generation occurs.
2. Anthropic-only and Google-only installations complete every role with honest diversity labels. No-provider installation allows settings but blocks generation.
3. Invalid pins, stale catalogs, revoked keys, unsupported effort, and failed managed isolation produce explicit errors, never silent substitutions.
4. Wire tests assert direct Argon uses medium thinking. Managed runtime tests assert empty tools survives serialization and no environment is provisioned.
5. A covered brief creates zero extra gap agents. A migration brief creates one justified agent. Duplicate, recursive, and fourth-gap proposals are rejected.
6. Independent reviews see the same candidate revision and overlap under the shared semaphore. No more than four provider calls run simultaneously.
7. A reviewer finding repairs only its dependent section once. Completed unrelated drafts are reused. Budget exhaustion leaves a resumable provisional artifact.
8. Temporary-agent admission appears in browser and TUI before execution. Reconnect restores its lifecycle without duplicating calls.
9. No drawer opens before fusion. One drawer contains all current material decisions. Stale answers are rejected. No questions advances to approval without granting approval.
10. Browser tests cover WebGL and list fallback, reduced motion, selection, drawer keyboard behavior, cancellation, and resource cleanup. TUI tests cover keyboard flow, resizing, EOF, cancellation, and terminal restoration.
11. Browser/TUI settings round-trip through the same service. Role/model values cannot be free text. Modified integration skills cannot be deleted by managed cleanup.
12. Changing a tracked PNG leaves the graph hash unchanged. Changing Markdown or SVG invalidates it. Malformed UTF-8, SVG external references, escaped symlinks, and oversized files never enter evidence.
13. Ready output has complete requirement/task mapping, resolved dependencies, valid interfaces, and runnable verification commands. Unresolved blockers prevent a ready label.

Measure latency rather than promise it. Run the existing `TestLiveDeliberationEndToEnd` before and after with identical brief, repository snapshot, and provider placements. Use `PLAN_FORGE_LIVE_DELIBERATION=1 go test -tags client -run TestLiveDeliberation -timeout 60m -v ./internal/app/`. Live calls require owner-provided API credentials and spend money. Record total wall time, calls per role, input/output tokens, retries, time waiting for humans, and readiness results. Report measured differences; do not infer speed from parallelism alone.

## Assumptions and contingencies

- Direct API credentials are the supported authentication path. Local subscription sign-in is diagnostic, not transferable authorization.
- Direct Google Argon is the default PRD path. Managed Antigravity is separately labeled and capability-gated; unsupported isolation disables it rather than weakening safeguards.
- Medium thinking is selected only where the provider exposes that setting. An unavailable control is disabled with an explanation.
- One normal pass and one targeted repair are the default. Material new requirements may still require another user-directed pass.
- TUI means full keyboard access to the same planning lifecycle, not a 3D renderer inside a terminal.
- AST-only restrictions do not alter standalone document uploads or build-asset packaging.

## Crash recovery and shared startup context

Every permanent and temporary agent MUST checkpoint its own progress under the repository's `.agents/` directory. Extend the existing checkpoint implementation rather than create a competing persistence system. Exact persistence seams remain to be verified: the targeted repository lookup timed out during planning.

Use `.agents/checkpoints/plan-forge/<run-id>/` as the run namespace. Store a versioned manifest and separate agent checkpoint records. Keep run identifiers path-safe and prevent symlink escapes. The manifest indexes agent records and artifact hashes; startup MUST NOT scan every historical transcript.

Before any agent starts or resumes, load:

1. The selected run manifest and relevant agent/dependency checkpoints.
2. Project-root `README.md`.
3. Project-root `AGENTS.md`, `CLAUDE.md`, and `GEMINI.md`, when present.

Read each root document once per session snapshot. Record its path, content hash, and presence or absence. Give every agent the same immutable workspace-rules snapshot. Add only its own checkpoint and dependency results to the role-specific context. A new temporary agent MUST follow the same startup contract. Do not execute instructions that request secrets, bypass permissions, or override application safety policy. Conflicting project rules MUST produce a visible decision rather than an arbitrary precedence choice.

Required context MUST NOT be silently truncated. Oversized, unreadable, or malformed required documents block agent startup with a precise diagnostic. Missing optional root rule files are recorded as absent. Respect project confidentiality controls; external-provider context submission follows the same consent policy as repository evidence.

Checkpoint records MUST include schema version, run ID, agent ID, state, input revision, repository fingerprint, context hashes, model placement, skill hashes, dependency revisions, validated output references, unresolved decisions, completed work, next action, and last error. Store concise results and execution metadata, not hidden reasoning or credentials. Exclude absolute home paths and tokens.

Persist before dispatch, after each validated deliverable, after decision resolution, on phase transitions, and during orderly cancellation. During long-running calls, flush resumable progress at most every five seconds when it changes. Partial output MUST remain marked incomplete. A crash may lose the interval since the last flush; it MUST NOT erase previously committed progress.

Write through same-directory temporary files, fsync, and atomic rename. Publish the manifest only after referenced records are durable. Use a single run writer with an ownership lock; independent agent records MUST NOT overwrite one another. Keep the previous valid checkpoint generation for recovery. Surface storage failures and pause scheduling; never claim work is checkpointed when a write failed.

At restart, validate checkpoint versions, hashes, artifacts, and current workspace inputs before reuse. Resume unchanged completed stages without another model call. Invalidate changed stages and their dependents only. Treat an interrupted running call as indeterminate: reconnect using its provider operation ID when supported. Otherwise mark it interrupted and disclose any retry cost before resubmission. Never infer provider completion from a stale local status.

Expose resume, inspect, and explicit discard in both browser and TUI. Do not automatically delete checkpoint history. A new run does not inherit stale decisions from another run merely because it is the newest directory.

Acceptance tests MUST cover process termination during record writes, manifest publication, provider calls, and decision resolution. Also cover concurrent launches, corrupt newest checkpoints, missing artifacts, stale locks, changed root rules, changed dependencies, disk-full failures, and escaped paths. Verify that recovery preserves validated work and never marks incomplete output complete.

## Sources

- https://antigravity.google/docs/sdk/overview
- https://ai.google.dev/gemini-api/docs/antigravity-agent
- https://ai.google.dev/gemini-api/docs/custom-agents
- https://ai.google.dev/gemini-api/docs/latest-model
- Repository anchors: `internal/host/anthropicapi.go`, `internal/host/router.go`, `internal/avatars/orchestrator.go`, `internal/avatars/decisions.go`, `internal/ast/scanner.go`, `cmd/client/cmd_config.go`, `internal/app/session.go`, `web/src/pages/Page2AvatarCanvas.tsx`.
