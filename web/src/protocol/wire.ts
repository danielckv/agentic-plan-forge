/**
 * Shared wire protocol for Plan-Forge.
 *
 * The strict JSON Schemas in `internal/contracts/schema/` are the single
 * source of truth; they are imported here through the `@schema/` Vite alias
 * so the frontend validates with the exact same bytes Go validates against.
 * No wire type is re-declared on the frontend beyond ergonomic TypeScript
 * views of the envelope; anything that must not disagree with the schema is
 * looked up from the schema file itself (see `eventTypeEnum`).
 */

import envelopeSchema from "@schema/envelope.v1.json";

/** Event type names, derived from the schema so they can never drift. */
const eventTypeEnum: readonly string[] = (
  envelopeSchema.properties.event_type.enum as readonly string[]
);

const isKnownEventType = (t: string): boolean => eventTypeEnum.includes(t);

/** The nine business event types plus control and doc-channel events. */
export type WSEventType = (typeof envelopeSchema.properties.event_type.enum)[number];

/**
 * Envelope as decoded on the wire. Decimal-string counters (`seq`,
 * `revision`, `epoch`) avoid JavaScript integer precision loss; they are
 * never parsed into Numbers.
 */
export interface WSEnvelope {
  version: number;
  session_id: string;
  sender_id: string;
  event_id: string;
  event_type: WSEventType;
  seq: string;
  revision: string;
  epoch: string;
  timestamp: string;
  payload: unknown;
}

const WIRE_VERSION = 1;

/**
 * Duplicate-key-strict, no-trailing-JSON, invalid-UTF-8 rejecting decode of
 * one wire frame. Mirrors the Go decoder's rejection rules:
 *
 * - duplicate JSON keys anywhere in the object tree are a protocol violation;
 * - trailing content after the first JSON value is a protocol violation;
 * - the `text` must parse as an object at the top level.
 */
function decodeEnvelope(text: string): { ok: true; value: WSEnvelope } | { ok: false; error: string } {
  let parsed: unknown;
  try {
    // JSON.parse rejects trailing content and malformed escapes; duplicate
    // keys are checked separately by decodeFrame via findDuplicateKeys.
    parsed = JSON.parse(text) as unknown;
  } catch (err) {
    return { ok: false, error: err instanceof Error ? err.message : "invalid JSON" };
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
    return { ok: false, error: "frame must be a JSON object" };
  }
  const env = parsed as WSEnvelope;
  if (env.version !== WIRE_VERSION) {
    return { ok: false, error: `unsupported wire version ${String(env.version)}` };
  }
  if (typeof env.event_type !== "string" || !isKnownEventType(env.event_type)) {
    return { ok: false, error: "unknown event_type" };
  }
  return { ok: true, value: env };
}

/** Report duplicate keys the way the schema conformance suite expects. */
function findDuplicateKeys(text: string): string[] {
  const dupes: string[] = [];
  const stack: Array<Set<string>> = [];
  let i = 0;
  const s = text;
  const len = s.length;
  const skipString = (): number => {
    // i points at the opening quote; returns index after the closing quote.
    let j = i + 1;
    while (j < len) {
      if (s[j] === "\\") j += 2;
      else if (s[j] === '"') return j + 1;
      else j++;
    }
    throw new SyntaxError("unterminated string");
  };
  const expect = (chars: string): void => {
    while (i < len && /\s/.test(s[i]!)) i++;
    if (!chars.includes(s[i]!)) {
      throw new SyntaxError(`unexpected ${s[i] ?? "EOF"} at ${i}`);
    }
  };
  const value = (): void => {
    expect('{["-0123456789tfn');
    const c = s[i]!;
    if (c === "{") {
      i++;
      const seen = new Set<string>();
      stack.push(seen);
      while (i < len && /\s/.test(s[i]!)) i++;
      if (s[i] === "}") {
        i++;
        stack.pop();
        return;
      }
      for (;;) {
        expect('"');
        const start = i;
        const end = skipString();
        const key = s.slice(start, end);
        if (seen.has(key)) dupes.push(key);
        seen.add(key);
        i = end;
        expect(":");
        i++;
        value();
        expect(",}");
        if (s[i] === ",") {
          i++;
          continue;
        }
        i++;
        stack.pop();
        return;
      }
    } else if (c === "[") {
      i++;
      stack.push(new Set<string>());
      while (i < len && /\s/.test(s[i]!)) i++;
      if (s[i] === "]") {
        i++;
        stack.pop();
        return;
      }
      for (;;) {
        value();
        expect(",]");
        if (s[i] === ",") {
          i++;
          continue;
        }
        i++;
        stack.pop();
        return;
      }
    } else if (c === '"') {
      i = skipString();
    } else {
      // number / literal
      while (i < len && !",]}".includes(s[i]!) && !/\s/.test(s[i]!)) i++;
    }
  };
  value();
  while (i < len && /\s/.test(s[i]!)) i++;
  if (i !== len) throw new SyntaxError("trailing content after JSON value");
  return dupes;
}

interface FrameResult {
  ok: boolean;
  envelope?: WSEnvelope;
  error?: string;
}

/**
 * Full strict frame decode: duplicate keys, trailing JSON, and unknown event
 * types are protocol violations; the caller closes the socket with 1008.
 */
export function decodeFrame(text: string): FrameResult {
  if (text.length > 4 * 1024 * 1024) {
    return { ok: false, error: "frame exceeds the 4 MiB decode cap" };
  }
  let dupes: string[];
  try {
    dupes = findDuplicateKeys(text);
  } catch (err) {
    return { ok: false, error: err instanceof Error ? err.message : "invalid JSON" };
  }
  if (dupes.length > 0) {
    return { ok: false, error: `duplicate JSON keys: ${dupes.join(", ")}` };
  }
  const decoded = decodeEnvelope(text);
  if (!decoded.ok) return { ok: false, error: decoded.error };
  return { ok: true, envelope: decoded.value };
}
