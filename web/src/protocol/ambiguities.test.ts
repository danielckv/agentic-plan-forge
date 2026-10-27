import { describe, expect, it } from "vitest";

import { MAX_AMBIGUITIES, parseNormalizedPRD } from "./drafts";

/**
 * Every document arrives from a model, so the reader must be total: a missing
 * field, a wrong type or a duplicated id cannot be allowed to break the page
 * that has to show the questions.
 */
describe("parseNormalizedPRD", () => {
  it("reads the questions a document raises", () => {
    const got = parseNormalizedPRD({
      ambiguities: [
        { id: "amb-1", text: "The invoice export", question: "Which format?", guess: "CSV" },
        { id: "amb-2", text: "Retention", question: "How long are invoices kept?", guess: "7 years" },
      ],
    });
    expect(got).toHaveLength(2);
    expect(got[0]).toEqual({ id: "amb-1", text: "The invoice export", question: "Which format?", guess: "CSV" });
  });

  it("keeps the questions apart from the product decisions they are not", () => {
    const got = parseNormalizedPRD({ ambiguities: [{ id: "x", question: "Choose a format", text: "", guess: "" }] });
    expect(got[0]?.question).toBe("Choose a format");
    expect(got[0]?.text).toBe("");
  });

  it("drops an entry with no id or no question, rather than throwing", () => {
    const got = parseNormalizedPRD({
      ambiguities: [
        { question: "No id here" },
        { id: "amb-2" },
        { id: "   ", question: "Blank id" },
        null,
        "not an object",
      ],
    });
    expect(got).toEqual([]);
  });

  it("keeps one entry per id: the same question twice is one question", () => {
    const got = parseNormalizedPRD({
      ambiguities: [
        { id: "amb-1", question: "First wording" },
        { id: "amb-1", question: "Second wording" },
      ],
    });
    expect(got).toHaveLength(1);
    expect(got[0]?.question).toBe("First wording");
  });

  it("caps a document that raises more questions than a page can show", () => {
    const many = Array.from({ length: MAX_AMBIGUITIES + 20 }, (_, i) => ({
      id: `amb-${String(i)}`,
      question: `Question ${String(i)}`,
    }));
    expect(parseNormalizedPRD({ ambiguities: many })).toHaveLength(MAX_AMBIGUITIES);
  });

  it("returns nothing for a draft that is not an object or has no list", () => {
    expect(parseNormalizedPRD(null)).toEqual([]);
    expect(parseNormalizedPRD("nope")).toEqual([]);
    expect(parseNormalizedPRD({})).toEqual([]);
    expect(parseNormalizedPRD({ ambiguities: "no" })).toEqual([]);
  });

  it("accepts the content as a JSON string, which is how a frame can arrive", () => {
    const got = parseNormalizedPRD(JSON.stringify({ ambiguities: [{ id: "a", question: "Q?" }] }));
    expect(got).toHaveLength(1);
  });
});
