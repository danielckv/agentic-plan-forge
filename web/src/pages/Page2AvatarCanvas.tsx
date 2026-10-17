import { useState } from "react";
import {
  ArrowRight,
  ChevronRight,
  Compass,
  Crown,
  DraftingCompass,
  LoaderCircle,
  ShieldAlert,
  Sparkles,
  TriangleAlert,
  Users,
  WifiOff,
} from "lucide-react";

import {
  useSync,
  cancelSession,
  retryStage,
  resolveDecision,
  synthesize,
  editBaseline,
} from "../store/syncStore";
import type { CritiqueIssue, DecisionCard } from "../store/syncStore";
import { MermaidSvg } from "../components/MermaidSvg";

/**
 * Page 2 — Avatar deliberation canvas.
 *
 * Top bar: cycle label, model badge per avatar, collaborator presence,
 * lease holder. Avatar cards (PM, Architect, specialists, Critic,
 * Synthesizer) with lucide icons and role colors; statuses Idle/Thinking/
 * Drafting/Critiquing/Revising/Synced/Failed/Cancelled/Stale. Expanding a
 * card shows "Live output and rationale" (public summaries + draft deltas —
 * the label never says reasoning). Decision cards with 2-4 options,
 * tradeoffs, recommended badge. Controls: cancel run (owner), retry stage,
 * run superseded notice, executor lost notice, max-cycles panel. Footer:
 * Synthesize Master Spec, enabled only when every avatar has finished the
 * cycle, all contracts validate, no CRITICAL/HIGH is open, and no decision is
 * pending.
 */

const CYCLE_STEPS: Array<{ n: number; label: string }> = [
  { n: 1, label: "Divergence" },
  { n: 2, label: "Red-team critique" },
  { n: 3, label: "Convergence" },
];

export type AvatarStatus =
  | "Idle"
  | "Queued"
  | "Thinking"
  | "Drafting"
  | "Critiquing"
  | "Revising"
  | "Synced"
  | "Failed"
  | "Cancelled"
  | "Stale";

interface AvatarCardSpec {
  id: string;
  label: string;
  icon: React.ComponentType<{ size?: number | string; strokeWidth?: number | string }>;
  color: string;
  soft: string;
}

const AVATAR_CARDS: AvatarCardSpec[] = [
  { id: "pm", label: "Product Manager", icon: Compass, color: "var(--pf-accent)", soft: "var(--pf-accent-soft)" },
  { id: "architect", label: "Technical Architect", icon: DraftingCompass, color: "var(--pf-epic)", soft: "var(--pf-epic-soft)" },
  { id: "critic", label: "Red-Team Critic", icon: ShieldAlert, color: "var(--pf-danger-bright)", soft: "var(--pf-danger-soft)" },
  { id: "synthesizer", label: "Lead Synthesizer", icon: Sparkles, color: "var(--pf-ok-bright)", soft: "var(--pf-ok-soft)" },
];

const STATUS_CHIP: Record<AvatarStatus, string> = {
  Idle: "",
  Queued: "pf-chip--accent pf-chip--live",
  Thinking: "pf-chip--accent pf-chip--live",
  Drafting: "pf-chip--accent pf-chip--live",
  Critiquing: "pf-chip--danger pf-chip--live",
  Revising: "pf-chip--warn pf-chip--live",
  Synced: "pf-chip--ok",
  Failed: "pf-chip--danger",
  Cancelled: "",
  Stale: "pf-chip--warn",
};

const KNOWN_STATUS: Record<string, true> = {
  Idle: true,
  Thinking: true,
  Drafting: true,
  Critiquing: true,
  Revising: true,
  Synced: true,
  Failed: true,
  Cancelled: true,
  Stale: true,
};

/**
 * Statuses that mean the avatar is working right now. The run chip and the
 * card skeletons key off these: an avatar with no output yet still has to
 * look busy, or a cycle that takes minutes reads as a frozen page.
 */
const WORKING_STATUS: Record<string, true> = {
  Queued: true,
  Thinking: true,
  Drafting: true,
  Critiquing: true,
  Revising: true,
};

const MERMAID_HEAD = /^(graph|flowchart|sequenceDiagram|classDiagram|stateDiagram|erDiagram|gantt|mindmap|timeline)\b/;

export function Page2AvatarCanvas(): React.ReactElement {
  const sync = useSync();
  const cycle = sync.cycle;
  const runId = sync.runId;
  const [maxCyclesReached] = useState(false);
  const [expanded, setExpanded] = useState<string | null>(null);

  const openBlockers = sync.drafts.filter((d) => d.revision === "failed");
  const openCritiques = sync.critiques.filter(
    (c) => !c.resolved && (c.severity === "CRITICAL" || c.severity === "HIGH"),
  );
  const openDecisions = sync.decisions.filter(
    (d) => d.resolvedOptionId === undefined && !d.stale,
  );

  // Synthesis reads every avatar's draft, so it cannot start while any of
  // them is still producing one: `working` is computed below, once statusFor
  // exists.

  // A run is in flight: an avatar the authority has not reported on yet is
  // queued behind the ones that are working, not idle.
  const runActive = runId !== null;

  const statusFor = (avatarId: string): AvatarStatus => {
    const t = sync.thoughts[avatarId];
    if (t !== undefined && KNOWN_STATUS[t.status] === true) return t.status as AvatarStatus;
    if (sync.drafts.some((d) => d.avatarId === avatarId)) return "Synced";
    return runActive ? "Queued" : "Idle";
  };

  const working = AVATAR_CARDS.filter((c) => WORKING_STATUS[statusFor(c.id)] === true);

  const synthesizeEnabled =
    sync.role === "owner" &&
    working.length === 0 &&
    openBlockers.length === 0 &&
    openCritiques.length === 0 &&
    openDecisions.length === 0 &&
    !maxCyclesReached;

  // The authority is the source of progress; while the socket is away the
  // page keeps its last state and says so rather than claiming work stalled.
  const linkDown = sync.conn !== "open";

  return (
    <div className="pf-page">
      {/* Top bar: cycle progress, presence, lease */}
      <div
        className="flex items-center gap-5 border-b px-8 py-3"
        style={{ borderColor: "var(--pf-border)", background: "var(--pf-panel)" }}
      >
        <div className="pf-stepper">
          {CYCLE_STEPS.map((s, i) => (
            <span key={s.n} className="flex items-center">
              {i > 0 && <span className="pf-step-connector" />}
              <span
                className="pf-step"
                data-state={s.n < cycle ? "done" : s.n === cycle ? "active" : "todo"}
              >
                <span className="pf-step-index">{s.n}</span>
                {s.label}
              </span>
            </span>
          ))}
        </div>

        <div className="ml-auto flex items-center gap-3">
          <span className="pf-avatar-stack" title={sync.members.join(", ")}>
            {sync.members.slice(0, 5).map((m) => (
              <span key={m} className="pf-avatar">
                {(m.includes(":") ? m.slice(m.indexOf(":") + 1) : m).slice(0, 2).toUpperCase()}
              </span>
            ))}
          </span>
          <span className="pf-chip">
            <Users size={12} />
            {sync.members.length} present
          </span>
          <span className="pf-chip pf-chip--accent">
            <Crown size={12} />
            Lease: {sync.leaseHolder ?? (sync.role === "owner" ? "you" : "owner")}
          </span>
          {linkDown ? (
            <span className="pf-chip pf-chip--warn pf-chip--live" title="Progress resumes when the session authority is back">
              <WifiOff size={12} />
              Reconnecting — last known state
            </span>
          ) : working.length > 0 ? (
            <span className="pf-chip pf-chip--accent pf-chip--live">
              <LoaderCircle size={12} className="pf-spin" />
              Cycle {cycle} running — {working.length} of {AVATAR_CARDS.length} working
            </span>
          ) : runActive ? (
            <span className="pf-chip pf-chip--ok">
              <span className="pf-dot" />
              Cycle {cycle} settled
            </span>
          ) : null}
          {maxCyclesReached && (
            <span className="pf-chip pf-chip--warn">
              <TriangleAlert size={12} />
              Max cycles reached
            </span>
          )}
        </div>
      </div>

      <div className="pf-page-body">
        <div className="pf-container py-7">
          <div className="mb-5">
            <h1 className="pf-title">Avatar deliberation</h1>
            <p className="pf-caption mt-1">
              Cycle {cycle} of 3 — each avatar works from the frozen baseline; public
              output streams below.
            </p>
          </div>

          {/* Avatar cards */}
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-4">
            {AVATAR_CARDS.map((spec) => {
              const draft = sync.drafts.find((d) => d.avatarId === spec.id);
              const thought = sync.thoughts[spec.id];
              const status = statusFor(spec.id);
              const Icon = spec.icon;
              const isOpen = expanded === spec.id;
              const isArchitectDiagram =
                spec.id === "architect" &&
                draft?.content !== undefined &&
                MERMAID_HEAD.test(draft.content.trim());
              return (
                <div
                  key={spec.id}
                  className="pf-card flex flex-col overflow-hidden"
                  style={{ borderTop: `3px solid ${spec.color}` }}
                >
                  <button
                    type="button"
                    className="flex flex-col gap-3 p-4 text-left"
                    onClick={() => setExpanded(isOpen ? null : spec.id)}
                  >
                    <div className="flex items-center gap-2.5">
                      <span
                        className="grid h-8 w-8 place-items-center rounded-lg"
                        style={{ background: spec.soft, color: spec.color }}
                      >
                        <Icon size={16} />
                      </span>
                      <span className="pf-section-title">{spec.label}</span>
                      <ChevronRight
                        size={14}
                        className="pf-accordion-chevron ml-auto"
                        data-open={isOpen}
                      />
                    </div>
                    <div className="flex items-center gap-2">
                      <span className={`pf-chip ${STATUS_CHIP[status]}`}>
                        <span className="pf-dot" />
                        {status}
                      </span>
                      {draft !== undefined && (
                        <span className="pf-caption pf-mono">rev {draft.revision}</span>
                      )}
                    </div>
                    {thought !== undefined && thought.summary !== "" && (
                      <p className="pf-caption" style={{ color: "var(--pf-ink-2)" }}>
                        {thought.summary}
                      </p>
                    )}
                    {thought === undefined && status === "Idle" && (
                      <p className="pf-caption">Waiting for cycle {cycle} to reach this role.</p>
                    )}
                    {thought === undefined && status === "Queued" && (
                      <p className="pf-caption">Queued for cycle {cycle}.</p>
                    )}
                    {WORKING_STATUS[status] === true &&
                      (thought === undefined || thought.summary === "") && (
                        <div className="flex w-full flex-col gap-1.5 pt-0.5" aria-hidden="true">
                          <span className="pf-skeleton" style={{ width: "92%" }} />
                          <span className="pf-skeleton" style={{ width: "74%" }} />
                        </div>
                      )}
                  </button>
                  {isOpen && (
                    <div
                      className="border-t px-4 py-3"
                      style={{ borderColor: "var(--pf-border)", background: "var(--pf-panel-subtle)" }}
                    >
                      <p className="pf-label mb-2">Live output and rationale</p>
                      {draft === undefined ? (
                        WORKING_STATUS[status] === true ? (
                          <div className="flex flex-col gap-2">
                            <p className="pf-caption flex items-center gap-1.5">
                              <LoaderCircle size={12} className="pf-spin" />
                              {status} — output appears here as it lands.
                            </p>
                            <span className="pf-skeleton" style={{ width: "88%" }} />
                            <span className="pf-skeleton" style={{ width: "96%" }} />
                            <span className="pf-skeleton" style={{ width: "61%" }} />
                          </div>
                        ) : (
                          <p className="pf-caption">No draft yet — output appears here as the avatar works.</p>
                        )
                      ) : isArchitectDiagram ? (
                        <MermaidSvg source={draft.content!} title="Proposed architecture" />
                      ) : (
                        <div className="flex flex-col gap-1">
                          <p className="pf-caption pf-mono">
                            draft rev {draft.revision}
                            {draft.contentHash !== undefined ? ` · ${draft.contentHash.slice(0, 8)}` : ""}
                          </p>
                          {draft.content !== undefined && (
                            <pre
                              className="pf-mono mt-1 max-h-48 overflow-auto rounded-md border p-2.5 whitespace-pre-wrap"
                              style={{
                                borderColor: "var(--pf-border)",
                                background: "var(--pf-panel)",
                                fontSize: 11.5,
                                color: "var(--pf-ink-2)",
                              }}
                            >
                              {draft.content.slice(0, 2000)}
                            </pre>
                          )}
                        </div>
                      )}
                    </div>
                  )}
                </div>
              );
            })}
          </div>

          {/* Critic issues */}
          {sync.critiques.length > 0 && (
            <section className="mt-7">
              <div className="mb-3 flex items-center gap-2">
                <ShieldAlert size={15} style={{ color: "var(--pf-danger)" }} />
                <h2 className="pf-section-title">Critic findings</h2>
                <span className="pf-caption">
                  {openCritiques.length} blocking · {sync.critiques.length} total
                </span>
              </div>
              <div className="pf-card overflow-hidden">
                <ul className="m-0 flex list-none flex-col p-0">
                  {sync.critiques.map((c, i) => (
                    <CritiqueRow key={c.issueId} issue={c} last={i === sync.critiques.length - 1} />
                  ))}
                </ul>
              </div>
            </section>
          )}

          {/* Decision cards: 2-4 options, tradeoffs, recommended badge */}
          {sync.decisions.length > 0 && (
            <section className="mt-7">
              <div className="mb-3 flex items-center gap-2">
                <h2 className="pf-section-title">Decisions</h2>
                {openDecisions.length > 0 && (
                  <span className="pf-chip pf-chip--warn">
                    <span className="pf-dot" />
                    {openDecisions.length} awaiting input
                  </span>
                )}
              </div>
              <div className="flex flex-col gap-3">
                {sync.decisions.map((d) => (
                  <DecisionCardView
                    key={d.decisionId}
                    card={d}
                    disabled={sync.role === "viewer"}
                    onResolve={resolveDecision}
                  />
                ))}
              </div>
            </section>
          )}

          {sync.decisions.length === 0 && sync.critiques.length === 0 && sync.drafts.length === 0 && (
            <div className="pf-empty mt-7">
              <span className="pf-empty-icon">
                <Compass size={20} />
              </span>
              <p className="pf-body font-medium" style={{ color: "var(--pf-ink)" }}>
                The deliberation has not produced output yet
              </p>
              <p className="pf-caption max-w-md">
                Drafts, critic findings, and decisions stream in here as the avatars
                work through the current cycle.
              </p>
            </div>
          )}

          {/* Max-cycles panel: open blockers, retry / revise-baseline */}
          {maxCyclesReached && (
            <section className="pf-notice pf-notice--warn mt-7 flex-col items-stretch">
              <div className="flex items-center gap-2">
                <TriangleAlert size={15} style={{ color: "var(--pf-warn)" }} />
                <span className="font-semibold" style={{ color: "var(--pf-warn)" }}>
                  Max cycles reached — unresolved issues
                </span>
              </div>
              <ul className="mt-1 list-none p-0 text-[13px]" style={{ color: "var(--pf-ink-2)" }}>
                {openBlockers.map((b) => (
                  <li key={b.avatarId}>{b.avatarId}: unresolved CRITICAL/HIGH critique</li>
                ))}
              </ul>
              <div className="mt-2 flex gap-2">
                <button
                  type="button"
                  className="pf-btn pf-btn-secondary pf-btn-sm"
                  onClick={() => retryStage(runId ?? "", "architect")}
                >
                  Retry stage
                </button>
                <button
                  type="button"
                  className="pf-btn pf-btn-secondary pf-btn-sm"
                  onClick={editBaseline}
                >
                  Revise baseline
                </button>
              </div>
            </section>
          )}
        </div>
      </div>

      {/* Controls + footer */}
      <footer className="pf-footer-bar">
        <button
          type="button"
          className="pf-btn pf-btn-danger"
          onClick={cancelSession}
          disabled={sync.role !== "owner"}
        >
          Cancel session
        </button>
        <p className="pf-caption ml-3 flex items-center gap-1.5">
          {working.length > 0 ? (
            <>
              <LoaderCircle size={12} className="pf-spin" />
              {working.length === 1
                ? `${working[0]!.label} is still working — synthesis waits for the cycle to finish.`
                : `${String(working.length)} avatars are still working — synthesis waits for the cycle to finish.`}
            </>
          ) : openDecisions.length > 0 ? (
            "Resolve open decisions to enable synthesis."
          ) : openCritiques.length > 0 ? (
            "Blocking critique findings must resolve before synthesis."
          ) : (
            "All contracts validated — ready to synthesize."
          )}
        </p>
        <button
          type="button"
          className="pf-btn pf-btn-primary pf-btn-lg ml-auto"
          disabled={!synthesizeEnabled}
          title={
            working.length > 0
              ? "Available when every avatar finishes this cycle"
              : "Synthesize the master specification"
          }
          onClick={() => runId !== null && synthesize(runId)}
        >
          Synthesize Master Spec
          <ArrowRight size={15} />
        </button>
      </footer>
    </div>
  );
}

function CritiqueRow(props: { issue: CritiqueIssue; last: boolean }): React.ReactElement {
  const { issue, last } = props;
  const badge =
    issue.severity === "CRITICAL"
      ? "pf-badge--critical"
      : issue.severity === "HIGH"
        ? "pf-badge--high"
        : "pf-badge--medium";
  return (
    <li
      className="flex items-center gap-3 px-4 py-3"
      style={last ? undefined : { borderBottom: "1px solid var(--pf-border)" }}
    >
      <span className={`pf-badge ${badge}`}>{issue.severity}</span>
      <span className="pf-body flex-1" style={{ color: "var(--pf-ink)" }}>
        {issue.summary}
      </span>
      {issue.resolved ? (
        <span className="pf-chip pf-chip--ok">
          <span className="pf-dot" />
          Resolved
        </span>
      ) : (
        <span className="pf-chip">
          <span className="pf-dot" />
          Open
        </span>
      )}
    </li>
  );
}

function DecisionCardView(props: {
  card: DecisionCard;
  disabled: boolean;
  onResolve: (decisionId: string, optionId: string) => void;
}): React.ReactElement {
  const { card, disabled, onResolve } = props;
  const resolved = card.resolvedOptionId !== undefined;
  return (
    <div
      className="pf-card pf-card-pad"
      style={
        card.stale
          ? { borderColor: "var(--pf-warn-border)", background: "var(--pf-warn-soft)" }
          : { borderColor: "var(--pf-accent-border)" }
      }
    >
      <div className="flex items-start justify-between gap-3">
        <p className="pf-section-title">{card.question}</p>
        {card.stale && (
          <span className="pf-chip pf-chip--warn">
            <span className="pf-dot" />
            Stale — resolved by {card.resolvedBy ?? "another member"}
          </span>
        )}
        {resolved && !card.stale && (
          <span className="pf-chip pf-chip--ok">
            <span className="pf-dot" />
            Resolved by {card.resolvedBy ?? "member"}
          </span>
        )}
        {!resolved && !card.stale && (
          <span className="pf-chip pf-chip--accent pf-chip--live">
            <span className="pf-dot" />
            Awaiting decision
          </span>
        )}
      </div>
      <div className="mt-3 grid grid-cols-1 gap-2 md:grid-cols-2">
        {card.options.map((o) => {
          const recommended = card.recommendedOptionId === o.optionId;
          const chosen = card.resolvedOptionId === o.optionId;
          return (
            <button
              key={o.optionId}
              type="button"
              disabled={disabled || resolved}
              className="flex flex-col gap-1 rounded-lg border p-3 text-left transition-colors disabled:cursor-not-allowed"
              style={{
                borderColor: chosen
                  ? "var(--pf-ok-border)"
                  : recommended
                    ? "var(--pf-accent-border)"
                    : "var(--pf-border)",
                background: chosen
                  ? "var(--pf-ok-soft)"
                  : recommended
                    ? "var(--pf-accent-soft)"
                    : "var(--pf-panel)",
                opacity: resolved && !chosen ? 0.55 : 1,
              }}
              onClick={() => onResolve(card.decisionId, o.optionId)}
            >
              <span className="flex items-center gap-2 text-[13px] font-semibold" style={{ color: "var(--pf-ink)" }}>
                {o.label}
                {recommended && <span className="pf-badge pf-badge--recommended">Recommended</span>}
              </span>
              <span className="pf-caption">{o.tradeoff}</span>
            </button>
          );
        })}
      </div>
    </div>
  );
}
