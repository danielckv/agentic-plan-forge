import { describe, expect, it, vi } from "vitest";

import {
  createPrefsStore,
  DARK_QUERY,
  isSidebarCollapsed,
  NARROW_QUERY,
  parseSidebarPref,
  parseTheme,
  resolveTheme,
  SIDEBAR_KEY,
  THEME_KEY,
  THEME_SETTLE_MS,
} from "./prefs";
import type { KeyValueStore, MediaQueryListLike, ThemeRoot } from "./prefs";

/** In-memory storage; `throws` makes every call fail like a locked-down profile. */
function fakeStorage(initial: Record<string, string> = {}, throws = false): KeyValueStore & { data: Map<string, string> } {
  const data = new Map(Object.entries(initial));
  return {
    data,
    getItem(k) {
      if (throws) throw new Error("denied");
      return data.get(k) ?? null;
    },
    setItem(k, v) {
      if (throws) throw new Error("denied");
      data.set(k, v);
    },
  };
}

/** A controllable `matchMedia`: flip a query and its listeners fire. */
function fakeMedia(initial: Record<string, boolean>): {
  matchMedia: (q: string) => MediaQueryListLike;
  set: (query: string, matches: boolean) => void;
} {
  const state = new Map(Object.entries(initial));
  const listeners = new Map<string, Set<(e: { matches: boolean }) => void>>();
  return {
    matchMedia: (query) => ({
      get matches() {
        return state.get(query) ?? false;
      },
      addEventListener(_type, l) {
        const set = listeners.get(query) ?? new Set();
        set.add(l);
        listeners.set(query, set);
      },
      removeEventListener(_type, l) {
        listeners.get(query)?.delete(l);
      },
    }),
    set(query, matches) {
      state.set(query, matches);
      for (const l of listeners.get(query) ?? []) l({ matches });
    },
  };
}

function fakeRoot(): ThemeRoot & { attrs: Map<string, string> } {
  const attrs = new Map<string, string>();
  return {
    attrs,
    setAttribute: (n, v) => void attrs.set(n, v),
    removeAttribute: (n) => void attrs.delete(n),
  };
}

describe("pure preference helpers", () => {
  it("parses stored values and falls back safely", () => {
    expect(parseTheme("dark")).toBe("dark");
    expect(parseTheme("light")).toBe("light");
    expect(parseTheme("system")).toBe("system");
    expect(parseTheme("sepia")).toBe("system");
    expect(parseTheme(null)).toBe("system");
    expect(parseSidebarPref("collapsed")).toBe("collapsed");
    expect(parseSidebarPref("expanded")).toBe("expanded");
    expect(parseSidebarPref("wide")).toBeNull();
    expect(parseSidebarPref(null)).toBeNull();
  });

  it("resolves the effective theme: an explicit choice beats the OS", () => {
    expect(resolveTheme("system", true)).toBe("dark");
    expect(resolveTheme("system", false)).toBe("light");
    expect(resolveTheme("light", true)).toBe("light");
    expect(resolveTheme("dark", false)).toBe("dark");
  });

  it("collapses the sidebar by viewport only until the user chooses", () => {
    expect(isSidebarCollapsed(null, true)).toBe(true);
    expect(isSidebarCollapsed(null, false)).toBe(false);
    expect(isSidebarCollapsed("expanded", true)).toBe(false);
    expect(isSidebarCollapsed("collapsed", false)).toBe(true);
  });

  it("uses the 1100px breakpoint: narrower than 1100 is narrow", () => {
    expect(NARROW_QUERY).toBe("(max-width: 1099.98px)");
  });
});

describe("prefs store: theme", () => {
  it("applies and persists an explicit choice", () => {
    const storage = fakeStorage();
    const root = fakeRoot();
    const store = createPrefsStore({ storage, matchMedia: null, root });

    store.setTheme("dark");
    expect(root.attrs.get("data-theme")).toBe("dark");
    expect(storage.data.get(THEME_KEY)).toBe("dark");
    expect(store.getSnapshot()).toMatchObject({ theme: "dark", effectiveTheme: "dark" });

    store.setTheme("light");
    expect(root.attrs.get("data-theme")).toBe("light");
    expect(storage.data.get(THEME_KEY)).toBe("light");
  });

  it("System removes data-theme so the OS decides", () => {
    const root = fakeRoot();
    const media = fakeMedia({ [DARK_QUERY]: true });
    const store = createPrefsStore({ storage: fakeStorage(), matchMedia: media.matchMedia, root });

    expect(root.attrs.has("data-theme")).toBe(false);
    expect(store.getSnapshot().effectiveTheme).toBe("dark");

    store.setTheme("light");
    expect(root.attrs.get("data-theme")).toBe("light");
    store.setTheme("system");
    expect(root.attrs.has("data-theme")).toBe(false);
    expect(store.getSnapshot().effectiveTheme).toBe("dark");
  });

  it("restores the saved choice on load, before anything renders", () => {
    const root = fakeRoot();
    const store = createPrefsStore({ storage: fakeStorage({ [THEME_KEY]: "dark" }), matchMedia: null, root });
    expect(root.attrs.get("data-theme")).toBe("dark");
    expect(store.getSnapshot().theme).toBe("dark");
  });

  it("tracks the OS while on System, and ignores it once pinned", () => {
    const media = fakeMedia({ [DARK_QUERY]: false });
    const store = createPrefsStore({ storage: fakeStorage(), matchMedia: media.matchMedia, root: fakeRoot() });
    const seen: string[] = [];
    store.subscribe(() => seen.push(store.getSnapshot().effectiveTheme));

    media.set(DARK_QUERY, true);
    expect(store.getSnapshot().effectiveTheme).toBe("dark");

    store.setTheme("light");
    media.set(DARK_QUERY, false);
    media.set(DARK_QUERY, true);
    expect(store.getSnapshot().effectiveTheme).toBe("light");
    expect(seen[0]).toBe("dark");
  });

  it("cycles System -> Light -> Dark -> System", () => {
    const store = createPrefsStore({ storage: fakeStorage(), matchMedia: null, root: fakeRoot() });
    const order: string[] = [store.getSnapshot().theme];
    for (let i = 0; i < 3; i++) {
      store.cycleTheme();
      order.push(store.getSnapshot().theme);
    }
    expect(order).toEqual(["system", "light", "dark", "system"]);
  });

  it("notifies subscribers with a fresh snapshot object on change only", () => {
    const store = createPrefsStore({ storage: fakeStorage(), matchMedia: null, root: fakeRoot() });
    const listener = vi.fn();
    store.subscribe(listener);
    const before = store.getSnapshot();

    store.setTheme("system"); // no change
    expect(listener).not.toHaveBeenCalled();
    expect(store.getSnapshot()).toBe(before);

    store.setTheme("dark");
    expect(listener).toHaveBeenCalledTimes(1);
    expect(store.getSnapshot()).not.toBe(before);
  });

  it("holds transitions off around a theme change, then lets go", () => {
    vi.useFakeTimers();
    try {
      const root = fakeRoot();
      const store = createPrefsStore({ storage: fakeStorage(), matchMedia: null, root });
      expect(root.attrs.has("data-theming")).toBe(false);

      store.setTheme("dark");
      expect(root.attrs.has("data-theming")).toBe(true);
      vi.advanceTimersByTime(THEME_SETTLE_MS + 1);
      expect(root.attrs.has("data-theming")).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });

  it("survives storage that throws", () => {
    const store = createPrefsStore({ storage: fakeStorage({}, true), matchMedia: null, root: fakeRoot() });
    expect(() => store.setTheme("dark")).not.toThrow();
    expect(store.getSnapshot().theme).toBe("dark");
    expect(() => store.toggleSidebar()).not.toThrow();
  });
});

describe("prefs store: sidebar", () => {
  it("auto-collapses when the viewport is narrow and no choice was made", () => {
    const media = fakeMedia({ [NARROW_QUERY]: true });
    const store = createPrefsStore({ storage: fakeStorage(), matchMedia: media.matchMedia, root: null });
    expect(store.getSnapshot()).toMatchObject({ narrow: true, sidebarPref: null, sidebarCollapsed: true });

    media.set(NARROW_QUERY, false);
    expect(store.getSnapshot().sidebarCollapsed).toBe(false);
    media.set(NARROW_QUERY, true);
    expect(store.getSnapshot().sidebarCollapsed).toBe(true);
  });

  it("toggling persists the choice and flips the effective state", () => {
    const storage = fakeStorage();
    const store = createPrefsStore({ storage, matchMedia: fakeMedia({ [NARROW_QUERY]: false }).matchMedia, root: null });
    expect(store.getSnapshot().sidebarCollapsed).toBe(false);

    store.toggleSidebar();
    expect(store.getSnapshot()).toMatchObject({ sidebarPref: "collapsed", sidebarCollapsed: true });
    expect(storage.data.get(SIDEBAR_KEY)).toBe("collapsed");

    store.toggleSidebar();
    expect(store.getSnapshot()).toMatchObject({ sidebarPref: "expanded", sidebarCollapsed: false });
    expect(storage.data.get(SIDEBAR_KEY)).toBe("expanded");
  });

  it("an explicit expand wins over a narrow viewport", () => {
    const media = fakeMedia({ [NARROW_QUERY]: true });
    const storage = fakeStorage();
    const store = createPrefsStore({ storage, matchMedia: media.matchMedia, root: null });
    expect(store.getSnapshot().sidebarCollapsed).toBe(true);

    store.toggleSidebar(); // collapsed -> expanded
    expect(store.getSnapshot().sidebarCollapsed).toBe(false);
    media.set(NARROW_QUERY, false);
    media.set(NARROW_QUERY, true);
    expect(store.getSnapshot().sidebarCollapsed).toBe(false);
    expect(storage.data.get(SIDEBAR_KEY)).toBe("expanded");
  });

  it("restores a saved choice on load", () => {
    const store = createPrefsStore({
      storage: fakeStorage({ [SIDEBAR_KEY]: "collapsed" }),
      matchMedia: fakeMedia({ [NARROW_QUERY]: false }).matchMedia,
      root: null,
    });
    expect(store.getSnapshot()).toMatchObject({ sidebarPref: "collapsed", sidebarCollapsed: true });
  });

  it("stops listening after dispose", () => {
    const media = fakeMedia({ [NARROW_QUERY]: false });
    const store = createPrefsStore({ storage: fakeStorage(), matchMedia: media.matchMedia, root: null });
    store.dispose();
    media.set(NARROW_QUERY, true);
    expect(store.getSnapshot().narrow).toBe(false);
  });
});
