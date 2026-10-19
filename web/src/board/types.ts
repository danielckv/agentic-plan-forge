export type BoardColumn = "todo" | "in_progress" | "done" | "drifted";
export type BoardPriority = "high" | "normal" | "low";
export type TargetOperation = "add" | "modify" | "remove" | "document";

export interface BoardDriftReason {
  code: string;
  detail: string;
}

export interface BoardTarget {
  path: string;
  operation: TargetOperation;
  symbol_id: string;
  verified: boolean;
}

export interface BoardVerification {
  total: number;
  passed: number;
}

export interface BoardEstimate {
  likely: number;
  basis: string;
}

export interface BoardNote {
  id: string;
  at: string;
  text: string;
}

export interface BoardTask {
  id: string;
  epic_id: string;
  story_id: string;
  workstream_id: string;
  title: string;
  column: BoardColumn;
  blocked: boolean;
  depends_on: string[];
  priority: BoardPriority;
  targets: BoardTarget[];
  verification: BoardVerification;
  acceptance_criteria: string[];
  human_estimate: BoardEstimate;
  agent_estimate: BoardEstimate;
  notes: BoardNote[];
}

export interface BoardConsumer {
  id: string;
  delivered_revision: number;
  seen_at: string;
}

export interface BoardSnapshot {
  repo: string;
  title: string;
  manifest_sha256: string;
  approved: boolean;
  status: string;
  verified: number;
  total: number;
  revision: number;
  host: string;
  evaluated_at: string;
  drift: BoardDriftReason[];
  tasks: BoardTask[];
  consumers: BoardConsumer[];
}

export type SnapshotValidation =
  | { ok: true; value: BoardSnapshot }
  | { ok: false; error: string };

const COLUMNS: Record<BoardColumn, true> = {
  todo: true,
  in_progress: true,
  done: true,
  drifted: true,
};
const PRIORITIES: Record<BoardPriority, true> = {
  high: true,
  normal: true,
  low: true,
};
const OPERATIONS: Record<TargetOperation, true> = {
  add: true,
  modify: true,
  remove: true,
  document: true,
};

function isString(value: unknown): value is string {
  return typeof value === "string";
}

function isNonNegativeInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isInteger(value) && value >= 0;
}

function isNonNegativeFinite(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value) && value >= 0;
}

function stringArray(value: unknown): value is string[] {
  return Array.isArray(value) && value.every(isString);
}

function validDrift(value: unknown): value is BoardDriftReason {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const drift = value as BoardDriftReason;
  return isString(drift.code) && isString(drift.detail);
}

function validTarget(value: unknown): value is BoardTarget {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const target = value as BoardTarget;
  return (
    isString(target.path) &&
    isString(target.operation) &&
    target.operation in OPERATIONS &&
    isString(target.symbol_id) &&
    typeof target.verified === "boolean"
  );
}

function validVerification(value: unknown): value is BoardVerification {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const verification = value as BoardVerification;
  return (
    isNonNegativeInteger(verification.total) &&
    isNonNegativeInteger(verification.passed) &&
    verification.passed <= verification.total
  );
}

function validEstimate(value: unknown): value is BoardEstimate {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const estimate = value as BoardEstimate;
  return isNonNegativeFinite(estimate.likely) && isString(estimate.basis);
}

function validNote(value: unknown): value is BoardNote {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const note = value as BoardNote;
  return isString(note.id) && isString(note.at) && isString(note.text);
}

function validTask(value: unknown): value is BoardTask {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const task = value as BoardTask;
  return (
    isString(task.id) &&
    isString(task.epic_id) &&
    isString(task.story_id) &&
    isString(task.workstream_id) &&
    isString(task.title) &&
    isString(task.column) &&
    task.column in COLUMNS &&
    typeof task.blocked === "boolean" &&
    stringArray(task.depends_on) &&
    isString(task.priority) &&
    task.priority in PRIORITIES &&
    Array.isArray(task.targets) &&
    task.targets.every(validTarget) &&
    validVerification(task.verification) &&
    stringArray(task.acceptance_criteria) &&
    validEstimate(task.human_estimate) &&
    validEstimate(task.agent_estimate) &&
    Array.isArray(task.notes) &&
    task.notes.every(validNote)
  );
}

function validConsumer(value: unknown): value is BoardConsumer {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const consumer = value as BoardConsumer;
  return (
    isString(consumer.id) &&
    isNonNegativeInteger(consumer.delivered_revision) &&
    isString(consumer.seen_at)
  );
}

/** Validate every field the fixed C5 snapshot contract exposes before it reaches React. */
export function validateBoardSnapshot(value: unknown): SnapshotValidation {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return { ok: false, error: "snapshot must be an object" };
  }
  const snapshot = value as BoardSnapshot;
  if (!isString(snapshot.repo)) return { ok: false, error: "repo must be a string" };
  if (!isString(snapshot.title)) return { ok: false, error: "title must be a string" };
  if (!isString(snapshot.manifest_sha256)) {
    return { ok: false, error: "manifest_sha256 must be a string" };
  }
  if (typeof snapshot.approved !== "boolean") {
    return { ok: false, error: "approved must be a boolean" };
  }
  if (!isString(snapshot.status)) return { ok: false, error: "status must be a string" };
  if (!isNonNegativeInteger(snapshot.verified) || !isNonNegativeInteger(snapshot.total)) {
    return { ok: false, error: "verified and total must be non-negative integers" };
  }
  if (snapshot.verified > snapshot.total) {
    return { ok: false, error: "verified cannot exceed total" };
  }
  if (!isNonNegativeInteger(snapshot.revision)) {
    return { ok: false, error: "revision must be a non-negative integer" };
  }
  if (!isString(snapshot.host)) return { ok: false, error: "host must be a string" };
  if (!isString(snapshot.evaluated_at)) {
    return { ok: false, error: "evaluated_at must be a string" };
  }
  if (!Array.isArray(snapshot.drift) || !snapshot.drift.every(validDrift)) {
    return { ok: false, error: "drift is malformed" };
  }
  if (!Array.isArray(snapshot.tasks) || !snapshot.tasks.every(validTask)) {
    return { ok: false, error: "tasks are malformed" };
  }
  const taskIDs = new Set(snapshot.tasks.map((task) => task.id));
  if (taskIDs.size !== snapshot.tasks.length) {
    return { ok: false, error: "task ids must be unique" };
  }
  if (!Array.isArray(snapshot.consumers) || !snapshot.consumers.every(validConsumer)) {
    return { ok: false, error: "consumers are malformed" };
  }
  return { ok: true, value: snapshot };
}
