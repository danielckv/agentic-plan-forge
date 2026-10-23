# Using Plan Forge

Type `/plan-forge` in your agent. Before it opens anything, the skill reads what the repository already says about your
request (`plan-forge context`, below) and asks a few questions to agree a short brief. It then opens a tab in your
default browser and waits, so the agent's terminal stays blocked until you approve a plan. Closing the tab without
approving ends the session.

## The three pages

The window has three pages in order. The top bar shows the project, its branch, the phase, who is here, the
connection, a System/Light/Dark switch and the shortcut sheet (`?`). The sidebar collapses with `[`.

### 1. Ingestion: the composer

Write what you want to build in the editor. It is one shared Markdown document; collaborators in a remote session see
each other's edits live.

- **Formatting.** The toolbar sets headings, bold, italic, inline code, lists, checklists, quotes and tables. `⌘B`, `⌘I`
  and `⌘E` work too, and Enter continues a list.
- **Blocks.** Type `/` at the start of a line for a heading, an epic section, a user story, a checklist, a table, a code
  block or a Mermaid diagram.
- **Components.** Type `@` to search the repository's public declarations, such as functions, types and routes. Choosing
  one writes its exact name as inline code. A bare `@` lists the blocks (directories). Technical names go to the
  technical appendix, which the architect reads and the Product Manager never sees.
- **Files.** Attach `.docx`, `.pdf` or `.md` files from the toolbar, or drop them on the editor: up to 5 files, 25 MiB
  each. They are parsed on this machine and added as a `## Source:` section.
- **Write, Split, Preview.** The switch under the editor shows the rendered document beside or instead of the source.

The right rail shows the plan as it forms:

- **Impact.** Which parts of your project this work touches, in plain language, so you do not have to read code to know
  what will change. See below.
- **Epics.** A few seconds after you stop typing, the interpretation groups your requirements into epics. Each shows as a
  file such as `e1-csv-invoice-export.md`, with its requirements, acceptance criteria and what it touches. Click one to
  read it as a document or copy its Markdown.
- **Codebase.** The repository's blocks and their public declarations, with search. Click a name to write it into the
  requirements.
- **Notes.** The project context the technical avatars will read (see below).

The footer shows whether the code is indexed, whether any documentation is stale, what the work touches and whether
related notes were found. **Confirm Context & Launch Avatars** freezes the document and starts the run. Plan Forge does
not scan or block what you write.

#### What this touches

Each epic names the parts of your project it affects, and says what happens to each:

| | |
| :--- | :--- |
| **Changes** | This part exists and the work modifies it. |
| **New** | Nothing in the project does this yet. |
| **Already there** | This part already does the job and is used unchanged. |
| **Clashes** | The requirement contradicts a decision or specification the team already recorded. The contradicted document is named, with what it says. |
| **Possibly involved** | A name match only, shown while the full read is still running. It claims nothing more. |

The panel paints immediately with name matches, then the model reads your repository's code, documentation and decision
records and replaces them with the list above, usually within a minute. Every file it names exists: the model chooses
among declarations and excerpts it was shown, so it cannot invent a path. The map is advisory and is not given to the
avatars, who read the code and the decisions for themselves.

It is recomputed only when something changed — the requirements, the code, or the project's notes — and cached in
`docs/plans/.plan/cache/`, which is not committed. Without a signed-in agent CLI the name matches are all you get, and
the panel says so. See [ADR 0005](adrs/0005-prd-impact-map.md).

### 2. Deliberation: the avatar room

The four avatars sit across the top with what each is doing now. Click one to show only its lines.

- **Discussion.** Every status, draft, critic finding and decision in the order it happened. Decisions the avatars cannot
  make wait at the top as A/B cards; synthesis waits until you choose.
- **Plan taking shape.** The Product Manager's epics as files, the architect's diagram and workstreams, and the critic's
  findings by severity.

**Synthesize Master Spec** unlocks once every avatar has finished the cycle, no CRITICAL or HIGH finding is open and no
decision waits. A run that reaches the cycle limit with blocking findings pauses and offers to retry the architect or
revise the requirements.

### 3. Synthesis: the specification

A Confluence-style specification with diagrams, Jira-style epics and subtasks, documentation-sync tasks and the two
effort schedules. The table of contents follows your scroll. The task matrix can be filtered by ID, title or target
file. Diagrams expand to full screen.

## Project context

Before planning, Plan Forge collects excerpts from the project's own notes, specs, plans and decision records that match
the requirements, plus the branch, `HEAD` and recent commit subjects. The Architect, Specialists and Critic receive them;
the Product Manager does not. Sources, in order:

1. The recall index at `<git-common-dir>/agents-local/recall.db`, rows for this working tree only.
2. The older index at `.agents/index/recall.db`.
3. The files those indexes cover: `.agents/STATE.md`, `.agents/lanes/`, `.agents/sessions/`, `docs/specs/`, `docs/plans/`, `docs/adrs/` and `specs/`.

Everything is read-only, bounded to 12 excerpts and 64 KiB, and repository-relative. Print the same context yourself:

```sh
plan-forge context --query "invoice exports"          # Markdown
plan-forge context --query "invoice exports" --json   # for scripts
```

[ADR 0002](adrs/0002-read-agents-recall-index.md) records the design.

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
