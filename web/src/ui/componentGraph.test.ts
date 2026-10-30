import { describe, expect, it } from "vitest";

import type { GraphView } from "../protocol/api";
import { focusIdForComponent, focusIdForModule, graphSummary, layoutGraph, neighboursOf, nodeContext } from "./componentGraph";

function view(over: Partial<GraphView> = {}): GraphView {
  return {
    ready: true,
    stack: ["go"],
    depth: 2,
    truncated: false,
    nodes: [
      { id: "file:internal/a/x.go", kind: "file", label: "x.go", path: "internal/a/x.go", language: "go", symbols: 3, depth: 0 },
      { id: "module:internal/a", kind: "module", label: "internal/a", path: "internal/a", symbols: 5, depth: 1 },
      { id: "file:internal/b/y.go", kind: "file", label: "y.go", path: "internal/b/y.go", language: "go", symbols: 2, depth: 1 },
    ],
    edges: [
      { from: "file:internal/a/x.go", to: "file:internal/b/y.go", kind: "imports" },
      { from: "module:internal/a", to: "file:internal/a/x.go", kind: "contains" },
    ],
    ...over,
  };
}

describe("layoutGraph", () => {
  it("puts the focus in the middle and each depth on its own ring", () => {
    const layout = layoutGraph(view());
    const focus = layout.nodes.find((n) => n.node.depth === 0);
    expect(focus).toBeDefined();
    expect(focus!.x).toBe(layout.cx);
    expect(focus!.y).toBe(layout.cy);
    const oneHop = layout.nodes.filter((n) => n.node.depth === 1);
    expect(oneHop).toHaveLength(2);
    for (const n of oneHop) {
      const radius = Math.hypot(n.x - layout.cx, n.y - layout.cy);
      expect(Math.round(radius)).toBe(170);
    }
  });

  it("keeps the module first inside a ring so the picture is stable", () => {
    const nodes = layoutGraph(view()).nodes.filter((n) => n.ring === 1);
    expect(nodes[0]?.node.kind).toBe("module");
  });

  it("does not draw an edge whose endpoint the walk left out", () => {
    const v = view({ nodes: view().nodes.slice(0, 1) });
    expect(layoutGraph(v).edges).toHaveLength(0);
  });

  it("never draws a self loop", () => {
    const v = view({ edges: [{ from: "file:internal/a/x.go", to: "file:internal/a/x.go", kind: "imports" }] });
    expect(layoutGraph(v).edges).toHaveLength(0);
  });

  it("fits an empty graph without dividing by zero", () => {
    const layout = layoutGraph(view({ nodes: [], edges: [] }));
    expect(layout.nodes).toHaveLength(0);
    expect(layout.width).toBeGreaterThan(0);
  });
});

describe("nodeContext", () => {
  it("names the path, kind, and size of a file", () => {
    const node = view().nodes[0]!;
    const ctx = nodeContext(node, { imports: ["y.go"], contained: [] });
    const labels = ctx.rows.map((r) => r.label);
    expect(labels).toContain("Path");
    expect(labels).toContain("Declarations");
    expect(ctx.rows.find((r) => r.label === "Path")?.value).toBe("internal/a/x.go");
    expect(ctx.rows.find((r) => r.label === "Imports")?.value).toBe("y.go");
  });

  it("says a module is a directory", () => {
    const ctx = nodeContext(view().nodes[1]!, { imports: [], contained: [] });
    expect(ctx.subtitle).toContain("directory");
    expect(ctx.rows.find((r) => r.label === "Kind")?.value).toContain("module");
  });

  it("renders an empty path as the repository root rather than a blank row", () => {
    const ctx = nodeContext(
      { id: "module:", kind: "module", label: ".", path: "", symbols: 1, depth: 0 },
      { imports: [], contained: [] },
    );
    expect(ctx.rows.find((r) => r.label === "Path")?.value).toBe(".");
  });
});

describe("neighboursOf", () => {
  it("separates what a node imports from what imports it", () => {
    const v = view();
    expect(neighboursOf(v.nodes[0]!, v).imports).toEqual(["y.go"]);
    expect(neighboursOf(v.nodes[2]!, v).incoming).toEqual(["x.go"]);
  });

  it("counts only import edges, never containment", () => {
    const v = view();
    const n = neighboursOf(v.nodes[1]!, v);
    expect(n.imports).toEqual([]);
    expect(n.incoming).toEqual([]);
  });
});

describe("graphSummary", () => {
  it("counts modules and files", () => {
    expect(graphSummary(view())).toBe("1 module · 2 files");
  });

  it("says when the walk was truncated", () => {
    expect(graphSummary(view({ truncated: true, truncated_at: 2 }))).toContain("truncated at 2 hops");
  });

  it("explains an unscanned repository", () => {
    expect(graphSummary(view({ ready: false }))).toContain("No repository snapshot");
  });

  it("explains an empty focus", () => {
    expect(graphSummary(view({ nodes: [], edges: [] }))).toContain("No components match");
  });
});

describe("focus ids", () => {
  it("centres on the file a component lives in", () => {
    expect(
      focusIdForComponent({ id: "A", name: "A", kind: "function", path: "internal/a/x.go", line: 1, module: "internal/a", language: "go" }),
    ).toBe("file:internal/a/x.go");
  });

  it("centres on a module, including the repository root", () => {
    expect(focusIdForModule("internal/a")).toBe("module:internal/a");
    expect(focusIdForModule("")).toBe("module:");
  });
});
