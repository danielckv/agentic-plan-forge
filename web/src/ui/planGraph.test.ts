import { describe, expect, it } from "vitest";

import { buildPlanGraph, countByKind, layoutPlanGraph, newNodeIds } from "./planGraph";
import type { ArchitectDraft, Finding, PMDraft } from "../protocol/drafts";

function pm(overrides: Partial<PMDraft> = {}): PMDraft {
  return {
    epics: [
      { id: "E1", title: "Checkout", goal: "Cut abandonment to under 20%" },
      { id: "E2", title: "Reporting", goal: "" },
    ],
    stories: [
      { id: "S1", epicId: "E1", actor: "shopper", need: "pay with one tap", benefit: "finish faster", criteria: [] },
      { id: "S2", epicId: "E1", actor: "shopper", need: "see the total", benefit: "trust it", criteria: [] },
    ],
    ...overrides,
  };
}

function architect(overrides: Partial<ArchitectDraft> = {}): ArchitectDraft {
  return {
    summary: "",
    constraints: [],
    workstreams: [{ id: "ws-pay", role: "backend", modules: ["internal/pay"], dependsOn: [] }],
    nodes: [],
    edges: [],
    ...overrides,
  };
}

function finding(severity: Finding["severity"], targets: string[]): Finding {
  return { issueId: `C-${severity}`, severity, summary: "", requiredChange: "", targets, resolved: false };
}

describe("buildPlanGraph", () => {
  it("nests stories under their epic", () => {
    const graph = buildPlanGraph(pm(), null);
    expect(graph.children[""]).toEqual(["E1", "E2", "ws-pay"].slice(0, 2));
    expect(graph.children["E1"]).toEqual(["S1", "S2"]);
    expect(graph.nodes.find((n) => n.id === "S1")?.depth).toBe(1);
  });

  // A run reveals the plan in layers. A view that required every layer would
  // show nothing until the specialists finished.
  it("builds a tree from whatever has been drafted so far", () => {
    expect(buildPlanGraph(null, null).nodes).toEqual([]);
    expect(countByKind(buildPlanGraph(pm(), null))).toMatchObject({ epic: 2, story: 2, workstream: 0 });
    expect(countByKind(buildPlanGraph(pm(), architect()))).toMatchObject({ epic: 2, story: 2, workstream: 1 });
  });

  // Losing a story because its epic has not arrived would hide real work.
  it("keeps a story whose epic is missing, at the root", () => {
    const graph = buildPlanGraph(pm({ epics: [] }), null);
    expect(graph.nodes.map((n) => n.id)).toContain("S1");
    expect(graph.nodes.find((n) => n.id === "S1")?.parentId).toBeNull();
  });

  it("ignores a duplicate id rather than drawing it twice", () => {
    const graph = buildPlanGraph(pm({ epics: [{ id: "E1", title: "A", goal: "" }, { id: "E1", title: "B", goal: "" }] }), null);
    expect(graph.nodes.filter((n) => n.id === "E1")).toHaveLength(1);
    expect(graph.nodes.find((n) => n.id === "E1")?.label).toBe("A");
  });

  // Showing whichever finding arrived first would make the badge depend on
  // ordering rather than on severity.
  it("badges a node with the worst severity against it", () => {
    const graph = buildPlanGraph(pm(), null, [finding("MEDIUM", ["E1"]), finding("CRITICAL", ["E1"])]);
    expect(graph.nodes.find((n) => n.id === "E1")?.severity).toBe("CRITICAL");
    expect(graph.nodes.find((n) => n.id === "E2")?.severity).toBeUndefined();
  });
});

describe("newNodeIds", () => {
  // Drafts arrive whole and replace the one before, so "a node appeared" is a
  // comparison. This is what animates growth without a per-entity event.
  it("reports only what is new", () => {
    const before = buildPlanGraph(pm({ stories: [] }), null);
    const after = buildPlanGraph(pm(), null);
    expect(newNodeIds(before, after)).toEqual(["S1", "S2"]);
    expect(newNodeIds(after, after)).toEqual([]);
  });

  it("treats a first draft as entirely new", () => {
    const graph = buildPlanGraph(pm(), null);
    expect(newNodeIds(null, graph)).toHaveLength(graph.nodes.length);
  });
});

describe("layoutPlanGraph", () => {
  it("places every node once", () => {
    const graph = buildPlanGraph(pm(), architect());
    const placed = layoutPlanGraph(graph);
    expect(placed).toHaveLength(graph.nodes.length);
    expect(new Set(placed.map((p) => p.id)).size).toBe(graph.nodes.length);
  });

  it("drops each level below its parent", () => {
    const placed = layoutPlanGraph(buildPlanGraph(pm(), null));
    const epic = placed.find((p) => p.id === "E1");
    const story = placed.find((p) => p.id === "S1");
    expect(epic?.y).toBe(0);
    expect(story?.y).toBeLessThan(epic?.y ?? 0);
  });

  // The same plan must always draw the same shape, or the view churns on every
  // refresh and a node the owner was reading moves under the cursor.
  it("is deterministic", () => {
    const graph = buildPlanGraph(pm(), architect());
    expect(layoutPlanGraph(graph)).toEqual(layoutPlanGraph(graph));
  });

  // A node already on screen keeps its place when a later sibling arrives.
  it("does not move an existing node when a sibling is added after it", () => {
    const before = layoutPlanGraph(buildPlanGraph(pm({ stories: [pm().stories[0]!] }), null));
    const after = layoutPlanGraph(buildPlanGraph(pm(), null));
    expect(after.find((p) => p.id === "E1")).toMatchObject({ x: before.find((p) => p.id === "E1")!.x });
  });

  it("handles an empty plan", () => {
    expect(layoutPlanGraph(buildPlanGraph(null, null))).toEqual([]);
  });
});
