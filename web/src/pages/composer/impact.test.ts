import { describe, expect, it } from "vitest";

import type { ImpactLink } from "../../protocol/api";
import { groupByVerdict, linkTitle, linkWhere, VERDICT_LABEL, VERDICT_TONE } from "./ImpactPanel";

function link(partial: Partial<ImpactLink>): ImpactLink {
  return {
    id: partial.id ?? Math.random().toString(36),
    area: "Invoice export",
    kind: "code",
    verdict: "change",
    source: "model",
    ...partial,
  };
}

describe("groupByVerdict", () => {
  it("reads in the order a planner thinks: what changes, what is new, what is reused", () => {
    const groups = groupByVerdict([
      link({ verdict: "reuse" }),
      link({ verdict: "add" }),
      link({ verdict: "change" }),
    ]);
    expect(groups.map((g) => g.verdict)).toEqual(["change", "add", "reuse"]);
  });

  it("drops empty groups rather than printing an empty heading", () => {
    expect(groupByVerdict([link({ verdict: "add" })]).map((g) => g.verdict)).toEqual(["add"]);
    expect(groupByVerdict([])).toEqual([]);
  });

  it("keeps keyword matches, which carry no verdict, in their own group", () => {
    const groups = groupByVerdict([link({ verdict: "", source: "lexical" })]);
    expect(groups).toHaveLength(1);
    expect(groups[0]!.verdict).toBe("");
  });
});

describe("the words shown to a reader who does not read code", () => {
  it("never uses a developer's vocabulary", () => {
    const jargon = ["function", "symbol", "ast", "repository", "declaration", "struct", "interface", "commit"];
    for (const label of Object.values(VERDICT_LABEL)) {
      for (const word of jargon) {
        expect(label.toLowerCase()).not.toContain(word);
      }
    }
  });

  it("says something for every verdict, including the keyword match", () => {
    for (const verdict of ["", "change", "add", "reuse", "conflicts_with"] as const) {
      expect(VERDICT_LABEL[verdict]).not.toBe("");
      expect(VERDICT_TONE[verdict]).not.toBe("");
    }
  });

  it("marks a clash with its own tone, so it does not read as an ordinary change", () => {
    expect(VERDICT_TONE.conflicts_with).toBe("conflict");
    expect(VERDICT_TONE.conflicts_with).not.toBe(VERDICT_TONE.change);
  });
});

describe("linkTitle", () => {
  it("leads with the plain-language area, not the code name", () => {
    expect(linkTitle(link({ area: "Invoice export", symbol_name: "ExportInvoices" }))).toBe("Invoice export");
  });

  it("falls back through the declaration and the heading to the path", () => {
    expect(linkTitle(link({ area: "", symbol_name: "ExportInvoices" }))).toBe("ExportInvoices");
    expect(linkTitle(link({ area: "", kind: "decision", heading: "Export format" }))).toBe("Export format");
    expect(linkTitle(link({ area: "", kind: "doc", path: "docs/plans/search.md" }))).toBe("docs/plans/search.md");
  });
});

describe("linkWhere", () => {
  it("is empty when there is nothing to open yet", () => {
    expect(linkWhere(link({ verdict: "add" }))).toBe("");
    expect(linkWhere(link({ verdict: "add", path: "" }))).toBe("");
  });

  it("names the declaration and the line for the reader who wants the file", () => {
    expect(linkWhere(link({ path: "internal/billing/export.go", line: 12, symbol_name: "ExportInvoices" }))).toBe(
      "ExportInvoices — internal/billing/export.go:12",
    );
  });

  it("omits the line for a document", () => {
    expect(linkWhere(link({ kind: "decision", path: "docs/adrs/0004-invoices.md" }))).toBe(
      "docs/adrs/0004-invoices.md",
    );
  });
});
