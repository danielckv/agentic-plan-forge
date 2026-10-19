// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";

const { parse, render } = vi.hoisted(() => ({
  parse: vi.fn(),
  render: vi.fn(),
}));

vi.mock("mermaid", () => ({
  default: { initialize: vi.fn(), parse, render },
}));

import { reidentifySvg, renderDiagram } from "../components/MermaidSvg";

describe("renderDiagram parse gate", () => {
  it("rejects when mermaid.parse resolves false and shows the source", async () => {
    parse.mockResolvedValueOnce(false);
    const result = await renderDiagram("graph TD; A-->B", "dark");
    expect(result.svg).toBeNull();
    expect(result.diagnostic).toContain("did not parse");
    expect(result.rawSource).toBe("graph TD; A-->B");
    expect(render).not.toHaveBeenCalled();
  });

  it("renders when parse succeeds and records the render id", async () => {
    parse.mockResolvedValueOnce({ diagramType: "flowchart" });
    render.mockImplementationOnce((id: string) =>
      Promise.resolve({ svg: `<svg id="${id}"><g id="${id}-a"></g></svg>` }),
    );
    const result = await renderDiagram("graph TD; A-->B", "dark");
    expect(result.renderId).toMatch(/^pf-mmd-/);
    expect(result.svg).toContain(result.renderId ?? "never");
  });
});

describe("reidentifySvg", () => {
  it("re-keys every occurrence of the render id", () => {
    const svg = '<svg id="x1"><style>#x1 .a{}</style><path marker-end="url(#x1_arrow)"/></svg>';
    const next = reidentifySvg(svg, "x1", "x1-expanded");
    expect(next).not.toMatch(/x1(?!-expanded)/);
    expect(next).toContain("url(#x1-expanded_arrow)");
    expect(reidentifySvg(svg, "", "y")).toBe(svg);
  });
});
