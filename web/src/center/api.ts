/**
 * The command center's handshake and REST calls.
 *
 * The launch ticket arrives in the URL fragment and is taken out of the
 * address bar before the first network call, so a reload can never replay a
 * spent one. The session token it buys lives in `sessionStorage` only: never a
 * cookie, which on loopback would be sent to every other service on this host,
 * and never the URL, which reaches history and logs.
 *
 * A tab opened by hand carries no ticket. That is not an error to retry but a
 * state to render: the daemon refuses to mint a token without proof the caller
 * held the local token file, so the UI parks and names the command to run.
 */

import type { Snapshot, SprintResponse } from "./types";

const TOKEN_KEY = "plan-forge.center.token";
const TICKET_PREFIX = "#t=";

/** The handshake refused: the tab needs to be opened by the CLI. */
export class NotLaunchedError extends Error {}

/** The daemon could not be reached or refused for another reason. */
export class CenterError extends Error {}

function readStoredToken(): string | null {
  try {
    return window.sessionStorage.getItem(TOKEN_KEY);
  } catch {
    return null;
  }
}

function storeToken(token: string): void {
  try {
    window.sessionStorage.setItem(TOKEN_KEY, token);
  } catch {
    // Not fatal: the in-memory memo still serves this document.
  }
}

/**
 * Take the launch ticket out of the URL and return it. Removing it first means
 * a reload cannot replay a ticket the daemon has already spent.
 */
export function takeTicket(): string | null {
  const hash = window.location.hash;
  if (!hash.startsWith(TICKET_PREFIX)) return null;
  const ticket = hash.slice(TICKET_PREFIX.length);
  window.history.replaceState(null, "", window.location.pathname + window.location.search);
  return /^[0-9a-f]{2,128}$/.test(ticket) ? ticket : null;
}

async function redeem(ticket: string): Promise<string> {
  let res: Response;
  try {
    res = await fetch("/api/session", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ticket }),
    });
  } catch (err) {
    throw new CenterError(
      `the command center did not answer: ${err instanceof Error ? err.message : "network error"}`,
    );
  }
  if (res.status === 403) throw new NotLaunchedError("this tab was not opened by plan-forge");
  if (!res.ok) throw new CenterError(`the command center answered ${String(res.status)}`);
  const body = (await res.json().catch(() => ({}))) as { token?: unknown };
  if (typeof body.token !== "string" || body.token === "") {
    throw new CenterError("the command center returned no session token");
  }
  return body.token;
}

/** One redemption per document, shared by every caller and every reconnect. */
let pending: Promise<string> | null = null;

/**
 * Resolve the session token for this tab. Rejects with NotLaunchedError when
 * the tab has no ticket and no stored token, which the shell renders as the
 * parked state rather than an error.
 */
export function bootstrap(): Promise<string> {
  pending ??= (async (): Promise<string> => {
    const ticket = takeTicket();
    if (ticket) {
      const token = await redeem(ticket);
      storeToken(token);
      return token;
    }
    const stored = readStoredToken();
    if (stored) return stored;
    throw new NotLaunchedError("this tab was not opened by plan-forge");
  })();
  return pending;
}

/** Forget the memo, so the next bootstrap re-reads the URL and storage. */
export function resetBootstrap(): void {
  pending = null;
}

async function getJSON<T>(path: string, token: string): Promise<T> {
  let res: Response;
  try {
    res = await fetch(path, { headers: { Authorization: `Bearer ${token}` } });
  } catch (err) {
    throw new CenterError(
      `the command center did not answer: ${err instanceof Error ? err.message : "network error"}`,
    );
  }
  if (res.status === 401) {
    // The token expired or the daemon restarted. Drop it so the next load
    // re-reads the URL instead of retrying a credential that cannot work.
    try {
      window.sessionStorage.removeItem(TOKEN_KEY);
    } catch {
      // ignored
    }
    resetBootstrap();
    throw new NotLaunchedError("the session expired");
  }
  if (!res.ok) throw new CenterError(`the command center answered ${String(res.status)}`);
  return (await res.json()) as T;
}

export function fetchSnapshot(token: string): Promise<Snapshot> {
  return getJSON<Snapshot>("/api/center/live", token);
}

export function fetchSprint(token: string): Promise<SprintResponse> {
  return getJSON<SprintResponse>("/api/center/sprint", token);
}

export const EVENTS_URL = "/api/center/events";
