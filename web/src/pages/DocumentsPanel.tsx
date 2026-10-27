import { useCallback, useEffect, useState } from "react";
import { exportDocuments, fetchDocuments, type BundleView, type ExportReport } from "../protocol/api";
import { canExport, documentsNote, exportLabel, exportResult } from "./documentsExport";

/**
 * The documents-only export.
 *
 * It lists every path an export would write, before anything is written, and
 * the button is the only thing that writes. The state is spelled out rather
 * than implied: a preview that looks like a success is the failure this panel
 * exists to prevent.
 */
export function DocumentsPanel(): React.ReactElement {
  const [view, setView] = useState<BundleView | null>(null);
  const [report, setReport] = useState<ExportReport | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async (): Promise<void> => {
    try {
      setView(await fetchDocuments());
    } catch {
      setView({ available: false, problem: "the binary did not answer", files: [] });
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const runExport = useCallback(async (): Promise<void> => {
    setBusy(true);
    setReport(null);
    try {
      const out = await exportDocuments({ confirmed: true });
      setReport(out);
      if (out.state === "exported") await load();
    } finally {
      setBusy(false);
    }
  }, [load]);

  if (view === null) {
    return (
      <section className="pf-docs" aria-busy="true">
        <h3 className="pf-docs-title">Documents</h3>
        <p className="pf-docs-note">Reading what an export would write…</p>
      </section>
    );
  }

  const result = report === null ? null : exportResult(report);
  return (
    <section className="pf-docs">
      <h3 className="pf-docs-title">Documents</h3>
      <p className="pf-docs-note">{documentsNote(view)}</p>
      {view.available && view.files.length > 0 && (
        <ul className="pf-docs-list">
          {view.files.map((file) => (
            <li key={file.path} className="pf-docs-file" data-kind={file.kind}>
              <code>{file.path}</code>
              <span className="pf-docs-kind">{file.kind}</span>
            </li>
          ))}
        </ul>
      )}
      <div className="pf-docs-actions">
        <button
          type="button"
          className="pf-btn"
          onClick={() => void runExport()}
          disabled={!canExport(view, busy)}
        >
          {exportLabel(busy)}
        </button>
        {result !== null && (
          <p className={`pf-docs-result pf-docs-${result.tone}`} role="status">
            {result.message}
          </p>
        )}
      </div>
    </section>
  );
}
