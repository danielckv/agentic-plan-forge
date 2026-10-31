import { useCallback, useEffect, useId, useMemo, useState } from "react";
import { AlertTriangle, GripVertical, Inbox, RotateCcw, TriangleAlert, Users } from "lucide-react";

import {
  BACKLOG,
  boardTotals,
  columnsOf,
  effortOf,
  fillPercent,
  formatEffort,
  loadBoard,
  moveItem,
  resetArrangement,
  saveSettings,
} from "../ui/sprints";
import type { Sprint, SprintBasis, SprintBoardView, SprintItem } from "../ui/sprints";

/**
 * The sprint board: the approved plan as columns of work to commit to.
 *
 * The server owns the arrangement. Every move posts and takes the recomputed
 * board back, so the page never holds an opinion about where work sits that
 * the plan has not agreed to.
 *
 * Dragging is the fast path, not the only one: every card carries a "Move to"
 * select, so the board is fully operable from the keyboard.
 */
export function PageSprints(): React.ReactElement {
  const [view, setView] = useState<SprintBoardView | null>(null);
  const [busy, setBusy] = useState(false);
  const [dragging, setDragging] = useState<string | null>(null);
  const [over, setOver] = useState<number | null>(null);

  useEffect(() => {
    let cancelled = false;
    void loadBoard().then((next) => {
      if (!cancelled) setView(next);
    });
    return () => {
      cancelled = true;
    };
  }, []);

  const apply = useCallback(async (run: () => Promise<SprintBoardView>) => {
    setBusy(true);
    const next = await run();
    setView(next);
    setBusy(false);
  }, []);

  const onDrop = useCallback(
    (sprint: number) => {
      const id = dragging;
      setDragging(null);
      setOver(null);
      if (id === null) return;
      void apply(() => moveItem(id, sprint));
    },
    [apply, dragging],
  );

  if (view === null) {
    return (
      <div className="pf-page">
        <p className="pf-caption">Loading the board…</p>
      </div>
    );
  }

  if (!view.available) {
    return (
      <div className="pf-page">
        <div className="pf-empty">
          <span className="pf-empty-icon">
            <Inbox size={20} />
          </span>
          <p className="pf-body font-medium" style={{ color: "var(--pf-ink)" }}>
            No board yet
          </p>
          <p className="pf-caption max-w-md">{view.problem ?? "The plan has not been synthesized."}</p>
        </div>
      </div>
    );
  }

  const board = view.board;
  const totals = boardTotals(board);
  const columns = columnsOf(board);

  return (
    <div className="pf-page pf-sprints">
      <header className="pf-sprints-head">
        <div>
          <h1 className="pf-h1">Sprints</h1>
          <p className="pf-caption">
            {totals.items} {totals.items === 1 ? "task" : "tasks"} · {totals.committed} committed ·{" "}
            {totals.backlog} in the backlog · {board.disclaimer}
          </p>
        </div>
        <BoardControls board={board} busy={busy} onChange={(s) => void apply(() => saveSettings(s))} onReset={() => void apply(resetArrangement)} />
      </header>

      {board.warnings !== undefined && board.warnings.length > 0 && (
        <ul className="pf-sprints-warnings" role="status">
          {board.warnings.map((w) => (
            <li key={w}>
              <TriangleAlert size={13} aria-hidden="true" />
              {w}
            </li>
          ))}
        </ul>
      )}

      <div className="pf-sprints-columns">
        {columns.map((column) => (
          <SprintColumn
            key={column.number}
            column={column}
            basis={board.basis}
            horizon={board.horizon}
            dropping={over === column.number}
            busy={busy}
            onDragEnter={() => setOver(column.number)}
            onDragLeaveColumn={() => setOver((cur) => (cur === column.number ? null : cur))}
            onDrop={() => onDrop(column.number)}
            onDragStartItem={setDragging}
            onMove={(taskID, sprint) => void apply(() => moveItem(taskID, sprint))}
          />
        ))}
      </div>
    </div>
  );
}

function BoardControls(props: {
  board: SprintBoardView["board"];
  busy: boolean;
  onChange: (settings: { basis?: SprintBasis; sprint_days?: number; team_size?: number; horizon?: number }) => void;
  onReset: () => void;
}): React.ReactElement {
  const { board, busy, onChange, onReset } = props;
  const daysId = useId();
  const teamId = useId();
  const horizonId = useId();
  return (
    <div className="pf-sprints-controls">
      <div className="pf-segmented" role="group" aria-label="Estimate basis">
        {(["human", "agent"] as const).map((basis) => (
          <button
            key={basis}
            type="button"
            className="pf-segment"
            aria-pressed={board.basis === basis}
            disabled={busy}
            onClick={() => onChange({ basis })}
          >
            {basis === "human" ? "Human" : "Agent"}
          </button>
        ))}
      </div>

      <label className="pf-sprints-field" htmlFor={daysId}>
        <span>Days</span>
        <input
          id={daysId}
          type="number"
          min={1}
          max={30}
          value={board.sprint_days}
          disabled={busy}
          onChange={(e) => {
            const v = Number(e.target.value);
            if (Number.isInteger(v) && v >= 1 && v <= 30) onChange({ sprint_days: v });
          }}
        />
      </label>

      <label className="pf-sprints-field" htmlFor={teamId}>
        <span>
          <Users size={12} aria-hidden="true" /> Team
        </span>
        <input
          id={teamId}
          type="number"
          min={1}
          max={50}
          value={board.team_size}
          disabled={busy || board.basis === "agent"}
          title={board.basis === "agent" ? "Agent capacity uses max_parallel, not team size" : undefined}
          onChange={(e) => {
            const v = Number(e.target.value);
            if (Number.isInteger(v) && v >= 1 && v <= 50) onChange({ team_size: v });
          }}
        />
      </label>

      <label className="pf-sprints-field" htmlFor={horizonId}>
        <span>Sprints</span>
        <input
          id={horizonId}
          type="number"
          min={1}
          max={12}
          value={board.horizon}
          disabled={busy}
          onChange={(e) => {
            const v = Number(e.target.value);
            if (Number.isInteger(v) && v >= 1 && v <= 12) onChange({ horizon: v });
          }}
        />
      </label>

      <button type="button" className="pf-btn pf-btn-secondary pf-btn-sm" onClick={onReset} disabled={busy}>
        <RotateCcw size={13} />
        Reset arrangement
      </button>
    </div>
  );
}

function SprintColumn(props: {
  column: Sprint;
  basis: SprintBasis;
  horizon: number;
  dropping: boolean;
  busy: boolean;
  onDragEnter: () => void;
  onDragLeaveColumn: () => void;
  onDrop: () => void;
  onDragStartItem: (taskID: string) => void;
  onMove: (taskID: string, sprint: number) => void;
}): React.ReactElement {
  const { column, basis, horizon, dropping, busy, onDragEnter, onDragLeaveColumn, onDrop, onDragStartItem, onMove } = props;
  const isBacklog = column.number === BACKLOG;
  const percent = useMemo(() => fillPercent(column.committed, column.capacity ?? 0), [column.committed, column.capacity]);

  return (
    <section
      className="pf-sprint-col"
      data-dropping={dropping}
      data-backlog={isBacklog}
      data-over={column.over === true}
      aria-label={column.name}
      onDragOver={(e) => {
        e.preventDefault();
        onDragEnter();
      }}
      onDragLeave={onDragLeaveColumn}
      onDrop={(e) => {
        e.preventDefault();
        onDrop();
      }}
    >
      <header className="pf-sprint-col-head">
        <h2 className="pf-sprint-col-title">
          {isBacklog && <Inbox size={14} aria-hidden="true" />}
          {column.name}
        </h2>
        <span className="pf-caption">
          {column.items.length} {column.items.length === 1 ? "task" : "tasks"}
        </span>
      </header>

      {isBacklog ? (
        <p className="pf-sprint-meter-label">{formatEffort(column.committed, basis)} of work waiting</p>
      ) : (
        <>
          <div className="pf-sprint-meter" role="img" aria-label={`${formatEffort(column.committed, basis)} of ${formatEffort(column.capacity ?? 0, basis)} committed`}>
            <span className="pf-sprint-meter-fill" style={{ width: `${String(percent)}%` }} data-over={column.over === true} />
          </div>
          <p className="pf-sprint-meter-label" data-over={column.over === true}>
            {formatEffort(column.committed, basis)} / {formatEffort(column.capacity ?? 0, basis)}
            {column.over === true && (
              <>
                {" "}
                <AlertTriangle size={11} aria-hidden="true" /> over capacity
              </>
            )}
          </p>
        </>
      )}

      <ul className="pf-sprint-items">
        {column.items.map((item) => (
          <SprintCard
            key={item.task_id}
            item={item}
            basis={basis}
            horizon={horizon}
            current={column.number}
            busy={busy}
            onDragStart={() => onDragStartItem(item.task_id)}
            onMove={(sprint) => onMove(item.task_id, sprint)}
          />
        ))}
        {column.items.length === 0 && <li className="pf-sprint-empty">Drop work here</li>}
      </ul>
    </section>
  );
}

function SprintCard(props: {
  item: SprintItem;
  basis: SprintBasis;
  horizon: number;
  current: number;
  busy: boolean;
  onDragStart: () => void;
  onMove: (sprint: number) => void;
}): React.ReactElement {
  const { item, basis, horizon, current, busy, onDragStart, onMove } = props;
  const selectId = useId();
  const blocked = item.blocked_by !== undefined && item.blocked_by.length > 0;
  return (
    <li
      className="pf-sprint-card"
      draggable={!busy}
      data-blocked={blocked}
      data-moved={item.moved === true}
      onDragStart={onDragStart}
    >
      <div className="pf-sprint-card-head">
        <GripVertical size={13} className="pf-sprint-card-grip" aria-hidden="true" />
        <span className="pf-chip pf-chip--task">{item.task_id}</span>
        <span className="pf-sprint-card-effort">{formatEffort(effortOf(item, basis), basis)}</span>
      </div>

      <p className="pf-sprint-card-title">{item.title}</p>

      {(item.epic_title !== undefined && item.epic_title !== "") || (item.story_title !== undefined && item.story_title !== "") ? (
        <p className="pf-sprint-card-context">
          {item.epic_title !== undefined && item.epic_title !== "" && <span>{item.epic_title}</span>}
          {item.story_title !== undefined && item.story_title !== "" && <span>{item.story_title}</span>}
        </p>
      ) : null}

      <div className="pf-sprint-card-foot">
        {item.targets > 0 && (
          <span className="pf-caption">
            {item.targets} {item.targets === 1 ? "target" : "targets"}
          </span>
        )}
        {item.moved === true && <span className="pf-badge pf-badge--moved">moved</span>}
        <label className="pf-sprint-move" htmlFor={selectId}>
          <span className="sr-only">Move {item.task_id} to</span>
          <select
            id={selectId}
            value={current}
            disabled={busy}
            onChange={(e) => onMove(Number(e.target.value))}
          >
            <option value={BACKLOG}>Backlog</option>
            {Array.from({ length: horizon }, (_, i) => i + 1).map((n) => (
              <option key={n} value={n}>
                Sprint {n}
              </option>
            ))}
          </select>
        </label>
      </div>

      {blocked && (
        <p className="pf-sprint-card-blocked">
          <TriangleAlert size={11} aria-hidden="true" />
          waits on {(item.blocked_by ?? []).join(", ")}
        </p>
      )}
    </li>
  );
}
