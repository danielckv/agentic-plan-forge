import { describe, expect, it } from "vitest";

import type { LaunchReport } from "./sandbox";
import { sourceSummary } from "./sandbox";

/**
 * A prepared VM is rented, so what travelled to it has to be visible. A run
 * that uploaded nothing must not read like a normal run.
 */

function report(over: Partial<LaunchReport> = {}): LaunchReport {
  return { state: "building", ...over };
}

describe("sourceSummary", () => {
  it("says nothing before a run has packaged anything", () => {
    expect(sourceSummary(report())).toBeNull();
    expect(sourceSummary(null)).toBeNull();
  });

  it("counts files and reports the size", () => {
    const got = sourceSummary(report({ source: { files: 12, bytes: 4096 } }));
    expect(got).toBe("Uploaded 12 files (4 KB)");
  });

  it("singularises one file", () => {
    expect(sourceSummary(report({ source: { files: 1, bytes: 900 } }))).toContain("1 file (");
  });

  it("uses MB once the archive is large", () => {
    expect(sourceSummary(report({ source: { files: 800, bytes: 3 * 1024 * 1024 } }))).toContain("3.0 MB");
  });

  it("names how many paths were left out, so an exclusion is never silent", () => {
    const got = sourceSummary(
      report({
        source: {
          files: 5,
          bytes: 1024,
          skipped: [
            { path: ".git/config", reason: "not build source" },
            { path: "node_modules/x/index.js", reason: "not build source" },
          ],
        },
      }),
    );
    expect(got).toContain("2 left out");
  });
});
