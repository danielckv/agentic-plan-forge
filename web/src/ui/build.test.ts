import { describe, expect, it } from "vitest";

import { changeCount, isRunning, stageLabel } from "./build";
import type { BuildView, FileChange } from "./build";

function view(patch: Partial<BuildView>): BuildView {
  return {
    stage: "IDLE",
    steps: [],
    log: [],
    box_running: false,
    target: "",
    finished: false,
    busy: false,
    updated_at: "2026-10-25T12:00:00Z",
    ...patch,
  };
}

describe("isRunning", () => {
  it("is false before the first state arrives", () => {
    expect(isRunning(null)).toBe(false);
  });

  it("covers every stage during which the binary is working", () => {
    for (const stage of ["CODING", "BUILDING", "DEPLOYING"] as const) {
      expect(isRunning(view({ stage }))).toBe(true);
    }
  });

  it("leaves the buttons live in a resting stage", () => {
    for (const stage of ["IDLE", "CODED", "PREVIEWING", "DEPLOYED", "FAILED"] as const) {
      expect(isRunning(view({ stage }))).toBe(false);
    }
  });

  // busy is the binary's own flag: a stage can be resting while an action it
  // did not change the stage for is still going.
  it("honours busy even in a resting stage", () => {
    expect(isRunning(view({ stage: "CODED", busy: true }))).toBe(true);
  });
});

describe("stageLabel", () => {
  it("names every stage", () => {
    const stages = [
      "IDLE",
      "CODING",
      "CODED",
      "BUILDING",
      "PREVIEWING",
      "DEPLOYING",
      "DEPLOYED",
    ] as const;
    for (const stage of stages) {
      expect(stageLabel(view({ stage }))).not.toBe("");
    }
  });

  // A container can exit on its own, and the page must not keep claiming a
  // preview is running when the binary says the container is gone.
  it("separates a running preview from a stopped container", () => {
    expect(stageLabel(view({ stage: "PREVIEWING", box_running: true }))).toContain("running");
    expect(stageLabel(view({ stage: "PREVIEWING", box_running: false }))).toContain("stopped");
  });

  it("shows the binary's own error text when it failed", () => {
    expect(stageLabel(view({ stage: "FAILED", error: "docker build: exit 1" }))).toBe(
      "docker build: exit 1",
    );
  });

  it("never shows an empty label for a failure with no detail", () => {
    expect(stageLabel(view({ stage: "FAILED" }))).toBe("Failed");
  });
});

describe("changeCount", () => {
  function file(patch: Partial<FileChange>): FileChange {
    return { path: "a.go", status: "M", added: 0, removed: 0, binary: false, ...patch };
  }

  it("reports both counts", () => {
    expect(changeCount(file({ added: 12, removed: 3 }))).toBe("+12 −3");
  });

  it("says binary rather than +0 −0, which reads as an untouched file", () => {
    expect(changeCount(file({ binary: true }))).toBe("binary");
  });
});
