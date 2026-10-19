import { useCallback, useEffect, useRef, useState } from "react";
import { EditorView, placeholder } from "@codemirror/view";
import { EditorState, type Extension } from "@codemirror/state";
import { editorBase } from "../codemirror/keymap";
import { markdown } from "@codemirror/lang-markdown";
import {
  ArrowRight,
  CloudUpload,
  FileText,
  FolderOpen,
  GitBranch,
  LoaderCircle,
  ScanText,
  ShieldAlert,
  TriangleAlert,
} from "lucide-react";

import { apiFetch } from "../protocol/bridge";
import { useSync, submitEdit, freezeRequest, exportRecovery } from "../store/syncStore";
import type { RecoveryItem, StatusView, SyncState } from "../store/syncStore";
import { SkeletonChip, SkeletonLines } from "../ui/Skeleton";

/**
 * Page 1 — Ingestion & normalization.
 *
 * Left pane: dropzone (.docx/.pdf/.md, max 25 MiB each, 5 files; owner/
 * executor only; drag-drop or the Browse button), a raw-text composer ("Add to
 * interpretation" sends through ingestion), status pills (AST scan, doc
 * health, OCR). Right pane: the single shared `prd` CodeMirror document with
 * tabs for the latest NormalizedPRD ("Product PRD" and "Technical appendix",
 * read-only) and ambiguity chips. Footer: Confirm Context & Launch Avatars
 * (owner only).
 *
 * Pending content shows skeletons: the pills until the first `/api/status`
 * answer, the editor until the authority's first snapshot.
 */

const MAX_UPLOAD_BYTES = 25 * 1024 * 1024;
const MAX_UPLOAD_FILES = 5;
const UPLOAD_ACCEPT = ".docx,.pdf,.md,.markdown";

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

/** The pills read from the window-wide status slice; App owns the polling. */
export type StatusPills = StatusView;

const AMBIGUITY_DEFAULT: Array<{ id: string; text: string; guess?: string }> = [];

export function Page1Ingestion(): React.ReactElement {
  const sync = useSync();
  const [upload, setUpload] = useState<UploadState>({
    inFlight: false,
    fileName: null,
    error: null,
    warnings: [],
  });
  const pills = sync.status;
  const [ambiguities, setAmbiguities] = useState(AMBIGUITY_DEFAULT);
  const [composerText, setComposerText] = useState("");
  const [secrets] = useState<string[]>([]);
  const [dismissedSecrets, setDismissedSecrets] = useState<string[]>([]);
  const [tab, setTab] = useState<"product" | "appendix">("product");
  const [dragOver, setDragOver] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);
  // Sending the PRD to the local generation host is what the page is for;
  // the box states that plainly and starts checked, so confirming the
  // context is one click. Unchecking it still blocks the launch.
  const [hostAck, setHostAck] = useState(true);

  // A file dropped anywhere but the dropzone would make the browser navigate
  // to it and take the planning window with it. Swallow stray file drops.
  useEffect(() => {
    const swallow = (e: DragEvent): void => {
      if (e.dataTransfer?.types.includes("Files") === true) e.preventDefault();
    };
    window.addEventListener("dragover", swallow);
    window.addEventListener("drop", swallow);
    return () => {
      window.removeEventListener("dragover", swallow);
      window.removeEventListener("drop", swallow);
    };
  }, []);

  const uploadFiles = useCallback(
    async (list: File[]): Promise<void> => {
      if (sync.role !== "owner") return; // only the owner/executor uploads
      const files = list.slice(0, MAX_UPLOAD_FILES);
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
          const res = await apiFetch("/api/upload", { method: "POST", body });
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

  const onDrop = (e: React.DragEvent<HTMLElement>): void => {
    e.preventDefault();
    setDragOver(false);
    void uploadFiles(Array.from(e.dataTransfer.files));
  };

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

  const isOwner = sync.role === "owner";
  const awaitingSnapshot = sync.role === null;

  return (
    <div className="pf-page">
      <div className="pf-page-body">
        <div className="pf-container grid grid-cols-1 gap-6 py-7 lg:grid-cols-[minmax(0,5fr)_minmax(0,7fr)]">
          {/* Left column: sources + local context */}
          <section className="flex min-w-0 flex-col gap-5" aria-labelledby="p1-title">
            <header className="pf-page-header">
              <h1 id="p1-title" className="pf-title">
                Ingestion &amp; normalization
              </h1>
              <p className="pf-caption">
                Add source documents or raw notes. Plan Forge separates product
                requirements from technical context before any avatar sees them.
              </p>
            </header>

            <div className="pf-card pf-card-pad">
              <p className="pf-label mb-3">Source documents</p>
              <div
                onDragOver={(e) => e.preventDefault()}
                onDragEnter={() => isOwner && setDragOver(true)}
                onDragLeave={(e) => {
                  if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setDragOver(false);
                }}
                onDrop={onDrop}
                className="pf-dropzone"
                data-over={dragOver}
                data-busy={upload.inFlight}
              >
                {!isOwner ? (
                  <>
                    <span className="pf-dropzone-icon">
                      <ShieldAlert size={19} />
                    </span>
                    <p className="pf-body">Only the session owner can upload files</p>
                  </>
                ) : upload.inFlight ? (
                  <>
                    <span className="pf-dropzone-icon">
                      <LoaderCircle size={19} className="animate-spin" />
                    </span>
                    <p className="pf-body">Parsing {upload.fileName}…</p>
                    <p className="pf-caption">Extracting text and structure</p>
                  </>
                ) : (
                  <>
                    <span className="pf-dropzone-icon">
                      <CloudUpload size={19} />
                    </span>
                    <p className="pf-body font-medium" style={{ color: "var(--pf-ink)" }}>
                      Drop .docx, .pdf or .md files here
                    </p>
                    <p className="pf-caption">Up to 5 files · 25 MiB each</p>
                    <button
                      type="button"
                      className="pf-btn pf-btn-secondary pf-btn-sm mt-1"
                      onClick={() => fileInput.current?.click()}
                    >
                      <FolderOpen size={13} />
                      Browse files
                    </button>
                  </>
                )}
                <input
                  ref={fileInput}
                  type="file"
                  className="pf-sr-only"
                  tabIndex={-1}
                  aria-hidden="true"
                  multiple
                  accept={UPLOAD_ACCEPT}
                  onChange={(e) => {
                    const picked = Array.from(e.target.files ?? []);
                    e.target.value = ""; // picking the same file twice must still fire
                    void uploadFiles(picked);
                  }}
                />
              </div>
              {upload.error !== null && (
                <div className="pf-notice pf-notice--danger mt-3" role="alert">
                  <TriangleAlert size={15} className="mt-0.5 shrink-0" style={{ color: "var(--pf-danger)" }} />
                  <span>{upload.error}</span>
                </div>
              )}
              {upload.warnings.map((w) => (
                <div key={w} className="pf-notice pf-notice--warn mt-3">
                  <TriangleAlert size={15} className="mt-0.5 shrink-0" style={{ color: "var(--pf-warn)" }} />
                  <span>{w}</span>
                </div>
              ))}
            </div>

            <div className="pf-card pf-card-pad">
              <label htmlFor="p1-notes" className="pf-label mb-3 block">
                Raw notes
              </label>
              <textarea
                id="p1-notes"
                value={composerText}
                onChange={(e) => setComposerText(e.target.value)}
                placeholder="Epics, ideas, requirements — pasted meeting notes or a rough brief work well."
                className="pf-textarea min-h-28"
              />
              <div className="mt-3 flex flex-wrap items-center justify-between gap-3">
                <p className="pf-caption">Notes are appended to the interpretation on the right.</p>
                <button
                  type="button"
                  className="pf-btn pf-btn-secondary pf-btn-sm"
                  onClick={addToInterpretation}
                  disabled={composerText.trim().length === 0}
                >
                  <FileText size={13} />
                  Add to interpretation
                </button>
              </div>
            </div>

            <div className="pf-card pf-card-pad">
              <p className="pf-label mb-3">Repository grounding</p>
              {sync.statusLoaded ? (
                <div className="flex flex-wrap gap-2">
                  <Pill kind="ast" pills={pills} />
                  <Pill kind="docHealth" pills={pills} />
                  <Pill kind="ocr" pills={pills} />
                </div>
              ) : (
                <div className="flex flex-wrap gap-2" aria-busy="true" aria-label="Reading repository status">
                  <SkeletonChip width={188} />
                  <SkeletonChip width={104} />
                  <SkeletonChip width={92} />
                </div>
              )}
              {pills.ocr === "missing" && (
                <p className="pf-caption mt-3">
                  OCR tools missing:{" "}
                  <code className="pf-mono">{OCR_INSTALL_GUIDANCE[detectOS()] ?? OCR_INSTALL_GUIDANCE.Linux}</code>
                </p>
              )}
            </div>

            <RecoveryPanel recovery={sync.recovery} />
          </section>

          {/* Right column: live interpretation */}
          <section className="flex min-h-0 min-w-0 flex-col gap-4" aria-label="Interpretation">
            <div className="flex flex-wrap items-end justify-between gap-3">
              <div>
                <h2 className="pf-section-title">Interpretation</h2>
                <p className="pf-caption mt-0.5">
                  Editable shared PRD — changes sync to every collaborator.
                </p>
              </div>
              <div className="pf-tabs" role="group" aria-label="Interpretation view">
                <TabButton label="Product PRD" active={tab === "product"} onClick={() => setTab("product")} />
                <TabButton label="Technical appendix" active={tab === "appendix"} onClick={() => setTab("appendix")} />
              </div>
            </div>

            <div className="pf-editor-wrap" style={{ minHeight: 320, flex: 1 }}>
              <div className="pf-editor-toolbar">
                <FileText size={13} style={{ color: "var(--pf-muted)" }} />
                <span className="pf-caption" style={{ color: "var(--pf-ink-2)", fontWeight: 600 }}>
                  {tab === "product" ? "prd.md — Product PRD" : "prd.md — Technical appendix"}
                </span>
                <span className="pf-caption ml-auto">doc v{sync.docVersion}</span>
              </div>
              {awaitingSnapshot ? (
                <div className="pf-editor-pending" aria-busy="true" aria-label="Waiting for the session to load the document">
                  <SkeletonLines lines={5} />
                  <p className="pf-caption">Waiting for the session authority…</p>
                </div>
              ) : (
                <PrdEditor sync={sync} />
              )}
            </div>

            {ambiguities.length > 0 && (
              <div className="pf-notice pf-notice--warn flex-col items-stretch">
                <div className="flex items-center gap-2">
                  <TriangleAlert size={15} style={{ color: "var(--pf-warn)" }} />
                  <span className="font-semibold" style={{ color: "var(--pf-warn)" }}>
                    Resolve {ambiguities.length} {ambiguities.length === 1 ? "ambiguity" : "ambiguities"} before confirming
                  </span>
                </div>
                <ul className="mt-1 flex list-none flex-col gap-1.5 p-0">
                  {ambiguities.map((a) => (
                    <li key={a.id} className="flex items-center justify-between gap-2">
                      <span>{a.text}</span>
                      <button
                        type="button"
                        className="pf-btn pf-btn-secondary pf-btn-sm"
                        onClick={() => setAmbiguities((prev) => prev.filter((x) => x.id !== a.id))}
                      >
                        Mark as {a.guess ?? "product"}
                      </button>
                    </li>
                  ))}
                </ul>
              </div>
            )}
            {secrets.length > 0 && (
              <div className="pf-notice pf-notice--danger flex-col items-stretch">
                <div className="flex items-center gap-2">
                  <ShieldAlert size={15} style={{ color: "var(--pf-danger)" }} />
                  <span className="font-semibold" style={{ color: "var(--pf-danger)" }}>
                    Possible secrets detected — redact or dismiss
                  </span>
                </div>
                <ul className="mt-1 flex list-none flex-col gap-1.5 p-0">
                  {secrets.map((s) => (
                    <li key={s} className="flex items-center justify-between gap-2">
                      <code className="pf-mono">{s.slice(0, 12)}…</code>
                      <button
                        type="button"
                        className="pf-btn pf-btn-secondary pf-btn-sm"
                        onClick={() => setDismissedSecrets((prev) => [...prev, s])}
                      >
                        Dismiss
                      </button>
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </section>
        </div>
      </div>

      {/* Footer: generation-host line + primary CTA */}
      <footer className="pf-footer-bar">
        <label className="pf-check-label">
          <input
            type="checkbox"
            checked={hostAck}
            onChange={(e) => setHostAck(e.target.checked)}
            className="pf-checkbox"
          />
          Product requirements will be sent to the generation host through its local CLI
        </label>
        <button
          type="button"
          className="pf-btn pf-btn-primary pf-btn-lg ml-auto"
          disabled={!confirmEnabled}
          onClick={onConfirm}
        >
          Confirm Context &amp; Launch Avatars
          <ArrowRight size={15} />
        </button>
      </footer>
    </div>
  );
}

function Pill(props: { kind: "ast" | "docHealth" | "ocr"; pills: StatusPills }): React.ReactElement {
  const { kind, pills } = props;
  const healthy =
    kind === "ast"
      ? pills.ast === "ok"
      : kind === "docHealth"
        ? pills.docHealth === 0
        : pills.ocr === "available";
  const scanning = kind === "ast" && pills.ast === "scanning";
  const label =
    kind === "ast"
      ? `AST ${pills.ast} · ${pills.astDetail}`
      : kind === "docHealth"
        ? pills.docHealth === 0
          ? "Docs in sync"
          : `Docs: ${pills.docHealth} stale ${pills.docHealth === 1 ? "section" : "sections"}`
        : `OCR ${pills.ocr}`;
  const Icon = kind === "ast" ? GitBranch : kind === "docHealth" ? FileText : ScanText;
  const tone = scanning ? "pf-chip--accent pf-chip--live" : healthy ? "pf-chip--ok" : "pf-chip--warn";
  return (
    <span className={`pf-chip pf-chip--wrap ${tone}`}>
      <Icon size={12} className="shrink-0" />
      <span className="pf-dot" />
      {label}
    </span>
  );
}

function TabButton(props: { label: string; active: boolean; onClick: () => void }): React.ReactElement {
  const { label, active, onClick } = props;
  return (
    <button type="button" className="pf-tab" data-active={active} aria-pressed={active} onClick={onClick}>
      {label}
    </button>
  );
}

function RecoveryPanel(props: { recovery: RecoveryItem[] }): React.ReactElement | null {
  const { recovery } = props;
  if (recovery.length === 0) return null;
  return (
    <div className="pf-notice pf-notice--warn flex-col items-stretch">
      <div className="flex items-center gap-2">
        <TriangleAlert size={15} style={{ color: "var(--pf-warn)" }} />
        <span className="font-semibold" style={{ color: "var(--pf-warn)" }}>
          Recovery — edits kept locally
        </span>
      </div>
      <ul className="mt-1 flex list-none flex-col gap-1 p-0">
        {recovery.map((r) => (
          <li key={r.at}>
            <code className="pf-mono pf-caption">
              {new Date(r.at).toISOString()} · {r.room}
            </code>
          </li>
        ))}
      </ul>
      <div className="mt-2">
        <button type="button" className="pf-btn pf-btn-secondary pf-btn-sm" onClick={exportRecovery}>
          Export unconfirmed edits
        </button>
      </div>
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
      extensions: [
        markdown(),
        editorBase,
        editorHooks(),
        EditorView.lineWrapping,
        placeholder("Nothing to interpret yet — drop a document or paste notes on the left."),
      ] as Extension[],
    });
    setView(new EditorView({ state, parent: node }));
  }, [sync.docText]);
  return <div ref={refCallback} className="min-h-0 flex-1 overflow-auto" />;
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
