/**
 * What this session has asked each generation host to do.
 *
 * The server counts one row per host, model and role. The room shows two
 * readings of the same rows: per vendor, which answers "where did the tokens
 * go", and per role, which answers "which avatar spent them". Both live here
 * so they can be tested without a browser.
 */

import type { UsageReport, UsageRow } from "../protocol/api";

/** One host's share of the session, with the models it served. */
export interface VendorUsage {
  host: string;
  models: string[];
  requests: number;
  failures: number;
  inputTokens: number;
  outputTokens: number;
  milliseconds: number;
}

function addInto(target: Omit<VendorUsage, "host" | "models">, row: UsageRow): void {
  target.requests += row.requests;
  target.failures += row.failures;
  target.inputTokens += row.input_tokens;
  target.outputTokens += row.output_tokens;
  target.milliseconds += row.milliseconds;
}

/** The rows summed per host, busiest first. */
export function byVendor(report: UsageReport): VendorUsage[] {
  const out = new Map<string, VendorUsage>();
  for (const row of report.rows) {
    let v = out.get(row.host);
    if (v === undefined) {
      v = { host: row.host, models: [], requests: 0, failures: 0, inputTokens: 0, outputTokens: 0, milliseconds: 0 };
      out.set(row.host, v);
    }
    if (!v.models.includes(row.model)) v.models.push(row.model);
    addInto(v, row);
  }
  return [...out.values()].sort((a, b) => b.requests - a.requests || a.host.localeCompare(b.host));
}

/** One role's line, for the per-avatar reading. */
export interface RoleUsage {
  role: string;
  requests: number;
  failures: number;
  inputTokens: number;
  outputTokens: number;
  milliseconds: number;
}

/** The rows summed per role, busiest first. */
export function byRole(report: UsageReport): RoleUsage[] {
  const out = new Map<string, RoleUsage>();
  for (const row of report.rows) {
    let r = out.get(row.role);
    if (r === undefined) {
      r = { role: row.role, requests: 0, failures: 0, inputTokens: 0, outputTokens: 0, milliseconds: 0 };
      out.set(row.role, r);
    }
    addInto(r, row);
  }
  return [...out.values()].sort((a, b) => b.requests - a.requests || a.role.localeCompare(b.role));
}

/** A token count at a glance: 940, 12.4k, 3.1M. */
export function formatTokens(n: number): string {
  if (n < 1000) return String(n);
  if (n < 1_000_000) return `${(n / 1000).toFixed(1)}k`;
  return `${(n / 1_000_000).toFixed(1)}M`;
}

/** A duration at a glance: 820ms, 4.2s, 3m 05s. */
export function formatDuration(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  const minutes = Math.floor(ms / 60_000);
  const seconds = Math.round((ms % 60_000) / 1000);
  return `${minutes}m ${String(seconds).padStart(2, "0")}s`;
}

/** The share of the session's requests a row holds, as a percentage 0..100. */
export function requestShare(requests: number, total: number): number {
  if (total <= 0) return 0;
  return Math.round((requests / total) * 100);
}
