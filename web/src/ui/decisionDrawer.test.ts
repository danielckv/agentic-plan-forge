import { describe, expect, it } from "vitest";

import type { Ambiguity } from "../protocol/drafts";
import type { AmbiguityAnswer, DecisionAnswer, DecisionCard } from "../store/syncStore";
import {
  ambiguityAnswer,
  ambiguityResolveRefusal,
  decisionAnswer,
  decisionResolveRefusal,
  isRecommended,
  MAX_AMBIGUITY_NOTE,
  pendingAmbiguities,
  pendingDecisions,
  pendingQuestionCount,
  recommendedOptionId,
} from "./decisionDrawer";
import type { DrawerGate } from "./decisionDrawer";

/**
 * The drawer's rules, exercised without a socket or a rendered component: which
 * questions still wait, what the recommended option means, and the exact
 * payload each answer travels in. The reducer is the authority these rules
 * mirror (DECISION_RESOLVE needs the card's revision and an offered option;
 * AMBIGUITY_RESOLVE is owner-only, during ingestion, once per question).
 */

const card = (over: Partial<DecisionCard> = {}): DecisionCard => ({
  decisionId: "dec-1",
  question: "Which export format?",
  options: [
    { optionId: "csv", label: "CSV", tradeoff: "Simplest to inspect", impact: "Adds a nightly job" },
    { optionId: "xlsx", label: "XLSX", tradeoff: "Needs a spreadsheet library" },
  ],
  inputRevision: 7,
  recommendedOptionId: "csv",
  stale: false,
  ...over,
});

const gate = (over: Partial<DrawerGate> = {}): DrawerGate => ({
  role: "owner",
  conn: "open",
  phase: "DELIBERATION",
  decisionAnswers: {},
  ambiguityAnswers: {},
  ...over,
});

/** A card the model left no recommendation on. */
const unrecommended = (): DecisionCard => {
  const copy = card();
  delete copy.recommendedOptionId;
  return copy;
};

const question = (over: Partial<Ambiguity> = {}): Ambiguity => ({
  id: "amb-1",
  text: "The invoice export should be quick.",
  question: "How quick is quick?",
  guess: "Under a minute",
  ...over,
});

describe("which questions wait for an answer", () => {
  it("keeps the open cards and drops resolved and stale ones", () => {
    const open = card({ decisionId: "open" });
    const resolved = card({ decisionId: "resolved", resolvedOptionId: "csv", resolvedBy: "m1" });
    const stale = card({ decisionId: "stale", stale: true, resolvedOptionId: "csv" });
    expect(pendingDecisions([open, resolved, stale]).map((c) => c.decisionId)).toEqual(["open"]);
  });

  it("keeps the questions this window has not answered", () => {
    const answers: Record<string, AmbiguityAnswer> = {
      "amb-1": { choice: "product", pending: true, superseded: false },
    };
    const waiting = pendingAmbiguities([question(), question({ id: "amb-2" })], answers);
    expect(waiting.map((q) => q.id)).toEqual(["amb-2"]);
  });

  it("counts both kinds for the unresolved-count action", () => {
    const answers: Record<string, AmbiguityAnswer> = {
      "amb-1": { choice: "dismiss", pending: false, superseded: false },
    };
    expect(pendingQuestionCount([card(), card({ decisionId: "dec-2", stale: true })], [question()], answers)).toBe(1);
    expect(pendingQuestionCount([], [], {})).toBe(0);
  });
});

describe("the recommended option is a hint, not an answer", () => {
  it("marks the option the model named", () => {
    expect(recommendedOptionId(card())).toBe("csv");
    expect(isRecommended(card(), "csv")).toBe(true);
    expect(isRecommended(card(), "xlsx")).toBe(false);
  });

  it("marks nothing when the model named no option or one the card does not offer", () => {
    expect(recommendedOptionId(unrecommended())).toBeNull();
    expect(recommendedOptionId(card({ recommendedOptionId: "pdf" }))).toBeNull();
  });
});

describe("answering a decision", () => {
  it("carries the card's revision as a decimal string", () => {
    const verdict = decisionAnswer(card({ inputRevision: 12 }), "xlsx", gate());
    expect(verdict).toEqual({
      ok: true,
      payload: { decision_id: "dec-1", input_revision: "12", option_id: "xlsx" },
    });
  });

  it("refuses a card that arrived without a revision", () => {
    const verdict = decisionAnswer(card({ inputRevision: undefined }), "csv", gate());
    expect(verdict.ok).toBe(false);
    if (!verdict.ok) expect(verdict.reason).toContain("revision");
  });

  it("refuses an option that is not on the card, and an empty choice", () => {
    expect(decisionResolveRefusal(card(), "pdf", gate())).toContain("not on this decision");
    expect(decisionResolveRefusal(card(), "", gate())).toContain("Choose one of the options");
  });

  it("refuses a window that may not answer, and says why", () => {
    expect(decisionResolveRefusal(card(), "csv", gate({ role: "viewer" }))).toContain("owner or an editor");
    expect(decisionResolveRefusal(card(), "csv", gate({ role: null }))).toContain("owner or an editor");
    expect(decisionResolveRefusal(card(), "csv", gate({ conn: "offline" }))).toContain("live connection");
    expect(decisionResolveRefusal(card(), "csv", gate({ phase: "INGESTION" }))).toContain("closed in phase");
  });

  it("refuses a card another member settled or a superseded revision", () => {
    expect(decisionResolveRefusal(card({ resolvedOptionId: "csv" }), "xlsx", gate())).toContain("already resolved");
    expect(decisionResolveRefusal(card({ stale: true }), "csv", gate())).toContain("refresh");
  });

  it("refuses a second answer while the first one is in flight", () => {
    const inFlight: Record<string, DecisionAnswer> = {
      "dec-1": { optionId: "csv", pending: true, eventId: "ev-1" },
    };
    expect(decisionResolveRefusal(card(), "xlsx", gate({ decisionAnswers: inFlight }))).toContain("already sending");
  });

  it("lets the window try again after the authority rejected the first answer", () => {
    const refused: Record<string, DecisionAnswer> = {
      "dec-1": { optionId: "csv", pending: false, eventId: "ev-1", error: "STALE_RUN: revision moved" },
    };
    const verdict = decisionAnswer(card(), "csv", gate({ decisionAnswers: refused }));
    expect(verdict.ok).toBe(true);
  });

  it("is live on a fresh card in a phase that accepts answers", () => {
    expect(decisionResolveRefusal(card(), "csv", gate({ phase: "SYNTHESIS" }))).toBeNull();
    expect(decisionResolveRefusal(card(), "csv", gate({ role: "editor" }))).toBeNull();
  });
});

describe("answering a brief question", () => {
  it("sends the classification, and no note when the note is blank", () => {
    expect(ambiguityAnswer(gate({ phase: "INGESTION" }), "amb-1", "product", "   ")).toEqual({
      ok: true,
      payload: { ambiguity_id: "amb-1", choice: "product" },
    });
  });

  it("carries the note when there is one", () => {
    expect(ambiguityAnswer(gate({ phase: "INGESTION" }), "amb-1", "technical", "Cache it")).toEqual({
      ok: true,
      payload: { ambiguity_id: "amb-1", choice: "technical", note: "Cache it" },
    });
  });

  it("refuses an answer that is not the owner's to give, or is out of phase", () => {
    expect(ambiguityResolveRefusal(gate({ phase: "INGESTION", role: "editor" }), "amb-1", "")).toContain("owner");
    expect(ambiguityResolveRefusal(gate({ phase: "SYNTHESIS" }), "amb-1", "")).toContain("moved on");
    expect(ambiguityResolveRefusal(gate({ phase: "INGESTION", conn: "closed" }), "amb-1", "")).toContain(
      "live connection",
    );
  });

  it("refuses an empty id, an oversized note, and a question this window answered", () => {
    expect(ambiguityResolveRefusal(gate({ phase: "INGESTION" }), "  ", "")).toContain("id");
    const long = "x".repeat(MAX_AMBIGUITY_NOTE + 1);
    expect(ambiguityResolveRefusal(gate({ phase: "INGESTION" }), "amb-1", long)).toContain("characters");
    const answers: Record<string, AmbiguityAnswer> = {
      "amb-1": { choice: "product", pending: true, superseded: false },
    };
    expect(ambiguityResolveRefusal(gate({ phase: "INGESTION", ambiguityAnswers: answers }), "amb-1", "")).toContain(
      "already has an answer",
    );
  });
});
