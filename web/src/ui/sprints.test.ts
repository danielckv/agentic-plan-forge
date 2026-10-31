import { describe, expect, it } from "vitest";

import { boardTotals, columnsOf, effortOf, fillPercent, formatEffort } from "./sprints";
import type { Sprint, SprintBoard, SprintItem } from "./sprints";

function item(id: string, hours: number, minutes: number): SprintItem {
  return { task_id: id, title: "task " + id, human_hours: hours, agent_minutes: minutes, targets: 0 };
}

function sprint(number: number, name: string, items: SprintItem[], committed: number, capacity?: number): Sprint {
  const base: Sprint = {
    number,
    name,
    items,
    human_hours: committed,
    agent_minutes: committed,
    committed,
  };
  return capacity === undefined ? base : { ...base, capacity };
}

const board: SprintBoard = {
  basis: "human",
  unit: "hours",
  sprint_days: 10,
  workday_hours: 9,
  team_size: 1,
  max_parallel: 4,
  horizon: 2,
  capacity: 90,
  sprints: [sprint(1, "Sprint 1", [item("T-1", 8, 48)], 8, 90), sprint(2, "Sprint 2", [], 0, 90)],
  backlog: sprint(0, "Backlog", [item("T-2", 4, 24), item("T-3", 4, 24)], 8),
  disclaimer: "model estimate, not measured",
};

describe("effortOf", () => {
  it("reads the estimate the basis names", () => {
    expect(effortOf(item("T-1", 8, 48), "human")).toBe(8);
    expect(effortOf(item("T-1", 8, 48), "agent")).toBe(48);
  });
});

describe("formatEffort", () => {
  it("renders human work in hours, to one decimal", () => {
    expect(formatEffort(8, "human")).toBe("8h");
    expect(formatEffort(8.25, "human")).toBe("8.3h");
  });

  // "80 minutes" is a number the reader has to convert; "1h 20m" is not.
  it("renders agent work past an hour as hours and minutes", () => {
    expect(formatEffort(48, "agent")).toBe("48m");
    expect(formatEffort(80, "agent")).toBe("1h 20m");
    expect(formatEffort(120, "agent")).toBe("2h");
  });

  it("refuses to render a value that is not a usable number", () => {
    expect(formatEffort(Number.NaN, "human")).toBe("—");
    expect(formatEffort(-1, "agent")).toBe("—");
  });
});

describe("fillPercent", () => {
  it("is the share of capacity used", () => {
    expect(fillPercent(45, 90)).toBe(50);
    expect(fillPercent(0, 90)).toBe(0);
  });

  // The bar is clamped so it never claims to be exactly full when it is
  // overfull; `over` carries that fact instead.
  it("clamps past capacity to 100", () => {
    expect(fillPercent(180, 90)).toBe(100);
  });

  it("is zero when there is no capacity to divide by", () => {
    expect(fillPercent(10, 0)).toBe(0);
    expect(fillPercent(10, Number.NaN)).toBe(0);
  });
});

describe("columnsOf", () => {
  it("puts the backlog first, then the sprints in order", () => {
    expect(columnsOf(board).map((c) => c.name)).toEqual(["Backlog", "Sprint 1", "Sprint 2"]);
  });
});

describe("boardTotals", () => {
  it("counts committed work apart from the backlog", () => {
    expect(boardTotals(board)).toEqual({ items: 3, committed: 1, backlog: 2 });
  });
});
