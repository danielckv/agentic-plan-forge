import { describe, expect, it } from "vitest";
import { byRole, byVendor, formatDuration, formatTokens, requestShare } from "./usage";
import type { UsageReport, UsageRow } from "../protocol/api";

function row(part: Partial<UsageRow>): UsageRow {
  return {
    host: "claude",
    model: "opus",
    role: "architect",
    requests: 1,
    failures: 0,
    input_tokens: 0,
    output_tokens: 0,
    milliseconds: 0,
    ...part,
  };
}

function report(rows: UsageRow[]): UsageReport {
  const totals = row({ host: "", model: "", role: "", requests: 0 });
  for (const r of rows) {
    totals.requests += r.requests;
    totals.failures += r.failures;
    totals.input_tokens += r.input_tokens;
    totals.output_tokens += r.output_tokens;
    totals.milliseconds += r.milliseconds;
  }
  return { rows, totals };
}

describe("byVendor", () => {
  it("sums the rows of one host and lists the models it served", () => {
    const got = byVendor(
      report([
        row({ host: "claude", model: "opus", requests: 3, input_tokens: 1000, output_tokens: 200, milliseconds: 9000 }),
        row({ host: "claude", model: "haiku", role: "critic", requests: 1, input_tokens: 500, milliseconds: 1000 }),
        row({ host: "omp", model: "opus", requests: 2, input_tokens: 100, failures: 1 }),
      ]),
    );
    expect(got.map((v) => v.host)).toEqual(["claude", "omp"]);
    expect(got[0]).toMatchObject({ requests: 4, inputTokens: 1500, outputTokens: 200, milliseconds: 10000 });
    expect(got[0]?.models).toEqual(["opus", "haiku"]);
    expect(got[1]).toMatchObject({ requests: 2, failures: 1 });
  });

  it("has nothing to show before the first call", () => {
    expect(byVendor(report([]))).toEqual([]);
  });
});

describe("byRole", () => {
  it("sums the rows of one role across hosts", () => {
    const got = byRole(
      report([
        row({ host: "claude", role: "architect", requests: 3 }),
        row({ host: "omp", role: "architect", requests: 1 }),
        row({ host: "claude", role: "pm", requests: 2 }),
      ]),
    );
    expect(got.map((r) => [r.role, r.requests])).toEqual([
      ["architect", 4],
      ["pm", 2],
    ]);
  });
});

describe("formatTokens", () => {
  it("reads at a glance", () => {
    expect(formatTokens(940)).toBe("940");
    expect(formatTokens(12_400)).toBe("12.4k");
    expect(formatTokens(3_100_000)).toBe("3.1M");
  });
});

describe("formatDuration", () => {
  it("reads at a glance", () => {
    expect(formatDuration(820)).toBe("820ms");
    expect(formatDuration(4200)).toBe("4.2s");
    expect(formatDuration(185_000)).toBe("3m 05s");
  });
});

describe("requestShare", () => {
  it("is a percentage, and zero before anything ran", () => {
    expect(requestShare(3, 12)).toBe(25);
    expect(requestShare(0, 0)).toBe(0);
  });
});
