import { describe, expect, it } from "vitest";

import {
  costLine,
  failingReceipts,
  previewAvailable,
  sandboxHeadline,
  sandboxLive,
  type LaunchReport,
  type RecipeView,
} from "./sandbox";

function report(partial: Partial<LaunchReport>): LaunchReport {
  return { state: "preparing", ...partial };
}

/**
 * A sandbox is paid for by the minute, so the page must never say it is running
 * when it is not, and must never offer a preview that has not passed its
 * recorded readiness check.
 */
describe("prepared sandbox state", () => {
  it("counts a created sandbox as live only while it exists", () => {
    expect(sandboxLive(report({ state: "building" }))).toBe(true);
    expect(sandboxLive(report({ state: "ready" }))).toBe(true);
    expect(sandboxLive(report({ state: "stopped" }))).toBe(false);
    expect(sandboxLive(report({ state: "expired" }))).toBe(false);
    expect(sandboxLive(null)).toBe(false);
  });

  it("offers a preview only when the sandbox is ready", () => {
    expect(previewAvailable(report({ state: "ready", preview: "/preview/" }))).toBe(true);
    expect(previewAvailable(report({ state: "building", preview: "/preview/" }))).toBe(false);
    expect(previewAvailable(report({ state: "ready" }))).toBe(false);
  });

  it("leads with the problem when there is one, not with the stage", () => {
    expect(sandboxHeadline(report({ state: "failed", problem: "build exited 1" }), null)).toBe("build exited 1");
  });

  it("says plainly that the last step failed", () => {
    expect(sandboxHeadline(report({ state: "failed" }), null)).toContain("failed");
  });

  it("explains setup instead of pretending a launch happened", () => {
    expect(sandboxHeadline(report({ state: "setup", problem: "No prepared-VM provider is configured." }), null)).toContain(
      "No prepared-VM provider",
    );
  });

  it("reports an unknown state as unknown rather than as progress", () => {
    expect(sandboxHeadline(report({ state: "unknown" }), null)).toContain("unknown");
  });

  it("names a project with no recorded run command", () => {
    const recipe: RecipeView = { available: false, reused: false, problem: "no run command" };
    expect(sandboxHeadline(null, recipe)).toContain("No command");
  });

  it("surfaces a failing build or test receipt, so a green page is earned", () => {
    const r = report({
      state: "ready",
      receipts: [
        { Kind: "build", Command: "go build ./...", ExitCode: 0 },
        { Kind: "test", Command: "go test ./...", ExitCode: 1 },
      ],
    });
    const failing = failingReceipts(r);
    expect(failing).toHaveLength(1);
    expect(failing[0]?.Kind).toBe("test");
  });

  it("never shows a zero cost when no rates are configured", () => {
    expect(costLine(null)).toContain("unavailable");
    expect(costLine({ lifetime: "60m" })).toContain("unavailable");
    expect(costLine({ lifetime: "60m", amount: 0.18 })).toBe("60m · about $0.18");
  });
});
