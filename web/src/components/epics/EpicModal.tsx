import { useState } from "react";
import { ChevronLeft, ChevronRight } from "lucide-react";

import { MarkdownView } from "../MarkdownView";
import { CopyButton } from "../../ui/CopyButton";
import { Dialog } from "../../ui/Dialog";

/** What the modal shows: one epic as a Markdown file. */
export interface EpicDocument {
  id: string;
  title: string;
  filename: string;
  markdown: string;
}

export interface EpicModalProps {
  /** The epics in order; `index` selects the open one, null closes the modal. */
  docs: EpicDocument[];
  index: number | null;
  onIndex: (index: number | null) => void;
  /** Extra controls in the footer, such as "Insert term". */
  footer?: (doc: EpicDocument) => React.ReactNode;
}

/**
 * The epic viewer: the file rendered as a document, or its Markdown source to
 * copy. Previous and next step through the other epics without closing.
 */
export function EpicModal({ docs, index, onIndex, footer }: EpicModalProps): React.ReactElement | null {
  const [view, setView] = useState<"doc" | "source">("doc");
  const doc = index === null ? undefined : docs[index];
  if (index === null || doc === undefined) return null;
  const hasPrev = index > 0;
  const hasNext = index < docs.length - 1;
  return (
    <Dialog open onClose={() => onIndex(null)} title={`${doc.id} · ${doc.title}`} description={doc.filename} size="doc">
      <div className="pf-epic-modal">
        <div className="pf-epic-modal-bar">
          <div className="pf-tabs" role="group" aria-label="Epic view">
            <button type="button" className="pf-tab" data-active={view === "doc"} aria-pressed={view === "doc"} onClick={() => setView("doc")}>
              Document
            </button>
            <button
              type="button"
              className="pf-tab"
              data-active={view === "source"}
              aria-pressed={view === "source"}
              onClick={() => setView("source")}
            >
              Markdown
            </button>
          </div>
          <CopyButton text={doc.markdown} label="Copy Markdown" />
        </div>
        {view === "doc" ? (
          <MarkdownView markdown={doc.markdown} className="pf-doc pf-epic-modal-doc" />
        ) : (
          <pre className="pf-epic-modal-source pf-mono">{doc.markdown}</pre>
        )}
        <div className="pf-epic-modal-foot">
          <button type="button" className="pf-btn pf-btn-ghost pf-btn-sm" disabled={!hasPrev} onClick={() => onIndex(index - 1)}>
            <ChevronLeft size={14} />
            Previous
          </button>
          <span className="pf-caption">
            {index + 1} of {docs.length}
          </span>
          <button type="button" className="pf-btn pf-btn-ghost pf-btn-sm" disabled={!hasNext} onClick={() => onIndex(index + 1)}>
            Next
            <ChevronRight size={14} />
          </button>
          <span className="pf-toolbar-spacer" />
          {footer?.(doc)}
        </div>
      </div>
    </Dialog>
  );
}
