import type {
  BoardColumn,
  BoardDiscovery,
  BoardPriority,
  BoardTask,
  DiscoveryStatus,
} from "../types";

/** Fixed geometry (graph units). Layout is a pure function of these and the input. */
export const GRAPH_METRICS = {
  taskW: 212,
  taskH: 72,
  groupW: 188,
  groupH: 46,
  pathW: 188,
  pathH: 28,
  gapX: 60,
  /** Gap between wrapped sub-columns of one layer. */
  chunkGapX: 24,
  gapY: 16,
  pad: 56,
  /** A layer taller than this wraps into extra sub-columns so wide plans stay roughly square. */
  maxPerColumn: 10,
  sweeps: 3,
} as const;

export type GraphNodeKind = "task" | "ghost" | "path" | "group";
export type GraphEdgeKind = "depends" | "discovered" | "path" | "cycle";

export interface GraphNode {
  /** `t:<task id>`, `d:<discovery id>`, or `g:unplanned`. */
  key: string;
  kind: GraphNodeKind;
  taskID: string | null;
  discoveryID: string | null;
  label: string;
  /** Wrapped title lines for task and ghost nodes. */
  lines: string[];
  layer: number;
  x: number;
  y: number;
  w: number;
  h: number;
  task: BoardTask | null;
  discovery: BoardDiscovery | null;
  column: BoardColumn | null;
  priority: BoardPriority;
  blocked: boolean;
  active: boolean;
  awaiting: boolean;
  /** Accepted discovery that became an ordinary (dashed) task node. */
  discovered: boolean;
  discoveryStatus: DiscoveryStatus | null;
  /** Number of path discoveries under a `group` node. */
  count: number;
}

export interface GraphEdge {
  key: string;
  from: string;
  to: string;
  kind: GraphEdgeKind;
  /** SVG cubic path from the right edge of `from` to the left edge of `to`. */
  d: string;
}

export interface GraphLayout {
  nodes: GraphNode[];
  edges: GraphEdge[];
  byKey: Map<string, GraphNode>;
  width: number;
  height: number;
  layerCount: number;
}

export interface LayoutOptions {
  showDismissed: boolean;
  showPaths: boolean;
}

export const EMPTY_LAYOUT: GraphLayout = {
  nodes: [],
  edges: [],
  byKey: new Map(),
  width: 0,
  height: 0,
  layerCount: 0,
};

/** Greedy word wrap with an ellipsis on the last line. */
export function wrapLabel(text: string, maxChars: number, maxLines: number): string[] {
  const words = text.trim().split(/\s+/).filter((word) => word !== "");
  const lines: string[] = [];
  let current = "";
  let index = 0;
  for (; index < words.length; index += 1) {
    const word = words[index] ?? "";
    const candidate = current === "" ? word : `${current} ${word}`;
    if (candidate.length <= maxChars) {
      current = candidate;
      continue;
    }
    if (lines.length === maxLines - 1) break;
    if (current !== "") {
      lines.push(current);
      current = word;
    } else {
      current = word;
    }
    if (current.length > maxChars) {
      lines.push(`${current.slice(0, maxChars - 1)}…`);
      current = "";
      if (lines.length === maxLines) return lines;
    }
  }
  if (index < words.length) {
    const rest = [current, ...words.slice(index)].filter((part) => part !== "").join(" ");
    lines.push(rest.length > maxChars ? `${rest.slice(0, maxChars - 1)}…` : rest);
  } else if (current !== "") {
    lines.push(current);
  }
  return lines.slice(0, maxLines);
}

function round(value: number): number {
  return Math.round(value * 10) / 10;
}

function edgePath(from: GraphNode, to: GraphNode): string {
  const x1 = from.x + from.w;
  const y1 = from.y + from.h / 2;
  const x2 = to.x;
  const y2 = to.y + to.h / 2;
  const dx = Math.max(48, Math.abs(x2 - x1) * 0.5);
  return `M${String(round(x1))} ${String(round(y1))} C${String(round(x1 + dx))} ${String(round(y1))} ${String(round(x2 - dx))} ${String(round(y2))} ${String(round(x2))} ${String(round(y2))}`;
}

function blankNode(key: string, kind: GraphNodeKind): GraphNode {
  return {
    key,
    kind,
    taskID: null,
    discoveryID: null,
    label: "",
    lines: [],
    layer: 0,
    x: 0,
    y: 0,
    w: GRAPH_METRICS.taskW,
    h: GRAPH_METRICS.taskH,
    task: null,
    discovery: null,
    column: null,
    priority: "normal",
    blocked: false,
    active: false,
    awaiting: false,
    discovered: false,
    discoveryStatus: null,
    count: 0,
  };
}

interface RawEdge {
  from: string;
  to: string;
  kind: GraphEdgeKind;
}

/**
 * Layer = longest path from the roots over `depends_on`. Cycles are broken by a
 * depth-first walk in manifest order: an edge into a node still on the walk's
 * stack is a back-edge and is ignored for layering (and drawn as `cycle`).
 */
function layerTasks(tasks: readonly BoardTask[]): {
  layers: Map<string, number>;
  deps: Map<string, string[]>;
  back: Set<string>;
} {
  const known = new Set(tasks.map((task) => task.id));
  const deps = new Map<string, string[]>();
  for (const task of tasks) {
    const clean: string[] = [];
    for (const dep of task.depends_on) {
      if (dep !== task.id && known.has(dep) && !clean.includes(dep)) clean.push(dep);
    }
    deps.set(task.id, clean);
  }

  const visit = new Map<string, 1 | 2>();
  const layers = new Map<string, number>();
  const back = new Set<string>();
  for (const task of tasks) {
    if (visit.has(task.id)) continue;
    visit.set(task.id, 1);
    const stack: Array<{ id: string; next: number }> = [{ id: task.id, next: 0 }];
    while (stack.length > 0) {
      const frame = stack[stack.length - 1];
      if (frame === undefined) break;
      const list = deps.get(frame.id) ?? [];
      if (frame.next < list.length) {
        const dep = list[frame.next] ?? "";
        frame.next += 1;
        const seen = visit.get(dep);
        if (seen === 1) {
          back.add(`${dep}>${frame.id}`);
        } else if (seen === undefined) {
          visit.set(dep, 1);
          stack.push({ id: dep, next: 0 });
        }
        continue;
      }
      let layer = 0;
      for (const dep of list) {
        if (back.has(`${dep}>${frame.id}`)) continue;
        layer = Math.max(layer, (layers.get(dep) ?? -1) + 1);
      }
      layers.set(frame.id, layer);
      visit.set(frame.id, 2);
      stack.pop();
    }
  }
  return { layers, deps, back };
}

/** Barycenter crossing reduction: `sweeps` alternating down/up passes, stable on ties. */
function orderLayers(
  layerLists: string[][],
  preds: Map<string, string[]>,
  succs: Map<string, string[]>,
  sweeps: number,
): void {
  const rank = new Map<string, number>();
  const refresh = (list: string[]): void => {
    list.forEach((key, index) => rank.set(key, (index + 0.5) / list.length));
  };
  for (const list of layerLists) refresh(list);

  const sweepLayer = (list: string[], refs: Map<string, string[]>): void => {
    const scored = list.map((key, index) => {
      const neighbors = refs.get(key) ?? [];
      let bary = rank.get(key) ?? 0;
      if (neighbors.length > 0) {
        let sum = 0;
        for (const neighbor of neighbors) sum += rank.get(neighbor) ?? 0;
        bary = sum / neighbors.length;
      }
      return { key, index, bary };
    });
    scored.sort((left, right) => left.bary - right.bary || left.index - right.index);
    list.splice(0, list.length, ...scored.map((entry) => entry.key));
    refresh(list);
  };

  for (let sweep = 0; sweep < sweeps; sweep += 1) {
    if (sweep % 2 === 0) {
      for (let layer = 1; layer < layerLists.length; layer += 1) {
        sweepLayer(layerLists[layer] ?? [], preds);
      }
    } else {
      for (let layer = layerLists.length - 2; layer >= 0; layer -= 1) {
        sweepLayer(layerLists[layer] ?? [], succs);
      }
    }
  }
}

/**
 * Structural signature of everything `layoutGraph` and the node views read.
 * Timestamps, `live.at`/`detail` and `coordinator.updated_at` are left out on
 * purpose: a push that only moves them must not re-run the layout.
 */
export function graphSignature(
  tasks: readonly BoardTask[],
  discoveries: readonly BoardDiscovery[],
): string {
  return JSON.stringify([
    tasks.map((task) => [
      task.id,
      task.title,
      task.depends_on,
      task.column,
      task.priority,
      task.blocked,
      task.live.state,
      task.live.by,
      task.awaiting_verification,
      task.discovered,
    ]),
    discoveries.map((discovery) => [
      discovery.id,
      discovery.title,
      discovery.status,
      discovery.kind,
      discovery.parent_task_id,
      discovery.paths,
    ]),
  ]);
}

export function layoutGraph(
  tasks: readonly BoardTask[],
  discoveries: readonly BoardDiscovery[],
  options: LayoutOptions,
): GraphLayout {
  const metrics = GRAPH_METRICS;
  const { layers, deps, back } = layerTasks(tasks);
  const nodes = new Map<string, GraphNode>();
  const rawEdges: RawEdge[] = [];
  const taskNodeKey = (id: string): string => `t:${id}`;

  for (const task of tasks) {
    const node = blankNode(taskNodeKey(task.id), "task");
    node.taskID = task.id;
    node.label = task.title;
    node.lines = wrapLabel(task.title, 28, 2);
    node.layer = layers.get(task.id) ?? 0;
    node.task = task;
    node.column = task.column;
    node.priority = task.priority;
    node.blocked = task.blocked;
    node.active = task.live.state === "active" && task.column !== "done";
    node.awaiting = task.awaiting_verification;
    node.discovered = task.discovered;
    nodes.set(node.key, node);
  }
  for (const task of tasks) {
    for (const dep of deps.get(task.id) ?? []) {
      rawEdges.push({
        from: taskNodeKey(dep),
        to: taskNodeKey(task.id),
        kind: back.has(`${dep}>${task.id}`) ? "cycle" : task.discovered ? "discovered" : "depends",
      });
    }
  }

  // Discoveries. Accepted task discoveries that are already board tasks are the
  // ordinary (dashed) nodes above and are not repeated as ghosts.
  const ghosts: GraphNode[] = [];
  const pills: GraphNode[] = [];
  for (const discovery of discoveries) {
    if (discovery.status === "dismissed" && !options.showDismissed) continue;
    if (discovery.kind === "task") {
      if (nodes.has(taskNodeKey(discovery.id))) continue;
      const node = blankNode(`d:${discovery.id}`, "ghost");
      node.discoveryID = discovery.id;
      node.label = discovery.title;
      node.lines = wrapLabel(discovery.title, 28, 2);
      node.discovery = discovery;
      node.priority = discovery.priority;
      node.discoveryStatus = discovery.status;
      ghosts.push(node);
    } else {
      if (!options.showPaths) continue;
      const node = blankNode(`d:${discovery.id}`, "path");
      node.discoveryID = discovery.id;
      const first = discovery.paths[0] ?? discovery.title;
      node.label = discovery.paths.length > 1 ? `${first} +${String(discovery.paths.length - 1)}` : first;
      node.w = metrics.pathW;
      node.h = metrics.pathH;
      node.discovery = discovery;
      node.discoveryStatus = discovery.status;
      pills.push(node);
    }
  }

  let maxLayer = 0;
  for (const node of nodes.values()) maxLayer = Math.max(maxLayer, node.layer);
  const orphanGhosts: GraphNode[] = [];
  for (const ghost of ghosts) {
    const discovery = ghost.discovery;
    const parent =
      discovery !== null && discovery.parent_task_id !== ""
        ? nodes.get(taskNodeKey(discovery.parent_task_id))
        : undefined;
    nodes.set(ghost.key, ghost);
    if (parent === undefined) {
      orphanGhosts.push(ghost);
      continue;
    }
    ghost.layer = parent.layer + 1;
    maxLayer = Math.max(maxLayer, ghost.layer);
    rawEdges.push({ from: parent.key, to: ghost.key, kind: "discovered" });
  }
  const orphanPills: GraphNode[] = [];
  for (const pill of pills) {
    const discovery = pill.discovery;
    const parent =
      discovery !== null && discovery.parent_task_id !== ""
        ? nodes.get(taskNodeKey(discovery.parent_task_id))
        : undefined;
    nodes.set(pill.key, pill);
    if (parent === undefined) {
      orphanPills.push(pill);
      continue;
    }
    pill.layer = parent.layer + 1;
    maxLayer = Math.max(maxLayer, pill.layer);
    rawEdges.push({ from: parent.key, to: pill.key, kind: "path" });
  }
  if (orphanGhosts.length > 0 || orphanPills.length > 0) {
    const finalLayer = maxLayer + 1;
    for (const ghost of orphanGhosts) ghost.layer = finalLayer;
    if (orphanPills.length > 0) {
      const group = blankNode("g:unplanned", "group");
      group.label = "Unplanned paths";
      group.w = metrics.groupW;
      group.h = metrics.groupH;
      group.layer = finalLayer;
      group.count = orphanPills.length;
      nodes.set(group.key, group);
      for (const pill of orphanPills) {
        pill.layer = finalLayer + 1;
        rawEdges.push({ from: group.key, to: pill.key, kind: "path" });
      }
    }
  }

  // Layer lists, in a stable initial order (insertion order of `nodes`).
  let layerCount = 0;
  for (const node of nodes.values()) layerCount = Math.max(layerCount, node.layer + 1);
  const layerLists: string[][] = Array.from({ length: layerCount }, () => []);
  for (const node of nodes.values()) layerLists[node.layer]?.push(node.key);

  const preds = new Map<string, string[]>();
  const succs = new Map<string, string[]>();
  for (const edge of rawEdges) {
    if (edge.kind === "cycle") continue;
    const into = preds.get(edge.to);
    if (into === undefined) preds.set(edge.to, [edge.from]);
    else into.push(edge.from);
    const out = succs.get(edge.from);
    if (out === undefined) succs.set(edge.from, [edge.to]);
    else out.push(edge.to);
  }
  orderLayers(layerLists, preds, succs, metrics.sweeps);

  // Coordinates: wrap tall layers into sub-columns, then centre each column.
  const columns: GraphNode[][] = [];
  const columnGapAfter: number[] = [];
  for (const list of layerLists) {
    const members = list.map((key) => nodes.get(key)).filter((node): node is GraphNode => node !== undefined);
    for (let start = 0; start < members.length; start += metrics.maxPerColumn) {
      columns.push(members.slice(start, start + metrics.maxPerColumn));
      columnGapAfter.push(start + metrics.maxPerColumn >= members.length ? metrics.gapX : metrics.chunkGapX);
    }
  }
  const heights = columns.map((column) =>
    column.reduce((sum, node) => sum + node.h, 0) + Math.max(0, column.length - 1) * metrics.gapY,
  );
  const tallest = heights.reduce((max, value) => Math.max(max, value), 0);
  let cursorX: number = metrics.pad;
  columns.forEach((column, index) => {
    const width = column.reduce((max, node) => Math.max(max, node.w), 0);
    let y = metrics.pad + (tallest - (heights[index] ?? 0)) / 2;
    for (const node of column) {
      node.x = cursorX;
      node.y = round(y);
      y += node.h + metrics.gapY;
    }
    cursorX += width + (columnGapAfter[index] ?? metrics.gapX);
  });
  const width = columns.length === 0 ? 0 : cursorX - (columnGapAfter[columns.length - 1] ?? 0) + metrics.pad;
  const height = columns.length === 0 ? 0 : tallest + metrics.pad * 2;

  const edges: GraphEdge[] = [];
  for (const edge of rawEdges) {
    const from = nodes.get(edge.from);
    const to = nodes.get(edge.to);
    if (from === undefined || to === undefined) continue;
    edges.push({ key: `${edge.from}>${edge.to}`, from: edge.from, to: edge.to, kind: edge.kind, d: edgePath(from, to) });
  }

  const ordered: GraphNode[] = [];
  for (const list of layerLists) {
    for (const key of list) {
      const node = nodes.get(key);
      if (node !== undefined) ordered.push(node);
    }
  }
  return { nodes: ordered, edges, byKey: nodes, width, height, layerCount };
}

/** Transitive upstream and downstream nodes and edges of `key` (hover highlight). */
export function graphNeighborhood(
  layout: GraphLayout,
  key: string,
): { nodes: Set<string>; edges: Set<string> } {
  const incoming = new Map<string, GraphEdge[]>();
  const outgoing = new Map<string, GraphEdge[]>();
  for (const edge of layout.edges) {
    const into = incoming.get(edge.to);
    if (into === undefined) incoming.set(edge.to, [edge]);
    else into.push(edge);
    const out = outgoing.get(edge.from);
    if (out === undefined) outgoing.set(edge.from, [edge]);
    else out.push(edge);
  }
  const nodeSet = new Set<string>([key]);
  const edgeSet = new Set<string>();
  const walk = (adjacent: Map<string, GraphEdge[]>, pick: (edge: GraphEdge) => string): void => {
    const queue = [key];
    const seen = new Set<string>([key]);
    for (let head = 0; head < queue.length; head += 1) {
      const current = queue[head] ?? "";
      for (const edge of adjacent.get(current) ?? []) {
        edgeSet.add(edge.key);
        const next = pick(edge);
        nodeSet.add(next);
        if (!seen.has(next)) {
          seen.add(next);
          queue.push(next);
        }
      }
    }
  };
  walk(incoming, (edge) => edge.from);
  walk(outgoing, (edge) => edge.to);
  return { nodes: nodeSet, edges: edgeSet };
}
