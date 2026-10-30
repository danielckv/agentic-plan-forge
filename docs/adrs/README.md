# Architecture decision records

Why each boundary sits where it does. An ADR is written when a decision is costly to reverse, when
it reverses an earlier one, or when the obvious choice was rejected for a reason that will not be
obvious later. Each one states the evidence it was decided on, usually a measurement.

These are the project's durable memory. Superseded planning documents have been deleted; the
decisions they contained live here.

| # | Decision | Status |
| :--- | :--- | :--- |
| [0001](0001-remove-live-task-views.md) | Remove the live task views | accepted |
| [0002](0002-read-agents-recall-index.md) | Ground planning in the project's own notes | accepted |
| [0003](0003-no-text-scanning-in-composer.md) | Do not scan the composed requirements text | accepted |
| [0004](0004-section-size-and-host-failure-detail.md) | Size a normalization section by the call timeout, and report what the host said | accepted |
| [0005](0005-prd-impact-map.md) | Map the requirements onto the project, and let the model judge candidates it was shown | accepted |
| [0006](0006-bounded-stage-evidence-and-a-resumable-run.md) | Bound every piece of stage evidence, and keep an unfinished run on disk | accepted |
| [0007](0007-the-architect-answers-in-parts.md) | The architect answers in parts, a timed-out call shrinks, and the session shows what it spent | accepted |
| [0008](0008-the-product-manager-is-judged-only-on-code-shaped-names.md) | The Product Manager is judged only on names that could not be ordinary English | accepted |
| [0009](0009-one-budget-per-stage-roles-across-hosts-and-a-live-room.md) | Give a stage its own budget, spread the roles across the hosts, and say what each avatar is doing | accepted |
| [0010](0010-a-specialist-sees-its-own-workstream.md) | Give a specialist its own workstream's code, and measure a whole run | accepted |
| [0011](0011-the-documentation-batches-leave-the-critical-path.md) | Take the documentation batches off the critical path, and bound them by count | accepted |
| [0012](0012-a-broken-host-costs-one-retry-not-the-run.md) | A broken host costs one retry, not the run | accepted |
| [0013](0013-the-approved-plan-is-built-in-a-worktree.md) | The approved plan is built in a worktree, previewed in a container, then deployed | accepted |
| [0014](0014-the-composer-says-what-the-deliberation-wants.md) | The composer says what the deliberation wants, and the window says what the install can do | accepted |
| [0015](0015-an-avatar-is-placed-not-a-role.md) | Place an avatar, not a role, and let the router own the model | accepted |
| [0016](0016-five-minute-deliberation-budget.md) | Bound ingestion, impact, narrator, workstreams and contract claims for a five-minute budget | accepted |
| [0017](0017-prepared-vm-from-recorded-scripts.md) | Build and preview an approved plan in a prepared VM, from recorded scripts | **proposed** |
| [0018](0018-documents-are-written-and-the-sandbox-gets-the-source.md) | Generated documents are written on request, and the sandbox receives the source | accepted |
| [0019](0019-a-malformed-reply-costs-one-repair-not-the-run.md) | A malformed reply costs one repair, not the run | accepted |
| [0020](0020-a-safeguard-verdict-is-not-a-tool-call.md) | A safeguard verdict is not a tool call | accepted |
| [0021](0021-independent-sections-and-batches-run-together.md) | Independent sections and batches run together | accepted |
| [0022](0022-api-providers-join-the-cli-hosts.md) | API providers join the CLI hosts, authenticated by API key | accepted |
| [0023](0023-a-run-always-produces-a-plan.md) | A run always produces a plan, and says what is still open | accepted |
| [0024](0024-temporary-gap-agents-and-embedded-stage-schemas.md) | Temporary gap agents behind a server-side admission gate, and an embedded schema on every stage | accepted |

[0017](0017-prepared-vm-from-recorded-scripts.md) still reads **proposed** although the prepared-VM
work shipped in PR #24. Its status is left as its author wrote it: moving a decision to accepted is
a decision, not a formatting fix, and belongs to whoever owns that feature.

## Threads worth following

Several decisions only make sense as a sequence.

**What a run costs, and what it is allowed to take.** [0004](0004-section-size-and-host-failure-detail.md)
sizes a section by the call timeout; [0006](0006-bounded-stage-evidence-and-a-resumable-run.md) bounds
every piece of evidence; [0007](0007-the-architect-answers-in-parts.md) splits the architect so a
timeout shrinks one part instead of killing the stage; [0009](0009-one-budget-per-stage-roles-across-hosts-and-a-live-room.md)
gives each stage its own budget; [0011](0011-the-documentation-batches-leave-the-critical-path.md) and
[0021](0021-independent-sections-and-batches-run-together.md) take independent work off the critical
path; [0016](0016-five-minute-deliberation-budget.md) sets the target the rest serve.

**What happens when something fails.** [0012](0012-a-broken-host-costs-one-retry-not-the-run.md) for a
host that breaks after passing every probe, [0019](0019-a-malformed-reply-costs-one-repair-not-the-run.md)
for a reply that will not parse, [0020](0020-a-safeguard-verdict-is-not-a-tool-call.md) for the check
that read a clean bill of health as a confession, and [0023](0023-a-run-always-produces-a-plan.md) for
the run that could finish neither way. Each one turns a dead run into a bounded cost.

**Who may see what.** [0003](0003-no-text-scanning-in-composer.md),
[0008](0008-the-product-manager-is-judged-only-on-code-shaped-names.md) and
[0010](0010-a-specialist-sees-its-own-workstream.md) are the asymmetries between the avatars: the
Product Manager cannot see code, a specialist sees only its own workstream. They are the design, not
an optimisation.

## Writing one

Number it next in sequence and name the file after the decision, not the feature. Keep the four
headings — context, decision, consequences, and the status line — and lead the context with the
evidence: what was measured, what failed, what it cost. A decision that reverses an earlier one says
so in its header and explains what changed, as [0023](0023-a-run-always-produces-a-plan.md) does for
the cycle cap.
