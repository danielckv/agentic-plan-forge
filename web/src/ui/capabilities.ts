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
import type { SkillScanReport } from "./skills";

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

/**
 * One HTTP generation provider's credential state (`app.ProviderStatus`).
 *
 * Never carries the key. `masked` is enough to tell two keys apart and useless
 * to anyone reading over a shoulder; `source` matters because a stored key
 * that an environment variable is overriding is the state owners misread most.
 */
export interface ProviderStatus {
  name: string;
  label: string;
  configured: boolean;
  source?: string;
  masked?: string;
  env_var: string;
  console_url: string;
  base_url?: string;
}

export interface CapabilitiesView {
  hosts: CapabilityHost[];
  tools: CapabilityTool[];
  skills: SkillRootView[];
  hooks: HookStatus[];
  /**
   * Coding agents installed on this machine, found at start-up. Optional so a
   * payload from an older bridge still type-checks; the helpers read `?? []`.
   */
  clis?: DetectedCLI[];
  /** `main_host = agy` and friends, when the pin names a host that cannot serve. */
  pinned: string[];
  /**
   * API providers and whether each holds a key. Optional so a payload from an
   * older bridge still type-checks.
   */
  providers?: ProviderStatus[];
  /**
   * Every skill the harnesses on this machine already have, with its
   * frontmatter description (`host.SkillScanReport`). Optional for the same
   * reason as `providers`: a payload from an older bridge omits it.
   */
  discovered_skills?: SkillScanReport;
}

/** The browser-local record that setup has been offered once. */
const SETUP_PROMPTED_KEY = "plan-forge:ui:setup-prompted";

/**
 * Whether to open setup unprompted. True only on a first run that holds no API
 * key at all, and only once per browser profile: an owner who runs on a local
 * agent CLI is not asked again, and nobody is asked twice.
 *
 * It reads `GET /api/providers`, which is one file read. The full capability
 * probe shells out to every host CLI, so it is far too expensive to run just
 * to decide whether to show a dialog.
 */
export async function shouldOfferSetup(): Promise<boolean> {
  try {
    if (window.localStorage.getItem(SETUP_PROMPTED_KEY) !== null) return false;
  } catch {
    // A browser with storage disabled simply never auto-opens.
    return false;
  }
  try {
    const res = await apiFetch("/api/providers");
    if (!res.ok) return false;
    const providers = (await res.json()) as ProviderStatus[];
    if (providers.some((p) => p.configured)) return false;
  } catch {
    return false;
  }
  try {
    window.localStorage.setItem(SETUP_PROMPTED_KEY, "1");
  } catch {
    /* asking twice is better than failing to open at all */
  }
  return true;
}

/**
 * Save one provider's key. An empty key disconnects the provider. Returns null
 * on success, else the message to show.
 *
 * The reply carries masked status only, so a key never travels back out of the
 * bridge once it has been stored.
 */
export async function saveProvider(
  provider: string,
  apiKey: string,
  baseURL: string,
): Promise<string | null> {
  try {
    const res = await apiFetch("/api/providers", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ provider, api_key: apiKey, base_url: baseURL }),
    });
    if (res.ok) return null;
    const detail = (await res.json().catch(() => null)) as { error?: string } | null;
    return detail?.error ?? `could not save the key (${res.status})`;
  } catch {
    return "the binary did not answer";
  }
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

type HostTone = "serves" | "blocked" | "signed-out" | "absent";

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
