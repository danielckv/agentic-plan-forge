import type { StageView } from "../store/syncStore";

/**
 * The run's headline, derived from the stages the executor owns.
 *
 * Everything here is a pure function of the stages and the current cycle, so
 * the window cannot invent progress: no timer decides that a stage moved, and a
 * stage that has not started is not shown as working.
 */

export type StageState = "queued" | "running" | "waiting_for_answer" | "retrying" | "failed" | "cancelled" | "completed";

/** What the page says a stage is doing, in the reader's words. */
export const STAGE_LABEL: Record<StageState, string> = {
  queued: "Waiting its turn",
  running: "Working",
  waiting_for_answer: "Waiting for you",
  retrying: "Trying again",
  failed: "Stopped",
  cancelled: "Cancelled",
  completed: "Done",
};

/** A stage that is over, whatever the outcome. */
export function stageTerminal(state: StageState): boolean {
  return state === "failed" || state === "cancelled" || state === "completed";
}

/** A stage the reader must act on before the run can continue. */
export function stageNeedsReader(state: StageState): boolean {
  return state === "waiting_for_answer" || state === "failed";
}

/** The stage in flight: running, retrying, or blocked on a human. */
export function activeStage(stages: StageView[] | undefined): StageView | undefined {
  return (stages ?? []).find((s) => !stageTerminal(s.state));
}

/** Completed work, in the order it finished. */
export function completedStages(stages: StageView[] | undefined): StageView[] {
  return (stages ?? []).filter((s) => s.state === "completed");
}

/** Stages that need the reader, in stage order. */
export function blockedStages(stages: StageView[] | undefined): StageView[] {
  return (stages ?? []).filter((s) => stageNeedsReader(s.state));
}

/** How long a stage has been running, from its own start time. */
export function elapsedMS(stage: StageView | undefined, now: number): number {
  if (stage?.startedAt === undefined) return 0;
  const started = Date.parse(stage.startedAt);
  if (!Number.isFinite(started)) return 0;
  return Math.max(0, now - started);
}

/**
 * The countdown to a stage's own deadline. Null when the stage has none, which
 * is what a finished stage reports.
 */
export function remainingMS(stage: StageView | undefined, now: number): number | null {
  if (stage?.deadlineAt === undefined) return null;
  const deadline = Date.parse(stage.deadlineAt);
  if (!Number.isFinite(deadline)) return null;
  return Math.max(0, deadline - now);
}

/** "2m 05s": a duration a person reads rather than a number of milliseconds. */
export function formatDuration(ms: number): string {
  if (ms <= 0) return "0s";
  const total = Math.round(ms / 1000);
  const minutes = Math.floor(total / 60);
  const seconds = total % 60;
  if (minutes === 0) return `${String(seconds)}s`;
  return `${String(minutes)}m ${String(seconds).padStart(2, "0")}s`;
}

/**
 * The one line at the top of the page: what the run is doing, or what it needs.
 * It leads with the outcome, so a product manager reads a sentence rather than
 * decoding four avatar cards.
 */
export function outcomeHeadline(stages: StageView[] | undefined, recovery: string): string {
  // A failure is terminal, so it is not the "active" stage: it is a stage the
  // reader must act on, and it has to lead the sentence rather than hide behind
  // a generic progress line.
  const blocked = blockedStages(stages)[0];
  if (blocked?.state === "waiting_for_answer") return "The run is waiting for your answer.";
  if (blocked?.state === "failed") return "A stage stopped before the plan was finished.";
  const active = activeStage(stages);
  if (active !== undefined) return `Planning: ${STAGE_LABEL[active.state].toLowerCase()}.`;
  if (recovery !== "") return recovery;
  const done = (stages ?? []).filter((s) => s.state === "completed").length;
  if (done > 0) return "Every stage of this cycle has finished.";
  return "Waiting for the requirements to be read.";
}

/** What the reader can do next, or null when nothing is waiting on them. */
export function nextAction(stages: StageView[] | undefined): { stage: string; action: string } | null {
  const blocked = (stages ?? []).find((s) => stageNeedsReader(s.state));
  if (blocked === undefined) return null;
  return { stage: blocked.id, action: blocked.recovery === undefined || blocked.recovery === "" ? "Review" : blocked.recovery };
}
