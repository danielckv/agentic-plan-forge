/**
 * Schema validation shared with the Go implementation.
 *
 * The strict JSON Schemas live in `internal/contracts/schema/`. Ajv compiles
 * them at build time (`web/scripts/gen-validators.mjs`) into plain ES module
 * source, so the frontend validates against the exact same bytes Go validates
 * against and the browser never calls `new Function`: the bridge serves the UI
 * under `default-src 'self'` with no `'unsafe-eval'`, which makes runtime
 * schema compilation a load-time `EvalError`.
 */

import { envelopeV1, impactMapV1, normalizedPrdV1, synthesisV1 } from "./validators.generated.js";
import type { StandaloneValidate } from "./validators.generated.js";

import mermaidDenylistSchema from "@schema/mermaid-denylist.json";

export type SchemaId = "envelope.v1" | "normalized_prd.v1" | "impact_map.v1" | "synthesis.v1";

const validators: Record<SchemaId, StandaloneValidate> = {
  "envelope.v1": envelopeV1,
  "normalized_prd.v1": normalizedPrdV1,
  "impact_map.v1": impactMapV1,
  "synthesis.v1": synthesisV1,
};

export interface SchemaResult {
  ok: boolean;
  errors?: Array<{ path: string; message: string }>;
}

/** Validate a payload against a shared schema; unknown keys are errors. */
export function validate(schemaId: SchemaId, data: unknown): SchemaResult {
  const validator = validators[schemaId];
  if (validator(data)) return { ok: true };
  // A standalone validator reports through its own `errors` property; the
  // next call overwrites it, so it is read immediately.
  const errs = validator.errors ?? [];
  return {
    ok: false,
    errors: errs.map((e) => ({
      path: e.instancePath || "/",
      message: `${e.keyword}: ${e.message ?? ""}`.trim(),
    })),
  };
}

/** Mermaid statement denylist tokens, read from the shared schema file. */
export const mermaidDenylistTokens: readonly string[] = (
  mermaidDenylistSchema.tokens as readonly string[]
);

/**
 * Shared Mermaid statement gate.
 *
 * Statements are the source split on every newline and every `;`, each
 * trimmed. A statement is denied when its first token, case-insensitively,
 * is in the denylist. The same rule runs in the Go compiler, the Mermaid
 * render component, and the sanitizing markdown renderer; this function is
 * the one implementation the frontend uses everywhere.
 */
export function mermaidStatementDenied(source: string): string | null {
  const statements = source
    .split(/[\n;]/)
    .map((s) => s.trim())
    .filter((s) => s.length > 0);
  for (const stmt of statements) {
    const first = stmt.split(/\s+/)[0]!.toLowerCase();
    if (mermaidDenylistTokens.includes(first)) return first;
  }
  return null;
}

/**
 * Pre-parse Mermaid source gate: reject frontmatter (`---`), directives
 * (`%%`), `themeCSS`, and denied statement keywords before any render.
 */
export function mermaidSourceRejected(source: string): string | null {
  let src = source;
  if (src.startsWith("﻿")) src = src.slice(1);
  if (/^\s*---/.test(src)) return "frontmatter block";
  if (src.includes("%%")) return "directive (%%)";
  if (src.includes("themeCSS")) return "themeCSS";
  const denied = mermaidStatementDenied(src);
  if (denied) return `denied statement keyword: ${denied}`;
  return null;
}
