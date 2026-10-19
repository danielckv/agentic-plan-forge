import { FolderGit2, Users } from "lucide-react";

import type { SyncState } from "../store/syncStore";
import { CONN_META } from "../ui/conn";
import { PhaseStepper } from "../ui/PhaseStepper";

/**
 * Top bar: which project, where the session is (the phase stepper), and who
 * is here. The stepper replaces the old `repo / Phase` breadcrumb.
 */
export function TopBar({ sync }: { sync: SyncState }): React.ReactElement {
  const conn = CONN_META[sync.conn];
  const repo = sync.status.repo === "" ? "Plan Forge" : sync.status.repo;
  return (
    <div className="pf-topbar">
      <span className="pf-topbar-project" title={sync.status.repoPath === "" ? undefined : sync.status.repoPath}>
        <FolderGit2 size={14} className="shrink-0" />
        <span className="truncate">{repo}</span>
      </span>
      <span className="pf-topbar-divider" aria-hidden="true" />
      <PhaseStepper phase={sync.phase} />

      <div className="pf-topbar-right">
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
  );
}
