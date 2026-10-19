import { memo, useCallback } from "react";
import { Hourglass, Lock } from "lucide-react";

import { actorLabel } from "../coordinator";
import type { BoardColumn } from "../types";
import type { GraphEdge, GraphNode } from "./layout";

export type HoverState = "normal" | "hl" | "dim";

export const COLUMN_LABEL: Record<BoardColumn, string> = {
  todo: "To do",
  in_progress: "In progress",
  done: "Done",
  drifted: "Drifted",
};

const STATUS_TAG: Record<string, string> = {
  proposed: "PROPOSED",
  accepted: "ACCEPTED",
  dismissed: "DISMISSED",
};

/** Accessible name for a node, announced by screen readers. */
export function nodeAriaLabel(node: GraphNode): string {
  if (node.kind === "task" && node.task !== null) {
    const task = node.task;
    const parts = [`${task.id}: ${task.title}`, COLUMN_LABEL[task.column]];
    if (task.live.state === "active" && task.column !== "done") {
      parts.push(`active, worked by ${actorLabel(task.live.by)}`);
    }
    if (task.blocked) parts.push("blocked");
    if (task.awaiting_verification) parts.push("awaiting verification");
    if (task.priority === "high") parts.push("high priority");
    if (task.discovered) parts.push("discovered by the agent");
    return `${parts.join(", ")}. Press Enter to open.`;
  }
  if (node.kind === "ghost" && node.discovery !== null) {
    const discovery = node.discovery;
    return `Discovered task ${discovery.id}: ${discovery.title}, ${discovery.status}. Press Enter to review.`;
  }
  if (node.kind === "path" && node.discovery !== null) {
    return `Discovered path ${node.label}, ${node.discovery.status}. Press Enter to review.`;
  }
  return `${node.label}, ${String(node.count)} paths outside the plan`;
}

function signature(node: GraphNode): string {
  return [
    node.key,
    node.x,
    node.y,
    node.label,
    node.column,
    node.priority,
    node.blocked,
    node.active,
    node.awaiting,
    node.discovered,
    node.discoveryStatus,
    node.count,
    node.task?.live.by,
  ].join("|");
}

interface GraphNodeProps {
  node: GraphNode;
  hover: HoverState;
  selected: boolean;
  onActivate: (node: GraphNode) => void;
  onHover: (key: string | null) => void;
  onReveal: (node: GraphNode) => void;
  register: (taskID: string, element: SVGGElement | null) => void;
}

function truncate(value: string, max: number): string {
  return value.length > max ? `…${value.slice(value.length - max + 1)}` : value;
}

function TaskBody({ node }: { node: GraphNode }): React.ReactElement {
  const task = node.task;
  const pills: Array<{ text: string; className: string; width: number }> = [];
  if (node.priority === "high") pills.push({ text: "HIGH", className: "pf-graph-pill--high", width: 36 });
  if (node.kind === "task" && node.discovered) {
    pills.push({ text: "FOUND", className: "pf-graph-pill--found", width: 42 });
  }
  if (node.kind === "ghost" && node.discoveryStatus !== null) {
    const text = STATUS_TAG[node.discoveryStatus] ?? "";
    pills.push({ text, className: `pf-graph-pill--${node.discoveryStatus}`, width: text.length * 6.2 + 12 });
  }
  let cursor = node.w - 10;
  const placed = pills.map((pill) => {
    cursor -= pill.width;
    const x = cursor;
    cursor -= 5;
    return { ...pill, x };
  });
  const id = node.taskID ?? node.discoveryID ?? "";
  const live = task !== null && node.active;
  const footer = live
    ? `${actorLabel(task.live.by)} working`
    : node.column !== null
      ? COLUMN_LABEL[node.column]
      : node.discovery?.by !== undefined && node.discovery.by !== ""
        ? `found by ${actorLabel(node.discovery.by)}`
        : "";
  const markers: Array<{ title: string; className: string; icon: React.ReactElement }> = [];
  if (node.blocked) {
    markers.push({
      title: "Blocked by an unfinished dependency",
      className: "pf-graph-marker--blocked",
      icon: <Lock size={13} aria-hidden="true" />,
    });
  }
  if (node.awaiting) {
    markers.push({
      title: "Awaiting verification",
      className: "pf-graph-marker--awaiting",
      icon: <Hourglass size={13} aria-hidden="true" />,
    });
  }
  return (
    <>
      <rect className="pf-graph-shadow" y={2} width={node.w} height={node.h} rx={8} />
      <rect className="pf-graph-bg" width={node.w} height={node.h} rx={8} />
      <rect className="pf-graph-stripe" width={4} height={node.h - 16} x={0} y={8} rx={2} />
      <text className="pf-graph-id" x={14} y={19}>{id}</text>
      {placed.map((pill) => (
        <g key={pill.text} transform={`translate(${String(pill.x)} 7)`}>
          <rect className={`pf-graph-pill ${pill.className}`} width={pill.width} height={16} rx={4} />
          <text className="pf-graph-pill-text" x={pill.width / 2} y={11.5} textAnchor="middle">{pill.text}</text>
        </g>
      ))}
      {node.lines.map((line, index) => (
        <text key={line} className="pf-graph-title" x={14} y={38 + index * 14}>{line}</text>
      ))}
      <text className={`pf-graph-foot${live ? " is-live" : ""}`} x={14} y={node.h - 9}>{footer}</text>
      {markers.map((marker, index) => (
        <g
          key={marker.className}
          transform={`translate(${String(node.w - 24 - index * 18)} ${String(node.h - 22)})`}
          className={`pf-graph-marker ${marker.className}`}
        >
          <title>{marker.title}</title>
          {marker.icon}
        </g>
      ))}
    </>
  );
}

function PathBody({ node }: { node: GraphNode }): React.ReactElement {
  return (
    <>
      <rect className="pf-graph-shadow" y={2} width={node.w} height={node.h} rx={node.h / 2} />
      <rect className="pf-graph-bg" width={node.w} height={node.h} rx={node.h / 2} />
      <circle className="pf-graph-path-dot" cx={13} cy={node.h / 2} r={3.5} />
      <text className="pf-graph-path-text" x={24} y={node.h / 2 + 3.6}>{truncate(node.label, 25)}</text>
    </>
  );
}

function GroupBody({ node }: { node: GraphNode }): React.ReactElement {
  return (
    <>
      <rect className="pf-graph-shadow" y={2} width={node.w} height={node.h} rx={8} />
      <rect className="pf-graph-bg" width={node.w} height={node.h} rx={8} />
      <text className="pf-graph-group-title" x={14} y={20}>{node.label}</text>
      <text className="pf-graph-group-sub" x={14} y={35}>
        {node.count} path{node.count === 1 ? "" : "s"} outside every task
      </text>
    </>
  );
}

function nodeClass(node: GraphNode, hover: HoverState, selected: boolean): string {
  const classes = ["pf-graph-node", `pf-graph-node--${node.kind}`];
  if (node.column !== null) classes.push(`pf-graph-node--${node.column}`);
  if (node.discoveryStatus !== null) classes.push(`pf-graph-node--${node.discoveryStatus}`);
  if (node.discovered) classes.push("pf-graph-node--found");
  if (node.active) classes.push("is-active");
  if (node.blocked) classes.push("is-blocked");
  if (hover === "dim") classes.push("is-dim");
  if (hover === "hl") classes.push("is-hl");
  if (selected) classes.push("is-selected");
  return classes.join(" ");
}

export const GraphNodeView = memo(
  function GraphNodeView({
    node,
    hover,
    selected,
    onActivate,
    onHover,
    onReveal,
    register,
  }: GraphNodeProps): React.ReactElement {
    const taskID = node.taskID;
    const ref = useCallback(
      (element: SVGGElement | null) => {
        if (taskID !== null) register(taskID, element);
      },
      [register, taskID],
    );
    const interactive = node.kind !== "group";
    return (
      <g
        ref={ref}
        className={nodeClass(node, hover, selected)}
        style={{ transform: `translate(${String(node.x)}px, ${String(node.y)}px)` }}
        data-node-key={node.key}
        role={interactive ? "button" : "img"}
        tabIndex={interactive ? 0 : undefined}
        aria-label={nodeAriaLabel(node)}
        onClick={interactive ? () => onActivate(node) : undefined}
        onKeyDown={
          interactive
            ? (event) => {
                if (event.key === "Enter" || event.key === " ") {
                  event.preventDefault();
                  onActivate(node);
                }
              }
            : undefined
        }
        onPointerEnter={() => onHover(node.key)}
        onPointerLeave={() => onHover(null)}
        onFocus={() => {
          onHover(node.key);
          onReveal(node);
        }}
        onBlur={() => onHover(null)}
      >
        <g className="pf-graph-body">
          {node.active ? (
            <rect className="pf-graph-ring" x={-5} y={-5} width={node.w + 10} height={node.h + 10} rx={12} />
          ) : null}
          {node.kind === "path" ? <PathBody node={node} /> : node.kind === "group" ? <GroupBody node={node} /> : <TaskBody node={node} />}
        </g>
      </g>
    );
  },
  (previous, next) =>
    previous.hover === next.hover &&
    previous.selected === next.selected &&
    previous.onActivate === next.onActivate &&
    signature(previous.node) === signature(next.node),
);

interface GraphEdgeProps {
  edge: GraphEdge;
  hover: HoverState;
}

export const GraphEdgeView = memo(
  function GraphEdgeView({ edge, hover }: GraphEdgeProps): React.ReactElement {
    const marker = hover === "hl" ? "hl" : edge.kind;
    return (
      <path
        className={`pf-graph-edge pf-graph-edge--${edge.kind}${hover === "hl" ? " is-hl" : ""}${hover === "dim" ? " is-dim" : ""}`}
        d={edge.d}
        // Chromium interpolates `d` through CSS, so edges glide with their nodes; others snap.
        style={{ d: `path("${edge.d}")` } as React.CSSProperties}
        markerEnd={`url(#pf-graph-arrow-${marker})`}
      />
    );
  },
  (previous, next) => previous.hover === next.hover && previous.edge.d === next.edge.d && previous.edge.kind === next.edge.kind,
);
