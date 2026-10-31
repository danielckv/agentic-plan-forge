/**
 * Pure Markdown editing operations behind the composer toolbar and slash menu.
 * Each takes the document and a selection and returns the changes and the new
 * selection, with every position relative to the original document. The
 * editor turns the result into one CodeMirror transaction, so an operation is
 * one undo step.
 */

export interface TextChange {
  from: number;
  to: number;
  insert: string;
}

export interface EditResult {
  changes: TextChange[];
  /** The selection after the change, in positions of the new document. */
  selection: { anchor: number; head: number };
}

/**
 * Wrap the selection in an inline marker (`**`, `_`, `` ` ``), or remove the
 * marker when the selection is already wrapped. An empty selection inserts the
 * marker around a placeholder word and selects it.
 */
export function toggleInline(doc: string, from: number, to: number, marker: string, placeholder: string): EditResult {
  const m = marker.length;
  const before = doc.slice(Math.max(0, from - m), from);
  const after = doc.slice(to, to + m);
  if (before === marker && after === marker) {
    return {
      changes: [
        { from: from - m, to: from, insert: "" },
        { from: to, to: to + m, insert: "" },
      ],
      selection: { anchor: from - m, head: to - m },
    };
  }
  if (from === to) {
    return {
      changes: [{ from, to, insert: marker + placeholder + marker }],
      selection: { anchor: from + m, head: from + m + placeholder.length },
    };
  }
  return {
    changes: [
      { from, to: from, insert: marker },
      { from: to, to, insert: marker },
    ],
    selection: { anchor: from + m, head: to + m },
  };
}

/**
 * The start offset of every line the selection touches. A selection that
 * ends right after a newline does not touch the line that follows it.
 */
function lineStarts(doc: string, from: number, to: number): number[] {
  let start = doc.lastIndexOf("\n", from - 1) + 1;
  const starts = [start];
  const end = to > from && doc[to - 1] === "\n" ? to - 1 : to;
  let next = doc.indexOf("\n", start);
  while (next !== -1 && next < end) {
    start = next + 1;
    starts.push(start);
    next = doc.indexOf("\n", start);
  }
  return starts;
}

function lineAt(doc: string, start: number): string {
  const end = doc.indexOf("\n", start);
  return doc.slice(start, end === -1 ? doc.length : end);
}

const HEADING = /^#{1,6} /;
const LIST = /^(?:[-*+] \[[ xX]\] |[-*+] |\d+\. |> )/;

/**
 * Toggle a line prefix (`- `, `1. `, `- [ ] `, `> `) on every touched line.
 * When every line already has it, it is removed; otherwise lines get it and
 * lose any other list or quote prefix, so the toggle never stacks markers.
 */
export function toggleLinePrefix(doc: string, from: number, to: number, prefix: string): EditResult {
  const starts = lineStarts(doc, from, to);
  const lines = starts.map((s) => lineAt(doc, s));
  const numbered = prefix === "1. ";
  const has = (line: string): boolean => (numbered ? /^\d+\. /.test(line) : line.startsWith(prefix));
  const remove = lines.every(has);
  const changes: TextChange[] = [];
  let delta = 0;
  let firstDelta = 0;
  starts.forEach((s, i) => {
    const line = lines[i] ?? "";
    const existing = (numbered ? /^\d+\. / : null)?.exec(line)?.[0] ?? LIST.exec(line)?.[0] ?? "";
    const insert = remove ? "" : numbered ? `${i + 1}. ` : prefix;
    const cut = remove ? (numbered ? existing : prefix) : existing;
    if (cut === "" && insert === "") return;
    changes.push({ from: s, to: s + cut.length, insert });
    const d = insert.length - cut.length;
    if (i === 0) firstDelta = d;
    delta += d;
  });
  return {
    changes,
    selection: { anchor: Math.max(starts[0] ?? 0, from + firstDelta), head: Math.max(starts[0] ?? 0, to + delta) },
  };
}

/** Make the touched lines a heading of `level`, or plain text when they already are. */
export function setHeading(doc: string, from: number, to: number, level: number): EditResult {
  const marker = "#".repeat(level) + " ";
  const starts = lineStarts(doc, from, to);
  const lines = starts.map((s) => lineAt(doc, s));
  const plain = lines.every((l) => l.startsWith(marker));
  const changes: TextChange[] = [];
  let delta = 0;
  let firstDelta = 0;
  starts.forEach((s, i) => {
    const existing = HEADING.exec(lines[i] ?? "")?.[0] ?? "";
    const insert = plain ? "" : marker;
    if (existing === insert) return;
    changes.push({ from: s, to: s + existing.length, insert });
    const d = insert.length - existing.length;
    if (i === 0) firstDelta = d;
    delta += d;
  });
  return {
    changes,
    selection: { anchor: Math.max(starts[0] ?? 0, from + firstDelta), head: Math.max(starts[0] ?? 0, to + delta) },
  };
}

/**
 * Insert a block of Markdown at `pos` on its own lines, with one blank line
 * before and after it. `cursor` marks where the caret lands inside the block
 * (the first `|` in the template, which is removed).
 */
export function insertBlock(doc: string, pos: number, template: string): EditResult {
  const caret = template.indexOf("|");
  const block = caret === -1 ? template : template.slice(0, caret) + template.slice(caret + 1);
  const lineStart = doc.lastIndexOf("\n", pos - 1) + 1;
  const lineEnd = doc.indexOf("\n", pos) === -1 ? doc.length : doc.indexOf("\n", pos);
  const lineEmpty = doc.slice(lineStart, lineEnd).trim() === "";
  const at = lineEmpty ? lineStart : lineEnd;
  const textBefore = doc.slice(0, at);
  let lead = "";
  if (textBefore.length > 0) {
    if (!textBefore.endsWith("\n")) lead = "\n\n";
    else if (!textBefore.endsWith("\n\n")) lead = "\n";
  }
  const replaceTo = lineEmpty ? lineEnd : at;
  const rest = doc.slice(replaceTo);
  const trail = rest.startsWith("\n\n") || rest === "" ? "" : rest.startsWith("\n") ? "\n" : "\n\n";
  const insert = lead + block + trail;
  const caretPos = at + lead.length + (caret === -1 ? block.length : caret);
  return { changes: [{ from: at, to: replaceTo, insert }], selection: { anchor: caretPos, head: caretPos } };
}

/** The term a component inserts into the text: its name as inline code. */
export function componentTerm(name: string): string {
  return "`" + name.replace(/`/g, "") + "`";
}

/** A block the slash menu and toolbar can insert. `|` marks the caret. */
interface BlockTemplate {
  id: string;
  label: string;
  detail: string;
  template: string;
}

export const BLOCK_TEMPLATES: BlockTemplate[] = [
  {
    id: "epic",
    label: "Epic",
    detail: "A unit of work with a goal and requirements",
    template: "## Epic: |\n\nGoal: \n\n- [ ] Requirement\n- [ ] Requirement",
  },
  {
    id: "story",
    label: "User story",
    detail: "As a … I want … so that …",
    template: "As a |, I want …, so that ….\n\nAcceptance criteria:\n\n- …",
  },
  { id: "checklist", label: "Checklist", detail: "Requirements to tick off", template: "- [ ] |\n- [ ] " },
  {
    id: "table",
    label: "Table",
    detail: "Three columns",
    template: "| Column | Column | Column |\n| :--- | :--- | :--- |\n| | | |",
  },
  { id: "code", label: "Code block", detail: "Fenced code", template: "```\n|\n```" },
  {
    id: "mermaid",
    label: "Diagram",
    detail: "A Mermaid flowchart",
    template: "```mermaid\nflowchart TD\n    A[|Start] --> B[Next]\n```",
  },
  { id: "quote", label: "Quote", detail: "A quoted note", template: "> |" },
  { id: "divider", label: "Divider", detail: "A horizontal rule", template: "---\n|" },
];
