import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";

import { PlanTree } from "./PlanTree";
import type { PlanGraph } from "../ui/planGraph";

/**
 * The 2D plan is a stack of documents, not a tree of dots: each node renders
 * as a card with its kind, its title and its own summary, and a selected node
 * renders its content beside the list. The repo has no DOM test harness (the
 * component library is not a dependency), so this asserts the rendered markup
 * rather than driving clicks; the tree logic itself is covered in
 * `ui/planGraph.test.ts`.
 */
const graph: PlanGraph = {
  nodes: [
    { id: "epic-1", kind: "epic", parentId: null, label: "Offline todos", detail: "Users finish a task with no network", depth: 0 },
    { id: "story-1", kind: "story", parentId: "epic-1", label: "Add a task", detail: "As a user", depth: 1 },
    { id: "ws-1", kind: "workstream", parentId: null, label: "ws-storage", detail: "internal/storage", depth: 0 },
  ],
  children: { "": ["epic-1", "ws-1"], "epic-1": ["story-1"] },
};

function markup(selectedId: string | null = null): string {
  return renderToStaticMarkup(<PlanTree graph={graph} selectedId={selectedId} onSelect={() => {}} />);
}

describe("PlanTree", () => {
  it("renders every node as a document card with its kind and title", () => {
    const html = markup();
    expect(html).toContain("Offline todos");
    expect(html).toContain("Add a task");
    expect(html).toContain("ws-storage");
    // The document shape, not a dot on a canvas.
    expect(html).toContain("pf-plandocs-card");
    expect(html).toContain("pf-plandocs-title");
    expect(html).toContain("Users finish a task with no network");
    // Nesting is preserved: a story sits inside its epic's group.
    expect(html).toContain('role="group"');
  });

  it("shows the selected node's own content beside the list", () => {
    const html = markup("epic-1");
    expect(html).toContain("pf-plandocs-detail");
    expect(html).toContain("Details for Offline todos");
    // The facts a reader needs: its id, its parent and its worst finding.
    expect(html).toContain("epic-1");
    expect(html).toContain("Users finish a task with no network");
  });

  it("says so when nothing has been drafted yet", () => {
    const html = renderToStaticMarkup(
      <PlanTree graph={{ nodes: [], children: {} }} onSelect={() => {}} />,
    );
    expect(html).toContain("Nothing has been drafted yet");
  });
});
