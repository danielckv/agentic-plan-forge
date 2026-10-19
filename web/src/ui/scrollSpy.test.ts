import { describe, expect, it } from "vitest";

import { pickActiveSection, SPY_OFFSET_PX } from "./scrollSpy";
import type { ScrollMetrics, SectionTop } from "./scrollSpy";

const sections: SectionTop[] = [
  { id: "executive-summary", top: 0 },
  { id: "architecture", top: 400 },
  { id: "constraints", top: 900 },
  { id: "matrix", top: 1500 },
  { id: "schedule", top: 2400 },
];

/** A 600px-tall scroller over 3000px of content. */
const at = (scrollTop: number): ScrollMetrics => ({ scrollTop, clientHeight: 600, scrollHeight: 3000 });

describe("pickActiveSection", () => {
  it("is null with no sections", () => {
    expect(pickActiveSection([], at(0))).toBeNull();
  });

  it("starts on the first section at the top", () => {
    expect(pickActiveSection(sections, at(0))).toBe("executive-summary");
  });

  it("stays on the first section while it is above the first one's top", () => {
    const lower: SectionTop[] = [{ id: "a", top: 300 }, { id: "b", top: 900 }];
    expect(pickActiveSection(lower, at(0))).toBe("a");
  });

  it("switches when a section's top reaches the activation line", () => {
    const line = SPY_OFFSET_PX;
    // Architecture starts at 400: it activates at scrollTop 400 - offset.
    expect(pickActiveSection(sections, at(400 - line - 1))).toBe("executive-summary");
    expect(pickActiveSection(sections, at(400 - line))).toBe("architecture");
    expect(pickActiveSection(sections, at(899 - line))).toBe("architecture");
    expect(pickActiveSection(sections, at(900 - line))).toBe("constraints");
  });

  it("follows a custom activation offset", () => {
    expect(pickActiveSection(sections, at(350), 0)).toBe("executive-summary");
    expect(pickActiveSection(sections, at(400), 0)).toBe("architecture");
    expect(pickActiveSection(sections, at(300), 100)).toBe("architecture");
  });

  it("is order-independent: sections may arrive unsorted", () => {
    const shuffled = [sections[3]!, sections[0]!, sections[4]!, sections[1]!, sections[2]!];
    expect(pickActiveSection(shuffled, at(1500))).toBe("matrix");
  });

  it("picks the last section at the very bottom even if it can never reach the line", () => {
    // `schedule` starts at 2400, but the scroller tops out at 3000 - 600 = 2400
    // minus the offset: without the bottom rule `matrix` would stay active.
    const nearEnd: SectionTop[] = [...sections.slice(0, 4), { id: "schedule", top: 2650 }];
    expect(pickActiveSection(nearEnd, at(2400))).toBe("schedule");
    expect(pickActiveSection(nearEnd, at(2300))).toBe("matrix");
  });

  it("does not force the last section when the content does not scroll", () => {
    const short: ScrollMetrics = { scrollTop: 0, clientHeight: 800, scrollHeight: 800 };
    expect(pickActiveSection(sections.slice(0, 2), short)).toBe("executive-summary");
  });

  it("ignores sub-pixel slack at the bottom", () => {
    const m: ScrollMetrics = { scrollTop: 2397, clientHeight: 600, scrollHeight: 3000 };
    expect(pickActiveSection(sections, m)).toBe("schedule");
  });
});
