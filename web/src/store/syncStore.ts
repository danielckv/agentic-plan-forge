/**
 * syncStore: native WebSocket state machine (connecting | open | resyncing |
 * offline | closed), OT push/pull through the DocClient (one change in
 * flight, local edits rebased over remote ones), recovery panel feed, and
 * phase-driven routing guards.
 *
 * Routing follows the authority phase: manual URL jumps to a page the phase
 * has not reached are blocked. The phase shown here is only ever
 * authority-assigned (`PAGE_TRANSITION`), never client-authored.
 */

import { useSyncExternalStore } from "react";
import { ChangeSet } from "@codemirror/state";
import type { ChangeDesc } from "@codemirror/state";

import { DocClient, type RemoteEffect } from "../collab/docClient";
import { draftContentText, parseCritique, type Finding } from "../protocol/drafts";

import { decodeFrame } from "../protocol/wire";
import type { WSEnvelope, WSEventType } from "../protocol/wire";
import {
  AUTH_OK,
  authFrame,
  bootstrap,
  bridgeWSURL,
  controlFrameType,
  WS_SUBPROTOCOL,
} from "../protocol/bridge";
import { validate } from "../protocol/schema";
import { insertAllowed, textSha256 } from "../collab/model";
import { toastStore } from "../ui/toasts";

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
  /** The draft as text: inline JSON from the Go side is serialized. */
  content?: string;
  /** The draft as sent (an object for pm.v1, architect.v1, synthesis.v1). */
  data?: unknown;
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

/** A critic finding carried by AVATAR_CRITIQUE (see protocol/drafts.ts). */
export type CritiqueIssue = Finding;

/**
 * One line of the deliberation timeline Page 2 shows: what each avatar said,
 * drafted, found or asked, in the order it happened.
 */
export type TimelineEntry =
  | { kind: "thought"; at: number; avatarId: string; cycle: number; status: string; summary: string }
  | { kind: "draft"; at: number; avatarId: string; schemaId: string; revision: string }
  | { kind: "critique"; at: number; cycle: number; open: number; blocking: number; total: number }
  | { kind: "decision"; at: number; decisionId: string; question: string }
  | { kind: "resolved"; at: number; decisionId: string; optionId: string; actor: string };

/** The timeline keeps the newest entries; older ones scroll off. */
export const MAX_TIMELINE = 200;

/**
 * GET /api/status, as the bridge answers it (`app.StatusView`). It carries
 * the Page 1 pills and the identity of the repository this session plans, so
 * every page can say which project the window belongs to.
 */
export interface StatusView {
  ast: "scanning" | "partial" | "failed" | "not-a-Git-repo" | "timeout" | "ok";
  astDetail: string;
  docHealth: number;
  ocr: "available" | "missing" | "unknown";
  repo: string;
  repoPath: string;
  /** The checked-out branch, once the scan has read it. */
  branch?: string;
  /**
   * The host the invoking skill passed with `--host` (`claude`, `omp`, `agy`,
   * or `auto`). Page 3 words its call to action and handoff panel for it.
   * Absent until the first `/api/status` answer, and on older bridges.
   */
  requestedHost?: string;
  /** The host that runs the avatars, once resolved. */
  mainHost?: string;
  /** Project context pill: gathering, ok, none or failed. */
  context?: "gathering" | "ok" | "none" | "failed";
  contextDetail?: string;
  /** Revision of the latest interpretation; 0 before the first one. */
  interpretation?: number;
  /** Impact map pill: mapping, quick, ok or failed. */
  impact?: "mapping" | "quick" | "ok" | "failed";
  impactDetail?: string;
  /**
   * Revision of the latest published impact map; 0 before the first one.
   *
   * Page 1 refetches on this as well as on `interpretation`: the
   * interpretation revision does not move when the model's map replaces the
   * keyword ranking, so keying on that alone would show the first paint
   * forever.
   */
  impactRevision?: number;
}

export const STATUS_UNKNOWN: StatusView = {
  ast: "scanning",
  astDetail: "0 files parsed, 0 symbols indexed",
  docHealth: 0,
  ocr: "unknown",
  repo: "",
  repoPath: "",
  impact: "mapping",
  impactDetail: "reading the code, docs and past decisions",
  impactRevision: 0,
};

export interface SyncState {
  conn: ConnState;
  phase: Phase;
  route: string;
  docVersion: number;
  docText: string;
  /** True while local edits wait for the authority's acknowledgement. */
  pendingEdits: boolean;
  /** When this window last edited the document (ms since epoch; 0 = never). */
  lastLocalEdit: number;
  recovery: RecoveryItem[];
  drafts: DraftSummary[];
  decisions: DecisionCard[];
  thoughts: Record<string, AvatarThought>;
  critiques: CritiqueIssue[];
  timeline: TimelineEntry[];
  runId: string | null;
  cycle: number;
  leaseHolder: string | null;
  banner: string | null;
  serverUrl: string;
  role: "owner" | "editor" | "viewer" | null;
  members: string[];
  errorMessage: string | null;
  /** False until the first `/api/status` answer lands; pages show skeletons. */
  statusLoaded: boolean;
  status: StatusView;
}

type Listener = () => void;

let state: SyncState = {
  conn: "connecting",
  phase: "INGESTION",
  route: "/ingest",
  docVersion: 0,
  docText: "",
  pendingEdits: false,
  lastLocalEdit: 0,
  recovery: [],
  drafts: [],
  decisions: [],
  thoughts: {},
  critiques: [],
  timeline: [],
  runId: null,
  cycle: 1,
  leaseHolder: null,
  banner: null,
  serverUrl: globalThis.PLAN_FORGE_WS_URL ?? "",
  role: null,
  members: [],
  errorMessage: null,
  statusLoaded: false,
  status: STATUS_UNKNOWN,
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

/**
 * The shared document's OT client. Local edits, acknowledgements and remote
 * updates all go through it, so the editor never loses a keystroke to a
 * concurrent upload or collaborator (see collab/docClient.ts).
 */
const remoteListeners = new Set<(effect: RemoteEffect) => void>();
const docClient = new DocClient({
  send: (payload) => send(commandEnvelope("TEXT_DELTA", payload)),
  clientId: () => `${state.role ?? "owner"}:${localSeq++}`,
  onRemote: (effect) => {
    for (const l of remoteListeners) l(effect);
  },
});

/**
 * The highest authority sequence number already on the timeline. A reconnect
 * replays recent frames; a frame at or below this mark is not added twice.
 * Window-only frames carry no sequence number and are always added.
 */
let timelineSeq = 0;

function appendTimeline(env: WSEnvelope, entry: TimelineEntry): TimelineEntry[] {
  const seq = Number(env.seq);
  if (Number.isFinite(seq) && seq > 0) {
    if (seq <= timelineSeq) return state.timeline;
    timelineSeq = seq;
  }
  const next = [...state.timeline, entry];
  return next.length > MAX_TIMELINE ? next.slice(next.length - MAX_TIMELINE) : next;
}

/** Mirror the client's text and version into the store. */
function syncDoc(extra: Partial<SyncState> = {}): void {
  setState({
    docText: docClient.localText,
    docVersion: docClient.confirmedVersion,
    pendingEdits: docClient.hasUnconfirmed,
    ...extra,
  });
}

/**
 * Subscribe the editor to text that changes under it (remote edits, uploads,
 * snapshots). Returns the unsubscribe function.
 */
export function subscribeRemoteEdits(fn: (effect: RemoteEffect) => void): () => void {
  remoteListeners.add(fn);
  return () => remoteListeners.delete(fn);
}

interface SnapshotPayload {
  phase?: Phase;
  doc_version?: string;
  role?: SyncState["role"];
  doc_text?: string;
  members?: string[];
}

interface TextUpdatesPayload {
  from_version?: string;
  updates?: Array<{ client_id?: string; changes: ChangeDesc }>;
}

function handleEnvelope(env: WSEnvelope): void {
  switch (env.event_type as WSEventType) {
    case "STATE_SNAPSHOT": {
      const p = env.payload as SnapshotPayload;
      if (p.doc_text !== undefined) docClient.reset(p.doc_text, Number(p.doc_version ?? "0"));
      syncDoc({
        phase: p.phase ?? state.phase,
        role: p.role ?? state.role,
        members: p.members ?? state.members,
        route: ROUTE_BY_PHASE[p.phase ?? state.phase],
        banner: null,
        errorMessage: null,
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
      docClient.ack(Number(p.version ?? env.revision));
      syncDoc();
      break;
    }
    case "TEXT_UPDATES": {
      const p = env.payload as TextUpdatesPayload;
      const from = Number(p.from_version ?? docClient.confirmedVersion);
      if (!docClient.receive(from, p.updates ?? [])) {
        // A gap: updates were missed. The snapshot that answers this resets.
        setState({ conn: "resyncing" });
        send(commandEnvelope("REPLAY_REQUEST", {}));
        break;
      }
      syncDoc();
      break;
    }
    case "TEXT_REJECT": {
      const p = env.payload as {
        reason?: string;
        resync?: boolean;
        updates?: Array<{ client_id?: string; changes: ChangeDesc }>;
      };
      if (p.resync === true) {
        // The authority follows with TEXT_SNAPSHOT; unconfirmed typing is kept.
        setState({ conn: "resyncing" });
      } else if (p.reason === "stale_base") {
        docClient.rejectStale(p.updates ?? []);
        syncDoc();
      } else {
        toastStore.push(
          "warning",
          p.reason === "locked"
            ? "The context is confirmed; the document no longer takes edits"
            : `Edit rejected (${p.reason ?? "unknown"})`,
        );
        docClient.discardUnconfirmed();
        syncDoc();
      }
      break;
    }
    case "TEXT_SNAPSHOT": {
      const p = env.payload as { version?: string; text?: string };
      docClient.reset(p.text ?? docClient.localText, Number(p.version ?? "0"));
      syncDoc({ conn: "open" });
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
        timeline: appendTimeline(env, {
          kind: "thought",
          at: Date.now(),
          avatarId,
          cycle: p.cycle ?? state.cycle,
          status: p.status ?? "Thinking",
          summary: p.public_summary ?? "",
        }),
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
      // The critic re-lists every finding each time, so the frame is the
      // current set: findings are replaced by ID, earlier ones kept.
      const { cycle, findings } = parseCritique(env.payload);
      const byId = new Map(state.critiques.map((c) => [c.issueId, c]));
      for (const f of findings) byId.set(f.issueId, f);
      const open = findings.filter((f) => !f.resolved);
      const p = env.payload as { run_id?: string };
      setState({
        critiques: [...byId.values()],
        timeline: appendTimeline(env, {
          kind: "critique",
          at: Date.now(),
          cycle: cycle || state.cycle,
          open: open.length,
          blocking: open.filter((f) => f.severity !== "MEDIUM").length,
          total: findings.length,
        }),
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
        content?: unknown;
      };
      const text = draftContentText(p.content);
      const avatarId = p.avatar_id ?? "?";
      const revision = p.draft_revision ?? env.revision;
      setState({
        drafts: [
          ...state.drafts.filter((d) => d.avatarId !== avatarId),
          {
            avatarId,
            ...(p.schema_id !== undefined ? { schemaId: p.schema_id } : {}),
            revision,
            ...(p.content_hash !== undefined ? { contentHash: p.content_hash } : {}),
            ...(text !== undefined ? { content: text, data: p.content } : {}),
          },
        ],
        // The interpretation draft belongs to Page 1, not the deliberation.
        ...(avatarId === "ingestion"
          ? {}
          : {
              timeline: appendTimeline(env, {
                kind: "draft",
                at: Date.now(),
                avatarId,
                schemaId: p.schema_id ?? "",
                revision,
              }),
            }),
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
        timeline: appendTimeline(env, {
          kind: "decision",
          at: Date.now(),
          decisionId: p.decision_id ?? "?",
          question: p.question ?? "",
        }),
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
        timeline: appendTimeline(env, {
          kind: "resolved",
          at: Date.now(),
          decisionId: p.decision_id ?? "?",
          optionId: p.option_id ?? "",
          actor: p.actor_id ?? "",
        }),
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
      toastStore.push("info", "Decision already resolved by another member");
      setState({
        decisions: state.decisions.map((d) =>
          d.decisionId === p.decision_id
            ? { ...d, stale: true, ...(p.option_id !== undefined ? { resolvedOptionId: p.option_id } : {}) }
            : d,
        ),
      });
      break;
    }
    case "ERROR": {
      const p = env.payload as { code?: string; message?: string };
      const text = `${p.code ?? "ERROR"}: ${p.message ?? ""}`.trim();
      // A version mismatch is a standing condition of the window (banner);
      // every other error is an event about one request (toast).
      if (p.code === "UNSUPPORTED_VERSION") setState({ errorMessage: text });
      else toastStore.push("error", text);
      break;
    }
    case "PRESENCE": {
      const p = env.payload as { members?: string[] };
      setState({ members: p.members ?? state.members });
      break;
    }
    case "RUN_ASSIGNED": {
      const p = env.payload as { run_id?: string };
      // A new run (after a baseline edit) starts its own story on Page 2.
      const fresh = p.run_id !== undefined && state.runId !== null && p.run_id !== state.runId;
      setState({
        runId: p.run_id ?? state.runId,
        ...(fresh ? { timeline: [], critiques: [], thoughts: {}, decisions: [] } : {}),
      });
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

/**
 * Null a socket's handlers before closing it so a late close/error from the
 * old connection cannot mutate state or schedule a reconnect for a socket that
 * has been replaced or torn down on purpose.
 */
function detachSocket(reason: string): void {
  const old = ws;
  ws = null;
  if (old === null) return;
  old.onopen = null;
  old.onmessage = null;
  old.onerror = null;
  old.onclose = null;
  try {
    old.close(1000, reason);
  } catch {
    /* already closed */
  }
}

/**
 * Open the session socket. The caller has already redeemed the launch nonce;
 * `token` is null only under the preview harness, which needs no auth frame.
 */
function openSocket(token: string | null): void {
  detachSocket("replaced by a new connection");
  try {
    ws = new WebSocket(state.serverUrl, WS_SUBPROTOCOL);
  } catch {
    setState({ conn: "offline", banner: "relay unreachable — edits are kept locally" });
    scheduleReconnect();
    return;
  }
  ws.onopen = () => {
    reconnectAttempt = 0;
    setState({ conn: "open" });
    // The bridge reads nothing before the auth frame, so both of these go out
    // on the raw socket: send() would route them through the offline banner
    // while the send buffer drains.
    const socket = ws;
    if (socket === null) return;
    if (token !== null) socket.send(authFrame(token));
    socket.send(
      JSON.stringify(
        commandEnvelope("HELLO", {
          proto: 1,
          last_seq: "0",
          doc_version: String(state.docVersion),
          capabilities: { can_execute: state.role === "owner" },
        }),
      ),
    );
  };
  ws.onmessage = (ev: MessageEvent) => {
    if (typeof ev.data !== "string") return;
    if (ev.data.length > 4 * 1024 * 1024) return; // decode max 4 MiB per frame
    const control = controlFrameType(ev.data);
    if (control !== null) {
      if (control !== AUTH_OK) {
        setState({ errorMessage: `unexpected bridge control frame: ${control}` });
        ws?.close(4408, "protocol violation");
      }
      return;
    }
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
        toastStore.push("error", `Invalid envelope: ${v.errors?.[0]?.message ?? ""}`);
        return;
      }
    }
    handleEnvelope(frame.envelope);
  };
  ws.onclose = (ev: CloseEvent) => {
    // 4403 is the bridge refusing the auth frame. The token will not become
    // valid by retrying, so this is a standing auth state, not a reconnect.
    if (ev.code === 4403) {
      setState({
        conn: "offline",
        banner: null,
        errorMessage: "authentication failed — reopen the window from the plan-forge CLI",
      });
      return;
    }
    setState({ conn: "closed", banner: "closed — reconnecting" });
    scheduleReconnect();
  };
  ws.onerror = () => {
    setState({ conn: "offline", banner: "relay unreachable — edits are kept locally" });
  };
}

/**
 * Connect to the session authority. The loopback handshake runs first and is
 * memoized, so a reconnect reuses the token rather than replaying the spent
 * launch nonce. A refused handshake is terminal: it is reported and not
 * retried, because no retry can succeed.
 */
export function connect(url?: string): void {
  setState({
    conn: "connecting",
    banner: null,
    serverUrl: url ?? (state.serverUrl === "" ? bridgeWSURL() : state.serverUrl),
  });
  void bootstrap().then(
    (token) => {
      openSocket(token);
    },
    (err: unknown) => {
      // Terminal, not transient: "closed" means a reconnect is scheduled, and
      // no reconnect can redeem a spent nonce.
      setState({
        conn: "offline",
        errorMessage: `session handshake failed: ${err instanceof Error ? err.message : String(err)}`,
      });
    },
  );
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
  detachSocket("client closing");
  setState({ conn: "closed" });
}

/** Merge one `/api/status` body; absent fields keep their last known value. */
export function applyStatus(patch: Partial<StatusView>): void {
  setState({ status: { ...state.status, ...patch }, statusLoaded: true });
}

/**
 * Local edit from the editor, expressed against the current document text.
 * Returns false (and changes nothing) when an insert carries a carriage
 * return or a lone surrogate, which the authority refuses.
 */
export function editDocument(changes: ChangeSet): boolean {
  let allowed = true;
  changes.iterChanges((_fromA, _toA, _fromB, _toB, inserted) => {
    if (!insertAllowed(inserted.toString())) allowed = false;
  });
  if (!allowed) {
    toastStore.push("error", "Rejected: CR or lone surrogate in inserted text");
    return false;
  }
  docClient.edit(changes);
  syncDoc({ lastLocalEdit: Date.now() });
  return true;
}

/** Replace `from..to` of the document with `insert`. */
export function submitEdit(insert: string, from: number, to: number): boolean {
  return editDocument(ChangeSet.of({ from, to, insert }, docClient.localText.length));
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
  if (!state.pendingEdits) return;
  toastStore.push("info", "Unconfirmed edits exported to the recovery panel");
  setState({
    recovery: [...state.recovery, { at: Date.now(), room: state.serverUrl, text: state.docText }],
  });
}

/**
 * Manual navigation guard: allow only routes the authority's phase has
 * reached. The limit is the phase, not the page on screen, so stepping back to
 * review earlier work never locks the pages ahead of it again.
 */
export function navigate(to: string): void {
  const target = ROUTE_ORDER[to];
  const reached = ROUTE_ORDER[ROUTE_BY_PHASE[state.phase]];
  if (target === undefined || reached === undefined) return;
  if (target > reached) return; // blocked: phase has not reached this page
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
