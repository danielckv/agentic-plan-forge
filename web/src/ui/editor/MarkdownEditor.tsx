import { useEffect, useRef, useState } from "react";
import { Annotation, Compartment, EditorState, type Extension } from "@codemirror/state";
import { EditorView, keymap, placeholder as cmPlaceholder, tooltips } from "@codemirror/view";
import { markdown, markdownKeymap, markdownLanguage } from "@codemirror/lang-markdown";
import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
import { tags as t } from "@lezer/highlight";
import {
  autocompletion,
  startCompletion,
  type Completion,
  type CompletionContext,
  type CompletionResult,
} from "@codemirror/autocomplete";
import {
  AtSign,
  Bold,
  Code,
  Columns2,
  Eye,
  Heading1,
  Heading2,
  Heading3,
  Italic,
  Layers,
  List,
  ListChecks,
  ListOrdered,
  Pencil,
  Quote,
  Table,
} from "lucide-react";

import { editorBase } from "../../codemirror/keymap";
import { insertAllowed } from "../../collab/model";
import { MarkdownView } from "../../components/MarkdownView";
import { fetchComponents } from "../../protocol/api";
import { editDocument, subscribeRemoteEdits } from "../../store/syncStore";
import { toastStore } from "../toasts";
import {
  BLOCK_TEMPLATES,
  componentTerm,
  insertBlock,
  setHeading,
  toggleInline,
  toggleLinePrefix,
  type EditResult,
} from "./markdownEdits";

/**
 * The composer: one CodeMirror 6 editor over the shared `prd` document, with
 * a prose surface (headings render large, markers fade), a formatting
 * toolbar, a `/` menu for blocks and `@` completion over the repository's
 * real components.
 *
 * The view is created once. Local transactions go to the store's OT client;
 * text that changes under the editor (an upload, a collaborator, a snapshot)
 * arrives through `subscribeRemoteEdits` and is applied with the `remoteEdit`
 * annotation, so it is never sent back.
 */

export type EditorMode = "edit" | "split" | "preview";

/** What the page can do to the editor from outside it. */
export interface EditorApi {
  /** Insert text at the cursor, replacing the selection. */
  insertText(text: string): void;
  /** Insert a block template on its own lines at the cursor. */
  insertTemplate(template: string): void;
  focus(): void;
}

const remoteEdit = Annotation.define<boolean>();

const MONO = 'ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace';

/** Prose highlighting on the theme tokens, so light and dark need no second style. */
const proseStyle = HighlightStyle.define([
  { tag: t.heading1, fontSize: "1.6em", fontWeight: "700", color: "var(--pf-ink)", lineHeight: "1.3" },
  { tag: t.heading2, fontSize: "1.3em", fontWeight: "650", color: "var(--pf-ink)" },
  { tag: t.heading3, fontSize: "1.12em", fontWeight: "600", color: "var(--pf-ink)" },
  { tag: [t.heading4, t.heading5, t.heading6], fontWeight: "600", color: "var(--pf-ink)" },
  { tag: t.strong, fontWeight: "700", color: "var(--pf-ink)" },
  { tag: t.emphasis, fontStyle: "italic" },
  { tag: t.strikethrough, textDecoration: "line-through" },
  { tag: [t.link, t.url], color: "var(--pf-accent)" },
  { tag: t.monospace, fontFamily: MONO, fontSize: "0.88em", color: "var(--pf-epic)" },
  { tag: t.quote, color: "var(--pf-ink-2)", fontStyle: "italic" },
  { tag: [t.processingInstruction, t.meta, t.contentSeparator], color: "var(--pf-faint)" },
]);

/** Refuse an insert the authority would refuse, before it reaches the document. */
const allowedInserts = EditorState.transactionFilter.of((tr) => {
  if (!tr.docChanged || tr.annotation(remoteEdit) === true) return tr;
  let ok = true;
  tr.changes.iterChanges((_a, _b, _c, _d, inserted) => {
    if (!insertAllowed(inserted.toString())) ok = false;
  });
  if (!ok) toastStore.push("error", "That text holds characters the document cannot store");
  return ok ? tr : [];
});

/** Send every local change to the store's OT client. */
const sendLocalEdits = EditorView.updateListener.of((u) => {
  for (const tr of u.transactions) {
    if (tr.docChanged && tr.annotation(remoteEdit) !== true) editDocument(tr.changes);
  }
});

function dispatchEdit(view: EditorView, fn: (doc: string, from: number, to: number) => EditResult): void {
  const { from, to } = view.state.selection.main;
  const r = fn(view.state.doc.toString(), from, to);
  view.dispatch({ changes: r.changes, selection: r.selection, scrollIntoView: true, userEvent: "input" });
  view.focus();
}

/**
 * `/` at the start of a line opens the block menu. The completion range starts
 * after the slash so the letters typed filter the labels; applying an option
 * removes the slash and the letters, then inserts the block.
 */
function slashSource(ctx: CompletionContext): CompletionResult | null {
  const line = ctx.state.doc.lineAt(ctx.pos);
  const m = /^(\s*)\/([\w-]*)$/.exec(line.text.slice(0, ctx.pos - line.from));
  if (m === null) return null;
  const from = line.from + (m[1] ?? "").length + 1;
  const replaceThen =
    (edit: (doc: string, a: number, b: number) => EditResult) =>
    (view: EditorView, _c: Completion, start: number, end: number): void => {
      view.dispatch({ changes: { from: start - 1, to: end, insert: "" } });
      dispatchEdit(view, edit);
    };
  const options: Completion[] = [
    ...[1, 2, 3].map((level) => ({
      label: `Heading ${level}`,
      detail: "#".repeat(level),
      type: "keyword",
      apply: replaceThen((doc, a, b) => setHeading(doc, a, b, level)),
    })),
    ...BLOCK_TEMPLATES.map((b) => ({
      label: b.label,
      detail: b.detail,
      type: "keyword",
      apply: replaceThen((doc, a) => insertBlock(doc, a, b.template)),
    })),
  ];
  return { from, options, validFor: /^[\w-]*$/ };
}

const componentType: Record<string, string> = {
  function: "function",
  method: "method",
  struct: "class",
  class: "class",
  interface: "interface",
  trait: "interface",
  type: "type",
  enum: "enum",
  constant: "constant",
  variable: "variable",
  route: "namespace",
};

/**
 * `@` completes the repository's real components. A bare `@` lists the blocks
 * (directories); `@name` searches public declarations. Either inserts the
 * term as inline code, which the interpretation routes to the technical
 * appendix rather than the product view.
 */
async function mentionSource(ctx: CompletionContext): Promise<CompletionResult | null> {
  const word = ctx.matchBefore(/@[\w./-]*/);
  if (word === null) return null;
  const query = word.text.slice(1);
  try {
    const view = await fetchComponents(query === "" ? {} : { q: query });
    if (ctx.aborted) return null;
    if (query === "") {
      return {
        from: word.from,
        filter: false,
        options: view.modules.slice(0, 200).map((m) => ({
          label: m.path,
          detail: `${m.language} · ${m.symbols} public`,
          type: "namespace",
          apply: componentTerm(m.path),
        })),
      };
    }
    return {
      from: word.from,
      filter: false,
      options: view.symbols.map((s) => ({
        label: s.name,
        detail: `${s.kind} · ${s.path}:${s.line}`,
        ...(s.signature !== undefined && s.signature !== "" ? { info: s.signature } : {}),
        type: componentType[s.kind] ?? "text",
        apply: componentTerm(s.name),
      })),
    };
  } catch {
    return null;
  }
}

function buildExtensions(readOnly: Compartment, placeholderText: string, canEdit: boolean): Extension[] {
  return [
    markdown({ base: markdownLanguage }),
    syntaxHighlighting(proseStyle),
    keymap.of(markdownKeymap),
    keymap.of([
      { key: "Mod-b", run: (v) => (dispatchEdit(v, (d, a, b) => toggleInline(d, a, b, "**", "bold")), true) },
      { key: "Mod-i", run: (v) => (dispatchEdit(v, (d, a, b) => toggleInline(d, a, b, "_", "italic")), true) },
      { key: "Mod-e", run: (v) => (dispatchEdit(v, (d, a, b) => toggleInline(d, a, b, "`", "code")), true) },
    ]),
    editorBase,
    EditorView.lineWrapping,
    EditorView.contentAttributes.of({ "aria-label": "Requirements", spellcheck: "true" }),
    cmPlaceholder(placeholderText),
    // Completion popups render at document level, so the editor's rounded,
    // clipped frame never cuts them off.
    tooltips({ position: "fixed", parent: document.body }),
    autocompletion({ override: [slashSource, mentionSource], icons: true, activateOnTyping: true }),
    allowedInserts,
    sendLocalEdits,
    readOnly.of([EditorState.readOnly.of(!canEdit), EditorView.editable.of(canEdit)]),
  ];
}

/** Debounce a value so the preview re-renders at most every `ms`. */
function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value);
  useEffect(() => {
    const id = setTimeout(() => setV(value), ms);
    return () => clearTimeout(id);
  }, [value, ms]);
  return v;
}

export interface MarkdownEditorProps {
  /** The document when the editor mounts; later changes arrive as remote edits. */
  initialText: string;
  /** The live text, for the preview and the word count. */
  text: string;
  canEdit: boolean;
  /** True while local edits wait for the authority. */
  syncing: boolean;
  placeholder: string;
  apiRef?: React.RefObject<EditorApi | null>;
  /** Extra controls at the right of the toolbar (the Attach button). */
  actions?: React.ReactNode;
  onDropFiles?: (files: File[]) => void;
}

export function MarkdownEditor(props: MarkdownEditorProps): React.ReactElement {
  const { initialText, text, canEdit, syncing, placeholder, apiRef, actions, onDropFiles } = props;
  const hostRef = useRef<HTMLDivElement>(null);
  const viewRef = useRef<EditorView | null>(null);
  const readOnlyRef = useRef(new Compartment());
  const [mode, setMode] = useState<EditorMode>("edit");
  const [dropping, setDropping] = useState(false);
  const preview = useDebounced(text, 120);

  // Create the view once. The initial text and placeholder are read at mount;
  // everything after that flows through the store.
  const initialRef = useRef({ initialText, placeholder, canEdit });
  useEffect(() => {
    const host = hostRef.current;
    if (host === null) return;
    const init = initialRef.current;
    const view = new EditorView({
      parent: host,
      state: EditorState.create({
        doc: init.initialText,
        extensions: buildExtensions(readOnlyRef.current, init.placeholder, init.canEdit),
      }),
    });
    viewRef.current = view;
    const unsubscribe = subscribeRemoteEdits((effect) => {
      const changes =
        effect.kind === "reset" ? { from: 0, to: view.state.doc.length, insert: effect.text } : effect.changes;
      view.dispatch({ changes, annotations: remoteEdit.of(true) });
    });
    return () => {
      unsubscribe();
      view.destroy();
      viewRef.current = null;
    };
  }, []);

  useEffect(() => {
    viewRef.current?.dispatch({
      effects: readOnlyRef.current.reconfigure([EditorState.readOnly.of(!canEdit), EditorView.editable.of(canEdit)]),
    });
  }, [canEdit]);

  useEffect(() => {
    if (apiRef === undefined) return;
    apiRef.current = {
      insertText: (insert) => {
        const view = viewRef.current;
        if (view === null || !canEdit) return;
        const { from, to } = view.state.selection.main;
        view.dispatch({ changes: { from, to, insert }, selection: { anchor: from + insert.length }, scrollIntoView: true });
        view.focus();
      },
      insertTemplate: (template) => {
        const view = viewRef.current;
        if (view === null || !canEdit) return;
        dispatchEdit(view, (doc, a) => insertBlock(doc, a, template));
      },
      focus: () => viewRef.current?.focus(),
    };
  }, [apiRef, canEdit]);

  const run = (fn: (doc: string, from: number, to: number) => EditResult) => (): void => {
    const view = viewRef.current;
    if (view !== null && canEdit) dispatchEdit(view, fn);
  };

  const words = text.trim() === "" ? 0 : text.trim().split(/\s+/).length;

  return (
    <div className="pf-composer" data-mode={mode}>
      <div className="pf-composer-toolbar" role="toolbar" aria-label="Formatting">
        <div className="pf-tool-group">
          <ToolButton label="Heading 1" icon={Heading1} onClick={run((d, a, b) => setHeading(d, a, b, 1))} disabled={!canEdit} />
          <ToolButton label="Heading 2" icon={Heading2} onClick={run((d, a, b) => setHeading(d, a, b, 2))} disabled={!canEdit} />
          <ToolButton label="Heading 3" icon={Heading3} onClick={run((d, a, b) => setHeading(d, a, b, 3))} disabled={!canEdit} />
        </div>
        <div className="pf-tool-group">
          <ToolButton label="Bold" shortcut="⌘B" icon={Bold} onClick={run((d, a, b) => toggleInline(d, a, b, "**", "bold"))} disabled={!canEdit} />
          <ToolButton label="Italic" shortcut="⌘I" icon={Italic} onClick={run((d, a, b) => toggleInline(d, a, b, "_", "italic"))} disabled={!canEdit} />
          <ToolButton label="Inline code" shortcut="⌘E" icon={Code} onClick={run((d, a, b) => toggleInline(d, a, b, "`", "code"))} disabled={!canEdit} />
        </div>
        <div className="pf-tool-group">
          <ToolButton label="Bulleted list" icon={List} onClick={run((d, a, b) => toggleLinePrefix(d, a, b, "- "))} disabled={!canEdit} />
          <ToolButton label="Numbered list" icon={ListOrdered} onClick={run((d, a, b) => toggleLinePrefix(d, a, b, "1. "))} disabled={!canEdit} />
          <ToolButton label="Checklist" icon={ListChecks} onClick={run((d, a, b) => toggleLinePrefix(d, a, b, "- [ ] "))} disabled={!canEdit} />
          <ToolButton label="Quote" icon={Quote} onClick={run((d, a, b) => toggleLinePrefix(d, a, b, "> "))} disabled={!canEdit} />
          <ToolButton
            label="Table"
            icon={Table}
            onClick={run((d, a) => insertBlock(d, a, BLOCK_TEMPLATES.find((b) => b.id === "table")?.template ?? ""))}
            disabled={!canEdit}
          />
        </div>
        <div className="pf-tool-group">
          <button
            type="button"
            className="pf-tool-text"
            disabled={!canEdit}
            onClick={run((d, a) => insertBlock(d, a, BLOCK_TEMPLATES[0]?.template ?? ""))}
            title="Insert an epic section"
          >
            <Layers size={14} />
            Epic
          </button>
          <button
            type="button"
            className="pf-tool-text"
            disabled={!canEdit}
            title="Mention a component of the codebase (type @)"
            onClick={() => {
              const view = viewRef.current;
              if (view === null) return;
              const { from, to } = view.state.selection.main;
              view.dispatch({ changes: { from, to, insert: "@" }, selection: { anchor: from + 1 } });
              view.focus();
              startCompletion(view);
            }}
          >
            <AtSign size={14} />
            Component
          </button>
        </div>
        <div className="pf-toolbar-spacer" />
        {actions}
      </div>

      <div
        className="pf-composer-body"
        data-dropping={dropping}
        onDragOver={(e) => {
          if (onDropFiles !== undefined && e.dataTransfer.types.includes("Files")) {
            e.preventDefault();
            setDropping(true);
          }
        }}
        onDragLeave={(e) => {
          if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setDropping(false);
        }}
        onDrop={(e) => {
          if (onDropFiles === undefined || !e.dataTransfer.types.includes("Files")) return;
          e.preventDefault();
          setDropping(false);
          onDropFiles(Array.from(e.dataTransfer.files));
        }}
      >
        <div ref={hostRef} className="pf-composer-editor pf-scroll-thin" hidden={mode === "preview"} />
        {mode !== "edit" && (
          <div className="pf-composer-preview pf-scroll-thin" aria-label="Preview">
            {preview.trim() === "" ? (
              <p className="pf-caption">Nothing to preview yet.</p>
            ) : (
              <MarkdownView markdown={preview} className="pf-doc" />
            )}
          </div>
        )}
        {dropping && (
          <div className="pf-composer-drop" aria-hidden="true">
            Drop .docx, .pdf or .md files to add them
          </div>
        )}
      </div>

      <div className="pf-composer-status">
        <span>
          {words} {words === 1 ? "word" : "words"}
        </span>
        <span aria-live="polite">{!canEdit ? "Read only" : syncing ? "Saving…" : "Saved"}</span>
        <span className="pf-composer-hint">
          Type <kbd className="pf-kbd">/</kbd> for blocks, <kbd className="pf-kbd">@</kbd> for components
        </span>
        <div className="pf-segmented-sm" role="group" aria-label="View">
          <ModeButton mode="edit" current={mode} onSelect={setMode} icon={Pencil} label="Write" />
          <ModeButton mode="split" current={mode} onSelect={setMode} icon={Columns2} label="Split" />
          <ModeButton mode="preview" current={mode} onSelect={setMode} icon={Eye} label="Preview" />
        </div>
      </div>
    </div>
  );
}

function ToolButton(props: {
  label: string;
  icon: React.ComponentType<{ size?: number }>;
  onClick: () => void;
  disabled: boolean;
  shortcut?: string;
}): React.ReactElement {
  const { label, icon: Icon, onClick, disabled, shortcut } = props;
  return (
    <button
      type="button"
      className="pf-tool"
      aria-label={label}
      title={shortcut === undefined ? label : `${label} (${shortcut})`}
      onMouseDown={(e) => e.preventDefault()}
      onClick={onClick}
      disabled={disabled}
    >
      <Icon size={15} />
    </button>
  );
}

function ModeButton(props: {
  mode: EditorMode;
  current: EditorMode;
  onSelect: (m: EditorMode) => void;
  icon: React.ComponentType<{ size?: number }>;
  label: string;
}): React.ReactElement {
  const { mode, current, onSelect, icon: Icon, label } = props;
  return (
    <button type="button" className="pf-seg" aria-pressed={mode === current} onClick={() => onSelect(mode)}>
      <Icon size={13} />
      <span>{label}</span>
    </button>
  );
}
