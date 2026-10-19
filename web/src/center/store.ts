/**
 * The command center's state: one external store the pages subscribe to.
 *
 * It mirrors the board store's shape — an external store read through
 * `useSyncExternalStore`, with its own transport — rather than extending it.
 * The board speaks WebSocket to a per-session bridge and this speaks SSE to a
 * long-lived daemon, so sharing the transport would couple two things that
 * reconnect, authenticate and fail differently.
 */

import { bootstrap, CenterError, EVENTS_URL, fetchSprint, NotLaunchedError } from "./api";
import { readStream } from "./sse";
import type { Snapshot, SprintResponse } from "./types";

export type ConnState = "connecting" | "live" | "offline" | "parked";

export interface CenterState {
  conn: ConnState;
  snapshot: Snapshot;
  sprint: SprintResponse;
  /** Set when the tab was not opened by the CLI, or its session expired. */
  parked: boolean;
  /** A transport error worth showing, cleared on the next successful frame. */
  error: string | null;
}

const EMPTY: CenterState = {
  conn: "connecting",
  snapshot: { at: "", repos: [] },
  sprint: { at: "", repos: [] },
  parked: false,
  error: null,
};

/** Reconnect backoff, matching the board's: quick at first, then patient. */
const BACKOFF_MIN_MS = 1_000;
const BACKOFF_MAX_MS = 10_000;

/** How often the plan view is refreshed. It needs a repository scan, so it is
 * deliberately slower than the event stream, which is pushed. */
const SPRINT_REFRESH_MS = 15_000;

let state: CenterState = EMPTY;
const listeners = new Set<() => void>();
let started = false;
let abort: AbortController | null = null;
let sprintTimer: ReturnType<typeof setInterval> | null = null;

function set(patch: Partial<CenterState>): void {
  state = { ...state, ...patch };
  for (const l of listeners) l();
}

export function getState(): CenterState {
  return state;
}

export function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/** Reset everything. Tests use it; the app calls it on unmount. */
export function stop(): void {
  abort?.abort();
  abort = null;
  if (sprintTimer) clearInterval(sprintTimer);
  sprintTimer = null;
  started = false;
  state = EMPTY;
}

async function refreshSprint(token: string): Promise<void> {
  try {
    set({ sprint: await fetchSprint(token) });
  } catch (err) {
    // The plan view is supplementary: losing it must not take the live view
    // down with it, so the failure is swallowed and retried on the next tick.
    if (err instanceof NotLaunchedError) set({ parked: true, conn: "parked" });
  }
}

/**
 * Connect and keep connected. The loop owns reconnection so no page has to:
 * an ended stream is normal (the daemon rotates one after six hours) and is
 * retried, while a refused handshake parks the tab instead of spinning.
 */
export async function start(): Promise<void> {
  if (started) return;
  started = true;
  let delay = BACKOFF_MIN_MS;

  for (;;) {
    if (!started) return;
    let token: string;
    try {
      token = await bootstrap();
    } catch (err) {
      if (err instanceof NotLaunchedError) {
        set({ parked: true, conn: "parked" });
        return;
      }
      set({ conn: "offline", error: err instanceof Error ? err.message : "handshake failed" });
      await sleep(delay);
      delay = Math.min(delay * 2, BACKOFF_MAX_MS);
      continue;
    }

    void refreshSprint(token);
    if (!sprintTimer) {
      sprintTimer = setInterval(() => void refreshSprint(token), SPRINT_REFRESH_MS);
    }

    abort = new AbortController();
    try {
      await readStream({
        url: EVENTS_URL,
        token,
        signal: abort.signal,
        onOpen: () => set({ conn: "live", error: null }),
        onMessage: (msg) => {
          if (msg.event === "overflow") {
            // The daemon dropped us because we stopped reading. The frames we
            // missed are gone, so refetch rather than carry on from a gap.
            set({ error: "reconnecting after a dropped stream" });
            return;
          }
          if (msg.event === "rotate") return;
          if (msg.event !== "snapshot") return;
          try {
            set({ snapshot: JSON.parse(msg.data) as Snapshot, conn: "live", error: null });
          } catch {
            set({ error: "the command center sent an unreadable snapshot" });
          }
        },
      });
      // A stream that ends cleanly is expected; reconnect promptly.
      delay = BACKOFF_MIN_MS;
      set({ conn: "connecting" });
    } catch (err) {
      if (!started) return;
      if (err instanceof NotLaunchedError) {
        set({ parked: true, conn: "parked" });
        return;
      }
      const msg = err instanceof CenterError || err instanceof Error ? err.message : "stream failed";
      set({ conn: "offline", error: msg });
      await sleep(delay);
      delay = Math.min(delay * 2, BACKOFF_MAX_MS);
    }
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
