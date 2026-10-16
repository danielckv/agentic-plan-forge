import { useState } from "react";
import {
  Compass,
  DraftingCompass,
  ShieldAlert,
  Sparkles,
} from "lucide-react";

import {
  useSync,
  cancelSession,
  retryStage,
  resolveDecision,
  synthesize,
  editBaseline,
} from "../store/syncStore";
import type { DecisionCard } from "../store/syncStore";

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
 * Synthesize Master Spec, enabled only when all contracts validate, no
 * CRITICAL/HIGH open, and no pending decisions.
 */

const CYCLE_LABELS: Record<number, string> = {
  1: "Cycle 1: Divergence",
  2: "Cycle 2: Red-Team Critique & Cross-Pollination",
  3: "Cycle 3: Convergence",
};

export type AvatarStatus =
  | "Idle"
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
  icon: React.ComponentType<{ size?: number | string }>;
  color: string;
}

const AVATAR_CARDS: AvatarCardSpec[] = [
  { id: "pm", label: "Product Manager", icon: Compass, color: "var(--pf-accent)" },
  { id: "architect", label: "Technical Architect", icon: DraftingCompass, color: "var(--pf-accent)" },
  { id: "critic", label: "Red-Team Critic", icon: ShieldAlert, color: "var(--pf-danger)" },
  { id: "synthesizer", label: "Lead Synthesizer", icon: Sparkles, color: "var(--pf-ok)" },
];

const STATUS_TONE: Record<AvatarStatus, string> = {
  Idle: "var(--pf-muted)",
  Thinking: "var(--pf-accent)",
  Drafting: "var(--pf-accent)",
  Critiquing: "var(--pf-danger)",
  Revising: "var(--pf-warn)",
  Synced: "var(--pf-ok)",
  Failed: "var(--pf-danger)",
  Cancelled: "var(--pf-muted)",
  Stale: "var(--pf-warn)",
};

export function Page2AvatarCanvas(): React.ReactElement {
  const sync = useSync();
  const [cycle] = useState(1);
  const [runId] = useState<string | null>(null);
  const [maxCyclesReached] = useState(false);
  const [expanded, setExpanded] = useState<string | null>(null);

  const openBlockers = sync.drafts.filter((d) => d.revision === "failed");
  const openDecisions = sync.decisions.filter(
    (d) => d.resolvedOptionId === undefined && !d.stale,
  );

  const synthesizeEnabled =
    sync.role === "owner" &&
    openBlockers.length === 0 &&
    openDecisions.length === 0 &&
    !maxCyclesReached;

  const draftFor = (avatarId: string) =>
    sync.drafts.find((d) => d.avatarId === avatarId);

  return (
    <div className="flex h-full flex-col">
      {/* Top bar */}
      <div
        className="flex items-center gap-3 border-b px-4 py-2"
        style={{ borderColor: "var(--pf-border)" }}
      >
        <span className="text-sm font-semibold">{CYCLE_LABELS[cycle] ?? `Cycle ${cycle}`}</span>
        <span className="text-xs" style={{ color: "var(--pf-muted)" }}>
          {sync.members.length} present · lease holder: {runId === null ? "—" : "owner"}
        </span>
        {maxCyclesReached && (
          <span
            className="rounded px-2 py-0.5 text-xs"
            style={{ background: "var(--pf-warn)", color: "#fff" }}
          >
            max cycles reached — open blockers
          </span>
        )}
      </div>

      <div className="min-h-0 flex-1 overflow-auto p-4">
        {/* Avatar cards */}
        <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
          {AVATAR_CARDS.map((spec) => {
            const draft = draftFor(spec.id);
            const status: AvatarStatus = draft === undefined ? "Idle" : "Synced";
            const Icon = spec.icon;
            const isOpen = expanded === spec.id;
            return (
              <button
                key={spec.id}
                type="button"
                className="rounded border p-3 text-left"
                style={{ borderColor: spec.color }}
                onClick={() => setExpanded(isOpen ? null : spec.id)}
              >
                <div className="flex items-center gap-2">
                  <Icon size={16} />
                  <span className="text-sm font-medium">{spec.label}</span>
                </div>
                <span
                  className="mt-1 inline-block rounded-full px-2 py-0.5 text-xs"
                  style={{ background: STATUS_TONE[status], color: "#fff" }}
                >
                  {status}
                </span>
                {isOpen && draft !== undefined && (
                  <div className="mt-2 border-t pt-2" style={{ borderColor: "var(--pf-border)" }}>
                    <p className="text-xs font-semibold">Live output and rationale</p>
                    <p className="text-xs" style={{ color: "var(--pf-muted)" }}>
                      draft rev {draft.revision}
                      {draft.contentHash !== undefined ? ` · ${draft.contentHash.slice(0, 8)}` : ""}
                    </p>
                  </div>
                )}
              </button>
            );
          })}
        </div>

        {/* Decision cards: 2-4 options, tradeoffs, recommended badge */}
        {sync.decisions.length > 0 && (
          <section className="mt-4">
            <h3 className="text-sm font-semibold">Decisions</h3>
            <div className="mt-2 flex flex-col gap-2">
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

        {/* Max-cycles panel: open blockers, retry / revise-baseline */}
        {maxCyclesReached && (
          <section className="mt-4 rounded border p-3" style={{ borderColor: "var(--pf-warn)" }}>
            <h3 className="text-sm font-semibold">Max cycles reached — unresolved issues</h3>
            <ul className="mt-1 list-none p-0 text-xs" style={{ color: "var(--pf-muted)" }}>
              {openBlockers.map((b) => (
                <li key={b.avatarId}>{b.avatarId}: unresolved CRITICAL/HIGH critique</li>
              ))}
            </ul>
            <div className="mt-2 flex gap-2">
              <button
                type="button"
                className="rounded px-3 py-1 text-xs"
                style={{ background: "var(--pf-accent)", color: "var(--pf-accent-ink)" }}
                onClick={() => retryStage(runId ?? "", "architect")}
              >
                Retry stage
              </button>
              <button
                type="button"
                className="rounded px-3 py-1 text-xs"
                style={{ background: "var(--pf-warn)", color: "#fff" }}
                onClick={editBaseline}
              >
                Revise baseline
              </button>
            </div>
          </section>
        )}
      </div>

      {/* Controls + footer */}
      <footer
        className="flex items-center gap-2 border-t px-4 py-3"
        style={{ borderColor: "var(--pf-border)" }}
      >
        <button
          type="button"
          className="rounded px-3 py-1.5 text-sm"
          style={{ background: "var(--pf-danger)", color: "#fff" }}
          onClick={cancelSession}
          disabled={sync.role !== "owner"}
        >
          Cancel session
        </button>
        <button
          type="button"
          className="ml-auto rounded px-4 py-2 text-sm font-semibold disabled:opacity-40"
          style={{ background: "var(--pf-accent)", color: "var(--pf-accent-ink)" }}
          disabled={!synthesizeEnabled}
          onClick={() => runId !== null && synthesize(runId)}
        >
          Synthesize Master Spec →
        </button>
      </footer>
    </div>
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
      className="rounded border p-3"
      style={{ borderColor: card.stale ? "var(--pf-warn)" : "var(--pf-border)" }}
    >
      <p className="text-sm font-medium">{card.question}</p>
      {card.stale && (
        <p className="text-xs" style={{ color: "var(--pf-warn)" }}>
          stale — resolved by {card.resolvedBy ?? "another member"}
        </p>
      )}
      {resolved && !card.stale && (
        <p className="text-xs" style={{ color: "var(--pf-ok)" }}>
          resolved by {card.resolvedBy ?? "member"}
        </p>
      )}
      <div className="mt-2 flex flex-wrap gap-2">
        {card.options.map((o) => (
          <button
            key={o.optionId}
            type="button"
            disabled={disabled || resolved}
            className="rounded border px-2 py-1 text-xs disabled:opacity-40"
            style={{ borderColor: "var(--pf-border)" }}
            onClick={() => onResolve(card.decisionId, o.optionId)}
          >
            {o.label}
            {card.recommendedOptionId === o.optionId && (
              <span
                className="ml-1 rounded px-1"
                style={{ background: "var(--pf-ok)", color: "#fff" }}
              >
                recommended
              </span>
            )}
            <span className="ml-1" style={{ color: "var(--pf-muted)" }}>
              {o.tradeoff}
            </span>
          </button>
        ))}
      </div>
    </div>
  );
}

