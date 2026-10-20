import { useCallback, useEffect, useSyncExternalStore } from "react";
import { Activity, LayoutGrid, Settings2 } from "lucide-react";

import { BrandMark } from "../ui/BrandMark";
import { ThemeSelect } from "../ui/ThemeSelect";
import { LivePage } from "./LivePage";
import { CENTER_ROUTES, DEFAULT_ROUTE, ROUTE_LABEL, routeFromPath } from "./route";
import type { CenterRoute } from "./route";
import { SetupPage } from "./SetupPage";
import { SprintPage } from "./SprintPage";
import { getState, start, stop, subscribe } from "./store";
import type { ConnState } from "./store";

const ROUTE_ICON: Record<CenterRoute, typeof Activity> = {
  "/live": Activity,
  "/sprint": LayoutGrid,
  "/setup": Settings2,
};

/** The address bar is the single source of truth for which page is showing. */
function useRoute(): [CenterRoute, (next: CenterRoute) => void] {
  const route = useSyncExternalStore(
    (cb) => {
      window.addEventListener("popstate", cb);
      return () => {
        window.removeEventListener("popstate", cb);
      };
    },
    () => routeFromPath(window.location.pathname),
    () => DEFAULT_ROUTE,
  );
  const navigate = useCallback((next: CenterRoute) => {
    if (routeFromPath(window.location.pathname) === next) return;
    window.history.pushState(null, "", next);
    window.dispatchEvent(new PopStateEvent("popstate"));
  }, []);
  return [route, navigate];
}

const CONN_LABEL: Record<ConnState, string> = {
  connecting: "Connecting",
  live: "Live",
  offline: "Offline",
  parked: "Not connected",
};

function ConnChip({ conn }: { conn: ConnState }): React.JSX.Element {
  return (
    <span className={`pf-center-conn pf-center-conn--${conn}`}>
      <span className="pf-center-conn-dot" aria-hidden="true" />
      {CONN_LABEL[conn]}
    </span>
  );
}

/**
 * The parked state. A tab opened by hand has no launch ticket, and the daemon
 * will not mint a session token without one, so this is a designed destination
 * rather than an error: it names the one command that fixes it.
 */
function Parked(): React.JSX.Element {
  return (
    <div className="pf-center-empty pf-center-empty--page">
      <h1 className="pf-h2">This tab was not opened by Plan Forge</h1>
      <p className="pf-body">
        The command center hands each tab a single-use launch ticket, so it will not start a session
        for a tab that arrived on its own. Open one from your terminal:
      </p>
      <pre className="pf-center-code">plan-forge center open</pre>
      <p className="pf-caption">It starts the daemon if it is not running, then opens a tab on it.</p>
    </div>
  );
}

export function CenterApp(): React.JSX.Element {
  const [route, navigate] = useRoute();
  const state = useSyncExternalStore(subscribe, getState, getState);

  useEffect(() => {
    void start();
    return stop;
  }, []);

  return (
    <div className="pf-center">
      <header className="pf-center-header">
        <div className="pf-center-header-inner">
          <div className="pf-center-brand">
            <BrandMark size={26} />
            <span className="pf-center-brand-name">Plan Forge</span>
            <span className="pf-center-brand-sub">Command center</span>
          </div>
          <nav className="pf-center-nav" aria-label="Command center">
            {CENTER_ROUTES.map((r) => {
              const Icon = ROUTE_ICON[r];
              const current = r === route;
              return (
                <button
                  key={r}
                  type="button"
                  className={`pf-center-nav-item${current ? " is-current" : ""}`}
                  aria-current={current ? "page" : undefined}
                  onClick={() => {
                    navigate(r);
                  }}
                >
                  <Icon size={15} aria-hidden="true" />
                  {ROUTE_LABEL[r]}
                </button>
              );
            })}
          </nav>
          <div className="pf-center-header-right">
            <ConnChip conn={state.conn} />
            <ThemeSelect collapsed={false} />
          </div>
        </div>
      </header>

      {state.error && state.conn !== "parked" ? (
        <div className="pf-center-notice" role="status">
          {state.error}
        </div>
      ) : null}

      <main className="pf-center-main">
        <div className="pf-center-main-inner">
          {state.parked ? (
            <Parked />
          ) : route === "/sprint" ? (
            <SprintPage sprint={state.sprint} conn={state.conn} />
          ) : route === "/setup" ? (
            <SetupPage />
          ) : (
            <LivePage snapshot={state.snapshot} sprint={state.sprint} conn={state.conn} />
          )}
        </div>
      </main>
    </div>
  );
}
