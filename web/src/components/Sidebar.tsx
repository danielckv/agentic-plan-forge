import { BrainCircuit, FileText, PanelLeftClose, PanelLeftOpen, ScrollText } from "lucide-react";

import { BrandMark } from "../ui/BrandMark";

import { navigate, ROUTE_BY_PHASE, ROUTE_ORDER } from "../store/syncStore";
import type { SyncState } from "../store/syncStore";
import { prefs } from "../ui/prefs";

const NAV_ITEMS: Array<{ route: string; label: string; hint: string; icon: React.ComponentType<{ size?: number | string }> }> = [
  { route: "/ingest", label: "Ingestion", hint: "Write the requirements", icon: FileText },
  { route: "/deliberate", label: "Deliberation", hint: "The avatars plan", icon: BrainCircuit },
  { route: "/synthesis", label: "Synthesis", hint: "Review and approve", icon: ScrollText },
];

/**
 * Left rail: the brand at the shared header height, the three pages, and the
 * collapse control. Collapsed, it keeps every control as an icon with a
 * tooltip and an accessible name. The router guard is unchanged: a page the
 * phase has not reached stays disabled.
 */
export function Sidebar(props: { sync: SyncState; collapsed: boolean }): React.ReactElement {
  const { sync, collapsed } = props;
  const reached = ROUTE_ORDER[ROUTE_BY_PHASE[sync.phase]] ?? 0;
  const ToggleIcon = collapsed ? PanelLeftOpen : PanelLeftClose;
  const toggleLabel = collapsed ? "Expand sidebar" : "Collapse sidebar";

  return (
    <aside className="pf-sidebar" data-collapsed={collapsed} aria-label="Plan Forge">
      <div className="pf-sidebar-brand">
        <BrandMark size={28} title="Plan Forge" />
        <span className="pf-sidebar-title">Plan Forge</span>
      </div>

      <nav className="pf-sidebar-nav" aria-label="Session pages">
        <p className="pf-label pf-sidebar-heading">Session</p>
        {NAV_ITEMS.map((item, i) => {
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
              title={locked ? `${item.label} — reached when the session advances` : `${item.label}: ${item.hint}`}
              onClick={() => navigate(item.route)}
            >
              <Icon size={16} />
              <span className="pf-nav-label">
                <span className="pf-nav-step">{i + 1}</span>
                {item.label}
              </span>
            </button>
          );
        })}
      </nav>

      <div className="pf-sidebar-foot">
        <button
          type="button"
          className="pf-rail-btn"
          aria-label={toggleLabel}
          aria-expanded={!collapsed}
          aria-keyshortcuts="["
          title={`${toggleLabel} ([)`}
          onClick={prefs.toggleSidebar}
        >
          <ToggleIcon size={16} />
          <span className="pf-nav-label">{collapsed ? "Expand" : "Collapse"}</span>
          <kbd className="pf-kbd pf-nav-label">[</kbd>
        </button>
      </div>
    </aside>
  );
}
