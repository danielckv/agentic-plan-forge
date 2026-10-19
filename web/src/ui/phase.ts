/**
 * The four-step phase model behind the top-bar stepper:
 * Ingestion -> Deliberation -> Synthesis -> Approved.
 *
 * The authority has more phases than the user has steps. APPROVING and
 * APPROVING_UNKNOWN are still Synthesis (the approval is in flight), and
 * CANCELLED returns to Ingestion, so the stepper never shows a step the
 * session cannot be in.
 */

import type { Phase } from "../store/syncStore";

export type StepState = "done" | "current" | "upcoming";

export interface PhaseStep {
  key: "INGESTION" | "DELIBERATION" | "SYNTHESIS" | "APPROVED";
  label: string;
  state: StepState;
  /** The session is finished: the last step reads as done, not in progress. */
  complete: boolean;
}

export const PHASE_STEP_DEFS: ReadonlyArray<Pick<PhaseStep, "key" | "label">> = [
  { key: "INGESTION", label: "Ingestion" },
  { key: "DELIBERATION", label: "Deliberation" },
  { key: "SYNTHESIS", label: "Synthesis" },
  { key: "APPROVED", label: "Approved" },
];

const STEP_INDEX: Record<Phase, number> = {
  INGESTION: 0,
  DELIBERATION: 1,
  SYNTHESIS: 2,
  APPROVING: 2,
  APPROVING_UNKNOWN: 2,
  APPROVED: 3,
  CANCELLED: 0,
};

export function phaseSteps(phase: Phase): PhaseStep[] {
  const current = STEP_INDEX[phase];
  return PHASE_STEP_DEFS.map((def, i) => ({
    ...def,
    state: i < current ? "done" : i === current ? "current" : "upcoming",
    complete: phase === "APPROVED" && i === current,
  }));
}

/** Short status beside the stepper for phases the four steps cannot express. */
export function phaseNote(phase: Phase): { label: string; tone: "warn" | "accent" } | null {
  if (phase === "APPROVING" || phase === "APPROVING_UNKNOWN") return { label: "Approving", tone: "accent" };
  if (phase === "CANCELLED") return { label: "Cancelled", tone: "warn" };
  return null;
}
