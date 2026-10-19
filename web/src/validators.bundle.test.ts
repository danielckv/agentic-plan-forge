import { runInNewContext } from "node:vm";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { build } from "vite";

/**
 * Regression guard for the "Hi is not a function" window crash.
 *
 * The generated Ajv validators import `ajv/dist/runtime/ucs2length`, a CJS
 * module that sets `exports.default`. Vitest resolves that import with its own
 * interop, so the unit suite stayed green while the production bundle (built by
 * Rolldown, which treats importers under a `type: module` package with Node ESM
 * semantics) bound `default` to the whole `module.exports` object. The first
 * `minLength` / `maxLength` check on an inbound AVATAR_DRAFT envelope then threw
 * inside `ws.onmessage`, the draft never reached the store, and no diagram
 * ever mounted.
 *
 * This test bundles `validators.generated.js` with the real bundler and runs
 * the output, so it fails for exactly that interop break and nothing else.
 */

interface BundledValidators {
  envelopeV1: ((data: unknown) => boolean) & { errors?: unknown };
  normalizedPrdV1: (data: unknown) => boolean;
  synthesisV1: (data: unknown) => boolean;
}

const root = fileURLToPath(new URL("..", import.meta.url));
const entry = fileURLToPath(new URL("./protocol/validators.generated.js", import.meta.url));

async function bundleValidators(): Promise<BundledValidators> {
  const result = await build({
    configFile: false,
    root,
    logLevel: "silent",
    build: {
      write: false,
      minify: false,
      lib: { entry, formats: ["iife"], name: "PfValidators", fileName: "validators" },
    },
  });
  const outputs = Array.isArray(result) ? result : [result];
  for (const out of outputs) {
    if (!("output" in out)) continue;
    for (const chunk of out.output) {
      if (chunk.type === "chunk" && chunk.isEntry) {
        return runInNewContext(`${chunk.code}\nPfValidators`) as BundledValidators;
      }
    }
  }
  throw new Error("vite build produced no entry chunk for validators.generated.js");
}

const validEnvelope = (): Record<string, unknown> => ({
  version: 1,
  session_id: "s",
  sender_id: "m",
  event_id: "e",
  event_type: "PRESENCE",
  seq: "1",
  revision: "1",
  epoch: "1",
  timestamp: "2026-10-17T10:15:00Z",
  payload: {},
});

describe("validators bundled for production", () => {
  it("runs the string-length checks (ucs2length) without throwing", async () => {
    const v = await bundleValidators();

    expect(v.envelopeV1(validEnvelope())).toBe(true);

    // minLength: an empty id is rejected, not a TypeError.
    expect(() => v.envelopeV1({ ...validEnvelope(), session_id: "" })).not.toThrow();
    expect(v.envelopeV1({ ...validEnvelope(), session_id: "" })).toBe(false);

    // maxLength: 65 code units exceeds the 64 limit.
    expect(v.envelopeV1({ ...validEnvelope(), sender_id: "x".repeat(65) })).toBe(false);
    // An astral character is two UTF-16 units but one code point; the limit
    // counts code points, which is what ucs2length computes.
    expect(v.envelopeV1({ ...validEnvelope(), sender_id: "😀".repeat(64) })).toBe(true);
  }, 60_000);
});
