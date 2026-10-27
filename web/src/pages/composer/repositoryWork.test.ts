import { describe, expect, it } from "vitest";

import type { RepositoryDocument } from "../../protocol/api";
import { documentsFor, statusOf } from "./RepositoryWork";

function doc(kind: RepositoryDocument["kind"], path: string): RepositoryDocument {
  return { id: path, path, kind, sha256: "", bytes: 1, headings: [], items: [] };
}

const inventory = [
  doc("state", ".agents/STATE.md"),
  doc("plan", "docs/plans/a.md"),
  doc("spec", "docs/specs/a.md"),
  doc("adr", "docs/adrs/0001-a.md"),
  doc("follow-up", "FOLLOW_UPS.md"),
  doc("epic", "docs/epics/a.md"),
  doc("task", "docs/epics/b.md"),
];

/**
 * The work view groups real files by what they are. A filter that silently
 * included the wrong kind would show a decision record as a task, and the next
 * item order would stop matching the state file it came from.
 */
describe("documentsFor", () => {
  it("returns no documents for Next, which is the state file's own order", () => {
    expect(documentsFor(inventory, "next")).toEqual([]);
  });

  it("groups epics and tasks together", () => {
    expect(documentsFor(inventory, "epics").map((d) => d.path)).toEqual(["docs/epics/a.md", "docs/epics/b.md"]);
  });

  it("groups plans, specs and decision records together", () => {
    expect(documentsFor(inventory, "plans").map((d) => d.kind)).toEqual(["plan", "spec", "adr"]);
  });

  it("keeps follow-ups and state separate", () => {
    expect(documentsFor(inventory, "follow-ups").map((d) => d.path)).toEqual(["FOLLOW_UPS.md"]);
    expect(documentsFor(inventory, "state").map((d) => d.path)).toEqual([".agents/STATE.md"]);
  });

  it("preserves source order and never invents an entry", () => {
    expect(documentsFor([], "epics")).toEqual([]);
    expect(documentsFor(inventory, "epics")[0]!.path).toBe("docs/epics/a.md");
  });
});

describe("statusOf", () => {
  it("states an unknown status instead of leaving it blank", () => {
    expect(statusOf("unknown")).toBe("status unknown");
    expect(statusOf("open")).toBe("open");
    expect(statusOf("done")).toBe("done");
  });

  it("passes through a status the reader can act on", () => {
    expect(statusOf("blocked")).toBe("blocked");
  });
});
