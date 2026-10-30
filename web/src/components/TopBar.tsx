import { FolderGit2, GitBranch, Keyboard, Network, SlidersHorizontal } from "lucide-react";

import { openGraph, type SyncState } from "../store/syncStore";
import { CONN_META } from "../ui/conn";
import { PhaseStepper } from "../ui/PhaseStepper";
import { ThemeSelect } from "../ui/ThemeSelect";

/**
 * Top bar, one row at the shared header height: the project and branch, the
 * phase stepper, then who is here, the connection, the theme and the
 * shortcut sheet. Every control is 32 px tall and centered on the same line.
 */
export function TopBar({
  sync,
  onOpenHelp,
  onOpenSetup,
}: {
  sync: SyncState;
  onOpenHelp: () => void;
  onOpenSetup: () => void;
}): React.ReactElement {
  const conn = CONN_META[sync.conn];
  const repo = sync.status.repo === "" ? "Plan Forge" : sync.status.repo;
  const branch = sync.status.branch ?? "";
  const you = sync.role === null ? null : `You are the ${sync.role}`;
  return (
    <header className="pf-topbar">
      <span className="pf-topbar-project" title={sync.status.repoPath === "" ? undefined : sync.status.repoPath}>
        <FolderGit2 size={15} className="shrink-0" />
        <span className="truncate">{repo}</span>
        {branch !== "" && (
          <span className="pf-topbar-branch" title={`Branch ${branch}`}>
            <GitBranch size={12} className="shrink-0" />
            <span className="truncate">{branch}</span>
          </span>
        )}
      </span>
      <span className="pf-topbar-divider" aria-hidden="true" />
      <PhaseStepper phase={sync.phase} />

      <div className="pf-topbar-right">
        {sync.members.length > 0 && (
          <span className="pf-avatar-stack" title={[you, ...sync.members].filter((m) => m !== null).join("\n")}>
            {sync.members.slice(0, 4).map((m) => (
              <span key={m} className="pf-avatar">
                {(m.includes(":") ? m.slice(m.indexOf(":") + 1) : m).slice(0, 2).toUpperCase()}
              </span>
            ))}
            {sync.members.length > 4 && <span className="pf-avatar pf-avatar--more">+{sync.members.length - 4}</span>}
          </span>
        )}
        <span className={`pf-conn ${conn.tone}`} title={you ?? undefined} role="status">
          <span className="pf-dot" />
          {conn.label}
        </span>
        <span className="pf-topbar-divider" aria-hidden="true" />
        <ThemeSelect variant="compact" />
        <button
          type="button"
          className="pf-icon-btn"
          aria-label="Setup: hosts, tools, skills and hooks"
          title="Setup — hosts, tools, skills and hooks"
          onClick={onOpenSetup}
        >
          <SlidersHorizontal size={16} />
        </button>
        <button
          type="button"
          className="pf-icon-btn"
          aria-label="Component graph: files, folders and their imports"
          title="Component graph — files, folders and their imports"
          onClick={() => openGraph()}
        >
          <Network size={16} />
        </button>
        <button
          type="button"
          className="pf-icon-btn"
          aria-label="Keyboard shortcuts"
          aria-keyshortcuts="?"
          title="Keyboard shortcuts (?)"
          onClick={onOpenHelp}
        >
          <Keyboard size={16} />
        </button>
      </div>
    </header>
  );
}
