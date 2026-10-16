import { ChangeSet, type ChangeDesc } from "@codemirror/state";

/**
 * CodeMirror collab client model (central OT authority lives in Go).
 * Mirrors the authority's Push/rebase semantics: sendable updates are
 * uncomposed, one entry per local update; own updates are confirmed
 * one-to-one by client ID.
 */

/** One local update pending central acknowledgement. */
export interface LocalUpdate {
  clientID: string;
  changes: ChangeSet;
}

/**
 * Build the uncomposed `updates` array for `TEXT_DELTA`: one entry per local
 * update, each serialized with `ChangeSet.toJSON()`, never composed — the
 * authority confirms own updates one-to-one by client ID.
 */
export function sendableUpdates(pending: LocalUpdate[], baseVersion: number): {
  document_id: "prd";
  base_version: string;
  updates: Array<{ client_id: string; changes: unknown }>;
} {
  return {
    document_id: "prd",
    base_version: String(baseVersion),
    updates: pending.map((u) => ({ client_id: u.clientID, changes: u.changes.toJSON() })),
  };
}

/** Deserialize a ChangeSet from `TEXT_UPDATES` payload JSON. */
export function changeSetFromJSON(json: unknown): ChangeSet {
  return ChangeSet.fromJSON(json as ChangeDesc);
}

/**
 * The transactionFilter rule: reject lone surrogates and `\r` in inserted
 * text before anything is sent. `String.prototype.isWellFormed` where
 * available; a manual scan otherwise.
 */
export function insertAllowed(text: string): boolean {
  if (text.includes("\r")) return false;
  if (typeof (text as string & { isWellFormed?: () => boolean }).isWellFormed === "function") {
    return (text as string & { isWellFormed: () => boolean }).isWellFormed();
  }
  for (let i = 0; i < text.length; i++) {
    const code = text.charCodeAt(i);
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = i + 1 < text.length ? text.charCodeAt(i + 1) : 0;
      if (next < 0xdc00 || next > 0xdfff) return false;
      i++;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return false;
    }
  }
  return true;
}

/** `sha256(utf8(text))` for `TEXT_CHECK` idle verification. */
export async function textSha256(text: string): Promise<string> {
  const bytes = new TextEncoder().encode(text);
  const digest = await crypto.subtle.digest("SHA-256", bytes);
  return Array.from(new Uint8Array(digest))
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");
}

