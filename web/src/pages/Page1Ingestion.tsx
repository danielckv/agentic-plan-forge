import { useCallback, useEffect, useState } from "react";
import { EditorView } from "@codemirror/view";
import { EditorState, type Extension } from "@codemirror/state";
import { editorBase } from "../codemirror/keymap";
import { markdown } from "@codemirror/lang-markdown";

import { useSync, submitEdit, freezeRequest, exportRecovery } from "../store/syncStore";
import type { RecoveryItem, SyncState } from "../store/syncStore";

/**
 * Page 1 — Ingestion & normalization.
 *
 * Left pane: dropzone (.docx/.pdf/.md, max 25 MiB each, 5 files; owner/
 * executor only), a raw-text composer ("Add to interpretation" sends through
 * ingestion), status pills (AST scan, doc health, OCR). Right pane: the
 * single shared `prd` CodeMirror document with tabs for the latest
 * NormalizedPRD ("Product PRD" and "Technical appendix", read-only) and
 * ambiguity chips. Footer: Confirm Context & Launch Avatars (owner only).
 */

const MAX_UPLOAD_BYTES = 25 * 1024 * 1024;
const MAX_UPLOAD_FILES = 5;

const OCR_INSTALL_GUIDANCE: Record<string, string> = {
  Darwin: "brew install poppler tesseract",
  Linux: "apt install poppler-utils tesseract-ocr",
  Windows: "winget install UB-Mannheim.TesseractOCR + Poppler bin on PATH",
};

export interface UploadState {
  inFlight: boolean;
  fileName: string | null;
  error: string | null;
  warnings: string[];
}

export interface StatusPills {
  ast: "scanning" | "partial" | "failed" | "not-a-Git-repo" | "timeout" | "ok";
  astDetail: string;
  docHealth: number;
  ocr: "available" | "missing" | "unknown";
}

const AMBIGUITY_DEFAULT: Array<{ id: string; text: string; guess?: string }> = [];

export function Page1Ingestion(): React.ReactElement {
  const sync = useSync();
  const [upload, setUpload] = useState<UploadState>({
    inFlight: false,
    fileName: null,
    error: null,
    warnings: [],
  });
  const [pills, setPills] = useState<StatusPills>({
    ast: "scanning",
    astDetail: "0 files parsed, 0 symbols indexed",
    docHealth: 0,
    ocr: "unknown",
  });
  const [ambiguities, setAmbiguities] = useState(AMBIGUITY_DEFAULT);
  const [composerText, setComposerText] = useState("");
  const [secrets] = useState<string[]>([]);
  const [dismissedSecrets, setDismissedSecrets] = useState<string[]>([]);
  const [tab, setTab] = useState<"product" | "appendix">("product");
  const [hostAck, setHostAck] = useState(false);

  // Status pills arrive from the local bridge; polling keeps them fresh
  // without blocking the page (background AST scan budget: 120s total).
  useEffect(() => {
    const id = window.setInterval(async () => {
      try {
        const res = await fetch("/api/status");
        if (!res.ok) return;
        const data = (await res.json()) as Partial<StatusPills>;
        setPills((prev) => ({ ...prev, ...data }));
      } catch {
        /* offline: pills stay at last known values */
      }
    }, 5000);
    return () => window.clearInterval(id);
  }, []);

  const onDrop = useCallback(
    async (e: React.DragEvent<HTMLElement>): Promise<void> => {
      e.preventDefault();
      if (sync.role !== "owner") return; // only the owner/executor uploads
      const files = Array.from(e.dataTransfer.files).slice(0, MAX_UPLOAD_FILES);
      for (const f of files) {
        if (f.size > MAX_UPLOAD_BYTES) {
          setUpload({ inFlight: false, fileName: f.name, error: "file too large (max 25 MiB)", warnings: [] });
          return;
        }
      }
      if (files.length === 0) return;
      setUpload({ inFlight: true, fileName: files[0]!.name, error: null, warnings: [] });
      try {
        for (const f of files) {
          const body = new FormData();
          body.append("file", f);
          const res = await fetch("/api/upload", { method: "POST", body });
          if (!res.ok) {
            const err = (await res.json().catch(() => ({}))) as { error?: string };
            throw new Error(err.error ?? `upload failed (${res.status})`);
          }
          const out = (await res.json()) as { warnings?: string[]; ambiguities?: typeof AMBIGUITY_DEFAULT };
          setUpload((prev) => ({
            inFlight: false,
            fileName: f.name,
            error: null,
            warnings: [...prev.warnings, ...(out.warnings ?? [])],
          }));
          setAmbiguities((prev) => [...prev, ...(out.ambiguities ?? [])]);
        }
      } catch (err) {
        setUpload({
          inFlight: false,
          fileName: null,
          error: err instanceof Error ? err.message : "upload failed",
          warnings: [],
        });
      }
    },
    [sync.role],
  );

  const addToInterpretation = useCallback((): void => {
    if (composerText.trim().length === 0) return;
    // Composer text flows through ingestion as INGEST_APPEND (source:
    // "composer"), never as a direct document edit.
    submitEdit(composerText, sync.docText.length, sync.docText.length);
    setComposerText("");
  }, [composerText, sync.docText.length]);

  const confirmEnabled =
    sync.role === "owner" &&
    sync.docText.trim().length > 0 &&
    !upload.inFlight &&
    ambiguities.length === 0 &&
    hostAck;

  const onConfirm = useCallback((): void => {
    // The executor first runs a secret scan over prd; hits become chips the
    // owner redacts or dismisses. Dismissed chips clear the way to freeze.
    if (secrets.length > dismissedSecrets.length) return;
    freezeRequest();
  }, [secrets.length, dismissedSecrets.length]);

  return (
    <div className="grid h-full grid-cols-2 gap-4 overflow-auto p-4">
      {/* Left pane: input + local context */}
      <section className="flex flex-col gap-3">
        <h2 className="text-lg font-semibold">Ingestion</h2>
        <div
          onDragOver={(e) => e.preventDefault()}
          onDrop={(e) => void onDrop(e)}
          className="flex min-h-24 flex-col items-center justify-center rounded border-2 border-dashed p-4"
          style={{ borderColor: "var(--pf-border)" }}
        >
          {sync.role !== "owner" ? (
            <p className="text-sm" style={{ color: "var(--pf-muted)" }}>
              only the session owner can upload
            </p>
          ) : upload.inFlight ? (
            <p className="text-sm">parsing {upload.fileName}…</p>
          ) : (
            <>
              <p className="text-sm">Drop .docx / .pdf / .md here</p>
              <p className="text-xs" style={{ color: "var(--pf-muted)" }}>
                max 25 MiB each, 5 files
              </p>
            </>
          )}
        </div>
        {upload.error !== null && (
          <p className="text-sm" style={{ color: "var(--pf-danger)" }} role="alert">
            {upload.error}
          </p>
        )}
        {upload.warnings.map((w) => (
          <p key={w} className="text-sm" style={{ color: "var(--pf-warn)" }}>
            {w}
          </p>
        ))}
        <textarea
          value={composerText}
          onChange={(e) => setComposerText(e.target.value)}
          placeholder="Raw text: epics, ideas, requirements…"
          className="min-h-24 rounded border p-2"
          style={{ borderColor: "var(--pf-border)" }}
        />
        <button
          type="button"
          className="self-start rounded px-3 py-1.5 text-sm"
          style={{ background: "var(--pf-accent)", color: "var(--pf-accent-ink)" }}
          onClick={addToInterpretation}
        >
          Add to interpretation
        </button>
        <div className="flex flex-wrap gap-2 text-xs">
          <Pill kind="ast" pills={pills} />
          <Pill kind="docHealth" pills={pills} />
          <Pill kind="ocr" pills={pills} />
        </div>
        {pills.ocr === "missing" && (
          <p className="text-xs" style={{ color: "var(--pf-muted)" }}>
            OCR tools missing: {OCR_INSTALL_GUIDANCE[detectOS()] ?? OCR_INSTALL_GUIDANCE.Linux}
          </p>
        )}
        <RecoveryPanel recovery={sync.recovery} />
      </section>

      {/* Right pane: live interpretation */}
      <section className="flex min-h-0 flex-col gap-2">
        <div className="flex items-center gap-2">
          <h2 className="text-lg font-semibold">Interpretation</h2>
          <div className="ml-auto flex gap-1">
            <TabButton label="Product PRD" active={tab === "product"} onClick={() => setTab("product")} />
            <TabButton label="Technical appendix" active={tab === "appendix"} onClick={() => setTab("appendix")} />
          </div>
        </div>
        <PrdEditor sync={sync} />
        {ambiguities.length > 0 && (
          <div className="rounded border p-2 text-sm" style={{ borderColor: "var(--pf-warn)" }}>
            <p className="mb-1 font-medium">Resolve ambiguities before confirm:</p>
            <ul className="flex list-none flex-col gap-1">
              {ambiguities.map((a) => (
                <li key={a.id} className="flex items-center gap-2">
                  <span>{a.text}</span>
                  <button
                    type="button"
                    className="rounded px-2 py-0.5 text-xs"
                    style={{ background: "var(--pf-accent)", color: "var(--pf-accent-ink)" }}
                    onClick={() => setAmbiguities((prev) => prev.filter((x) => x.id !== a.id))}
                  >
                    resolve: {a.guess ?? "product"}
                  </button>
                </li>
              ))}
            </ul>
          </div>
        )}
        {secrets.length > 0 && (
          <div className="rounded border p-2 text-sm" style={{ borderColor: "var(--pf-danger)" }}>
            <p className="mb-1 font-medium">Possible secrets detected — redact or dismiss:</p>
            <ul className="flex list-none flex-col gap-1">
              {secrets.map((s) => (
                <li key={s} className="flex items-center gap-2">
                  <code className="text-xs">{s.slice(0, 12)}…</code>
                  <button
                    type="button"
                    className="rounded px-2 py-0.5 text-xs"
                    onClick={() => setDismissedSecrets((prev) => [...prev, s])}
                  >
                    dismiss
                  </button>
                </li>
              ))}
            </ul>
          </div>
        )}
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={hostAck} onChange={(e) => setHostAck(e.target.checked)} />
          Product requirements will be sent to the generation host through its local CLI
        </label>
        <button
          type="button"
          disabled={!confirmEnabled}
          className="self-end rounded px-4 py-2 text-sm font-semibold disabled:opacity-40"
          style={{ background: "var(--pf-accent)", color: "var(--pf-accent-ink)" }}
          onClick={onConfirm}
        >
          Confirm Context &amp; Launch Avatars →
        </button>
      </section>
    </div>
  );
}

function Pill(props: { kind: "ast" | "docHealth" | "ocr"; pills: StatusPills }): React.ReactElement {
  const { kind, pills } = props;
  const tone =
    kind === "ast" && pills.ast === "ok"
      ? "var(--pf-ok)"
      : kind === "docHealth" && pills.docHealth === 0
        ? "var(--pf-ok)"
        : kind === "ocr" && pills.ocr === "available"
          ? "var(--pf-ok)"
          : "var(--pf-warn)";
  const label =
    kind === "ast"
      ? `AST: ${pills.ast} — ${pills.astDetail}`
      : kind === "docHealth"
        ? `Doc health: ${pills.docHealth} stale doc sections detected in docs/`
        : `OCR: ${pills.ocr}`;
  return (
    <span className="rounded-full px-2 py-0.5" style={{ background: tone, color: "#fff" }}>
      {label}
    </span>
  );
}

function TabButton(props: { label: string; active: boolean; onClick: () => void }): React.ReactElement {
  const { label, active, onClick } = props;
  return (
    <button
      type="button"
      className="rounded px-2 py-1 text-xs"
      style={{
        background: active ? "var(--pf-accent)" : "transparent",
        color: active ? "var(--pf-accent-ink)" : "var(--pf-ink)",
        border: "1px solid var(--pf-border)",
      }}
      onClick={onClick}
    >
      {label}
    </button>
  );
}

function RecoveryPanel(props: { recovery: RecoveryItem[] }): React.ReactElement | null {
  const { recovery } = props;
  if (recovery.length === 0) return null;
  return (
    <div className="rounded border p-2 text-sm" style={{ borderColor: "var(--pf-warn)" }}>
      <p className="mb-1 font-medium">Recovery — edits kept locally</p>
      <ul className="flex list-none flex-col gap-1">
        {recovery.map((r) => (
          <li key={r.at}>
            <code className="text-xs">
              {new Date(r.at).toISOString()} · {r.room}
            </code>
          </li>
        ))}
      </ul>
      <button type="button" className="mt-1 rounded px-2 py-0.5 text-xs" onClick={exportRecovery}>
        Export unconfirmed edits
      </button>
    </div>
  );
}

/** The shared `prd` document — OT through the collab extension. */
function PrdEditor(props: { sync: SyncState }): React.ReactElement {
  const { sync } = props;
  const [, setView] = useState<EditorView | null>(null);
  const refCallback = useCallback((node: HTMLDivElement | null): void => {
    if (node === null) {
      setView((v) => {
        v?.destroy();
        return null;
      });
      return;
    }
    const state = EditorState.create({
      doc: sync.docText,
      extensions: [markdown(), editorBase, editorHooks()] as Extension[],
    });
    setView(new EditorView({ state, parent: node }));
  }, [sync.docText]);
  return <div ref={refCallback} className="min-h-0 flex-1 overflow-hidden" />;
}

/** Editor changes flow through submitEdit (transactionFilter rejects \r and lone surrogates). */
function editorHooks(): Extension[] {
  return [
    EditorView.updateListener.of((v) => {
      if (!v.docChanged) return;
      const tr = v.transactions[v.transactions.length - 1];
      if (tr === undefined) return;
      for (const spec of tr.changes.desc as unknown as Array<{ from: number; to: number; insert?: string }>) {
        if (typeof spec.insert === "string" && spec.insert.length > 0) {
          submitEdit(spec.insert, spec.from, spec.to);
        }
      }
    }),
  ];
}

function detectOS(): string {
  const ua = navigator.userAgent;
  if (ua.includes("Mac")) return "Darwin";
  if (ua.includes("Windows")) return "Windows";
  return "Linux";
}

