import { useEffect, useMemo, useRef, useState } from "react";
import {
  ArrowRight,
  CircleCheck,
  Cpu,
  Crown,
  FileText,
  Gauge,
  LoaderCircle,
  MessagesSquare,
  Network,
  Radio,
  ShieldAlert,
  TriangleAlert,
  WifiOff,
  X,
} from "lucide-react";

import { AvatarBadge, MAIN_AVATARS, avatarSpec } from "../components/avatars/AvatarBadge";
import { UsagePanel } from "../components/UsagePanel";
import { PlanGraphView } from "./PlanGraphView";
import {
  parseArchitectDraft,
  parsePMDraft,
  type ArchitectDraft,
  type PMDraft,
} from "../protocol/drafts";
import { cancelSession, editBaseline, resolveDecision, retryStage, synthesize, useSync } from "../store/syncStore";
import {
  avatarStatus,
  runFailure,
  workingAvatars,
  KNOWN_STATUS,
  NARRATOR_ID,
  WORKING_STATUS,
  type AvatarStatus,
  type RoomInput,
} from "../ui/room";
import type { AvatarThought, CritiqueIssue, DecisionCard, SyncState, TimelineEntry } from "../store/syncStore";
import {
  activeStage,
  blockedStages,
  completedStages,
  elapsedMS,
  formatDuration,
  outcomeHeadline,
  remainingMS,
  STAGE_LABEL,
} from "../ui/runStages";
import { SkeletonCard, SkeletonLines } from "../ui/Skeleton";

/**
 * Page 2 — the avatar room.
 *
 * The four avatars sit across the top with what each is doing now. Below,
 * the discussion: every status, draft, critique and decision in the order it
 * happened, filterable by avatar, with open decisions pinned first. Beside
 * it, the plan as it takes shape: the Product Manager's epics as files, the
 * architect's diagram and workstreams, and the critic's findings.
 *
 * Footer: Cancel, and Synthesize Master Spec, enabled once every avatar has
 * finished the cycle, no CRITICAL or HIGH finding is open, and no decision
 * waits.
 */

const CYCLE_STEPS: Array<{ n: number; label: string }> = [
  { n: 1, label: "Divergence" },
  { n: 2, label: "Red-team critique" },
  { n: 3, label: "Convergence" },
];

const STATUS_CHIP: Record<AvatarStatus, string> = {
  Idle: "",
  Queued: "",
  Thinking: "pf-chip--accent pf-chip--live",
  Drafting: "pf-chip--accent pf-chip--live",
  Critiquing: "pf-chip--danger pf-chip--live",
  Revising: "pf-chip--warn pf-chip--live",
  Synced: "pf-chip--ok",
  Failed: "pf-chip--danger",
  Cancelled: "",
  Stale: "pf-chip--warn",
};

/** The reader's name for a stage id. Specialist lanes keep their workstream. */
function humanStage(id: string): string {
  if (id === "pm") return "Product brief";
  if (id === "architect") return "Architecture";
  if (id === "critic") return "Review";
  if (id === "synthesizer") return "Specification";
  if (id.startsWith("specialist:")) return `Specialist ${id.slice("specialist:".length)}`;
  return id;
}

// The graph absorbs what the Epics and Architecture tabs used to show: both
// are the same plan at different depths, and splitting them across tabs hid
// the shape of the work.
type PlanTab = "graph" | "findings" | "usage";

/**
 * What one avatar is running on and how far it has got: the host and model
 * serving it, the part of the stage in flight, the evidence that part can
 * see, and the progress of the call. Each piece is optional — a stage that
 * has not resolved a host yet simply shows fewer of them — so the row is
 * built from whatever arrived.
 */
function AvatarMeta({ thought }: { thought: AvatarThought | undefined }): React.ReactElement | null {
  if (thought === undefined) return null;
  const where = [thought.model, thought.host].filter((v) => v !== undefined && v !== "").join(" on ");
  const progress =
    thought.elapsedMs !== undefined && thought.elapsedMs > 0
      ? `${Math.round(thought.elapsedMs / 1000)}s${
          thought.outputBytes !== undefined && thought.outputBytes > 0
            ? ` · ${(thought.outputBytes / 1024).toFixed(1)} KB`
            : ""
        }`
      : "";
  if (where === "" && (thought.step ?? "") === "" && progress === "" && (thought.contextNote ?? "") === "") {
    return null;
  }
  return (
    <span className="pf-roster-meta">
      {where !== "" && (
        <span className="pf-tag" title="The host and model serving this avatar">
          <Cpu size={10} aria-hidden="true" />
          {where}
        </span>
      )}
      {thought.step !== undefined && thought.step !== "" && (
        <span className="pf-tag" title="The part of the stage in progress">
          {thought.step}
        </span>
      )}
      {progress !== "" && (
        <span className="pf-tag pf-tag--live" title="Time and output of the call in flight">
          {progress}
        </span>
      )}
      {thought.contextNote !== undefined && thought.contextNote !== "" && (
        <span className="pf-roster-context" title="The evidence this call can see">
          {thought.contextNote}
        </span>
      )}
    </span>
  );
}

export function Page2AvatarCanvas(): React.ReactElement {
  const sync = useSync();
  const cycle = sync.cycle;
  const runId = sync.runId;
  const [focus, setFocus] = useState<string | null>(null);
  const [planTab, setPlanTab] = useState<PlanTab>("graph");
  const [selectedNode, setSelectedNode] = useState<string | null>(null);
  // A retry that has been sent but whose stage has not started yet. Without it
  // a second click cancels a call that is already paid for; the executor
  // refuses that, and the button says so rather than looking dead.
  const [retrySent, setRetrySent] = useState<string | null>(null);

  const runActive = runId !== null;
  // Specialists work under the architect: any specialist at work keeps the
  // cycle open even though it has no card of its own.
  const room: RoomInput = {
    thoughts: sync.thoughts,
    draftAvatars: sync.drafts.map((d) => d.avatarId),
    runActive,
  };
  const statusFor = (avatarId: string): AvatarStatus => avatarStatus(room, avatarId);
  const workingIds = workingAvatars(
    room,
    MAIN_AVATARS.map((a) => a.id),
  );
  const working = workingIds.map((id) => avatarSpec(id).name);
  const workingCount = working.length;

  // The narrator is not one of the four: it holds no draft and must never
  // count as an avatar still working, or a finished cycle could not
  // synthesize while it spoke.
  const narration = sync.thoughts[NARRATOR_ID];

  const openCritiques = sync.critiques.filter((c) => !c.resolved && c.severity !== "MEDIUM");
  const openDecisions = sync.decisions.filter((d) => d.resolvedOptionId === undefined && !d.stale);
  // The orchestrator pauses a run that reaches the cycle cap with blocking
  // findings, and says so with a Stale critic status.
  const paused = sync.thoughts.critic?.status === "Stale" && openCritiques.length > 0 && workingCount === 0;
  // A failed stage ends the run: the avatars that never ran were cancelled, so
  // the room offers the retry instead of waiting for them.
  const failure = runFailure(room);

  const synthesizeEnabled =
    sync.role === "owner" &&
    workingCount === 0 &&
    openCritiques.length === 0 &&
    openDecisions.length === 0 &&
    !paused &&
    failure === null;
  const linkDown = sync.conn !== "open";

  useEffect(() => {
    // The board moved: a new failure, or none at all, makes the button live.
    setRetrySent(null);
  }, [failure?.avatarId, failure?.summary]);

  const askRetry = (avatarId: string): void => {
    setRetrySent(avatarId);
    retryStage(runId ?? "", avatarId);
  };

  const pm = useMemo(() => parsePMDraft(sync.drafts.find((d) => d.avatarId === "pm")?.data), [sync.drafts]);
  const arch = useMemo(() => parseArchitectDraft(sync.drafts.find((d) => d.avatarId === "architect")?.data), [sync.drafts]);

  const active = activeStage(sync.status.stages);
  const completed = completedStages(sync.status.stages);
  const blocked = blockedStages(sync.status.stages);

  // The stage state is authoritative; only the clock reading is local. A
  // once-a-second tick keeps elapsed and remaining honest without inventing
  // progress.
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (active === undefined) return;
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, [active?.id, active?.state]);

  const elapsed = elapsedMS(active, now);
  const remaining = remainingMS(active, now);

  return (
    <div className="pf-page">
      <div className="pf-subbar">
        <div className="pf-stepper" role="list" aria-label="Deliberation cycle">
          {CYCLE_STEPS.map((s, i) => (
            <span key={s.n} className="flex items-center" role="listitem">
              {i > 0 && <span className="pf-step-connector" aria-hidden="true" />}
              <span
                className="pf-step"
                data-state={s.n < cycle ? "done" : s.n === cycle ? "active" : "todo"}
                aria-current={s.n === cycle ? "step" : undefined}
              >
                <span className="pf-step-index">{s.n}</span>
                {s.label}
              </span>
            </span>
          ))}
        </div>
        <div className="pf-subbar-right">
          <span className="pf-chip pf-chip--accent">
            <Crown size={12} />
            Lease: {sync.leaseHolder ?? (sync.role === "owner" ? "you" : "owner")}
          </span>
          {linkDown ? (
            <span className="pf-chip pf-chip--warn pf-chip--live" title="Progress resumes when the session authority is back">
              <WifiOff size={12} />
              Reconnecting — last known state
            </span>
          ) : workingCount > 0 ? (
            <span className="pf-chip pf-chip--accent pf-chip--live">
              <LoaderCircle size={12} className="pf-spin" />
              Cycle {cycle} running — {workingCount} working
            </span>
          ) : runActive ? (
            <span className="pf-chip pf-chip--ok">
              <span className="pf-dot" />
              Cycle {cycle} settled
            </span>
          ) : null}
        </div>
      </div>

      <div className="pf-page-body pf-room-page">
        <div className="pf-room">
          <header className="pf-page-header">
            <h1 className="pf-title">{outcomeHeadline(sync.status.stages, sync.status.recovery ?? "")}</h1>
            <p className="pf-caption">
              {active !== undefined ? (
                <>
                  {STAGE_LABEL[active.state]} · {formatDuration(elapsed)} elapsed
                  {remaining !== null ? ` · gives up in ${formatDuration(remaining)}` : ""}
                </>
              ) : (
                <>Cycle {cycle} of 3. The avatars plan from the frozen requirements, the code and the project's notes.</>
              )}
            </p>
            {blocked.length > 0 && (
              <p className="pf-notice pf-notice--warn" role="status">
                <TriangleAlert size={14} aria-hidden="true" />
                <span>
                  {blocked.length === 1 ? "One stage needs you" : `${String(blocked.length)} stages need you`} before the
                  plan can finish:{" "}
                  {blocked.map((b) => `${humanStage(b.id)} (${b.recovery ?? "review"})`).join(", ")}.
                </span>
              </p>
            )}
            {sync.status.checkpointNotice !== undefined && sync.status.checkpointNotice !== "" && (
              <p className="pf-notice pf-notice--warn" role="status">
                <TriangleAlert size={14} aria-hidden="true" />
                <span>{sync.status.checkpointNotice}</span>
              </p>
            )}
          </header>

          {completed.length > 0 && (
            <section className="pf-outcome-done" aria-label="Finished work">
              <h2 className="pf-section-title">Finished</h2>
              <ul className="pf-outcome-list">
                {completed.map((s) => (
                  <li key={s.id}>
                    <CircleCheck size={13} aria-hidden="true" />
                    <span>{humanStage(s.id)}</span>
                    {s.detail !== undefined && s.detail !== "" && <span className="pf-caption"> — {s.detail}</span>}
                  </li>
                ))}
              </ul>
            </section>
          )}

          {narration !== undefined && narration.summary !== "" && (
            <p className="pf-narration" role="status" aria-live="polite">
              <Radio size={13} aria-hidden="true" className={workingCount > 0 ? "pf-spin-slow" : undefined} />
              <span className="pf-narration-text">{narration.summary}</span>
            </p>
          )}

          <details className="pf-roster-details">
            <summary className="pf-caption">
              Avatar detail — who is on which host, and what each one is doing
            </summary>
            <div className="pf-roster" role="group" aria-label="Avatars">
              {MAIN_AVATARS.map((a) => {
                const status = statusFor(a.id);
                const thought = sync.thoughts[a.id];
                const selected = focus === a.id;
                return (
                  <button
                    key={a.id}
                    type="button"
                    className="pf-roster-card"
                    data-status={status}
                    aria-pressed={selected}
                    title={selected ? "Show everyone" : `Show only ${a.name}`}
                    onClick={() => setFocus(selected ? null : a.id)}
                    style={{ ["--pf-role" as string]: a.color }}
                  >
                    <AvatarBadge id={a.id} size={44} speaking={WORKING_STATUS.has(status) && status !== "Queued"} />
                    <span className="pf-roster-main">
                      <span className="pf-roster-name">{a.name}</span>
                      <span className="pf-roster-job">{a.job}</span>
                      <span key={status} className={`pf-chip pf-chip--swap ${STATUS_CHIP[status]}`}>
                        <span className="pf-dot" />
                        {status}
                      </span>
                      <AvatarMeta thought={thought} />
                      {thought !== undefined && thought.summary !== "" ? (
                        <span className="pf-roster-say">{thought.summary}</span>
                      ) : WORKING_STATUS.has(status) ? (
                        <SkeletonLines lines={2} />
                      ) : null}
                    </span>
                  </button>
                );
              })}
            </div>
          </details>

          <div className="pf-room-grid">
            <section className="pf-room-feed" aria-labelledby="p2-feed">
              <div className="pf-room-head">
                <MessagesSquare size={15} aria-hidden="true" />
                <h2 id="p2-feed" className="pf-section-title">
                  Discussion
                </h2>
                {focus !== null && (
                  <button type="button" className="pf-chip pf-chip--accent pf-room-filter" onClick={() => setFocus(null)}>
                    {avatarSpec(focus).name}
                    <X size={11} aria-label="Show everyone" />
                  </button>
                )}
              </div>
              {openDecisions.length > 0 && (
                <div className="pf-room-decisions">
                  {openDecisions.map((d) => (
                    <DecisionCardView key={d.decisionId} card={d} disabled={sync.role === "viewer"} onResolve={resolveDecision} />
                  ))}
                </div>
              )}
              <Timeline sync={sync} focus={focus} pm={pm} arch={arch} onShow={setPlanTab} linkDown={linkDown} />
            </section>

            <aside className="pf-room-plan" aria-label="Plan taking shape">
              <div className="pf-rail-tabs" role="tablist" aria-label="Plan views">
                <PlanTabButton
                  id="graph"
                  current={planTab}
                  onSelect={setPlanTab}
                  icon={Network}
                  label="Plan"
                  count={(pm?.epics.length ?? 0) + (pm?.stories.length ?? 0) + (arch?.workstreams.length ?? 0)}
                />
                <PlanTabButton
                  id="findings"
                  current={planTab}
                  onSelect={setPlanTab}
                  icon={ShieldAlert}
                  label="Findings"
                  count={sync.critiques.filter((c) => !c.resolved).length}
                />
                <PlanTabButton id="usage" current={planTab} onSelect={setPlanTab} icon={Gauge} label="Usage" />
              </div>
              <div className="pf-rail-body pf-scroll-thin" role="tabpanel">
                {planTab === "graph" && (
                  <PlanGraphView
                    pm={pm}
                    architect={arch}
                    critiques={sync.critiques}
                    selectedId={selectedNode}
                    onSelect={setSelectedNode}
                  />
                )}
                {planTab === "findings" && <FindingsPanel findings={sync.critiques} />}
                {planTab === "usage" && <UsagePanel live={workingCount > 0} />}
              </div>
            </aside>
          </div>

          {sync.decisions.some((d) => d.resolvedOptionId !== undefined || d.stale) && (
            <section className="pf-room-resolved" aria-label="Resolved decisions">
              {sync.decisions
                .filter((d) => d.resolvedOptionId !== undefined || d.stale)
                .map((d) => (
                  <DecisionCardView key={d.decisionId} card={d} disabled onResolve={resolveDecision} />
                ))}
            </section>
          )}

          {failure !== null && (
            <section className="pf-notice pf-notice--danger flex-col items-stretch">
              <div className="flex items-center gap-2">
                <TriangleAlert size={15} style={{ color: "var(--pf-danger)" }} />
                <span className="font-semibold" style={{ color: "var(--pf-danger)" }}>
                  The run stopped: the {avatarSpec(failure.avatarId).name} stage failed
                </span>
              </div>
              <p className="pf-caption">{failure.summary}</p>
              <p className="pf-caption">
                The drafts that finished are kept. Retrying re-runs only the stage that failed.
              </p>
              <div className="mt-2 flex gap-2">
                <button
                  type="button"
                  className="pf-btn pf-btn-secondary pf-btn-sm"
                  onClick={() => askRetry(failure.avatarId)}
                  disabled={sync.role !== "owner" || retrySent !== null}
                >
                  {retrySent !== null ? "Retrying…" : "Retry stage"}
                </button>
                <button type="button" className="pf-btn pf-btn-secondary pf-btn-sm" onClick={editBaseline}>
                  Revise baseline
                </button>
              </div>
            </section>
          )}

          {paused && (
            <section className="pf-notice pf-notice--warn flex-col items-stretch">
              <div className="flex items-center gap-2">
                <TriangleAlert size={15} style={{ color: "var(--pf-warn)" }} />
                <span className="font-semibold" style={{ color: "var(--pf-warn)" }}>
                  Paused at the cycle limit: {openCritiques.length} blocking {openCritiques.length === 1 ? "finding" : "findings"} still open
                </span>
              </div>
              <p className="pf-caption">Retry the architect with the findings, or edit the requirements and confirm again.</p>
              <div className="mt-2 flex gap-2">
                <button
                  type="button"
                  className="pf-btn pf-btn-secondary pf-btn-sm"
                  onClick={() => askRetry("architect")}
                  disabled={retrySent !== null}
                >
                  {retrySent !== null ? "Retrying…" : "Retry stage"}
                </button>
                <button type="button" className="pf-btn pf-btn-secondary pf-btn-sm" onClick={editBaseline}>
                  Revise baseline
                </button>
              </div>
            </section>
          )}
        </div>
      </div>

      <footer className="pf-footer-bar">
        <button type="button" className="pf-btn pf-btn-danger" onClick={cancelSession} disabled={sync.role !== "owner"}>
          Cancel session
        </button>
        <p className="pf-caption pf-footer-note">
          {workingCount > 0 ? (
            <>
              <LoaderCircle size={12} className="pf-spin shrink-0" />
              {working.length === 1
                ? `${working[0]} is still working — synthesis waits for the cycle to finish.`
                : `${workingCount} avatars are still working — synthesis waits for the cycle to finish.`}
            </>
          ) : failure !== null ? (
            "The run stopped on a failed stage — retry it or revise the baseline."
          ) : openDecisions.length > 0 ? (
            "Resolve the open decision to enable synthesis."
          ) : openCritiques.length > 0 ? (
            "Blocking findings must be resolved before synthesis."
          ) : runActive ? (
            "Every avatar has finished — ready to synthesize."
          ) : (
            "Waiting for the run to start."
          )}
        </p>
        <button
          type="button"
          className="pf-btn pf-btn-primary pf-btn-lg ml-auto"
          disabled={!synthesizeEnabled}
          title={workingCount > 0 ? "Available when every avatar finishes this cycle" : "Synthesize the master specification"}
          onClick={() => runId !== null && synthesize(runId)}
        >
          Synthesize Master Spec
          <ArrowRight size={15} />
        </button>
      </footer>
    </div>
  );
}

function clock(at: number): string {
  return new Date(at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

/** Which avatar a timeline entry belongs to, for the filter. */
function entryAvatar(e: TimelineEntry): string | null {
  if (e.kind === "thought" || e.kind === "draft") return e.avatarId;
  if (e.kind === "critique") return "critic";
  return null;
}

function Timeline(props: {
  sync: SyncState;
  focus: string | null;
  pm: PMDraft | null;
  arch: ArchitectDraft | null;
  onShow: (tab: PlanTab) => void;
  linkDown: boolean;
}): React.ReactElement {
  const { sync, focus, pm, arch, onShow, linkDown } = props;
  const listRef = useRef<HTMLOListElement>(null);
  const entries = sync.timeline.filter((e) => {
    if (focus === null) return true;
    const who = entryAvatar(e);
    return who === focus || (focus === "architect" && who?.startsWith("specialist:") === true);
  });

  // Follow the conversation while the reader is at the bottom; leave them be
  // when they scrolled up to read.
  useEffect(() => {
    const el = listRef.current;
    if (el === null) return;
    if (el.scrollHeight - el.scrollTop - el.clientHeight < 120) el.scrollTop = el.scrollHeight;
  }, [entries.length]);

  if (entries.length === 0) {
    return (
      <div className="pf-empty" aria-live="polite">
        <span className="pf-empty-icon" data-busy={!linkDown}>
          {linkDown ? <WifiOff size={20} /> : <MessagesSquare size={20} />}
        </span>
        <p className="pf-body font-medium" style={{ color: "var(--pf-ink)" }}>
          {linkDown ? "Waiting for the session authority" : focus !== null ? "Nothing from this avatar yet" : "The avatars are getting started"}
        </p>
        <p className="pf-caption max-w-md">
          {linkDown
            ? "This page keeps its last state and picks up as soon as the connection is back."
            : "Every status, draft, finding and decision appears here as it happens."}
        </p>
        {!linkDown && (
          <div className="pf-empty-preview" aria-hidden="true">
            <SkeletonCard lines={2} />
            <SkeletonCard lines={2} />
          </div>
        )}
      </div>
    );
  }

  return (
    <ol ref={listRef} className="pf-timeline pf-scroll-thin" aria-live="polite">
      {entries.map((e, i) => (
        <li key={`${e.kind}-${e.at}-${i}`} className="pf-line" data-kind={e.kind}>
          <TimelineLine entry={e} sync={sync} pm={pm} arch={arch} onShow={onShow} />
        </li>
      ))}
    </ol>
  );
}

function TimelineLine(props: {
  entry: TimelineEntry;
  sync: SyncState;
  pm: PMDraft | null;
  arch: ArchitectDraft | null;
  onShow: (tab: PlanTab) => void;
}): React.ReactElement {
  const { entry: e, sync, pm, arch, onShow } = props;
  const who = entryAvatar(e);
  const time = <time className="pf-line-time">{clock(e.at)}</time>;

  if (e.kind === "decision" || e.kind === "resolved") {
    const card = sync.decisions.find((d) => d.decisionId === e.decisionId);
    if (e.kind === "decision") {
      return (
        <div className="pf-line-system">
          <TriangleAlert size={14} aria-hidden="true" />
          <span>
            <strong>Decision needed:</strong> {e.question}
          </span>
          {time}
        </div>
      );
    }
    const option = card?.options.find((o) => o.optionId === e.optionId);
    return (
      <div className="pf-line-system pf-line-system--ok">
        <Crown size={14} aria-hidden="true" />
        <span>
          <strong>{e.actor === "" ? "The owner" : e.actor}</strong> chose “{option?.label ?? e.optionId}”.
        </span>
        {time}
      </div>
    );
  }

  const spec = avatarSpec(who ?? "");
  let body: React.ReactNode;
  if (e.kind === "thought") {
    const status = KNOWN_STATUS.has(e.status) ? (e.status as AvatarStatus) : "Thinking";
    body = (
      <>
        <span className={`pf-chip ${STATUS_CHIP[status]}`}>{e.status}</span>
        {e.summary !== "" && <p className="pf-line-text">{e.summary}</p>}
      </>
    );
  } else if (e.kind === "draft") {
    const what =
      e.avatarId === "pm" && pm !== null
        ? `drafted ${pm.epics.length} ${pm.epics.length === 1 ? "epic" : "epics"} and ${pm.stories.length} ${pm.stories.length === 1 ? "story" : "stories"}`
        : e.avatarId === "architect" && arch !== null
          ? `drafted the architecture: ${arch.workstreams.length} ${arch.workstreams.length === 1 ? "workstream" : "workstreams"}`
          : e.schemaId === "synthesis.v1"
            ? "compiled the master specification"
            : `published draft ${e.revision}`;
    const tab: PlanTab | null = e.avatarId === "pm" || e.avatarId === "architect" ? "graph" : null;
    body = (
      <p className="pf-line-text">
        <FileText size={13} aria-hidden="true" /> {what}.
        {tab !== null && (
          <button type="button" className="pf-line-link" onClick={() => onShow(tab)}>
            View
          </button>
        )}
      </p>
    );
  } else {
    body = (
      <p className="pf-line-text">
        Found {e.total} {e.total === 1 ? "issue" : "issues"}: {e.blocking} blocking, {e.open} open.
        <button type="button" className="pf-line-link" onClick={() => onShow("findings")}>
          View
        </button>
      </p>
    );
  }

  return (
    <div className="pf-line-msg">
      <AvatarBadge id={spec.id} size={30} />
      <div className="pf-line-main">
        <div className="pf-line-head">
          <span className="pf-line-name" style={{ color: spec.color }}>
            {spec.name}
          </span>
          {"cycle" in e && <span className="pf-line-cycle">cycle {e.cycle}</span>}
          {time}
        </div>
        {body}
      </div>
    </div>
  );
}

function FindingsPanel({ findings }: { findings: CritiqueIssue[] }): React.ReactElement {
  if (findings.length === 0) {
    return (
      <div className="pf-rail-empty">
        <p className="pf-body">No findings yet.</p>
        <p className="pf-caption">The critic attacks the drafts once the architect and specialists publish them.</p>
      </div>
    );
  }
  const order = { CRITICAL: 0, HIGH: 1, MEDIUM: 2 } as const;
  const sorted = [...findings].sort((a, b) => Number(a.resolved) - Number(b.resolved) || order[a.severity] - order[b.severity]);
  return (
    <ul className="pf-findings">
      {sorted.map((f) => (
        <li key={f.issueId} className="pf-finding" data-resolved={f.resolved}>
          <div className="pf-finding-head">
            <span className={`pf-badge ${f.severity === "CRITICAL" ? "pf-badge--critical" : f.severity === "HIGH" ? "pf-badge--high" : "pf-badge--medium"}`}>
              {f.severity}
            </span>
            <span className="pf-caption pf-mono">{f.issueId}</span>
            <span className={`pf-chip ${f.resolved ? "pf-chip--ok" : ""}`}>
              <span className="pf-dot" />
              {f.resolved ? "Resolved" : "Open"}
            </span>
          </div>
          <p className="pf-body">{f.summary}</p>
          {f.requiredChange !== "" && <p className="pf-caption">Needs: {f.requiredChange}</p>}
          {f.targets.length > 0 && (
            <span className="pf-arch-ws-mods">
              {f.targets.map((t) => (
                <code key={t} className="pf-tag">
                  {t}
                </code>
              ))}
            </span>
          )}
        </li>
      ))}
    </ul>
  );
}

function PlanTabButton(props: {
  id: PlanTab;
  current: PlanTab;
  onSelect: (t: PlanTab) => void;
  icon: React.ComponentType<{ size?: number }>;
  label: string;
  count?: number;
}): React.ReactElement {
  const { id, current, onSelect, icon: Icon, label, count } = props;
  return (
    <button type="button" role="tab" className="pf-rail-tab" aria-selected={id === current} onClick={() => onSelect(id)}>
      <Icon size={14} />
      {label}
      {count !== undefined && count > 0 && <span className="pf-rail-count">{count}</span>}
    </button>
  );
}

function DecisionCardView(props: {
  card: DecisionCard;
  disabled: boolean;
  onResolve: (decisionId: string, optionId: string) => void;
}): React.ReactElement {
  const { card, disabled, onResolve } = props;
  const resolved = card.resolvedOptionId !== undefined;
  return (
    <div className="pf-card pf-card-pad pf-decision" data-state={card.stale ? "stale" : resolved ? "resolved" : "open"}>
      <div className="flex items-start justify-between gap-3">
        <p className="pf-section-title">{card.question}</p>
        {card.stale && (
          <span className="pf-chip pf-chip--swap pf-chip--warn">
            <span className="pf-dot" />
            Stale — resolved by {card.resolvedBy ?? "another member"}
          </span>
        )}
        {resolved && !card.stale && (
          <span className="pf-chip pf-chip--swap pf-chip--ok">
            <span className="pf-dot" />
            Resolved by {card.resolvedBy ?? "member"}
          </span>
        )}
        {!resolved && !card.stale && (
          <span className="pf-chip pf-chip--swap pf-chip--accent pf-chip--live">
            <span className="pf-dot" />
            Awaiting decision
          </span>
        )}
      </div>
      <div className="mt-3 grid grid-cols-1 gap-2 md:grid-cols-2">
        {card.options.map((o) => {
          const recommended = card.recommendedOptionId === o.optionId;
          const chosen = card.resolvedOptionId === o.optionId;
          return (
            <button
              key={o.optionId}
              type="button"
              disabled={disabled || resolved}
              className="pf-option"
              data-chosen={chosen}
              data-recommended={recommended}
              data-dim={resolved && !chosen}
              onClick={() => onResolve(card.decisionId, o.optionId)}
            >
              <span className="flex items-center gap-2 text-[13px] font-semibold" style={{ color: "var(--pf-ink)" }}>
                {o.label}
                {recommended && <span className="pf-badge pf-badge--recommended">Recommended</span>}
              </span>
              <span className="pf-caption">{o.tradeoff}</span>
            </button>
          );
        })}
      </div>
    </div>
  );
}
