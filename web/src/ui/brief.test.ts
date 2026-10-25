import { describe, expect, it } from "vitest";

import {
  BRIEF_SECTIONS,
  briefSummary,
  MAX_EVIDENCE_CHARS,
  mentionedCount,
  MIN_WORDS_TO_CHECK,
  readBrief,
  wordCount,
} from "./brief";
import type { BriefSectionID } from "./brief";

function found(text: string): BriefSectionID[] {
  return readBrief(text)
    .filter((c) => c.mentioned)
    .map((c) => c.section.id);
}

describe("BRIEF_SECTIONS", () => {
  it("is the five topics the skill interviews for, in its order", () => {
    expect(BRIEF_SECTIONS.map((s) => s.id)).toEqual([
      "audience",
      "outcome",
      "motivation",
      "nonGoals",
      "existing",
    ]);
  });

  // Add writes the heading into the document, and readBrief then has to see
  // it, or a section the writer just inserted would still read as absent.
  it("inserts a heading that its own cues recognise", () => {
    for (const section of BRIEF_SECTIONS) {
      expect(found(section.insert)).toContain(section.id);
    }
  });

  it("gives every section a reason naming what the deliberation does with it", () => {
    for (const section of BRIEF_SECTIONS) {
      expect(section.need.length).toBeGreaterThan(20);
      expect(section.label).not.toBe("");
    }
  });
});

describe("readBrief", () => {
  it("reports all five absent for an empty document", () => {
    expect(found("")).toEqual([]);
    expect(readBrief("")).toHaveLength(5);
  });

  it("finds a topic mentioned mid-sentence, not just as a heading", () => {
    expect(found("We are building this for the user who reconciles invoices.")).toContain("audience");
  });

  it("quotes the line that matched", () => {
    const checks = readBrief("nothing here\n## Why now\nthe problem is month-end takes four days");
    const motivation = checks.find((c) => c.section.id === "motivation");
    expect(motivation?.mentioned).toBe(true);
    expect(motivation?.evidence).toContain("Why now");
  });

  it("strips the heading marker from evidence", () => {
    const checks = readBrief("## What is out of scope");
    const nonGoals = checks.find((c) => c.section.id === "nonGoals");
    expect(nonGoals?.evidence).toBe("What is out of scope");
  });

  it("bounds a long line", () => {
    const checks = readBrief(`out of scope ${"x".repeat(500)}`);
    const nonGoals = checks.find((c) => c.section.id === "nonGoals");
    expect(nonGoals?.evidence.length).toBeLessThanOrEqual(MAX_EVIDENCE_CHARS + 1);
    expect(nonGoals?.evidence.endsWith("…")).toBe(true);
  });

  it("ignores blank lines when looking for evidence", () => {
    const checks = readBrief("\n\n\nthe problem is slow exports");
    expect(checks.find((c) => c.section.id === "motivation")?.evidence).toBe(
      "the problem is slow exports",
    );
  });

  it("is case-insensitive", () => {
    expect(found("OUT OF SCOPE: billing")).toContain("nonGoals");
  });

  // A full brief written in ordinary prose, with none of the headings, should
  // still read as covered: the scaffold is for people who want it, not a form.
  it("recognises a prose brief with no headings", () => {
    const prose = [
      "Finance analysts reconcile invoices by hand every month end.",
      "Currently that takes four days and we lose two of them to formatting.",
      "We will know this worked when a reconciliation closes in under a day.",
      "Billing rules are out of scope; we are not touching the existing ledger.",
      "The current export job already produces the CSV we need to build on.",
    ].join("\n");
    expect(mentionedCount(readBrief(prose))).toBe(5);
  });

  // The known limit, recorded rather than hidden. Job titles are open-ended,
  // so a brief naming one the cue list does not carry reads as not mentioned
  // even though it answers the question perfectly well. This is why the rail
  // says "not mentioned yet" and never "missing", and why Confirm is never
  // gated on any of this.
  it("misses an audience named by a job title it does not know", () => {
    expect(found("Radiographers need the scan queue to survive a restart.")).not.toContain(
      "audience",
    );
  });
});

describe("briefSummary", () => {
  it("does not report absent topics against a sketch", () => {
    const text = "billing export";
    expect(wordCount(text)).toBeLessThan(MIN_WORDS_TO_CHECK);
    expect(briefSummary(text, readBrief(text))).toContain("fill in as you go");
  });

  it("counts what is not mentioned once the document is long enough", () => {
    const text = `${"word ".repeat(MIN_WORDS_TO_CHECK)}and nothing else at all`;
    expect(briefSummary(text, readBrief(text))).toMatch(/topics are not mentioned/);
  });

  it("uses the singular for exactly one", () => {
    const checks = readBrief("");
    const four = checks.map((c, i) => (i === 0 ? { ...c, mentioned: false } : { ...c, mentioned: true }));
    expect(briefSummary("x ".repeat(40), four)).toBe("One topic is not mentioned yet.");
  });

  // The whole point of the honesty rule: a word search may never tell a writer
  // their brief is ready, only that the words appear.
  it("never claims the brief is ready, complete or good", () => {
    const covered = readBrief("").map((c) => ({ ...c, mentioned: true }));
    const summary = briefSummary("x ".repeat(40), covered);
    expect(summary).toBe("All five topics appear somewhere in your brief.");
    for (const word of ["ready", "complete", "good", "sufficient", "score"]) {
      expect(summary.toLowerCase()).not.toContain(word);
    }
  });
});

describe("wordCount", () => {
  it("is zero for whitespace", () => {
    expect(wordCount("   \n\t ")).toBe(0);
  });

  it("collapses runs of whitespace", () => {
    expect(wordCount("a   b\n\nc")).toBe(3);
  });
});
