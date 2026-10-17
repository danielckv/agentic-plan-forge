// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  apiFetch,
  AUTH_OK,
  authFrame,
  bootstrap,
  bridgeWSURL,
  controlFrameType,
  HandshakeError,
  resetBootstrapForTests,
  WS_SUBPROTOCOL,
} from "./bridge";

/**
 * The loopback handshake in `internal/relay/local_ws.go`: the launch nonce is
 * single use and redeeming it twice aborts the session, so these cases pin
 * that the nonce leaves the URL before the request, that the token is reused
 * instead of re-redeemed, and that every API call carries it.
 */

const NONCE = "8c49925cf1c96bcc56ac84709182489b";
const TOKEN = "b6b2f0f7b1b64b2ba0c2f3c7d5e8a9f0";

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  resetBootstrapForTests();
  window.sessionStorage.clear();
  window.history.replaceState(null, "", "/");
  fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
  vi.unstubAllGlobals();
  globalThis.PLAN_FORGE_WS_URL = undefined;
});

describe("bootstrap", () => {
  it("redeems the launch nonce and strips it from the URL", async () => {
    window.location.hash = `#n=${NONCE}`;
    fetchMock.mockResolvedValue(jsonResponse({ token: TOKEN }));

    await expect(bootstrap()).resolves.toBe(TOKEN);

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [path, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(path).toBe("/api/session");
    expect(init.method).toBe("POST");
    expect(init.body).toBe(JSON.stringify({ nonce: NONCE }));
    expect(window.location.hash).toBe("");
  });

  it("redeems only once even when several callers race", async () => {
    window.location.hash = `#n=${NONCE}`;
    fetchMock.mockResolvedValue(jsonResponse({ token: TOKEN }));

    const [a, b] = await Promise.all([bootstrap(), bootstrap()]);

    expect(a).toBe(TOKEN);
    expect(b).toBe(TOKEN);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("reuses the stored token after a reload, without a nonce", async () => {
    window.sessionStorage.setItem("plan-forge:session-token", TOKEN);

    await expect(bootstrap()).resolves.toBe(TOKEN);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("fails when the window carries neither a nonce nor a stored token", async () => {
    await expect(bootstrap()).rejects.toBeInstanceOf(HandshakeError);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("reports the bridge's own refusal message", async () => {
    window.location.hash = `#n=${NONCE}`;
    fetchMock.mockResolvedValue(jsonResponse({ error: "launch nonce expired" }, 403));

    await expect(bootstrap()).rejects.toThrow("launch nonce expired");
  });

  it("skips the handshake under the preview harness", async () => {
    globalThis.PLAN_FORGE_WS_URL = "ws://127.0.0.1:4321/ws";

    await expect(bootstrap()).resolves.toBeNull();
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("apiFetch", () => {
  it("attaches the session token as a Bearer header", async () => {
    window.sessionStorage.setItem("plan-forge:session-token", TOKEN);
    fetchMock.mockResolvedValue(jsonResponse({ ok: true }));

    await apiFetch("/api/status");

    const [path, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(path).toBe("/api/status");
    expect(new Headers(init.headers).get("Authorization")).toBe(`Bearer ${TOKEN}`);
  });

  it("sends no Authorization header under the preview harness", async () => {
    globalThis.PLAN_FORGE_WS_URL = "ws://127.0.0.1:4321/ws";
    fetchMock.mockResolvedValue(jsonResponse({ ok: true }));

    await apiFetch("/api/status");

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(new Headers(init.headers).get("Authorization")).toBeNull();
  });
});

describe("socket contract", () => {
  it("derives the bridge socket URL from the document origin", () => {
    expect(bridgeWSURL()).toBe(`ws://${window.location.host}/ws`);
  });

  it("prefers the preview harness URL when one is injected", () => {
    globalThis.PLAN_FORGE_WS_URL = "ws://127.0.0.1:4321/ws";
    expect(bridgeWSURL()).toBe("ws://127.0.0.1:4321/ws");
  });

  it("builds the auth frame the bridge reads first", () => {
    expect(JSON.parse(authFrame(TOKEN))).toEqual({ type: "auth", token: TOKEN });
    expect(WS_SUBPROTOCOL).toBe("plan-forge.v1");
  });
});

describe("controlFrameType", () => {
  it("recognises the bridge's auth acknowledgement", () => {
    expect(controlFrameType(JSON.stringify({ type: AUTH_OK }))).toBe(AUTH_OK);
  });

  it("leaves application envelopes to the envelope decoder", () => {
    const envelope = JSON.stringify({ version: 1, event_type: "STATE_SNAPSHOT", payload: {} });
    expect(controlFrameType(envelope)).toBeNull();
  });

  it("treats a versioned frame as an envelope even when it carries a type", () => {
    expect(controlFrameType(JSON.stringify({ version: 1, type: "auth.ok" }))).toBeNull();
  });

  it("leaves malformed and oversized frames to the envelope decoder", () => {
    expect(controlFrameType("{not json")).toBeNull();
    expect(controlFrameType(JSON.stringify([1, 2, 3]))).toBeNull();
    expect(controlFrameType(`{"type":"${"a".repeat(300)}"}`)).toBeNull();
  });
});
