/**
 * syncStore: native WebSocket state machine (connecting | open | resyncing |
 * offline | closed), OT push/pull through `@codemirror/collab`, durable
 * outbox, recovery panel feed, and phase-driven routing guards.
 *
 * Routing follows the authority phase: manual URL jumps to a page the phase
 * has not reached are blocked. The phase shown here is only ever
 * authority-assigned (`PAGE_TRANSITION`), never client-authored.
 */

import { useSyncExternalStore } from "react";
import { ChangeSet, Text } from "@codemirror/state";
import type { ChangeDesc } from "@codemirror/state";

import { decodeFrame } from "../protocol/wire";
import type { WSEnvelope, WSEventType } from "../protocol/wire";
import { validate } from "../protocol/schema";
import { insertAllowed, textSha256 } from "../collab/model";

export type ConnState = "connecting" | "open" | "resyncing" | "offline" | "closed";

/** Session phases, authority-driven only. */
export type Phase =
  | "INGESTION"
  | "DELIBERATION"
  | "SYNTHESIS"
  | "APPROVING"
  | "APPROVING_UNKNOWN"
  | "APPROVED"
  | "CANCELLED";

/** Route per phase — the router follows authority phase, blocking manual jumps. */
export const ROUTE_BY_PHASE: Record<Phase, string> = {
  INGESTION: "/ingest",
  DELIBERATION: "/deliberate",
  SYNTHESIS: "/synthesis",
  APPROVING: "/synthesis",
  APPROVING_UNKNOWN: "/synthesis",
  APPROVED: "/synthesis",
  CANCELLED: "/ingest",
};

/** Page order for the navigation guard. */
export const ROUTE_ORDER: Record<string, number> = {
  "/ingest": 0,
  "/deliberate": 1,
  "/synthesis": 2,
};

export interface OutboxEntry {
  id: string;
  baseVersion: number;
  payload: string;
  attempts: number;
}

export interface RecoveryItem {
  at: number;
  room: string;
  text: string;
}

export interface DraftSummary {
  avatarId: string;
  schemaId?: string;
  revision: string;
  contentHash?: string;
  content?: string;
}

export interface DecisionOption {
  optionId: string;
  label: string;
  tradeoff: string;
}

export interface DecisionCard {
  decisionId: string;
  question: string;
  options: DecisionOption[];
  recommendedOptionId?: string;
  resolvedOptionId?: string;
  resolvedBy?: string;
  stale: boolean;
}

/** Public avatar activity (status + public summary; never raw reasoning). */
export interface AvatarThought {
  avatarId: string;
  cycle: number;
  status: string;
  summary: string;
}

/** A single critic finding carried by AVATAR_CRITIQUE. */
export interface CritiqueIssue {
  issueId: string;
  avatarId: string;
  severity: "CRITICAL" | "HIGH" | "MEDIUM";
  summary: string;
  resolved: boolean;
}

export interface SyncState {
  conn: ConnState;
  phase: Phase;
  route: string;
  docVersion: number;
  docText: string;
  outbox: OutboxEntry[];
  recovery: RecoveryItem[];
  drafts: DraftSummary[];
  decisions: DecisionCard[];
  thoughts: Record<string, AvatarThought>;
  critiques: CritiqueIssue[];
  runId: string | null;
  cycle: number;
  leaseHolder: string | null;
  banner: string | null;
  serverUrl: string;
  role: "owner" | "editor" | "viewer" | null;
  members: string[];
  errorMessage: string | null;
}

type Listener = () => void;

let state: SyncState = {
  conn: "connecting",
  phase: "INGESTION",
  route: "/ingest",
  docVersion: 0,
  docText: "",
  outbox: [],
  recovery: [],
  drafts: [],
  decisions: [],
  thoughts: {},
  critiques: [],
  runId: null,
  cycle: 1,
  leaseHolder: null,
  banner: null,
  serverUrl: globalThis.PLAN_FORGE_WS_URL ?? "ws://127.0.0.1:0/",
  role: null,
  members: [],
  errorMessage: null,
};

let ws: WebSocket | null = null;
let reconnectAttempt = 0;
/**
 * Handle returned by the timer functions in every runtime we support.
 * Under Node typings setTimeout returns Timeout; under DOM typings it is a number.
 */
type TimerHandle = NodeJS.Timeout | number;

let reconnectTimer: TimerHandle | undefined;
let checkTimer: TimerHandle | undefined;
let localSeq = 0;
const listeners = new Set<Listener>();

function emit(): void {
  for (const l of listeners) l();
}

/**
 * Every update replaces `state` with a fresh object. `useSyncExternalStore`
 * compares snapshots by reference, so in-place mutation would never re-render.
 */
function setState(patch: Partial<SyncState>): void {
  state = { ...state, ...patch };
  emit();
}

/** Exponential backoff 250ms -> 5s with jitter, per the reconnect contract. */
function backoffDelay(): number {
  const base = Math.min(5000, 250 * 2 ** reconnectAttempt);
  reconnectAttempt += 1;
  return base * (0.75 + Math.random() * 0.5);
}

function send(obj: unknown): void {
  if (ws !== null && ws.readyState === WebSocket.OPEN && ws.bufferedAmount === 0) {
    ws.send(JSON.stringify(obj));
    return;
  }
  // Offline: edits stay locally (banner: "edits are kept locally"); retried on open.
  setState({ banner: "offline — edits are kept locally" });
}

interface CommandEnvelope {
  version: number;
  session_id: string;
  sender_id: string;
  event_id: string;
  event_type: WSEventType;
  seq: string;
  revision: string;
  epoch: string;
  timestamp: string;
  payload: Record<string, unknown>;
}

/** Envelope skeleton for client commands; counters are authority-assigned. */
function commandEnvelope(eventType: WSEventType, payload: Record<string, unknown>): CommandEnvelope {
  return {
    version: 1,
    session_id: state.serverUrl,
    sender_id: state.role ?? "anonymous",
    event_id: crypto.randomUUID(),
    event_type: eventType,
    seq: "0",
    revision: "0",
    epoch: "0",
    timestamp: new Date().toISOString(),
    payload,
  };
}

/** Push the head outbox entry as one uncomposed `TEXT_DELTA` batch. */
function pushHeadEdit(): void {
  const next = state.outbox[0];
  if (next === undefined) return;
  send(
    commandEnvelope("TEXT_DELTA", {
      document_id: "prd",
      base_version: String(next.baseVersion),
      updates: [
        { client_id: `${state.role}:${localSeq}`, changes: JSON.parse(next.payload) },
      ],
    }),
  );
  localSeq += 1;
}

/**
 * Apply a serialized ChangeSet to a text string through CodeMirror `Text`
 * (UTF-16 offsets, matching the central authority's document model).
 */
export function applyChangeSetToText(text: string, json: ChangeDesc): string {
  return ChangeSet.fromJSON(json).apply(Text.of(text.split("\n"))).toString();
}

interface SnapshotPayload {
  phase?: Phase;
  doc_version?: string;
  role?: SyncState["role"];
  doc_text?: string;
  members?: string[];
}

interface TextUpdatesPayload {
  updates?: Array<{ changes: ChangeDesc }>;
}

function handleEnvelope(env: WSEnvelope): void {
  switch (env.event_type as WSEventType) {
    case "STATE_SNAPSHOT": {
      const p = env.payload as SnapshotPayload;
      setState({
        phase: p.phase ?? state.phase,
        docVersion: Number(p.doc_version ?? "0"),
        docText: p.doc_text ?? state.docText,
        role: p.role ?? state.role,
        members: p.members ?? state.members,
        route: ROUTE_BY_PHASE[p.phase ?? state.phase],
        banner: null,
      });
      break;
    }
    case "PAGE_TRANSITION": {
      const p = env.payload as { to?: Phase };
      const to = p.to ?? state.phase;
      setState({ phase: to, route: ROUTE_BY_PHASE[to] });
      break;
    }
    case "TEXT_ACK": {
      const p = env.payload as { version?: string };
      setState({ docVersion: Number(p.version ?? env.revision), outbox: [] });
      break;
    }
    case "TEXT_UPDATES": {
      const p = env.payload as TextUpdatesPayload;
      let text = state.docText;
      for (const u of p.updates ?? []) {
        text = applyChangeSetToText(text, u.changes);
      }
      setState({ docText: text, docVersion: state.docVersion + (p.updates?.length ?? 0) });
      break;
    }
    case "TEXT_REJECT": {
      const p = env.payload as { reason?: string; resync?: boolean };
      if (p.resync === true) {
        setState({ conn: "resyncing" });
        send(commandEnvelope("REPLAY_REQUEST", {}));
      } else {
        setState({
          banner: `edit rejected (${p.reason ?? "unknown"}) — retrying`,
          outbox: state.outbox.map((e) => ({ ...e, attempts: e.attempts + 1 })),
        });
      }
      break;
    }
    case "TEXT_SNAPSHOT": {
      const p = env.payload as { version?: string; text?: string };
      setState({
        docText: p.text ?? state.docText,
        docVersion: Number(p.version ?? "0"),
        conn: "open",
        outbox: [],
      });
      break;
    }
    case "AVATAR_THOUGHT": {
      const p = env.payload as {
        run_id?: string;
        avatar_id?: string;
        cycle?: number;
        status?: string;
        public_summary?: string;
      };
      const avatarId = p.avatar_id ?? "?";
      setState({
        thoughts: {
          ...state.thoughts,
          [avatarId]: {
            avatarId,
            cycle: p.cycle ?? state.cycle,
            status: p.status ?? "Thinking",
            summary: p.public_summary ?? "",
          },
        },
        cycle: p.cycle ?? state.cycle,
        runId: state.runId ?? p.run_id ?? null,
      });
      break;
    }
    case "AVATAR_CRITIQUE": {
      const p = env.payload as {
        run_id?: string;
        avatar_id?: string;
        issue_id?: string;
        severity?: string;
        summary?: string;
        resolved?: boolean;
      };
      const severity =
        p.severity === "CRITICAL" || p.severity === "HIGH" ? p.severity : "MEDIUM";
      const issue: CritiqueIssue = {
        issueId: p.issue_id ?? crypto.randomUUID(),
        avatarId: p.avatar_id ?? "critic",
        severity,
        summary: p.summary ?? "",
        resolved: p.resolved === true,
      };
      setState({
        critiques: [...state.critiques.filter((c) => c.issueId !== issue.issueId), issue],
        runId: state.runId ?? p.run_id ?? null,
      });
      break;
    }
    case "AVATAR_DRAFT": {
      const p = env.payload as {
        run_id?: string;
        avatar_id?: string;
        schema_id?: string;
        draft_revision?: string;
        content_hash?: string;
        content?: string;
      };
      setState({
        drafts: [
          ...state.drafts.filter((d) => d.avatarId !== p.avatar_id),
          {
            avatarId: p.avatar_id ?? "?",
            ...(p.schema_id !== undefined ? { schemaId: p.schema_id } : {}),
            revision: p.draft_revision ?? env.revision,
            ...(p.content_hash !== undefined ? { contentHash: p.content_hash } : {}),
            ...(p.content !== undefined ? { content: p.content } : {}),
          },
        ],
        runId: state.runId ?? p.run_id ?? null,
      });
      break;
    }
    case "DECISION_PROMPT": {
      const p = env.payload as {
        decision_id?: string;
        question?: string;
        options?: Array<{ id: string; label: string; tradeoff: string }>;
        recommended_option_id?: string;
      };
      setState({
        decisions: [
          ...state.decisions.filter((d) => d.decisionId !== p.decision_id),
          {
            decisionId: p.decision_id ?? "?",
            question: p.question ?? "",
            options: (p.options ?? []).map((o) => ({
              optionId: o.id,
              label: o.label,
              tradeoff: o.tradeoff,
            })),
            ...(p.recommended_option_id !== undefined
              ? { recommendedOptionId: p.recommended_option_id }
              : {}),
            stale: false,
          },
        ],
      });
      break;
    }
    case "DECISION_RESOLVE": {
      const p = env.payload as { decision_id?: string; option_id?: string; actor_id?: string };
      setState({
        decisions: state.decisions.map((d) =>
          d.decisionId === p.decision_id
            ? {
                ...d,
                ...(p.option_id !== undefined ? { resolvedOptionId: p.option_id } : {}),
                ...(p.actor_id !== undefined ? { resolvedBy: p.actor_id } : {}),
                stale: false,
              }
            : d,
        ),
      });
      break;
    }
    case "ALREADY_RESOLVED": {
      const p = env.payload as { decision_id?: string; option_id?: string };
      setState({
        decisions: state.decisions.map((d) =>
          d.decisionId === p.decision_id
            ? { ...d, stale: true, ...(p.option_id !== undefined ? { resolvedOptionId: p.option_id } : {}) }
            : d,
        ),
        banner: "decision already resolved by another member",
      });
      break;
    }
    case "ERROR": {
      const p = env.payload as { code?: string; message?: string };
      setState({ errorMessage: `${p.code ?? "ERROR"}: ${p.message ?? ""}`.trim() });
      break;
    }
    case "PRESENCE": {
      const p = env.payload as { members?: string[] };
      setState({ members: p.members ?? state.members });
      break;
    }
    case "RUN_ASSIGNED": {
      const p = env.payload as { run_id?: string };
      setState({ runId: p.run_id ?? state.runId });
      break;
    }
    case "LEASE_GRANTED": {
      const p = env.payload as { holder_id?: string; actor_id?: string };
      setState({ leaseHolder: p.holder_id ?? p.actor_id ?? state.leaseHolder });
      break;
    }
    case "LEASE_LOST": {
      setState({ leaseHolder: null });
      break;
    }
    case "PLAN_APPROVED": {
      setState({ phase: "APPROVED", route: ROUTE_BY_PHASE.APPROVED });
      break;
    }
    default:
      // Control events surfaced elsewhere (leases, runs, artifacts).
      break;
  }
}

export function connect(url?: string): void {
  setState({
    conn: "connecting",
    banner: null,
    ...(url !== undefined ? { serverUrl: url } : {}),
  });
  try {
    ws = new WebSocket(state.serverUrl, "plan-forge.v1");
  } catch {
    setState({ conn: "offline", banner: "relay unreachable — edits are kept locally" });
    scheduleReconnect();
    return;
  }
  ws.onopen = () => {
    reconnectAttempt = 0;
    setState({ conn: "open" });
    send(
      commandEnvelope("HELLO", {
        proto: 1,
        last_seq: "0",
        doc_version: String(state.docVersion),
        capabilities: { can_execute: state.role === "owner" },
      }),
    );
    pushHeadEdit();
  };
  ws.onmessage = (ev: MessageEvent) => {
    if (typeof ev.data !== "string") return;
    if (ev.data.length > 4 * 1024 * 1024) return; // decode max 4 MiB per frame
    const frame = decodeFrame(ev.data);
    if (!frame.ok || frame.envelope === undefined) {
      setState({ errorMessage: `protocol violation: ${frame.error}` });
      // Browsers reject reserved code 1008 on close(); 4408 carries the
      // same policy-violation meaning inside the app-defined 4000 range.
      ws?.close(4408, "protocol violation");
      return;
    }
    if (frame.envelope.event_type === "AVATAR_DRAFT") {
      const v = validate("envelope.v1", frame.envelope);
      if (!v.ok) {
        setState({ errorMessage: `invalid envelope: ${v.errors?.[0]?.message ?? ""}` });
        return;
      }
    }
    handleEnvelope(frame.envelope);
  };
  ws.onclose = () => {
    setState({ conn: "closed", banner: "closed — reconnecting" });
    scheduleReconnect();
  };
  ws.onerror = () => {
    setState({ conn: "offline", banner: "relay unreachable — edits are kept locally" });
  };
}

function scheduleReconnect(): void {
  if (reconnectTimer !== undefined) return;
  reconnectTimer = setTimeout(() => {
    reconnectTimer = undefined;
    connect();
  }, backoffDelay());
}

export function disconnect(): void {
  clearInterval(checkTimer);
  clearTimeout(reconnectTimer);
  reconnectTimer = undefined;
  checkTimer = undefined;
  ws?.close(1000, "client closing");
  ws = null;
  setState({ conn: "closed" });
}

/** Local edit (composer/editor): filter, queue, push. */
export function submitEdit(insert: string, from: number, to: number): void {
  if (!insertAllowed(insert)) {
    setState({ errorMessage: "rejected: CR or lone surrogate in inserted text" });
    return;
  }
  const cs = ChangeSet.of({ from, to, insert }, state.docText.length);
  setState({
    docText: state.docText.slice(0, from) + insert + state.docText.slice(to),
    outbox: [
      ...state.outbox,
      {
        id: crypto.randomUUID(),
        baseVersion: state.docVersion,
        payload: JSON.stringify(cs.toJSON()),
        attempts: 0,
      },
    ],
  });
  pushHeadEdit();
}

/** Idle `TEXT_CHECK` heartbeat (sha256 of the current document). */
export function startTextCheck(): void {
  if (checkTimer !== undefined) return;
  checkTimer = setInterval(() => {
    if (state.conn !== "open") return;
    void textSha256(state.docText).then((sha) => {
      send(
        commandEnvelope("TEXT_CHECK", {
          document_id: "prd",
          version: String(state.docVersion),
          sha256: sha,
        }),
      );
    });
  }, 10_000);
}

/** Owner confirm: `FREEZE_REQUEST` (Page 1 footer). */
export function freezeRequest(): void {
  send(commandEnvelope("FREEZE_REQUEST", {}));
}

/** Decision resolution (owner and editors). */
export function resolveDecision(decisionId: string, optionId: string): void {
  send(commandEnvelope("DECISION_RESOLVE", { decision_id: decisionId, option_id: optionId }));
}

/** Owner commands. */
export function cancelSession(): void {
  send(commandEnvelope("CANCEL", {}));
}

export function retryStage(runId: string, stageId: string): void {
  send(commandEnvelope("RETRY_STAGE", { run_id: runId, stage_id: stageId }));
}

export function editBaseline(): void {
  send(commandEnvelope("EDIT_BASELINE", {}));
}

export function synthesize(runId: string): void {
  send(commandEnvelope("SYNTHESIZE_REQUEST", { run_id: runId }));
}

export function approve(runId: string, synthesisRev: number, contentSha256: string): void {
  send(
    commandEnvelope("APPROVE", {
      run_id: runId,
      synthesis_rev: String(synthesisRev),
      content_sha256: contentSha256,
    }),
  );
}

/** Export unconfirmed edits to the recovery panel feed (never silently dropped). */
export function exportRecovery(): void {
  if (state.outbox.length === 0) return;
  setState({
    recovery: [...state.recovery, { at: Date.now(), room: state.serverUrl, text: state.docText }],
    outbox: [],
    banner: "unconfirmed edits exported to the recovery panel",
  });
}

/** Manual navigation guard: allow only routes the phase has reached. */
export function navigate(to: string): void {
  const target = ROUTE_ORDER[to];
  const current = ROUTE_ORDER[state.route];
  if (target === undefined || current === undefined) return;
  if (target > current) return; // blocked: phase has not reached this page
  setState({ route: to });
}

function subscribe(l: Listener): () => void {
  listeners.add(l);
  return () => listeners.delete(l);
}

function getSnapshot(): SyncState {
  return state;
}

/** React binding: `useSyncExternalStore` over the mutable singleton. */
export function useSync(): SyncState {
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
}
