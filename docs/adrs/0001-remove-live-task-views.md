# ADR 0001: Remove the live task views

- Status: accepted
- Date: 2026-10-22
- Supersedes: the user decisions of 2026-10-19 recorded in the former `docs/plans/COMMAND_CENTER_PLAN.md` (a persistent
  fixed-port dashboard tab) and the plan board design in the former `docs/plans/GUI_BOARD_HANDOFF_PLAN.md`. Both files
  remain in git history.

## Context

Plan Forge is a pre-planning gate: it turns requirements into an approved specification before an agent writes code.
Version 0.3.0 also shipped three features that watch agents after planning:

- **Command center.** A loopback daemon on port 8420 with Live, Sprint and Setup pages. Every Claude Code and OMP
  session start launched it, and every tool call reported to it.
- **Plan board.** A `/plan-forge-board` window with Board and Graph views and a Coordinator. Hooks injected board edits
  into running agent sessions.
- **Swarm reporting.** The OMP hook forwarded oh-my-pi `assistant:status` snapshots to the live store.

These features cost every user something whether or not they planned anything:

- a background process per machine;
- an extra hook process on every tool call and every prompt;
- about 100 source files to maintain.

The maintainer uses a separate tool to watch agents, so the features also duplicate work.

## Decision

Remove all three. Plan Forge ends at the approved plan and the plan-mode handoff.

- Session start runs `hook check-state` only. It never starts a process and never opens a tab.
- The Claude Code hooks are `SessionStart` (plan status), `PreToolUse` (the stale-plan guard) and `PostToolUse` on `Bash`
  (the plan-mode handoff). `UserPromptSubmit` is no longer installed.
- `plan-forge install` removes the 0.3.0 handlers and the `plan-forge-board` skill. It removes the skill only when its
  bytes still match the install manifest.
- `center`, `board` and `swarm` remain for one release as retired commands. Each prints one line on stderr and exits 0,
  so a hook written by 0.3.0 never fails a session. They go in 0.5.0.
- `plan-forge uninstall --purge` still deletes the old `board`, `live` and `center` state directories for one release.

## Consequences

- A session start costs one bounded `check-state` call, down from that call plus a daemon launch.
- Tool calls cost one hook process for `Bash` only, down from one for every edit tool plus prompt submission.
- Agents no longer receive board priorities or Coordinator lines. Teams that relied on them need another tool.
- A daemon started by 0.3.0 keeps running until the user stops it or logs out. The upgrade notes in
  [install.md](../install.md#upgrading-from-030) say so.
