import { describe, expect, it } from "vitest";

import { phaseNote, phaseSteps } from "./phase";
import type { Phase } from "../store/syncStore";

const states = (phase: Phase): string[] => phaseSteps(phase).map((s) => s.state);

describe("phase stepper model", () => {
  it("always lists Ingestion -> Deliberation -> Synthesis -> Approved", () => {
    expect(phaseSteps("INGESTION").map((s) => s.label)).toEqual(["Ingestion", "Deliberation", "Synthesis", "Approved"]);
  });

  it("marks done, current, and upcoming steps for each phase", () => {
    expect(states("INGESTION")).toEqual(["current", "upcoming", "upcoming", "upcoming"]);
    expect(states("DELIBERATION")).toEqual(["done", "current", "upcoming", "upcoming"]);
    expect(states("SYNTHESIS")).toEqual(["done", "done", "current", "upcoming"]);
    expect(states("APPROVED")).toEqual(["done", "done", "done", "current"]);
  });

  it("keeps an in-flight approval on Synthesis", () => {
    expect(states("APPROVING")).toEqual(states("SYNTHESIS"));
    expect(states("APPROVING_UNKNOWN")).toEqual(states("SYNTHESIS"));
  });

  it("returns a cancelled session to Ingestion", () => {
    expect(states("CANCELLED")).toEqual(states("INGESTION"));
  });

  it("has exactly one current step in every phase", () => {
    const all: Phase[] = ["INGESTION", "DELIBERATION", "SYNTHESIS", "APPROVING", "APPROVING_UNKNOWN", "APPROVED", "CANCELLED"];
    for (const phase of all) {
      expect(phaseSteps(phase).filter((s) => s.state === "current")).toHaveLength(1);
    }
  });

  it("marks only the Approved step complete, and only once approved", () => {
    expect(phaseSteps("APPROVED").map((s) => s.complete)).toEqual([false, false, false, true]);
    expect(phaseSteps("SYNTHESIS").some((s) => s.complete)).toBe(false);
  });

  it("notes the phases the four steps cannot express", () => {
    expect(phaseNote("APPROVING")).toEqual({ label: "Approving", tone: "accent" });
    expect(phaseNote("APPROVING_UNKNOWN")).toEqual({ label: "Approving", tone: "accent" });
    expect(phaseNote("CANCELLED")).toEqual({ label: "Cancelled", tone: "warn" });
    expect(phaseNote("SYNTHESIS")).toBeNull();
    expect(phaseNote("APPROVED")).toBeNull();
  });
});
