import { useState } from "react";

import { MarkdownView } from "../components/MarkdownView";
import { MermaidSvg } from "../components/MermaidSvg";
import { useSync, approve, navigate } from "../store/syncStore";

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

export function Page3SynthesisConfluence(): React.ReactElement {
  const sync = useSync();
  const [spec] = useState<SpecView | null>(null);
  const [pageState, setPageState] = useState<Page3State>("loading");
  const [openEpic, setOpenEpic] = useState<string | null>(null);
  const [openStory, setOpenStory] = useState<string | null>(null);
  const [failureReason] = useState<string | null>(null);

  const approved = pageState === "approved";
  const ownerApproves = sync.role === "owner";

  const ctaDisabled =
    !ownerApproves || pageState === "approving" || pageState === "approved";

  const onApprove = (): void => {
    if (ctaDisabled) return;
    setPageState("approving");
    const runId = spec?.markdown.match(/run[_-]id: ([\w-]+)/)?.[1] ?? "";
    approve(runId, 1, "");
  };

  return (
    <div className="flex h-full min-h-0">
      {/* Sticky TOC */}
      <nav
        className="hidden w-56 shrink-0 overflow-auto border-r p-3 lg:block"
        style={{ borderColor: "var(--pf-border)" }}
      >
        <p className="mb-2 text-xs font-semibold uppercase" style={{ color: "var(--pf-muted)" }}>
          Contents
        </p>
        <ul className="list-none p-0 text-sm">
          {TOC_ENTRIES.map((t) => (
            <li key={t} className="py-0.5">
              <a href={`#${t.toLowerCase().replace(/[^a-z0-9]+/g, "-")}`}>{t}</a>
            </li>
          ))}
        </ul>
      </nav>

      <div className="min-h-0 flex-1 overflow-auto p-6">
        {pageState === "loading" && spec === null && (
          <p className="text-sm" style={{ color: "var(--pf-muted)" }}>
            Compiling master specification…
          </p>
        )}
        {pageState === "stale-approval" && (
          <div className="mb-4 rounded border p-3" style={{ borderColor: "var(--pf-warn)" }}>
            <p className="text-sm font-medium">Stale approval</p>
            <p className="text-sm" style={{ color: "var(--pf-muted)" }}>
              The synthesis changed since this view was opened — review the latest draft.
            </p>
            <button
              type="button"
              className="mt-2 rounded px-3 py-1 text-xs"
              style={{ background: "var(--pf-accent)", color: "var(--pf-accent-ink)" }}
              onClick={() => navigate("/deliberate")}
            >
              Review latest
            </button>
          </div>
        )}
        {pageState === "approve-failed" && (
          <div className="mb-4 rounded border p-3" style={{ borderColor: "var(--pf-danger)" }}>
            <p className="text-sm font-medium">Approve failed</p>
            <p className="text-sm" style={{ color: "var(--pf-muted)" }}>
              {failureReason ?? "artifact commit failed"}
            </p>
            <button
              type="button"
              className="mt-2 rounded px-3 py-1 text-xs"
              style={{ background: "var(--pf-accent)", color: "var(--pf-accent-ink)" }}
              onClick={() => {
                setPageState("approving");
                const runId = spec?.markdown.match(/run[_-]id: ([\w-]+)/)?.[1] ?? "";
                approve(runId, 1, "");
              }}
            >
              Retry
            </button>
          </div>
        )}

        {spec !== null && (
          <>
            <h1 className="text-xl font-bold">Master specification</h1>
            <section className="mt-4">
              <h2 className="text-base font-semibold">Executive summary</h2>
              <MarkdownView className="mt-2 text-sm" markdown={sectionOf(spec.markdown, "Executive summary")} />
            </section>
            <section className="mt-6">
              <h2 className="text-base font-semibold">Architecture</h2>
              <div className="mt-2">
                <MermaidSvg source={spec.architectureDiagram} title="Architecture (graph TD)" />
              </div>
              <h3 className="mt-4 text-sm font-semibold">Sequence</h3>
              <MermaidSvg source={spec.sequenceDiagram} title="Sequence" />
            </section>
            <section className="mt-6">
              <h2 className="text-base font-semibold">Immutable constraints</h2>
              <MarkdownView className="mt-2 text-sm" markdown={sectionOf(spec.markdown, "Immutable constraints")} />
            </section>
            <section className="mt-6">
              <h2 className="text-base font-semibold">AST file/symbol targets</h2>
              <MarkdownView className="mt-2 text-sm" markdown={sectionOf(spec.markdown, "AST targets")} />
            </section>
            <section className="mt-6">
              <h2 className="text-base font-semibold">Documentation Sync Tasks</h2>
              <MarkdownView className="mt-2 text-sm" markdown={sectionOf(spec.markdown, "Documentation Sync")} />
            </section>

            {/* Jira matrix */}
            <section className="mt-6">
              <h2 className="text-base font-semibold">Jira matrix</h2>
              <div className="mt-2 flex flex-col gap-1">
                {spec.epics.map((epic) => (
                  <div key={epic.id} className="rounded border" style={{ borderColor: "var(--pf-border)" }}>
                    <button
                      type="button"
                      className="flex w-full items-center justify-between p-2 text-left text-sm font-medium"
                      onClick={() => setOpenEpic(openEpic === epic.id ? null : epic.id)}
                    >
                      <span>
                        {epic.id} — {epic.title}
                      </span>
                      <span className="text-xs" style={{ color: "var(--pf-muted)" }}>
                        {openEpic === epic.id ? "collapse" : "expand"}
                      </span>
                    </button>
                    {openEpic === epic.id && (
                      <div className="border-t p-2" style={{ borderColor: "var(--pf-border)" }}>
                        <p className="mb-2 text-xs" style={{ color: "var(--pf-muted)" }}>
                          {epic.goal}
                        </p>
                        {epic.stories.map((story) => (
                          <div key={story.id} className="mb-2">
                            <button
                              type="button"
                              className="text-left text-sm"
                              onClick={() => setOpenStory(openStory === story.id ? null : story.id)}
                            >
                              {story.id} — {story.need}
                            </button>
                            {openStory === story.id && (
                              <ul className="mt-1 list-none p-0 text-xs">
                                {story.subtasks.map((st) => (
                                  <li key={st.id} className="mb-2 rounded border p-2" style={{ borderColor: "var(--pf-border)" }}>
                                    <p className="font-medium">
                                      {st.id} — {st.title}
                                    </p>
                                    <p style={{ color: "var(--pf-muted)" }}>Acceptance:</p>
                                    <ul className="list-disc pl-4">
                                      {st.acceptance.map((a) => (
                                        <li key={a}>{a}</li>
                                      ))}
                                    </ul>
                                    <p style={{ color: "var(--pf-muted)" }}>Targets: {st.targets.join(", ")}</p>
                                    <p style={{ color: "var(--pf-muted)" }}>Verification: {st.verification.join("; ")}</p>
                                    <p className="mt-1" style={{ color: "var(--pf-muted)" }}>
                                      Human: {st.humanEstimate} | Coding Agent: {st.agentEstimate}
                                    </p>
                                  </li>
                                ))}
                              </ul>
                            )}
                          </div>
                        ))}
                      </div>
                    )}
                  </div>
                ))}
              </div>
            </section>

            {/* Dual Gantt */}
            <section className="mt-6">
              <h2 className="text-base font-semibold">Dual schedule</h2>
              <div className="mt-2 flex flex-wrap gap-2 text-xs">
                <span className="rounded-full px-2 py-0.5" style={{ background: "var(--pf-accent)", color: "#fff" }}>
                  Human: {spec.ganttChips.human}
                </span>
                <span className="rounded-full px-2 py-0.5" style={{ background: "var(--pf-ok)", color: "#fff" }}>
                  Coding Agent: {spec.ganttChips.agent}
                </span>
                <span className="rounded-full px-2 py-0.5" style={{ background: "var(--pf-muted)", color: "#fff" }}>
                  model estimate, not measured
                </span>
              </div>
              <div className="mt-3">
                <MermaidSvg source={spec.humanGantt} title="Human — 9h/day calendar" />
              </div>
              <div className="mt-3">
                <MermaidSvg source={spec.agentGantt} title="Coding Agent — elapsed minutes" />
              </div>
            </section>
          </>
        )}

        {approved && (
          <div className="mt-6 rounded border p-3" style={{ borderColor: "var(--pf-ok)" }}>
            <p className="text-sm font-semibold" style={{ color: "var(--pf-ok)" }}>
              Approved — safe to close
            </p>
            <p className="text-xs" style={{ color: "var(--pf-muted)" }}>
              {sync.members.length > 0
                ? `Synced: ${sync.members.join(", ")}`
                : "Artifacts committed to .plan/"}
            </p>
          </div>
        )}
      </div>

      {/* Primary CTA */}
      <footer
        className="flex items-center justify-end gap-2 border-t px-4 py-3"
        style={{ borderColor: "var(--pf-border)" }}
      >
        {approved ? (
          <span className="text-sm" style={{ color: "var(--pf-ok)" }}>
            Approved — artifacts written to .plan/synthesized_spec.md and .plan/plan_manifest.json
          </span>
        ) : (
          <button
            type="button"
            className="rounded px-4 py-2 text-sm font-semibold disabled:opacity-40"
            style={{ background: "var(--pf-accent)", color: "var(--pf-accent-ink)" }}
            disabled={ctaDisabled}
            onClick={onApprove}
          >
            {ownerApproves ? "Approve & Inject into Agent Plan Mode" : "owner approves"}
          </button>
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

