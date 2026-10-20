import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import {
  AlertTriangle,
  CheckCircle2,
  CircleDot,
  FolderGit2,
  LayoutDashboard,
  Network,
  RotateCw,
  Search,
  Wifi,
  WifiOff,
  X,
} from "lucide-react";

import { BrandMark } from "../ui/BrandMark";

import { BoardCard } from "./BoardCard";
import { BoardDrawer } from "./BoardDrawer";
import { CoordinatorPanel } from "./CoordinatorPanel";
import { SwarmPanel } from "./SwarmPanel";
import { AUTH_FAILED_MESSAGE, boardStore, useBoardState } from "./boardStore";
import type { BoardConnection } from "./boardStore";
import {
  boardHostLabel,
  deriveDeliveryChip,
  EMPTY_FILTERS,
  filterBoardTasks,
  sortBoardTasks,
} from "./boardUtils";
import type { BoardFilters } from "./boardUtils";
import { deriveCoordinatorView, nextReviewRequest } from "./coordinator";
import type { ReviewRequest } from "./coordinator";
import { GraphView } from "./graph/GraphView";
import {
  readCoordinatorOpen,
  readViewMode,
  writeCoordinatorOpen,
  writeViewMode,
} from "./viewMode";
import type { BoardViewMode } from "./viewMode";
import type { BoardColumn, BoardPriority, BoardTask } from "./types";

const COLUMNS: Array<{ id: BoardColumn; label: string; description: string }> = [
  { id: "todo", label: "To do", description: "Not started" },
  { id: "in_progress", label: "In progress", description: "Evidence detected" },
  { id: "done", label: "Done", description: "Fully verified" },
  { id: "drifted", label: "Drifted", description: "Needs attention" },
];

const CONNECTION_META: Record<BoardConnection, { label: string; tone: string }> = {
  connecting: { label: "Connecting", tone: "pf-chip--warn pf-chip--live" },
  open: { label: "Live", tone: "pf-chip--ok" },
  reconnecting: { label: "Reconnecting", tone: "pf-chip--warn pf-chip--live" },
  offline: { label: "Offline", tone: "pf-chip--danger" },
};

function uniqueValues(tasks: readonly BoardTask[], field: "epic_id" | "workstream_id"): string[] {
  return Array.from(new Set(tasks.map((task) => task[field])));
}

function statusTone(status: string): string {
  if (status === "DRIFTED" || status === "UNKNOWN") return "pf-chip--danger";
  if (status === "DONE" || status === "COMPLETE" || status === "COMPLETED") {
    return "pf-chip--ok";
  }
  if (status === "IN_PROGRESS") return "pf-chip--accent";
  return "";
}

function readableStatus(status: string): string {
  return status.toLocaleLowerCase().replaceAll("_", " ");
}

function prefersReducedMotion(): boolean {
  return (
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches
  );
}

export function BoardApp(): React.ReactElement {
  const board = useBoardState();
  const [filters, setFilters] = useState<BoardFilters>({ ...EMPTY_FILTERS });
  const [selectedTaskID, setSelectedTaskID] = useState<string | null>(null);
  const [viewMode, setViewMode] = useState<BoardViewMode>(readViewMode);
  const [reviewRequest, setReviewRequest] = useState<ReviewRequest | null>(null);
  const reviewSeq = useRef(0);
  const [coordinatorOpen, setCoordinatorOpen] = useState<boolean>(readCoordinatorOpen);
  // Relative times ("12s ago") need a clock; one coarse tick keeps them honest.
  const [nowMs, setNowMs] = useState<number>(() => Date.now());
  const nodeElements = useRef(new Map<string, SVGGElement>());
  const searchInput = useRef<HTMLInputElement>(null);
  const cardElements = useRef(new Map<string, HTMLButtonElement>());
  const previousRects = useRef(new Map<string, DOMRect>());
  const previousColumns = useRef(new Map<string, BoardColumn>());
  const returnFocusTask = useRef<string | null>(null);
  const pendingFocusTask = useRef<string | null>(null);

  useEffect(() => {
    boardStore.start();
    return () => boardStore.stop();
  }, []);

  useEffect(() => {
    if (board.toast === null) return;
    const timer = window.setTimeout(boardStore.clearToast, 5000);
    return () => window.clearTimeout(timer);
  }, [board.toast]);

  useEffect(() => {
    const timer = window.setInterval(() => setNowMs(Date.now()), 10_000);
    return () => window.clearInterval(timer);
  }, []);

  const changeView = useCallback((mode: BoardViewMode) => {
    setViewMode(mode);
    setReviewRequest(null);
    writeViewMode(mode);
  }, []);

  const toggleCoordinator = useCallback(() => {
    setCoordinatorOpen((open) => {
      writeCoordinatorOpen(!open);
      return !open;
    });
  }, []);

  const registerNode = useCallback((taskID: string, element: SVGGElement | null) => {
    if (element === null) nodeElements.current.delete(taskID);
    else nodeElements.current.set(taskID, element);
  }, []);

  const registerCard = useCallback((taskID: string, element: HTMLButtonElement | null) => {
    if (element === null) cardElements.current.delete(taskID);
    else cardElements.current.set(taskID, element);
  }, []);

  const closeDrawer = useCallback(() => {
    const taskID = returnFocusTask.current;
    setSelectedTaskID(null);
    // Resolve the card at focus time: a column move remounts it, so an element
    // captured when the drawer opened may be detached by now.
    window.requestAnimationFrame(() => {
      if (taskID !== null) (cardElements.current.get(taskID) ?? nodeElements.current.get(taskID))?.focus();
    });
  }, []);

  const openTask = useCallback((taskID: string) => {
    returnFocusTask.current = taskID;
    setSelectedTaskID(taskID);
  }, []);

  const snapshotTasks = board.snapshot?.tasks;
  useLayoutEffect(() => {
    const currentRects = new Map<string, DOMRect>();
    const currentColumns = new Map<string, BoardColumn>();
    for (const task of snapshotTasks ?? []) {
      const element = cardElements.current.get(task.id);
      if (element === undefined) continue;
      const rect = element.getBoundingClientRect();
      currentRects.set(task.id, rect);
      currentColumns.set(task.id, task.column);
      const oldRect = previousRects.current.get(task.id);
      const oldColumn = previousColumns.current.get(task.id);
      const reduceMotion = prefersReducedMotion();
      if (
        !reduceMotion &&
        oldRect !== undefined &&
        oldColumn !== undefined &&
        oldColumn !== task.column &&
        typeof element.animate === "function"
      ) {
        element.animate(
          [
            { transform: `translate(${String(oldRect.left - rect.left)}px, ${String(oldRect.top - rect.top)}px)` },
            { transform: "translate(0, 0)" },
          ],
          { duration: 280, easing: "cubic-bezier(.2,.8,.2,1)" },
        );
      }
    }
    previousRects.current = currentRects;
    previousColumns.current = currentColumns;
  }, [snapshotTasks]);

  const filteredTasks = useMemo(
    () => filterBoardTasks(board.snapshot?.tasks ?? [], filters),
    [board.snapshot?.tasks, filters],
  );

  useEffect(() => {
    const taskID = pendingFocusTask.current;
    if (taskID === null || selectedTaskID !== null) return;
    const element = cardElements.current.get(taskID);
    if (element === undefined) return;
    pendingFocusTask.current = null;
    element.scrollIntoView({ behavior: prefersReducedMotion() ? "auto" : "smooth", block: "center" });
    element.focus();
    returnFocusTask.current = taskID;
  }, [filteredTasks, selectedTaskID]);

  // A task that leaves the snapshot (plan changed under the drawer) must not
  // stay selected: it would swallow the board shortcuts with no drawer shown.
  if (
    selectedTaskID !== null &&
    board.snapshot !== null &&
    !board.snapshot.tasks.some((task) => task.id === selectedTaskID)
  ) {
    setSelectedTaskID(null);
  }

  const selectedTask =
    selectedTaskID === null
      ? undefined
      : board.snapshot?.tasks.find((task) => task.id === selectedTaskID);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent): void => {
      const target = event.target;
      const editing =
        target instanceof HTMLInputElement ||
        target instanceof HTMLTextAreaElement ||
        target instanceof HTMLSelectElement ||
        (target instanceof HTMLElement && target.isContentEditable);
      if (event.key === "Escape" && selectedTaskID !== null) {
        event.preventDefault();
        closeDrawer();
        return;
      }
      if (event.key === "/" && !editing) {
        // The board behind an open drawer is inert: nothing to search.
        if (selectedTaskID !== null) return;
        if (viewMode !== "board") return;
        event.preventDefault();
        searchInput.current?.focus();
        return;
      }
      if (
        event.key.toLocaleLowerCase() === "g" &&
        !editing &&
        selectedTaskID === null &&
        !event.repeat &&
        !event.metaKey &&
        !event.ctrlKey &&
        !event.altKey
      ) {
        event.preventDefault();
        changeView(viewMode === "board" ? "graph" : "board");
        return;
      }
      if (selectedTaskID === null || editing || event.repeat) return;
      if (event.metaKey || event.ctrlKey || event.altKey) return;
      const priorityByKey: Record<string, BoardPriority | undefined> = {
        h: "high",
        n: "normal",
        l: "low",
      };
      const priority = priorityByKey[event.key.toLocaleLowerCase()];
      if (priority !== undefined) {
        event.preventDefault();
        void boardStore.mutate(selectedTaskID, { priority });
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [changeView, closeDrawer, selectedTaskID, viewMode]);

  const connection = CONNECTION_META[board.connection];

  if (board.snapshot === null) {
    return (
      <main className="pf-page pf-board pf-board-state-page">
        <div className="pf-empty pf-board-state">
          <div className="pf-empty-icon">
            {board.loading ? (
              <RotateCw className="pf-spin" size={22} aria-hidden="true" />
            ) : (
              <WifiOff size={22} aria-hidden="true" />
            )}
          </div>
          <h1 className="pf-title">{board.loading ? "Loading the live board" : "Board offline"}</h1>
          <p className="pf-body">
            {board.loading
              ? "Connecting to the approved plan and evaluating its evidence."
              : (board.lastError ?? "The local Plan-Forge bridge is unavailable.")}
          </p>
          {!board.loading ? (
            <button
              type="button"
              className="pf-btn pf-btn-secondary"
              onClick={() => void boardStore.refresh()}
            >
              <RotateCw size={14} aria-hidden="true" /> Retry
            </button>
          ) : null}
        </div>
      </main>
    );
  }

  const snapshot = board.snapshot;
  const delivery = deriveDeliveryChip(snapshot);
  const progress = snapshot.total === 0 ? 0 : Math.min(100, (snapshot.verified / snapshot.total) * 100);
  const epicOptions = uniqueValues(snapshot.tasks, "epic_id");
  const workstreamOptions = uniqueValues(snapshot.tasks, "workstream_id");
  const manifestOrder = snapshot.tasks.map((task) => task.id);
  const coordinatorView = deriveCoordinatorView(snapshot, nowMs);
  const filtersActive =
    filters.search !== "" ||
    filters.epic !== "all" ||
    filters.workstream !== "all" ||
    filters.priority !== "all" ||
    filters.blockedOnly;

  return (
    <main className="pf-page pf-board">
      {/* Everything behind the drawer is inert while it is open (focus and AT). */}
      <div style={{ display: "contents" }} inert={selectedTask !== undefined}>
      <header className="pf-board-header">
        <div className="pf-board-brand">
          <BrandMark size={26} />
          <div className="pf-board-heading-copy">
            <div className="pf-board-title-line">
              <h1>{snapshot.title}</h1>
              <span className={`pf-chip ${statusTone(snapshot.status)}`}>
                <span className="pf-dot" />{readableStatus(snapshot.status)}
              </span>
            </div>
            <div className="pf-board-repo-line">
              <span><FolderGit2 size={13} aria-hidden="true" />{snapshot.repo}</span>
              <span className="pf-board-separator" aria-hidden="true">·</span>
              <span>{boardHostLabel(snapshot.host)}</span>
              <span className="pf-board-separator" aria-hidden="true">·</span>
              <span>Revision {snapshot.revision}</span>
            </div>
          </div>
        </div>

        <div className="pf-board-viewswitch" role="group" aria-label="Board view">
          <button
            type="button"
            className={viewMode === "board" ? "is-selected" : ""}
            aria-pressed={viewMode === "board"}
            onClick={() => changeView("board")}
          >
            <LayoutDashboard size={14} aria-hidden="true" /> Board
          </button>
          <button
            type="button"
            className={viewMode === "graph" ? "is-selected" : ""}
            aria-pressed={viewMode === "graph"}
            onClick={() => changeView("graph")}
          >
            <Network size={14} aria-hidden="true" /> Graph
            {coordinatorView.proposed.length > 0 ? (
              <span className="pf-board-viewswitch-count" title="Discoveries awaiting review">
                {coordinatorView.proposed.length}
              </span>
            ) : null}
          </button>
          <kbd title="Press g to switch views">g</kbd>
        </div>

        <div className="pf-board-header-status">
          <div className="pf-board-progress-copy">
            <span>Plan evidence</span>
            <strong>{snapshot.verified}/{snapshot.total} verified</strong>
          </div>
          <div
            className="pf-board-progress"
            role="progressbar"
            aria-label="Plan evidence verified"
            aria-valuemin={0}
            aria-valuemax={snapshot.total}
            aria-valuenow={snapshot.verified}
          >
            <span style={{ width: `${String(progress)}%` }} />
          </div>
          <div className="pf-board-header-chips">
            <span className={`pf-chip ${delivery.delivered ? "pf-chip--ok" : "pf-chip--warn"}`}>
              {delivery.delivered ? <CheckCircle2 size={12} aria-hidden="true" /> : <CircleDot size={12} aria-hidden="true" />}
              {delivery.label}
            </span>
            <span className={`pf-chip ${connection.tone}`}>
              {board.connection === "offline" ? <WifiOff size={12} aria-hidden="true" /> : <Wifi size={12} aria-hidden="true" />}
              <span className="pf-dot" />{connection.label}
            </span>
          </div>
        </div>
      </header>
      <div className="pf-board-body">
      <div className="pf-board-main">

      {board.connection !== "open" ? (
        <div className={`pf-board-connection-banner ${board.connection === "offline" ? "is-offline" : ""}`}>
          {board.connection === "offline" ? <WifiOff size={14} aria-hidden="true" /> : <RotateCw className="pf-spin" size={14} aria-hidden="true" />}
          <span>
            {board.authFailed
              ? `Board offline — ${AUTH_FAILED_MESSAGE}.`
              : board.connection === "offline"
                ? "Board offline — showing the last received snapshot."
                : "Reconnecting to live plan updates…"}
          </span>
        </div>
      ) : null}

      {snapshot.status === "DRIFTED" ? (
        <section className="pf-notice pf-notice--danger pf-board-drift" aria-label="Plan drift">
          <AlertTriangle size={17} aria-hidden="true" />
          <div>
            <strong>Plan drift needs attention</strong>
            {snapshot.drift.length === 0 ? (
              <p>Plan evidence no longer matches the approved manifest.</p>
            ) : (
              <ul>
                {snapshot.drift.map((reason, index) => (
                  <li key={`${reason.code}-${String(index)}`}>
                    <span className="pf-mono">{reason.code}</span> — {reason.detail}
                  </li>
                ))}
              </ul>
            )}
          </div>
        </section>
      ) : null}

      <SwarmPanel sessions={snapshot.swarm} />

      {viewMode === "board" ? (
      <>
      <section className="pf-board-filters" aria-label="Board filters">
        <label className="pf-board-search">
          <span className="pf-board-sr-only">Search tasks</span>
          <Search size={15} aria-hidden="true" />
          <input
            ref={searchInput}
            type="search"
            value={filters.search}
            placeholder="Search ID, title, or target path  /"
            onChange={(event) => setFilters((current) => ({ ...current, search: event.target.value }))}
          />
        </label>

        <label className="pf-board-filter-field">
          <span>Epic</span>
          <select
            value={filters.epic}
            onChange={(event) => setFilters((current) => ({ ...current, epic: event.target.value }))}
          >
            <option value="all">All epics</option>
            {epicOptions.map((epic) => <option key={epic} value={epic}>{epic}</option>)}
          </select>
        </label>

        <label className="pf-board-filter-field">
          <span>Workstream</span>
          <select
            value={filters.workstream}
            onChange={(event) => setFilters((current) => ({ ...current, workstream: event.target.value }))}
          >
            <option value="all">All workstreams</option>
            {workstreamOptions.map((workstream) => (
              <option key={workstream} value={workstream}>{workstream}</option>
            ))}
          </select>
        </label>

        <label className="pf-board-filter-field">
          <span>Priority</span>
          <select
            value={filters.priority}
            onChange={(event) => setFilters((current) => ({
              ...current,
              priority: event.target.value as BoardFilters["priority"],
            }))}
          >
            <option value="all">All priorities</option>
            <option value="high">High</option>
            <option value="normal">Normal</option>
            <option value="low">Low</option>
          </select>
        </label>

        <label className="pf-board-blocked-filter">
          <input
            type="checkbox"
            checked={filters.blockedOnly}
            onChange={(event) => setFilters((current) => ({ ...current, blockedOnly: event.target.checked }))}
          />
          <span>Blocked only</span>
        </label>

        {filtersActive ? (
          <button
            type="button"
            className="pf-btn pf-btn-ghost pf-btn-sm"
            onClick={() => setFilters({ ...EMPTY_FILTERS })}
          >
            <X size={13} aria-hidden="true" /> Clear
          </button>
        ) : null}
      </section>

      {snapshot.tasks.length === 0 ? (
        <section className="pf-empty pf-board-empty">
          <div className="pf-empty-icon"><LayoutDashboard size={21} aria-hidden="true" /></div>
          <h2 className="pf-section-title">No tasks in the approved plan</h2>
          <p className="pf-caption">Tasks will appear here when the plan manifest includes them.</p>
        </section>
      ) : (
        <section className="pf-board-grid" aria-label="Plan tasks by status">
          {COLUMNS.map((column) => {
            const tasks = sortBoardTasks(
              filteredTasks.filter((task) => task.column === column.id),
              manifestOrder,
            );
            return (
              <section key={column.id} className={`pf-board-column pf-board-column--${column.id}`}>
                <header>
                  <div>
                    <span className="pf-board-column-marker" aria-hidden="true" />
                    <h2>{column.label}</h2>
                    <span className="pf-board-column-count">{tasks.length}</span>
                  </div>
                  <p>{column.description}</p>
                </header>
                <div className="pf-board-card-list pf-scroll-thin">
                  {tasks.length === 0 ? (
                    <div className="pf-board-column-empty">
                      {filtersActive ? "No matching tasks" : "No tasks"}
                    </div>
                  ) : tasks.map((task) => (
                    <BoardCard
                      key={task.id}
                      task={task}
                      onOpen={openTask}
                      register={registerCard}
                    />
                  ))}
                </div>
              </section>
            );
          })}
        </section>
      )}
      </>
      ) : (
        <GraphView
          snapshot={snapshot}
          pending={board.pendingActions > 0}
          nowMs={nowMs}
          onOpenTask={openTask}
          registerNode={registerNode}
          focusDiscovery={reviewRequest}
        />
      )}
      </div>
      <CoordinatorPanel
        view={coordinatorView}
        open={coordinatorOpen}
        onToggle={toggleCoordinator}
        onOpenTask={openTask}
        onReviewDiscoveries={() => {
          const first = coordinatorView.proposed[0];
          changeView("graph");
          if (first !== undefined) {
            setReviewRequest(nextReviewRequest(reviewSeq, first.id));
          }
        }}
      />
      </div>
      </div>

      {selectedTask !== undefined ? (
        <BoardDrawer
          task={selectedTask}
          pending={board.pendingActions > 0}
          onClose={closeDrawer}
          onFocusTask={(taskID) => {
            pendingFocusTask.current = taskID;
            setFilters({ ...EMPTY_FILTERS });
            setSelectedTaskID(null);
          }}
          onPriority={(priority) => void boardStore.mutate(selectedTask.id, { priority })}
          onAddNote={(text) => boardStore.mutate(selectedTask.id, { note: text })}
          onDeleteNote={(noteID) => void boardStore.mutate(selectedTask.id, { delete_note: noteID })}
        />
      ) : null}

      {board.pendingActions > 0 ? (
        <div className="pf-board-saving" role="status">
          <RotateCw className="pf-spin" size={13} aria-hidden="true" /> Saving board update…
        </div>
      ) : null}

      {board.toast !== null ? (
        <div className="pf-board-toast" role="alert">
          <AlertTriangle size={16} aria-hidden="true" />
          <span>{board.toast.message}</span>
          <button type="button" onClick={boardStore.clearToast} aria-label="Dismiss error">
            <X size={14} aria-hidden="true" />
          </button>
        </div>
      ) : null}
    </main>
  );
}
