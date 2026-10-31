/**
 * Scroll-spy for the Page 3 contents list.
 *
 * `pickActiveSection` is pure: it takes each section's top edge in scroll-
 * content coordinates plus the scroller's metrics and names the section the
 * reader is in. `useScrollSpy` feeds it from a real scroll container.
 */

import { useCallback, useEffect, useRef, useState } from "react";

export interface SectionTop {
  id: string;
  /** Top edge relative to the scroller's content origin (not the viewport). */
  top: number;
}

export interface ScrollMetrics {
  scrollTop: number;
  clientHeight: number;
  scrollHeight: number;
}

/** A section becomes active once its top passes this far below the viewport top. */
export const SPY_OFFSET_PX = 32;
const BOTTOM_SLACK_PX = 4;

/**
 * The active section is the last one whose top has reached the activation line.
 * Above the first section the first one is active. At the very bottom the last
 * section wins even when it is too short to ever reach the line.
 */
export function pickActiveSection(
  sections: readonly SectionTop[],
  metrics: ScrollMetrics,
  offset: number = SPY_OFFSET_PX,
): string | null {
  if (sections.length === 0) return null;
  const ordered = [...sections].sort((a, b) => a.top - b.top);
  const last = ordered[ordered.length - 1]!;
  const scrollable = metrics.scrollHeight > metrics.clientHeight + BOTTOM_SLACK_PX;
  if (scrollable && metrics.scrollTop + metrics.clientHeight >= metrics.scrollHeight - BOTTOM_SLACK_PX) {
    return last.id;
  }
  const line = metrics.scrollTop + offset;
  let active = ordered[0]!.id;
  for (const s of ordered) {
    if (s.top <= line) active = s.id;
    else break;
  }
  return active;
}

function prefersReducedMotion(): boolean {
  return (
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches
  );
}

/** How long a TOC click suppresses the spy while the smooth scroll runs. */
const CLICK_LOCK_MS = 700;
/** Gap left above a section when the TOC scrolls to it. */
const SCROLL_MARGIN_PX = 16;

interface ScrollSpy {
  active: string | null;
  /** Scroll to a section and mark it active immediately. */
  scrollTo: (id: string) => void;
  /** Re-measure after content height changes (accordions, filters). */
  refresh: () => void;
}

/**
 * Track which of `ids` is in view inside `containerRef`. Sections are located
 * by element id; ids with no element yet are skipped.
 */
export function useScrollSpy(containerRef: React.RefObject<HTMLElement | null>, ids: readonly string[]): ScrollSpy {
  const [active, setActive] = useState<string | null>(ids[0] ?? null);
  const lockUntil = useRef(0);
  const frame = useRef(0);
  const idsKey = ids.join("|");

  const measure = useCallback((): void => {
    const el = containerRef.current;
    if (el === null) return;
    const origin = el.getBoundingClientRect().top - el.scrollTop;
    const tops: SectionTop[] = [];
    for (const id of idsKey.split("|")) {
      const node = document.getElementById(id);
      if (node === null) continue;
      tops.push({ id, top: node.getBoundingClientRect().top - origin });
    }
    const next = pickActiveSection(tops, {
      scrollTop: el.scrollTop,
      clientHeight: el.clientHeight,
      scrollHeight: el.scrollHeight,
    });
    setActive((prev) => (prev === next ? prev : next));
  }, [containerRef, idsKey]);

  const refresh = useCallback((): void => {
    if (performance.now() < lockUntil.current) return;
    cancelAnimationFrame(frame.current);
    frame.current = requestAnimationFrame(measure);
  }, [measure]);

  useEffect(() => {
    const el = containerRef.current;
    if (el === null) return;
    el.addEventListener("scroll", refresh, { passive: true });
    window.addEventListener("resize", refresh);
    refresh();
    return () => {
      el.removeEventListener("scroll", refresh);
      window.removeEventListener("resize", refresh);
      cancelAnimationFrame(frame.current);
    };
  }, [containerRef, refresh]);

  const scrollTo = useCallback(
    (id: string): void => {
      const container = containerRef.current;
      const node = document.getElementById(id);
      if (container === null || node === null) return;
      const reduced = prefersReducedMotion();
      setActive(id);
      lockUntil.current = performance.now() + (reduced ? 0 : CLICK_LOCK_MS);
      // Scroll the container itself. `scrollIntoView` would also scroll any
      // clipped ancestor once the container hits its end, shifting the whole app.
      const top = node.getBoundingClientRect().top - container.getBoundingClientRect().top + container.scrollTop;
      container.scrollTo({ top: Math.max(0, top - SCROLL_MARGIN_PX), behavior: reduced ? "auto" : "smooth" });
      if (!reduced) window.setTimeout(refresh, CLICK_LOCK_MS + 50);
    },
    [containerRef, refresh],
  );

  return { active, scrollTo, refresh };
}
