import { describe, expect, it } from "vitest";

import type { StageView } from "../store/syncStore";
import {
  activeStage,
  blockedStages,
  completedStages,
  elapsedMS,
  formatDuration,
  nextAction,
  outcomeHeadline,
  remainingMS,
  stageNeedsReader,
} from "./runStages";

const t0 = "2026-10-27T10:00:00.000Z";

function stage(partial: Partial<StageView>): StageView {
  return { id: "architect", state: "running", startedAt: t0, ...partial };
}

/**
 * The page leads with the outcome. These derivations are the whole of it: no
 * timer, no inference from an avatar card, and no stage shown as working once
 * it has stopped.
 */
describe("outcome-first derivations", () => {
  it("names the stage in flight and ignores finished ones", () => {
    const stages = [stage({ id: "pm", state: "completed" }), stage({ id: "architect", state: "running" })];
    expect(activeStage(stages)?.id).toBe("architect");
    expect(completedStages(stages).map((s) => s.id)).toEqual(["pm"]);
  });

  it("counts a question as the active stage, because the run is blocked on it", () => {
    const stages = [stage({ id: "architect", state: "waiting_for_answer" })];
    expect(activeStage(stages)?.state).toBe("waiting_for_answer");
    expect(blockedStages(stages)).toHaveLength(1);
    expect(stageNeedsReader("waiting_for_answer")).toBe(true);
  });

  it("leads with the question rather than a generic progress line", () => {
    expect(outcomeHeadline([stage({ state: "waiting_for_answer" })], "")).toContain("waiting for your answer");
  });

  it("says plainly that a stage stopped, without dressing it up", () => {
    expect(outcomeHeadline([stage({ state: "failed" })], "")).toContain("stopped");
  });

  it("reports the end of a cycle when every stage completed", () => {
    const stages = [stage({ id: "pm", state: "completed" }), stage({ id: "architect", state: "completed" })];
    expect(outcomeHeadline(stages, "")).toContain("finished");
    expect(activeStage(stages)).toBeUndefined();
    expect(nextAction(stages)).toBeNull();
  });

  it("never claims progress before any stage has started", () => {
    expect(outcomeHeadline(undefined, "")).toContain("Waiting for the requirements");
    expect(activeStage(undefined)).toBeUndefined();
    expect(completedStages(undefined)).toEqual([]);
  });

  it("reads elapsed time from the stage's own start, never a browser start", () => {
    const now = Date.parse(t0) + 125_000;
    expect(elapsedMS(stage({}), now)).toBe(125_000);
    expect(formatDuration(125_000)).toBe("2m 05s");
    expect(elapsedMS(undefined, now)).toBe(0);
    expect(elapsedMS(stage({ startedAt: "not a date" }), now)).toBe(0);
  });

  it("counts down a deadline and reports none for a finished stage", () => {
    const now = Date.parse(t0) + 60_000;
    const running = stage({ deadlineAt: "2026-10-27T10:10:00.000Z" });
    expect(remainingMS(running, now)).toBe(9 * 60_000);
    expect(remainingMS(stage({ state: "completed", deadlineAt: undefined }), now)).toBeNull();
  });

  it("never reports a negative remaining time", () => {
    const now = Date.parse(t0) + 3600_000;
    expect(remainingMS(stage({ deadlineAt: "2026-10-27T10:10:00.000Z" }), now)).toBe(0);
  });

  it("names the action that moves a blocked stage on", () => {
    const blocked = stage({ state: "failed", recovery: "retry" });
    expect(nextAction([blocked])).toEqual({ stage: "architect", action: "retry" });
  });
});
