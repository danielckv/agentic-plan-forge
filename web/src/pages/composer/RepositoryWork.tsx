import { useEffect, useMemo, useRef, useState } from "react";
import { BookOpen, FileSearch, LoaderCircle, TriangleAlert } from "lucide-react";

import { MarkdownView } from "../../components/MarkdownView";
import {
  fetchRepositoryDocument,
  fetchRepositoryWork,
  type RepositoryDocument,
  type RepositoryItem,
  type RepositoryWorkDocument,
  type RepositoryWorkView,
} from "../../protocol/api";
import { Dialog } from "../../ui/Dialog";

/** The filters the view offers; `next` is the active state file's own order. */
type WorkFilter = "next" | "epics" | "plans" | "follow-ups" | "state";

const FILTERS: { id: WorkFilter; label: string; kinds: RepositoryDocument["kind"][] }[] = [
  { id: "next", label: "Next", kinds: [] },
  { id: "epics", label: "Epics & tasks", kinds: ["epic", "task"] },
  { id: "plans", label: "Plans, specs & ADRs", kinds: ["plan", "spec", "adr"] },
  { id: "follow-ups", label: "Follow-ups", kinds: ["follow-up"] },
  { id: "state", label: "State", kinds: ["state"] },
];

/** How an item's status reads; `unknown` is stated, never guessed. */
const STATUS_LABEL: Record<string, string> = {
  open: "open",
  done: "done",
  unknown: "status unknown",
};

/**
 * documentsFor narrows the inventory to one filter. `next` is not a kind: it is
 * the active state file's own order, so it returns no documents.
 */
export function documentsFor(all: RepositoryDocument[], filter: WorkFilter): RepositoryDocument[] {
  if (filter === "next") return [];
  const kinds = FILTERS.find((f) => f.id === filter)?.kinds ?? [];
  return all.filter((d) => kinds.includes(d.kind));
}

/** statusOf states an unknown status rather than leaving a blank cell. */
export function statusOf(status: string): string {
  return STATUS_LABEL[status] ?? status;
}

/**
 * The repository's own planning documents: what is next, which epics and tasks
 * exist, and the plans, specifications and decision records behind them. The
 * view reads the real files and their line ranges; it never writes them and
 * never starts a model call.
 */
export function RepositoryWork(props: { onInsert?: (text: string) => void }): React.ReactElement {
  const { onInsert } = props;
  const [view, setView] = useState<RepositoryWorkView | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [filter, setFilter] = useState<WorkFilter>("next");
  const [open, setOpen] = useState<{ doc: RepositoryWorkDocument; item?: RepositoryItem } | null>(null);
  const [opening, setOpening] = useState<string | null>(null);
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  useEffect(() => {
    let live = true;
    fetchRepositoryWork()
      .then((v) => {
        if (!live) return;
        setView(v);
        setError(null);
      })
      .catch((err: unknown) => {
        if (!live) return;
        setError(err instanceof Error ? err.message : "the repository could not be read");
      });
    return () => {
      live = false;
    };
  }, []);

  const documents = useMemo(() => documentsFor(view?.documents ?? [], filter), [view, filter]);

  async function openDocument(doc: RepositoryDocument, item?: RepositoryItem): Promise<void> {
    setOpening(doc.id);
    try {
      const full = await fetchRepositoryDocument(doc.id);
      if (mounted.current) setOpen(item === undefined ? { doc: full } : { doc: full, item });
    } catch (err: unknown) {
      if (!mounted.current) return;
      setError(err instanceof Error ? err.message : "the document changed; refresh the view");
      setOpen(null);
    } finally {
      if (mounted.current) setOpening(null);
    }
  }

  const next = view?.next ?? [];

  return (
    <section className="pf-work" aria-label="Repository work">
      <header className="pf-work-head">
        <div>
          <h2 className="pf-section-title">Repository work</h2>
          <p className="pf-caption">
            Read from the repository's own planning files. Browsing changes nothing and starts no model call.
          </p>
        </div>
        <span className="pf-caption">
          {view === null ? "reading…" : `${view.documents?.length ?? 0} documents`}
          {view !== null && view.partial ? " · partial" : ""}
        </span>
      </header>

      {error !== null && (
        <div className="pf-notice pf-notice--warn">
          <TriangleAlert size={14} aria-hidden="true" />
          <span>{error}</span>
        </div>
      )}

      <div className="pf-work-tabs" role="tablist" aria-label="Repository work filters">
        {FILTERS.map((f) => (
          <button
            key={f.id}
            type="button"
            role="tab"
            aria-selected={filter === f.id}
            className={`pf-chip ${filter === f.id ? "pf-chip--accent" : ""}`}
            onClick={() => setFilter(f.id)}
          >
            {f.label}
            {f.id === "next" && next.length > 0 ? ` (${next.length})` : ""}
          </button>
        ))}
      </div>

      {view === null && !error && (
        <p className="pf-body">
          <LoaderCircle size={14} className="animate-spin" aria-hidden="true" /> Reading the repository…
        </p>
      )}

      {view !== null && filter === "next" && (
        <div className="pf-work-list">
          {next.length === 0 ? (
            <p className="pf-body">The active state file names no next work.</p>
          ) : (
            next.map((item) => {
              const doc = (view.documents ?? []).find((d) => d.path === item.source);
              return (
                <button
                  key={`${item.source}:${item.line}`}
                  type="button"
                  className="pf-work-item"
                  disabled={doc === undefined || opening === doc.id}
                  onClick={() => doc !== undefined && void openDocument(doc, item)}
                >
                  <span className="pf-work-item-text">{item.text}</span>
                  <span className="pf-work-item-meta pf-caption">
                    <code className="pf-mono">{item.source}</code>: {item.line}
                  </span>
                </button>
              );
            })
          )}
        </div>
      )}

      {view !== null && filter !== "next" && (
        <div className="pf-work-list">
          {documents.length === 0 ? (
            <p className="pf-body">No documents of this kind in the repository.</p>
          ) : (
            documents.map((doc) => (
              <button
                key={doc.id}
                type="button"
                className="pf-work-item"
                disabled={opening === doc.id}
                onClick={() => void openDocument(doc)}
              >
                <span className="pf-work-item-head">
                  <FileSearch size={13} aria-hidden="true" />
                  <code className="pf-mono">{doc.path}</code>
                  <span className="pf-caption">{doc.kind}</span>
                </span>
                {doc.headings[0] !== undefined && <span className="pf-work-item-title">{doc.headings[0].text}</span>}
                <span className="pf-work-item-meta pf-caption">
                  {doc.items.length} item{doc.items.length === 1 ? "" : "s"} · {doc.bytes} bytes
                </span>
              </button>
            ))
          )}
        </div>
      )}

      {(view?.skipped?.length ?? 0) > 0 && (
        <details className="pf-work-skipped">
          <summary className="pf-caption">{view?.skipped?.length} files were left out</summary>
          <ul className="pf-work-skipped-list">
            {(view?.skipped ?? []).map((s) => (
              <li key={s.path}>
                <code className="pf-mono">{s.path}</code>
                <span className="pf-caption"> — {s.reason}</span>
              </li>
            ))}
          </ul>
        </details>
      )}

      {open !== null && (
        <Dialog
          open
          onClose={() => setOpen(null)}
          title={open.doc.headings[0]?.text ?? open.doc.path}
          description={open.doc.path}
          size="doc"
        >
          {open.item !== undefined && (
            <p className="pf-caption">
              <BookOpen size={12} aria-hidden="true" /> {statusOf(open.item.status)} · lines{" "}
              {open.item.line}–{open.item.end_line}
            </p>
          )}
          {onInsert !== undefined && (
            <div className="pf-work-insert">
              <button
                type="button"
                className="pf-btn pf-btn-secondary pf-btn-sm"
                onClick={() => {
                  // An explicit reference plus the exact source line: the brief
                  // says where the requirement comes from, and the excerpt stays
                  // editable rather than becoming a hidden copy.
                  const quote =
                    open.item !== undefined
                      ? `${open.item.text}\n\n(source: ${open.doc.path}:${open.item.line})`
                      : `(source: ${open.doc.path})`;
                  onInsert(`\n\n${quote}\n`);
                  setOpen(null);
                }}
              >
                Use in brief
              </button>
              <span className="pf-caption">Adds the source path and line to your requirements.</span>
            </div>
          )}
          <MarkdownView markdown={open.doc.markdown} className="pf-doc" />
        </Dialog>
      )}
    </section>
  );
}
