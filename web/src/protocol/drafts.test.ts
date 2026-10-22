import { describe, expect, it } from "vitest";

import {
  architectureMermaid,
  draftContentText,
  parseArchitectDraft,
  parseCritique,
  parsePMDraft,
  pmEpicMarkdown,
} from "./drafts";

// Payloads in the exact shapes internal/avatars publishes (json.RawMessage
// content, the whole critique set per frame).
const PM = {
  epics: [{ id: "E1", title: "CSV export", goal: "Finance downloads invoices." }],
  stories: [
    { id: "S1", epic_id: "E1", actor: "finance analyst", need: "export a range", benefit: "I close the week", acceptance_criteria: ["Under 30 s"], requirement_ids: ["REQ-1"] },
  ],
};
const ARCH = {
  summary: "Stream rows.",
  constraints: ["Memory stays flat"],
  workstreams: [{ id: "reporting", role: "backend", module_paths: ["internal/reporting"], depends_on: [], contract_ids: [] }],
  contracts: [],
  diagrams: {
    architecture: { nodes: [{ id: "ui", label: "Finance UI" }, { id: "q", label: "Query<script>" }], edges: [{ from: "ui", to: "q", label: "range" }] },
    sequence: { participants: [], messages: [] },
  },
  doc_tasks: [],
};

describe("draft readers", () => {
  it("keeps strings and serializes inline JSON", () => {
    expect(draftContentText("x")).toBe("x");
    expect(draftContentText({ a: 1 })).toBe('{"a":1}');
    expect(draftContentText(undefined)).toBeUndefined();
  });

  it("reads the whole critique set", () => {
    const { cycle, findings } = parseCritique({
      run_id: "r",
      cycle: 2,
      issues: [
        { id: "C-1", severity: "CRITICAL", target_ids: ["reporting"], evidence: "Partial files.", required_change: "Add a trailer.", status: "resolved" },
        { id: "C-2", severity: "LOW?", evidence: "x", required_change: "y", status: "open" },
        { severity: "HIGH" },
      ],
    });
    expect(cycle).toBe(2);
    expect(findings).toEqual([
      { issueId: "C-1", severity: "CRITICAL", summary: "Partial files.", requiredChange: "Add a trailer.", targets: ["reporting"], resolved: true },
      { issueId: "C-2", severity: "MEDIUM", summary: "x", requiredChange: "y", targets: [], resolved: false },
    ]);
    expect(parseCritique("garbage").findings).toEqual([]);
  });

  it("reads the PM draft from an object or its JSON text, and renders an epic", () => {
    for (const content of [PM, JSON.stringify(PM)]) {
      const d = parsePMDraft(content);
      expect(d?.epics[0]?.title).toBe("CSV export");
      expect(d?.stories[0]?.epicId).toBe("E1");
    }
    const md = pmEpicMarkdown(parsePMDraft(PM)!, "E1");
    expect(md).toContain("# E1 · CSV export");
    expect(md).toContain("As a **finance analyst**, I want to export a range, so that I close the week.");
    expect(md).toContain("- [ ] Under 30 s");
    expect(parsePMDraft({ summary: "not a pm doc" })).toBeNull();
  });

  it("renders the architecture the way the Go compiler does, with safe labels", () => {
    const d = parseArchitectDraft(ARCH);
    expect(d?.workstreams[0]?.modules).toEqual(["internal/reporting"]);
    expect(architectureMermaid(d!)).toBe('graph TD\n    n0["Finance UI"]\n    n1["Query script"]\n    n0 -->|"range"| n1');
    expect(architectureMermaid({ ...d!, nodes: [] })).toBeNull();
    expect(parseArchitectDraft(PM)).toBeNull();
  });
});
