import { Check } from "lucide-react";

import type { Phase } from "../store/syncStore";
import { phaseNote, phaseSteps } from "./phase";

/**
 * Top-bar phase stepper. Steps before the current one are done, the current
 * one carries `aria-current="step"`, the rest are upcoming. It is a status
 * display, not navigation: the router follows the authority phase and the
 * sidebar is the only way back to an earlier page.
 */
export function PhaseStepper({ phase }: { phase: Phase }): React.ReactElement {
  const steps = phaseSteps(phase);
  const note = phaseNote(phase);
  return (
    <div className="pf-phase-wrap">
      <ol className="pf-phase" aria-label="Session phase">
        {steps.map((step, i) => (
          <li
            key={step.key}
            className="pf-phase-step"
            data-state={step.state}
            data-complete={step.complete}
            aria-current={step.state === "current" ? "step" : undefined}
          >
            <span className="pf-phase-index" aria-hidden="true">
              {step.state === "done" || step.complete ? <Check size={11} strokeWidth={3} /> : i + 1}
            </span>
            <span className="pf-phase-label">{step.label}</span>
            {step.state === "done" && <span className="pf-sr-only">(completed)</span>}
          </li>
        ))}
      </ol>
      {note !== null && (
        <span className={`pf-chip pf-chip--${note.tone} pf-chip--live`}>
          <span className="pf-dot" />
          {note.label}
        </span>
      )}
    </div>
  );
}
