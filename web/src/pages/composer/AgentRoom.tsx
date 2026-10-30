import { useMemo, useState } from "react";

import { AvatarBadge, avatarSpec } from "../../components/avatars/AvatarBadge";
import { useSync } from "../../store/syncStore";
import { buildRoom, fusedCandidate, workingInRoom, type RoomAgent, type RoomGapAgent, type RoomStatus } from "../../ui/agentRoom";

/**
 * The agent room: the run's team as a map.
 *
 * Permanent avatars sit on a fixed inner ring, specialists just outside them and
 * temporary agents further out on the spoke of the workstream they serve. The
 * fused candidate is the centre. Selecting a face shows what it is doing and
 * what it was given; the rings never re-flow between cycles, so a reader keeps
 * their bearings.
 *
 * It is drawn with SVG and DOM rather than a 3D renderer: every face has to stay
 * readable, focusable and reachable by keyboard, and a ring of badges gains
 * nothing from a perspective camera. The 3D scene on this page stays what it is
 * good at — the plan graph.
 */
export function AgentRoom(props: { gaps: readonly RoomGapAgent[] }): React.ReactElement {
  const sync = useSync();
  const room = useMemo(() => buildRoom(sync, props.gaps), [sync, props.gaps]);
  const centre = useMemo(() => fusedCandidate(sync), [sync]);
  const [selected, setSelected] = useState<string | null>(null);
  const working = workingInRoom(room);
  const chosen = room.find((a) => a.id === selected) ?? null;

  return (
    <section className="pf-agentroom" aria-label="The agent room">
      <header className="pf-agentroom-head">
        <h3 className="pf-section-title">Team</h3>
        <span className="pf-caption" aria-live="polite">
          {working === 0 ? "nobody working" : `${String(working)} working`}
        </span>
      </header>

      <div className="pf-agentroom-stage">
        <svg
          className="pf-agentroom-rings"
          viewBox="0 0 1 1"
          preserveAspectRatio="xMidYMid meet"
          aria-hidden="true"
        >
          <circle cx={0.5} cy={0.5} r={0.3} className="pf-agentroom-ring" />
          <circle cx={0.5} cy={0.5} r={0.38} className="pf-agentroom-ring" />
          <circle cx={0.5} cy={0.5} r={0.46} className="pf-agentroom-ring" />
          {room
            .filter((a) => a.kind === "temporary" && a.parentId !== undefined)
            .map((a) => {
              const parent = room.find((p) => p.parentId === a.parentId && p.kind === "specialist");
              if (parent === undefined) return null;
              return <line key={a.id} x1={parent.x} y1={parent.y} x2={a.x} y2={a.y} className="pf-agentroom-link" />;
            })}
        </svg>

        <div className="pf-agentroom-centre" data-present={centre.present}>
          <span className="pf-agentroom-centre-label">{centre.label}</span>
          {centre.openFindings > 0 && (
            <span className="pf-badge pf-badge--warn">{centre.openFindings} open</span>
          )}
        </div>

        {room.map((agent) => (
          <button
            key={agent.id}
            type="button"
            className="pf-agentroom-face"
            style={{ left: `${String(agent.x * 100)}%`, top: `${String(agent.y * 100)}%` }}
            data-status={agent.status}
            data-kind={agent.kind}
            aria-pressed={agent.id === selected}
            title={`${agent.name} — ${agent.job}`}
            onClick={() => setSelected(agent.id === selected ? null : agent.id)}
          >
            <AvatarBadge id={agent.id} size={34} speaking={agent.status === "working"} />
            <span className="pf-agentroom-name">{shortName(agent)}</span>
          </button>
        ))}
      </div>

      <div className="pf-agentroom-detail" aria-live="polite">
        {chosen === null ? (
          <p className="pf-body">
            Select a face to see its job, its status and the line it last said.
          </p>
        ) : (
          <AgentDetail agent={chosen} />
        )}
      </div>
    </section>
  );
}

/** The name shown under a face: short enough not to collide with its neighbours. */
function shortName(agent: RoomAgent): string {
  if (agent.kind === "specialist" && agent.parentId !== undefined) return agent.parentId;
  if (agent.kind === "temporary") return agent.name.length > 18 ? `${agent.name.slice(0, 17)}…` : agent.name;
  const words = avatarSpec(agent.id).name.split(" ");
  return words[words.length - 1] ?? agent.name;
}

function AgentDetail({ agent }: { agent: RoomAgent }): React.ReactElement {
  const spec = avatarSpec(agent.id);
  const Icon = spec.icon;
  return (
    <div className="pf-room-agent">
      <div className="pf-room-agent-head">
        <span style={{ color: spec.color, background: spec.soft }} className="pf-room-agent-glyph">
          <Icon size={16} strokeWidth={2} />
        </span>
        <div>
          <h4 className="pf-graph-context-title">{spec.name}</h4>
          <p className="pf-caption">{spec.job}</p>
        </div>
      </div>
      <dl className="pf-graph-rows">
        <div className="pf-graph-row">
          <dt>Status</dt>
          <dd>{STATUS_LABEL[agent.status]}</dd>
        </div>
        {agent.parentId !== undefined && (
          <div className="pf-graph-row">
            <dt>Works for</dt>
            <dd className="pf-mono">{agent.parentId}</dd>
          </div>
        )}
        {agent.summary !== "" && (
          <div className="pf-graph-row">
            <dt>Last said</dt>
            <dd>{agent.summary}</dd>
          </div>
        )}
      </dl>
    </div>
  );
}

/** Status words, so the room never shows an internal enum value. */
const STATUS_LABEL: Record<RoomStatus, string> = {
  working: "working now",
  queued: "queued",
  synced: "finished",
  failed: "failed",
  idle: "idle",
  cancelled: "cancelled",
};
