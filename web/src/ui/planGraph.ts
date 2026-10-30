/**
 * The plan as a tree of nodes, and where each one sits in space.
 *
 * Everything here is pure. The renderer owns a WebGL context and cannot be
 * tested — jsdom has no WebGL at all — so the parts worth being sure about
 * live here instead: what the tree contains, which nodes are new since the
 * last draft, and where each node is placed. The same functions feed the
 * fallback list view, so a machine without WebGL shows the same plan rather
 * than an empty page.
 *
 * This follows `ui/room.ts`: behaviour out of the component, into a module
 * that a test can call directly.
 */

import type { ArchitectDraft, PMDraft } from "../protocol/drafts";
import type { CritiqueIssue } from "../store/syncStore";

/** How deep in the plan a node sits. The order is the nesting order. */
export type PlanNodeKind = "epic" | "story" | "workstream" | "task";

export interface PlanNode {
  id: string;
  kind: PlanNodeKind;
  /** The node this hangs from; null for a root. */
  parentId: string | null;
  label: string;
  /** One line for the inspector, never the whole document. */
  detail: string;
  /** 0 for a root, 1 for its children, and so on. */
  depth: number;
  /** The worst severity of any finding naming this node. */
  severity?: string;
}

export interface PlanGraph {
  nodes: PlanNode[];
  /** Child ids by parent id. Roots are under the empty key. */
  children: Record<string, string[]>;
}

const SEVERITY_RANK: Record<string, number> = { CRITICAL: 3, HIGH: 2, MEDIUM: 1 };

/**
 * worstSeverity keeps the highest severity naming a node. A node with both a
 * MEDIUM and a CRITICAL against it is a CRITICAL node; showing the first one
 * found would make the badge depend on arrival order.
 */
function worstSeverity(current: string | undefined, next: string): string {
  if (current === undefined) return next;
  return (SEVERITY_RANK[next] ?? 0) > (SEVERITY_RANK[current] ?? 0) ? next : current;
}

/**
 * buildPlanGraph assembles the tree from whatever has been drafted so far.
 *
 * A run reveals the plan in layers: the Product Manager's epics and stories
 * arrive first, the architect's workstreams next, and tasks only once the
 * specialists have drafted. A missing layer is simply absent — the tree is
 * always well formed, and grows as the drafts land.
 */
export function buildPlanGraph(
  pm: PMDraft | null,
  architect: ArchitectDraft | null,
  critiques: CritiqueIssue[] = [],
): PlanGraph {
  const nodes: PlanNode[] = [];
  const known = new Set<string>();

  const push = (node: PlanNode): void => {
    // A duplicate id would give a node two places in the tree and two boxes on
    // screen. First definition wins, which keeps the layout stable.
    if (node.id === "" || known.has(node.id)) return;
    known.add(node.id);
    nodes.push(node);
  };

  for (const epic of pm?.epics ?? []) {
    push({ id: epic.id, kind: "epic", parentId: null, label: epic.title || epic.id, detail: epic.goal, depth: 0 });
  }
  for (const story of pm?.stories ?? []) {
    // A story whose epic was never drafted still belongs on the board; it
    // hangs from the root rather than vanishing.
    const parent = known.has(story.epicId) ? story.epicId : null;
    push({
      id: story.id,
      kind: "story",
      parentId: parent,
      label: story.need || story.id,
      detail: story.actor === "" ? "" : `As ${story.actor}`,
      depth: parent === null ? 0 : 1,
    });
  }
  for (const ws of architect?.workstreams ?? []) {
    push({
      id: ws.id,
      kind: "workstream",
      parentId: null,
      label: ws.id,
      detail: ws.modules.join(", "),
      depth: 0,
    });
  }

  const bySeverity = new Map<string, string>();
  for (const c of critiques) {
    for (const target of c.targets ?? []) {
      bySeverity.set(target, worstSeverity(bySeverity.get(target), c.severity));
    }
  }
  for (const node of nodes) {
    const severity = bySeverity.get(node.id);
    if (severity !== undefined) node.severity = severity;
  }

  const children: Record<string, string[]> = {};
  for (const node of nodes) {
    const key = node.parentId ?? "";
    (children[key] ??= []).push(node.id);
  }
  return { nodes, children };
}

/**
 * newNodeIds returns the ids present in `next` and absent from `prev`.
 *
 * Drafts arrive whole — a frame carries every epic at once, replacing the one
 * before it — so "a node appeared" is a comparison, not an event. This is what
 * lets the view animate growth without the backend having to announce each
 * entity separately.
 */
export function newNodeIds(prev: PlanGraph | null, next: PlanGraph): string[] {
  if (prev === null) return next.nodes.map((n) => n.id);
  const before = new Set(prev.nodes.map((n) => n.id));
  return next.nodes.filter((n) => !before.has(n.id)).map((n) => n.id);
}

export interface PlacedNode extends PlanNode {
  x: number;
  y: number;
  z: number;
}

/** Spacing of the layout, in scene units. */
const RING_RADIUS = 6;
const CHILD_RADIUS = 2.6;
const DEPTH_DROP = 3.2;

/**
 * layoutPlanGraph places every node, clustered by root.
 *
 * Roots are spread evenly around a circle and each subtree is a smaller circle
 * under its parent, one step down per level. The result depends only on the
 * tree, so the same plan always draws the same shape and a node does not jump
 * when a sibling arrives after it — a node already on screen keeps its angle
 * as long as its position among its siblings is unchanged.
 */
export function layoutPlanGraph(graph: PlanGraph): PlacedNode[] {
  const byId = new Map(graph.nodes.map((n) => [n.id, n]));
  const placed: PlacedNode[] = [];

  const place = (id: string, x: number, z: number, depth: number): void => {
    const node = byId.get(id);
    if (node === undefined) return;
    // `-depth * DEPTH_DROP` is negative zero at the root, which compares
    // unequal to 0 under Object.is and would survive into serialized state.
    placed.push({ ...node, x, y: depth === 0 ? 0 : -depth * DEPTH_DROP, z });
    const kids = graph.children[id] ?? [];
    const radius = CHILD_RADIUS * Math.max(1, Math.sqrt(kids.length));
    kids.forEach((childId, i) => {
      const angle = (2 * Math.PI * i) / Math.max(1, kids.length);
      place(childId, x + radius * Math.cos(angle), z + radius * Math.sin(angle), depth + 1);
    });
  };

  const roots = graph.children[""] ?? [];
  const radius = RING_RADIUS * Math.max(1, Math.sqrt(roots.length) / 2);
  roots.forEach((id, i) => {
    const angle = (2 * Math.PI * i) / Math.max(1, roots.length);
    place(id, radius * Math.cos(angle), radius * Math.sin(angle), 0);
  });
  return placed;
}

/** countByKind reports how much of each layer the plan has so far. */
export function countByKind(graph: PlanGraph): Record<PlanNodeKind, number> {
  const out: Record<PlanNodeKind, number> = { epic: 0, story: 0, workstream: 0, task: 0 };
  for (const node of graph.nodes) out[node.kind] += 1;
  return out;
}
