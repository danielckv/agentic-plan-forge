import type { HookAPI } from "@oh-my-pi/pi-coding-agent/extensibility/hooks";

// Plan-Forge session-start hook (managed). Sends a bounded plan-status banner
// when the repository's approved plan changed since the last session.
export default function hook(pi: HookAPI): void {
  pi.on("session_start", async (_event, ctx) => {
    try {
      const out = await pi.exec("{{PLAN_FORGE_BIN}}", [
        "hook",
        "check-state",
        "--repo",
        ctx.cwd,
        "--format",
        "omp",
      ]);
      const banner = String(out ?? "").trim();
      if (banner) {
        pi.sendMessage(banner);
      }
    } catch {
      // Plan-Forge is optional at session start; never block the session.
    }
  });
}
