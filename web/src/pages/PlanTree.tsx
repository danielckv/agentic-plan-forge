import { ChevronDown, ChevronRight, CircleAlert, FileText } from "lucide-react";
import { useState } from "react";

import type { PlanGraph, PlanNode } from "../ui/planGraph";

/**
 * The plan as a stack of documents.
 *
 * This is not a lesser view kept for completeness — it is the one that always
 * works. jsdom implements no WebGL and neither do some locked-down browsers or
 * machines that have exhausted their GPU contexts, so the 3D view cannot be
 * the only way to read a plan. It renders from the same `PlanGraph` the 3D
 * scene is built from, so the two can never disagree about what the plan
 * contains.
 *
 * A node is drawn as a document card, not a dot: the point of the 2D view is
 * to read the plan, and a circle answers nothing. Each card shows its kind and
 * title, expands to its children, and selecting one opens its detail below, so
 * a reader can see what an epic, story or workstream actually says without
 * leaving the page.
 */
export function PlanTree(props: {
  graph: PlanGraph;
  /** Ids added by the most recent draft, highlighted as they arrive. */
  fresh?: ReadonlySet<string>;
  selectedId?: string | null;
  onSelect: (id: string) => void;
}): React.ReactElement {
  const { graph, fresh, selectedId, onSelect } = props;
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(() => new Set());

  if (graph.nodes.length === 0) {
    return <p className="pf-caption">Nothing has been drafted yet. Nodes appear as the avatars work.</p>;
  }
  const byId = new Map(graph.nodes.map((n) => [n.id, n]));
  const roots = graph.children[""] ?? [];
  const selected = selectedId === null || selectedId === undefined ? null : byId.get(selectedId) ?? null;

  const toggle = (id: string): void => {
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  return (
    <div className="pf-plandocs">
      <ul className="pf-plandocs-list" role="tree" aria-label="Plan documents">
        {roots.map((id) => (
          <PlanDoc
            key={id}
            id={id}
            byId={byId}
            graph={graph}
            fresh={fresh}
            collapsed={collapsed}
            selectedId={selectedId ?? null}
            onToggle={toggle}
            onSelect={onSelect}
          />
        ))}
      </ul>
      {selected !== null && <PlanNodeDetail node={selected} graph={graph} parent={parentOf(selected, byId)} />}
    </div>
  );
}

function parentOf(node: PlanNode, byId: Map<string, PlanNode>): PlanNode | null {
  if (node.parentId === null) return null;
  return byId.get(node.parentId) ?? null;
}

// Written as explicitly-undefined rather than optional: the repository builds
// with exactOptionalPropertyTypes, so passing a value that may be undefined
// into an optional property is an error.
function PlanDoc(props: {
  id: string;
  byId: Map<string, PlanNode>;
  graph: PlanGraph;
  fresh: ReadonlySet<string> | undefined;
  collapsed: ReadonlySet<string>;
  selectedId: string | null;
  onToggle: (id: string) => void;
  onSelect: (id: string) => void;
}): React.ReactElement | null {
  const { id, byId, graph, fresh, collapsed, selectedId, onToggle, onSelect } = props;
  const node = byId.get(id);
  if (node === undefined) return null;
  const kids = graph.children[id] ?? [];
  const isCollapsed = collapsed.has(id);
  const hasKids = kids.length > 0;

  return (
    <li className="pf-plandocs-item" role="treeitem" aria-expanded={hasKids ? !isCollapsed : undefined}>
      <div
        className="pf-plandocs-card"
        data-kind={node.kind}
        data-selected={selectedId === id ? "yes" : undefined}
        data-fresh={fresh?.has(id) === true ? "yes" : undefined}
        data-severity={node.severity ?? ""}
      >
        <button
          type="button"
          className="pf-plandocs-toggle"
          aria-label={isCollapsed ? `Expand ${node.label}` : `Collapse ${node.label}`}
          disabled={!hasKids}
          onClick={() => onToggle(id)}
        >
          {hasKids ? isCollapsed ? <ChevronRight size={14} /> : <ChevronDown size={14} /> : <span className="pf-plandocs-spacer" />}
        </button>
        <button type="button" className="pf-plandocs-open" onClick={() => onSelect(id)}>
          <FileText size={14} aria-hidden="true" className="pf-plandocs-icon" />
          <span className="pf-plandocs-body">
            <span className="pf-plandocs-kind">{node.kind}</span>
            <span className="pf-plandocs-title">{node.label}</span>
            {node.detail !== "" && <span className="pf-plandocs-summary">{node.detail}</span>}
          </span>
          {node.severity !== undefined && (
            <span className="pf-plandocs-severity" title={`${node.severity} finding against this node`}>
              <CircleAlert size={12} aria-hidden="true" /> {node.severity}
            </span>
          )}
        </button>
      </div>
      {hasKids && !isCollapsed && (
        <ul className="pf-plandocs-children" role="group">
          {kids.map((childId) => (
            <PlanDoc
              key={childId}
              id={childId}
              byId={byId}
              graph={graph}
              fresh={fresh}
              collapsed={collapsed}
              selectedId={selectedId}
              onToggle={onToggle}
              onSelect={onSelect}
            />
          ))}
        </ul>
      )}
    </li>
  );
}

/** The selected node's own content, shown beside the list. */
function PlanNodeDetail(props: {
  node: PlanNode;
  graph: PlanGraph;
  parent: PlanNode | null;
}): React.ReactElement {
  const { node, graph, parent } = props;
  const kids = graph.children[node.id] ?? [];
  return (
    <aside className="pf-plandocs-detail" aria-label={`Details for ${node.label}`}>
      <header className="pf-plandocs-detail-head">
        <span className="pf-plandocs-kind">{node.kind}</span>
        <h3 className="pf-plandocs-detail-title">{node.label}</h3>
      </header>
      <dl className="pf-plandocs-facts">
        <dt>Id</dt>
        <dd className="pf-mono">{node.id}</dd>
        {parent !== null && (
          <>
            <dt>Parent</dt>
            <dd>{parent.label}</dd>
          </>
        )}
        <dt>Children</dt>
        <dd>{kids.length === 0 ? "none" : String(kids.length)}</dd>
        {node.severity !== undefined && (
          <>
            <dt>Worst finding</dt>
            <dd className="pf-plandocs-detail-severity">{node.severity}</dd>
          </>
        )}
      </dl>
      {node.detail !== "" ? (
        <p className="pf-plandocs-detail-text">{node.detail}</p>
      ) : (
        <p className="pf-caption">This node carries no summary of its own; its title and children are the whole entry.</p>
      )}
    </aside>
  );
}
