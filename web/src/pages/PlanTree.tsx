import { CircleAlert } from "lucide-react";

import type { PlanGraph, PlanNode } from "../ui/planGraph";

/**
 * The plan as a nested list.
 *
 * This is not a lesser view kept for completeness — it is the one that always
 * works. jsdom implements no WebGL and neither do some locked-down browsers or
 * machines that have exhausted their GPU contexts, so the 3D view cannot be
 * the only way to read a plan. It renders from the same `PlanGraph` the 3D
 * scene is built from, so the two can never disagree about what the plan
 * contains.
 */
export function PlanTree(props: {
  graph: PlanGraph;
  /** Ids added by the most recent draft, highlighted as they arrive. */
  fresh?: ReadonlySet<string>;
  selectedId?: string | null;
  onSelect: (id: string) => void;
}): React.ReactElement {
  const { graph, fresh, selectedId, onSelect } = props;
  if (graph.nodes.length === 0) {
    return <p className="pf-caption">Nothing has been drafted yet. Nodes appear as the avatars work.</p>;
  }
  const byId = new Map(graph.nodes.map((n) => [n.id, n]));
  const roots = graph.children[""] ?? [];
  return (
    <ul className="pf-plantree" role="tree" aria-label="Plan">
      {roots.map((id) => (
        <PlanTreeNode
          key={id}
          id={id}
          byId={byId}
          graph={graph}
          fresh={fresh}
          selectedId={selectedId}
          onSelect={onSelect}
        />
      ))}
    </ul>
  );
}

// Written as explicitly-undefined rather than optional: the repository builds
// with exactOptionalPropertyTypes, so passing a value that may be undefined
// into an optional property is an error.
function PlanTreeNode(props: {
  id: string;
  byId: Map<string, PlanNode>;
  graph: PlanGraph;
  fresh: ReadonlySet<string> | undefined;
  selectedId: string | null | undefined;
  onSelect: (id: string) => void;
}): React.ReactElement | null {
  const { id, byId, graph, fresh, selectedId, onSelect } = props;
  const node = byId.get(id);
  if (node === undefined) return null;
  const kids = graph.children[id] ?? [];
  return (
    <li className="pf-plantree-item" role="treeitem" aria-selected={selectedId === id}>
      <button
        type="button"
        className="pf-plantree-node"
        data-kind={node.kind}
        data-severity={node.severity ?? ""}
        data-fresh={fresh?.has(id) === true ? "yes" : undefined}
        data-selected={selectedId === id ? "yes" : undefined}
        onClick={() => onSelect(id)}
      >
        <span className="pf-plantree-kind">{node.kind}</span>
        <span className="pf-plantree-label">{node.label}</span>
        {node.severity !== undefined && (
          <span className="pf-plantree-severity" title={`${node.severity} finding against this node`}>
            <CircleAlert size={12} aria-hidden="true" /> {node.severity}
          </span>
        )}
      </button>
      {kids.length > 0 && (
        <ul role="group">
          {kids.map((childId) => (
            <PlanTreeNode
              key={childId}
              id={childId}
              byId={byId}
              graph={graph}
              fresh={fresh}
              selectedId={selectedId}
              onSelect={onSelect}
            />
          ))}
        </ul>
      )}
    </li>
  );
}
