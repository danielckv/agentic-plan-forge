import { useMemo, useState } from "react";
import {
  ChartGantt,
  CircleCheck,
  ChevronRight,
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

/**
 * Page 3 — Master specification (Confluence-style).
 *
 * Read-only document: sticky TOC, executive summary, Mermaid architecture/
 * sequence diagrams, immutable constraints, AST file/symbol targets, and a
 * Documentation Sync Tasks section. Jira matrix: accordion Epics → Stories →
 * Subtasks with acceptance criteria, target files, verification commands.
 * Dual Gantt: two Mermaid gantt diagrams (human 9h/day calendar; agent
 * elapsed minutes) plus summary chips labeled "model estimate". States:
 * loading, Mermaid error fallback, stale approval, approving (frozen),
 * approve failed, approved with per-member sync list and "safe to close".
 * Primary CTA: Approve & Inject into Agent Plan Mode (owner only);
 * followers see it disabled with "owner approves".
 *
 * The view is data-driven: it derives from the synthesizer's `synthesis.v1`
 * draft (`spec_markdown` + `manifest_json`). The manifest optionally carries
 * structured epics, diagrams, and gantt estimates; markdown sections are
 * extracted from `spec_markdown` headings.
 */

export interface EpicRow {
  id: string;
  title: string;
  goal: string;
  stories: Array<{
    id: string;
    need: string;
    subtasks: Array<{
      id: string;
      title: string;
      severity?: "CRITICAL" | "HIGH" | "MEDIUM";
      acceptance: string[];
      targets: string[];
      verification: string[];
      humanEstimate: string;
      agentEstimate: string;
    }>;
  }>;
}

export interface GanttChips {
  human: string;
  agent: string;
}

export interface SpecView {
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

export function Page3SynthesisConfluence(): React.ReactElement {
  const sync = useSync();
  const synthesisDraft = sync.drafts.find((d) => d.schemaId === "synthesis.v1");
  const spec = useMemo(() => specFromDraft(synthesisDraft), [synthesisDraft]);
  const [pageState, setPageState] = useState<Page3State>("loading");
  const [openEpic, setOpenEpic] = useState<string | null>(null);
  const [openStory, setOpenStory] = useState<string | null>(null);
  const [failureReason] = useState<string | null>(null);

  const approved = sync.phase === "APPROVED" || pageState === "approved";
  const ownerApproves = sync.role === "owner";

  const ctaDisabled =
    !ownerApproves || pageState === "approving" || approved || spec === null;

  const onApprove = (): void => {
    if (ctaDisabled) return;
    setPageState("approving");
    const runId = sync.runId ?? spec?.markdown.match(/run[_-]id: ([\w-]+)/)?.[1] ?? "";
    approve(runId, 1, synthesisDraft?.contentHash ?? "");
  };

  return (
    <div className="pf-page">
      <div className="flex min-h-0 flex-1">
        {/* Sticky TOC */}
        <nav className="pf-toc pf-scroll-thin hidden lg:block">
          <p className="pf-label mb-3 px-2.5">Contents</p>
          <ul className="m-0 flex list-none flex-col gap-0.5 p-0">
            {TOC_ENTRIES.map((t) => (
              <li key={t}>
                <a className="pf-toc-link" href={`#${t.toLowerCase().replace(/[^a-z0-9]+/g, "-")}`}>
                  {t}
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

        <div className="pf-page-body pf-scroll-thin">
          <div className="mx-auto max-w-[860px] px-8 py-8">
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
              <div className="pf-empty mt-10">
                <span className="pf-empty-icon">
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
            ) : (
              <>
                <div className="mb-6 border-b pb-5" style={{ borderColor: "var(--pf-border)" }}>
                  <div className="flex items-center gap-2.5">
                    <h1 className="pf-title">Master specification</h1>
                    {approved && (
                      <span className="pf-chip pf-chip--ok">
                        <CircleCheck size={12} />
                        Approved
                      </span>
                    )}
                    {pageState === "approving" && (
                      <span className="pf-chip pf-chip--accent pf-chip--live">
                        <LoaderCircle size={12} className="animate-spin" />
                        Approving
                      </span>
                    )}
                  </div>
                  <p className="pf-caption mt-1">
                    Compiled from cycle {sync.cycle} deliberation · run {sync.runId ?? "—"}
                  </p>
                </div>

                <section id="executive-summary" className="pf-card pf-card-pad">
                  <div className="mb-2 flex items-center gap-2">
                    <FileCheck size={15} style={{ color: "var(--pf-accent)" }} />
                    <h2 className="pf-section-title">Executive summary</h2>
                  </div>
                  <MarkdownView className="pf-doc" markdown={sectionOf(spec.markdown, "Executive summary")} />
                </section>

                <section id="architecture" className="mt-6">
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

                <section id="immutable-constraints" className="pf-card pf-card-pad mt-6">
                  <div className="mb-2 flex items-center gap-2">
                    <Lock size={15} style={{ color: "var(--pf-danger)" }} />
                    <h2 className="pf-section-title">Immutable constraints</h2>
                  </div>
                  <MarkdownView className="pf-doc" markdown={sectionOf(spec.markdown, "Immutable constraints")} />
                </section>

                <section id="ast-targets" className="pf-card pf-card-pad mt-6">
                  <div className="mb-2 flex items-center gap-2">
                    <ListTree size={15} style={{ color: "var(--pf-accent)" }} />
                    <h2 className="pf-section-title">AST file/symbol targets</h2>
                  </div>
                  <MarkdownView className="pf-doc" markdown={sectionOf(spec.markdown, "AST targets")} />
                </section>

                <section id="documentation-sync" className="pf-card pf-card-pad mt-6">
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
                <section id="jira-matrix" className="mt-6">
                  <div className="mb-3 flex items-center gap-2">
                    <ListTree size={15} style={{ color: "var(--pf-accent)" }} />
                    <h2 className="pf-section-title">Jira matrix</h2>
                    {spec.epics.length > 0 && (
                      <span className="pf-caption">
                        {spec.epics.length} {spec.epics.length === 1 ? "epic" : "epics"} ·{" "}
                        {spec.epics.reduce((n, e) => n + e.stories.length, 0)} stories
                      </span>
                    )}
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
                    <div className="flex flex-col gap-3">
                      {spec.epics.map((epic) => (
                        <div key={epic.id} className="pf-accordion shadow-(--pf-shadow-sm)">
                          <button
                            type="button"
                            className="pf-accordion-trigger"
                            onClick={() => setOpenEpic(openEpic === epic.id ? null : epic.id)}
                          >
                            <ChevronRight
                              size={15}
                              className="pf-accordion-chevron"
                              data-open={openEpic === epic.id}
                            />
                            <span className="pf-chip pf-chip--epic">{epic.id}</span>
                            <span className="text-[14px] font-semibold" style={{ color: "var(--pf-ink)" }}>
                              {epic.title}
                            </span>
                            <span className="pf-caption ml-auto">
                              {epic.stories.length} {epic.stories.length === 1 ? "story" : "stories"}
                            </span>
                          </button>
                          {openEpic === epic.id && (
                            <div className="pf-accordion-body px-5 py-4">
                              <p className="pf-caption mb-4">{epic.goal}</p>
                              <div className="flex flex-col gap-2">
                                {epic.stories.map((story) => (
                                  <div
                                    key={story.id}
                                    className="rounded-lg border"
                                    style={{ borderColor: "var(--pf-border)", background: "var(--pf-panel)" }}
                                  >
                                    <button
                                      type="button"
                                      className="pf-accordion-trigger"
                                      onClick={() => setOpenStory(openStory === story.id ? null : story.id)}
                                    >
                                      <ChevronRight
                                        size={14}
                                        className="pf-accordion-chevron"
                                        data-open={openStory === story.id}
                                      />
                                      <span className="pf-chip">{story.id}</span>
                                      <span className="text-[13.5px] font-medium" style={{ color: "var(--pf-ink)" }}>
                                        {story.need}
                                      </span>
                                    </button>
                                    {openStory === story.id && (
                                      <div
                                        className="flex flex-col gap-2.5 border-t px-4 py-3"
                                        style={{ borderColor: "var(--pf-border)" }}
                                      >
                                        {story.subtasks.map((st) => (
                                          <div
                                            key={st.id}
                                            className="rounded-lg border p-3.5"
                                            style={{
                                              borderColor: "var(--pf-border)",
                                              background: "var(--pf-panel-subtle)",
                                            }}
                                          >
                                            <div className="flex items-center gap-2">
                                              <span className="pf-caption pf-mono font-semibold" style={{ color: "var(--pf-muted)" }}>
                                                {st.id}
                                              </span>
                                              <span className="text-[13px] font-semibold" style={{ color: "var(--pf-ink)" }}>
                                                {st.title}
                                              </span>
                                              {st.severity !== undefined && (
                                                <span
                                                  className={`pf-badge ${
                                                    st.severity === "CRITICAL"
                                                      ? "pf-badge--critical"
                                                      : st.severity === "HIGH"
                                                        ? "pf-badge--high"
                                                        : "pf-badge--medium"
                                                  }`}
                                                >
                                                  {st.severity}
                                                </span>
                                              )}
                                              <span className="pf-chip ml-auto" title="model estimate, not measured">
                                                Human: {st.humanEstimate}
                                              </span>
                                              <span className="pf-chip pf-chip--ok" title="model estimate, not measured">
                                                <Zap size={11} />
                                                Agent: {st.agentEstimate}
                                              </span>
                                            </div>
                                            <p className="pf-label mt-3 mb-1">Acceptance criteria</p>
                                            <ul className="m-0 list-disc pl-5 text-[13px]" style={{ color: "var(--pf-ink-2)" }}>
                                              {st.acceptance.map((a) => (
                                                <li key={a}>{a}</li>
                                              ))}
                                            </ul>
                                            <div className="mt-2.5 flex flex-wrap gap-x-5 gap-y-1">
                                              <p className="pf-caption">
                                                <span className="font-semibold" style={{ color: "var(--pf-ink-2)" }}>Targets:</span>{" "}
                                                <code className="pf-mono">{st.targets.join(", ")}</code>
                                              </p>
                                              <p className="pf-caption">
                                                <span className="font-semibold" style={{ color: "var(--pf-ink-2)" }}>Verification:</span>{" "}
                                                <code className="pf-mono">{st.verification.join("; ")}</code>
                                              </p>
                                            </div>
                                          </div>
                                        ))}
                                      </div>
                                    )}
                                  </div>
                                ))}
                              </div>
                            </div>
                          )}
                        </div>
                      ))}
                    </div>
                  )}
                </section>

                {/* Dual Gantt */}
                <section id="dual-schedule" className="mt-6">
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
                  <div className="mt-4 grid grid-cols-1 gap-4 xl:grid-cols-2">
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

                {approved && (
                  <div className="pf-notice pf-notice--ok mt-8">
                    <CircleCheck size={16} className="mt-0.5 shrink-0" style={{ color: "var(--pf-ok)" }} />
                    <div>
                      <p className="font-semibold" style={{ color: "var(--pf-ok)" }}>
                        Approved — safe to close
                      </p>
                      <p className="pf-caption mt-0.5">
                        {sync.members.length > 0
                          ? `Synced: ${sync.members.join(", ")}`
                          : "Artifacts committed to docs/plans/.plan/"}
                      </p>
                    </div>
                  </div>
                )}
              </>
            )}
          </div>
        </div>
      </div>

      {/* Primary CTA */}
      <footer className="pf-footer-bar">
        {approved ? (
          <p className="pf-body flex items-center gap-2" style={{ color: "var(--pf-ok)" }}>
            <CircleCheck size={15} />
            Approved — artifacts written to docs/plans/.plan/synthesized_spec.md and docs/plans/.plan/plan_manifest.json
          </p>
        ) : (
          <>
            <p className="pf-caption">
              {ownerApproves
                ? "Approving freezes the spec and injects it into the host agent's plan mode."
                : "Only the session owner can approve."}
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
                "Approve & Inject into Agent Plan Mode"
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

const TOC_ENTRIES: string[] = [
  "Executive summary",
  "Architecture",
  "Immutable constraints",
  "AST targets",
  "Documentation Sync",
  "Jira matrix",
  "Dual schedule",
];

function sectionOf(markdown: string, heading: string): string {
  const lines = markdown.split("\n");
  const start = lines.findIndex((l) => l.includes(heading));
  if (start === -1) return `_${heading}: not present_`;
  let end = lines.length;
  for (let i = start + 1; i < lines.length; i++) {
    if (/^#{1,2}\s/.test(lines[i]!)) {
      end = i;
      break;
    }
  }
  return lines.slice(start, end).join("\n");
}
