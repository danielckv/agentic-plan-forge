/**
 * The model catalogue the loopback bridge serves and the model pins the
 * settings document carries.
 *
 * `GET /api/models` answers one entry per API host: the provider's live list,
 * or the built-in catalogue marked stale when the live list cannot be
 * fetched. The pins come from the same settings document the Runtime Limits
 * section edits (`default_main_model`, `default_judge_model`, `role_models`),
 * so the Models tab can mark which catalogue entries are the current
 * selections without inventing a second source of truth.
 */

import { apiFetch } from "../protocol/bridge";
import { settingsErrorMessage } from "./settings";
import type { SettingsDocument } from "./settings";

/** One selectable model, as a host lists it (`host.ModelRef`). */
export interface ModelRef {
  id: string;
  label: string;
}

/**
 * One host's selectable models (`app.modelCatalogEntry`). `models` is the
 * live list; when it could not be fetched, `fallback` holds the built-in
 * catalogue and `stale` is true, so the window labels the list as unverified
 * instead of certifying availability.
 */
export interface ModelCatalogEntry {
  host: string;
  label: string;
  models: ModelRef[];
  fallback?: ModelRef[];
  stale?: boolean;
  error?: string;
}

function parseModelRef(value: unknown): ModelRef | null {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return null;
  const record = value as Record<string, unknown>;
  if (typeof record["id"] !== "string" || typeof record["label"] !== "string") return null;
  return { id: record["id"], label: record["label"] };
}

function parseModelRefList(value: unknown): ModelRef[] | null {
  // Go marshals an empty slice as null; that is a valid empty list, not a
  // malformed reply.
  if (value === null || value === undefined) return [];
  if (!Array.isArray(value)) return null;
  const refs: ModelRef[] = [];
  for (const item of value) {
    const ref = parseModelRef(item);
    if (ref === null) return null;
    refs.push(ref);
  }
  return refs;
}

/**
 * Read a catalogue reply. Null when it is not a catalogue array — the caller
 * says so instead of showing an empty panel.
 */
export function parseModelCatalog(value: unknown): ModelCatalogEntry[] | null {
  if (!Array.isArray(value)) return null;
  const entries: ModelCatalogEntry[] = [];
  for (const item of value) {
    if (typeof item !== "object" || item === null || Array.isArray(item)) return null;
    const record = item as Record<string, unknown>;
    if (typeof record["host"] !== "string" || typeof record["label"] !== "string") return null;
    const models = parseModelRefList(record["models"]);
    if (models === null) return null;
    const fallback = parseModelRefList(record["fallback"]);
    if (fallback === null) return null;
    const entry: ModelCatalogEntry = { host: record["host"], label: record["label"], models };
    if (fallback.length > 0) entry.fallback = fallback;
    if (record["stale"] === true) entry.stale = true;
    if (typeof record["error"] === "string" && record["error"] !== "") entry.error = record["error"];
    entries.push(entry);
  }
  return entries;
}

/** One model pin from the settings document, with the host it applies to. */
export interface ModelPin {
  /** The settings key the pin comes from: default_main_model, or role_models.pm. */
  key: string;
  /** How the row reads in the list. */
  label: string;
  /** The host the pin applies to; empty when no host is pinned alongside. */
  host: string;
  /** The pinned model id; empty lets the host pick its own default. */
  model: string;
}

function asString(value: unknown): string {
  return typeof value === "string" ? value : "";
}

/**
 * The model pins of a settings document, in display order: the two defaults
 * first, then every pinned role. A role pinned to a host with no model is
 * listed too — it is a selection ("this host, its own default"), just not one
 * that marks a catalogue row.
 */
export function modelPins(settings: SettingsDocument | null): ModelPin[] {
  if (settings === null) return [];
  const pins: ModelPin[] = [];
  const mainModel = asString(settings["default_main_model"]);
  if (mainModel !== "") {
    pins.push({ key: "default_main_model", label: "Default main model", host: asString(settings["main_host"]), model: mainModel });
  }
  const judgeModel = asString(settings["default_judge_model"]);
  if (judgeModel !== "") {
    pins.push({ key: "default_judge_model", label: "Default judge model", host: asString(settings["judge_host"]), model: judgeModel });
  }
  const roles = settings["role_models"];
  if (typeof roles === "object" && roles !== null && !Array.isArray(roles)) {
    for (const [role, binding] of Object.entries(roles as Record<string, unknown>)) {
      if (typeof binding !== "object" || binding === null || Array.isArray(binding)) continue;
      const record = binding as Record<string, unknown>;
      const host = asString(record["host"]);
      const model = asString(record["model"]);
      if (host === "" && model === "") continue;
      pins.push({ key: `role_models.${role}`, label: `Role: ${role}`, host, model });
    }
  }
  return pins;
}

/**
 * True when a pin selects this model on this host. A pin with no host matches
 * the id on every host, because the settings document did not name one.
 */
export function pinSelects(pin: ModelPin, host: string, modelId: string): boolean {
  return pin.model !== "" && pin.model === modelId && (pin.host === "" || pin.host === host);
}

export type ModelsResult =
  | { ok: true; catalogue: ModelCatalogEntry[] }
  | { ok: false; message: string };

/** Read the model catalogue, or the message to show instead. */
export async function loadModels(): Promise<ModelsResult> {
  try {
    const res = await apiFetch("/api/models");
    if (!res.ok) {
      const detail = (await res.json().catch(() => null)) as { error?: string } | null;
      return { ok: false, message: settingsErrorMessage(res.status, detail?.error) };
    }
    const catalogue = parseModelCatalog(await res.json());
    if (catalogue === null) {
      return { ok: false, message: "The models reply was not a catalogue." };
    }
    return { ok: true, catalogue };
  } catch {
    return { ok: false, message: "The binary did not answer." };
  }
}
