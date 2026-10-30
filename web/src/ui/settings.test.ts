import { describe, expect, it } from "vitest";

import {
  clampMaxGapAgents,
  gapAgentLabel,
  MAX_GAP_AGENTS_CHOICES,
  MAX_GAP_AGENTS_DEFAULT,
  parseSettingsDocument,
  settingsErrorMessage,
  withMaxGapAgents,
} from "./settings";

describe("clampMaxGapAgents", () => {
  it("keeps a legal value and clamps one outside 0–3", () => {
    expect(clampMaxGapAgents(0)).toBe(0);
    expect(clampMaxGapAgents(3)).toBe(3);
    expect(clampMaxGapAgents(4)).toBe(3);
    expect(clampMaxGapAgents(-1)).toBe(0);
  });

  it("truncates a fraction and falls back for a non-number", () => {
    expect(clampMaxGapAgents(2.7)).toBe(2);
    expect(clampMaxGapAgents(Number.NaN)).toBe(MAX_GAP_AGENTS_DEFAULT);
    expect(clampMaxGapAgents(Number.POSITIVE_INFINITY)).toBe(MAX_GAP_AGENTS_DEFAULT);
  });
});

describe("gapAgentLabel", () => {
  it("names the zero case and counts the rest", () => {
    expect(MAX_GAP_AGENTS_CHOICES.map(gapAgentLabel)).toEqual(["None", "1 agent", "2 agents", "3 agents"]);
  });
});

describe("parseSettingsDocument", () => {
  it("reads the revision and keeps every other key", () => {
    const doc = parseSettingsDocument({
      revision: 7,
      max_gap_agents: 1,
      main_host: "claude",
      role_bindings: [{ role: "architect", model: "opus" }],
    });

    expect(doc).toEqual({
      revision: 7,
      max_gap_agents: 1,
      main_host: "claude",
      role_bindings: [{ role: "architect", model: "opus" }],
    });
  });

  it("clamps a limit the server should not have sent", () => {
    expect(parseSettingsDocument({ revision: 1, max_gap_agents: 9 })?.max_gap_agents).toBe(3);
  });

  it("leaves the limit absent when the build has none", () => {
    const doc = parseSettingsDocument({ revision: 1, main_host: "claude" });

    expect(doc).not.toBeNull();
    expect(doc?.max_gap_agents).toBeUndefined();
    expect(doc?.main_host).toBe("claude");
  });

  it("rejects anything that is not a settings document", () => {
    expect(parseSettingsDocument(null)).toBeNull();
    expect(parseSettingsDocument("nope")).toBeNull();
    expect(parseSettingsDocument([{ revision: 1 }])).toBeNull();
    expect(parseSettingsDocument({ max_gap_agents: 2 })).toBeNull();
    expect(parseSettingsDocument({ revision: "1" })).toBeNull();
    expect(parseSettingsDocument({ revision: Number.NaN })).toBeNull();
  });
});

describe("withMaxGapAgents", () => {
  it("replaces the limit and carries the revision and other keys through", () => {
    const doc = withMaxGapAgents({ revision: 4, max_gap_agents: 1, main_host: "claude" }, 3);

    expect(doc).toEqual({ revision: 4, max_gap_agents: 3, main_host: "claude" });
  });

  it("clamps before writing, so a save can never be out of range", () => {
    expect(withMaxGapAgents({ revision: 4, max_gap_agents: 1 }, 12).max_gap_agents).toBe(3);
    expect(withMaxGapAgents({ revision: 4, max_gap_agents: 1 }, -3).max_gap_agents).toBe(0);
  });

  it("adds the key when the loaded document had none", () => {
    expect(withMaxGapAgents({ revision: 4 }, 2).max_gap_agents).toBe(2);
  });
});

describe("settingsErrorMessage", () => {
  it("names the two statuses that mean something specific", () => {
    expect(settingsErrorMessage(404, undefined)).toBe("This build has no settings endpoint.");
    expect(settingsErrorMessage(409, undefined)).toBe(
      "The settings changed elsewhere; reload them before saving.",
    );
  });

  it("prefers the server's own words and falls back to the status", () => {
    expect(settingsErrorMessage(400, "max_gap_agents must be between 0 and 3, got 5")).toBe(
      "max_gap_agents must be between 0 and 3, got 5",
    );
    expect(settingsErrorMessage(400, "")).toBe("The bridge refused the request (400).");
    expect(settingsErrorMessage(500, undefined)).toBe("The bridge refused the request (500).");
  });
});
