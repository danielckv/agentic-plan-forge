/**
 * Schema validation shared with the Go implementation.
 *
 * The strict JSON Schemas live in `internal/contracts/schema/` and are
 * imported through the `@schema/` Vite alias, so the frontend validates
 * against the exact same bytes Go validates against. Ajv runs with the
 * 2020 dialect and no network access; `$ref` loading stays disabled.
 */

import Ajv2020 from "ajv/dist/2020";
import addFormats from "ajv-formats";

import envelopeSchema from "@schema/envelope.v1.json";
import normalizedPrdSchema from "@schema/normalized_prd.v1.json";
import synthesisSchema from "@schema/synthesis.v1.json";
import mermaidDenylistSchema from "@schema/mermaid-denylist.json";

export type SchemaId = "envelope.v1" | "normalized_prd.v1" | "synthesis.v1";

const SCHEMA_BY_ID: Record<SchemaId, object> = {
  "envelope.v1": envelopeSchema,
  "normalized_prd.v1": normalizedPrdSchema,
  "synthesis.v1": synthesisSchema,
};

/** Single Ajv instance; schemas are local embedded resources, no network. */
const ajv = new Ajv2020({ allErrors: true, strict: true, allowUnionTypes: true });
addFormats(ajv);

const validators: Record<SchemaId, (data: unknown) => boolean> = {
  "envelope.v1": ajv.compile(SCHEMA_BY_ID["envelope.v1"]) as (d: unknown) => boolean,
  "normalized_prd.v1": ajv.compile(SCHEMA_BY_ID["normalized_prd.v1"]) as (d: unknown) => boolean,
  "synthesis.v1": ajv.compile(SCHEMA_BY_ID["synthesis.v1"]) as (d: unknown) => boolean,
};


export interface SchemaResult {
  ok: boolean;
  errors?: Array<{ path: string; message: string }>;
}

/** Validate a payload against a shared schema; unknown keys are errors. */
export function validate(schemaId: SchemaId, data: unknown): SchemaResult {
  const valid = validators[schemaId](data);
  if (valid) return { ok: true };
  const errs = ajv.errors ?? [];
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
