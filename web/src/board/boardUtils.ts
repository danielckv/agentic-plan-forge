import type { BoardPriority, BoardSnapshot, BoardTask } from "./types";

export interface BoardFilters {
  search: string;
  epic: string;
  workstream: string;
  priority: BoardPriority | "all";
  blockedOnly: boolean;
}

export const EMPTY_FILTERS: BoardFilters = {
  search: "",
  epic: "all",
  workstream: "all",
  priority: "all",
  blockedOnly: false,
};

const PRIORITY_RANK: Record<BoardPriority, number> = {
  high: 0,
  normal: 1,
  low: 2,
};

/** HIGH tasks lead, LOW tasks trail, and manifest order is stable inside each tier. */
export function sortBoardTasks(
  tasks: readonly BoardTask[],
  manifestOrder: readonly string[] = tasks.map((task) => task.id),
): BoardTask[] {
  const order = new Map(manifestOrder.map((id, index) => [id, index]));
  return [...tasks].sort((left, right) => {
    const priorityDelta = PRIORITY_RANK[left.priority] - PRIORITY_RANK[right.priority];
    if (priorityDelta !== 0) return priorityDelta;
    return (order.get(left.id) ?? Number.MAX_SAFE_INTEGER) -
      (order.get(right.id) ?? Number.MAX_SAFE_INTEGER);
  });
}

export function filterBoardTasks(tasks: readonly BoardTask[], filters: BoardFilters): BoardTask[] {
  const query = filters.search.trim().toLocaleLowerCase();
  return tasks.filter((task) => {
    if (filters.epic !== "all" && task.epic_id !== filters.epic) return false;
    if (filters.workstream !== "all" && task.workstream_id !== filters.workstream) return false;
    if (filters.priority !== "all" && task.priority !== filters.priority) return false;
    if (filters.blockedOnly && !task.blocked) return false;
    if (query === "") return true;
    return (
      task.id.toLocaleLowerCase().includes(query) ||
      task.title.toLocaleLowerCase().includes(query) ||
      task.targets.some((target) => target.path.toLocaleLowerCase().includes(query))
    );
  });
}

export interface DeliveryChip {
  delivered: boolean;
  label: string;
}

/** Host label from a consumer id prefix (`claude:…`, `omp:…`). */
export function consumerHostLabel(consumerID: string): string {
  const separator = consumerID.indexOf(":");
  const prefix = separator < 0 ? "" : consumerID.slice(0, separator);
  if (prefix === "claude") return "Claude Code";
  if (prefix === "omp") return "oh-my-pi";
  return prefix === "" ? "the agent" : prefix;
}

export function deriveDeliveryChip(snapshot: BoardSnapshot): DeliveryChip {
  const consumer = snapshot.consumers.find(
    (candidate) => candidate.delivered_revision >= snapshot.revision,
  );
  return {
    delivered: consumer !== undefined,
    label:
      consumer === undefined
        ? "Waiting for the agent's next step"
        : `Delivered to ${consumerHostLabel(consumer.id)} · rev ${String(snapshot.revision)}`,
  };
}

export function boardHostLabel(host: string): string {
  if (host === "claude") return "Claude Code";
  if (host === "omp") return "oh-my-pi";
  return host === "" ? "Unknown host" : host;
}
