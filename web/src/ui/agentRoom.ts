import { MAIN_AVATARS, avatarSpec } from "../components/avatars/AvatarBadge";
import { parseArchitectDraft, parsePMDraft } from "../protocol/drafts";
import type { SyncState } from "../store/syncStore";

/**
 * The agent room: who is in this run, what each is doing, and how the temporary
 * agents relate to the workstreams they serve.
 *
 * The layout is computed, not hand-placed. Permanent agents sit on a fixed ring
 * at fixed angles, so the room looks the same at the start of every run and a
 * reader learns it once. Specialists sit just outside them, one per workstream.
 * A temporary agent borrows its parent workstream's angle and sits further out,
 * because "this agent serves that workstream" is the relationship worth seeing.
 *
 * Everything here is state the authority sent. Nothing estimates progress: a
 * stage is queued, working, synced or failed, and that is all this file knows.
 */

/** One face in the room. */
export interface RoomAgent {
  id: string;
  name: string;
  job: string;
  status: RoomStatus;
  /** The line the agent last said, already sanitized by the authority. */
  summary: string;
  kind: "permanent" | "specialist" | "temporary";
  /** For a specialist or a temporary agent: the workstream it serves. */
  parentId: string | undefined;
  /** Where to draw it, in a 0..1 unit square. */
  x: number;
  y: number;
  /** 0 for the permanent ring. */
  ring: number;
}

export type RoomStatus = "working" | "queued" | "synced" | "failed" | "idle" | "cancelled";

const CENTRE = 0.5;
const INNER_RADIUS = 0.3;
const SPECIALIST_RADIUS = 0.38;
const OUTER_RADIUS = 0.46;

/** Statuses that mean the agent is doing something right now. */
const WORKING_STATUS: Record<string, true> = {
  Thinking: true,
  Drafting: true,
  Reviewing: true,
  Critiquing: true,
  Revising: true,
};

/** The angle one item of `count` occupies, starting at the top. */
function angleAt(index: number, count: number): number {
  if (count <= 1) return -Math.PI / 2;
  return (2 * Math.PI * index) / count - Math.PI / 2;
}

/** A point on a ring of the unit square. */
function pointOn(angle: number, radius: number): { x: number; y: number } {
  return {
    x: Math.round((CENTRE + Math.cos(angle) * radius) * 1000) / 1000,
    y: Math.round((CENTRE + Math.sin(angle) * radius) * 1000) / 1000,
  };
}

/**
 * The status a face shows. An explicit thought wins; a draft the run already
 * holds means the stage finished; otherwise the run is either active (queued) or
 * not (idle). This mirrors the rule `ui/room.ts` already uses, so the plan view
 * and this room cannot disagree about one agent.
 */
export function roomStatus(sync: SyncState, id: string): RoomStatus {
  const thought = sync.thoughts[id];
  if (thought !== undefined) {
    const s = String(thought.status);
    if (s === "Failed") return "failed";
    if (s === "Cancelled") return "cancelled";
    if (s === "Synced") return "synced";
    if (WORKING_STATUS[s] === true) return "working";
  }
  if (sync.drafts.some((d) => d.avatarId === id)) return "synced";
  return sync.runId !== null ? "queued" : "idle";
}

/** A workstream as the room needs it: the id and the role the architect gave it. */
export interface RoomWorkstream {
  id: string;
  role: string;
}

/** The shape the authority sends for a temporary agent (`contracts.GapAgentDescriptor`). */
export interface RoomGapAgent {
  id: string;
  specialty: string;
  purpose: string;
  deliverable: string;
  parentId: string;
  status: string;
  reason?: string;
}

/** The workstreams the architect declared, from the architect draft. */
export function roomWorkstreams(sync: SyncState): RoomWorkstream[] {
  const draft = sync.drafts.find((d) => d.avatarId === "architect");
  const arch = parseArchitectDraft(draft?.data);
  if (arch === null) return [];
  return arch.workstreams.map((ws) => ({ id: ws.id, role: ws.role }));
}

/** Maps a gap descriptor's lifecycle to a room status. */
function gapRoomStatus(status: string): RoomStatus {
  switch (status) {
    case "admitted":
    case "queued":
      return "queued";
    case "running":
      return "working";
    case "completed":
    case "fused":
      return "synced";
    case "failed":
      return "failed";
    case "cancelled":
    case "retired":
    case "rejected":
      return "cancelled";
    default:
      return "idle";
  }
}

/** What a temporary agent says about itself, given where it is in its life. */
function gapSummary(g: RoomGapAgent): string {
  switch (g.status) {
    case "rejected":
      return g.reason === undefined || g.reason === "" ? "not admitted" : g.reason;
    case "fused":
      return "deliverable fused into the plan";
    case "retired":
      return g.reason === undefined || g.reason === "" ? "retired without a deliverable" : g.reason;
    case "failed":
      return g.reason === undefined || g.reason === "" ? "failed" : g.reason;
    default:
      return g.purpose;
  }
}

/**
 * Builds the room from the run state.
 *
 * Permanent agents are always present, before the run starts too: the room is a
 * map of the team, not a log of activity. A specialist appears once a workstream
 * exists. A temporary agent appears when the architect proposed one and stays
 * visible after it retires, its status saying how it ended.
 */
export function buildRoom(sync: SyncState, gaps: readonly RoomGapAgent[]): RoomAgent[] {
  const permanent: RoomAgent[] = MAIN_AVATARS.map((spec, i) => {
    const p = pointOn(angleAt(i, MAIN_AVATARS.length), INNER_RADIUS);
    return {
      id: spec.id,
      name: spec.name,
      job: spec.job,
      status: roomStatus(sync, spec.id),
      summary: sync.thoughts[spec.id]?.summary ?? "",
      kind: "permanent",
      parentId: undefined,
      x: p.x,
      y: p.y,
      ring: 0,
    };
  });

  const workstreams = roomWorkstreams(sync);
  const specialists: RoomAgent[] = workstreams.map((ws, i) => {
    const p = pointOn(angleAt(i, workstreams.length), SPECIALIST_RADIUS);
    const id = `specialist:${ws.id}`;
    const spec = avatarSpec(id);
    return {
      id,
      name: spec.name,
      job: ws.role === "" ? spec.job : `${spec.job} (${ws.role})`,
      status: roomStatus(sync, id),
      summary: sync.thoughts[id]?.summary ?? "",
      kind: "specialist",
      parentId: ws.id,
      x: p.x,
      y: p.y,
      ring: 0,
    };
  });

  const temporary: RoomAgent[] = gaps.map((g) => {
    const parentIndex = workstreams.findIndex((ws) => ws.id === g.parentId);
    const angle = parentIndex >= 0 ? angleOfParent(parentIndex, workstreams.length) : -Math.PI / 2;
    const p = pointOn(angle, OUTER_RADIUS);
    return {
      id: `gap:${g.id}`,
      name: g.specialty === "" ? g.id : g.specialty,
      job: g.deliverable === "" ? g.purpose : g.deliverable,
      status: gapRoomStatus(g.status),
      summary: gapSummary(g),
      kind: "temporary",
      parentId: g.parentId === "" ? undefined : g.parentId,
      x: p.x,
      y: p.y,
      ring: 1,
    };
  });

  return [...permanent, ...specialists, ...temporary];
}

/*
 * A temporary agent uses the same angle function as its parent, so the two sit
 * on one spoke. The wrapper keeps that intent named rather than repeating the
 * arithmetic at the call site.
 */
function angleOfParent(index: number, count: number): number {
  return angleAt(index, count);
}

/** The fused candidate at the centre of the room. */
export interface FusedCandidate {
  present: boolean;
  label: string;
  /** Findings still open against the candidate. */
  openFindings: number;
}

/**
 * The centre of the room. It says whether a candidate exists and how many
 * findings are still open against it. It never estimates how close the run is to
 * done, because the authority does not send that.
 */
export function fusedCandidate(sync: SyncState): FusedCandidate {
  const hasDraft = sync.drafts.some((d) => d.avatarId === "synthesizer");
  return {
    present: hasDraft,
    label: hasDraft ? "Fused candidate" : "Awaiting fusion",
    openFindings: sync.critiques.filter((c) => !c.resolved).length,
  };
}

/** The count the room header shows. */
export function workingInRoom(room: readonly RoomAgent[]): number {
  return room.filter((a) => a.status === "working").length;
}

/**
 * The epics a specialist's workstream was assigned, so a face can say what its
 * owner is actually building. Empty when the architect has not placed it yet.
 */
export function specialistStoryCount(sync: SyncState): number {
  const draft = sync.drafts.find((d) => d.avatarId === "pm");
  const pm = parsePMDraft(draft?.data);
  return pm === null ? 0 : pm.stories.length;
}
