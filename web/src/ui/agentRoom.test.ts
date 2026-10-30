import { describe, expect, it } from "vitest";

import type { SyncState } from "../store/syncStore";
import { buildRoom, fusedCandidate, roomStatus, roomWorkstreams, workingInRoom, type RoomGapAgent } from "./agentRoom";

function sync(over: Partial<SyncState> = {}): SyncState {
  return {
    conn: "live",
    phase: "DELIBERATION",
    route: "/deliberate",
    runId: "run-1",
    cycle: 1,
    drafts: [],
    decisions: [],
    thoughts: {},
    critiques: [],
    timeline: [],
    ...over,
  } as SyncState;
}

function architectDraft(workstreams: { id: string; role: string }[], gaps: unknown[] = []): SyncState["drafts"][number] {
  return {
    avatarId: "architect",
    revision: 1,
    data: { summary: "s", constraints: [], workstreams, proposed_gaps: gaps },
  } as unknown as SyncState["drafts"][number];
}

describe("roomStatus", () => {
  it("reports working for a live thought", () => {
    const s = sync({ thoughts: { architect: { status: "Thinking", summary: "x" } } as never });
    expect(roomStatus(s, "architect")).toBe("working");
  });

  it("reports failed and cancelled distinctly", () => {
    const failed = sync({ thoughts: { architect: { status: "Failed", summary: "x" } } as never });
    expect(roomStatus(failed, "architect")).toBe("failed");
    const cancelled = sync({ thoughts: { architect: { status: "Cancelled", summary: "x" } } as never });
    expect(roomStatus(cancelled, "architect")).toBe("cancelled");
  });

  it("treats a finished draft as synced even without a thought", () => {
    const s = sync({ drafts: [{ avatarId: "pm", revision: 1, data: {} }] as never });
    expect(roomStatus(s, "pm")).toBe("synced");
  });

  it("is queued during a run and idle without one", () => {
    expect(roomStatus(sync(), "critic")).toBe("queued");
    expect(roomStatus(sync({ runId: null }), "critic")).toBe("idle");
  });
});

describe("roomWorkstreams", () => {
  it("reads the workstreams the architect declared", () => {
    const s = sync({ drafts: [architectDraft([{ id: "billing-api", role: "backend" }])] });
    expect(roomWorkstreams(s)).toEqual([{ id: "billing-api", role: "backend" }]);
  });

  it("is empty before an architect draft exists", () => {
    expect(roomWorkstreams(sync())).toEqual([]);
  });
});

describe("buildRoom", () => {
  it("always shows every permanent agent, even before a run", () => {
    const room = buildRoom(sync({ runId: null }), []);
    const ids = room.map((a) => a.id);
    for (const id of ["pm", "architect", "critic", "synthesizer", "code_reviewer", "tech_lead"]) {
      expect(ids).toContain(id);
    }
  });

  it("places permanent agents on the inner ring, inside the centre", () => {
    const room = buildRoom(sync(), []);
    for (const a of room.filter((x) => x.kind === "permanent")) {
      expect(a.ring).toBe(0);
      const r = Math.hypot(a.x - 0.5, a.y - 0.5);
      expect(r).toBeGreaterThan(0.2);
      expect(r).toBeLessThan(0.4);
    }
  });

  it("adds one specialist per workstream", () => {
    const s = sync({ drafts: [architectDraft([{ id: "a", role: "backend" }, { id: "b", role: "frontend" }])] });
    const specialists = buildRoom(s, []).filter((a) => a.kind === "specialist");
    expect(specialists.map((a) => a.parentId)).toEqual(["a", "b"]);
  });

  it("puts a temporary agent on its parent workstream's spoke, further out", () => {
    const s = sync({ drafts: [architectDraft([{ id: "a", role: "backend" }, { id: "b", role: "frontend" }])] });
    const gaps: RoomGapAgent[] = [
      { id: "gap-1", specialty: "security review", purpose: "p", deliverable: "d", parentId: "b", status: "running" },
    ];
    const room = buildRoom(s, gaps);
    const temp = room.find((a) => a.id === "gap:gap-1");
    const parent = room.find((a) => a.parentId === "b" && a.kind === "specialist");
    expect(temp).toBeDefined();
    expect(parent).toBeDefined();
    const sameAngle = Math.abs(temp!.x - 0.5 - (parent!.x - 0.5) * (0.46 / 0.38));
    expect(sameAngle).toBeLessThan(0.01);
    expect(temp!.status).toBe("working");
  });

  it("keeps a rejected proposal visible with its reason", () => {
    const gaps: RoomGapAgent[] = [
      { id: "gap-2", specialty: "nope", purpose: "p", deliverable: "d", parentId: "", status: "rejected", reason: "scope already owned" },
    ];
    const room = buildRoom(sync(), gaps);
    const temp = room.find((a) => a.id === "gap:gap-2");
    expect(temp?.status).toBe("cancelled");
    expect(temp?.summary).toBe("scope already owned");
  });

  it("maps the whole gap lifecycle to room statuses", () => {
    const cases: [string, string][] = [
      ["admitted", "queued"],
      ["queued", "queued"],
      ["running", "working"],
      ["completed", "synced"],
      ["fused", "synced"],
      ["failed", "failed"],
      ["retired", "cancelled"],
      ["cancelled", "cancelled"],
    ];
    for (const [lifecycle, expected] of cases) {
      const room = buildRoom(sync(), [
        { id: "g", specialty: "s", purpose: "p", deliverable: "d", parentId: "", status: lifecycle },
      ]);
      expect(room.find((a) => a.id === "gap:g")?.status).toBe(expected);
    }
  });
});

describe("fusedCandidate", () => {
  it("reports no candidate before synthesis and the open finding count", () => {
    const c = fusedCandidate(sync({ critiques: [{ id: "C-1", resolved: false }, { id: "C-2", resolved: true }] as never }));
    expect(c.present).toBe(false);
    expect(c.label).toContain("Awaiting");
    expect(c.openFindings).toBe(1);
  });

  it("reports a candidate once the synthesizer drafted", () => {
    const c = fusedCandidate(sync({ drafts: [{ avatarId: "synthesizer", revision: 1, data: {} }] as never }));
    expect(c.present).toBe(true);
    expect(c.label).toBe("Fused candidate");
  });
});

describe("workingInRoom", () => {
  it("counts only agents working right now", () => {
    const room = buildRoom(
      sync({ thoughts: { pm: { status: "Thinking", summary: "" } } as never }),
      [],
    );
    expect(workingInRoom(room)).toBe(1);
  });
});
