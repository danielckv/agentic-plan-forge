# ADR 0002: Ground planning in the project's own notes

- Status: accepted
- Date: 2026-10-22
- Supersedes: the "no SQLite" decision of 2026-10-19 recorded in the former `docs/plans/COMMAND_CENTER_PLAN.md`. That
  decision kept SQLite out of the command center's live store; this one adds a read-only SQLite reader for planning.

## Context

Before this change, the avatars saw three things about the repository: the requirements text, a Tree-sitter symbol
graph, and a documentation drift report. They never saw what the team had already written down about the work: decision
records, specs, earlier plans, or session notes. A plan could reopen a settled decision or contradict a spec without
knowing either existed.

Many repositories keep that material in Markdown. Some also keep a project-memory index: the `agents` CLI writes a SQLite
FTS5 database of those files and ranks excerpts with `bm25`. It lives at `<git-common-dir>/agents-local/recall.db`. An
older version wrote `.agents/index/recall.db`, with a different schema. Plan Forge must work the same with or without
that tool.

## Decision

Add `internal/gather`. It collects the project context before deliberation, and `plan-forge context` prints the same
result.

- **Sources, first that answers wins.**
  - The current recall index, rows for this working tree only.
  - The legacy recall index.
  - A direct read of the files those indexes cover: `.agents/STATE.md`, `.agents/lanes`, `.agents/sessions`, `docs/specs`,
    `docs/plans`, `docs/adrs` and `specs` (`.md`, `.markdown`, `.txt`; 2 MB per file, depth 4, 400 files).
- **Git state** is added from a closed set of read-only Git commands: branch, `HEAD`, and up to 20 distinct commit
  subjects. `git status` and `git diff` stay forbidden, as in `internal/ast`.
- **Read-only.** The index opens with `mode=ro`, retried as `immutable=1` when its directory is read-only. Plan Forge
  never writes, re-indexes or creates the database. It does not follow symbolic links.
- **Bounded and local.** At most 12 excerpts of 1,200 characters and 64 KiB in total. Absolute repository and home paths
  inside excerpts are rewritten to relative and `~` forms.
- **Who sees it.** The Architect, Specialist and Critic payloads carry it as `project_context`. The Product Manager
  never does: its input stays product-only. The Synthesizer narrates the locked design and does not need it.
- **Identity.** The context hash joins the checkpoint identity, so a resumed run never replays drafts grounded in notes
  that have since changed.
- **Driver.** `modernc.org/sqlite`, a pure-Go driver, so cross-compiled release builds need no extra C toolchain. The
  relay build must not link it; CI and the release workflow assert that.

## Consequences

- The client binary grows by 6.3 MB: 28.0 MB to 34.3 MB (+22.5%), measured on darwin/arm64 with `-trimpath`.
- The `agents` CLI owns the index schema. A schema Plan Forge does not recognise becomes a note, and the file read runs
  instead. A stale index answers with stale excerpts until that tool re-indexes.
- Planning gains one bounded read before deliberation, unmeasured but limited to a 5-second timeout.
- Page 1 shows a context pill and the excerpts, so the user sees what the avatars will read.
