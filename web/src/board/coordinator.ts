import { consumerHostLabel } from "./boardUtils";
import type { BoardDiscovery, BoardSnapshot, BoardTask } from "./types";

export interface ActiveEntry {
  task: BoardTask;
  /** Who is working: "Claude Code", "oh-my-pi", "You" … */
  actor: string;
  /** Relative time of the last activity, e.g. "12s ago"; empty when unknown. */
  ago: string;
  detail: string;
  at: string;
}

export interface NextUpEntry {
  task: BoardTask;
  /** 1-based position in the coordinator queue. */
  rank: number;
  unblocked: boolean;
  /** Short hint shown beside the priority. */
  hint: string;
}

export interface CoordinatorView {
  active: ActiveEntry[];
  nextUp: NextUpEntry[];
  /** Proposed discoveries awaiting a decision. */
  proposed: BoardDiscovery[];
  proposedTasks: number;
  proposedPaths: number;
  /** Relative age of `coordinator.updated_at`, or "" when never set. */
  updatedAgo: string;
}

/** `by` is a consumer id (`claude:abc`, `omp:1`), the literal `user`, or empty. */
export function actorLabel(by: string): string {
  if (by === "user") return "You";
  return consumerHostLabel(by);
}

/** Compact relative time. Unparseable input yields "". */
export function formatAgo(at: string, nowMs: number): string {
  const then = Date.parse(at);
  if (Number.isNaN(then)) return "";
  const seconds = Math.max(0, Math.round((nowMs - then) / 1000));
  if (seconds < 5) return "just now";
  if (seconds < 60) return `${String(seconds)}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${String(minutes)}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 48) return `${String(hours)}h ago`;
  return `${String(Math.floor(hours / 24))}d ago`;
}

export interface ReviewRequest {
  id: string;
  seq: number;
}

/**
 * Build the next "Review in graph" request. The counter lives in a ref that a
 * view change never resets, so every request gets a fresh sequence number and
 * the graph reopens its panel each time.
 */
export function nextReviewRequest(counter: { current: number }, id: string): ReviewRequest {
  counter.current += 1;
  return { id, seq: counter.current };
}

/**
 * Active tasks come from each task's `live.state`. The coordinator's `active`
 * list (newest first) orders them; any active task it does not mention follows,
 * newest activity first.
 */
function deriveActive(snapshot: BoardSnapshot, nowMs: number): ActiveEntry[] {
  const order = new Map(snapshot.coordinator.active.map((id, index) => [id, index]));
  const active = snapshot.tasks.filter((task) => task.live.state === "active" && task.column !== "done");
  active.sort((left, right) => {
    const leftRank = order.get(left.id) ?? Number.MAX_SAFE_INTEGER;
    const rightRank = order.get(right.id) ?? Number.MAX_SAFE_INTEGER;
    if (leftRank !== rightRank) return leftRank - rightRank;
    return Date.parse(right.live.at) - Date.parse(left.live.at) || 0;
  });
  return active.map((task) => ({
    task,
    actor: actorLabel(task.live.by),
    ago: formatAgo(task.live.at, nowMs),
    detail: task.live.detail,
    at: task.live.at,
  }));
}

function deriveNextUp(snapshot: BoardSnapshot): NextUpEntry[] {
  const byID = new Map(snapshot.tasks.map((task) => [task.id, task]));
  const entries: NextUpEntry[] = [];
  for (const id of snapshot.coordinator.next_up) {
    const task = byID.get(id);
    if (task === undefined) continue;
    entries.push({
      task,
      rank: entries.length + 1,
      unblocked: !task.blocked,
      hint: task.blocked ? "Blocked" : entries.length === 0 ? "Unblocked · steering the agent" : "Unblocked",
    });
  }
  return entries;
}

export function deriveCoordinatorView(snapshot: BoardSnapshot, nowMs: number): CoordinatorView {
  const proposed = snapshot.discoveries.filter((discovery) => discovery.status === "proposed");
  return {
    active: deriveActive(snapshot, nowMs),
    nextUp: deriveNextUp(snapshot),
    proposed,
    proposedTasks: proposed.filter((discovery) => discovery.kind === "task").length,
    proposedPaths: proposed.filter((discovery) => discovery.kind === "path").length,
    updatedAgo: formatAgo(snapshot.coordinator.updated_at, nowMs),
  };
}
