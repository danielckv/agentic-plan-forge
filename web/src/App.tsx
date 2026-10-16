import { useEffect } from "react";

import { useSync, navigate, connect, disconnect } from "./store/syncStore";
import { Page1Ingestion } from "./pages/Page1Ingestion";
import { Page2AvatarCanvas } from "./pages/Page2AvatarCanvas";
import { Page3SynthesisConfluence } from "./pages/Page3SynthesisConfluence";

/**
 * Three routed views with explicit phase-driven transitions; never one
 * scrolling page. The router follows the authority phase and blocks manual
 * URL jumps (navigate() guards). Global states render as banners: relay
 * unreachable, protocol version mismatch, host CLI not authenticated.
 */

export function App(): React.ReactElement {
  const sync = useSync();

  useEffect(() => {
    connect();
    return () => disconnect();
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

  const page =
    sync.route === "/ingest" ? (
      <Page1Ingestion />
    ) : sync.route === "/deliberate" ? (
      <Page2AvatarCanvas />
    ) : (
      <Page3SynthesisConfluence />
    );

  return (
    <div className="flex h-full flex-col">
      {sync.banner !== null && (
        <div className="pf-banner" style={{ background: "var(--pf-warn)", color: "#fff" }}>
          {sync.banner}
        </div>
      )}
      {sync.errorMessage !== null && (
        <div className="pf-banner" style={{ background: "var(--pf-danger)", color: "#fff" }} role="alert">
          {sync.errorMessage}
        </div>
      )}
      <header className="flex items-center justify-between border-b px-4 py-2" style={{ borderColor: "var(--pf-border)" }}>
        <span className="font-semibold">Plan-Forge</span>
        <span className="text-sm" style={{ color: "var(--pf-muted)" }}>
          phase: {sync.phase} · {sync.conn}
          {sync.role !== null ? ` · ${sync.role}` : ""}
        </span>
      </header>
      <main className="min-h-0 flex-1 overflow-hidden">{page}</main>
    </div>
  );
}

export default App;
