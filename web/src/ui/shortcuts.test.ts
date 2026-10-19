// @vitest-environment jsdom
import { describe, expect, it } from "vitest";

import { isTypingTarget, SHORTCUT_HELP, shortcutFor } from "./shortcuts";
import type { ShortcutKeyEvent } from "./shortcuts";

const key = (k: string, extra: Partial<ShortcutKeyEvent> = {}): ShortcutKeyEvent => ({
  key: k,
  ctrlKey: false,
  metaKey: false,
  altKey: false,
  target: document.body,
  ...extra,
});

describe("global shortcuts", () => {
  it("maps [ ? / to their actions on the page", () => {
    expect(shortcutFor(key("["))).toBe("toggle-sidebar");
    expect(shortcutFor(key("?"))).toBe("help");
    expect(shortcutFor(key("/"))).toBe("focus-filter");
  });

  it("ignores other keys", () => {
    for (const k of ["a", "]", "Escape", "Enter", "Tab", " "]) expect(shortcutFor(key(k))).toBeNull();
  });

  it("never fires with Ctrl, Cmd, or Alt held", () => {
    expect(shortcutFor(key("[", { ctrlKey: true }))).toBeNull();
    expect(shortcutFor(key("[", { metaKey: true }))).toBeNull();
    expect(shortcutFor(key("?", { altKey: true }))).toBeNull();
  });

  it("stands down while the user is typing", () => {
    for (const tag of ["input", "textarea", "select"]) {
      const el = document.createElement(tag);
      expect(isTypingTarget(el)).toBe(true);
      expect(shortcutFor(key("[", { target: el }))).toBeNull();
    }
  });

  it("treats the CodeMirror document and contenteditable as typing", () => {
    const cm = document.createElement("div");
    cm.className = "cm-editor";
    const line = document.createElement("div");
    cm.appendChild(line);
    expect(isTypingTarget(line)).toBe(true);

    const editable = document.createElement("div");
    editable.setAttribute("contenteditable", "true");
    const inner = document.createElement("span");
    editable.appendChild(inner);
    expect(isTypingTarget(inner)).toBe(true);
    expect(shortcutFor(key("/", { target: inner }))).toBeNull();
  });

  it("does not treat buttons, links, or the body as typing", () => {
    expect(isTypingTarget(document.createElement("button"))).toBe(false);
    expect(isTypingTarget(document.body)).toBe(false);
    expect(isTypingTarget(null)).toBe(false);
  });

  it("lists every mapped key in the help overlay", () => {
    const listed = SHORTCUT_HELP.flatMap((s) => s.keys);
    for (const k of ["[", "?", "/", "Esc"]) expect(listed).toContain(k);
  });
});
