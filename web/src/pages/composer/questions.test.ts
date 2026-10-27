import { describe, expect, it } from "vitest";

import { MAX_AMBIGUITY_NOTE } from "../../store/syncStore";
import { ambiguityRefusal, resolveAmbiguity } from "../../store/syncStore";

/**
 * The reducer accepts AMBIGUITY_RESOLVE from the owner, during ingestion, once
 * per question. The window must refuse the same cases locally, with a reason,
 * instead of sending a command the session will reject.
 *
 * These tests exercise the refusal rules directly; they do not open a socket.
 */
describe("answering a classification question", () => {
  it("refuses an answer from someone who is not the owner", () => {
    expect(ambiguityRefusal("amb-1", "")).toContain("owner");
  });

  it("refuses an empty id before anything is sent", () => {
    expect(ambiguityRefusal("  ", "")).toContain("id");
  });

  it("refuses a note longer than the bound", () => {
    const long = "x".repeat(MAX_AMBIGUITY_NOTE + 1);
    expect(ambiguityRefusal("amb-1", long)).toContain("characters");
  });

  it("never throws when the store has no session yet", () => {
    expect(() => {
      resolveAmbiguity("amb-1", "product");
    }).not.toThrow();
  });
});
