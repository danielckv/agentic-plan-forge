import type { ConnState } from "../store/syncStore";

/**
 * A dropped socket is the normal case, not a failure: the authority restarts,
 * a cycle finishes, the machine sleeps. "closed" therefore reads as
 * reconnecting, and only a refused handshake ("offline") reads as broken.
 */
export const CONN_META: Record<ConnState, { label: string; tone: string }> = {
  connecting: { label: "Connecting", tone: "pf-chip--warn pf-chip--live" },
  open: { label: "Connected", tone: "pf-chip--ok" },
  resyncing: { label: "Resyncing", tone: "pf-chip--warn pf-chip--live" },
  offline: { label: "Offline", tone: "pf-chip--danger" },
  closed: { label: "Reconnecting", tone: "pf-chip--warn pf-chip--live" },
};
