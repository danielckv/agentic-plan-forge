/**
 * The Build page's data: what the binary reports about the post-approval
 * worktree, and the calls that drive it.
 *
 * Every field here mirrors Go `delivery.View`. The stage is read straight from
 * the binary and never derived on this side: Page 2 learned what a derived
 * status costs when the synthesizer sat on "Queued" forever (INV-7), and a
 * build has more ways to be between states than a deliberation does.
 */

import { useCallback, useEffect, useRef, useState } from "react";

import { apiFetch } from "../protocol/bridge";

type BuildStage =
  | "IDLE"
  | "CODING"
  | "CODED"
  | "BUILDING"
  | "PREVIEWING"
  | "DEPLOYING"
  | "DEPLOYED"
  | "FAILED";

interface CoderStep {
  at: string;
  text: string;
}

export interface FileChange {
  path: string;
  status: string;
  added: number;
  removed: number;
  binary: boolean;
}

interface Changes {
  branch: string;
  base: string;
  files: FileChange[];
  diff: string;
  truncated: string[];
  added: number;
  removed: number;
}

interface Box {
  image: string;
  container?: string;
  url?: string;
  host_port?: number;
  port?: number;
}

interface Deployment {
  target: string;
  url?: string;
  detail?: string;
  at: string;
  image?: string;
  provider?: string;
}

export interface BuildView {
  stage: BuildStage;
  approval?: string;
  worktree?: string;
  branch?: string;
  steps: CoderStep[];
  log: string[];
  changes?: Changes;
  box?: Box;
  box_running: boolean;
  deployment?: Deployment;
  target: string;
  target_ready?: string;
  error?: string;
  finished: boolean;
  busy: boolean;
  updated_at: string;
}

interface DeployTargetOption {
  id: string;
  name: string;
  ok: boolean;
  why?: string;
}

export interface DeployTargetView {
  target: string;
  box_url: string;
  options: DeployTargetOption[];
  /** False until the owner has chosen once: the window then asks. */
  asked: boolean;
}

export interface FileContent {
  path: string;
  text: string;
  bytes: number;
  truncated: boolean;
  binary: boolean;
}

/** Stages during which the binary is working and the buttons stand down. */
const RUNNING: ReadonlySet<BuildStage> = new Set<BuildStage>(["CODING", "BUILDING", "DEPLOYING"]);

export function isRunning(view: BuildView | null): boolean {
  return view !== null && (view.busy || RUNNING.has(view.stage));
}

/** One-line description of where the build is, for the page header. */
export function stageLabel(view: BuildView | null): string {
  if (view === null) return "Loading";
  switch (view.stage) {
    case "IDLE":
      return "Approved — ready to code";
    case "CODING":
      return "Writing code in the worktree";
    case "CODED":
      return "Code written — review it";
    case "BUILDING":
      return "Building the container";
    case "PREVIEWING":
      return view.box_running ? "Preview running" : "Preview container stopped";
    case "DEPLOYING":
      return "Deploying";
    case "DEPLOYED":
      return "Deployed";
    case "FAILED":
      return view.error ?? "Failed";
  }
}

/**
 * Poll the build state. The interval is slow while nothing is happening and
 * fast while the binary is working, so a two-hour coding run shows live
 * progress without a request per second for the hours either side of it.
 */
export function useBuild(enabled: boolean): {
  view: BuildView | null;
  refresh: () => Promise<void>;
} {
  const [view, setView] = useState<BuildView | null>(null);
  const viewRef = useRef<BuildView | null>(null);

  const refresh = useCallback(async (): Promise<void> => {
    try {
      const res = await apiFetch("/api/build");
      if (!res.ok) return;
      const body = (await res.json()) as BuildView;
      viewRef.current = body;
      setView(body);
    } catch {
      /* the binary is away: the last known state stays on screen */
    }
  }, []);

  useEffect(() => {
    if (!enabled) return;
    let cancelled = false;
    let timer = 0;
    const tick = async (): Promise<void> => {
      if (cancelled) return;
      await refresh();
      if (cancelled) return;
      timer = window.setTimeout(() => void tick(), isRunning(viewRef.current) ? 1000 : 5000);
    };
    void tick();
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [enabled, refresh]);

  return { view, refresh };
}

/** POST a build action and return the error message, or null on success. */
export async function postBuild(path: string, body?: unknown): Promise<string | null> {
  try {
    const init: RequestInit =
      body === undefined
        ? { method: "POST" }
        : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) };
    const res = await apiFetch(path, init);
    if (res.ok) return null;
    const detail = (await res.json().catch(() => null)) as { error?: string } | null;
    return detail?.error ?? `request failed (${String(res.status)})`;
  } catch {
    return "the binary did not answer";
  }
}

export async function fetchDeployTarget(): Promise<DeployTargetView | null> {
  try {
    const res = await apiFetch("/api/deploy/target");
    if (!res.ok) return null;
    return (await res.json()) as DeployTargetView;
  } catch {
    return null;
  }
}

export async function fetchWorktreeFile(path: string): Promise<FileContent | null> {
  try {
    const res = await apiFetch(`/api/build/file?path=${encodeURIComponent(path)}`);
    if (!res.ok) return null;
    return (await res.json()) as FileContent;
  } catch {
    return null;
  }
}

/** `+12 −3`, or `binary` when git counted no lines. */
export function changeCount(file: FileChange): string {
  if (file.binary) return "binary";
  return `+${String(file.added)} −${String(file.removed)}`;
}
