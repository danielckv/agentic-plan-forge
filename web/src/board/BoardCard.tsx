import {
  CheckCircle2,
  ChevronsDown,
  ChevronsUp,
  CircleDot,
  FileCheck2,
  Link2,
  MessageSquareText,
  Minus,
} from "lucide-react";

import type { BoardPriority, BoardTask } from "./types";

interface BoardCardProps {
  task: BoardTask;
  onOpen: (taskID: string, trigger: HTMLButtonElement) => void;
  register: (taskID: string, element: HTMLButtonElement | null) => void;
}

const PRIORITY_META: Record<
  BoardPriority,
  { label: string; className: string; icon: React.ComponentType<{ size?: number }> }
> = {
  high: { label: "High priority", className: "pf-board-priority--high", icon: ChevronsUp },
  normal: { label: "Normal priority", className: "pf-board-priority--normal", icon: Minus },
  low: { label: "Low priority", className: "pf-board-priority--low", icon: ChevronsDown },
};

export function BoardCard({ task, onOpen, register }: BoardCardProps): React.ReactElement {
  const priority = PRIORITY_META[task.priority];
  const PriorityIcon = priority.icon;
  const verifiedTargets = task.targets.filter((target) => target.verified).length;

  return (
    <button
      ref={(element) => register(task.id, element)}
      type="button"
      className="pf-card pf-board-card"
      data-task-id={task.id}
      data-column={task.column}
      onClick={(event) => onOpen(task.id, event.currentTarget)}
      aria-label={`Open ${task.id}: ${task.title}`}
    >
      <span className="pf-board-card-topline">
        <span className="pf-board-card-id pf-mono">{task.id}</span>
        <span className={`pf-board-priority ${priority.className}`} title={priority.label}>
          <PriorityIcon size={15} aria-hidden="true" />
          <span className="pf-board-sr-only">{priority.label}</span>
        </span>
      </span>

      <span className="pf-board-card-title">{task.title}</span>

      <span className="pf-board-card-labels">
        <span className="pf-chip pf-chip--epic">{task.epic_id}</span>
        {task.blocked ? <span className="pf-badge pf-badge--high">Blocked</span> : null}
      </span>

      <span className="pf-board-card-metrics">
        <span title={`${String(verifiedTargets)} of ${String(task.targets.length)} targets verified`}>
          <FileCheck2 size={13} aria-hidden="true" />
          {verifiedTargets}/{task.targets.length}
        </span>
        <span title={`${String(task.verification.passed)} of ${String(task.verification.total)} checks passed`}>
          <CheckCircle2 size={13} aria-hidden="true" />
          {task.verification.passed}/{task.verification.total}
        </span>
        {task.depends_on.length > 0 ? (
          <span title={`${String(task.depends_on.length)} dependencies`}>
            <Link2 size={13} aria-hidden="true" />
            {task.depends_on.length}
          </span>
        ) : (
          <span title="No dependencies">
            <CircleDot size={13} aria-hidden="true" />0
          </span>
        )}
        <span title={`${String(task.notes.length)} context notes`}>
          <MessageSquareText size={13} aria-hidden="true" />
          {task.notes.length}
        </span>
      </span>
    </button>
  );
}
