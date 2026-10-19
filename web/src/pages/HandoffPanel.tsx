import { Check, CircleCheck, LoaderCircle } from "lucide-react";

import { handoffSteps, hostName, POST_APPROVAL_CLOSE_SECONDS, useCountdown } from "../ui/handoff";

/**
 * Shown on Page 3 once the plan is approved: the three steps the host takes
 * from here, and the countdown to the binary closing this window
 * (`window.PostApprovalClose`, 5 s).
 *
 * The window cannot see the host, so the steps are an honest outline, not a
 * progress feed: Approved is done, the host entering Plan Mode is the step in
 * flight, and validation follows on its own (`plan-forge validate`).
 */
export function HandoffPanel(props: {
  requestedHost: string | undefined;
  members: string[];
}): React.ReactElement {
  const { requestedHost, members } = props;
  const steps = handoffSteps(requestedHost);
  const remaining = useCountdown(true);
  const host = hostName(requestedHost);

  return (
    <section className="pf-handoff" aria-labelledby="pf-handoff-title">
      <header className="pf-handoff-head">
        <span className="pf-handoff-badge">
          <CircleCheck size={18} />
        </span>
        <div className="min-w-0">
          <h2 id="pf-handoff-title" className="pf-section-title">
            Approved — handing off to {host}
          </h2>
          <p className="pf-caption mt-0.5">
            {members.length > 0
              ? `Synced: ${members.join(", ")}`
              : "Artifacts committed to docs/plans/.plan/"}
          </p>
        </div>
      </header>

      <ol className="pf-handoff-steps">
        {steps.map((step, i) => (
          <li key={step.id} className="pf-handoff-step" data-state={step.state}>
            <span className="pf-handoff-marker" aria-hidden="true">
              {step.state === "done" ? (
                <Check size={13} strokeWidth={3} />
              ) : step.state === "current" ? (
                <LoaderCircle size={13} className="pf-spin" />
              ) : (
                i + 1
              )}
            </span>
            <span className="pf-handoff-label">
              {step.label}
              {step.command !== undefined && (
                <>
                  {" "}
                  <code className="pf-mono">({step.command})</code>
                </>
              )}
            </span>
            <span className="pf-sr-only">
              {step.state === "done" ? "— done" : step.state === "current" ? "— in progress" : "— next"}
            </span>
          </li>
        ))}
      </ol>

      <p className="pf-handoff-close" role="timer">
        {remaining === null || remaining > 0
          ? `This window closes in ${String(remaining ?? POST_APPROVAL_CLOSE_SECONDS)} s`
          : "This window is closing — it is safe to close it now."}
      </p>
    </section>
  );
}
