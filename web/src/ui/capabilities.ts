/**
 * What this installation can do: the generation hosts, the external tools, the
 * installed skills and the managed hooks.
 *
 * Mirrors Go `app.CapabilitiesView`. All of it was already computed for
 * `plan-forge doctor` and printed to a terminal nobody writing a brief is
 * looking at — which is where "agy is signed in but cannot serve a call" sat
 * while every avatar quietly went elsewhere.
 */

import { useCallback, useEffect, useState } from "react";

import { apiFetch } from "../protocol/bridge";

export interface CapabilityHost {
  host: string;
  found: boolean;
  version?: string;
  ready: boolean;
  method?: string;
  sign_in?: string;
  /** False when the host is signed in and still cannot take a call. */
  serves: boolean;
  blocks?: string;
  models: number;
}

export interface CapabilityTool {
  name: string;
  purpose: string;
  found: boolean;
  path?: string;
  remedy?: string;
  essential: boolean;
}

export interface SkillRootView {
  host: string;
  scope?: string;
  path: string;
  skills?: string[];
  duplicate?: boolean;
}

export interface HookStatus {
  host: string;
  path: string;
  state: string;
  remedy?: string;
}

/** One coding agent found by the first-run scan (`host.DetectedCLI`). */
export interface DetectedCLI {
  name: string;
  binaries: string[];
  found: boolean;
  /** A path on the owner's machine; shown only through the loopback bridge. */
  path?: string;
  /** The adapter kind, when Plan Forge can use this CLI as a host. */
  kind?: string;
  purpose: string;
  /** How the CLI is integrated when it has no adapter yet. */
  supports?: string;
  /** True only for a host whose preflight passes. */
  usable: boolean;
  blocks?: string;
}

export interface CapabilitiesView {
  hosts: CapabilityHost[];
  tools: CapabilityTool[];
  skills: SkillRootView[];
  hooks: HookStatus[];
  /** Coding agents installed on this machine, found at start-up. */
  clis: DetectedCLI[];
  /** `main_host = agy` and friends, when the pin names a host that cannot serve. */
  pinned: string[];
}

/** The detected agents that can actually take a call. */
export function usableCLIs(view: CapabilitiesView | null): DetectedCLI[] {
  return (view?.clis ?? []).filter((c) => c.usable);
}

/**
 * Detected agents Plan Forge cannot use yet. They are worth showing: the owner
 * has the tool installed, and the row says why a plan will not run on it.
 */
export function unsupportedCLIs(view: CapabilitiesView | null): DetectedCLI[] {
  return (view?.clis ?? []).filter((c) => c.found && !c.usable);
}

/**
 * The one-line answer for the first run: which agent will run the stages, or
 * what to install. Empty when the list has not loaded.
 */
export function cliSummary(view: CapabilitiesView | null): string {
  if (view === null) return "";
  const usable = usableCLIs(view);
  if (usable.length > 0) {
    return usable.length === 1
      ? `Plans will run on ${usable[0]!.name}.`
      : `Plans can run on ${usable.map((c) => c.name).join(", ")}.`;
  }
  const installed = unsupportedCLIs(view);
  if (installed.length > 0) {
    return `No coding agent Plan Forge can call yet. ${installed.length} installed tool${installed.length === 1 ? " is" : "s are"} not supported.`;
  }
  return "No coding agent found. Install Claude Code, oh-my-pi, or agy to run a plan.";
}

export type HostTone = "serves" | "blocked" | "signed-out" | "absent";

/**
 * One host's state as the window shows it. "blocked" is the case worth having
 * a word for: found, signed in, and still unable to take a call.
 */
export function hostTone(h: CapabilityHost): HostTone {
  if (!h.found) return "absent";
  if (h.serves) return "serves";
  if (h.ready) return "blocked";
  return "signed-out";
}

export const HOST_TONE_LABEL: Record<HostTone, string> = {
  serves: "Serving",
  blocked: "Cannot serve",
  "signed-out": "Not signed in",
  absent: "Not installed",
};

/** The line under a host name: the reason, in the host's own words. */
export function hostDetail(h: CapabilityHost): string {
  switch (hostTone(h)) {
    case "serves":
      return `${String(h.models)} models · ${h.method ?? "ready"}`;
    case "blocked":
      return h.blocks ?? "preflight failed";
    case "signed-out":
      return h.sign_in ?? "not authenticated";
    case "absent":
      return "not on PATH";
  }
}

/**
 * How many hosts can actually take a call. The run needs one; the spread
 * across roles needs more than one.
 */
export function servingCount(view: CapabilitiesView | null): number {
  return (view?.hosts ?? []).filter((h) => h.serves).length;
}

/**
 * The headline. It states the number rather than a verdict: "1 of 3 hosts can
 * serve" is a fact the owner can act on, where "degraded" is a label they
 * would have to decode.
 */
export function capabilitySummary(view: CapabilitiesView | null): string {
  if (view === null) return "Checking…";
  const serving = servingCount(view);
  const total = view.hosts.length;
  if (serving === 0) return "No host can serve a call. A run cannot start.";
  if (serving === total) return `All ${String(total)} hosts can serve.`;
  return `${String(serving)} of ${String(total)} hosts can serve.`;
}

/** Tools that are missing, with what each one would have enabled. */
export function missingTools(view: CapabilitiesView | null): CapabilityTool[] {
  return (view?.tools ?? []).filter((t) => !t.found);
}

export function useCapabilities(open: boolean): {
  view: CapabilitiesView | null;
  refresh: () => Promise<void>;
} {
  const [view, setView] = useState<CapabilitiesView | null>(null);

  const refresh = useCallback(async (): Promise<void> => {
    try {
      const res = await apiFetch("/api/capabilities");
      if (!res.ok) return;
      setView((await res.json()) as CapabilitiesView);
    } catch {
      /* the binary is away: the last known answer stays on screen */
    }
  }, []);

  // Probing shells out to every host CLI, so it runs when the panel opens and
  // on an explicit refresh, never on a timer.
  useEffect(() => {
    if (open) void refresh();
  }, [open, refresh]);

  return { view, refresh };
}
