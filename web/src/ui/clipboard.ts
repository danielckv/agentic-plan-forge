/**
 * Copy text to the clipboard. The async Clipboard API is tried first (the
 * window is served from loopback, a secure context); when it is missing or
 * refuses, a hidden textarea and `execCommand("copy")` is the fallback.
 * Resolves false when both paths fail so the caller can say so.
 */
export async function copyText(text: string): Promise<boolean> {
  if (typeof navigator !== "undefined" && navigator.clipboard !== undefined) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      /* permission denied or not focused: fall through to the legacy path */
    }
  }
  if (typeof document === "undefined") return false;
  const area = document.createElement("textarea");
  area.value = text;
  area.setAttribute("readonly", "");
  area.style.position = "fixed";
  area.style.opacity = "0";
  area.style.pointerEvents = "none";
  document.body.appendChild(area);
  const restore = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  area.select();
  let ok = false;
  try {
    ok = document.execCommand("copy");
  } catch {
    ok = false;
  }
  area.remove();
  restore?.focus();
  return ok;
}
