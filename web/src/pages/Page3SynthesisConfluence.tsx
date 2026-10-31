import { useEffect, useMemo, useRef, useState } from "react";
import {
  ChartGantt,
  CircleCheck,
  FileCheck,
  ListTree,
  LoaderCircle,
  Lock,
  ShieldCheck,
  TriangleAlert,
  Zap,
} from "lucide-react";

import { MarkdownView } from "../components/MarkdownView";
import { MermaidSvg } from "../components/MermaidSvg";
import { useSync, approve, navigate } from "../store/syncStore";
import type { DraftSummary } from "../store/syncStore";
import { approveHint, approveLabel } from "../ui/handoff";
import type { EpicRow } from "../ui/matrix";
import { useScrollSpy } from "../ui/scrollSpy";
import { SkeletonCard, SkeletonLines } from "../ui/Skeleton";
import { HandoffPanel } from "./HandoffPanel";
import { JiraMatrix } from "./JiraMatrix";

/**
 * Page 3 — Master specification (Confluence-style).
 *
 * Read-only document: sticky TOC with scroll-spy, executive summary, Mermaid
 * architecture/sequence diagrams (expandable, with source), immutable
 * constraints, AST file/symbol targets, and a Documentation Sync Tasks
 * section. Jira matrix: accordion Epics → Stories → Subtasks with acceptance
 * criteria, target files, copyable verification commands, expand/collapse all
 * and a filter. Dual Gantt: two Mermaid gantt diagrams (human 9h/day calendar;
 * agent elapsed minutes) plus summary chips labeled "model estimate". States:
 * loading, Mermaid error fallback, stale approval, approving (frozen), approve
 * failed, approved with the plan-mode handoff panel and close countdown.
 * Primary CTA: Approve & open in <host> Plan Mode (owner only); followers see
 * it disabled with "owner approves".
 *
 * The view is data-driven: it derives from the synthesizer's `synthesis.v1`
 * draft (`spec_markdown` + `manifest_json`). The manifest optionally carries
 * structured epics, diagrams, and gantt estimates; markdown sections are
 * extracted from `spec_markdown` headings.
 */

interface GanttChips {
  human: string;
  agent: string;
}

interface SpecView {
  markdown: string;
  epics: EpicRow[];
  architectureDiagram: string;
  sequenceDiagram: string;
  humanGantt: string;
  agentGantt: string;
  ganttChips: GanttChips;
}

type Page3State =
  | "loading"
  | "stale-approval"
  | "approving"
  | "approve-failed"
  | "approved";

interface ManifestShape {
  epics?: EpicRow[];
  architecture_diagram?: string;
  sequence_diagram?: string;
  human_gantt?: string;
  agent_gantt?: string;
  gantt_chips?: Partial<GanttChips>;
}

/** Pull the first mermaid fenced block whose source starts with `head`. */
function mermaidBlock(markdown: string, head: RegExp): string {
  const blocks = markdown.matchAll(/```mermaid\n([\s\S]*?)```/g);
  for (const b of blocks) {
    const src = (b[1] ?? "").trim();
    if (head.test(src)) return src;
  }
  return "";
}

/** Derive the view model from the synthesizer draft; null until it exists. */
function specFromDraft(draft: DraftSummary | undefined): SpecView | null {
  if (draft?.content === undefined) return null;
  let parsed: { spec_markdown?: string; manifest_json?: string };
  try {
    parsed = JSON.parse(draft.content) as typeof parsed;
  } catch {
    return null;
  }
  const markdownText = parsed.spec_markdown ?? "";
  let manifest: ManifestShape = {};
  if (typeof parsed.manifest_json === "string" && parsed.manifest_json.trim() !== "") {
    try {
      manifest = JSON.parse(parsed.manifest_json) as ManifestShape;
    } catch {
      manifest = {};
    }
  }
  return {
    markdown: markdownText,
    epics: manifest.epics ?? [],
    architectureDiagram:
      manifest.architecture_diagram ?? mermaidBlock(markdownText, /^(graph|flowchart)\b/),
    sequenceDiagram: manifest.sequence_diagram ?? mermaidBlock(markdownText, /^sequenceDiagram\b/),
    humanGantt: manifest.human_gantt ?? "",
    agentGantt: manifest.agent_gantt ?? "",
    ganttChips: {
      human: manifest.gantt_chips?.human ?? "—",
      agent: manifest.gantt_chips?.agent ?? "—",
    },
  };
}

/** Contents entries, in document order; `id` is the section element id. */
const TOC: ReadonlyArray<{ id: string; label: string }> = [
  { id: "executive-summary", label: "Executive summary" },
  { id: "architecture", label: "Architecture" },
  { id: "immutable-constraints", label: "Immutable constraints" },
  { id: "ast-targets", label: "AST targets" },
  { id: "documentation-sync", label: "Documentation Sync" },
  { id: "jira-matrix", label: "Jira matrix" },
  { id: "dual-schedule", label: "Dual schedule" },
];
const TOC_IDS = TOC.map((t) => t.id);

export function Page3SynthesisConfluence(): React.ReactElement {
  const sync = useSync();
  const synthesisDraft = sync.drafts.find((d) => d.schemaId === "synthesis.v1");
  const spec = useMemo(() => specFromDraft(synthesisDraft), [synthesisDraft]);
  const [pageState, setPageState] = useState<Page3State>("loading");
  const [failureReason] = useState<string | null>(null);
  const bodyRef = useRef<HTMLDivElement>(null);
  const docRef = useRef<HTMLDivElement>(null);
  const spy = useScrollSpy(bodyRef, TOC_IDS);
  const { refresh } = spy;

  const approved = sync.phase === "APPROVED" || pageState === "approved";
  const ownerApproves = sync.role === "owner";
  const host = sync.status.requestedHost;

  const ctaDisabled =
    !ownerApproves || pageState === "approving" || approved || spec === null;

  const onApprove = (): void => {
    if (ctaDisabled) return;
    setPageState("approving");
    const runId = sync.runId ?? spec?.markdown.match(/run[_-]id: ([\w-]+)/)?.[1] ?? "";
    approve(runId, 1, synthesisDraft?.contentHash ?? "");
  };

  // The spy measures section tops; they move when the document first renders
  // and whenever an accordion opens or a diagram lands, so re-measure on any
  // size change of the document column.
  useEffect(() => {
    refresh();
    const doc = docRef.current;
    if (doc === null || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(() => refresh());
    observer.observe(doc);
    return () => observer.disconnect();
  }, [refresh, spec]);

  return (
    <div className="pf-page">
      <div className="flex min-h-0 flex-1">
        {/* Sticky TOC with scroll-spy */}
        <nav className="pf-toc pf-scroll-thin hidden lg:block" aria-label="Contents">
          <p className="pf-label mb-3 px-2.5">Contents</p>
          <ul className="m-0 flex list-none flex-col gap-0.5 p-0">
            {TOC.map((t) => (
              <li key={t.id}>
                <a
                  className="pf-toc-link"
                  href={`#${t.id}`}
                  data-active={spec !== null && spy.active === t.id}
                  aria-current={spec !== null && spy.active === t.id ? "location" : undefined}
                  onClick={(e) => {
                    e.preventDefault();
                    spy.scrollTo(t.id);
                  }}
                >
                  {t.label}
                </a>
              </li>
            ))}
          </ul>
          {synthesisDraft !== undefined && (
            <div className="mt-6 border-t px-2.5 pt-4" style={{ borderColor: "var(--pf-border)" }}>
              <p className="pf-label mb-2">Draft</p>
              <p className="pf-caption pf-mono">rev {synthesisDraft.revision}</p>
              {synthesisDraft.contentHash !== undefined && (
                <p className="pf-caption pf-mono">sha {synthesisDraft.contentHash.slice(0, 10)}</p>
              )}
            </div>
          )}
        </nav>

        <div ref={bodyRef} className="pf-page-body pf-scroll-thin">
          <div ref={docRef} className="pf-doc-wrap">
            {pageState === "stale-approval" && (
              <div className="pf-notice pf-notice--warn mb-5">
                <TriangleAlert size={16} className="mt-0.5 shrink-0" style={{ color: "var(--pf-warn)" }} />
                <div className="flex-1">
                  <p className="font-semibold" style={{ color: "var(--pf-warn)" }}>Stale approval</p>
                  <p className="pf-caption mt-0.5">
                    The synthesis changed since this view was opened — review the latest draft.
                  </p>
                </div>
                <button
                  type="button"
                  className="pf-btn pf-btn-secondary pf-btn-sm self-center"
                  onClick={() => navigate("/deliberate")}
                >
                  Review latest
                </button>
              </div>
            )}
            {pageState === "approve-failed" && (
              <div className="pf-notice pf-notice--danger mb-5">
                <TriangleAlert size={16} className="mt-0.5 shrink-0" style={{ color: "var(--pf-danger)" }} />
                <div className="flex-1">
                  <p className="font-semibold" style={{ color: "var(--pf-danger)" }}>Approve failed</p>
                  <p className="pf-caption mt-0.5">{failureReason ?? "artifact commit failed"}</p>
                </div>
                <button
                  type="button"
                  className="pf-btn pf-btn-secondary pf-btn-sm self-center"
                  onClick={() => {
                    setPageState("approving");
                    const runId = sync.runId ?? spec?.markdown.match(/run[_-]id: ([\w-]+)/)?.[1] ?? "";
                    approve(runId, 1, synthesisDraft?.contentHash ?? "");
                  }}
                >
                  Retry
                </button>
              </div>
            )}

            {spec === null ? (
              <div aria-busy="true" aria-label="Compiling the master specification">
                <div className="pf-empty mt-4">
                  <span className="pf-empty-icon" data-busy="true">
                    <LoaderCircle size={20} className="animate-spin" />
                  </span>
                  <p className="pf-body font-medium" style={{ color: "var(--pf-ink)" }}>
                    Compiling the master specification
                  </p>
                  <p className="pf-caption max-w-md">
                    The Lead Synthesizer is merging avatar contracts into a single
                    spec and plan manifest. The document appears here when the draft lands.
                  </p>
                </div>
                <div className="mt-6 flex flex-col gap-4">
                  <SkeletonCard lines={3} />
                  <SkeletonLines lines={4} />
                  <SkeletonCard lines={2} />
                </div>
              </div>
            ) : (
              <>
                <header className="pf-page-header pf-doc-header">
                  <div className="flex flex-wrap items-center gap-2.5">
                    <h1 className="pf-title">Master specification</h1>
                    {approved && (
                      <span className="pf-chip pf-chip--swap pf-chip--ok">
                        <CircleCheck size={12} />
                        Approved
                      </span>
                    )}
                    {pageState === "approving" && !approved && (
                      <span className="pf-chip pf-chip--swap pf-chip--accent pf-chip--live">
                        <LoaderCircle size={12} className="animate-spin" />
                        Approving
                      </span>
                    )}
                  </div>
                  <p className="pf-caption">
                    Compiled from cycle {sync.cycle} deliberation · run {sync.runId ?? "—"}
                  </p>
                </header>

                {approved && <HandoffPanel requestedHost={host} members={sync.members} />}

                <section id="executive-summary" className="pf-card pf-card-pad pf-doc-section">
                  <div className="mb-2 flex items-center gap-2">
                    <FileCheck size={15} style={{ color: "var(--pf-accent)" }} />
                    <h2 className="pf-section-title">Executive summary</h2>
                  </div>
                  <MarkdownView className="pf-doc" markdown={sectionOf(spec.markdown, "Executive summary")} />
                </section>

                <section id="architecture" className="pf-doc-section">
                  <div className="mb-3 flex items-center gap-2">
                    <ListTree size={15} style={{ color: "var(--pf-accent)" }} />
                    <h2 className="pf-section-title">Architecture</h2>
                  </div>
                  {spec.architectureDiagram !== "" ? (
                    <MermaidSvg source={spec.architectureDiagram} title="Architecture — component graph" />
                  ) : (
                    <p className="pf-caption">No architecture diagram in this draft.</p>
                  )}
                  {spec.sequenceDiagram !== "" && (
                    <div className="mt-4">
                      <MermaidSvg source={spec.sequenceDiagram} title="Sequence — primary flow" />
                    </div>
                  )}
                </section>

                <section id="immutable-constraints" className="pf-card pf-card-pad pf-doc-section">
                  <div className="mb-2 flex items-center gap-2">
                    <Lock size={15} style={{ color: "var(--pf-danger)" }} />
                    <h2 className="pf-section-title">Immutable constraints</h2>
                  </div>
                  <MarkdownView className="pf-doc" markdown={sectionOf(spec.markdown, "Immutable constraints")} />
                </section>

                <section id="ast-targets" className="pf-card pf-card-pad pf-doc-section">
                  <div className="mb-2 flex items-center gap-2">
                    <ListTree size={15} style={{ color: "var(--pf-accent)" }} />
                    <h2 className="pf-section-title">AST file/symbol targets</h2>
                  </div>
                  <MarkdownView className="pf-doc" markdown={sectionOf(spec.markdown, "AST targets")} />
                </section>

                <section id="documentation-sync" className="pf-card pf-card-pad pf-doc-section">
                  <div className="mb-2 flex items-center gap-2">
                    <ShieldCheck size={15} style={{ color: "var(--pf-ok)" }} />
                    <h2 className="pf-section-title">Documentation sync tasks</h2>
                  </div>
                  <MarkdownView
                    className="pf-doc"
                    markdown={sectionOf(spec.markdown, "Documentation Sync")}
                  />
                </section>

                {/* Jira matrix */}
                <section id="jira-matrix" className="pf-doc-section">
                  <div className="mb-3 flex items-center gap-2">
                    <ListTree size={15} style={{ color: "var(--pf-accent)" }} />
                    <h2 className="pf-section-title">Jira matrix</h2>
                  </div>
                  {spec.epics.length === 0 ? (
                    <div className="pf-empty">
                      <span className="pf-empty-icon">
                        <ListTree size={20} />
                      </span>
                      <p className="pf-body font-medium" style={{ color: "var(--pf-ink)" }}>
                        No epics in this draft
                      </p>
                      <p className="pf-caption max-w-md">
                        The plan manifest did not carry a work breakdown. Re-run
                        synthesis after the PM avatar publishes typed epics.
                      </p>
                    </div>
                  ) : (
                    <JiraMatrix epics={spec.epics} />
                  )}
                </section>

                {/* Dual Gantt */}
                <section id="dual-schedule" className="pf-doc-section">
                  <div className="mb-3 flex items-center gap-2">
                    <ChartGantt size={15} style={{ color: "var(--pf-accent)" }} />
                    <h2 className="pf-section-title">Dual schedule</h2>
                  </div>
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="pf-chip pf-chip--accent" title="model estimate, not measured">
                      <span className="pf-dot" />
                      Human: {spec.ganttChips.human}
                    </span>
                    <span className="pf-chip pf-chip--ok" title="model estimate, not measured">
                      <Zap size={11} />
                      Coding Agent: {spec.ganttChips.agent}
                    </span>
                    <span className="pf-chip">model estimate, not measured</span>
                  </div>
                  <div className="mt-4 flex flex-col gap-4">
                    {spec.humanGantt !== "" ? (
                      <MermaidSvg source={spec.humanGantt} title="Human — 9h/day calendar" />
                    ) : (
                      <p className="pf-caption">No human schedule in this draft.</p>
                    )}
                    {spec.agentGantt !== "" ? (
                      <MermaidSvg source={spec.agentGantt} title="Coding Agent — elapsed minutes" />
                    ) : (
                      <p className="pf-caption">No agent schedule in this draft.</p>
                    )}
                  </div>
                </section>
              </>
            )}
          </div>
        </div>
      </div>

      {/* Primary CTA */}
      <footer className="pf-footer-bar">
        {approved ? (
          <p className="pf-body pf-footer-note" style={{ color: "var(--pf-ok)" }}>
            <CircleCheck size={15} className="shrink-0" />
            Approved — artifacts written to docs/plans/.plan/synthesized_spec.md and docs/plans/.plan/plan_manifest.json
          </p>
        ) : (
          <>
            <p className="pf-caption pf-footer-note">
              {ownerApproves ? approveHint(host) : "Only the session owner can approve."}
            </p>
            <button
              type="button"
              className="pf-btn pf-btn-primary pf-btn-lg ml-auto"
              disabled={ctaDisabled}
              onClick={onApprove}
            >
              {pageState === "approving" ? (
                <>
                  <LoaderCircle size={15} className="animate-spin" />
                  Approving…
                </>
              ) : ownerApproves ? (
                approveLabel(host)
              ) : (
                "Owner approves"
              )}
            </button>
          </>
        )}
      </footer>
    </div>
  );
}

function sectionOf(markdown: string, heading: string): string {
  const lines = markdown.split("\n");
  const isHeading = (l: string): boolean => /^#{1,6}\s/.test(l);
  // Prefer a real heading line; fall back to any line that names the section.
  const headingAt = lines.findIndex((l) => isHeading(l) && l.includes(heading));
  const start = headingAt !== -1 ? headingAt : lines.findIndex((l) => l.includes(heading));
  if (start === -1) return `_${heading}: not present_`;
  let end = lines.length;
  for (let i = start + 1; i < lines.length; i++) {
    if (/^#{1,2}\s/.test(lines[i]!)) {
      end = i;
      break;
    }
  }
  // The card already shows the section title: keep the heading out of the body.
  return lines.slice(headingAt !== -1 ? start + 1 : start, end).join("\n");
}
