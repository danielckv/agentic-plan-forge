import { Bot, CircleSlash, Hourglass, Users } from "lucide-react";

import type { BoardSwarmSession } from "./types";

interface SwarmPanelProps {
  sessions: BoardSwarmSession[];
}

/**
 * What a session's channel state means, in the user's terms. The three states
 * are kept apart on purpose: a harness with nothing running and a harness that
 * cannot be asked look identical on screen unless the panel says which it is.
 */
const CHANNEL_NOTE: Record<BoardSwarmSession["channel"], string> = {
  live: "",
  silent: "Connected — no agents dispatched yet.",
  none: "Could not communicate with this session: Claude Code reports tool activity, but publishes no swarm channel, so its agents cannot be listed here.",
};

function harnessLabel(harness: string): string {
  switch (harness) {
    case "omp":
    case "oh-my-pi":
      return "oh-my-pi";
    case "claude":
    case "claude-code":
      return "Claude Code";
    default:
      return harness;
  }
}

function SwarmSessionCard({ session }: { session: BoardSwarmSession }): React.ReactElement {
  const swarm = session.swarm;
  const agents = swarm?.agents ?? [];
  const note = CHANNEL_NOTE[session.channel];
  return (
    <li className={`pf-swarm-session${session.stale ? " is-stale" : ""}`}>
      <div className="pf-swarm-session-head">
        <span className="pf-swarm-harness">{harnessLabel(session.harness)}</span>
        <span className="pf-swarm-repo" title={session.repo}>
          {session.repo}
          {session.this_repo ? <span className="pf-swarm-here"> · this plan</span> : null}
        </span>
        {session.branch ? <span className="pf-mono pf-swarm-branch">{session.branch}</span> : null}
        {swarm ? (
          <span className={`pf-swarm-word${swarm.waiting_on_user ? " is-waiting" : ""}`}>
            {swarm.waiting_on_user ? "WAITING" : swarm.word}
          </span>
        ) : null}
        {session.stale ? <span className="pf-swarm-stale-tag">went quiet</span> : null}
      </div>

      {swarm && (swarm.epic ?? swarm.current_task) ? (
        <div className="pf-swarm-focus">
          {swarm.epic ? <span className="pf-swarm-epic">{swarm.epic}</span> : null}
          {swarm.current_task ? <span className="pf-swarm-step">{swarm.current_task}</span> : null}
          {swarm.dispatch ? <span className="pf-swarm-dispatch">{swarm.dispatch}</span> : null}
        </div>
      ) : null}

      {note ? (
        <p className={`pf-swarm-note${session.channel === "none" ? " is-unreachable" : ""}`}>
          {session.channel === "none" ? (
            <CircleSlash size={13} aria-hidden="true" />
          ) : (
            <Hourglass size={13} aria-hidden="true" />
          )}
          <span>{note}</span>
        </p>
      ) : null}

      {agents.length > 0 ? (
        <ul className="pf-swarm-agents">
          {agents.map((agent) => (
            <li key={agent.id} className="pf-swarm-agent">
              <Bot size={13} aria-hidden="true" />
              <span className="pf-swarm-agent-name">{agent.employee ?? agent.agent}</span>
              <span className="pf-swarm-agent-type pf-mono">{agent.agent}</span>
              <span className="pf-swarm-agent-task">{agent.task}</span>
            </li>
          ))}
        </ul>
      ) : null}

      {/* A harness may report a count without naming the agents behind it. */}
      {swarm && agents.length === 0 && swarm.active_agents > 0 ? (
        <p className="pf-swarm-note">
          <Bot size={13} aria-hidden="true" />
          <span>
            {swarm.active_agents} agent{swarm.active_agents === 1 ? "" : "s"} running, unnamed.
          </span>
        </p>
      ) : null}
    </li>
  );
}

/**
 * The live swarm beside the plan: every harness session on this machine, this
 * repository's first, with the agents each one has dispatched.
 */
export function SwarmPanel({ sessions }: SwarmPanelProps): React.ReactElement | null {
  if (sessions.length === 0) return null;
  const liveAgents = sessions.reduce((total, session) => total + (session.swarm?.active_agents ?? 0), 0);
  return (
    <section className="pf-swarm" aria-label="Live agents">
      <header className="pf-swarm-head">
        <Users size={15} aria-hidden="true" />
        <h2 className="pf-h3">Live agents</h2>
        <span className="pf-swarm-count">
          {sessions.length} session{sessions.length === 1 ? "" : "s"}
          {liveAgents > 0 ? ` · ${String(liveAgents)} agent${liveAgents === 1 ? "" : "s"}` : ""}
        </span>
      </header>
      <ul className="pf-swarm-sessions">
        {sessions.map((session) => (
          <SwarmSessionCard key={session.id} session={session} />
        ))}
      </ul>
    </section>
  );
}
