import { useState } from "react";
import { BookOpen, GitBranch } from "lucide-react";

import { MarkdownView } from "../../components/MarkdownView";
import type { ContextHit, ProjectContext } from "../../protocol/api";
import { Dialog } from "../../ui/Dialog";

/** Where the excerpts came from, as the caption names it; null when nowhere. */
const SOURCE_LABEL: Record<ProjectContext["source"], string | null> = {
  "recall-index": "the project's recall index",
  "legacy-recall-index": "the project's legacy recall index",
  files: "the project's notes and docs",
  none: null,
};

/** The excerpt as one line of prose: no heading lines, no list or quote markers. */
export function notePreview(text: string): string {
  return text
    .split("\n")
    .filter((line) => !/^\s*#{1,6}\s/.test(line))
    .map((line) => line.replace(/^\s*(?:[-*+]\s+(?:\[[ xX]\]\s+)?|\d+\.\s+|>\s?)/, ""))
    .join(" ")
    .replace(/\s+/g, " ")
    .trim()
    .slice(0, 220);
}

/**
 * What the repository already says about this work: excerpts from its notes,
 * specs, plans and decision records, and where the branch stands. The
 * technical avatars read exactly this list.
 */
export function NotesList({ ctx }: { ctx: ProjectContext | null }): React.ReactElement {
  const [open, setOpen] = useState<ContextHit | null>(null);
  if (ctx === null) {
    return (
      <div className="pf-rail-empty">
        <p className="pf-body">Reading project notes, specs and decisions…</p>
      </div>
    );
  }
  return (
    <div className="pf-notes">
      <p className="pf-caption">
        {SOURCE_LABEL[ctx.source] === null
          ? "The architect, specialists and critic read related project notes from here before they plan."
          : `From ${SOURCE_LABEL[ctx.source]}. The architect, specialists and critic read these before they plan.`}
      </p>
      {ctx.branch !== undefined && ctx.branch !== "" && (
        <div className="pf-notes-branch">
          <GitBranch size={13} aria-hidden="true" />
          <code className="pf-mono">{ctx.branch}</code>
          <span className="pf-caption">at {ctx.head}</span>
          {(ctx.recent_commits?.length ?? 0) > 0 && <span className="pf-caption">· {ctx.recent_commits?.[0]}</span>}
        </div>
      )}
      {ctx.hits.length === 0 ? (
        <div className="pf-rail-empty">
          <p className="pf-body">Nothing related yet.</p>
          <p className="pf-caption">
            Notes appear as the requirements mention things the repository's specs, plans or decision records talk about.
          </p>
        </div>
      ) : (
        <ul className="pf-note-list">
          {ctx.hits.map((h) => (
            <li key={`${h.path}:${h.text.slice(0, 40)}`}>
              <button type="button" className="pf-note" onClick={() => setOpen(h)}>
                <span className="pf-note-head">
                  <BookOpen size={13} aria-hidden="true" />
                  <span className="pf-note-path">{h.path}</span>
                </span>
                {h.heading !== undefined && h.heading !== "" && <span className="pf-note-title">{h.heading}</span>}
                <span className="pf-note-text">{notePreview(h.text)}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
      {open !== null && (
        <Dialog
          open
          onClose={() => setOpen(null)}
          title={open.heading !== undefined && open.heading !== "" ? open.heading : open.path}
          description={open.path}
          size="doc"
        >
          <MarkdownView markdown={open.text} className="pf-doc" />
        </Dialog>
      )}
    </div>
  );
}
