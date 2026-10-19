import { describe, expect, it } from "vitest";

import { allOpenIds, countMatrix, filterMatrix, subtaskMatches } from "./matrix";
import type { EpicRow, SubtaskRow } from "./matrix";

const sub = (id: string, title: string, targets: string[]): SubtaskRow => ({
  id,
  title,
  acceptance: [],
  targets,
  verification: [],
  humanEstimate: "1 day",
  agentEstimate: "~5 mins",
});

const epics: EpicRow[] = [
  {
    id: "E1",
    title: "CSV export flow",
    goal: "",
    stories: [
      {
        id: "E1-S1",
        need: "export a range",
        subtasks: [
          sub("E1-S1-T1", "Extend query service with range filters", ["internal/reporting/query.go"]),
          sub("E1-S1-T2", "Streaming CSV serializer", ["internal/reporting/csv.go"]),
        ],
      },
    ],
  },
  {
    id: "E2",
    title: "Audit trail",
    goal: "",
    stories: [
      { id: "E2-S1", need: "list exports", subtasks: [sub("E2-S1-T1", "RecordExport with filter hash", ["internal/audit/log.go"])] },
      { id: "E2-S2", need: "retention", subtasks: [sub("E2-S2-T1", "Purge old records", ["internal/audit/purge.go", "docs/audit.md"])] },
    ],
  },
];

describe("subtaskMatches", () => {
  const st = sub("E1-S1-T2", "Streaming CSV serializer", ["internal/reporting/csv.go", "docs/csv.md"]);

  it("matches by ID, title, or any target file, ignoring case", () => {
    expect(subtaskMatches(st, "e1-s1-t2")).toBe(true);
    expect(subtaskMatches(st, "SERIALIZER")).toBe(true);
    expect(subtaskMatches(st, "reporting/csv.go")).toBe(true);
    expect(subtaskMatches(st, "docs/csv")).toBe(true);
  });

  it("does not match other fields or absent text", () => {
    expect(subtaskMatches(st, "go test")).toBe(false);
    expect(subtaskMatches(st, "E2-")).toBe(false);
  });

  it("an empty or blank query matches everything", () => {
    expect(subtaskMatches(st, "")).toBe(true);
    expect(subtaskMatches(st, "   ")).toBe(true);
  });
});

describe("filterMatrix", () => {
  it("counts the whole matrix", () => {
    expect(countMatrix(epics)).toEqual({ epics: 2, stories: 3, subtasks: 4 });
  });

  it("returns everything, inactive, for a blank query", () => {
    const view = filterMatrix(epics, "  ");
    expect(view).toMatchObject({ matched: 4, total: 4, active: false });
    expect(view.epics).toHaveLength(2);
  });

  it("prunes epics and stories that hold no match", () => {
    const view = filterMatrix(epics, "audit/log");
    expect(view).toMatchObject({ matched: 1, total: 4, active: true });
    expect(view.epics.map((e) => e.id)).toEqual(["E2"]);
    expect(view.epics[0]?.stories.map((s) => s.id)).toEqual(["E2-S1"]);
    expect(view.epics[0]?.stories[0]?.subtasks.map((t) => t.id)).toEqual(["E2-S1-T1"]);
  });

  it("keeps only the matching subtasks inside a story", () => {
    const view = filterMatrix(epics, "csv");
    expect(view.matched).toBe(1);
    expect(view.epics[0]?.stories[0]?.subtasks.map((t) => t.id)).toEqual(["E1-S1-T2"]);
  });

  it("matches by subtask ID across epics", () => {
    const view = filterMatrix(epics, "-t1");
    expect(view.matched).toBe(3);
    expect(view.epics.map((e) => e.id)).toEqual(["E1", "E2"]);
  });

  it("reports no match as an empty, active result", () => {
    expect(filterMatrix(epics, "kubernetes")).toEqual({ epics: [], matched: 0, total: 4, active: true });
  });

  it("does not mutate its input", () => {
    const before = JSON.stringify(epics);
    filterMatrix(epics, "csv");
    expect(JSON.stringify(epics)).toBe(before);
  });
});

describe("allOpenIds", () => {
  it("lists every epic and story, for Expand all", () => {
    const ids = allOpenIds(epics);
    expect([...ids.epics]).toEqual(["E1", "E2"]);
    expect([...ids.stories]).toEqual(["E1-S1", "E2-S1", "E2-S2"]);
  });

  it("scopes to the filtered set", () => {
    const ids = allOpenIds(filterMatrix(epics, "purge").epics);
    expect([...ids.epics]).toEqual(["E2"]);
    expect([...ids.stories]).toEqual(["E2-S2"]);
  });
});
