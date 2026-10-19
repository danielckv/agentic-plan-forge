import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { createToastStore, DEFAULT_TOAST_TTL, TOAST_EXIT_MS, TOAST_INFO_TTL_MS } from "./toasts";
import type { ToastStore } from "./toasts";

describe("toast queue", () => {
  let store: ToastStore;

  beforeEach(() => {
    vi.useFakeTimers();
    store = createToastStore();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  const ids = (): number[] => store.getSnapshot().map((t) => t.id);

  it("defaults: info and success 6 s, warning 10 s, error sticky", () => {
    expect(TOAST_INFO_TTL_MS).toBe(6000);
    expect(DEFAULT_TOAST_TTL).toEqual({ info: 6000, success: 6000, warning: 10_000, error: null });
  });

  it("an info toast dismisses itself after 6 s, after its exit animation", () => {
    store.push("info", "Saved");
    expect(store.getSnapshot()).toHaveLength(1);

    vi.advanceTimersByTime(5999);
    expect(store.getSnapshot()[0]?.leaving).toBe(false);

    vi.advanceTimersByTime(1);
    expect(store.getSnapshot()[0]?.leaving).toBe(true); // exit animation plays
    expect(store.getSnapshot()).toHaveLength(1);

    vi.advanceTimersByTime(TOAST_EXIT_MS);
    expect(store.getSnapshot()).toHaveLength(0);
  });

  it("an error toast stays until it is dismissed", () => {
    const id = store.push("error", "ROLE_FORBIDDEN: only the owner can cancel");
    vi.advanceTimersByTime(10 * 60_000);
    expect(ids()).toEqual([id]);

    store.dismiss(id);
    vi.advanceTimersByTime(TOAST_EXIT_MS);
    expect(ids()).toEqual([]);
  });

  it("a warning outlives an info toast but not forever", () => {
    store.push("info", "a");
    store.push("warning", "b");
    vi.advanceTimersByTime(TOAST_INFO_TTL_MS + TOAST_EXIT_MS);
    expect(store.getSnapshot().map((t) => t.message)).toEqual(["b"]);
    vi.advanceTimersByTime(4000 + TOAST_EXIT_MS);
    expect(store.getSnapshot()).toHaveLength(0);
  });

  it("an identical message bumps a counter and restarts the timer instead of stacking", () => {
    const first = store.push("info", "Decision already resolved by another member");
    vi.advanceTimersByTime(4000);
    const second = store.push("info", "Decision already resolved by another member");

    expect(second).toBe(first);
    expect(store.getSnapshot()).toHaveLength(1);
    expect(store.getSnapshot()[0]?.count).toBe(2);

    // 4 s + 4 s: the original timer would have fired at 6 s; the restart holds it.
    vi.advanceTimersByTime(4000);
    expect(store.getSnapshot()[0]?.leaving).toBe(false);
    vi.advanceTimersByTime(2000);
    expect(store.getSnapshot()[0]?.leaving).toBe(true);
  });

  it("the same text at a different kind is a different toast", () => {
    store.push("info", "x");
    store.push("error", "x");
    expect(store.getSnapshot()).toHaveLength(2);
  });

  it("caps what is on screen at four, dropping the oldest", () => {
    const first = store.push("error", "one");
    for (const m of ["two", "three", "four", "five"]) store.push("error", m);
    expect(store.getSnapshot().map((t) => t.message)).toEqual(["two", "three", "four", "five"]);
    expect(ids()).not.toContain(first);
  });

  it("pausing holds the auto-dismiss; resuming restarts a short grace", () => {
    const id = store.push("info", "hover me");
    store.pause(id);
    vi.advanceTimersByTime(60_000);
    expect(store.getSnapshot()).toHaveLength(1);
    expect(store.getSnapshot()[0]?.leaving).toBe(false);

    store.resume(id);
    vi.advanceTimersByTime(3000);
    expect(store.getSnapshot()[0]?.leaving).toBe(true);
  });

  it("resuming an error toast keeps it sticky", () => {
    const id = store.push("error", "stays");
    store.pause(id);
    store.resume(id);
    vi.advanceTimersByTime(60_000);
    expect(ids()).toEqual([id]);
  });

  it("dismissing twice removes it at once (skip the exit animation)", () => {
    const id = store.push("error", "e");
    store.dismiss(id);
    expect(store.getSnapshot()[0]?.leaving).toBe(true);
    store.dismiss(id);
    expect(store.getSnapshot()).toHaveLength(0);
  });

  it("notifies subscribers and hands React a fresh array each time", () => {
    const listener = vi.fn();
    store.subscribe(listener);
    const empty = store.getSnapshot();
    store.push("info", "a");
    expect(listener).toHaveBeenCalledTimes(1);
    expect(store.getSnapshot()).not.toBe(empty);
  });

  it("clear() empties the queue and cancels pending timers", () => {
    store.push("info", "a");
    store.push("error", "b");
    store.clear();
    expect(store.getSnapshot()).toHaveLength(0);
    expect(() => vi.advanceTimersByTime(60_000)).not.toThrow();
    expect(store.getSnapshot()).toHaveLength(0);
  });

  it("truncates a runaway message", () => {
    store.push("error", "x".repeat(2000));
    const text = store.getSnapshot()[0]?.message ?? "";
    expect(text.length).toBeLessThanOrEqual(500);
    expect(text.endsWith("…")).toBe(true);
  });

  it("accepts injected timers and per-kind TTLs", () => {
    const scheduled: Array<{ ms: number }> = [];
    const custom = createToastStore({
      ttl: { info: 50 },
      timers: {
        set: (_fn, ms) => scheduled.push({ ms }),
        clear: () => undefined,
      },
    });
    custom.push("info", "quick");
    expect(scheduled).toEqual([{ ms: 50 }]);
  });
});
