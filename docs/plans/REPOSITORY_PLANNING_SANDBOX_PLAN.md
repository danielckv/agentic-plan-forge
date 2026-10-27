# Repository planning and remote sandbox

## Context

The web app shows one repository's plans, follow-ups, epics, tasks and state. A
deliberation survives a crash and can be resumed. A plan is approved once and
then implemented, built, tested, previewed and accepted as separate, visible
steps.

Planning is read-only. Browsing a repository never starts a model call and never
writes a document.

## Product decisions

- Plan approval authorizes implementation. Implementation acceptance needs two
  separate records: PM sign-off and a developer's GitHub approval on the pull
  request.
- Outcomes, open questions, produced documents and recovery actions lead the
  interface. Avatar activity stays behind an expandable detail panel.
- Two implementation paths: automatic implementation, and attaching an existing
  pull request.
- E2B is the first managed sandbox provider. The unrelated RFC service in
  `docs/plans/CLOUD_MODE.md` is out of scope.
- A launch needs explicit confirmation of revision, scripts, permissions,
  resources, estimated cost and expiry.
- Default sandbox lifetime is 60 minutes, enforced by the provider. No automatic
  extension and no automatic paid relaunch.
- Implementation uses the project's specs, docs, plans, applicable rules and
  existing build knowledge.
- The model reuses existing build assets first, prepares what is missing, and
  records the result as a recipe.
- The prepared VM runs the reviewed, recorded build, test and run commands. It
  does not invent commands during deployment.
- CI packages the selected revision for the prepared sandbox. The repository is
  never cloned onto a rented VM.

## Verified reuse and gaps

The window already has ingestion, deliberation, synthesis and build pages.
These are extended rather than duplicated in a second application.

| Area | Existing | Gap |
| --- | --- | --- |
| Repository reading | `internal/gather` markdown handling and context source selection | No complete inventory of planning documents |
| Deliberation state | executor run fencing, host failover, stage timeouts, validated checkpoints | Stage states, resume and a bounded event queue |
| Questions | `AMBIGUITY_RESOLVE` and the decision protocol | No inbox, no outcome-first presentation |
| Execution knowledge | Make, package scripts, task runners, CI workflows | No versioned recipe with hashes |
| Documents | deterministic spec and manifest pair | No plan, spec and ADR bundle |
| Sandbox | Docker-based local build | No managed lease |
| Acceptance | plan approval | No implementation acceptance bound to a revision |

## Approach

The steps are ordered. Steps 2 and 3 are independent of each other. Step 4
consumes their contracts. Steps 5 to 8 are sequential. Every behaviour change
carries a regression test that fails before it. Verification runs in CI after
integration.

### 1. Retire two stale next items

Two items in the active state file are no longer wanted:
"run a real /plan-forge end to end and confirm the architect reaches synthesis
inside 300s", and the first-run dev-server preview follow-up. Both are recorded
as retired by direction, not as completed. The state file keeps its history and
takes this plan's ordered steps as active work.

### 2. Browse actual repository work

Extend `internal/gather` with a bounded document discovery layer. It reuses the
existing markdown handling, but a relevance-ranked excerpt list is not an
inventory.

Add read APIs `GET /api/repository-work` and
`GET /api/repository-work/document?id=<opaque-id>`. Index repository-relative
markdown in existing planning, spec and ADR directories, the active state file,
and root files whose names identify plans, epics, tasks, follow-ups or state.
Owners can configure extra repository-relative roots for a non-standard layout.
Dependencies, `.git`, generated build trees and host paths are never crawled.

Each document returns an opaque ID, its relative path, kind, content hash,
headings, source line ranges and extracted work items. An item carries its
source text, source range, explicit status where one exists, and dependencies
only where they are stated. Headings, task checkboxes and explicit state
sections are parsed deterministically. Unknown status stays `unknown`: prose
and stale history never imply completion. The active state file's explicit
order wins; other items keep source order and stay separately labelled.

Bounds: 400 documents, 2 MB per file, 20 MB of decoded text in total, depth 4,
and a cancellable 2-second indexing budget. Exceeding one returns `partial` with
the reasons. A repository with no documents is a normal empty state. The index
caches on file metadata and confirms with the content hash when a document is
opened. It refreshes on request and after an export.

Traversal, absolute paths, symlinks at any component, and files that escape the
repository are rejected. Opening is constrained by a root descriptor rather
than a check followed by an unrestricted read. Markdown is rendered sanitized,
with no raw HTML. Errors never carry host paths or secrets.

A Repository work view is reachable before and during planning without changing
the workflow phase. It filters Next, Epics and tasks, Plans and specs and ADRs,
Follow-ups, and State. Selecting an item opens its real source with line
references. "Use in brief" adds explicit source references and an editable
excerpt.

### 3. Bound and recover deliberation

Reuse the executor's run fencing, host failover, stage timeouts and validated
checkpoints. No second retry loop is layered on the existing retries.

Each stage reports `queued`, `running`, `waiting_for_answer`, `retrying`,
`failed`, `cancelled` or `completed`, with start time, last activity, configured
deadline and the recovery action available. These come from authoritative events
and checkpoints, not from browser timers. A heartbeat means the process is alive,
not that it is progressing. A timeout or a lost lease becomes a visible paused or
terminal state, never an endless spinner.

`internal/app/runcheckpoint.go` separates invalid checkpoint content from
permission and path errors. Invalid bytes are preserved in a bounded private
quarantine before they are replaced, and the window shows a recovery warning. The
next save succeeds on its first attempt. Permission, full-disk and unsafe-path
failures stay errors and are never reported as saved. Writes follow the existing
atomic discipline in storage.

Resume happens only after owner action and after the checkpoint identity checks
out. Work never restarts automatically after a lease loss or an ambiguous
provider completion. A reconnect restores pending questions and finished drafts.
Retry resumes the failed stage when its dependencies still match; otherwise the
window names the input that changed and requires a restart.

The executor event queue in `internal/app/room.go` is bounded. Disposable status
and heartbeat updates coalesce per stage. Ordered decisions, drafts, approvals and
terminal failures are preserved. If an essential event cannot be queued the
executor fails closed and resynchronizes from a snapshot instead of dropping it.
Shutdown cancels and joins owned goroutines. Provider calls and process
termination stay inside their configured timeouts.

Reliability here means bounded failures, preserved valid work and recoverability.
It does not mean crashes cannot happen, and no universal five-minute completion
time is claimed without measured runs.

### 4. Put questions and outcomes first

A normalized-PRD parser joins the existing draft parsers. The sync store gains
`resolveAmbiguity(id, choice, note)` on the existing `AMBIGUITY_RESOLVE` path,
keeping the `product`, `technical` and `dismiss` wire values. A question shows its
text, the excerpt it came from and a plain-language explanation of the choices.
These are classification questions, not invented product decisions.

Deliberation decisions use the existing decision protocol. A card shows the
question, why it matters, the work it affects, two to four options, the
recommendation when one was supplied, and an optional rationale. Unanswered cards
survive a reconnect. Submission is disabled for observers, stale revisions,
disconnected clients and questions already resolved. A conflict is explained and
the accepted answer shown.

`Page2AvatarCanvas` becomes an outcome-first workspace: current goal, produced
documents, current stage, blocking question, elapsed time and the next available
action. Avatar identity, role, host and diagnostic output move behind an
expandable panel. Developer evidence is never removed and failures are never
hidden.

Retry, Resume, Cancel and Edit brief sit next to the state that enables them. An
edit that invalidates drafts states its effect on saved work first. A question
that needs a person pauses automation visibly; it is not counted as an
unexplained stall. Keyboard navigation, focus after resolution, screen-reader
announcements and small screens are covered with the existing components.

### 5. Prepare reusable implementation and execution knowledge

Synthesis and delivery inputs gain a versioned `ExecutionRecipe`. Its approved
JSON is stored with the plan artifacts and shown in human-readable form in the
implementation view. Executable scripts live where the project already keeps
them. If there is no script directory, `scripts/` is used. Commands already
provided by Make, package scripts, a task runner, a container or a workflow are
never wrapped.

The recipe records: schema version, repository revision, source document paths
and hashes, applicable instruction hashes, the selected project or subproject
root, toolchain versions, script paths and hashes, dependency lockfile hashes,
build, test and run argv arrays, working directories, per-step timeouts, required
environment variable names, runtime port, readiness probe, output artifact paths
and the prepared-sandbox template it supports. Secrets are stored as references,
never as values.

The model reads the selected plan, spec and ADR, the linked documentation, the
applicable repository rules, the current CI, manifests, lockfiles and existing
build assets before it writes anything. Each chosen command is explained against
a source reference. Written policy constrains commands; the working tree
establishes what exists. A conflict that changes execution becomes a blocking
question. The newer instruction is never silently preferred.

Existing commands are reused unchanged where possible. Only a missing build, test
or run script is generated, with explicit error propagation, cleanup, bounded
runtime and no invented passing check. Its tests land in the same change. The
owner reviews the script diff and the recipe before anything executes. Script
content is untrusted code even when a model wrote it; a syntax check is not a
security boundary.

The recipe and its scripts live in the implementation pull request, so a later
run reuses them. The exact recipe, scripts, instructions, manifests and lockfiles
are hashed. Any mismatch triggers review and revalidation and never silently
reuses a passing receipt. Unchanged inputs reuse the recipe without another model
call. A missing test command shows `not configured`, never `passed`, and needs a
recorded waiver before the run continues.

Both implementation paths use this. Automatic implementation runs the coding-host
integration inside an authorized CI or prepared environment with the approved
plan and the repository evidence. Existing-PR mode reads that pull request's
exact head and prepares or reviews its recipe. Without coding-host credentials,
automatic implementation is disabled with setup instructions, while
existing-PR and documents-only modes stay available.

### 6. Generate repository-aligned documents without a VM

Deterministic compilation also produces a document bundle beside the approved
spec and manifest pair: a plan with ordered epics and tasks, a specification with
acceptance criteria, and ADRs for decisions that were actually made. No
architectural decision is invented to produce an ADR.

Output paths come from explicit project documentation configuration first, then
from matching existing directories and naming conventions. With no convention,
`docs/plans/`, `docs/specs/` and `docs/adrs/` are proposed. The complete file list
is shown before export. An ambiguous destination or an incompatible convention
becomes a question. An existing ADR is never overwritten and history is never
renumbered silently.

A bundle entry is a relative path, a kind, its bytes and hash, and the expected
prior hash when it replaces an approved document. Duplicate paths, traversal,
symlinks, changed destinations and collisions are rejected. The bundle is staged
and journaled with a manifest written last, so a partial export is never
presented as complete. Storage recovery tests cover an interruption. The
canonical approved pair stays for existing consumers. Relay artifact allowlists
are not bypassed: if followers download the bundle, the versioned artifact
contract and its validation change together.

Plan approval, document export and implementation acceptance are three separate
statuses. "Documents only" produces the same planning deliverables and recorded
recipe with no sandbox account, no coding credentials, no launch and no invented
validation receipt. A generated script stays marked unverified until it actually
runs.

### 7. Build, test and preview in a prepared VM

The existing Build page and delivery controller are extended; no new cloud
planning binary is added. The new flow replaces local coding and Docker execution
with CI and prepared-sandbox execution. That changed execution boundary is
recorded in a new ADR, and earlier ADRs stay as recorded decisions.

E2B private sandboxes run with `allowPublicTraffic: false` and a provider timeout
of 3600 seconds. One provider adapter sits under `internal/delivery`; no managed
lease adapter existed. The documented sandbox REST lifecycle and the
authenticated process and file APIs are used. Provider credentials stay
server-side and never enter the guest. No model-based deployment agent is
introduced.

CI checks out the exact selected commit and packages an allowlisted source bundle
together with the approved recipe and scripts. `.git`, host state, credentials and
unrelated artifacts are excluded. Paths and archive bounds are validated when
packaging and when extracting. The bundle is transferred to the sandbox; the
repository is not cloned there. The declared toolchain template is provisioned,
then the recorded build, test and run commands execute. Source and script hashes
must match the approved request before anything runs.

Automatic implementation uses an explicitly authorized coding environment. The
job that runs code is separate from the trusted job that publishes the resulting
pull request. GitHub write tokens and provider master credentials are never
exposed to project scripts. Fork and untrusted pull request workflows gain no
secrets through `pull_request_target`. Application secrets are selected
individually by the owner and injected only into that sandbox.

Sandbox ID, operation ID, repository, pull request, head, approval and recipe
hashes, provider expiry, command receipts and lifecycle state are persisted
outside the guest. The states are `preparing`, `building`, `testing`, `starting`,
`ready`, `failed`, `stopping`, `stopped`, `expired` and `unknown`. An ambiguous
network result becomes `unknown` and is reconciled by operation identity before a
retry. A paid VM is never duplicated by a blind retry.

A build or test failure prevents preview-ready status. Readiness means the
declared probe passed, not that a port opened. Logs are bounded and redacted and
carry exact exit codes. A command deadline stays shorter than the remaining
sandbox lifetime. On failure or cancel the available receipts are collected and
deletion is requested. When deletion cannot be confirmed the window shows
cleanup pending, and provider expiry remains the backstop. Persisted leases are
reconciled after a restart.

Stop is always offered. Extension is an explicit action with a new cost and
expiry confirmation. When the account cannot extend, a fresh run from the same
bundle is offered and separately confirmed; nothing relaunches silently. Default
resources are 2 vCPU and 4 GiB. An incompatible workload produces a visible
resource or template question before any spend.

E2B preview traffic needs an authentication header, so previews are served
through a dedicated authenticated loopback proxy on an origin separate from the
control UI. Its upstream is fixed to the sandbox and the approved application
port. The traffic token is injected server-side and WebSocket upgrades are
supported. Control cookies, provider headers, internal ports and a generic URL
proxy are never exposed. Access starts from a single-use short-lived nonce,
exchanged for a private HttpOnly cookie, with the nonce removed from the URL.
Access is revoked on stop and on expiry. The untrusted application never shares
the Plan Forge control origin.

The window shows launch progress, source and script evidence, checks, logs, an
expiry countdown and Open preview. A missing account or provider secret produces
a setup state rather than a failed planning session. Live launch stays opt-in;
without credentials or cost authorization only integration tests run, and they
are reported as not live-verified.

### 8. Accept the exact implementation

The plan state gains implementation acceptance records, separate from plan
approval. Each record binds the approval ID, manifest hash, repository, pull
request number, exact head SHA, recipe hash, artifact and bundle digest, and the
validation receipts. It records the authenticated PM actor and the time.

GitHub approval is read through an authenticated server-side client. Acceptance
requires a non-dismissed approval covering the current head from a developer with
write, maintain or admin permission, other than the pull request author. Branch
protection and blocking change requests are respected. An API failure or
insufficient permission yields `unknown`, never approved. A reviewer identity sent
by the browser is never evidence.

Acceptance happens only when PM sign-off and a qualifying developer approval point
at the same current revision and the validation policy is satisfied. GitHub is
rechecked immediately before acceptance. New commits, recipe or script changes,
revoked reviews and stale receipts all invalidate it. Document-only approval stays
available without implementation acceptance. The window lists exactly what
remains outstanding and never merges the pull request.

## Verification

Builds, tests and application runs happen in CI or a prepared sandbox. Extend
`.github/workflows/ci.yml` rather than running a local command as a shortcut. The
existing client-tag job runs after its web-dist prerequisite, alongside the
frontend unit and browser jobs. Real run URLs, SHAs, commands and exit codes are
recorded.

Checks in CI:

- `go test -tags client ./...` with the workflow's CGO and web artifact setup.
- `go test -tags server ./cmd/relay/... ./internal/relay/...` for changed wire and
  artifact handling.
- `npm --prefix web run typecheck` and `npm --prefix web run test`.
- `npm --prefix web run build` and `npm --prefix web run test:e2e`.

Behaviour proofs:

1. State retirement: both named items are gone from active work and recorded as
   retired, not completed.
2. Repository work fixture: a state next list, checked and unchecked epic tasks,
   root follow-ups, nested plans and conflicting historical notes. The view shows
   exact source lines and explicit status with no model call. A symlink escape, an
   oversize file, malformed UTF-8, a timeout and an empty repository all return
   bounded results.
3. Runtime failure fixtures: host timeout, partial-output failure, queue
   saturation, checkpoint corruption, failed writes, lease loss, cancel during a
   stage, and a reconnect with a pending decision. Each yields a bounded status,
   no duplicate execution and preserved valid output. The checkpoint regression
   fails before its fix.
4. Questions: the owner answers an ingestion ambiguity without another
   normalization call; an observer cannot answer; stale and duplicate answers
   reconcile; focus moves to the next question.
5. Recipe fixtures: a project with Make or package scripts reuses them; a project
   without a run script gets a reviewed generated one; contradictory docs block
   execution. An unchanged recipe costs no model call. A changed lockfile, script
   or instruction hash invalidates the receipts.
6. Documents only: export a plan, a spec and a decision ADR with no provider
   credentials and no launch. An existing ADR collision and an interrupted export
   both preserve the prior valid artifacts. A generated script that has not run is
   shown unverified.
7. Provider contract tests: duplicate click, create timeout, lost connection,
   failed build, test or readiness, expiry, a stop failure, and restart
   reconciliation. No false ready state and no duplicate paid creation.
   Unauthenticated preview access fails, preview content cannot reach
   control-origin credentials, and a WebSocket preview works.
8. Acceptance fixtures: PM sign-off only, review only, stale head, dismissed
   approval, blocking changes requested, unavailable API and changed recipe all
   stay unaccepted. Matching PM sign-off with an eligible current-head review and
   the required receipts accepts exactly that revision.
9. Credential-gated live CI: package a small web fixture, transfer it into the
   prepared E2B VM, run its persisted build, test and run scripts, open the
   authenticated preview, capture receipts, stop it and confirm deletion. It runs
   only with explicit spend authorization, and the recorded script hashes and exit
   codes are checked against the displayed revision.

The workflow is dispatched on the branch with
`gh workflow run ci.yml --ref <branch>` and the resulting run is observed; a
dispatch is not verification. Live provider verification is never claimed when it
was not authorized and run.

## Backlog

Recorded here so the idea is not lost. None of it is built yet.

**A backlog, so a second plan can start while the first is being implemented.**
Today one session carries one deliberation and one plan. Implementation then
blocks the window: the plan is approved, coded, built and accepted before
anything else begins. A team usually has the next piece of work in mind while
the current one is in review, and making them wait means the next brief is
written somewhere else.

The shape of it:

- A session holds many plans, not one. A plan joins the backlog when it is
  approved, and stays there while implementation runs.
- The backlog is a list of approved plans with their own state: waiting,
  implementing, in review, accepted. Each keeps its approval ID, manifest hash
  and revision, the same binding acceptance already relies on.
- Starting the next plan does not disturb the running one. The executor runs one
  implementation at a time — a second coding run would race the first over the
  worktree and the sandbox — so the queue advances as each one finishes, and a
  queued plan shows what it is waiting for.
- The Work tab already reads the repository's own planning files, which is the
  same surface a backlog entry would appear on. A plan that lands in
  `docs/plans/` becomes visible there whether or not this window wrote it.

Open questions, to settle before building it:

- Whether the backlog belongs to the session or to the repository. A repository
  backlog survives the window; a session one does not.
- Whether an unapproved brief may sit in the backlog, or only an approved plan.
  The second is safer: the backlog then never holds text nobody approved.
- What happens to a queued plan when main moves underneath it. The binding has
  to say which revision it was approved against, and mark itself stale.

## Assumptions and contingencies

- Linux prepared sandboxes are the first execution target. A requirement that
  needs native or GPU access stops at a visible capability question, with no
  silent local fallback.
- The E2B lifecycle and private-traffic contracts are documented at
  https://docs.e2b.dev/sandbox.md and
  https://docs.e2b.dev/network/restrict-public-access.md. The pinned API version is
  confirmed during implementation. An incompatible contract blocks launch rather
  than weakening isolation.
- Published E2B CPU and memory rates imply about $0.1656 per running hour for
  2 vCPU and 4 GiB, excluding subscription and other charges:
  https://e2b.dev/pricing. That is an estimate, not a billing guarantee. The
  window shows the configured rates and says when they are unavailable.
- No paid sandbox is created while planning. Missing cloud or coding credentials
  disable only the affected execution path.
- A project with no actual architectural decision receives no invented ADR. The
  document view explains why none was needed.
