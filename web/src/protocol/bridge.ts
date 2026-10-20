/**
 * Loopback bridge handshake.
 *
 * `internal/relay/local_ws.go` launches the Plan-Forge tab at
 * `http://127.0.0.1:<port>/#n=<nonce>` and answers nothing else until the page
 * completes this exchange:
 *
 *   POST /api/session {"nonce":"<hex>"}  -> {"token":"<hex>"}
 *   GET  /ws   (subprotocol plan-forge.v1), first frame {"type":"auth","token":"<hex>"}
 *   GET  /api/*  with Authorization: Bearer <token>
 *
 * The nonce is 128 random bits, lives 10 seconds and is redeemable once:
 * presenting it twice aborts the session on the Go side. It is therefore
 * stripped from the URL before the redemption request goes out, the resulting
 * token is held in `sessionStorage` so a reload or a socket reconnect reuses
 * it, and the redemption itself is memoized for the lifetime of the document.
 */

/** Subprotocol both ends require; see `relay.WSSubprotocol`. */
export const WS_SUBPROTOCOL = "plan-forge.v1";

const TOKEN_STORAGE_KEY = "plan-forge:session-token";
const NONCE_HASH_PREFIX = "#n=";

/** Thrown when the window cannot establish a session with its own bridge. */
export class HandshakeError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "HandshakeError";
  }
}

/**
 * The preview harness (`web/scripts/preview.mjs`) injects its own relay URL and
 * plays the authority without the loopback handshake. A page that carries this
 * global is not talking to the Go bridge and must not try to redeem a nonce.
 */
function injectedWSURL(): string | undefined {
  return globalThis.PLAN_FORGE_WS_URL;
}

/** The bridge WebSocket endpoint for this document's origin. */
export function bridgeWSURL(): string {
  const injected = injectedWSURL();
  if (injected !== undefined) return injected;
  const scheme = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${scheme}//${window.location.host}/ws`;
}

function readStoredToken(): string | null {
  try {
    return window.sessionStorage.getItem(TOKEN_STORAGE_KEY);
  } catch {
    // Storage can be denied by policy; the handshake then runs once per load.
    return null;
  }
}

function storeToken(token: string): void {
  try {
    window.sessionStorage.setItem(TOKEN_STORAGE_KEY, token);
  } catch {
    // Not fatal: the in-memory memo still serves this document.
  }
}

/**
 * Take the launch nonce out of the URL and return it. Removing it first means
 * a reload can never replay a spent nonce and abort the session.
 */
function takeNonce(): string | null {
  const hash = window.location.hash;
  if (!hash.startsWith(NONCE_HASH_PREFIX)) return null;
  const nonce = hash.slice(NONCE_HASH_PREFIX.length);
  window.history.replaceState(null, "", window.location.pathname + window.location.search);
  if (!/^[0-9a-f]{2,128}$/.test(nonce)) return null;
  return nonce;
}

async function redeem(nonce: string): Promise<string> {
  let res: Response;
  try {
    res = await fetch("/api/session", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ nonce }),
    });
  } catch (err) {
    throw new HandshakeError(
      `the local bridge did not answer: ${err instanceof Error ? err.message : "network error"}`,
    );
  }
  if (!res.ok) {
    const body = (await res.json().catch(() => ({}))) as { error?: string };
    throw new HandshakeError(body.error ?? `the local bridge answered ${String(res.status)}`);
  }
  const body = (await res.json().catch(() => ({}))) as { token?: unknown };
  if (typeof body.token !== "string" || body.token === "") {
    throw new HandshakeError("the local bridge returned no session token");
  }
  return body.token;
}

/** Memo: one redemption per document, shared by every caller and reconnect. */
let pending: Promise<string | null> | null = null;

/**
 * Resolve the session token for this window, redeeming the launch nonce on the
 * first call. Resolves to `null` when the page runs under the preview harness,
 * which needs no token. Rejects with `HandshakeError` when the bridge is
 * reachable but refuses the handshake.
 */
export function bootstrap(): Promise<string | null> {
  pending ??= (async (): Promise<string | null> => {
    if (injectedWSURL() !== undefined) return null;
    const nonce = takeNonce();
    if (nonce === null) {
      const stored = readStoredToken();
      if (stored !== null) return stored;
      throw new HandshakeError(
        "this window was not launched by plan-forge (no session nonce in the URL)",
      );
    }
    const token = await redeem(nonce);
    storeToken(token);
    return token;
  })().catch((err: unknown) => {
    // A failed handshake is terminal for this document: the nonce is spent.
    // Keep the rejection memoized so callers do not retry and abort the
    // session with a replayed nonce.
    throw err;
  });
  return pending;
}

/** The bridge's acknowledgement of the auth frame. */
export const AUTH_OK = "auth.ok";

/**
 * Classify one text frame from the bridge.
 *
 * The bridge answers the auth frame with `{"type":"auth.ok"}` and only then
 * forwards application envelopes (`internal/relay/local_ws.go`). Control
 * frames carry `type` and no `version`; feeding one to the envelope decoder
 * reads as `unsupported wire version undefined`, which closes the socket and
 * starts a reconnect loop that re-authenticates and fails the same way.
 * Returns the control type, or null when the frame is an envelope.
 */
export function controlFrameType(text: string): string | null {
  if (text.length > 256) return null; // control frames are tiny
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return null; // malformed: let the envelope decoder report it
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) return null;
  const frame = parsed as { type?: unknown; version?: unknown };
  if (frame.version !== undefined) return null;
  return typeof frame.type === "string" ? frame.type : null;
}

/** The first WebSocket frame the bridge reads; nothing else is read before it. */
export function authFrame(token: string): string {
  return JSON.stringify({ type: "auth", token });
}

/**
 * `fetch` against the bridge API with the session token attached. Every
 * `/api/*` route except `POST /api/session` requires the Bearer header.
 */
export async function apiFetch(path: string, init?: RequestInit): Promise<Response> {
  const token = await bootstrap();
  const headers = new Headers(init?.headers);
  if (token !== null) headers.set("Authorization", `Bearer ${token}`);
  return fetch(path, { ...init, headers });
}

/** Test seam: drop the memoized handshake so each case starts clean. */
export function resetBootstrapForTests(): void {
  pending = null;
}
