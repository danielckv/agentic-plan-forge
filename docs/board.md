# Plan board

Type `/plan-forge-board` in Claude Code or OMP after a plan is approved. A tab opens in your default browser on a live
board.

- Tasks are laid out as To do, In progress, Done and Drifted, from evidence in the code. Cards move as the agent works.
- Each task can be set to High, Normal or Low priority, and can carry context notes for the agent.
- Edits reach the running agent through its own APIs. OMP receives them pushed into the session. Claude Code receives
  them as hook context at its next step.
- The approved manifest is never modified. Edits live in a machine-local overlay in the Plan-Forge cache.
- The board header has a `Board | Graph` switch. The Graph view draws the plan as task nodes with dependency edges,
  colored by live state; tasks the plan did not contain use a dashed style.
- **Live agents** lists every harness session on this machine, this repository's first, with the agents each one has
  dispatched: the persona, the agent type and what it is working on. oh-my-pi publishes this on its `assistant:status`
  channel and Plan Forge forwards it verbatim. A session that went quiet is dimmed rather than dropped. A Claude Code
  session says it could not be reached — it reports tool activity but publishes no swarm channel, so an empty swarm
  there would read as idle when it means unknown.
- A coordinator moves cards in real time from what the agent actually does — file edits and `board report` calls —
  not only from the periodic AST scan. Verification receipts count as plan evidence through that periodic evidence
  scan; nothing records them on the board directly. A coordinator panel on the right lists Active now and Up next.
- The Graph also shows discovered items: tasks and repository paths the agent touched that no planned target covers.
  Each stays proposed until you accept or dismiss it; nothing enters the plan automatically.
- Host capture is automatic: the Claude `PostToolUse` hook covers `Bash|Edit|Write|MultiEdit|NotebookEdit`, and the
  OMP hook covers its edit/write tools.

```sh
plan-forge board pull --repo . --consumer claude:<id> --format text
```

prints the edits a consumer has not seen yet.
