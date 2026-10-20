export type BoardColumn = "todo" | "in_progress" | "done" | "drifted";
export type BoardPriority = "high" | "normal" | "low";
export type TargetOperation = "add" | "modify" | "remove" | "document";
/** Harness activity state; the empty string means "no activity recorded". */
export type LiveState = "active" | "done" | "blocked" | "";
export type DiscoveryKind = "task" | "path";
export type DiscoveryStatus = "proposed" | "accepted" | "dismissed";

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

/** C11 `live`: the latest harness activity recorded for a task. */
export interface BoardLive {
  state: LiveState;
  by: string;
  source: string;
  detail: string;
  at: string;
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
  /** C11. Defaults to an empty activity when an older server omits it. */
  live: BoardLive;
  /** C11. The harness reported done, evidence has not confirmed it yet. */
  awaiting_verification: boolean;
  /** C11. True for accepted discoveries that became board tasks. */
  discovered: boolean;
}

export interface BoardDiscovery {
  id: string;
  kind: DiscoveryKind;
  title: string;
  paths: string[];
  parent_task_id: string;
  reason: string;
  by: string;
  at: string;
  status: DiscoveryStatus;
  priority: BoardPriority;
}

export interface BoardCoordinator {
  active: string[];
  next_up: string[];
  updated_at: string;
}

export interface BoardConsumer {
  id: string;
  delivered_revision: number;
  seen_at: string;
}

/**
 * Why a session shows no agents. `live`: snapshots are arriving. `silent`: the
 * harness speaks the swarm protocol but has published nothing yet. `none`: the
 * harness has no swarm channel to ask — Claude Code reports tool telemetry
 * only, and an empty swarm there would read as idle rather than as unknown.
 */
export type SwarmChannel = "live" | "silent" | "none";

/** One live subagent, as the harness itself reports it. */
export interface SwarmAgent {
  id: string;
  agent: string;
  employee?: string;
  task: string;
}

/** The harness's own view of what it is doing. */
export interface Swarm {
  word: string;
  waiting_on_user: boolean;
  task_id?: string;
  stage?: string;
  dispatch?: string;
  epic?: string;
  current_task?: string;
  active_agents: number;
  agents: SwarmAgent[];
  at: string;
}

/** One live harness session on this machine. */
export interface BoardSwarmSession {
  id: string;
  harness: string;
  repo: string;
  branch?: string;
  this_repo: boolean;
  stale: boolean;
  channel: SwarmChannel;
  /** Present only when `channel` is `live`. */
  swarm?: Swarm;
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
  /** C11. Defaults to `[]` when an older server omits it. */
  discoveries: BoardDiscovery[];
  /** C11. Defaults to an empty coordinator when an older server omits it. */
  coordinator: BoardCoordinator;
  /** Live harness sessions. Defaults to `[]` when an older server omits it. */
  swarm: BoardSwarmSession[];
}

export type SnapshotValidation =
  | { ok: true; value: BoardSnapshot }
  | { ok: false; error: string };

export const EMPTY_LIVE: BoardLive = Object.freeze({
  state: "",
  by: "",
  source: "",
  detail: "",
  at: "",
});

export const EMPTY_COORDINATOR: BoardCoordinator = Object.freeze({
  active: [],
  next_up: [],
  updated_at: "",
}) as BoardCoordinator;

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
const LIVE_STATES: Record<LiveState, true> = {
  active: true,
  done: true,
  blocked: true,
  "": true,
};
const DISCOVERY_KINDS: Record<DiscoveryKind, true> = { task: true, path: true };
const DISCOVERY_STATUSES: Record<DiscoveryStatus, true> = {
  proposed: true,
  accepted: true,
  dismissed: true,
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

/** An empty or missing operation is a server-shaped discovered task; it normalizes to `modify`. */
function blankOperation(operation: unknown): boolean {
  return operation === undefined || operation === null || operation === "";
}

function validTarget(value: unknown): value is BoardTarget {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const target = value as BoardTarget;
  return (
    isString(target.path) &&
    (blankOperation(target.operation) ||
      (isString(target.operation) && Object.prototype.hasOwnProperty.call(OPERATIONS, target.operation))) &&
    isString(target.symbol_id) &&
    typeof target.verified === "boolean"
  );
}

function normalizeTargets(targets: BoardTarget[]): BoardTarget[] {
  if (targets.every((target) => !blankOperation(target.operation))) return targets;
  return targets.map((target) =>
    blankOperation(target.operation) ? { ...target, operation: "modify" } : target,
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

function validLive(value: unknown): value is BoardLive {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const live = value as Record<string, unknown>;
  return (
    isString(live.state) &&
    live.state in LIVE_STATES &&
    isString(live.by) &&
    isString(live.source) &&
    isString(live.detail) &&
    isString(live.at)
  );
}

/**
 * The C5 task fields are mandatory. The C11 fields are optional on the wire (an
 * older server omits them) but strictly typed when present.
 */
function validTaskBase(value: unknown): value is BoardTask {
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

function c11TaskFieldsValid(task: Record<string, unknown>): boolean {
  if (task.live !== undefined && task.live !== null && !validLive(task.live)) return false;
  if (
    task.awaiting_verification !== undefined &&
    task.awaiting_verification !== null &&
    typeof task.awaiting_verification !== "boolean"
  ) {
    return false;
  }
  if (task.discovered !== undefined && task.discovered !== null && typeof task.discovered !== "boolean") {
    return false;
  }
  return true;
}

/** Fill the C11 task defaults and normalize blank target operations; returns the same object when nothing changes. */
function normalizeTask(task: BoardTask): BoardTask {
  const raw = task as unknown as Record<string, unknown>;
  const targets = normalizeTargets(task.targets);
  if (
    targets === task.targets &&
    validLive(raw.live) &&
    typeof raw.awaiting_verification === "boolean" &&
    typeof raw.discovered === "boolean"
  ) {
    return task;
  }
  return {
    ...task,
    targets,
    live: validLive(raw.live) ? raw.live : EMPTY_LIVE,
    awaiting_verification: raw.awaiting_verification === true,
    discovered: raw.discovered === true,
  };
}

function validDiscovery(value: unknown): value is Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const d = value as Record<string, unknown>;
  return (
    isString(d.id) &&
    d.id !== "" &&
    isString(d.kind) &&
    d.kind in DISCOVERY_KINDS &&
    isString(d.title) &&
    (d.paths === undefined || d.paths === null || stringArray(d.paths)) &&
    (d.parent_task_id === undefined || d.parent_task_id === null || isString(d.parent_task_id)) &&
    isString(d.reason) &&
    isString(d.by) &&
    isString(d.at) &&
    isString(d.status) &&
    d.status in DISCOVERY_STATUSES &&
    isString(d.priority) &&
    d.priority in PRIORITIES
  );
}

function normalizeDiscovery(value: Record<string, unknown>): BoardDiscovery {
  return {
    id: value.id as string,
    kind: value.kind as DiscoveryKind,
    title: value.title as string,
    paths: Array.isArray(value.paths) ? (value.paths as string[]) : [],
    parent_task_id: isString(value.parent_task_id) ? value.parent_task_id : "",
    reason: value.reason as string,
    by: value.by as string,
    at: value.at as string,
    status: value.status as DiscoveryStatus,
    priority: value.priority as BoardPriority,
  };
}

function validCoordinator(value: unknown): value is Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const c = value as Record<string, unknown>;
  return (
    (c.active === undefined || c.active === null || stringArray(c.active)) &&
    (c.next_up === undefined || c.next_up === null || stringArray(c.next_up)) &&
    isString(c.updated_at)
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

function validSwarmAgent(value: unknown): value is SwarmAgent {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const agent = value as Record<string, unknown>;
  return (
    isString(agent.id) &&
    isString(agent.agent) &&
    isString(agent.task) &&
    (agent.employee === undefined || isString(agent.employee))
  );
}

function validSwarm(value: unknown): boolean {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const swarm = value as Record<string, unknown>;
  if (!isString(swarm.word) || typeof swarm.waiting_on_user !== "boolean") return false;
  if (!isNonNegativeInteger(swarm.active_agents) || !isString(swarm.at)) return false;
  for (const key of ["task_id", "stage", "dispatch", "epic", "current_task"]) {
    if (swarm[key] !== undefined && !isString(swarm[key])) return false;
  }
  const agents: unknown = swarm.agents;
  if (agents === undefined || agents === null) return true;
  return Array.isArray(agents) && agents.every(validSwarmAgent);
}

function validSwarmSession(value: unknown): boolean {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const session = value as Record<string, unknown>;
  if (!isString(session.id) || !isString(session.harness) || !isString(session.repo)) return false;
  if (typeof session.this_repo !== "boolean" || typeof session.stale !== "boolean") return false;
  if (session.branch !== undefined && !isString(session.branch)) return false;
  if (session.channel !== "live" && session.channel !== "silent" && session.channel !== "none") {
    return false;
  }
  if (session.swarm === undefined || session.swarm === null) return true;
  return validSwarm(session.swarm);
}

function normalizeSwarmSession(value: Record<string, unknown>): BoardSwarmSession {
  const { swarm, ...session } = value as unknown as BoardSwarmSession;
  if (!swarm) return session;
  return { ...session, swarm: { ...swarm, agents: swarm.agents ?? [] } };
}

/**
 * Validate every field the fixed C5/C11 snapshot contract exposes before it
 * reaches React. C11 fields are optional (older servers) but strict when sent;
 * `null` arrays (Go nil slices) read as empty.
 */
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
  if (
    !Array.isArray(snapshot.tasks) ||
    !snapshot.tasks.every(
      (task) =>
        validTaskBase(task) && c11TaskFieldsValid(task as unknown as Record<string, unknown>),
    )
  ) {
    return { ok: false, error: "tasks are malformed" };
  }
  const taskIDs = new Set(snapshot.tasks.map((task) => task.id));
  if (taskIDs.size !== snapshot.tasks.length) {
    return { ok: false, error: "task ids must be unique" };
  }
  if (!Array.isArray(snapshot.consumers) || !snapshot.consumers.every(validConsumer)) {
    return { ok: false, error: "consumers are malformed" };
  }

  const rawDiscoveries: unknown = (value as Record<string, unknown>).discoveries;
  let discoveries: BoardDiscovery[] = [];
  if (rawDiscoveries !== undefined && rawDiscoveries !== null) {
    if (!Array.isArray(rawDiscoveries) || !rawDiscoveries.every(validDiscovery)) {
      return { ok: false, error: "discoveries are malformed" };
    }
    discoveries = (rawDiscoveries as Array<Record<string, unknown>>).map(normalizeDiscovery);
    if (new Set(discoveries.map((discovery) => discovery.id)).size !== discoveries.length) {
      return { ok: false, error: "discovery ids must be unique" };
    }
  }

  const rawCoordinator: unknown = (value as Record<string, unknown>).coordinator;
  let coordinator: BoardCoordinator = EMPTY_COORDINATOR;
  if (rawCoordinator !== undefined && rawCoordinator !== null) {
    if (!validCoordinator(rawCoordinator)) return { ok: false, error: "coordinator is malformed" };
    coordinator = {
      active: Array.isArray(rawCoordinator.active) ? (rawCoordinator.active as string[]) : [],
      next_up: Array.isArray(rawCoordinator.next_up) ? (rawCoordinator.next_up as string[]) : [],
      updated_at: rawCoordinator.updated_at as string,
    };
  }

  const rawSwarm: unknown = (value as Record<string, unknown>).swarm;
  let swarm: BoardSwarmSession[] = [];
  if (rawSwarm !== undefined && rawSwarm !== null) {
    if (!Array.isArray(rawSwarm) || !rawSwarm.every(validSwarmSession)) {
      return { ok: false, error: "swarm is malformed" };
    }
    swarm = (rawSwarm as Array<Record<string, unknown>>).map(normalizeSwarmSession);
  }

  return {
    ok: true,
    value: {
      ...snapshot,
      tasks: snapshot.tasks.map(normalizeTask),
      discoveries,
      coordinator,
      swarm,
    },
  };
}
