# Command center

Starting `claude` or `omp` opens `http://127.0.0.1:8420` in your browser, once. It is a browser tab, not the dedicated
window the forge and board use, and it outlives any one session.

- **Live** shows every running harness — Claude Code and OMP, across repositories — with its branch, the tool it just
  ran and the files it has touched. **It works with no plan at all.** The board needs an approved manifest and refuses
  to exist without one; this records a session from the moment a harness starts, on any branch, planned or not.
- When a repository does have an approved plan, Live leads with the coordinator's verdict: **Active now** and **Begin
  next**, the same ranking the board window computes, so the task to start is stated rather than inferred. Accepting a
  discovery re-ranks it. Nothing enters the plan automatically, and the approved manifest is never modified.
- **Sprint** renders the approved plan as epic, story and task, with live columns.
- **Setup** explains the local surface. There is nothing to connect.

The daemon hands each tab a single-use launch ticket, so a tab you open by hand parks on a screen telling you to run
`plan-forge center ensure --open` rather than being handed a session. A tab is opened only when none is already
connected, and a cooldown stops several terminals starting at once from opening several tabs. Control it with
`center_port` and `center_auto_open` in the config, or `PLAN_FORGE_CENTER_PORT`.

```sh
plan-forge center status          # is it running, and where
plan-forge center ensure --open   # start it and open one tab
plan-forge center serve           # run it in the foreground
```

**No trackers, no egress.** The command center binds loopback only and makes no outbound request. It connects to no
issue tracker and stores no credentials: the plan, the board and the live sessions are local state. An end-to-end test
asserts the page issues zero external requests.
