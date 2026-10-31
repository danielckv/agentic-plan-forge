import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { SetupContent } from "./CapabilitiesDialog";
import { ModelsPanel } from "./ModelsSection";
import type { ModelCatalogEntry } from "../ui/models";
import { parseSettingsDocument } from "../ui/settings";
import type { CapabilitiesView } from "../ui/capabilities";

/**
 * The repo has no DOM test harness, so the Setup dialog is asserted as static
 * markup (see PlanTree.test.tsx): SetupContent without the portal, the Models
 * panel with the documents the bridge would have answered.
 */
const settings = parseSettingsDocument({
  revision: 3,
  main_host: "claude",
  judge_host: "omp",
  default_main_model: "claude-opus-4-7",
  default_judge_model: "omp-flash",
  role_models: { pm: { host: "claude", model: "claude-haiku" }, critic: { host: "omp" } },
});
if (settings === null) throw new Error("fixture is not a settings document");

const catalogue: ModelCatalogEntry[] = [
  {
    host: "claude",
    label: "Anthropic API",
    models: [
      { id: "claude-opus-4-7", label: "claude-opus-4-7" },
      { id: "claude-haiku", label: "Claude Haiku" },
      { id: "claude-sonnet", label: "Claude Sonnet" },
    ],
  },
  {
    host: "omp",
    label: "OpenModel API",
    models: [],
    fallback: [{ id: "omp-flash", label: "omp-flash" }],
    stale: true,
    error: "dial tcp: timeout",
  },
];

const view: CapabilitiesView = { hosts: [], tools: [], skills: [], hooks: [], pinned: [] };

describe("SetupContent", () => {
  it("offers a General and a Models tab, opening on General", () => {
    const html = renderToStaticMarkup(<SetupContent view={view} onRefresh={() => {}} />);
    expect(html).toContain('role="tablist"');
    expect(html).toContain("General");
    expect(html).toContain("Models");
    // The General panel holds the capability sections.
    expect(html).toContain("Generation hosts");
  });

  it("renders the Models tab when it is the one opened", () => {
    const html = renderToStaticMarkup(<SetupContent view={view} onRefresh={() => {}} initialTab="models" />);
    expect(html).toContain('aria-selected="true"');
    // Before the bridge answers, the panel says what it is reading.
    expect(html).toContain("Reading the settings…");
    expect(html).toContain("Reading the models…");
  });
});

describe("ModelsPanel", () => {
  it("lists the current selections exactly as the settings document carries them", () => {
    const html = renderToStaticMarkup(<ModelsPanel settings={settings} catalogue={catalogue} message={null} />);
    expect(html).toContain("Current selections");
    expect(html).toContain("default_main_model");
    expect(html).toContain("default_judge_model");
    expect(html).toContain("role_models.pm");
    expect(html).toContain("role_models.critic");
    // A role pinned to a host with no model leaves the choice to the host.
    expect(html).toContain("the host picks its own default");
  });

  it("lists the models per host and marks the pinned ones", () => {
    const html = renderToStaticMarkup(<ModelsPanel settings={settings} catalogue={catalogue} message={null} />);
    expect(html).toContain("Anthropic API");
    expect(html).toContain("OpenModel API");
    expect(html).toContain("claude-sonnet");
    // The stale entry shows its fallback and says why it is unverified.
    expect(html).toContain("unverified list");
    expect(html).toContain("dial tcp: timeout");
    expect(html).toContain("omp-flash");
    // Marked: claude-opus-4-7 (main default + its host), claude-haiku (pm),
    // omp-flash (judge default). claude-sonnet is served but not pinned.
    expect(html.match(/data-selected="true"/g)).toHaveLength(3);
  });

  it("says so when nothing is pinned", () => {
    const empty = parseSettingsDocument({ revision: 1 });
    if (empty === null) throw new Error("fixture is not a settings document");
    const html = renderToStaticMarkup(<ModelsPanel settings={empty} catalogue={[]} message={null} />);
    expect(html).toContain("No models pinned");
    expect(html).not.toContain("data-selected");
  });

  it("shows the bridge's answer when the reads fail", () => {
    const html = renderToStaticMarkup(
      <ModelsPanel settings={null} catalogue={null} message="The binary did not answer." />,
    );
    expect(html).toContain("The binary did not answer.");
  });
});
