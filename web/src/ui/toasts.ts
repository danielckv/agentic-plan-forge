/**
 * Toast queue: transient notices and errors, rendered top-right by `Toaster`.
 *
 * Persistent states (offline, protocol mismatch, auth) are NOT toasts; they
 * stay banners because they describe the window's condition, not an event.
 *
 * Lifetime by kind: info and success dismiss themselves after 6 s, warnings
 * after 10 s, errors stay until the user dismisses them. An identical message
 * that is already on screen bumps a counter and restarts its timer instead of
 * stacking. Timers are injectable so the queue is testable with a fake clock.
 */

import { useSyncExternalStore } from "react";

export type ToastKind = "info" | "success" | "warning" | "error";

export interface Toast {
  id: number;
  kind: ToastKind;
  message: string;
  /** How many times this exact message arrived while on screen. */
  count: number;
  /** Set while the exit animation plays; the toast is removed after `exitMs`. */
  leaving: boolean;
}

interface ToastTimers {
  set(fn: () => void, ms: number): unknown;
  clear(handle: unknown): void;
}

interface ToastStoreOptions {
  timers?: ToastTimers;
  /** Per-kind auto-dismiss in ms; `null` keeps the toast until dismissed. */
  ttl?: Partial<Record<ToastKind, number | null>>;
  exitMs?: number;
  /** Most toasts shown at once; the oldest is dropped past this. */
  max?: number;
}

export interface ToastStore {
  push(kind: ToastKind, message: string): number;
  dismiss(id: number): void;
  /** Hold the auto-dismiss timer while the pointer or focus is on a toast. */
  pause(id: number): void;
  resume(id: number): void;
  clear(): void;
  getSnapshot(): readonly Toast[];
  subscribe(listener: () => void): () => void;
}

export const TOAST_INFO_TTL_MS = 6000;
export const TOAST_EXIT_MS = 160;
const RESUME_GRACE_MS = 3000;
const MESSAGE_LIMIT = 500;

export const DEFAULT_TOAST_TTL: Record<ToastKind, number | null> = {
  info: TOAST_INFO_TTL_MS,
  success: TOAST_INFO_TTL_MS,
  warning: 10_000,
  error: null,
};

const realTimers: ToastTimers = {
  set: (fn, ms) => setTimeout(fn, ms),
  clear: (handle) => clearTimeout(handle as number),
};

export function createToastStore(options: ToastStoreOptions = {}): ToastStore {
  const timers = options.timers ?? realTimers;
  const ttl: Record<ToastKind, number | null> = { ...DEFAULT_TOAST_TTL, ...options.ttl };
  const exitMs = options.exitMs ?? TOAST_EXIT_MS;
  const max = options.max ?? 4;

  let toasts: readonly Toast[] = [];
  let nextId = 1;
  const dismissTimers = new Map<number, unknown>();
  const exitTimers = new Map<number, unknown>();
  const listeners = new Set<() => void>();

  const emit = (next: readonly Toast[]): void => {
    toasts = next;
    for (const l of listeners) l();
  };

  const stopTimer = (map: Map<number, unknown>, id: number): void => {
    const handle = map.get(id);
    if (handle === undefined) return;
    timers.clear(handle);
    map.delete(id);
  };

  const remove = (id: number): void => {
    stopTimer(dismissTimers, id);
    stopTimer(exitTimers, id);
    emit(toasts.filter((t) => t.id !== id));
  };

  const arm = (id: number, ms: number | null): void => {
    stopTimer(dismissTimers, id);
    if (ms === null) return;
    dismissTimers.set(id, timers.set(() => dismiss(id), ms));
  };

  function dismiss(id: number): void {
    const toast = toasts.find((t) => t.id === id);
    if (toast === undefined) return;
    stopTimer(dismissTimers, id);
    if (toast.leaving) {
      remove(id);
      return;
    }
    emit(toasts.map((t) => (t.id === id ? { ...t, leaving: true } : t)));
    exitTimers.set(
      id,
      timers.set(() => remove(id), exitMs),
    );
  }

  return {
    push(kind, message) {
      const text = message.length > MESSAGE_LIMIT ? `${message.slice(0, MESSAGE_LIMIT - 1)}…` : message;
      const existing = toasts.find((t) => !t.leaving && t.kind === kind && t.message === text);
      if (existing !== undefined) {
        emit(toasts.map((t) => (t.id === existing.id ? { ...t, count: t.count + 1 } : t)));
        arm(existing.id, ttl[kind]);
        return existing.id;
      }
      const id = nextId++;
      const next = [...toasts, { id, kind, message: text, count: 1, leaving: false }];
      // Past the cap the oldest toast goes immediately; it is already stale.
      const overflow = next.length - max;
      for (const dropped of next.slice(0, Math.max(0, overflow))) {
        stopTimer(dismissTimers, dropped.id);
        stopTimer(exitTimers, dropped.id);
      }
      emit(overflow > 0 ? next.slice(overflow) : next);
      arm(id, ttl[kind]);
      return id;
    },
    dismiss,
    pause(id) {
      stopTimer(dismissTimers, id);
    },
    resume(id) {
      const toast = toasts.find((t) => t.id === id);
      if (toast === undefined || toast.leaving) return;
      const limit = ttl[toast.kind];
      arm(id, limit === null ? null : Math.min(limit, RESUME_GRACE_MS));
    },
    clear() {
      for (const id of [...dismissTimers.keys()]) stopTimer(dismissTimers, id);
      for (const id of [...exitTimers.keys()]) stopTimer(exitTimers, id);
      emit([]);
    },
    getSnapshot: () => toasts,
    subscribe(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
  };
}

/** The window-wide queue: syncStore, pages, and `Toaster` share this one. */
export const toastStore: ToastStore = createToastStore();

export function useToasts(): readonly Toast[] {
  return useSyncExternalStore(toastStore.subscribe, toastStore.getSnapshot, toastStore.getSnapshot);
}
