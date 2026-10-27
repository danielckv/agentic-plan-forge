# Repository planning and remote sandbox

## Context
Expose the current repository's plans, follow-ups, epics, tasks, and state in the web app. Make deliberation recoverable and understandable for product managers and developers. Generate repository-aligned plans, specifications, and ADRs independently of deployment. Offer automatic implementation or an existing implementation PR, followed by optional remote build, tests, preview, and separate implementation acceptance.

Planning is read-only. Neither old next item has been removed yet. No builds, tests, deployment, infrastructure purchases, or working-tree edits were performed for this plan.

## Product decisions
- Plan approval authorizes implementation. Implementation acceptance requires PM sign-off and developer GitHub PR approval.
- Outcomes, outstanding questions, deliverables, and recovery actions lead the interface. Avatar activity remains expandable.
- Support both automatic implementation and attaching an existing implementation PR.
- Use E2B as the first managed sandbox provider. Do not implement the unrelated RFC service in `docs/plans/CLOUD_MODE.md`.
- Launch requires explicit confirmation of revision, scripts, permissions, resources, estimated cost, and expiry.
- Default sandbox lifetime: 60 minutes, enforced by the provider. No automatic extension or paid relaunch.
- Implementation MUST use project specs, docs, plans, applicable rules, and existing build knowledge.
- The LLM MUST reuse existing build assets first, prepare missing scripts, and record the recipe for future runs.
- The prepared VM MUST execute the reviewed, recorded build/test/run recipe. It MUST NOT invent commands during deployment.
- CI packages the selected revision for the prepared sandbox. Never clone the repository onto a rented VM.

## Verified reuse and gaps
- `web/src/App.tsx` has ingestion, deliberation, synthesis, and build pages. Extend these surfaces rather than introducing a second application.
- `internal/app/planfiles.go` exposes generated draft epics, not a browser for existing repository markdown. Keep those data sources distinct.
- `internal/gather/files.go:18-29,69-118` discovers bounded context files. It currently omits root follow-up files and is not a user-facing document index.
- `web/src/pages/Page1Ingestion.tsx:84-93` consumes ingestion revision but does not render its ambiguities. A search of `web/src` found no ambiguity resolution command.
- Existing `AMBIGUITY_RESOLVE` and `DECISION_PROMPT`/resolution machinery provide question authority and persistence; extend their UI, not parallel question storage.
- `internal/app/runcheckpoint.go:40-70` returns a load error before the first `Put` can save. Later calls can proceed. Preserve diagnostics and make recovery explicit rather than silently deleting evidence.
- `internal/synthesizer/compiler.go:247-311` generates a deterministic combined specification and manifest. It does not emit separate repository-aligned plan/spec/ADR files.
- `internal/storage/artifacts.go:117-124` defines journaled spec/manifest commits with manifest-last publication. Preserve this approved pair and its hash consistency.
- `internal/delivery/build.go:226-305` currently implements locally and previews through local Docker. The new flow must not inherit local execution.
- `internal/delivery/deploy.go:104-139` delegates to AgentBox and extracts a URL. That is not a durable managed sandbox lease.
- `.github/workflows/ci.yml` provides dispatch, client-tag tests after web build artifacts, and browser verification. Extend it for new tests.

## Approach

Execute steps in order. Steps 2 and 3 can be implemented independently after step 1. Step 4 consumes their contracts. Steps 5–8 are sequential. Use the session worktree and one session PR; preserve unrelated work. Every behavior change carries its regression test. No worker runs builds or tests mid-flight; verification runs in CI after integration.

### 1. Retire next-1 and next-2 first
Edit `.agents/STATE.md` before implementation. Remove these two active `## Next` bullets:
- `run a real /plan-forge end to end and confirm the architect reaches synthesis inside 300s`
- `decide: first-run dev-server preview (agent runs project with user approval → screenshot + DOM outline → architect context) → follow-up PR if approved`

Record both under `## Done` as `2026-10-27: not needed by user direction`, retaining their identities. Preserve historical session records. Replace active next work with this plan's ordered steps. This retires those tasks; it does not claim either was completed.

### 2. Browse actual repository work
Extend `internal/gather` with a shared bounded document discovery layer. Reuse its markdown handling and existing context source selection, but do not repurpose relevance-ranked excerpts as a complete inventory.

Add owner-session read APIs in `internal/app`: `GET /api/repository-work` and `GET /api/repository-work/document?id=<opaque-id>`. Index repository-relative markdown in existing planning/spec/ADR directories, `.agents/STATE.md`, and root files whose names identify plans, epics, tasks, follow-ups, or state. Support additional owner-configured repository-relative roots for nonstandard layouts. Do not crawl dependencies, `.git`, generated build trees, or arbitrary host paths.

Return document ID, relative path, kind, content hash, headings, source line ranges, and extracted work items. Each item carries source text, source range, explicit status when available, and dependencies only when stated. Parse headings, task checkboxes, and explicit state sections deterministically. Unknown status stays `unknown`; never infer completion from prose or stale history. Prefer the active state file's explicit next order. Other items retain source order and remain separately labeled.

Bounds: 400 documents, 2 MB per file, 20 MB total decoded text, recursion depth 4, and a cancellable 2-second indexing budget. Return `partial` plus skipped reasons on limits or unreadable files. A zero-document result is a normal empty state. Cache by file metadata with content hash confirmation on open; refresh explicitly and after relevant exports.

Reject traversal, absolute paths, symlinks at any path component, and files that escape the repository during opening. Use descriptor/root-constrained opening rather than a pre-check followed by unrestricted reading. Render sanitized markdown without raw HTML or active embedded content. Do not expose host paths or secrets through errors.

Add a Repository work view reachable before and during planning without changing the authoritative workflow phase. Show Next, Epics/tasks, Plans/specs/ADRs, Follow-ups, and State filters. Selecting an item opens its real source with line references. Selecting “Use in brief” adds explicit source references and an editable excerpt; browsing alone never mutates documents or starts a model call.

### 3. Bound and recover deliberation
Reuse executor run-generation fencing, host failover, stage timeouts, and validated checkpoints. Do not add another retry loop around existing retries.

Expose per-stage states `queued`, `running`, `waiting_for_answer`, `retrying`, `failed`, `cancelled`, and `completed`, with start time, last activity, configured deadline, and recovery action. Derive these from authoritative events/checkpoints, not browser timers. A heartbeat means alive, not progress. Timeout or lost lease must become a visible terminal/paused condition, not an endless spinner.

In `internal/app/runcheckpoint.go`, distinguish invalid checkpoint content from permission/path errors. Preserve invalid bytes in a bounded private quarantine before replacing them; show a recovery warning. A fresh checkpoint save must succeed on its first attempt after successful quarantine. Permission, full-disk, or unsafe-path failures must remain errors and cannot be reported as saved. Reuse storage's atomic write discipline.

Resume only after owner action and successful checkpoint identity validation. Never automatically restart work after lease loss or ambiguous provider completion. Reconnect restores pending questions and completed drafts. Retry resumes the failed stage when dependencies match; otherwise show which changed input requires restarting.

Bound the executor event queue in `internal/app/room.go`. Coalesce disposable heartbeat/status updates by stage. Preserve ordered decisions, drafts, approval events, and terminal failures. If essential events cannot be queued, fail closed and trigger snapshot/resynchronization rather than silently dropping them. Add cancellation checks and join owned goroutines on shutdown. Keep provider calls and process termination bounded by existing configured timeouts.

Reliability acceptance means bounded failures, preserved validated work, and recoverability—not a guarantee that crashes are impossible. Do not claim a universal five-minute completion time without measured representative runs.

### 4. Put questions and outcomes first
Add normalized-PRD parsing alongside existing draft parsers. Add `resolveAmbiguity(id, choice, note)` to the sync store using existing `AMBIGUITY_RESOLVE`; retain `product`, `technical`, and `dismiss` wire values. Display question text, the relevant excerpt, and a plain-language explanation of the choices. Label these as classification questions, not fabricated product decisions.

Present deliberation decisions through the existing decision protocol. Show the question, why it matters, affected work, 2–4 available options, recommendation when supplied, and optional rationale. Preserve unanswered cards across reconnect. Disable submission for observers, stale revisions, disconnected clients, or already resolved questions. Explain conflicts and show the accepted answer.

Rework `Page2AvatarCanvas` into an outcome-first progress workspace: current goal, completed outputs, current stage, blocking question, elapsed time, and the next available action. Keep avatar identity, role, host, and diagnostic output behind an expandable details panel. Do not remove developer evidence or hide failures.

Put Retry, Resume, Cancel, and Edit brief next to the state that enables them. Show saved-work consequences before edits that invalidate drafts. Questions requiring human input pause automation visibly; they do not count as unexplained stalls. Cover keyboard navigation, focus after resolution, screen-reader announcements, and small screens using the existing UI components.

### 5. Prepare reusable implementation and execution knowledge
Extend synthesis/delivery inputs with a versioned `ExecutionRecipe`. Store its approved JSON with the generated plan artifacts; expose a human-readable recipe in the implementation view. Keep executable scripts in the project's existing script/build locations. If no suitable script directory exists, use `scripts/`; do not add wrappers around commands already provided by Make, package scripts, task runners, containers, or workflows.

The recipe records: schema version, repository revision, source document paths and content hashes, applicable instruction hashes, selected project/subproject root, toolchain versions, existing or generated script paths and hashes, dependency lockfile hashes, build/test/run argv arrays, working directories, per-step timeouts, required environment variable names, runtime port, readiness probe, output artifact paths, and supported prepared-sandbox template. Store secret references only, never values.

The LLM first reads the selected plan/spec/ADR, linked repository documentation, applicable repository rules, current CI, manifests, lockfiles, and existing build assets. Use source references to explain each chosen command. Written policy constrains commands; working source establishes what exists. A conflict that affects execution becomes a blocking UI question. Do not silently pick whichever instruction is newer.

Reuse existing commands unchanged where possible. Generate only missing build/test/run scripts, with explicit error propagation, cleanup, bounded operations, and no invented successful checks. Generate tests for script behavior in the same change. Review the proposed script diff and recipe before execution. Script content is untrusted code even when an LLM wrote it; syntax validation is not a security boundary.

Persist the recipe and scripts in the implementation PR so later runs reuse them. Hash the exact recipe, scripts, relevant instructions, manifests, and lockfiles. A mismatch triggers review and revalidation; it must not silently reuse a green receipt. Unchanged inputs reuse the recipe without another model call. Missing test commands show `not configured`, never `passed`; require an explicit recorded waiver before proceeding without them.

Apply this to both implementation paths. Automatic implementation runs the existing coding-host integration inside an authorized CI/prepared environment, with the approved plan and repository evidence. Existing-PR mode reads that PR's exact head and prepares or reviews its execution recipe. If coding-host credentials are unavailable, disable automatic implementation with setup instructions; keep existing-PR and document-only modes available.

### 6. Generate repository-aligned documents without a VM
Extend deterministic compilation to produce a document bundle in addition to the existing approved spec/manifest pair. Include a plan with ordered epics/tasks, a specification with acceptance criteria, and ADRs for actual architectural decisions. Do not invent an architectural decision solely to create an ADR.

Choose output paths from explicit project documentation configuration first, then existing matching directories and naming conventions. If no conventions exist, propose `docs/plans/`, `docs/specs/`, and `docs/adrs/`. Show the complete proposed file list before export. Ambiguous destinations or incompatible conventions become UI questions; never overwrite an existing ADR or renumber history silently.

Represent bundle entries as relative path, kind, bytes/hash, and expected prior hash for an approved replacement. Reject duplicate paths, traversal, symlinks, changed destinations, and collisions. Stage and journal the bundle with a manifest commit marker so partial exports are not presented as complete. Extend storage recovery tests; retain the canonical approved pair for existing consumers. Do not bypass relay artifact allowlists: extend versioned artifact contracts and validation together if followers download the bundle.

Show plan approval, document export, and implementation acceptance as distinct statuses. “Documents only” generates and exports the same planning deliverables and recorded execution recipe without a sandbox account, coding credentials, launch, or fabricated validation receipt. Generated scripts remain marked unverified until actually checked.

### 7. Build, test, and preview in a prepared VM
Extend the existing Build page and delivery controller rather than adding a cloud planning binary. Replace the new flow's local coding/Docker execution with CI/prepared-sandbox execution. Record this changed execution boundary in a new ADR; retain earlier ADRs as historical decisions.

Use E2B private sandboxes with `allowPublicTraffic: false` and a provider timeout of 3600 seconds. Add one provider adapter under `internal/delivery`; no equivalent managed lease adapter exists. Use the documented sandbox REST lifecycle and authenticated process/file APIs. Keep provider credentials server-side and outside the guest. Do not introduce a model-based deployment agent.

CI checks out the exact selected commit and packages an allowlisted source bundle plus approved recipe/scripts for the prepared sandbox. It must exclude `.git`, host state, credentials, and unrelated artifacts. Validate paths and archive bounds during packaging and extraction. Transfer the bundle to the sandbox; no `git clone` on the VM. Provision the declared toolchain template, then execute the recorded build, test, and run commands there. Source and script hashes must match the approved request before any execution.

For automatic implementation, use an explicitly authorized coding environment. Separate the code-running job from the trusted job that publishes the resulting PR. Do not expose GitHub write tokens or provider master credentials to project scripts. Fork or untrusted PR workflows must not gain secrets through `pull_request_target` execution. Required application secrets are individually selected by the owner and injected only for that sandbox.

Persist sandbox ID, operation ID, repository/PR/head, approval and recipe hashes, provider expiry, command receipts, and lifecycle state outside the guest. States: `preparing`, `building`, `testing`, `starting`, `ready`, `failed`, `stopping`, `stopped`, `expired`, and `unknown`. An ambiguous network result becomes `unknown`; reconcile by operation identity before retrying creation. Never duplicate a paid VM on a blind retry.

Build/test failure prevents preview-ready status. Readiness requires the declared probe, not merely an opened port. Capture bounded, redacted logs and exact exit codes. Keep command deadlines shorter than the remaining sandbox lifetime. On failure/cancel, collect available receipts and request deletion. If deletion cannot be confirmed, display cleanup pending; provider expiry remains the backstop. Reconcile persisted leases after restart.

Offer Stop and an explicit extension action with new cost/expiry confirmation. If the account cannot extend runtime, offer a separately confirmed fresh run from the same bundle; do not silently relaunch. Default resources are 2 vCPU and 4 GiB; incompatible workloads require a visible resource/template question before spending.

E2B preview traffic needs an authentication header. Serve previews through a dedicated, authenticated loopback proxy on a separate origin from the control UI. Keep its upstream fixed to the sandbox and approved application port. Inject the traffic token server-side; support WebSocket upgrades. Do not expose control cookies, provider headers, internal ports, or a generic URL proxy. Bootstrap access with a single-use short-lived nonce, exchange it for a private HttpOnly cookie, and remove the nonce from the URL. Revoke access on stop/expiry. The untrusted application must never share the Plan Forge control origin.

Provide launch progress, script/source evidence, checks, logs, expiry countdown, and Open preview. No account or missing provider secret produces a setup state, not a failed planning session. Live launch remains opt-in; no credentials or cost authorization means integration tests only, explicitly reported as not live-verified.

### 8. Accept the exact implementation
Extend planstate with implementation acceptance records, separate from existing plan approval. Bind each record to approval ID, manifest hash, repository, PR number, exact head SHA, recipe hash, artifact/bundle digest, and validation receipts. Record the authenticated PM actor and timestamp.

Read GitHub approval through an authenticated server-side client. Require a non-dismissed approval covering the current head from a developer with repository write/maintain/admin permission, other than the PR author. Respect branch-protection requirements and blocking change requests. API failures or insufficient permissions produce `unknown`, never approved. Do not accept browser-submitted reviewer identity as evidence.

Accept only when PM sign-off and qualifying developer approval refer to the same current revision and validation policy is satisfied. Recheck GitHub immediately before acceptance. New commits, recipe/script changes, revoked reviews, or stale receipts invalidate acceptance. Keep document-only approval available without implementation acceptance. Display exactly what remains outstanding; never merge the PR automatically.

## Verification
All builds, tests, and application runs occur in CI or prepared sandboxes. Extend `.github/workflows/ci.yml`; do not run local commands as a shortcut. Use the existing client-tag job after its web-dist prerequisite, plus frontend unit and browser jobs. Record actual run URLs, SHAs, commands, and exit codes.

Required checks inside CI:
- `go test -tags client ./...` with the workflow's existing CGO and web artifact setup.
- `go test -tags server ./cmd/relay/... ./internal/relay/...` for changed wire/artifact handling.
- `npm --prefix web run typecheck` and `npm --prefix web run test`.
- `npm --prefix web run build` and `npm --prefix web run test:e2e` using existing bridge setup.

Add behavior proofs:
1. State retirement: both specified bullets absent from active Next; both recorded as not needed, not completed.
2. Repository work fixture: STATE next list, checked/unchecked epic tasks, root FOLLOW_UPS, nested plans, conflicting historical notes. UI shows exact source lines and explicit status without model calls. Symlink escapes, oversize files, malformed UTF-8, timeout, and empty repo return safe bounded results.
3. Runtime failure fixtures: host timeout, partial-output failure, queue saturation, checkpoint corruption, failed writes, lease loss, cancel during a stage, and reconnect with a pending decision. Each yields bounded status, no duplicate execution, and preserved valid outputs. Verify the checkpoint regression fails before its fix.
4. Questions: owner answers an ingestion ambiguity without another normalization call; observer cannot answer; stale/duplicate answers are reconciled; keyboard focus moves to the next question.
5. Recipe fixtures: an existing Make/package-script project reuses commands; a project missing run scripts gets a reviewed generated script; contradictory docs block execution. An unchanged recipe uses no model call. Changed lockfile/script/instruction hash invalidates receipts.
6. Documents-only: export plan/spec/decision ADR with no provider credentials or launch. Existing ADR collision and interrupted export preserve prior valid artifacts; generated-but-unexecuted scripts show unverified.
7. Provider contract tests: duplicate click, create timeout, lost connection, failed build/test/readiness, expiry, stop failure, and restart reconciliation. No false ready state or duplicate paid creation. Unauthenticated preview access fails; preview content cannot access control-origin credentials; WebSocket preview works.
8. Acceptance fixtures: PM-only, review-only, stale head, dismissed approval, blocking changes requested, API unavailable, and changed recipe all remain unaccepted. Matching PM sign-off plus eligible current-head review and required receipts accepts precisely that revision.
9. Credential-gated live CI: package a small web fixture, transfer it into the prepared E2B VM, execute its persisted build/test/run scripts, open the authenticated preview, capture receipts, stop it, and confirm deletion. Run only with explicit spend authorization. Verify recorded script hashes and actual exit codes match the displayed revision.

Dispatch the updated CI on the session branch using `gh workflow run ci.yml --ref <session-branch>`. Observe the resulting run rather than treating dispatch as verification. Commit/push the session branch and publish its PR through the repository's normal gated flow. Do not claim live provider verification if it was not authorized and run.

## Assumptions and contingencies
- Linux prepared sandboxes are the first execution target. Unsupported native/GPU requirements stop at a visible capability question; no silent local fallback.
- E2B lifecycle and private-traffic contracts are documented at https://docs.e2b.dev/sandbox.md and https://docs.e2b.dev/network/restrict-public-access.md. Confirm the pinned API version during implementation; incompatible contracts block launch rather than weakening isolation.
- Published E2B CPU/RAM rates imply approximately $0.1656 per running hour for 2 vCPU / 4 GiB, excluding subscription and other charges: https://e2b.dev/pricing. Treat this as an estimate, not a billing guarantee. Show current configured rates and disclose when unavailable.
- No paid sandbox is created while planning. Missing cloud/coding credentials disable only the affected execution path.
- A project with no actual architectural decision receives no fabricated ADR. The document view explains why no ADR was needed.
