/**
 * Focus-trap helpers for modal dialogs. The index math is pure so the wrap
 * rules are testable; the DOM half only filters what can really take focus.
 */

const FOCUSABLE_SELECTOR = [
  "a[href]",
  "button:not([disabled])",
  "input:not([disabled]):not([type='hidden'])",
  "select:not([disabled])",
  "textarea:not([disabled])",
  "[tabindex]:not([tabindex='-1'])",
].join(",");

/**
 * Where Tab or Shift+Tab lands inside a trapped container of `length`
 * focusable elements when focus is on index `current` (`-1` = outside the list).
 * Focus wraps at both ends. Returns `-1` when there is nothing to focus.
 */
function nextFocusIndex(length: number, current: number, backwards: boolean): number {
  if (length <= 0) return -1;
  if (current < 0 || current >= length) return backwards ? length - 1 : 0;
  return (current + (backwards ? -1 : 1) + length) % length;
}

/** Layout-aware default: an element with no client rects is not reachable. */
function isRendered(el: HTMLElement): boolean {
  return el.getClientRects().length > 0;
}

function focusableWithin(
  container: HTMLElement,
  visible: (el: HTMLElement) => boolean = isRendered,
): HTMLElement[] {
  return Array.from(container.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)).filter(visible);
}

/**
 * Keep Tab inside `container`: move focus to the next (or previous) focusable
 * element, wrapping at both ends, and cancel the browser's own focus move.
 * With nothing focusable inside, focus parks on the container itself.
 */
export function trapTab(
  container: HTMLElement,
  event: { shiftKey: boolean; preventDefault(): void },
  active: Element | null,
  visible?: (el: HTMLElement) => boolean,
): void {
  const items = focusableWithin(container, visible);
  event.preventDefault();
  const current = active === null ? -1 : items.indexOf(active as HTMLElement);
  const target = items[nextFocusIndex(items.length, current, event.shiftKey)];
  (target ?? container).focus();
}
