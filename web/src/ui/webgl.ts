/**
 * Whether this browser can draw the 3D plan view.
 *
 * The answer is cached: probing creates a real context, and doing that on
 * every render would leak contexts until the browser starts dropping the
 * oldest ones. The probe is also the only honest test — a feature string can
 * be present while the context still fails to allocate, which is exactly what
 * happens on a machine that has run out of GPU contexts.
 */
let cached: boolean | null = null;

export function hasWebGL(): boolean {
  if (cached !== null) return cached;
  cached = probe();
  return cached;
}

function probe(): boolean {
  if (typeof document === "undefined") return false;
  try {
    const canvas = document.createElement("canvas");
    const context = canvas.getContext("webgl2") ?? canvas.getContext("webgl");
    if (context === null) return false;
    // Release it immediately. Holding the probe context would cost a real one
    // from a budget browsers keep small.
    const lose = context.getExtension("WEBGL_lose_context");
    if (lose !== null) lose.loseContext();
    return true;
  } catch {
    // Some browsers throw rather than returning null when WebGL is disabled by
    // policy. Either way the answer is the same.
    return false;
  }
}

