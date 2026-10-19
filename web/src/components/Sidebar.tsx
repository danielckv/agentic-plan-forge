import {
  BrainCircuit,
  Crown,
  FileText,
  FolderGit2,
  Keyboard,
  PanelLeftClose,
  PanelLeftOpen,
  ScrollText,
  Wifi,
  WifiOff,
} from "lucide-react";

import { BrandMark } from "../ui/BrandMark";

import { navigate, ROUTE_BY_PHASE, ROUTE_ORDER } from "../store/syncStore";
import type { SyncState } from "../store/syncStore";
import { CONN_META } from "../ui/conn";
import { prefs } from "../ui/prefs";
import { ThemeSelect } from "../ui/ThemeSelect";

const NAV_ITEMS: Array<{ route: string; label: string; icon: React.ComponentType<{ size?: number | string }> }> = [
  { route: "/ingest", label: "Ingestion", icon: FileText },
  { route: "/deliberate", label: "Deliberation", icon: BrainCircuit },
  { route: "/synthesis", label: "Synthesis", icon: ScrollText },
];

/**
 * Left rail. Expanded it shows the project, the three pages, who you are, the
 * connection, and the theme; collapsed it keeps every control as an icon with
 * a tooltip and an accessible name. The router guard is unchanged: a page the
 * phase has not reached stays disabled.
 */
export function Sidebar(props: {
  sync: SyncState;
  collapsed: boolean;
  onOpenHelp: () => void;
}): React.ReactElement {
  const { sync, collapsed, onOpenHelp } = props;
  const conn = CONN_META[sync.conn];
  const reached = ROUTE_ORDER[ROUTE_BY_PHASE[sync.phase]] ?? 0;
  const projectTitle = sync.status.repoPath === "" ? "Planning session" : sync.status.repoPath;
  const ConnIcon = sync.conn === "offline" || sync.conn === "closed" ? WifiOff : Wifi;
  const ToggleIcon = collapsed ? PanelLeftOpen : PanelLeftClose;
  const toggleLabel = collapsed ? "Expand sidebar" : "Collapse sidebar";

  return (
    <aside className="pf-sidebar" data-collapsed={collapsed} aria-label="Plan Forge">
      <div className="pf-sidebar-brand">
        <BrandMark size={26} />
        <div className="pf-sidebar-brand-text">
          <span className="pf-sidebar-title">Plan Forge</span>
          <span className="pf-sidebar-project" title={projectTitle}>
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

      <nav className="pf-sidebar-nav" aria-label="Session pages">
        <p className="pf-label pf-sidebar-heading">Session</p>
        {NAV_ITEMS.map((item) => {
          const Icon = item.icon;
          const locked = (ROUTE_ORDER[item.route] ?? 0) > reached;
          const active = sync.route === item.route;
          return (
            <button
              key={item.route}
              type="button"
              className="pf-nav-item"
              data-active={active}
              aria-current={active ? "page" : undefined}
              aria-label={item.label}
              disabled={locked}
              title={locked ? `${item.label} — reached when the session advances` : item.label}
              onClick={() => navigate(item.route)}
            >
              <Icon size={15} />
              <span className="pf-nav-label">{item.label}</span>
            </button>
          );
        })}
      </nav>

      <div className="pf-sidebar-foot">
        {sync.role !== null && (
          <span
            className="pf-sidebar-status"
            title={`Signed in as ${sync.role}`}
            aria-label={collapsed ? `Signed in as ${sync.role}` : undefined}
          >
            <Crown size={13} />
            <span className="pf-nav-label">Signed in as {sync.role}</span>
          </span>
        )}
        <span
          className="pf-sidebar-status"
          title={conn.label}
          aria-label={collapsed ? conn.label : undefined}
        >
          <ConnIcon size={13} />
          <span className="pf-nav-label">{conn.label}</span>
        </span>

        <ThemeSelect collapsed={collapsed} />

        <div className="pf-sidebar-actions">
          <button
            type="button"
            className="pf-rail-btn"
            aria-label="Keyboard shortcuts"
            aria-keyshortcuts="?"
            title="Keyboard shortcuts (?)"
            onClick={onOpenHelp}
          >
            <Keyboard size={15} />
            <span className="pf-nav-label">Shortcuts</span>
            <kbd className="pf-kbd pf-nav-label">?</kbd>
          </button>
          <button
            type="button"
            className="pf-rail-btn"
            aria-label={toggleLabel}
            aria-expanded={!collapsed}
            aria-keyshortcuts="["
            title={`${toggleLabel} ([)`}
            onClick={prefs.toggleSidebar}
          >
            <ToggleIcon size={15} />
            <span className="pf-nav-label">{collapsed ? "Expand" : "Collapse"}</span>
            <kbd className="pf-kbd pf-nav-label">[</kbd>
          </button>
        </div>
      </div>
    </aside>
  );
}
