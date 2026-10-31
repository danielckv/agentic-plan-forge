# Follow-ups

Open work, newest first. Each entry says what was observed, not what might be nice.

## The plan needs a Jira-shaped view, and the room needs to show thinking

Owner report, 2026-10-31, two parts.

**Epics, tasks and sub-tasks.** The approved plan ends as a specification document and a plan graph. The owner wants
the same content as a worked issue tree — epics containing tasks containing sub-tasks — and better looking than Jira.
The data is already there: `synthesized_spec.md` and `plan_manifest.json` carry epics, tasks, dependencies,
acceptance criteria and verification commands.

**Thinking, or speed.** A stage that takes 8m16s with a spinner reads as hung. Either the run gets materially faster,
or the room shows what the avatar is thinking while it works. The avatar card already carries the stage part, elapsed
time and bytes written; the thinking text is not streamed.

Next: decide which of the two to do first — they answer the same complaint. The plan view is bounded work; streaming
thinking depends on the SDK migration below, because a CLI subprocess does not expose it.

## Generation runs on whatever host answers, and a weak one stalls the run

Observed 2026-10-31 in a real run (`docs/plans/.plan/run.json`, saved 2026-10-30T22:30Z): `Main` was
`together/zai-org/GLM-5.3-Flash`, reached through oh-my-pi's Together provider. The run reached `phase: drafting`
with one workstream spec, 0 critiques and 0 cycles done, then produced nothing. Two later runs the same morning
refreshed the AST and impact caches and wrote no run record at all.

`docs/plans/SDK_AGENT_FLOW_PLAN.md` is the answer already written down: generation moves to the Anthropic Go SDK
(extending `internal/host/anthropicapi.go`) and `google.golang.org/genai`, and the CLI hosts stop being generation
candidates. Until then, a machine with a stray pin can silently plan on a model that cannot hold the schema.

Partly fixed 2026-10-31: oh-my-pi no longer generates at all, Plan Forge names its own model per role, and no model
serves more than two avatars. What remains is the SDK migration itself.

Next: make the placement snapshot at run start visible in the window and in the run record, so a surprising model is
seen before the run spends an hour.

## A failed run should checkpoint where it stopped

`run.json` records stage-level state (PM, Arch, Specs, Critiques, Decisions, `Seq`, `Meta.completed_work`). It does not
record where inside a batch a stage stopped, so a resumed run re-does the batch. ADR 0006 (now decisions 0006) covers
the resumable run; the batch position is the gap.

Next: carry the batch index and the item index inside it into the checkpoint, flush on failure as well as on success,
and mark partial output incomplete.

## The installer never asks for a provider

With no Claude Code and no Antigravity on the machine, `plan-forge install --host auto` exits 3 "no supported agent
host found". It should instead ask which provider to use — Anthropic, Google, OpenAI, Fireworks — take the key, and
offer per-role model placement, which `role_models` already supports.

Related: there is no way to reach Google without an API key today. `gcloud auth application-default login` and
`GOOGLE_APPLICATION_CREDENTIALS` are not read, and Vertex is not a runtime. Anthropic has no equivalent: a Claude
subscription is reachable only by running the Claude Code CLI, which is what the `claude` host does.

## The terminal frontend measures width but never height

`Render(v, w)` takes a width only. A frame taller than the terminal scrolls the header off the top. The brief pane is
bounded now, but the agents, plan and questions panes are not.

Next: measure rows alongside columns in `internal/tui/term.go`, pass them into `Render`, and bound each pane to the
space it has.

## The window's tabs and the 2D/3D map need a design pass

Owner report, 2026-10-31: the tabs and the plan map do not look good. No specific defect isolated yet.

Next: name the concrete problems first — spacing, hierarchy, contrast, the 3D scene's readability at small sizes —
then fix them. A screenshot pass with `web/scripts/preview.mjs` across widths is the cheapest way to find them.
