import { ArrowRight, CircleDot, GitBranch, Terminal } from "lucide-react";

import type { ConnState } from "./store";
import { allSessions, isStale } from "./types";
import type { LiveSession, Snapshot, SprintResponse, SprintTask, SprintView } from "./types";

/**
 * The live view.
 *
 * The ordering is the point. The coordinator's verdict comes first — what is
 * being worked on and what to begin next — because a reader should never have
 * to infer the next task from a list. Sessions come second, and the raw
 * activity log last, where it supports the summary instead of competing with
 * it.
 */

function relative(iso: string, now: number): string {
  const at = Date.parse(iso);
  if (Number.isNaN(at)) return "unknown";
  const secs = Math.max(0, Math.round((now - at) / 1000));
  if (secs < 10) return "just now";
  if (secs < 60) return `${String(secs)}s ago`;
  const mins = Math.round(secs / 60);
  if (mins < 60) return `${String(mins)}m ago`;
  const hours = Math.round(mins / 60);
  if (hours < 24) return `${String(hours)}h ago`;
  return `${String(Math.round(hours / 24))}d ago`;
}

function taskByID(view: SprintView, id: string): SprintTask | undefined {
  return view.tasks.find((t) => t.id === id);
}

/** One task in the coordinator's queue. */
function QueueRow({
  task,
  id,
  lead,
}: {
  task: SprintTask | undefined;
  id: string;
  lead?: boolean;
}): React.JSX.Element {
  return (
    <li className={`pf-center-queue-row${lead ? " is-lead" : ""}`}>
      <code className="pf-center-task-id">{id}</code>
      <span className="pf-center-queue-title">{task?.title ?? "an accepted discovery"}</span>
      {task?.blocked ? <span className="pf-badge pf-badge--high">blocked</span> : null}
      {task?.priority && task.priority !== "normal" ? (
        <span className="pf-chip">{task.priority}</span>
      ) : null}
    </li>
  );
}

/**
 * The coordinator panel. Up next is rendered as an ordered queue with its head
 * called out, because "which one do I start" is the question this answers.
 */
function Coordinator({ view }: { view: SprintView }): React.JSX.Element {
  const head = view.next_up[0];
  const rest = view.next_up.slice(1);
  return (
    <section className="pf-center-card pf-center-coordinator" aria-labelledby="pf-coordinator-h">
      <header className="pf-center-card-head">
        <h2 id="pf-coordinator-h" className="pf-h3">
          {view.repo_name}
        </h2>
        <span className="pf-caption">
          {String(view.verified)} of {String(view.total)} verified
        </span>
      </header>

      <div className="pf-center-coordinator-grid">
        <div>
          <h3 className="pf-center-label">Active now</h3>
          {view.active.length === 0 ? (
            <p className="pf-center-quiet">Nothing in progress.</p>
          ) : (
            <ul className="pf-center-queue">
              {view.active.map((id) => (
                <QueueRow key={id} id={id} task={taskByID(view, id)} />
              ))}
            </ul>
          )}
        </div>

        <div>
          <h3 className="pf-center-label">Begin next</h3>
          {head === undefined ? (
            <p className="pf-center-quiet">
              {view.total > 0 && view.verified === view.total
                ? "Every task is verified."
                : "Nothing is ready to start."}
            </p>
          ) : (
            <ol className="pf-center-queue">
              <QueueRow key={head} id={head} task={taskByID(view, head)} lead />
              {rest.map((id) => (
                <QueueRow key={id} id={id} task={taskByID(view, id)} />
              ))}
            </ol>
          )}
        </div>
      </div>

      {view.drifted ? (
        <p className="pf-banner pf-banner--warn">
          The repository has drifted from the approved plan. Verify before continuing.
        </p>
      ) : null}
    </section>
  );
}

function SessionCard({
  session,
  repoName,
  now,
}: {
  session: LiveSession;
  repoName: string;
  now: number;
}): React.JSX.Element {
  const stale = isStale(session, now);
  const planned = session.mode === "PLANNED";
  return (
    <li className="pf-center-card pf-center-session">
      <header className="pf-center-session-head">
        <span className={`pf-center-pulse${stale ? " is-idle" : ""}`} aria-hidden="true" />
        <span className="pf-center-session-harness">{session.harness}</span>
        <span className={`pf-chip${planned ? " pf-chip--accent" : ""}`}>
          {planned ? (session.task_id ?? "planned") : "ad hoc"}
        </span>
        <span className="pf-center-session-when">{relative(session.last_heartbeat, now)}</span>
      </header>

      <dl className="pf-center-session-facts">
        <div>
          <dt>
            <GitBranch size={13} aria-hidden="true" /> Branch
          </dt>
          <dd>{session.branch ?? "unknown"}</dd>
        </div>
        <div>
          <dt>
            <CircleDot size={13} aria-hidden="true" /> Repository
          </dt>
          <dd>{repoName}</dd>
        </div>
        {session.last_tool ? (
          <div>
            <dt>
              <Terminal size={13} aria-hidden="true" /> Last tool
            </dt>
            <dd>{session.last_tool}</dd>
          </div>
        ) : null}
      </dl>

      {session.last_command ? <pre className="pf-center-code">{session.last_command}</pre> : null}

      {session.dirty_files.length > 0 ? (
        <ul className="pf-center-files">
          {session.dirty_files.slice(0, 8).map((f) => (
            <li key={f}>
              <code>{f}</code>
            </li>
          ))}
          {session.dirty_files.length > 8 ? (
            <li className="pf-center-quiet">
              and {String(session.dirty_files.length - 8)} more
            </li>
          ) : null}
        </ul>
      ) : null}
    </li>
  );
}

export function LivePage({
  snapshot,
  sprint,
  conn,
}: {
  snapshot: Snapshot;
  sprint: SprintResponse;
  conn: ConnState;
}): React.JSX.Element {
  const now = Date.now();
  const sessions = allSessions(snapshot);
  const planned = sprint.repos.filter((r) => r.approved);

  if (sessions.length === 0 && planned.length === 0) {
    return (
      <div className="pf-center-empty pf-center-empty--page">
        <h1 className="pf-h2">No harness is running</h1>
        <p className="pf-body">
          {conn === "live"
            ? "Start Claude Code or oh-my-pi in a repository and its session appears here, whether or not a plan exists for it."
            : "Waiting for the command center…"}
        </p>
      </div>
    );
  }

  return (
    <>
      <div className="pf-center-pagehead">
        <h1 className="pf-h2">Live</h1>
        <p className="pf-caption">
          {sessions.length === 0
            ? "No active session"
            : `${String(sessions.length)} active ${sessions.length === 1 ? "session" : "sessions"}`}
        </p>
      </div>

      {planned.length > 0 ? (
        <div className="pf-center-stack">
          {planned.map((view) => (
            <Coordinator key={view.repo_key} view={view} />
          ))}
        </div>
      ) : (
        <section className="pf-center-card pf-center-unplanned">
          <h2 className="pf-h3">No approved plan</h2>
          <p className="pf-body">
            Everything below is being recorded without one. Run the forge to turn this work into a
            plan the coordinator can rank.
          </p>
          <p className="pf-center-run">
            <code>/plan-forge</code>
            <ArrowRight size={14} aria-hidden="true" />
            <span className="pf-caption">in your agent</span>
          </p>
        </section>
      )}

      <h2 className="pf-center-section">Sessions</h2>
      {sessions.length === 0 ? (
        <p className="pf-center-quiet">No harness has reported recently.</p>
      ) : (
        <ul className="pf-center-sessions">
          {sessions.map(({ repo, session }) => (
            <SessionCard
              key={`${repo.repo_key}:${session.id}`}
              session={session}
              repoName={repo.repo_name}
              now={now}
            />
          ))}
        </ul>
      )}
    </>
  );
}
