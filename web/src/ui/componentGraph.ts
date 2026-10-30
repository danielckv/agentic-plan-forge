import type { ComponentRef, GraphEdge, GraphNode, GraphView } from "../protocol/api";

/**
 * Pure logic for the 2D component graph overlay.
 *
 * The overlay is a big, readable diagram, not a third renderer: nodes are laid
 * out on concentric rings by hop count, so the focus sits in the middle and the
 * repository spreads outward. Everything here is deterministic and testable;
 * the component only draws what these functions return.
 */

/** One node as it is drawn: ring by depth, angle by position within the ring. */
export interface PlacedGraphNode {
  node: GraphNode;
  x: number;
  y: number;
  /** Ring index: 0 is the focus. */
  ring: number;
}

/** A laid-out edge, referencing the placed endpoints. */
export interface PlacedGraphEdge {
  from: PlacedGraphNode;
  to: PlacedGraphNode;
  kind: "imports" | "contains";
}

export interface GraphLayout {
  nodes: PlacedGraphNode[];
  edges: PlacedGraphEdge[];
  /** The space the drawing occupies, so the viewBox can fit it. */
  width: number;
  height: number;
  /** Centre, where the focus node sits. */
  cx: number;
  cy: number;
}

/** Distance between rings, in view units. */
const RING_GAP = 170;

/** Room around the outermost ring, so labels are not clipped. */
const PADDING = 90;

/**
 * Places the nodes returned by GET /api/component-graph. Radius grows with the
 * hop count, so the structure is readable: the node you asked about is central
 * and what it depends on is further out.
 */
export function layoutGraph(view: GraphView): GraphLayout {
  const rings = new Map<number, GraphNode[]>();
  for (const node of view.nodes) {
    const ring = Math.max(0, Math.min(4, node.depth));
    const list = rings.get(ring);
    if (list === undefined) rings.set(ring, [node]);
    else list.push(node);
  }
  // A stable order inside a ring: modules first, then by path, so two requests
  // for the same graph draw the same picture.
  for (const list of rings.values()) {
    list.sort((a, b) => {
      if (a.kind !== b.kind) return a.kind === "module" ? -1 : 1;
      return a.path < b.path ? -1 : a.path > b.path ? 1 : 0;
    });
  }
  const maxRing = rings.size === 0 ? 0 : Math.max(...[...rings.keys()]);
  const extent = maxRing * RING_GAP + PADDING * 2;
  const cx = extent / 2;
  const cy = extent / 2;
  const placed: PlacedGraphNode[] = [];
  const byId = new Map<string, PlacedGraphNode>();
  for (const [ring, list] of [...rings.entries()].sort((a, b) => a[0] - b[0])) {
    const radius = ring * RING_GAP;
    list.forEach((node, i) => {
      // Even spacing around the ring; a single node sits at the centre.
      const angle = list.length === 1 ? -Math.PI / 2 : (2 * Math.PI * i) / list.length - Math.PI / 2;
      const p: PlacedGraphNode = {
        node,
        x: Math.round(cx + Math.cos(angle) * radius),
        y: Math.round(cy + Math.sin(angle) * radius),
        ring,
      };
      placed.push(p);
      byId.set(node.id, p);
    });
  }
  const edges: PlacedGraphEdge[] = [];
  for (const edge of view.edges) {
    const from = byId.get(edge.from);
    const to = byId.get(edge.to);
    // An edge whose endpoint the walk did not include is not drawn: a line to
    // nowhere would read as a relationship that is not in the picture.
    if (from === undefined || to === undefined || from === to) continue;
    edges.push({ from, to, kind: edge.kind === "contains" ? "contains" : "imports" });
  }
  return {
    nodes: placed,
    edges,
    width: Math.max(extent, RING_GAP * 2 + PADDING * 2),
    height: Math.max(extent, RING_GAP * 2 + PADDING * 2),
    cx,
    cy,
  };
}

/** The context panel text for one node: what it is, where, and how big. */
export interface NodeContext {
  title: string;
  subtitle: string;
  rows: { label: string; value: string }[];
}

/**
 * Describes one node for the click-through panel. A module and a file answer
 * different questions, so the rows differ; both name the path, because that is
 * what a reader takes to their editor.
 */
export function nodeContext(
  node: GraphNode,
  neighbours: { imports: string[]; contained: string[] },
): NodeContext {
  const rows: { label: string; value: string }[] = [
    { label: "Path", value: node.path === "" ? "." : node.path },
    { label: "Kind", value: node.kind === "module" ? "module (directory)" : "file" },
  ];
  if (node.language !== undefined && node.language !== "") rows.push({ label: "Language", value: node.language });
  rows.push({ label: "Declarations", value: String(node.symbols) });
  rows.push({ label: "Hops from focus", value: String(node.depth) });
  if (neighbours.imports.length > 0) {
    rows.push({ label: "Imports", value: neighbours.imports.join(", ") });
  }
  if (neighbours.contained.length > 0) {
    rows.push({ label: "Contains", value: neighbours.contained.join(", ") });
  }
  return {
    title: node.label === "" ? node.path : node.label,
    subtitle: node.kind === "module" ? "A directory of the repository" : "A file in the snapshot",
    rows,
  };
}

/** The neighbour names of one node, for the context panel. */
export function neighboursOf(node: GraphNode, view: GraphView): { imports: string[]; incoming: string[] } {
  const byId = new Map(view.nodes.map((n) => [n.id, n]));
  const imports: string[] = [];
  const incoming: string[] = [];
  for (const edge of view.edges) {
    if (edge.from === node.id) {
      const target = byId.get(edge.to);
      if (target !== undefined && edge.kind === "imports") imports.push(target.label);
    }
    if (edge.to === node.id) {
      const source = byId.get(edge.from);
      if (source !== undefined && edge.kind === "imports") incoming.push(source.label);
    }
  }
  return { imports: [...new Set(imports)].sort(), incoming: [...new Set(incoming)].sort() };
}

/** A short summary line for the overlay header. */
export function graphSummary(view: GraphView): string {
  if (!view.ready) return "No repository snapshot yet. Scan the codebase first.";
  if (view.nodes.length === 0) return "No components match this focus.";
  const modules = view.nodes.filter((n) => n.kind === "module").length;
  const files = view.nodes.length - modules;
  const parts = [`${String(modules)} module${modules === 1 ? "" : "s"}`, `${String(files)} file${files === 1 ? "" : "s"}`];
  if (view.truncated) {
    parts.push(`truncated at ${String(view.truncated_at ?? view.depth)} hops`);
  }
  return parts.join(" · ");
}

/**
 * Nodes referenced by a component from the existing browser, so the Codebase
 * tab can open the graph centred on what the reader was looking at.
 */
export function focusIdForComponent(ref: ComponentRef): string {
  return ref.path === "" ? "" : `file:${ref.path}`;
}

/** The focus id for a module row of the component browser. */
export function focusIdForModule(path: string): string {
  return path === "" ? "module:" : `module:${path}`;
}

/** Types re-exported so the component imports from one place. */
export type { ComponentRef, GraphEdge, GraphNode, GraphView };
