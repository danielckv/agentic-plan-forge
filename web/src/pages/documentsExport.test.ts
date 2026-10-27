import { describe, expect, it } from "vitest";

import type { BundleView, ExportReport } from "../protocol/api";
import { canExport, documentsNote, exportLabel, exportResult } from "./documentsExport";

/**
 * The documents panel must never present a preview as an export. These tests
 * pin that boundary: the list is a list, the button is the only write, and a
 * refusal is reported as a refusal.
 */

function view(over: Partial<BundleView> = {}): BundleView {
  return {
    available: true,
    files: [
      { path: "docs/plans/invoice-export.md", kind: "plan", title: "Invoice export" },
      { path: "docs/specs/invoice-export.md", kind: "spec", title: "Invoice export" },
    ],
    ...over,
  };
}

function report(over: Partial<ExportReport> = {}): ExportReport {
  return { state: "exported", committed: ["docs/plans/a.md"], ...over };
}

describe("documentsNote", () => {
  it("says the list is a preview, not something written", () => {
    const note = documentsNote(view());
    expect(note).toContain("would be written");
    expect(note).toContain("Nothing is written until you press the button");
  });

  it("counts one file as a file, not files", () => {
    const note = documentsNote(view({ files: [{ path: "a.md", kind: "plan", title: "A" }] }));
    expect(note).toContain("1 file would");
  });

  it("says an existing file is never replaced", () => {
    expect(documentsNote(view())).toContain("never replaced");
  });

  it("reports why there is nothing to export", () => {
    expect(documentsNote({ available: false, problem: "the plan has to be approved first", files: [] })).toContain(
      "approved",
    );
  });

  it("says nothing would be written when the plan has no documents", () => {
    expect(documentsNote(view({ files: [] }))).toContain("Nothing would be written");
  });
});

describe("canExport", () => {
  it("needs something to write", () => {
    expect(canExport(view({ files: [] }), false)).toBe(false);
    expect(canExport(view(), false)).toBe(true);
  });

  it("is disabled while a write is in flight, so one press is one export", () => {
    expect(canExport(view(), true)).toBe(false);
  });

  it("is disabled when the view is unavailable", () => {
    expect(canExport(view({ available: false }), false)).toBe(false);
    expect(canExport(null, false)).toBe(false);
  });
});

describe("exportLabel", () => {
  it("changes while writing", () => {
    expect(exportLabel(false)).toBe("Write documents");
    expect(exportLabel(true)).toBe("Writing…");
  });
});

describe("exportResult", () => {
  it("reports how many files landed", () => {
    const got = exportResult(report({ committed: ["a.md", "b.md"] }));
    expect(got.tone).toBe("ok");
    expect(got.message).toBe("Wrote 2 files into the repository.");
  });

  it("names the file that blocked the export rather than reporting success", () => {
    const got = exportResult({ state: "collision", problem: "docs/plans/a.md already exists." });
    expect(got.tone).toBe("warn");
    expect(got.message).toContain("docs/plans/a.md");
  });

  it("reports a failed write as a failure", () => {
    const got = exportResult({ state: "failed", problem: "disk full" });
    expect(got.tone).toBe("warn");
    expect(got.message).toBe("disk full");
  });

  it("reports a missing confirmation as a warning, not a write", () => {
    expect(exportResult({ state: "confirm", problem: "needs confirmation" }).tone).toBe("warn");
  });
});
