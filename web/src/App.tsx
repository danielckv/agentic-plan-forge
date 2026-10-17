import { useEffect } from "react";
import {
  BrainCircuit,
  Crown,
  FolderGit2,
  FileText,
  Layers,
  ScrollText,
  TriangleAlert,
  Users,
  Wifi,
  WifiOff,
} from "lucide-react";

import { apiFetch } from "./protocol/bridge";
import {
  applyStatus,
  useSync,
  navigate,
  connect,
  disconnect,
  ROUTE_ORDER,
} from "./store/syncStore";
import type { ConnState, Phase, StatusView } from "./store/syncStore";
import { Page1Ingestion } from "./pages/Page1Ingestion";
import { Page2AvatarCanvas } from "./pages/Page2AvatarCanvas";
import { Page3SynthesisConfluence } from "./pages/Page3SynthesisConfluence";

/**
 * Three routed views with explicit phase-driven transitions; never one
 * scrolling page. The router follows the authority phase and blocks manual
 * URL jumps (navigate() guards). Global states render as banners: relay
 * unreachable, protocol version mismatch, host CLI not authenticated.
 */

const PHASE_LABEL: Record<Phase, string> = {
  INGESTION: "Ingestion",
  DELIBERATION: "Deliberation",
  SYNTHESIS: "Synthesis",
  APPROVING: "Approving",
  APPROVING_UNKNOWN: "Approving",
  APPROVED: "Approved",
  CANCELLED: "Cancelled",
};

/**
 * A dropped socket is the normal case, not a failure: the authority restarts,
 * a cycle finishes, the machine sleeps. "closed" therefore reads as
 * reconnecting, and only a refused handshake ("offline") reads as broken.
 */
const CONN_META: Record<ConnState, { label: string; tone: string }> = {
  connecting: { label: "Connecting", tone: "pf-chip--warn pf-chip--live" },
  open: { label: "Connected", tone: "pf-chip--ok" },
  resyncing: { label: "Resyncing", tone: "pf-chip--warn pf-chip--live" },
  offline: { label: "Offline", tone: "pf-chip--danger" },
  closed: { label: "Reconnecting", tone: "pf-chip--warn pf-chip--live" },
};

const NAV_ITEMS: Array<{ route: string; label: string; icon: React.ComponentType<{ size?: number | string }> }> = [
  { route: "/ingest", label: "Ingestion", icon: FileText },
  { route: "/deliberate", label: "Deliberation", icon: BrainCircuit },
  { route: "/synthesis", label: "Synthesis", icon: ScrollText },
];

export function App(): React.ReactElement {
  const sync = useSync();

  useEffect(() => {
    connect();
    return () => disconnect();
  }, []);

  // The bridge owns the status pills and the repository identity. Polling
  // lives here, not on Page 1, so the project stays named on every page and
  // survives a reload straight into deliberation or synthesis.
  useEffect(() => {
    let cancelled = false;
    const poll = async (): Promise<void> => {
      try {
        const res = await apiFetch("/api/status");
        if (!res.ok || cancelled) return;
        applyStatus((await res.json()) as Partial<StatusView>);
      } catch {
        /* the authority is away: the last known status stays on screen */
      }
    };
    void poll();
    const id = window.setInterval(() => void poll(), 5000);
    return () => {
      cancelled = true;
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

  const page =
    sync.route === "/ingest" ? (
      <Page1Ingestion />
    ) : sync.route === "/deliberate" ? (
      <Page2AvatarCanvas />
    ) : (
      <Page3SynthesisConfluence />
    );

  const conn = CONN_META[sync.conn];
  const reached = ROUTE_ORDER[sync.route] ?? 0;

  return (
    <div className="flex h-full">
      {/* Jira-style dark left rail */}
      <aside className="pf-sidebar">
        <div className="pf-sidebar-brand">
          <span className="pf-brand-mark">
            <Layers size={16} strokeWidth={2.2} />
          </span>
          <div className="flex min-w-0 flex-col leading-tight">
            <span className="text-[14px] font-semibold" style={{ color: "var(--pf-sidebar-ink-strong)" }}>
              Plan Forge
            </span>
            <span
              className="flex items-center gap-1 truncate text-[11px]"
              style={{ color: "var(--pf-sidebar-ink)" }}
              title={sync.status.repoPath === "" ? "Planning session" : sync.status.repoPath}
            >
              {sync.status.repo === "" ? (
                "Planning session"
              ) : (
                <>
                  <FolderGit2 size={11} className="shrink-0" />
                  <span className="truncate">{sync.status.repo}</span>
                </>
              )}
            </span>
          </div>
        </div>

        <nav className="pf-sidebar-nav">
          <p className="pf-label px-2.5 pt-1 pb-2" style={{ color: "var(--pf-sidebar-ink)" }}>
            Session
          </p>
          {NAV_ITEMS.map((item) => {
            const Icon = item.icon;
            const locked = (ROUTE_ORDER[item.route] ?? 0) > reached;
            return (
              <button
                key={item.route}
                type="button"
                className="pf-nav-item"
                data-active={sync.route === item.route}
                disabled={locked}
                title={locked ? "Reached when the session advances" : item.label}
                onClick={() => navigate(item.route)}
              >
                <Icon size={15} />
                {item.label}
              </button>
            );
          })}
        </nav>

        <div className="mt-auto flex flex-col gap-2 border-t px-4 py-4" style={{ borderColor: "var(--pf-sidebar-border)" }}>
          {sync.role !== null && (
            <span className="flex items-center gap-2 text-[12px]" style={{ color: "var(--pf-sidebar-ink)" }}>
              <Crown size={12} />
              Signed in as {sync.role}
            </span>
          )}
          <span className="flex items-center gap-2 text-[12px]" style={{ color: "var(--pf-sidebar-ink)" }}>
            {sync.conn === "offline" || sync.conn === "closed" ? <WifiOff size={12} /> : <Wifi size={12} />}
            {conn.label}
          </span>
        </div>
      </aside>

      {/* Content column */}
      <div className="flex min-w-0 flex-1 flex-col">
        <div className="pf-topbar">
          <nav className="pf-breadcrumb" aria-label="Session phase">
            <span>{sync.status.repo === "" ? "Plan Forge" : sync.status.repo}</span>
            <span aria-hidden="true">/</span>
            <span className="pf-breadcrumb-current">{PHASE_LABEL[sync.phase]}</span>
          </nav>

          <div className="ml-auto flex items-center gap-2.5">
            {sync.members.length > 0 && (
              <span className="pf-avatar-stack" title={sync.members.join(", ")}>
                {sync.members.slice(0, 5).map((m) => (
                  <span key={m} className="pf-avatar">
                    {(m.includes(":") ? m.slice(m.indexOf(":") + 1) : m).slice(0, 2).toUpperCase()}
                  </span>
                ))}
              </span>
            )}
            {sync.members.length > 0 && (
              <span className="pf-chip">
                <Users size={11} />
                {sync.members.length}
              </span>
            )}
            <span className={`pf-chip ${conn.tone}`}>
              <span className="pf-dot" />
              {conn.label}
            </span>
          </div>
        </div>

        {sync.banner !== null && (
          <div className="pf-banner" style={{ background: "var(--pf-warn-bright)" }}>
            <TriangleAlert size={14} />
            {sync.banner}
          </div>
        )}
        {sync.errorMessage !== null && (
          <div className="pf-banner" style={{ background: "var(--pf-danger-bright)" }} role="alert">
            <TriangleAlert size={14} />
            {sync.errorMessage}
          </div>
        )}

        <main className="min-h-0 flex-1 overflow-hidden">{page}</main>
      </div>
    </div>
  );
}

export default App;
