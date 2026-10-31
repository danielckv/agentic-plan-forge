/**
 * The sprint board: the approved plan arranged as work to commit to.
 *
 * The server computes the board from the task graph every time it is asked
 * for, and stores only the owner's own moves. So this module never keeps a
 * board of its own: every change posts and takes the recomputed board back as
 * the answer, which is what keeps the page and the plan from disagreeing.
 */

import { apiFetch } from "../protocol/bridge";

/** Sprint 0 is the backlog, as the planner has it. */
export const BACKLOG = 0;

/** Which estimate the board counts in. */
export type SprintBasis = "human" | "agent";

/** One task on the board (`synthesizer.SprintItem`). */
export interface SprintItem {
  task_id: string;
  epic_id?: string;
  story_id?: string;
  workstream_id?: string;
  title: string;
  epic_title?: string;
  story_title?: string;
  human_hours: number;
  agent_minutes: number;
  depends_on?: string[];
  targets: number;
  /** True when the owner put this item here, rather than the auto-fill. */
  moved?: boolean;
  /** Dependencies that land later than this item. Only a move causes one. */
  blocked_by?: string[];
}

/** One column (`synthesizer.Sprint`). Number 0 is the backlog. */
export interface Sprint {
  number: number;
  name: string;
  items: SprintItem[];
  human_hours: number;
  agent_minutes: number;
  committed: number;
  capacity?: number;
  over?: boolean;
}

/** The whole board (`synthesizer.SprintBoard`). */
export interface SprintBoard {
  basis: SprintBasis;
  unit: string;
  sprint_days: number;
  workday_hours: number;
  team_size: number;
  max_parallel: number;
  horizon: number;
  capacity: number;
  sprints: Sprint[];
  backlog: Sprint;
  warnings?: string[];
  disclaimer: string;
}

/** What `GET /api/sprints` answers (`app.SprintBoardView`). */
export interface SprintBoardView {
  available: boolean;
  problem?: string;
  board: SprintBoard;
}

/** Settings the board can be recomputed with. */
export interface SprintSettings {
  basis?: SprintBasis;
  sprint_days?: number;
  team_size?: number;
  horizon?: number;
}

const EMPTY_BOARD: SprintBoard = {
  basis: "human",
  unit: "hours",
  sprint_days: 10,
  workday_hours: 9,
  team_size: 1,
  max_parallel: 4,
  horizon: 3,
  capacity: 0,
  sprints: [],
  backlog: { number: BACKLOG, name: "Backlog", items: [], human_hours: 0, agent_minutes: 0, committed: 0 },
  disclaimer: "",
};

/** A board that could not be loaded, carrying the reason to show. */
export function boardProblem(problem: string): SprintBoardView {
  return { available: false, problem, board: EMPTY_BOARD };
}

async function boardFrom(res: Response): Promise<SprintBoardView> {
  if (!res.ok) {
    let detail = `the board could not be loaded (HTTP ${String(res.status)})`;
    try {
      const body = (await res.json()) as { error?: string };
      if (typeof body.error === "string" && body.error !== "") detail = body.error;
    } catch {
      // Keep the status line: the body was not JSON.
    }
    return boardProblem(detail);
  }
  return (await res.json()) as SprintBoardView;
}

export async function loadBoard(): Promise<SprintBoardView> {
  try {
    return await boardFrom(await apiFetch("/api/sprints"));
  } catch (err) {
    return boardProblem(err instanceof Error ? err.message : "the board could not be loaded");
  }
}

export async function moveItem(taskID: string, sprint: number): Promise<SprintBoardView> {
  try {
    return await boardFrom(
      await apiFetch("/api/sprints/move", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ task_id: taskID, sprint }),
      }),
    );
  } catch (err) {
    return boardProblem(err instanceof Error ? err.message : "the move could not be saved");
  }
}

export async function saveSettings(settings: SprintSettings): Promise<SprintBoardView> {
  try {
    return await boardFrom(
      await apiFetch("/api/sprints/settings", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(settings),
      }),
    );
  } catch (err) {
    return boardProblem(err instanceof Error ? err.message : "the settings could not be saved");
  }
}

export async function resetArrangement(): Promise<SprintBoardView> {
  try {
    return await boardFrom(await apiFetch("/api/sprints/reset", { method: "POST" }));
  } catch (err) {
    return boardProblem(err instanceof Error ? err.message : "the arrangement could not be reset");
  }
}

/** The effort one item costs in the board's current basis. */
export function effortOf(item: SprintItem, basis: SprintBasis): number {
  return basis === "agent" ? item.agent_minutes : item.human_hours;
}

/**
 * Render an effort for a card. Hours stay hours; minutes become "1h 20m" past
 * an hour, because "80 minutes" is a number the reader has to convert.
 */
export function formatEffort(value: number, basis: SprintBasis): string {
  if (!Number.isFinite(value) || value < 0) return "—";
  if (basis === "human") {
    const rounded = Math.round(value * 10) / 10;
    return `${String(rounded)}h`;
  }
  const minutes = Math.round(value);
  if (minutes < 60) return `${String(minutes)}m`;
  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;
  return rest === 0 ? `${String(hours)}h` : `${String(hours)}h ${String(rest)}m`;
}

/**
 * How full a sprint is, as a percentage clamped to 100 for the bar's width.
 * `over` carries the fact that it went past, so the bar never lies by being
 * full when it is overfull.
 */
export function fillPercent(committed: number, capacity: number): number {
  if (!Number.isFinite(capacity) || capacity <= 0) return 0;
  const pct = (committed / capacity) * 100;
  if (!Number.isFinite(pct) || pct < 0) return 0;
  return Math.min(100, Math.round(pct));
}

/** Every column in display order: the backlog first, then the sprints. */
export function columnsOf(board: SprintBoard): Sprint[] {
  return [board.backlog, ...board.sprints];
}

/** Totals across the whole board, for the page header. */
export function boardTotals(board: SprintBoard): { items: number; committed: number; backlog: number } {
  const committed = board.sprints.reduce((sum, s) => sum + s.items.length, 0);
  return { items: committed + board.backlog.items.length, committed, backlog: board.backlog.items.length };
}
