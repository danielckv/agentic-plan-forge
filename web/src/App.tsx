import { useEffect, useState } from "react";
import { TriangleAlert } from "lucide-react";

import { apiFetch } from "./protocol/bridge";
import { applyStatus, useSync, navigate, connect, disconnect } from "./store/syncStore";
import type { StatusView } from "./store/syncStore";
import { Page1Ingestion } from "./pages/Page1Ingestion";
import { Page2AvatarCanvas } from "./pages/Page2AvatarCanvas";
import { Page3SynthesisConfluence } from "./pages/Page3SynthesisConfluence";
import { Page4Build } from "./pages/Page4Build";
import { Sidebar } from "./components/Sidebar";
import { TopBar } from "./components/TopBar";
import { prefs, usePrefs } from "./ui/prefs";
import { ShortcutHelp } from "./ui/ShortcutHelp";
import { Toaster } from "./ui/Toaster";
import { useGlobalShortcuts } from "./ui/useShortcuts";

/**
 * Three routed views with explicit phase-driven transitions; never one
 * scrolling page. The router follows the authority phase and blocks manual
 * URL jumps (navigate() guards).
 *
 * Notices are split by lifetime. Persistent window states render as banners:
 * relay unreachable, reconnecting, protocol version mismatch, authentication.
 * Everything else (a rejected edit, a copy that failed, a decision someone else
 * resolved) is a dismissible toast, rendered by `Toaster`.
 */
export function App(): React.ReactElement {
  const sync = useSync();
  const { sidebarCollapsed } = usePrefs();
  const [helpOpen, setHelpOpen] = useState(false);

  useEffect(() => {
    connect();
    return () => disconnect();
  }, []);

  // The bridge owns the status pills and the repository identity. Polling
  // lives here, not on Page 1, so the project stays named on every page and
  // survives a reload straight into deliberation or synthesis.
  useEffect(() => {
    let cancelled = false;
    let inFlight = false;
    const controller = new AbortController();
    const poll = async (): Promise<void> => {
      if (inFlight || cancelled) return;
      inFlight = true;
      try {
        const res = await apiFetch("/api/status", { signal: controller.signal });
        if (!res.ok || cancelled) return;
        const body = (await res.json()) as Partial<StatusView>;
        if (cancelled) return;
        applyStatus(body);
      } catch {
        /* the authority is away: the last known status stays on screen */
      } finally {
        inFlight = false;
      }
    };
    void poll();
    const id = window.setInterval(() => void poll(), 5000);
    return () => {
      cancelled = true;
      controller.abort();
      window.clearInterval(id);
    };
  }, []);

  // Manual URL jumps are blocked: navigate() only honors routes the phase
  // has reached. Back/forward history entries are likewise ignored.
  useEffect(() => {
    const onPopState = () => {
      navigate(sync.route);
    };
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, [sync.route]);

  useGlobalShortcuts({ toggleSidebar: prefs.toggleSidebar, openHelp: () => setHelpOpen(true) });

  const page =
    sync.route === "/ingest" ? (
      <Page1Ingestion />
    ) : sync.route === "/deliberate" ? (
      <Page2AvatarCanvas />
    ) : sync.route === "/build" ? (
      <Page4Build />
    ) : (
      <Page3SynthesisConfluence />
    );

  return (
    <div className="pf-app">
      <Sidebar sync={sync} collapsed={sidebarCollapsed} />

      {/* Content column */}
      <div className="pf-content">
        <TopBar sync={sync} onOpenHelp={() => setHelpOpen(true)} />

        {sync.banner !== null && (
          <div className="pf-banner pf-banner--warn" role="status">
            <TriangleAlert size={14} />
            {sync.banner}
          </div>
        )}
        {sync.errorMessage !== null && (
          <div className="pf-banner pf-banner--danger" role="alert">
            <TriangleAlert size={14} />
            {sync.errorMessage}
          </div>
        )}

        {/* Keyed by route: each page change replays the 200 ms enter animation. */}
        <main className="pf-main">
          <div key={sync.route} className="pf-route">
            {page}
          </div>
        </main>
      </div>

      <Toaster />
      <ShortcutHelp open={helpOpen} onClose={() => setHelpOpen(false)} />
    </div>
  );
}

export default App;
