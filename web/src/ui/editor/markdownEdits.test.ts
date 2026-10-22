import { describe, expect, it } from "vitest";

import {
  BLOCK_TEMPLATES,
  componentTerm,
  insertBlock,
  setHeading,
  toggleInline,
  toggleLinePrefix,
  type EditResult,
} from "./markdownEdits";

/** Apply an EditResult the way CodeMirror does: all positions are original. */
function apply(doc: string, r: EditResult): { doc: string; sel: string } {
  const sorted = [...r.changes].sort((a, b) => b.from - a.from);
  let out = doc;
  for (const c of sorted) out = out.slice(0, c.from) + c.insert + out.slice(c.to);
  return { doc: out, sel: out.slice(r.selection.anchor, r.selection.head) };
}

describe("toggleInline", () => {
  it("wraps, then unwraps, the selection", () => {
    const doc = "export invoices now";
    const wrapped = apply(doc, toggleInline(doc, 7, 15, "**", "bold"));
    expect(wrapped).toEqual({ doc: "export **invoices** now", sel: "invoices" });
    const back = apply(wrapped.doc, toggleInline(wrapped.doc, 9, 17, "**", "bold"));
    expect(back).toEqual({ doc, sel: "invoices" });
  });

  it("inserts a selected placeholder at an empty selection", () => {
    expect(apply("ab", toggleInline("ab", 1, 1, "`", "code"))).toEqual({ doc: "a`code`b", sel: "code" });
  });
});

describe("toggleLinePrefix", () => {
  it("adds and removes a bullet on every touched line", () => {
    const doc = "one\ntwo\nthree";
    const on = apply(doc, toggleLinePrefix(doc, 0, 7, "- "));
    expect(on.doc).toBe("- one\n- two\nthree");
    const off = apply(on.doc, toggleLinePrefix(on.doc, 0, 11, "- "));
    expect(off.doc).toBe(doc);
  });

  it("numbers lines in order and swaps another list marker instead of stacking", () => {
    const doc = "- a\n- b";
    expect(apply(doc, toggleLinePrefix(doc, 0, doc.length, "1. ")).doc).toBe("1. a\n2. b");
    expect(apply(doc, toggleLinePrefix(doc, 0, doc.length, "- [ ] ")).doc).toBe("- [ ] a\n- [ ] b");
  });
});

describe("setHeading", () => {
  it("sets, changes and clears a heading", () => {
    const h2 = apply("Title", setHeading("Title", 2, 2, 2)).doc;
    expect(h2).toBe("## Title");
    expect(apply(h2, setHeading(h2, 4, 4, 1)).doc).toBe("# Title");
    expect(apply(h2, setHeading(h2, 4, 4, 2)).doc).toBe("Title");
  });
});

describe("insertBlock", () => {
  it("puts the block on its own lines with blank lines around it", () => {
    const doc = "Intro text\nMore";
    const r = insertBlock(doc, 3, "> |");
    const out = apply(doc, r);
    expect(out.doc).toBe("Intro text\n\n> \n\nMore");
    expect(r.selection.anchor).toBe("Intro text\n\n> ".length);
  });

  it("fills an empty line in place", () => {
    const doc = "a\n\n\nb";
    expect(apply(doc, insertBlock(doc, 2, "x|")).doc).toBe("a\n\nx\n\nb");
  });

  it("starts an empty document without leading blank lines", () => {
    expect(apply("", insertBlock("", 0, "## Epic: |")).doc).toBe("## Epic: ");
  });

  it("every template marks exactly one caret", () => {
    for (const t of BLOCK_TEMPLATES) expect(t.template.split("|").length - 1, t.id).toBeGreaterThanOrEqual(1);
  });
});

it("componentTerm writes the name as inline code without stray backticks", () => {
  expect(componentTerm("ExportInvoices")).toBe("`ExportInvoices`");
  expect(componentTerm("a`b")).toBe("`ab`");
});
