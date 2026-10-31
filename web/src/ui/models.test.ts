import { describe, expect, it } from "vitest";

import { modelPins, parseModelCatalog, pinSelects } from "./models";
import { parseSettingsDocument } from "./settings";
import type { SettingsDocument } from "./settings";

function settings(value: unknown): SettingsDocument {
  const doc = parseSettingsDocument(value);
  if (doc === null) throw new Error("fixture is not a settings document");
  return doc;
}

describe("parseModelCatalog", () => {
  it("reads a live list and a stale fallback entry", () => {
    const catalogue = parseModelCatalog([
      { host: "claude", label: "Anthropic API", models: [{ id: "claude-opus-4-7", label: "Opus" }] },
      {
        host: "omp",
        label: "OpenModel API",
        models: null,
        fallback: [{ id: "omp-flash", label: "omp-flash" }],
        stale: true,
        error: "dial tcp: timeout",
      },
    ]);

    expect(catalogue).toEqual([
      { host: "claude", label: "Anthropic API", models: [{ id: "claude-opus-4-7", label: "Opus" }] },
      {
        host: "omp",
        label: "OpenModel API",
        models: [],
        fallback: [{ id: "omp-flash", label: "omp-flash" }],
        stale: true,
        error: "dial tcp: timeout",
      },
    ]);
  });

  it("rejects anything that is not a catalogue", () => {
    expect(parseModelCatalog(null)).toBeNull();
    expect(parseModelCatalog({ host: "claude" })).toBeNull();
    expect(parseModelCatalog([{ label: "Anthropic API", models: [] }])).toBeNull();
    expect(parseModelCatalog([{ host: "claude", label: "x", models: [{ id: 1, label: "y" }] }])).toBeNull();
  });
});

describe("modelPins", () => {
  it("lists the two defaults and every pinned role, in display order", () => {
    const pins = modelPins(
      settings({
        revision: 3,
        main_host: "claude",
        judge_host: "omp",
        default_main_model: "claude-opus-4-7",
        default_judge_model: "omp-flash",
        role_models: {
          pm: { host: "claude", model: "claude-haiku" },
          critic: { host: "omp" },
        },
      }),
    );

    expect(pins).toEqual([
      { key: "default_main_model", label: "Default main model", host: "claude", model: "claude-opus-4-7" },
      { key: "default_judge_model", label: "Default judge model", host: "omp", model: "omp-flash" },
      { key: "role_models.pm", label: "Role: pm", host: "claude", model: "claude-haiku" },
      { key: "role_models.critic", label: "Role: critic", host: "omp", model: "" },
    ]);
  });

  it("skips empty bindings and malformed values", () => {
    const pins = modelPins(
      settings({
        revision: 1,
        role_models: { pm: {}, critic: "not-a-binding", synthesizer: { model: "omp-flash" } },
      }),
    );
    expect(pins).toEqual([{ key: "role_models.synthesizer", label: "Role: synthesizer", host: "", model: "omp-flash" }]);
    expect(modelPins(null)).toEqual([]);
  });
});

describe("pinSelects", () => {
  it("matches a hostless pin on every host and a pinned host only on its own", () => {
    const hostless = { key: "default_main_model", label: "", host: "", model: "m1" };
    expect(pinSelects(hostless, "claude", "m1")).toBe(true);
    expect(pinSelects(hostless, "omp", "m1")).toBe(true);

    const pinned = { key: "role_models.pm", label: "", host: "claude", model: "m1" };
    expect(pinSelects(pinned, "claude", "m1")).toBe(true);
    expect(pinSelects(pinned, "omp", "m1")).toBe(false);

    expect(pinSelects({ key: "role_models.critic", label: "", host: "omp", model: "" }, "omp", "m1")).toBe(false);
  });
});
