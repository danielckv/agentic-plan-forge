/**
 * Window preferences: theme choice and sidebar state.
 *
 * Both survive reloads through `localStorage`. The Plan-Forge tab runs with
 * an ephemeral browser profile, so nothing here outlives the window itself;
 * the keys exist so a reload (or the relaunch of a still-open window) keeps
 * the choice. Storage is optional everywhere: a refused read or write falls
 * back to the defaults and never throws.
 *
 * The store is a factory over an injectable environment so the behavior is
 * testable without a browser; the module-level `prefs` instance binds it to
 * the real window and applies the saved theme as soon as this module loads,
 * which is before React renders anything.
 */

import { useSyncExternalStore } from "react";

export const THEME_KEY = "plan-forge:ui:theme";
export const SIDEBAR_KEY = "plan-forge:ui:sidebar";

/** Below this viewport width the sidebar collapses to its icon rail. */
export const SIDEBAR_AUTO_COLLAPSE_PX = 1100;
export const NARROW_QUERY = `(max-width: ${String(SIDEBAR_AUTO_COLLAPSE_PX - 0.02)}px)`;
export const DARK_QUERY = "(prefers-color-scheme: dark)";
/** How long `data-theming` holds transitions off around a theme change. */
export const THEME_SETTLE_MS = 100;

export type ThemeChoice = "system" | "light" | "dark";
export type EffectiveTheme = "light" | "dark";
export type SidebarPref = "expanded" | "collapsed";

export const THEME_ORDER: readonly ThemeChoice[] = ["system", "light", "dark"];

export function parseTheme(raw: string | null): ThemeChoice {
  return raw === "light" || raw === "dark" || raw === "system" ? raw : "system";
}

/** `null` means the user never chose; the viewport decides. */
export function parseSidebarPref(raw: string | null): SidebarPref | null {
  return raw === "expanded" || raw === "collapsed" ? raw : null;
}

export function resolveTheme(choice: ThemeChoice, systemDark: boolean): EffectiveTheme {
  if (choice === "system") return systemDark ? "dark" : "light";
  return choice;
}

/**
 * An explicit choice always wins. With none, a narrow viewport collapses the
 * rail and a wide one keeps it open.
 */
export function isSidebarCollapsed(pref: SidebarPref | null, narrow: boolean): boolean {
  if (pref !== null) return pref === "collapsed";
  return narrow;
}

export interface KeyValueStore {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

export interface MediaQueryListLike {
  readonly matches: boolean;
  addEventListener(type: "change", listener: (event: { matches: boolean }) => void): void;
  removeEventListener(type: "change", listener: (event: { matches: boolean }) => void): void;
}

export interface ThemeRoot {
  setAttribute(name: string, value: string): void;
  removeAttribute(name: string): void;
}

export interface PrefsEnv {
  storage: KeyValueStore | null;
  matchMedia: ((query: string) => MediaQueryListLike) | null;
  root: ThemeRoot | null;
}

export interface PrefsSnapshot {
  theme: ThemeChoice;
  effectiveTheme: EffectiveTheme;
  /** The persisted sidebar choice, `null` until the user toggles it. */
  sidebarPref: SidebarPref | null;
  narrow: boolean;
  sidebarCollapsed: boolean;
}

export interface PrefsStore {
  getSnapshot(): PrefsSnapshot;
  subscribe(listener: () => void): () => void;
  setTheme(choice: ThemeChoice): void;
  /** System -> Light -> Dark -> System, for the collapsed rail's single button. */
  cycleTheme(): void;
  setSidebarPref(pref: SidebarPref): void;
  toggleSidebar(): void;
  dispose(): void;
}

function readKey(storage: KeyValueStore | null, key: string): string | null {
  if (storage === null) return null;
  try {
    return storage.getItem(key);
  } catch {
    return null;
  }
}

function writeKey(storage: KeyValueStore | null, key: string, value: string): void {
  if (storage === null) return;
  try {
    storage.setItem(key, value);
  } catch {
    /* quota or a locked-down profile: the choice just stays in memory */
  }
}

export function createPrefsStore(env: PrefsEnv): PrefsStore {
  const darkQuery = env.matchMedia === null ? null : env.matchMedia(DARK_QUERY);
  const narrowQuery = env.matchMedia === null ? null : env.matchMedia(NARROW_QUERY);

  let theme = parseTheme(readKey(env.storage, THEME_KEY));
  let sidebarPref = parseSidebarPref(readKey(env.storage, SIDEBAR_KEY));
  let systemDark = darkQuery?.matches ?? false;
  let narrow = narrowQuery?.matches ?? false;

  const listeners = new Set<() => void>();

  const compute = (): PrefsSnapshot => ({
    theme,
    effectiveTheme: resolveTheme(theme, systemDark),
    sidebarPref,
    narrow,
    sidebarCollapsed: isSidebarCollapsed(sidebarPref, narrow),
  });

  let snapshot = compute();

  /**
   * An explicit theme sets `data-theme`, which the stylesheet ranks above the
   * `prefers-color-scheme` block. "System" removes the attribute so the OS
   * decides.
   */
  const applyTheme = (): void => {
    if (env.root === null) return;
    if (theme === "system") env.root.removeAttribute("data-theme");
    else env.root.setAttribute("data-theme", theme);
  };

  /**
   * A theme change repaints every surface at once. Surfaces that carry a
   * transition would fade while the rest snap, so `data-theming` switches
   * transitions off for a beat (the stylesheet honors it) and is then removed.
   */
  let settleTimer: unknown;
  const holdTransitions = (): void => {
    if (env.root === null) return;
    env.root.setAttribute("data-theming", "");
    clearTimeout(settleTimer as number);
    settleTimer = setTimeout(() => env.root?.removeAttribute("data-theming"), THEME_SETTLE_MS);
  };

  const commit = (): void => {
    snapshot = compute();
    for (const l of listeners) l();
  };

  const onDark = (e: { matches: boolean }): void => {
    systemDark = e.matches;
    if (theme === "system") holdTransitions();
    commit();
  };
  const onNarrow = (e: { matches: boolean }): void => {
    narrow = e.matches;
    commit();
  };
  darkQuery?.addEventListener("change", onDark);
  narrowQuery?.addEventListener("change", onNarrow);

  applyTheme();

  const setTheme = (choice: ThemeChoice): void => {
    if (choice === theme) return;
    theme = choice;
    writeKey(env.storage, THEME_KEY, choice);
    holdTransitions();
    applyTheme();
    commit();
  };

  const setSidebarPref = (pref: SidebarPref): void => {
    if (pref === sidebarPref) return;
    sidebarPref = pref;
    writeKey(env.storage, SIDEBAR_KEY, pref);
    commit();
  };

  return {
    getSnapshot: () => snapshot,
    subscribe(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    setTheme,
    cycleTheme: () => setTheme(THEME_ORDER[(THEME_ORDER.indexOf(theme) + 1) % THEME_ORDER.length] ?? "system"),
    setSidebarPref,
    toggleSidebar: () => setSidebarPref(snapshot.sidebarCollapsed ? "expanded" : "collapsed"),
    dispose() {
      darkQuery?.removeEventListener("change", onDark);
      narrowQuery?.removeEventListener("change", onNarrow);
      listeners.clear();
    },
  };
}

function browserEnv(): PrefsEnv {
  if (typeof window === "undefined") return { storage: null, matchMedia: null, root: null };
  let storage: KeyValueStore | null = null;
  try {
    storage = window.localStorage;
  } catch {
    storage = null;
  }
  return {
    storage,
    matchMedia:
      typeof window.matchMedia === "function" ? (q) => window.matchMedia(q) : null,
    root: typeof document === "undefined" ? null : document.documentElement,
  };
}

export const prefs: PrefsStore = createPrefsStore(browserEnv());

export function usePrefs(): PrefsSnapshot {
  return useSyncExternalStore(prefs.subscribe, prefs.getSnapshot, prefs.getSnapshot);
}

export function useEffectiveTheme(): EffectiveTheme {
  return usePrefs().effectiveTheme;
}
