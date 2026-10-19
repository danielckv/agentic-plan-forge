import { describe, expect, it, vi } from "vitest";

import {
  approveHint,
  approveLabel,
  createCountdown,
  handoffSteps,
  hostKind,
  hostName,
  POST_APPROVAL_CLOSE_SECONDS,
  remainingSeconds,
} from "./handoff";
import type { CountdownTimers } from "./handoff";

describe("host label mapping (contract C8)", () => {
  it("maps requestedHost to a host kind, case- and space-insensitively", () => {
    expect(hostKind("claude")).toBe("claude");
    expect(hostKind(" Claude ")).toBe("claude");
    expect(hostKind("omp")).toBe("omp");
    expect(hostKind("OMP")).toBe("omp");
    for (const other of ["agy", "auto", "all", "", "gemini", undefined]) {
      expect(hostKind(other)).toBe("other");
    }
  });

  it("words the call to action for each host", () => {
    expect(approveLabel("claude")).toBe("Approve & open in Claude Code Plan Mode");
    expect(approveLabel("omp")).toBe("Approve & open in oh-my-pi Plan Mode");
    expect(approveLabel("agy")).toBe("Approve & hand off to agent Plan Mode");
    expect(approveLabel(undefined)).toBe("Approve & hand off to agent Plan Mode");
  });

  it("names the host in the handoff panel", () => {
    expect(hostName("claude")).toBe("Claude Code");
    expect(hostName("omp")).toBe("oh-my-pi");
    expect(hostName("agy")).toBe("Your agent");
    expect(hostName(undefined)).toBe("Your agent");
  });

  it("explains approval in the host's terms", () => {
    expect(approveHint("claude")).toContain("Claude Code");
    expect(approveHint("omp")).toContain("oh-my-pi");
    expect(approveHint("agy")).toContain("agent");
  });

  it("lists the three handoff steps with the approval already done", () => {
    const steps = handoffSteps("claude");
    expect(steps.map((s) => s.label)).toEqual([
      "Approved",
      "Claude Code enters Plan Mode",
      "Validation runs automatically",
    ]);
    expect(steps.map((s) => s.state)).toEqual(["done", "current", "upcoming"]);
    expect(steps[2]?.command).toBe("plan-forge validate");
    expect(handoffSteps("omp")[1]?.label).toBe("oh-my-pi enters Plan Mode");
    expect(handoffSteps("agy")[1]?.label).toBe("Your agent enters Plan Mode");
  });
});

describe("close countdown", () => {
  it("matches Go window.PostApprovalClose (5 s)", () => {
    expect(POST_APPROVAL_CLOSE_SECONDS).toBe(5);
  });

  it("rounds up so 1 s shows until the last moment, and never goes negative", () => {
    expect(remainingSeconds(0, 0, 5)).toBe(5);
    expect(remainingSeconds(0, 1, 5)).toBe(5);
    expect(remainingSeconds(0, 999, 5)).toBe(5);
    expect(remainingSeconds(0, 1000, 5)).toBe(4);
    expect(remainingSeconds(0, 4001, 5)).toBe(1);
    expect(remainingSeconds(0, 5000, 5)).toBe(0);
    expect(remainingSeconds(0, 60_000, 5)).toBe(0);
  });

  /** A manual clock and timer: advance() moves time and fires due ticks. */
  function manual(): { now: () => number; timers: CountdownTimers; advance: (ms: number) => void; active: () => boolean } {
    let t = 1000;
    let tick: (() => void) | undefined;
    let every = 0;
    return {
      now: () => t,
      timers: {
        set(fn, ms) {
          tick = fn;
          every = ms;
          return 1;
        },
        clear() {
          tick = undefined;
        },
      },
      advance(ms) {
        const end = t + ms;
        while (t < end) {
          t = Math.min(end, t + every);
          tick?.();
        }
      },
      active: () => tick !== undefined,
    };
  }

  it("counts 5, 4, 3, 2, 1, 0 and then stops ticking", () => {
    const clock = manual();
    const seen: number[] = [];
    const countdown = createCountdown({ now: clock.now, timers: clock.timers, tickMs: 250, onChange: (n) => seen.push(n) });

    countdown.start();
    expect(seen).toEqual([5]);
    clock.advance(5000);
    expect(seen).toEqual([5, 4, 3, 2, 1, 0]);
    expect(clock.active()).toBe(false);
  });

  it("emits only when the displayed second changes", () => {
    const clock = manual();
    const onChange = vi.fn();
    createCountdown({ now: clock.now, timers: clock.timers, tickMs: 250, onChange }).start();
    clock.advance(900); // three ticks inside the first second
    expect(onChange).toHaveBeenCalledTimes(1);
  });

  it("stop() cancels the timer", () => {
    const clock = manual();
    const onChange = vi.fn();
    const countdown = createCountdown({ now: clock.now, timers: clock.timers, onChange });
    countdown.start();
    countdown.stop();
    expect(clock.active()).toBe(false);
    clock.advance(10_000);
    expect(onChange).toHaveBeenCalledTimes(1);
  });

  it("start() again begins a fresh countdown", () => {
    const clock = manual();
    const seen: number[] = [];
    const countdown = createCountdown({ seconds: 3, now: clock.now, timers: clock.timers, onChange: (n) => seen.push(n) });
    countdown.start();
    clock.advance(2000);
    countdown.start();
    expect(seen.at(-1)).toBe(3);
  });

  it("computes from the clock, so a throttled tab catches up in one tick", () => {
    let t = 0;
    let tick: (() => void) | undefined;
    const seen: number[] = [];
    createCountdown({
      now: () => t,
      timers: { set: (fn) => ((tick = fn), 1), clear: () => (tick = undefined) },
      onChange: (n) => seen.push(n),
    }).start();
    t = 3600; // the tab slept; one late tick
    tick?.();
    expect(seen).toEqual([5, 2]);
  });
});
