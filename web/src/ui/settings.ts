/**
 * The settings document as the loopback bridge serves it.
 *
 * `GET /api/settings` returns the persisted `config.Config` plus the revision a
 * saving client must echo; `PUT /api/settings` takes the whole document back
 * and answers 409 when that revision is stale. Only the keys this window can
 * edit are interpreted here — every other key is carried through untouched, so
 * a save can never drop a setting the window does not know about.
 */

import { apiFetch } from "../protocol/bridge";

export interface SettingsDocument {
  /** The revision this document was read at; a save echoes it back. */
  revision: number;
  /**
   * How many expertise-gap agents a run may add, 0 through 3. Absent when the
   * running binary has no such limit, which the window states rather than
   * inventing a default.
   */
  max_gap_agents?: number;
  [key: string]: unknown;
}

const MAX_GAP_AGENTS_MIN = 0;
const MAX_GAP_AGENTS_MAX = 3;
export const MAX_GAP_AGENTS_DEFAULT = 2;

/** The values the limit accepts, in order. */
export const MAX_GAP_AGENTS_CHOICES: readonly number[] = [0, 1, 2, 3];

/** How one value reads in the control: "None", "1 agent", "2 agents". */
export function gapAgentLabel(value: number): string {
  if (value === 0) return "None";
  return value === 1 ? "1 agent" : `${String(value)} agents`;
}

/**
 * The limit as the server will accept it. A value outside 0–3, a fraction or a
 * non-number becomes the nearest legal value, so a control can never send a
 * document `config.Validate` rejects.
 */
export function clampMaxGapAgents(value: number): number {
  if (!Number.isFinite(value)) return MAX_GAP_AGENTS_DEFAULT;
  const whole = Math.trunc(value);
  return Math.min(MAX_GAP_AGENTS_MAX, Math.max(MAX_GAP_AGENTS_MIN, whole));
}

/**
 * The message for a refused settings request. 404 and 409 get their own words
 * because they mean different things to the owner: no endpoint to talk to, or
 * someone else's save landed first. Null when the reply is not a document.
 */
export function settingsErrorMessage(status: number, detail: string | undefined): string {
  if (status === 404) return "This build has no settings endpoint.";
  if (status === 409) return "The settings changed elsewhere; reload them before saving.";
  if (detail !== undefined && detail !== "") return detail;
  return `The bridge refused the request (${String(status)}).`;
}

/**
 * Read a settings reply. Null when it is not a settings document — the caller
 * says so instead of showing an empty form. A `max_gap_agents` outside the
 * legal range is clamped rather than trusted.
 */
export function parseSettingsDocument(value: unknown): SettingsDocument | null {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return null;
  const record = value as Record<string, unknown>;
  const revision = record["revision"];
  if (typeof revision !== "number" || !Number.isFinite(revision)) return null;
  const base: SettingsDocument = { ...record, revision };
  const limit = record["max_gap_agents"];
  if (typeof limit === "number" && Number.isFinite(limit)) {
    return { ...base, max_gap_agents: clampMaxGapAgents(limit) };
  }
  return base;
}

type SettingsResult =
  | { ok: true; settings: SettingsDocument }
  | { ok: false; message: string };

/** The document with the gap-agent limit replaced; the rest is untouched. */
export function withMaxGapAgents(document: SettingsDocument, value: number): SettingsDocument {
  return { ...document, max_gap_agents: clampMaxGapAgents(value) };
}

/** Read the current settings, or the message to show instead. */
export async function loadSettings(): Promise<SettingsResult> {
  try {
    const res = await apiFetch("/api/settings");
    if (!res.ok) {
      const detail = (await res.json().catch(() => null)) as { error?: string } | null;
      return { ok: false, message: settingsErrorMessage(res.status, detail?.error) };
    }
    const settings = parseSettingsDocument(await res.json());
    if (settings === null) {
      return { ok: false, message: "The settings reply was not a settings document." };
    }
    return { ok: true, settings };
  } catch {
    return { ok: false, message: "The binary did not answer." };
  }
}

/**
 * Save the gap-agent limit. The whole document goes back, because the endpoint
 * takes a document and not a patch: a partial write would need per-field merge
 * rules that drift from the ones `plan-forge config` applies.
 */
export async function saveMaxGapAgents(
  document: SettingsDocument,
  value: number,
): Promise<SettingsResult> {
  try {
    const res = await apiFetch("/api/settings", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(withMaxGapAgents(document, value)),
    });
    if (!res.ok) {
      const detail = (await res.json().catch(() => null)) as { error?: string } | null;
      return { ok: false, message: settingsErrorMessage(res.status, detail?.error) };
    }
    const settings = parseSettingsDocument(await res.json());
    if (settings === null) {
      return {
        ok: false,
        message: "The settings were saved but the reply could not be read; reopen this panel.",
      };
    }
    return { ok: true, settings };
  } catch {
    return { ok: false, message: "The binary did not answer; nothing was saved." };
  }
}
