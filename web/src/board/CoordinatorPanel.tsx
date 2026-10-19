import { ChevronsDown, ChevronsUp, ChevronsRight, ChevronsLeft, Lock, Minus, Network, Radio, Sparkles } from "lucide-react";

import type { CoordinatorView } from "./coordinator";
import type { BoardPriority } from "./types";

interface CoordinatorPanelProps {
  view: CoordinatorView;
  open: boolean;
  onToggle: () => void;
  onOpenTask: (taskID: string) => void;
  onReviewDiscoveries: () => void;
}

const PRIORITY_ICON: Record<BoardPriority, { icon: React.ComponentType<{ size?: number }>; label: string }> = {
  high: { icon: ChevronsUp, label: "High" },
  normal: { icon: Minus, label: "Normal" },
  low: { icon: ChevronsDown, label: "Low" },
};

export function CoordinatorPanel({
  view,
  open,
  onToggle,
  onOpenTask,
  onReviewDiscoveries,
}: CoordinatorPanelProps): React.ReactElement {
  const pendingReview = view.proposed.length;
  if (!open) {
    return (
      <aside className="pf-coord is-collapsed" aria-label="Coordinator">
        <button
          type="button"
          className="pf-coord-rail"
          onClick={onToggle}
          aria-expanded={false}
          aria-label={`Expand coordinator: ${String(view.active.length)} active, ${String(view.nextUp.length)} up next, ${String(pendingReview)} discovered`}
        >
          <ChevronsLeft size={15} aria-hidden="true" />
          <span className="pf-coord-rail-label">Coordinator</span>
          <span className="pf-coord-rail-counts" aria-hidden="true">
            <span className={view.active.length > 0 ? "is-live" : ""} title="Active now">{view.active.length}</span>
            <span title="Up next">{view.nextUp.length}</span>
            <span className={pendingReview > 0 ? "is-review" : ""} title="Discovered">{pendingReview}</span>
          </span>
        </button>
      </aside>
    );
  }

  return (
    <aside className="pf-coord" aria-label="Coordinator">
      <header className="pf-coord-head">
        <div>
          <h2><Radio size={14} aria-hidden="true" /> Coordinator</h2>
          <p>{view.updatedAgo === "" ? "Moves tasks as the agent works" : `Updated ${view.updatedAgo}`}</p>
        </div>
        <button
          type="button"
          className="pf-btn pf-btn-ghost pf-btn-icon"
          onClick={onToggle}
          aria-expanded={true}
          aria-label="Collapse coordinator"
        >
          <ChevronsRight size={15} aria-hidden="true" />
        </button>
      </header>

      <div className="pf-coord-scroll pf-scroll-thin">
        <section aria-labelledby="pf-coord-active">
          <h3 id="pf-coord-active">
            Active now <span className="pf-coord-count">{view.active.length}</span>
          </h3>
          {view.active.length === 0 ? (
            <p className="pf-coord-empty">Nothing active. Tasks appear here when an agent starts editing toward them.</p>
          ) : (
            <ul>
              {view.active.map((entry) => (
                <li key={entry.task.id}>
                  <button type="button" className="pf-coord-item is-active" onClick={() => onOpenTask(entry.task.id)}>
                    <span className="pf-coord-item-top">
                      <span className="pf-mono pf-coord-id">{entry.task.id}</span>
                      <span className="pf-coord-live"><span className="pf-dot" />{entry.actor}</span>
                      {entry.ago !== "" ? <span className="pf-coord-ago">{entry.ago}</span> : null}
                    </span>
                    <span className="pf-coord-title">{entry.task.title}</span>
                    {entry.detail !== "" ? (
                      <span className="pf-coord-detail pf-mono" title={entry.detail}>{entry.detail}</span>
                    ) : null}
                  </button>
                </li>
              ))}
            </ul>
          )}
        </section>

        <section aria-labelledby="pf-coord-next">
          <h3 id="pf-coord-next">
            Up next <span className="pf-coord-count">{view.nextUp.length}</span>
          </h3>
          {view.nextUp.length === 0 ? (
            <p className="pf-coord-empty">The queue is empty: everything is done, active, or blocked.</p>
          ) : (
            <ol>
              {view.nextUp.map((entry) => {
                const priority = PRIORITY_ICON[entry.task.priority];
                const Icon = priority.icon;
                return (
                  <li key={entry.task.id}>
                    <button
                      type="button"
                      className={`pf-coord-item${entry.rank === 1 ? " is-head" : ""}`}
                      onClick={() => onOpenTask(entry.task.id)}
                    >
                      <span className="pf-coord-item-top">
                        <span className="pf-coord-rank" aria-hidden="true">{entry.rank}</span>
                        <span className="pf-mono pf-coord-id">{entry.task.id}</span>
                        <span className={`pf-board-priority pf-board-priority--${entry.task.priority}`} title={`${priority.label} priority`}>
                          <Icon size={14} aria-hidden="true" />
                          <span className="pf-board-sr-only">{priority.label} priority</span>
                        </span>
                      </span>
                      <span className="pf-coord-title">{entry.task.title}</span>
                      <span className={`pf-coord-hint${entry.unblocked ? " is-ok" : " is-blocked"}`}>
                        {entry.unblocked ? null : <Lock size={11} aria-hidden="true" />}
                        {entry.hint}
                      </span>
                    </button>
                  </li>
                );
              })}
            </ol>
          )}
        </section>

        <section aria-labelledby="pf-coord-found">
          <h3 id="pf-coord-found">
            Discovered <span className="pf-coord-count">{pendingReview}</span>
          </h3>
          {pendingReview === 0 ? (
            <p className="pf-coord-empty">No pending discoveries. Work outside the plan shows up here for review.</p>
          ) : (
            <div className="pf-coord-found">
              <p>
                <Sparkles size={13} aria-hidden="true" />
                {view.proposedTasks} task{view.proposedTasks === 1 ? "" : "s"} and {view.proposedPaths} path
                {view.proposedPaths === 1 ? "" : "s"} found outside the approved plan.
              </p>
              <button type="button" className="pf-btn pf-btn-secondary pf-btn-sm" onClick={onReviewDiscoveries}>
                <Network size={13} aria-hidden="true" /> Review in graph
              </button>
            </div>
          )}
        </section>
      </div>
    </aside>
  );
}
