import { useCallback, useEffect, useRef, useState } from "react";
import {
  ArrowRight,
  BookOpen,
  FileText,
  GitBranch,
  LoaderCircle,
  Paperclip,
  ScanText,
  TriangleAlert,
  Waypoints,
  X,
} from "lucide-react";

import { apiFetch } from "../protocol/bridge";
import { exportRecovery, freezeRequest, useSync } from "../store/syncStore";
import type { RecoveryItem, StatusView } from "../store/syncStore";
import { MarkdownEditor, type EditorApi } from "../ui/editor/MarkdownEditor";
import { SkeletonChip, SkeletonLines } from "../ui/Skeleton";
import { PlanRail } from "./composer/PlanRail";
import { useComponentBlocks, useImpact, usePlanFiles, useProjectContext } from "./composer/usePlanData";

/**
 * Page 1 — the composer.
 *
 * Left: one large Markdown editor over the shared `prd` document, with a
 * toolbar, a `/` block menu, `@` completion over the repository's components,
 * and drag-and-drop for .docx, .pdf and .md files (parsed on this machine and
 * appended as a `## Source:` section).
 *
 * Right: the plan as it forms. The interpretation that runs a few seconds
 * after typing stops groups the requirements into epics; each shows as a file
 * that opens in a modal, linked to the components and project notes it
 * matches. A Codebase tab and a Notes tab sit beside it.
 *
 * Footer: what the repository scan and the context step found, and the
 * Confirm button that freezes the document and starts the avatars.
 */

const MAX_UPLOAD_BYTES = 25 * 1024 * 1024;
const MAX_UPLOAD_FILES = 5;
const UPLOAD_ACCEPT = ".docx,.pdf,.md,.markdown";
/** How long the rail says "updating" after an edit with no new interpretation. */
const INTERPRETING_WINDOW_MS = 20_000;

const OCR_INSTALL_GUIDANCE: Record<string, string> = {
  Darwin: "brew install poppler tesseract",
  Linux: "apt install poppler-utils tesseract-ocr",
  Windows: "winget install UB-Mannheim.TesseractOCR + Poppler bin on PATH",
};

const PLACEHOLDER =
  "Describe what you want to build, for whom, and what done looks like.\n\n" +
  "Paste a brief or meeting notes, drop a document, or type / for epics, checklists and tables.";

interface Attachment {
  name: string;
  warnings: string[];
  error?: string;
}

/** One file as POST /api/upload reports it (`app.uploadFileResult`). */
interface UploadResult {
  name: string;
  appended: boolean;
  warnings?: string[];
  error?: string;
}

export function Page1Ingestion(): React.ReactElement {
  const sync = useSync();
  const status = sync.status;
  const editorApi = useRef<EditorApi | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const [uploading, setUploading] = useState<string | null>(null);
  const [uploadError, setUploadError] = useState<string | null>(null);
  const [attachments, setAttachments] = useState<Attachment[]>([]);

  const isOwner = sync.role === "owner";
  const canEdit = sync.role === "owner" || sync.role === "editor";
  const awaitingSnapshot = sync.role === null;
  const hasText = sync.docText.trim().length > 0;

  // The interpretation revision moves when the window hears the new draft or
  // the next status poll lands, whichever comes first.
  const draftRevision = Number(sync.drafts.find((d) => d.avatarId === "ingestion")?.revision ?? 0);
  const revision = Math.max(status.interpretation ?? 0, Number.isFinite(draftRevision) ? draftRevision : 0);
  // The impact map moves on its own: it is published twice per interpretation,
  // once with the keyword matches and once when the model has read the project.
  const impactRevision = Number(status.impactRevision ?? 0);
  const plan = usePlanFiles(revision, impactRevision);
  const impact = useImpact(impactRevision);
  const ctx = useProjectContext(`${status.context ?? ""}|${status.contextDetail ?? ""}|${revision}`);
  const scanDone = status.ast !== "scanning";
  const blocks = useComponentBlocks(scanDone && sync.statusLoaded);
  const interpreting = useInterpreting(hasText, sync.lastLocalEdit, revision);

  // A file dropped anywhere but the editor would make the browser navigate to
  // it and take the planning window with it. Swallow stray file drops.
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
      if (!isOwner) return; // the owner's machine parses files
      const files = list.slice(0, MAX_UPLOAD_FILES);
      const tooBig = files.find((f) => f.size > MAX_UPLOAD_BYTES);
      if (tooBig !== undefined) {
        setUploadError(`${tooBig.name} is larger than 25 MiB`);
        return;
      }
      setUploadError(null);
      for (const f of files) {
        setUploading(f.name);
        try {
          const body = new FormData();
          body.append("file", f);
          const res = await apiFetch("/api/upload", { method: "POST", body });
          const out = (await res.json().catch(() => ({}))) as { files?: UploadResult[]; error?: string };
          if (!res.ok) throw new Error(out.error ?? `upload failed (${res.status})`);
          const results = out.files ?? [];
          setAttachments((prev) => [
            ...prev,
            ...results.map((r) => ({
              name: r.name,
              warnings: r.warnings ?? [],
              ...(r.error !== undefined && r.error !== "" ? { error: r.error } : {}),
            })),
          ]);
        } catch (err) {
          setUploadError(err instanceof Error ? err.message : "upload failed");
        }
      }
      setUploading(null);
    },
    [isOwner],
  );

  const confirmEnabled = isOwner && hasText && uploading === null && !sync.pendingEdits;
  const host = status.mainHost !== undefined && status.mainHost !== "" ? status.mainHost : (status.requestedHost ?? "your agent");

  return (
    <div className="pf-page">
      <div className="pf-page-body pf-composer-page">
        <div className="pf-composer-layout">
          <section className="pf-composer-main" aria-labelledby="p1-title">
            <header className="pf-composer-head">
              <h1 id="p1-title" className="pf-title">
                What are we building?
              </h1>
              <p className="pf-caption">
                Write the requirements in your own words. Plan Forge groups them into epics, links each to the code it
                touches, and reads the project's notes before any avatar plans.
              </p>
            </header>

            {awaitingSnapshot ? (
              <div className="pf-composer pf-composer--pending" aria-busy="true" aria-label="Loading the document">
                <SkeletonLines lines={7} />
              </div>
            ) : (
              <MarkdownEditor
                initialText={sync.docText}
                text={sync.docText}
                canEdit={canEdit}
                syncing={sync.pendingEdits}
                placeholder={PLACEHOLDER}
                apiRef={editorApi}
                {...(isOwner ? { onDropFiles: (files: File[]) => void uploadFiles(files) } : {})}
                actions={
                  isOwner ? (
                    <button
                      type="button"
                      className="pf-tool-text"
                      onClick={() => fileInput.current?.click()}
                      disabled={uploading !== null}
                      title="Attach .docx, .pdf or .md files (up to 5, 25 MiB each)"
                    >
                      {uploading !== null ? <LoaderCircle size={14} className="animate-spin" /> : <Paperclip size={14} />}
                      {uploading !== null ? "Reading…" : "Attach"}
                    </button>
                  ) : null
                }
              />
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

            {(attachments.length > 0 || uploadError !== null) && (
              <div className="pf-attachments">
                {attachments.map((a, i) => (
                  <span
                    key={`${a.name}-${i}`}
                    className={`pf-attachment ${a.error !== undefined ? "pf-attachment--error" : ""}`}
                    title={a.error ?? (a.warnings.length > 0 ? a.warnings.join("\n") : `${a.name} was added to the document`)}
                  >
                    {a.error !== undefined || a.warnings.length > 0 ? <TriangleAlert size={12} /> : <FileText size={12} />}
                    {a.name}
                    {a.error !== undefined && <span className="pf-caption">— {a.error}</span>}
                  </span>
                ))}
                {uploadError !== null && (
                  <span className="pf-attachment pf-attachment--error" role="alert">
                    <TriangleAlert size={12} />
                    {uploadError}
                    <button type="button" className="pf-attachment-x" aria-label="Dismiss" onClick={() => setUploadError(null)}>
                      <X size={11} />
                    </button>
                  </span>
                )}
              </div>
            )}
            <RecoveryPanel recovery={sync.recovery} />
          </section>

          <PlanRail
            files={plan.files}
            interpreting={interpreting}
            hasText={hasText}
            blocks={blocks}
            scanning={!scanDone}
            ctx={ctx}
            impact={impact}
            onInsert={(text) => editorApi.current?.insertText(text)}
          />
        </div>
      </div>

      <footer className="pf-footer-bar">
        <div className="pf-footer-status" aria-label="Grounding">
          {sync.statusLoaded ? (
            <>
              <Pill kind="ast" status={status} />
              <Pill kind="docHealth" status={status} />
              <Pill kind="impact" status={status} />
              <Pill kind="context" status={status} />
              {status.ocr === "missing" && <Pill kind="ocr" status={status} />}
            </>
          ) : (
            <>
              <SkeletonChip width={150} />
              <SkeletonChip width={96} />
              <SkeletonChip width={120} />
            </>
          )}
        </div>
        <span className="pf-footer-note pf-caption">
          {isOwner ? `Confirming sends this text to ${host} on this machine.` : "Only the session owner can confirm."}
        </span>
        <button
          type="button"
          className="pf-btn pf-btn-primary pf-btn-lg"
          disabled={!confirmEnabled}
          onClick={freezeRequest}
          title={!hasText ? "Write the requirements first" : sync.pendingEdits ? "Saving your last edits…" : undefined}
        >
          Confirm Context &amp; Launch Avatars
          <ArrowRight size={15} />
        </button>
      </footer>
    </div>
  );
}

/**
 * True while requirements this window wrote wait for an interpretation: from
 * the last local edit until a newer revision lands, for at most 20 s. Before
 * the first interpretation, the page load counts as the edit.
 */
function useInterpreting(hasText: boolean, lastLocalEdit: number, revision: number): boolean {
  const [mountedAt] = useState(() => Date.now());
  const [revisionAt, setRevisionAt] = useState({ revision, at: 0 });
  const [, tick] = useState(0);
  if (revisionAt.revision !== revision) setRevisionAt({ revision, at: Date.now() });
  const since = revision === 0 ? Math.max(lastLocalEdit, mountedAt) : lastLocalEdit;
  const waiting = hasText && since > revisionAt.at;
  useEffect(() => {
    if (!waiting) return;
    const id = setTimeout(() => tick((n) => n + 1), Math.max(0, since + INTERPRETING_WINDOW_MS - Date.now()) + 50);
    return () => clearTimeout(id);
  }, [waiting, since]);
  return waiting && Date.now() - since < INTERPRETING_WINDOW_MS;
}

function Pill(props: { kind: "ast" | "docHealth" | "context" | "impact" | "ocr"; status: StatusView }): React.ReactElement {
  const { kind, status } = props;
  let tone: string;
  let label: string;
  let title: string;
  let Icon: React.ComponentType<{ size?: number; className?: string }>;
  switch (kind) {
    case "ast": {
      const scanning = status.ast === "scanning";
      tone = scanning ? "pf-chip--accent pf-chip--live" : status.ast === "ok" ? "pf-chip--ok" : "pf-chip--warn";
      label = scanning ? "Reading code" : status.ast === "ok" ? "Code indexed" : `Code: ${status.ast}`;
      title = status.astDetail;
      Icon = GitBranch;
      break;
    }
    case "docHealth":
      tone = status.docHealth === 0 ? "pf-chip--ok" : "pf-chip--warn";
      label = status.docHealth === 0 ? "Docs in sync" : `${status.docHealth} stale doc ${status.docHealth === 1 ? "section" : "sections"}`;
      title = "Documentation checked against the code";
      Icon = FileText;
      break;
    case "context": {
      const c = status.context ?? "gathering";
      tone = c === "gathering" ? "pf-chip--accent pf-chip--live" : c === "ok" ? "pf-chip--ok" : c === "failed" ? "pf-chip--warn" : "";
      label = c === "gathering" ? "Reading notes" : c === "ok" ? "Project notes found" : c === "failed" ? "Notes unavailable" : "No related notes";
      title = status.contextDetail ?? "";
      Icon = BookOpen;
      break;
    }
    case "impact": {
      const i = status.impact ?? "mapping";
      tone = i === "mapping" || i === "quick" ? "pf-chip--accent pf-chip--live" : i === "ok" ? "pf-chip--ok" : "pf-chip--warn";
      label =
        i === "mapping"
          ? "Finding what this touches"
          : i === "quick"
            ? "Quick match"
            : i === "ok"
              ? "Mapped"
              : "Map unavailable";
      title = status.impactDetail ?? "";
      Icon = Waypoints;
      break;
    }
    default:
      tone = "pf-chip--warn";
      label = "OCR missing";
      title = `Scanned PDFs need OCR: ${OCR_INSTALL_GUIDANCE[detectOS()] ?? OCR_INSTALL_GUIDANCE.Linux}`;
      Icon = ScanText;
  }
  return (
    <span className={`pf-chip ${tone}`} title={title}>
      <Icon size={12} className="shrink-0" />
      <span className="pf-dot" />
      {label}
    </span>
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

function detectOS(): string {
  const ua = navigator.userAgent;
  if (ua.includes("Mac")) return "Darwin";
  if (ua.includes("Windows")) return "Windows";
  return "Linux";
}
