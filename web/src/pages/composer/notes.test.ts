import { expect, it } from "vitest";

import { notePreview } from "./NotesList";

it("notePreview shows prose without Markdown markers", () => {
  expect(notePreview("# ADR 7: Exports stream\n\nEvery export streams.\n- [ ] keep memory flat\n> quoted\n1. first")).toBe(
    "Every export streams. keep memory flat quoted first",
  );
  expect(notePreview("x".repeat(500))).toHaveLength(220);
});
