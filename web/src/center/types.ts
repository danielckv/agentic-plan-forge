/** The shapes the command center daemon serves. Mirrors internal/center. */

export type SessionMode = "PLANNED" | "UNPLANNED";

export interface LiveSession {
  id: string;
  harness: string;
  developer?: string;
  branch?: string;
  mode: SessionMode;
  task_id?: string;
  last_tool?: string;
  last_command?: string;
  dirty_files: string[];
  started_at: string;
  last_heartbeat: string;
  seq: number;
}

export interface LiveEvent {
  seq: number;
  at: string;
  session_id: string;
  kind: string;
  detail?: string;
}

export interface LiveRepo {
  version: number;
  repo_key: string;
  repo_name: string;
  seq: number;
  sessions: Record<string, LiveSession>;
  events: LiveEvent[];
}

export interface Snapshot {
  at: string;
  repos: LiveRepo[];
}

export type TaskColumn = "todo" | "in_progress" | "done" | "drifted";

export interface SprintTask {
  id: string;
  title: string;
  epic_id?: string;
  story_id?: string;
  column: TaskColumn | string;
  priority?: string;
  blocked?: boolean;
  depends_on?: string[];
  targets: number;
  targets_met: number;
  verified: number;
}

export interface SprintView {
  repo_key: string;
  repo_name: string;
  title?: string;
  approved: boolean;
  tasks: SprintTask[];
  active: string[];
  next_up: string[];
  blocked?: string[];
  awaiting_verification?: string[];
  verified: number;
  total: number;
  drifted?: boolean;
  updated_at: string;
}

export interface SprintResponse {
  at: string;
  repos: SprintView[];
}

/**
 * A session is stale when the daemon has not heard from it recently. The
 * threshold matches livestore.StaleAfter; it only changes how a session reads,
 * never whether it is kept.
 */
export const STALE_AFTER_MS = 90_000;

export function isStale(session: LiveSession, now: number): boolean {
  const beat = Date.parse(session.last_heartbeat);
  if (Number.isNaN(beat)) return true;
  return now - beat > STALE_AFTER_MS;
}

/** Sessions newest-heartbeat first, so the busiest terminal reads first. */
export function sortedSessions(repo: LiveRepo): LiveSession[] {
  return Object.values(repo.sessions).sort(
    (a, b) => Date.parse(b.last_heartbeat) - Date.parse(a.last_heartbeat),
  );
}

/** Every session across every repository, newest first. */
export function allSessions(snapshot: Snapshot): { repo: LiveRepo; session: LiveSession }[] {
  return snapshot.repos
    .flatMap((repo) => sortedSessions(repo).map((session) => ({ repo, session })))
    .sort((a, b) => Date.parse(b.session.last_heartbeat) - Date.parse(a.session.last_heartbeat));
}
