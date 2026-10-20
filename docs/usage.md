# Using Plan Forge

Type `/plan-forge` in your agent. The skill opens a tab in your default browser and waits, so the agent's terminal
stays blocked until you approve a plan. Closing the tab without approving ends the session.

## The three views

The UI has three sequential views, with a phase stepper, a collapsible sidebar (`[`), a System/Light/Dark theme switch
and a shortcut sheet (`?`):

1. **Ingestion.** Add `.docx`, `.pdf` or `.md` files, or paste text. Status pills show the repository scan and the
  documentation health check. The interpretation is editable Markdown, shared live with any collaborators.
2. **Deliberation.** Avatar cards show each role's status, public progress summaries, drafts, diagrams and critiques.
  Cross-avatar conflicts are highlighted, and trade-offs that need judgement become A/B decision cards instead of silent
  choices by a model.
3. **Synthesis.** A Confluence-style specification with diagrams, Jira-style epics and subtasks, documentation-sync tasks
  and the two effort schedules. The table of contents follows your scroll. The task matrix can be filtered by ID, title
  or target file. Diagrams expand to full screen.

## Plan Mode handoff and validation

Approving writes the plan, and the tab shows each handoff step. It then says it is safe to close: the tab is yours, in
your own browser, so Plan Forge never closes it for you.

1. `plan-forge wait` returns the approval with a `validation` result. The validation checks that the plan is approved on
  this machine, that it has not drifted from the repository, and how many AST targets already exist.
2. The agent enters Plan Mode with the specification as the source of truth.
3. The agent reviews the validation, verifies the targets, and presents the execution checklist for your approval.

How each host enters Plan Mode:

- **Claude Code** calls `EnterPlanMode`, enforced by the managed `PostToolUse` hook.
- **OMP** `/plan-forge` opens a new plan-mode session seeded with the spec and the validation result. If the session API
  is unavailable, the hook prefills `/plan …` and you press Enter.
- **Antigravity** asks you to switch modes.

You can re-run the check at any time:

```sh
plan-forge validate            # exit 0 valid, 6 invalid
plan-forge validate --json
```

## Solo and remote

Solo mode keeps everything local. In remote mode, teammates install the client and join a session with an invite, and the
relay mirrors session state so everyone sees the same document, drafts and decisions. Only the host machine runs the
model pipeline, so a run is never billed twice. Approving a plan never stages, commits or pushes anything: your Git
history stays under your control.
