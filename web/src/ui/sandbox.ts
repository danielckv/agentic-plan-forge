/**
 * The prepared-VM tab of the build page.
 *
 * Everything the page shows comes from two answers: the recorded execution
 * recipe (the commands the sandbox will run) and the launch state. A launch is
 * an explicit, paid action, so the confirmation names the revision, the
 * commands, the expiry and the cost estimate before anything is created.
 *
 * Mirrors Go `app.RecipeView` and `app.LaunchReport`.
 */

import { useCallback, useEffect, useState } from "react";

import { apiFetch } from "../protocol/bridge";

/** One command a prepared sandbox runs (`recipe.Command`). */
export interface RecipeCommand {
  id: string;
  argv: string[];
  dir: string;
  timeout_seconds: number;
  env?: string[];
  source_ref?: string;
}

/** GET /api/recipe (`app.RecipeView`). */
export interface RecipeView {
  available: boolean;
  reused: boolean;
  stored?: string;
  problem?: string;
  /** SHA-256 of the recipe the owner reviewed; a launch pins it. */
  hash?: string;
  recipe?: {
    schema_version: string;
    revision: string;
    build: RecipeCommand;
    test: RecipeCommand;
    run: RecipeCommand;
    runtime_port: number;
    readiness_probe: string[];
    toolchains: string[];
    template: string;
    lockfiles?: Array<{ path: string; sha256: string }>;
    scripts?: Array<{ path: string; sha256: string; generated: boolean }>;
  };
}

/** One recorded command's outcome (`sandbox.Receipt`). */
export interface SandboxReceipt {
  Kind: string;
  Command: string;
  ExitCode: number;
}

/** GET/POST /api/sandbox (`app.LaunchReport`). */
export interface LaunchReport {
  state: string;
  sandboxId?: string;
  expiresAt?: string;
  preview?: string;
  problem?: string;
  cleanup?: string;
  receipts?: SandboxReceipt[];
}

/** The states in which the sandbox is doing something or can be used. */
const LIVE: Record<string, true> = { preparing: true, building: true, testing: true, starting: true, ready: true };

/** True while the sandbox exists and may be shown. */
export function sandboxLive(report: LaunchReport | null): boolean {
  return report !== null && LIVE[report.state] === true;
}

/** A preview opens only when the sandbox is ready: not merely created. */
export function previewAvailable(report: LaunchReport | null): boolean {
  return report !== null && report.state === "ready" && (report.preview ?? "") !== "";
}

/** The one line the tab leads with. */
export function sandboxHeadline(report: LaunchReport | null, recipe: RecipeView | null): string {
  if (report === null) return recipe?.available === false ? "No command to run yet" : "Not launched";
  if (report.problem !== undefined && report.problem !== "") return report.problem;
  switch (report.state) {
    case "setup":
      return "The prepared VM needs setup first";
    case "preparing":
      return "Preparing the sandbox";
    case "building":
      return "Building from the recorded commands";
    case "testing":
      return "Running the recorded tests";
    case "starting":
      return "Starting the application";
    case "ready":
      return "The application is running";
    case "failed":
      return "The last step failed";
    case "stopped":
      return "The sandbox is stopped";
    case "expired":
      return "The sandbox expired";
    case "unknown":
      return "The sandbox state is unknown";
    default:
      return report.state;
  }
}

/** A build or test step that did not pass, so the page never shows a false pass. */
export function failingReceipts(report: LaunchReport | null): SandboxReceipt[] {
  return (report?.receipts ?? []).filter((r) => r.ExitCode !== 0);
}

/**
 * What a launch would cost, from the estimate the Go side computed. A missing
 * estimate says so rather than showing zero.
 */
export function costLine(estimate: { lifetime: string; amount?: number } | null): string {
  if (estimate === null) return "Cost estimate unavailable";
  if (estimate.amount === undefined) return `${estimate.lifetime} · cost estimate unavailable`;
  return `${estimate.lifetime} · about $${estimate.amount.toFixed(2)}`;
}

export function useRecipe(enabled: boolean): { view: RecipeView | null; refresh: () => Promise<void> } {
  const [view, setView] = useState<RecipeView | null>(null);
  const refresh = useCallback(async () => {
    try {
      const res = await apiFetch("/api/recipe");
      if (!res.ok) return;
      setView((await res.json()) as RecipeView);
    } catch {
      // Keep the last good answer: a blip must not empty the panel.
    }
  }, []);
  useEffect(() => {
    if (!enabled) return;
    void refresh();
  }, [enabled, refresh]);
  return { view, refresh };
}

export function useSandbox(enabled: boolean): {
  report: LaunchReport | null;
  refresh: () => Promise<void>;
  launch: (body: Record<string, unknown>) => Promise<string | null>;
  stop: () => Promise<string | null>;
} {
  const [report, setReport] = useState<LaunchReport | null>(null);
  const refresh = useCallback(async () => {
    try {
      const res = await apiFetch("/api/sandbox");
      if (!res.ok) return;
      setReport((await res.json()) as LaunchReport);
    } catch {
      // Keep the last good answer.
    }
  }, []);
  const post = useCallback(async (path: string, body?: unknown): Promise<string | null> => {
    try {
      const res = await apiFetch(path, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
      });
      if (res.ok) {
        setReport((await res.json()) as LaunchReport);
        return null;
      }
      const payload = (await res.json().catch(() => ({}))) as { error?: string };
      return payload.error ?? `the request answered ${String(res.status)}`;
    } catch (err) {
      return err instanceof Error ? err.message : "the request failed";
    }
  }, []);
  useEffect(() => {
    if (!enabled) return;
    void refresh();
  }, [enabled, refresh]);
  return {
    report,
    refresh,
    launch: (body) => post("/api/sandbox/launch", body),
    stop: () => post("/api/sandbox/stop"),
  };
}
