/**
 * What the avatar room shows for one run: a status per avatar, who is still
 * working, and whether the run stopped.
 *
 * The rules live here, not in the page, because two of them were wrong on the
 * board and could not be tested from a React component:
 *
 * 1. An avatar with no status yet reads as "Queued" while a run is active. The
 *    Lead Synthesizer never takes part in a cycle — it waits for the user to
 *    start synthesis — so a queued synthesizer must not count as working, or
 *    the cycle never reads as finished.
 * 2. A failed stage ends the run. The avatars that never ran are cancelled by
 *    the authority, and the room must offer the retry instead of waiting.
 */

export type AvatarStatus =
  | "Idle"
  | "Queued"
  | "Thinking"
  | "Drafting"
  | "Critiquing"
  | "Revising"
  | "Synced"
  | "Failed"
  | "Cancelled"
  | "Stale";

/** Statuses the authority sends. "Queued" and "Idle" are the room's own. */
export const KNOWN_STATUS: ReadonlySet<string> = new Set([
  "Idle",
  "Thinking",
  "Drafting",
  "Critiquing",
  "Revising",
  "Synced",
  "Failed",
  "Cancelled",
  "Stale",
]);

/** Statuses that mean the avatar is working right now. */
export const WORKING_STATUS: ReadonlySet<string> = new Set([
  "Queued",
  "Thinking",
  "Drafting",
  "Critiquing",
  "Revising",
]);

/** The avatar that locks the design once the cycle is finished. */
export const SYNTHESIZER_ID = "synthesizer";

/**
 * The avatar that narrates the run. It holds no draft, publishes only the
 * status below, and is never one of the avatarIds the room counts, so it can
 * never keep a finished cycle from synthesizing.
 */
export const NARRATOR_ID = "narrator";

/** The only status the narrator publishes. It is not a working status. */
export const NARRATING_STATUS = "Narrating";

/**
 * A heartbeat is the progress line a running call emits every few seconds
 * ("Running for 40s, 7.2 KB written, now on contracts"). The avatar card
 * wants the newest one; the discussion wants none of them, or a slow stage
 * buries every other line.
 *
 * The shape is fixed by the executor's progressLine, so matching its opening
 * is enough — and a miss is cheap: one extra timeline line, never a lost
 * status.
 */
export function isHeartbeat(summary: string): boolean {
  return summary.startsWith("Running for ");
}

export interface RoomThought {
  avatarId: string;
  status: string;
  summary: string;
}

export interface RoomInput {
  /** The newest thought per avatar id. */
  thoughts: Record<string, RoomThought>;
  /** Avatar ids that have published a draft. */
  draftAvatars: readonly string[];
  /** A run is assigned: avatars with no status yet are queued, not idle. */
  runActive: boolean;
}

/** The status of one avatar, from its thought, its draft, or the run. */
export function avatarStatus(input: RoomInput, avatarId: string): AvatarStatus {
  const t = input.thoughts[avatarId];
  if (t !== undefined && KNOWN_STATUS.has(t.status)) return t.status as AvatarStatus;
  if (input.draftAvatars.includes(avatarId)) return "Synced";
  return input.runActive ? "Queued" : "Idle";
}

/**
 * The avatars still working this cycle. The synthesizer is excluded: it is
 * queued by design until the user starts synthesis.
 */
export function workingAvatars(input: RoomInput, avatarIds: readonly string[]): string[] {
  const out = avatarIds.filter((id) => id !== SYNTHESIZER_ID && WORKING_STATUS.has(avatarStatus(input, id)));
  for (const t of Object.values(input.thoughts)) {
    if (t.avatarId.startsWith("specialist:") && WORKING_STATUS.has(t.status)) out.push(t.avatarId);
  }
  return out;
}

export interface RunFailure {
  avatarId: string;
  summary: string;
}

/**
 * The stage that stopped the run, or null. A cancelled avatar is a consequence
 * of the failure, never the failure itself.
 */
export function runFailure(input: RoomInput): RunFailure | null {
  for (const t of Object.values(input.thoughts)) {
    if (t.status === "Failed") return { avatarId: t.avatarId, summary: t.summary };
  }
  return null;
}
