import { describe, expect, it } from "vitest";

import {
  capabilitySummary,
  hostDetail,
  hostTone,
  missingTools,
  servingCount,
} from "./capabilities";
import type { CapabilitiesView, CapabilityHost, CapabilityTool } from "./capabilities";

function host(patch: Partial<CapabilityHost>): CapabilityHost {
  return { host: "claude", found: true, ready: true, serves: true, models: 3, ...patch };
}

function view(patch: Partial<CapabilitiesView>): CapabilitiesView {
  return { hosts: [], tools: [], skills: [], hooks: [], pinned: [], ...patch };
}

describe("hostTone", () => {
  it("separates a host that serves from one that is merely signed in", () => {
    expect(hostTone(host({ serves: true }))).toBe("serves");
    // The live AGY case: found, auth ready, preflight refuses.
    expect(hostTone(host({ serves: false, ready: true }))).toBe("blocked");
  });

  it("distinguishes not signed in from not installed", () => {
    expect(hostTone(host({ found: true, ready: false, serves: false }))).toBe("signed-out");
    expect(hostTone(host({ found: false, ready: false, serves: false }))).toBe("absent");
  });
});

describe("hostDetail", () => {
  // The whole reason this panel exists: the host's own refusal text, verbatim,
  // where someone will read it.
  it("shows the refusal when a signed-in host cannot serve", () => {
    const blocked = host({
      host: "agy",
      serves: false,
      ready: true,
      blocks: "capability missing for agy: AGY exposes no verified isolated settings mechanism",
    });
    expect(hostDetail(blocked)).toContain("no verified isolated settings mechanism");
  });

  it("shows the sign-in command when a host is not authenticated", () => {
    expect(hostDetail(host({ ready: false, serves: false, sign_in: "claude auth login" }))).toBe(
      "claude auth login",
    );
  });

  it("never returns an empty string", () => {
    for (const h of [
      host({}),
      host({ found: false, ready: false, serves: false }),
      host({ ready: true, serves: false }),
      host({ ready: false, serves: false }),
    ]) {
      expect(hostDetail(h).length).toBeGreaterThan(0);
    }
  });
});

describe("capabilitySummary", () => {
  it("says a run cannot start when nothing serves", () => {
    const v = view({ hosts: [host({ serves: false, ready: true })] });
    expect(capabilitySummary(v)).toContain("cannot start");
  });

  // The live installation: three hosts signed in, one able to take a call.
  it("counts rather than labelling", () => {
    const v = view({
      hosts: [
        host({ host: "agy", serves: false, ready: true }),
        host({ host: "claude", serves: true }),
        host({ host: "omp", serves: false, ready: true }),
      ],
    });
    expect(servingCount(v)).toBe(1);
    expect(capabilitySummary(v)).toBe("1 of 3 hosts can serve.");
  });

  it("reports every host serving without a caveat", () => {
    const v = view({ hosts: [host({ host: "claude" }), host({ host: "omp" })] });
    expect(capabilitySummary(v)).toBe("All 2 hosts can serve.");
  });

  it("is honest before the first answer lands", () => {
    expect(capabilitySummary(null)).toBe("Checking…");
    expect(servingCount(null)).toBe(0);
  });
});

describe("missingTools", () => {
  function tool(patch: Partial<CapabilityTool>): CapabilityTool {
    return { name: "docker", purpose: "run a preview", found: false, essential: false, ...patch };
  }

  it("lists only what is absent", () => {
    const v = view({
      tools: [tool({ name: "git", found: true }), tool({ name: "docker", found: false })],
    });
    expect(missingTools(v).map((t) => t.name)).toEqual(["docker"]);
  });

  it("is empty rather than null with no data", () => {
    expect(missingTools(null)).toEqual([]);
  });
});
