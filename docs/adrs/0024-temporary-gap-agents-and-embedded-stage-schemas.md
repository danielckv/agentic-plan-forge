# ADR 0024: Temporary gap agents, and a schema on every stage

- Status: accepted
- Date: 2026-10-30

## Decision

Two decisions, both from the same problem: a plan is only as good as the expertise that reached it, and only as
trustworthy as the shape the model was forced to answer in.

**1. A run may hire bounded temporary agents, proposed by the architect and admitted by the server.**

The Technical Architect answers in parts. Its design reply may carry a `proposed_gaps` list — at most three
proposals. A `GapAgentDescriptor` names `id`, `specialty`, `purpose`, `requirement_ids`, `evidence_refs`,
`deliverable`, `parent_id`, `placement`, and `status`; the server fills `reason` when it rejects one or retires an
agent.

The server is the authority, not the model:

- `AdmitGaps` (`internal/avatars/gapagent.go`) checks every proposal deterministically: the id is present and unique,
  the specialty, purpose and deliverable are non-empty, every `requirement_ids` entry exists in the PM draft's
  stories, a named `parent_id` is a declared workstream, and the normalized specialty is not already owned by a
  workstream role, a workstream id, or an earlier admitted agent. `normalizeScope` folds case, hyphens, underscores,
  spaces and the qualifiers `specialist`, `expertise`, `expert` before comparing, so *"security specialist"* and
  *"Security-Specialist"* collide as they should.
- A reply with more than twelve proposals has the excess retained as `rejected` with a recorded reason. Nothing is
  silently truncated.
- At most `max_gap_agents` are admitted, in proposal order. `max_gap_agents` is 0-3 with a default of 2 and a hard
  ceiling of 3 (`internal/avatars.HardMaxGapAgents`). **Zero disables the feature**; the proposals are still recorded
  as rejected so the workspace can explain why nothing ran.
- Admission happens once per run (`runState.GapsAdmitted`), so a resumed run does not re-admit or re-charge. On
  resume, only `admitted`, `queued`, `running` and `completed` descriptors are eligible to run again.

An admitted agent is a run-scoped specialist, not a new persona:

- It runs as stage `gap:<id>` in role `gap_agent`, on its parent workstream's lane, with the specialist prompt and
  the `gap_agent.v1` schema.
- Its payload carries its own descriptor instead of a workstream, and never the architect's proposals. Its reply may
  not contain `proposed_gaps`: `genesisHasGaps` names that as a validation error, and the schema rejects the key too.
  An agent fills a gap; it never creates one.
- Lifecycle: `proposed → admitted → queued → running → completed → fused → retired`, with `rejected`, `failed` and
  `cancelled` as explicit alternatives. Every transition is written to the checkpoint before the event that follows
  it, so the workspace shows an agent before it works.
- Fusion is real: a completed deliverable joins `SynthesisInput.Specialists` and the descriptor becomes `fused`. A
  deliverable that never validated is `retired` with a reason. Retired agents stay inspectable in the run history and
  never become settings entries.

**2. Every avatar stage document has an embedded JSON schema, and the request carries it.**

`internal/contracts/schema/` now holds `pm.v1`, `architect.v1`, `specialist.v1`, `critic.v1`, `code_reviewer.v1`,
`tech_lead.v1`, `synthesizer.v1` and `gap_agent.v1`. `orchestrator.request` sets both `SchemaID` (so the host can
validate the terminal result and write a schema file for a CLI) and `OutputSchema` (so a provider that supports
structured output constrains the reply at generation time). A provider that cannot constrain output ignores the field;
the avatars layer's strict decode and semantic validation remain the gate either way.

A schema-valid plan can still be inconsistent, so schema validation does not replace `Validate`: requirement
coverage, interface comparison, dependency order and the readiness gates still run.

## Consequences

- A weaker model now fails less often at strict decode, because the provider is constrained to the right shape. That
  addresses the failure mode recorded in `STATE.md` where missing stage schemas made a Flash-class model look like a
  broken synthesizer.
- The schemas are a second description of the Go structs, so they can drift. `internal/contracts/stageschema_test.go`
  validates a representative document for every stage against its own schema, and rejects `proposed_gaps` in a gap
  agent reply; a drift fails that test.
- The schema loader compiles each file in isolation (one `AddResource` per file). A cross-file `$ref` cannot resolve,
  so shared shapes (`critique`, `task`, `estimate`, `claims`, `tradeoffs`) are inlined under `$defs` in each file that
  needs them. Duplication is the price of the isolation that keeps network `$ref` loading impossible by construction.
- An extra bounded agent costs one stage call, at most three per run. It buys expertise the design said it lacked,
  and it cannot multiply: no recursion, no scope duplication, no persisted persona.
- `max_gap_agents: 0` is a supported setting with a visible explanation trail, not a silent no-op.

## Alternatives considered

- **An unrestricted agent factory.** Rejected: it turns a bounded planning run into an unbounded spend, and the plan
  explicitly requires justified temporary agents.
- **A separate discovery model call to find gaps.** Rejected: the architect already holds the design in context, so
  proposing gaps in the same reply costs nothing extra.
- **Prompt-only structure.** This was the state before this change, and it failed at a measured rate on weaker models.
  Prose describes a shape; a schema enforces it.
