import { describe, expect, it } from "vitest";
import {
  avatarStatus,
  isHeartbeat,
  runFailure,
  workingAvatars,
  NARRATING_STATUS,
  NARRATOR_ID,
  type RoomInput,
  type RoomThought,
} from "./room";

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

describe("the narrator", () => {
  // It holds no draft. If it ever counted as working, a finished cycle could
  // not synthesize while the narrator happened to be speaking.
  it("never counts as an avatar still working", () => {
    const r = room([
      { avatarId: "pm", status: "Synced", summary: "" },
      { avatarId: "architect", status: "Synced", summary: "" },
      { avatarId: "critic", status: "Synced", summary: "" },
      { avatarId: NARRATOR_ID, status: NARRATING_STATUS, summary: "The architect is mapping the export." },
    ]);
    expect(workingAvatars(r, MAIN)).toEqual([]);
  });

  // Its status is not one the room knows, so an avatar card would read it as
  // queued. Only the narration banner shows it, and the banner gates nothing.
  it("does not turn a healthy run into a failed one", () => {
    const r = room([{ avatarId: NARRATOR_ID, status: NARRATING_STATUS, summary: "Three avatars are drafting." }]);
    expect(runFailure(r)).toBeNull();
  });

  // A specialist is counted by prefix; the narrator must not be swept up by
  // the same rule.
  it("is not mistaken for a specialist", () => {
    const r = room([{ avatarId: NARRATOR_ID, status: NARRATING_STATUS, summary: "" }]);
    expect(workingAvatars(r, [])).toEqual([]);
  });
});

describe("isHeartbeat", () => {
  it("recognises the progress line a running call emits", () => {
    expect(isHeartbeat("Running for 40s, 7.2 KB written, now on contracts.")).toBe(true);
    expect(isHeartbeat("Running for 10s.")).toBe(true);
  });

  // Everything an avatar actually says belongs in the discussion.
  it("leaves real statements alone", () => {
    for (const said of [
      "Part 1 of 3: mapping the stories onto module boundaries, contracts and a workstream DAG.",
      "Draft validated and published.",
      "The draft failed validation with 2 problem(s); requesting one repair.",
      "",
    ]) {
      expect(isHeartbeat(said)).toBe(false);
    }
  });
});
