import { describe, expect, it } from "vitest";

import type { RefreshState } from "../../store/syncStore";
import { refreshLabel, refreshNote } from "./refreshButton";

/**
 * The composer's Refresh button re-reads the document once and switches the
 * automatic path off. These tests pin the wording, because the note is the only
 * place a writer learns why the plan stopped following them.
 */

function state(over: Partial<RefreshState> = {}): RefreshState {
  return { automatic: true, automaticOff: false, refreshes: 1, maxRefreshes: 4, wordsRemaining: 30, ...over };
}

describe("refreshLabel", () => {
  it("refuses to read an empty document", () => {
    const got = refreshLabel(state(), false);
    expect(got.disabled).toBe(true);
    expect(got.hint).toContain("first");
  });

  it("says the press switches the automatic path off", () => {
    const got = refreshLabel(state(), true);
    expect(got.disabled).toBe(false);
    expect(got.hint).toContain("switches automatic refresh off");
  });

  it("still works after the owner switched the automatic path off", () => {
    const got = refreshLabel(state({ automatic: false, automaticOff: true }), true);
    expect(got.disabled).toBe(false);
    expect(got.hint).toContain("re-reads the document now");
  });
});

describe("refreshNote", () => {
  it("says nothing when the bridge is too old to report the path", () => {
    expect(refreshNote(undefined)).toBeNull();
  });

  it("promises the first read at a stated length, not at a word count", () => {
    // Before the first read the note states the threshold itself. Reporting a
    // number here would claim the executor had already read the document.
    expect(refreshNote(state({ refreshes: 0, wordsRemaining: 60 }))).toContain("past 100 words");
  });

  it("names the remaining words, singular and plural", () => {
    expect(refreshNote(state({ wordsRemaining: 30 }))).toContain("30 more words");
    expect(refreshNote(state({ wordsRemaining: 1 }))).toContain("1 more word");
  });

  it("says the automatic path is finished once the reads are spent", () => {
    const note = refreshNote(state({ automatic: false, refreshes: 4 }));
    expect(note).toContain("done");
    expect(note).toContain("Refresh plan");
  });

  it("says the owner switched it off, and that the button still works", () => {
    const note = refreshNote(state({ automatic: false, automaticOff: true }));
    expect(note).toContain("off for this session");
    expect(note).toContain("Refresh plan");
  });
});
