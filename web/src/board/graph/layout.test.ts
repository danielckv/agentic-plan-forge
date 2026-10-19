import { describe, expect, it } from "vitest";

import type { BoardDiscovery, BoardTask } from "../types";
import { EMPTY_LIVE } from "../types";
import { graphNeighborhood, graphSignature, layoutGraph, wrapLabel } from "./layout";
import type { GraphLayout } from "./layout";

const OPTIONS = { showDismissed: false, showPaths: true };

function task(id: string, dependsOn: string[] = [], extra: Partial<BoardTask> = {}): BoardTask {
  return {
    id,
    epic_id: "E-1",
    story_id: "S-1",
    workstream_id: "W-1",
    title: `Task ${id} with a reasonably long descriptive title`,
    column: "todo",
    blocked: false,
    depends_on: dependsOn,
    priority: "normal",
    targets: [],
    verification: { total: 0, passed: 0 },
    acceptance_criteria: [],
    human_estimate: { likely: 1, basis: "" },
    agent_estimate: { likely: 1, basis: "" },
    notes: [],
    live: EMPTY_LIVE,
    awaiting_verification: false,
    discovered: false,
    ...extra,
  };
}

function discovery(id: string, extra: Partial<BoardDiscovery> = {}): BoardDiscovery {
  return {
    id,
    kind: "task",
    title: `Discovered ${id}`,
    paths: [],
    parent_task_id: "",
    reason: "",
    by: "claude:abc",
    at: "2026-10-19T12:00:00Z",
    status: "proposed",
    priority: "normal",
    ...extra,
  };
}

function layerOf(layout: GraphLayout, key: string): number {
  const node = layout.byKey.get(key);
  if (node === undefined) throw new Error(`missing node ${key}`);
  return node.layer;
}

describe("layoutGraph layering", () => {
  it("uses the longest path from the roots as the layer", () => {
    const layout = layoutGraph(
      [task("A"), task("B", ["A"]), task("C", ["A", "B"]), task("D", ["C"]), task("E")],
      [],
      OPTIONS,
    );
    expect(layerOf(layout, "t:A")).toBe(0);
    expect(layerOf(layout, "t:E")).toBe(0);
    expect(layerOf(layout, "t:B")).toBe(1);
    // C depends on both A (layer 0) and B (layer 1): longest path wins.
    expect(layerOf(layout, "t:C")).toBe(2);
    expect(layerOf(layout, "t:D")).toBe(3);
    expect(layout.layerCount).toBe(4);
  });

  it("ignores dependencies on unknown tasks and self-loops", () => {
    const layout = layoutGraph([task("A", ["A", "ghost"]), task("B", ["A"])], [], OPTIONS);
    expect(layerOf(layout, "t:A")).toBe(0);
    expect(layerOf(layout, "t:B")).toBe(1);
    expect(layout.edges.map((edge) => edge.key)).toEqual(["t:A>t:B"]);
  });

  it("places layers left to right with fixed sizes and no overlap", () => {
    const layout = layoutGraph([task("A"), task("B", ["A"]), task("C", ["A"])], [], OPTIONS);
    const a = layout.byKey.get("t:A");
    const b = layout.byKey.get("t:B");
    const c = layout.byKey.get("t:C");
    if (a === undefined || b === undefined || c === undefined) throw new Error("missing node");
    expect(b.x).toBeGreaterThan(a.x + a.w);
    expect(b.x).toBe(c.x);
    expect(Math.abs(b.y - c.y)).toBeGreaterThanOrEqual(b.h);
    expect(layout.width).toBeGreaterThan(c.x + c.w);
    expect(layout.height).toBeGreaterThan(0);
  });

  it("returns an empty layout for an empty plan", () => {
    const layout = layoutGraph([], [], OPTIONS);
    expect(layout.nodes).toEqual([]);
    expect(layout.width).toBe(0);
  });
});

describe("layoutGraph cycle safety", () => {
  it("terminates on a cycle, breaks it deterministically, and keeps the back-edge as `cycle`", () => {
    const tasks = [task("A", ["C"]), task("B", ["A"]), task("C", ["B"])];
    const first = layoutGraph(tasks, [], OPTIONS);
    const second = layoutGraph(tasks, [], OPTIONS);
    expect(first.nodes.map((node) => [node.key, node.layer])).toEqual(
      second.nodes.map((node) => [node.key, node.layer]),
    );
    // DFS from A follows A->C->B->A: the edge A depends-on C is not the back edge;
    // B->A closes the loop, so the edge A>B is the one ignored.
    const kinds = Object.fromEntries(first.edges.map((edge) => [edge.key, edge.kind]));
    expect(Object.values(kinds).filter((kind) => kind === "cycle")).toHaveLength(1);
    expect(kinds["t:A>t:B"]).toBe("cycle");
    expect(layerOf(first, "t:B")).toBe(0);
    expect(layerOf(first, "t:C")).toBe(1);
    expect(layerOf(first, "t:A")).toBe(2);
  });

  it("handles a self-contained 2-cycle plus an unrelated chain", () => {
    const layout = layoutGraph([task("X", ["Y"]), task("Y", ["X"]), task("P"), task("Q", ["P"])], [], OPTIONS);
    expect(layout.nodes).toHaveLength(4);
    expect(layerOf(layout, "t:Q")).toBe(1);
  });
});

describe("layoutGraph barycenter ordering", () => {
  it("orders a layer by the average position of its parents to remove a crossing", () => {
    // Manifest order puts X before Y, but X hangs off B and Y off A.
    const layout = layoutGraph([task("A"), task("B"), task("X", ["B"]), task("Y", ["A"])], [], OPTIONS);
    const y = layout.byKey.get("t:Y");
    const x = layout.byKey.get("t:X");
    if (x === undefined || y === undefined) throw new Error("missing node");
    expect(y.y).toBeLessThan(x.y);
  });

  it("is deterministic across runs and across a fresh copy of the input", () => {
    const tasks = Array.from({ length: 40 }, (_, index) =>
      task(`T-${String(index)}`, index < 4 ? [] : [`T-${String((index * 7) % index)}`, `T-${String((index * 3) % index)}`]),
    );
    const one = layoutGraph(tasks, [], OPTIONS);
    const two = layoutGraph(structuredClone(tasks), [], OPTIONS);
    const pick = (layout: GraphLayout) => layout.nodes.map((node) => [node.key, node.x, node.y]);
    expect(pick(one)).toEqual(pick(two));
    expect(one.edges.map((edge) => edge.d)).toEqual(two.edges.map((edge) => edge.d));
  });
});

describe("layoutGraph discoveries", () => {
  const tasks = [task("T-1"), task("T-2", ["T-1"])];

  it("attaches ghost task discoveries next to their parent and orphans to a final layer", () => {
    const layout = layoutGraph(
      tasks,
      [discovery("D-1", { parent_task_id: "T-1" }), discovery("D-2")],
      OPTIONS,
    );
    const ghost = layout.byKey.get("d:D-1");
    const orphan = layout.byKey.get("d:D-2");
    expect(ghost?.kind).toBe("ghost");
    expect(ghost?.layer).toBe(1);
    expect(layout.edges.some((edge) => edge.key === "t:T-1>d:D-1" && edge.kind === "discovered")).toBe(true);
    expect(orphan?.layer).toBe(layerOf(layout, "t:T-2") + 1);
  });

  it("renders accepted discovered tasks as ordinary nodes, not ghosts", () => {
    const accepted = task("D-3", ["T-1"], { discovered: true });
    const layout = layoutGraph(
      [...tasks, accepted],
      [discovery("D-3", { status: "accepted", parent_task_id: "T-1" })],
      OPTIONS,
    );
    expect(layout.byKey.get("t:D-3")?.kind).toBe("task");
    expect(layout.byKey.get("t:D-3")?.discovered).toBe(true);
    expect(layout.byKey.has("d:D-3")).toBe(false);
    expect(layout.edges.find((edge) => edge.key === "t:T-1>t:D-3")?.kind).toBe("discovered");
  });

  it("hides dismissed discoveries unless asked, and paths unless enabled", () => {
    const found = [
      discovery("D-4", { status: "dismissed" }),
      discovery("D-5", { kind: "path", paths: ["a/b.go"], parent_task_id: "T-2" }),
      discovery("D-6", { kind: "path", paths: ["c/d.go", "c/e.go"] }),
    ];
    const hidden = layoutGraph(tasks, found, { showDismissed: false, showPaths: false });
    expect(hidden.byKey.has("d:D-4")).toBe(false);
    expect(hidden.byKey.has("d:D-5")).toBe(false);
    expect(hidden.byKey.has("g:unplanned")).toBe(false);

    const shown = layoutGraph(tasks, found, { showDismissed: true, showPaths: true });
    expect(shown.byKey.get("d:D-4")?.discoveryStatus).toBe("dismissed");
    expect(shown.byKey.get("d:D-5")?.kind).toBe("path");
    expect(shown.edges.some((edge) => edge.key === "t:T-2>d:D-5" && edge.kind === "path")).toBe(true);
    // Orphan path groups under a synthetic node, one layer further right.
    const group = shown.byKey.get("g:unplanned");
    expect(group?.count).toBe(1);
    expect(shown.byKey.get("d:D-6")?.layer).toBe((group?.layer ?? -1) + 1);
    expect(shown.byKey.get("d:D-6")?.label).toBe("c/d.go +1");
  });
});

describe("layoutGraph scale and helpers", () => {
  it("lays out 150 nodes in under 50 ms", () => {
    const tasks = Array.from({ length: 150 }, (_, index) => {
      const deps: string[] = [];
      if (index > 0) deps.push(`T-${String(Math.floor(index / 2))}`);
      if (index > 10) deps.push(`T-${String(index - 7)}`);
      return task(`T-${String(index)}`, deps);
    });
    layoutGraph(tasks, [], OPTIONS); // warm the JIT, then measure
    const started = performance.now();
    const layout = layoutGraph(tasks, [], OPTIONS);
    const elapsed = performance.now() - started;
    expect(layout.nodes).toHaveLength(150);
    expect(elapsed).toBeLessThan(50);
  });

  it("wraps long titles onto at most two lines with an ellipsis", () => {
    expect(wrapLabel("Short", 30, 2)).toEqual(["Short"]);
    const lines = wrapLabel("Expose the authenticated board snapshot endpoint for every consumer", 30, 2);
    expect(lines).toHaveLength(2);
    expect(lines[1]?.endsWith("…")).toBe(true);
    expect(lines.every((line) => line.length <= 30)).toBe(true);
  });

  it("finds transitive upstream and downstream edges for hover highlighting", () => {
    const layout = layoutGraph([task("A"), task("B", ["A"]), task("C", ["B"]), task("Z")], [], OPTIONS);
    const around = graphNeighborhood(layout, "t:B");
    expect([...around.nodes].sort()).toEqual(["t:A", "t:B", "t:C"]);
    expect([...around.edges].sort()).toEqual(["t:A>t:B", "t:B>t:C"]);
  });
});

describe("graphSignature", () => {
  const active = (at: string, detail: string) =>
    ({ state: "active", by: "claude:abc", source: "file_edit", detail, at }) as const;

  it("is stable when only timestamps and activity detail change", () => {
    const before = [task("A", [], { live: active("2026-10-19T11:00:00Z", "a.go") }), task("B", ["A"])];
    const after = [task("A", [], { live: active("2026-10-19T11:05:00Z", "b.go") }), task("B", ["A"])];
    const found = [discovery("D-1", { at: "2026-10-19T11:00:00Z" })];
    const foundLater = [discovery("D-1", { at: "2026-10-19T11:09:00Z" })];
    expect(graphSignature(after, foundLater)).toBe(graphSignature(before, found));
  });

  it("changes when a column, a dependency, or a discovery status changes", () => {
    const base = graphSignature([task("A"), task("B", ["A"])], [discovery("D-1")]);
    expect(graphSignature([task("A", [], { column: "done" }), task("B", ["A"])], [discovery("D-1")])).not.toBe(base);
    expect(graphSignature([task("A"), task("B")], [discovery("D-1")])).not.toBe(base);
    expect(graphSignature([task("A"), task("B", ["A"])], [discovery("D-1", { status: "accepted" })])).not.toBe(base);
  });

  it("does not mark a done task as active in the layout", () => {
    const live = active("2026-10-19T11:00:00Z", "a.go");
    const layout = layoutGraph([task("A", [], { column: "done", live }), task("B", [], { live })], [], OPTIONS);
    expect(layout.byKey.get("t:A")?.active).toBe(false);
    expect(layout.byKey.get("t:B")?.active).toBe(true);
  });
});
