/**
 * Post-approval handoff (contract C8).
 *
 * `/api/status` reports `requestedHost`, the host the invoking skill passed
 * with `--host`. Page 3 words its call to action and the handoff panel for that
 * host. Only Claude Code and oh-my-pi have a managed plan-mode entry; every
 * other value gets the generic wording.
 */

import { useEffect, useState } from "react";

/** Matches Go `window.PostApprovalClose` (5 s): the binary closes the window. */
export const POST_APPROVAL_CLOSE_SECONDS = 5;

export type HostKind = "claude" | "omp" | "other";

export function hostKind(requestedHost: string | undefined): HostKind {
  const h = (requestedHost ?? "").trim().toLowerCase();
  return h === "claude" || h === "omp" ? h : "other";
}

const HOST_NAME: Record<HostKind, string> = {
  claude: "Claude Code",
  omp: "oh-my-pi",
  other: "Your agent",
};

/** Subject of "<Host> enters Plan Mode". */
export function hostName(requestedHost: string | undefined): string {
  return HOST_NAME[hostKind(requestedHost)];
}

const APPROVE_LABEL: Record<HostKind, string> = {
  claude: "Approve & open in Claude Code Plan Mode",
  omp: "Approve & open in oh-my-pi Plan Mode",
  other: "Approve & hand off to agent Plan Mode",
};

export function approveLabel(requestedHost: string | undefined): string {
  return APPROVE_LABEL[hostKind(requestedHost)];
}

const APPROVE_HINT: Record<HostKind, string> = {
  claude: "Approving freezes the spec and hands it to Claude Code's Plan Mode.",
  omp: "Approving freezes the spec and opens a new oh-my-pi session in Plan Mode.",
  other: "Approving freezes the spec and hands it to the agent's Plan Mode.",
};

export function approveHint(requestedHost: string | undefined): string {
  return APPROVE_HINT[hostKind(requestedHost)];
}

export type HandoffStepState = "done" | "current" | "upcoming";

export interface HandoffStep {
  id: "approved" | "plan-mode" | "validate";
  label: string;
  /** Monospace command shown after the label. */
  command?: string;
  state: HandoffStepState;
}

/**
 * The window cannot observe the host, so only the approval itself is "done":
 * the host entering Plan Mode is the step in flight and validation follows it.
 */
export function handoffSteps(requestedHost: string | undefined): HandoffStep[] {
  return [
    { id: "approved", label: "Approved", state: "done" },
    { id: "plan-mode", label: `${hostName(requestedHost)} enters Plan Mode`, state: "current" },
    {
      id: "validate",
      label: "Validation runs automatically",
      command: "plan-forge validate",
      state: "upcoming",
    },
  ];
}

/** Whole seconds left, rounded up so "1 s" shows until the last moment. */
export function remainingSeconds(startedAtMs: number, nowMs: number, totalSeconds: number): number {
  const left = totalSeconds - (nowMs - startedAtMs) / 1000;
  return Math.max(0, Math.ceil(left));
}

export interface CountdownTimers {
  set(fn: () => void, ms: number): unknown;
  clear(handle: unknown): void;
}

export interface CountdownOptions {
  seconds?: number;
  now?: () => number;
  timers?: CountdownTimers;
  tickMs?: number;
  /** Called whenever the displayed second changes, including the final 0. */
  onChange: (remaining: number) => void;
}

export interface Countdown {
  start(): void;
  stop(): void;
}

const realTimers: CountdownTimers = {
  set: (fn, ms) => setInterval(fn, ms),
  clear: (handle) => clearInterval(handle as number),
};

/**
 * A drift-free countdown: each tick recomputes from the clock, so a throttled
 * background tab shows the right number when it wakes. Emits only on change.
 */
export function createCountdown(options: CountdownOptions): Countdown {
  const seconds = options.seconds ?? POST_APPROVAL_CLOSE_SECONDS;
  const now = options.now ?? Date.now;
  const timers = options.timers ?? realTimers;
  const tickMs = options.tickMs ?? 250;
  let handle: unknown;
  let last = -1;

  const stop = (): void => {
    if (handle === undefined) return;
    timers.clear(handle);
    handle = undefined;
  };

  return {
    start() {
      stop();
      const startedAt = now();
      const emit = (): void => {
        const remaining = remainingSeconds(startedAt, now(), seconds);
        if (remaining !== last) {
          last = remaining;
          options.onChange(remaining);
        }
        if (remaining === 0) stop();
      };
      last = -1;
      emit();
      if (last !== 0) handle = timers.set(emit, tickMs);
    },
    stop,
  };
}

/**
 * Seconds until the window closes, counting down from `seconds` once `active`
 * turns true; `null` while inactive.
 */
export function useCountdown(active: boolean, seconds: number = POST_APPROVAL_CLOSE_SECONDS): number | null {
  const [remaining, setRemaining] = useState<number | null>(null);
  useEffect(() => {
    if (!active) return;
    const countdown = createCountdown({ seconds, onChange: setRemaining });
    countdown.start();
    return () => countdown.stop();
  }, [active, seconds]);
  return active ? remaining : null;
}
