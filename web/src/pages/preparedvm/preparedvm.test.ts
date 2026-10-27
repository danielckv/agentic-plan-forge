import { describe, expect, it } from "vitest";

import type { LaunchReport, RecipeView } from "../../ui/sandbox";
import { canLaunch, commandLine, expiryLine, receiptRows } from "./PreparedVM";

const recipe: RecipeView = {
  available: true,
  reused: false,
  hash: "abc",
  recipe: {
    schema_version: "execution_recipe.v1",
    revision: "rev-1",
    build: { id: "build", argv: ["go", "build", "./..."], dir: ".", timeout_seconds: 600 },
    test: { id: "test", argv: ["go", "test", "./..."], dir: ".", timeout_seconds: 1800 },
    run: { id: "run", argv: ["npm", "run", "start"], dir: "web", timeout_seconds: 600 },
    runtime_port: 8080,
    readiness_probe: ["curl", "-fsS", "http://127.0.0.1:8080"],
    toolchains: ["go1.27"],
    template: "base",
  },
};

function report(partial: Partial<LaunchReport>): LaunchReport {
  return { state: "building", ...partial };
}

/**
 * The panel is the only place a paid sandbox is started, so the rules that stop
 * a second VM, a launch without review, or a false pass are pure functions and
 * tested here.
 */
describe("launch confirmation", () => {
  it("refuses an unticked confirmation", () => {
    expect(canLaunch(recipe, null, false)).toBe(false);
  });

  it("refuses when no recipe is recorded", () => {
    expect(canLaunch({ available: false, reused: false, problem: "no build" }, null, true)).toBe(false);
    expect(canLaunch(null, null, true)).toBe(false);
  });

  it("refuses a second sandbox while one is live", () => {
    expect(canLaunch(recipe, report({ state: "ready" }), true)).toBe(false);
    expect(canLaunch(recipe, report({ state: "stopped" }), true)).toBe(true);
  });

  it("allows one launch after review", () => {
    expect(canLaunch(recipe, null, true)).toBe(true);
  });
});

describe("recorded commands", () => {
  it("shows an argv array, and says so when a step has none", () => {
    expect(commandLine(recipe.recipe?.build)).toBe("go build ./...");
    expect(commandLine(undefined)).toBe("not configured");
  });
});

describe("receipts", () => {
  it("reads an absent receipt as not run, never as passed", () => {
    const rows = receiptRows(report({ receipts: [{ Kind: "build", Command: "go build ./...", ExitCode: 0 }] }));
    expect(rows.find((r) => r.kind === "build")?.ok).toBe(true);
    expect(rows.find((r) => r.kind === "test")?.result).toBe("not run");
    expect(rows.find((r) => r.kind === "test")?.ok).toBe(false);
  });

  it("keeps the provider's own exit code visible", () => {
    const rows = receiptRows(
      report({
        receipts: [
          { Kind: "build", Command: "go build ./...", ExitCode: 0 },
          { Kind: "test", Command: "go test ./...", ExitCode: 2 },
        ],
      }),
    );
    expect(rows.find((r) => r.kind === "test")?.result).toBe("exit 2");
  });

  it("counts the readiness probe as the run step", () => {
    const rows = receiptRows(report({ receipts: [{ Kind: "readiness", Command: "curl -fsS", ExitCode: 0 }] }));
    expect(rows.find((r) => r.kind === "run")?.ok).toBe(true);
  });
});

describe("expiry", () => {
  it("counts the minutes the provider has left", () => {
    const now = Date.parse("2026-10-27T10:00:00Z");
    expect(expiryLine(report({ expiresAt: "2026-10-27T10:30:00Z" }), now)).toBe("30 min left");
  });

  it("says expired rather than a negative count", () => {
    const now = Date.parse("2026-10-27T11:00:00Z");
    expect(expiryLine(report({ expiresAt: "2026-10-27T10:30:00Z" }), now)).toBe("expired");
  });

  it("reports nothing when no sandbox was created", () => {
    expect(expiryLine(null, Date.now())).toBe("");
  });
});
