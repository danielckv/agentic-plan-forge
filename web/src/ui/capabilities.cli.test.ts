import { describe, expect, it } from "vitest";

import { cliSummary, type CapabilitiesView, type DetectedCLI, unsupportedCLIs, usableCLIs } from "./capabilities";

function cli(partial: Partial<DetectedCLI>): DetectedCLI {
  return {
    name: "Claude Code",
    binaries: ["claude"],
    found: true,
    purpose: "Generate plan stages",
    usable: false,
    ...partial,
  };
}

function view(clis: DetectedCLI[]): CapabilitiesView {
  return { hosts: [], tools: [], skills: [], hooks: [], clis, pinned: [] };
}

/**
 * The first run has to answer one question: which coding agent will run the
 * stages. A found tool Plan Forge cannot call must never be offered as one.
 */
describe("first-run coding agent scan", () => {
  it("offers only agents whose adapter can take a call", () => {
    const v = view([
      cli({ name: "Claude Code", kind: "claude", usable: true }),
      cli({ name: "Codex CLI", kind: "", usable: false, supports: "detected only" }),
    ]);
    expect(usableCLIs(v).map((c) => c.name)).toEqual(["Claude Code"]);
    expect(unsupportedCLIs(v).map((c) => c.name)).toEqual(["Codex CLI"]);
  });

  it("names the agent a plan will run on", () => {
    expect(cliSummary(view([cli({ usable: true })]))).toBe("Plans will run on Claude Code.");
  });

  it("lists every usable agent when more than one is installed", () => {
    const summary = cliSummary(
      view([cli({ name: "Claude Code", usable: true }), cli({ name: "oh-my-pi", usable: true })]),
    );
    expect(summary).toBe("Plans can run on Claude Code, oh-my-pi.");
  });

  it("says what to install when nothing usable is present", () => {
    const summary = cliSummary(view([cli({ found: false, usable: false })]));
    expect(summary).toContain("No coding agent found");
    expect(summary).toContain("Claude Code");
  });

  it("explains installed-but-unsupported instead of promising a run", () => {
    const summary = cliSummary(view([cli({ name: "Codex CLI", kind: "", usable: false })]));
    expect(summary).toContain("not supported");
    expect(summary).not.toContain("Plans will run");
  });

  it("says nothing before the list has loaded", () => {
    expect(cliSummary(null)).toBe("");
  });
});
