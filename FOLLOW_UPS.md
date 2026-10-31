# Follow-ups

This document tracks open follow-up work for Plan Forge maintainers. It covers observed defects and pending improvements, ordered newest first.

## The room needs to show thinking during long stages

Observed 2026-10-31: a stage that takes over eight minutes with a spinner reads as hung. The avatar card displays the stage part, elapsed time, and bytes written, but avatar thinking text is not streamed.

Next: stream thinking text to the avatar card during long stages. This depends on the direct SDK migration, because CLI subprocesses do not expose thinking deltas.

## Complete the direct SDK migration

Observed 2026-10-31 in a real run (`docs/plans/.plan/run.json`): generation ran against a weak model (`together/zai-org/GLM-5.3-Flash`) via an integration CLI host, stalling after one workstream draft.

Plan Forge resolved the immediate routing failure by making oh-my-pi integration-only, naming models per role across three tiers, and enforcing a limit of two avatars per model. What remains is the direct SDK migration described in `docs/plans/SDK_AGENT_FLOW_PLAN.md`: routing generation directly through the Anthropic Go SDK and Google GenAI SDK rather than CLI subprocesses.

Next: display the resolved placement snapshot at run start in the window and the run record before generation starts.

## A failed run should checkpoint where it stopped

`run.json` records stage-level state (PM, Arch, Specs, Critiques, Decisions, `Seq`, `Meta.completed_work`). It does not record where inside a batch a stage stopped, so a resumed run re-does the batch. ADR 0006 covers the resumable run; the batch position is the gap.

Next: carry the batch index and the item index inside it into the checkpoint, flush on failure as well as on success, and mark partial output incomplete.

## The installer never asks for a provider

With no Claude Code and no Antigravity on the machine, `plan-forge install --host auto` exits 3 "no supported agent host found". It should instead ask which provider to use — Anthropic, Google, OpenAI, Fireworks — take the key, and offer per-role model placement, which `role_models` already supports.

Related: there is no way to reach Google without an API key today. `gcloud auth application-default login` and `GOOGLE_APPLICATION_CREDENTIALS` are not read, and Vertex is not a runtime. Anthropic has no equivalent: a Claude subscription is reachable only by running the Claude Code CLI, which is what the `claude` host does.

## The terminal frontend measures width but never height

`Render(v, w)` takes a width only. A frame taller than the terminal scrolls the header off the top. The brief pane is bounded now, but the agents, plan and questions panes are not.

Next: measure rows alongside columns in `internal/tui/term.go`, pass them into `Render`, and bound each pane to the space it has.

## The window tabs and 2D/3D map need a design pass

Owner report, 2026-10-31: the tabs and the plan map do not look good. No specific defect isolated yet.

Next: name the concrete problems first — spacing, hierarchy, contrast, the 3D scene readability at small sizes — then fix them. A screenshot pass with `web/scripts/preview.mjs` across widths is the cheapest way to find them.
