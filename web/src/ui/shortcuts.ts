/**
 * Global keyboard shortcuts. Single printable keys never fire while the user
 * is typing (inputs, the CodeMirror document, any contenteditable), and never
 * with Ctrl, Cmd, or Alt held, so they cannot shadow browser or editor chords.
 */

export type ShortcutAction = "toggle-sidebar" | "help" | "focus-filter";

/** Dispatched on `window` for `/`; Page 3 cancels it after focusing its filter. */
export const FOCUS_FILTER_EVENT = "pf:focus-filter";

export interface ShortcutKeyEvent {
  key: string;
  ctrlKey: boolean;
  metaKey: boolean;
  altKey: boolean;
  target: EventTarget | null;
}

export function isTypingTarget(target: EventTarget | null): boolean {
  if (!(target instanceof Element)) return false;
  const tag = target.tagName;
  if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return true;
  // `isContentEditable` is unimplemented in some DOMs; the attribute is not.
  return (
    (target as HTMLElement).isContentEditable === true ||
    target.closest("[contenteditable]:not([contenteditable='false']), .cm-editor") !== null
  );
}

const ACTION_BY_KEY: Record<string, ShortcutAction> = {
  "[": "toggle-sidebar",
  "?": "help",
  "/": "focus-filter",
};

export function shortcutFor(e: ShortcutKeyEvent): ShortcutAction | null {
  if (e.ctrlKey || e.metaKey || e.altKey) return null;
  if (isTypingTarget(e.target)) return null;
  return ACTION_BY_KEY[e.key] ?? null;
}

export interface ShortcutHelpEntry {
  keys: string[];
  label: string;
  /** Where it applies, when not everywhere. */
  scope?: string;
}

export const SHORTCUT_HELP: readonly ShortcutHelpEntry[] = [
  { keys: ["["], label: "Collapse or expand the sidebar" },
  { keys: ["?"], label: "Show this keyboard help" },
  { keys: ["/"], label: "Filter the Jira matrix", scope: "Synthesis page" },
  { keys: ["Esc"], label: "Close a dialog or an expanded diagram" },
  { keys: ["Tab", "Shift+Tab"], label: "Move between controls" },
  { keys: ["Enter", "Space"], label: "Open or close a focused section" },
];
