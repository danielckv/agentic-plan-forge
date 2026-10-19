/**
 * Server-sent events read over `fetch`, not `EventSource`.
 *
 * `EventSource` cannot set an `Authorization` header, and the two ways around
 * that are both refused here: a token in the query string reaches logs, and a
 * cookie on loopback is port-blind, so it would be sent to every other service
 * on 127.0.0.1. Reading the stream through `fetch` keeps the Bearer header,
 * which is also what forces a CORS preflight and makes the fixed port safe
 * against a page on another origin.
 *
 * The parser handles the parts of the wire format the server actually emits:
 * `id:`, `event:`, one or more `data:` lines joined with newlines, and `:`
 * comments used as heartbeats. A frame split across two network chunks is
 * reassembled, because chunk boundaries have nothing to do with frame
 * boundaries.
 */

/** One decoded server-sent event. */
export interface SSEMessage {
  id: string;
  event: string;
  data: string;
}

/** The biggest single frame that will be accepted, as a backstop. */
export const MAX_FRAME_BYTES = 4 << 20;

export class SSEError extends Error {}

/**
 * Split a buffer into complete frames and whatever remains.
 *
 * Exported for the test: frame reassembly is the part most likely to break
 * silently, because it only shows up when a chunk lands mid-frame.
 */
export function splitFrames(buffer: string): { frames: string[]; rest: string } {
  const normalised = buffer.replace(/\r\n/g, "\n");
  const parts = normalised.split("\n\n");
  const rest = parts.pop() ?? "";
  return { frames: parts.filter((p) => p !== ""), rest };
}

/** Parse one frame's lines. Returns null for a frame that is only comments. */
export function parseFrame(frame: string): SSEMessage | null {
  let id = "";
  let event = "";
  const data: string[] = [];
  for (const line of frame.split("\n")) {
    if (line === "" || line.startsWith(":")) continue;
    const colon = line.indexOf(":");
    const field = colon === -1 ? line : line.slice(0, colon);
    // A single leading space after the colon is part of the format, not data.
    let value = colon === -1 ? "" : line.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    switch (field) {
      case "id":
        id = value;
        break;
      case "event":
        event = value;
        break;
      case "data":
        data.push(value);
        break;
      default:
        break;
    }
  }
  if (data.length === 0 && event === "") return null;
  return { id, event: event || "message", data: data.join("\n") };
}

/** What a caller supplies to open a stream. */
export interface StreamOptions {
  url: string;
  token: string;
  signal: AbortSignal;
  onMessage: (msg: SSEMessage) => void;
  /** Called once the response headers arrive, so a UI can clear "connecting". */
  onOpen?: () => void;
}

/**
 * Read an event stream until the signal aborts or the server closes it.
 *
 * It resolves when the stream ends rather than throwing, so the caller's
 * reconnect loop stays in one place. A transport failure rejects.
 */
export async function readStream(opts: StreamOptions): Promise<void> {
  const res = await fetch(opts.url, {
    headers: { Authorization: `Bearer ${opts.token}`, Accept: "text/event-stream" },
    signal: opts.signal,
  });
  if (!res.ok) {
    throw new SSEError(`the command center answered ${String(res.status)}`);
  }
  if (!res.body) {
    throw new SSEError("the command center returned no stream");
  }
  opts.onOpen?.();

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  for (;;) {
    const { done, value } = await reader.read();
    if (done) return;
    buffer += decoder.decode(value, { stream: true });
    if (buffer.length > MAX_FRAME_BYTES) {
      throw new SSEError("a single event exceeded the size cap");
    }
    const { frames, rest } = splitFrames(buffer);
    buffer = rest;
    for (const frame of frames) {
      const msg = parseFrame(frame);
      if (msg) opts.onMessage(msg);
    }
  }
}
