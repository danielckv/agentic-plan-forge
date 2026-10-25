# ADR 0013: The approved plan is built in a worktree, previewed in a container, then deployed

- Status: accepted
- Date: 2026-10-25

## Context

Plan Forge stopped at the plan. An approval committed `docs/plans/`, the window showed the handoff
panel, and five seconds later the binary closed it and the owner's agent took over in Plan Mode. The
product's own claim — a blocking pre-planning gate — ended exactly where the code began.

The gap that leaves is real. The spec is verified by four avatars and nothing verifies that it can be
built. An owner who wants to see the plan working has to leave the window, start their agent by hand,
watch it edit their working tree, and find out at the end whether the result runs.

Three constraints shaped what was built instead.

**Coding needs tools; every avatar call is tool-free.** The entire `internal/host` package exists to
prove a generation ran with no tools, no MCP and no file access — `rejectAdvertisedTools` fails a call
that even advertises a tools list. Implementing a plan is the opposite: it is file writes. The two
cannot share an invocation path.

**The owner's working tree is not ours to write to.** An agent implementing a 200-task plan across a
repository the owner has uncommitted work in is a bad trade whatever the plan's quality.

**Deploying leaves the machine.** Every other action Plan Forge takes is local and reversible.

## Decision

**A new package, `internal/delivery`, owns everything after the approval.** It is tagged `client`,
like `internal/host`, and the relay never sees it.

**All coding happens in a git worktree under the Plan Forge cache**, on a `plan-forge/<approval-id>`
branch, created from the repository `HEAD`. Never inside the repository: a worktree in the tree would
be picked up by `ast.Scan`, by the drift report and by the owner's own tooling. The agent's working
directory is that worktree and `--add-dir` is never passed, so the only files it can reach are the
copy. A plan that goes wrong costs a directory. The owner merges the branch themselves; Plan Forge
never commits to their branch, never pushes and never touches a remote.

**The coding agent is the host agent, driven non-interactively.** `claude -p --permission-mode
acceptEdits` and `omp -p --approval-mode write`, both read off the installed CLIs rather than
assumed. Neither is the unrestricted bypass mode. AGY is refused rather than guessed at: it has no
non-interactive coding invocation this build has verified. The approved spec is fenced into the
prompt and labelled untrusted, because it was written by models — the same defence every avatar
payload carries.

**The preview is a container, and it is the same image the deploy publishes.** `docker build` from
the worktree's root `Dockerfile`, then `docker run` published on `127.0.0.1` only. One build path, so
a preview cannot disagree with what ships. A worktree with no root `Dockerfile` is reported as
such rather than guessed at from a deeper one.

**The Code and Preview tabs are one page with two tabs**, not two pages. The diff and the running app
answer the same question — did the plan work — and an owner comparing them should not navigate.

**The deploy target is asked once and then a toggle.** `agent-box` (the AgentBox CLI, which owns its
own provider choice: local Docker, SSH, Hetzner, DigitalOcean, Daytona, Vercel), `docker` (the local
daemon), or `none`. It is stored in `config.json` as `deploy_target`, so the question is asked once
per installation. `POST /api/deploy/target` re-reads the config from disk before writing, so a
whole-file save cannot revert a setting another session changed.

**`deploy_target: none` keeps the old behaviour exactly.** The window closes five seconds after an
approval, as it always did. Any other answer — including not having been asked — holds the window
open for the Build page, released by an explicit Finish.

## Consequences

The window now outlives an approval by as long as the owner wants, where before it closed on a timer.
That is a change every existing installation sees, and `none` is the opt-out, offered in the same
dialog as the other two.

`BridgeCSP` gains `frame-src http://127.0.0.1:* http://localhost:*`. It is the first opening in a
policy that was `default-src 'self'` throughout, and it is as narrow as the feature allows: the
preview publishes on loopback only, and the frame is sandboxed without `allow-same-origin`, so the
code inside it runs on an opaque origin and cannot read the window that framed it.

**What is not verified.** Neither `docker` nor `agentbox` is installed on the machine this was
written on, so the build, run, log and deploy paths are covered by their argv construction and their
error handling, not by a live run. `TargetAvailable` reports a missing CLI before the owner commits
to a target rather than after a build they waited for. The worktree, the diff reader, the path guard
and the prompt boundary are tested directly, against a real git repository.

A coding run has no verification gate of its own yet: the agent is told to leave the plan's own
verification commands passing, and nothing checks that it did. The diff is shown to the owner because
the owner is currently the gate. Running the plan's `verification` argv vectors in the worktree and
reporting the exit codes is the obvious next step and is deliberately not in this change.
