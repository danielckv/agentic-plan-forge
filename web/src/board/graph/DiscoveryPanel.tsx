import { useEffect, useRef } from "react";
import { Check, ChevronsDown, ChevronsUp, FileCode2, Minus, Sparkles, X } from "lucide-react";

import { actorLabel, formatAgo } from "../coordinator";
import type { BoardDiscovery, BoardPriority } from "../types";

interface DiscoveryPanelProps {
  discovery: BoardDiscovery;
  nowMs: number;
  pending: boolean;
  onClose: () => void;
  onAccept: (priority: BoardPriority) => void;
  onDismiss: () => void;
  onOpenParent: (taskID: string) => void;
}

const PRIORITY_CHOICES: Array<{
  value: BoardPriority;
  label: string;
  icon: React.ComponentType<{ size?: number }>;
}> = [
  { value: "high", label: "High", icon: ChevronsUp },
  { value: "normal", label: "Normal", icon: Minus },
  { value: "low", label: "Low", icon: ChevronsDown },
];

export function DiscoveryPanel({
  discovery,
  nowMs,
  pending,
  onClose,
  onAccept,
  onDismiss,
  onOpenParent,
}: DiscoveryPanelProps): React.ReactElement {
  const panel = useRef<HTMLElement>(null);
  const resolvable = discovery.status === "proposed";
  const isTask = discovery.kind === "task";

  useEffect(() => {
    panel.current?.focus();
  }, [discovery.id]);

  return (
    <aside
      ref={panel}
      className="pf-graph-panel"
      role="dialog"
      aria-modal="false"
      aria-labelledby="pf-graph-panel-title"
      tabIndex={-1}
      onKeyDown={(event) => {
        if (event.key === "Escape") {
          event.stopPropagation();
          onClose();
        }
      }}
    >
      <header className="pf-graph-panel-head">
        <span className="pf-graph-panel-icon" aria-hidden="true">
          {isTask ? <Sparkles size={14} /> : <FileCode2 size={14} />}
        </span>
        <div>
          <span className="pf-graph-panel-kicker">
            <span className="pf-mono">{discovery.id}</span>
            <span className={`pf-chip pf-graph-status pf-graph-status--${discovery.status}`}>{discovery.status}</span>
          </span>
          <h3 id="pf-graph-panel-title">{isTask ? discovery.title : "Path outside every planned target"}</h3>
        </div>
        <button type="button" className="pf-btn pf-btn-ghost pf-btn-icon" onClick={onClose} aria-label="Close discovery">
          <X size={14} aria-hidden="true" />
        </button>
      </header>

      <dl className="pf-graph-panel-meta">
        <div>
          <dt>Found by</dt>
          <dd>
            {actorLabel(discovery.by)}
            {discovery.at !== "" ? <span> · {formatAgo(discovery.at, nowMs)}</span> : null}
          </dd>
        </div>
        {discovery.parent_task_id !== "" ? (
          <div>
            <dt>Parent task</dt>
            <dd>
              <button type="button" className="pf-graph-link pf-mono" onClick={() => onOpenParent(discovery.parent_task_id)}>
                {discovery.parent_task_id}
              </button>
            </dd>
          </div>
        ) : null}
      </dl>

      {discovery.reason !== "" ? (
        <section>
          <h4>Why it was found</h4>
          <p className="pf-graph-panel-reason">{discovery.reason}</p>
        </section>
      ) : null}

      {discovery.paths.length > 0 ? (
        <section>
          <h4>{discovery.paths.length === 1 ? "Path" : `Paths (${String(discovery.paths.length)})`}</h4>
          <ul className="pf-graph-panel-paths pf-scroll-thin">
            {discovery.paths.map((path) => (
              <li key={path} className="pf-mono" title={path}>{path}</li>
            ))}
          </ul>
        </section>
      ) : null}

      {resolvable ? (
        <footer className="pf-graph-panel-actions">
          {isTask ? (
            <div className="pf-graph-accept" role="group" aria-label="Accept onto the board with priority">
              <span>Accept as</span>
              {PRIORITY_CHOICES.map((choice) => {
                const Icon = choice.icon;
                return (
                  <button
                    key={choice.value}
                    type="button"
                    className={`pf-btn pf-btn-sm pf-graph-accept-${choice.value}`}
                    disabled={pending}
                    onClick={() => onAccept(choice.value)}
                  >
                    <Icon size={13} aria-hidden="true" /> {choice.label}
                  </button>
                );
              })}
            </div>
          ) : (
            <button type="button" className="pf-btn pf-btn-primary pf-btn-sm" disabled={pending} onClick={() => onAccept("normal")}>
              <Check size={13} aria-hidden="true" /> Accept
            </button>
          )}
          <button type="button" className="pf-btn pf-btn-secondary pf-btn-sm" disabled={pending} onClick={onDismiss}>
            Dismiss
          </button>
        </footer>
      ) : (
        <p className="pf-graph-panel-resolved">
          {discovery.status === "accepted"
            ? isTask
              ? `Accepted onto the board as ${discovery.id}, ${discovery.priority} priority.`
              : "Accepted."
            : "Dismissed. It stays hidden unless “Show dismissed” is on."}
        </p>
      )}
    </aside>
  );
}
