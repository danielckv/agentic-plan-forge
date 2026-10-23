import { describe, expect, it } from "vitest";
import { avatarStatus, runFailure, workingAvatars, type RoomInput, type RoomThought } from "./room";

const MAIN = ["pm", "architect", "critic", "synthesizer"];

function room(thoughts: RoomThought[], draftAvatars: string[] = [], runActive = true): RoomInput {
  const byId: Record<string, RoomThought> = {};
  for (const t of thoughts) byId[t.avatarId] = t;
  return { thoughts: byId, draftAvatars, runActive };
}

describe("avatarStatus", () => {
  it("uses the newest thought", () => {
    expect(avatarStatus(room([{ avatarId: "pm", status: "Drafting", summary: "" }]), "pm")).toBe("Drafting");
  });

  it("reads a published draft as synced", () => {
    expect(avatarStatus(room([], ["pm"]), "pm")).toBe("Synced");
  });

  it("is queued during a run and idle outside one", () => {
    expect(avatarStatus(room([]), "critic")).toBe("Queued");
    expect(avatarStatus(room([], [], false), "critic")).toBe("Idle");
  });

  it("ignores a status the room does not know", () => {
    expect(avatarStatus(room([{ avatarId: "pm", status: "Pondering", summary: "" }]), "pm")).toBe("Queued");
  });
});

describe("workingAvatars", () => {
  it("counts the avatars at work", () => {
    const r = room([
      { avatarId: "pm", status: "Synced", summary: "" },
      { avatarId: "architect", status: "Thinking", summary: "" },
    ]);
    expect(workingAvatars(r, MAIN)).toEqual(["architect", "critic"]);
  });

  // The synthesizer waits for the user, so a queued one must not keep the
  // cycle open: that is what kept "ready to synthesize" from ever showing.
  it("never counts a queued synthesizer", () => {
    const r = room([
      { avatarId: "pm", status: "Synced", summary: "" },
      { avatarId: "architect", status: "Synced", summary: "" },
      { avatarId: "critic", status: "Synced", summary: "" },
    ]);
    expect(workingAvatars(r, MAIN)).toEqual([]);
  });

  it("counts a specialist at work", () => {
    const r = room([
      { avatarId: "pm", status: "Synced", summary: "" },
      { avatarId: "architect", status: "Synced", summary: "" },
      { avatarId: "critic", status: "Synced", summary: "" },
      { avatarId: "specialist:billing", status: "Drafting", summary: "" },
    ]);
    expect(workingAvatars(r, MAIN)).toEqual(["specialist:billing"]);
  });

  it("counts nothing before a run starts", () => {
    expect(workingAvatars(room([], [], false), MAIN)).toEqual([]);
  });
});

describe("runFailure", () => {
  it("names the stage that failed", () => {
    const r = room([
      { avatarId: "pm", status: "Synced", summary: "" },
      { avatarId: "architect", status: "Failed", summary: "architect stage failed: bound exceeded" },
      { avatarId: "critic", status: "Cancelled", summary: "The run stopped." },
    ]);
    expect(runFailure(r)).toEqual({
      avatarId: "architect",
      summary: "architect stage failed: bound exceeded",
    });
  });

  it("is null while the run is healthy", () => {
    expect(runFailure(room([{ avatarId: "pm", status: "Synced", summary: "" }]))).toBeNull();
  });

  // A cancelled avatar is a consequence of a failure elsewhere.
  it("is null when avatars were only cancelled", () => {
    expect(runFailure(room([{ avatarId: "critic", status: "Cancelled", summary: "" }]))).toBeNull();
  });
});
