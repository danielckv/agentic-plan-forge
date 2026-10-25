import { useCallback, useEffect, useState } from "react";
import {
  CircleCheck,
  FileCode,
  LoaderCircle,
  Monitor,
  RefreshCw,
  Rocket,
  Square,
  TriangleAlert,
  Trash2,
} from "lucide-react";

import {
  changeCount,
  fetchDeployTarget,
  fetchWorktreeFile,
  isRunning,
  postBuild,
  stageLabel,
  useBuild,
} from "../ui/build";
import type { BuildView, DeployTargetView, FileContent } from "../ui/build";
import { Dialog } from "../ui/Dialog";
import { toastStore } from "../ui/toasts";

/**
 * Page 4, after the plan is approved: drive the host agent to implement it in
 * an isolated worktree, read back what it wrote, run it in a container, and
 * deploy.
 *
 * The page never writes to the repository the owner is working in. Everything
 * it shows comes out of a git worktree under the Plan Forge cache, which is
 * also the only directory the coding agent is given.
 */
export function Page4Build(): React.ReactElement {
  const { view, refresh } = useBuild(true);
  const [tab, setTab] = useState<"code" | "preview">("code");
  const [target, setTarget] = useState<DeployTargetView | null>(null);
  const [askOpen, setAskOpen] = useState(false);

  useEffect(() => {
    void (async () => {
      const got = await fetchDeployTarget();
      setTarget(got);
      // The first time only: the owner has never said where a build should go,
      // so the question is asked rather than a target assumed.
      if (got !== null && !got.asked) setAskOpen(true);
    })();
  }, []);

  const act = useCallback(
    async (path: string, body?: unknown): Promise<void> => {
      const err = await postBuild(path, body);
      if (err !== null) toastStore.push("error", err);
      await refresh();
    },
    [refresh],
  );

  const chooseTarget = useCallback(
    async (id: string): Promise<void> => {
      const err = await postBuild("/api/deploy/target", { target: id });
      if (err !== null) {
        toastStore.push("error", err);
        return;
      }
      setTarget(await fetchDeployTarget());
      setAskOpen(false);
      await refresh();
    },
    [refresh],
  );

  const busy = isRunning(view);
  const hasCode = view?.changes !== undefined && view.changes.files.length > 0;

  return (
    <section className="pf-build" aria-labelledby="pf-build-title">
      <header className="pf-build-head">
        <span className="pf-build-badge" data-stage={view?.stage ?? "IDLE"}>
          {busy ? <LoaderCircle size={18} className="pf-spin" /> : <CircleCheck size={18} />}
        </span>
        <div className="min-w-0">
          <h2 id="pf-build-title" className="pf-section-title">
            Build &amp; deploy
          </h2>
          <p className="pf-caption mt-0.5">{stageLabel(view)}</p>
        </div>
        <div className="pf-build-actions">
          <BuildButtons view={view} busy={busy} act={act} />
        </div>
      </header>

      {view?.error !== undefined && view.error !== "" && (
        <div className="pf-banner pf-banner--danger" role="alert">
          <TriangleAlert size={14} />
          {view.error}
        </div>
      )}

      {view?.worktree !== undefined && view.worktree !== "" && (
        <p className="pf-caption pf-build-worktree">
          Worktree <code className="pf-mono">{view.worktree}</code> on branch{" "}
          <code className="pf-mono">{view.branch}</code>
        </p>
      )}

      <nav className="pf-build-tabs" role="tablist" aria-label="Build view">
        <button
          type="button"
          role="tab"
          aria-selected={tab === "code"}
          className="pf-build-tab"
          onClick={() => setTab("code")}
        >
          <FileCode size={14} /> Code
          {hasCode && <span className="pf-build-tab-count">{view.changes?.files.length}</span>}
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === "preview"}
          className="pf-build-tab"
          onClick={() => setTab("preview")}
        >
          <Monitor size={14} /> Preview
        </button>
      </nav>

      {tab === "code" ? <CodeTab view={view} /> : <PreviewTab view={view} act={act} />}

      <DeployBar view={view} target={target} onChange={chooseTarget} onAsk={() => setAskOpen(true)} act={act} />

      <Dialog
        open={askOpen}
        onClose={() => setAskOpen(false)}
        title="Where should a built plan go?"
        description="Asked once. Change it any time with the toggle on this page or `plan-forge config`."
      >
        <ul className="pf-target-choices">
          {(target?.options ?? []).map((option) => (
            <li key={option.id}>
              <button
                type="button"
                className="pf-target-choice"
                disabled={!option.ok}
                onClick={() => void chooseTarget(option.id)}
              >
                <span className="pf-target-choice-name">{option.name}</span>
                <span className="pf-caption">
                  {option.ok ? targetBlurb(option.id) : option.why}
                </span>
              </button>
            </li>
          ))}
        </ul>
      </Dialog>
    </section>
  );
}

function targetBlurb(id: string): string {
  if (id === "agent-box") return "Run the project in an Agent Box sandbox (agent-box.sh).";
  if (id === "docker") return "Run the built image on the local Docker daemon.";
  return "Build and preview only; never deploy.";
}

/** The action buttons, which depend only on the stage the binary reports. */
function BuildButtons(props: {
  view: BuildView | null;
  busy: boolean;
  act: (path: string, body?: unknown) => Promise<void>;
}): React.ReactElement {
  const { view, busy, act } = props;
  if (busy) {
    return (
      <button type="button" className="pf-btn" onClick={() => void act("/api/build/cancel")}>
        <Square size={14} /> Stop
      </button>
    );
  }
  const coded = view?.changes !== undefined;
  return (
    <>
      <button type="button" className="pf-btn pf-btn--primary" onClick={() => void act("/api/build/code")}>
        <RefreshCw size={14} /> {coded ? "Re-run coding" : "Write the code"}
      </button>
      {coded && (
        <button type="button" className="pf-btn" onClick={() => void act("/api/build/discard")}>
          <Trash2 size={14} /> Discard worktree
        </button>
      )}
      <button type="button" className="pf-btn" onClick={() => void act("/api/build/finish")}>
        Finish
      </button>
    </>
  );
}

/** Code tab: the agent's steps, the changed files, one file, and the diff. */
function CodeTab(props: { view: BuildView | null }): React.ReactElement {
  const { view } = props;
  const [open, setOpen] = useState<FileContent | null>(null);

  const show = useCallback(async (path: string): Promise<void> => {
    const file = await fetchWorktreeFile(path);
    if (file === null) {
      toastStore.push("error", `could not read ${path}`);
      return;
    }
    setOpen(file);
  }, []);

  if (view === null) return <p className="pf-caption">Loading…</p>;
  const changes = view.changes;

  return (
    <div className="pf-build-body">
      <div className="pf-build-side">
        <h3 className="pf-build-sub">
          Changed files
          {changes !== undefined && (
            <span className="pf-caption">
              {" "}
              +{changes.added} −{changes.removed}
            </span>
          )}
        </h3>
        {changes === undefined || changes.files.length === 0 ? (
          <p className="pf-caption">
            Nothing written yet. “Write the code” hands the approved plan to the agent.
          </p>
        ) : (
          <ul className="pf-file-list">
            {changes.files.map((file) => (
              <li key={file.path}>
                <button type="button" className="pf-file-row" onClick={() => void show(file.path)}>
                  <span className="pf-file-status" data-status={file.status}>
                    {file.status || "?"}
                  </span>
                  <span className="pf-file-path pf-mono">{file.path}</span>
                  <span className="pf-caption">{changeCount(file)}</span>
                </button>
              </li>
            ))}
          </ul>
        )}
        {changes?.truncated.map((note) => (
          <p key={note} className="pf-caption pf-build-trunc">
            {note}
          </p>
        ))}

        <h3 className="pf-build-sub">What the agent did</h3>
        <ol className="pf-step-log">
          {view.steps.length === 0 ? (
            <li className="pf-caption">No steps yet.</li>
          ) : (
            view.steps.slice(-200).map((step, i) => (
              <li key={`${step.at}-${String(i)}`} className="pf-step-row">
                {step.text}
              </li>
            ))
          )}
        </ol>
      </div>

      <div className="pf-build-main">
        <h3 className="pf-build-sub">Diff</h3>
        {changes === undefined || changes.diff === "" ? (
          <p className="pf-caption">No diff yet.</p>
        ) : (
          <pre className="pf-diff pf-mono">
            {changes.diff.split("\n").map((line, i) => (
              <span key={`${String(i)}-${line.slice(0, 12)}`} className="pf-diff-line" data-kind={diffKind(line)}>
                {line}
                {"\n"}
              </span>
            ))}
          </pre>
        )}
      </div>

      <Dialog
        open={open !== null}
        onClose={() => setOpen(null)}
        title={open?.path ?? ""}
        size="doc"
        description={open === null ? "" : `${String(open.bytes)} bytes${open.truncated ? " (truncated)" : ""}`}
      >
        {open?.binary === true ? (
          <p className="pf-caption">Binary file; not shown.</p>
        ) : (
          <pre className="pf-file-view pf-mono">{open?.text}</pre>
        )}
      </Dialog>
    </div>
  );
}

/** One diff line's kind, for colouring. A header is neither an add nor a cut. */
function diffKind(line: string): "add" | "del" | "meta" | "ctx" {
  if (line.startsWith("+++") || line.startsWith("---") || line.startsWith("@@")) return "meta";
  if (line.startsWith("diff ") || line.startsWith("index ")) return "meta";
  if (line.startsWith("+")) return "add";
  if (line.startsWith("-")) return "del";
  return "ctx";
}

/** Preview tab: the built container, its address and its build log. */
function PreviewTab(props: {
  view: BuildView | null;
  act: (path: string, body?: unknown) => Promise<void>;
}): React.ReactElement {
  const { view, act } = props;
  const [port, setPort] = useState("");

  if (view === null) return <p className="pf-caption">Loading…</p>;
  const box = view.box;
  const live = box?.url !== undefined && box.url !== "" && view.box_running;

  return (
    <div className="pf-build-body">
      <div className="pf-build-main">
        <div className="pf-preview-bar">
          <label className="pf-caption" htmlFor="pf-preview-port">
            Container port
          </label>
          <input
            id="pf-preview-port"
            className="pf-input pf-input--port"
            inputMode="numeric"
            placeholder="8080"
            value={port}
            onChange={(e) => setPort(e.target.value.replace(/\D/g, "").slice(0, 5))}
          />
          <button
            type="button"
            className="pf-btn pf-btn--primary"
            disabled={isRunning(view)}
            onClick={() => void act("/api/build/preview", { port: port === "" ? 0 : Number(port) })}
          >
            <Monitor size={14} /> Build &amp; run
          </button>
          {box !== undefined && (
            <button type="button" className="pf-btn" onClick={() => void act("/api/build/preview/stop")}>
              <Square size={14} /> Stop
            </button>
          )}
          {live && (
            <a className="pf-btn" href={box.url} target="_blank" rel="noreferrer">
              Open {box.url}
            </a>
          )}
        </div>

        {live ? (
          /*
           * Code a model wrote minutes ago. No allow-same-origin: the
           * container gets an opaque origin and cannot read this window.
           */
          <iframe
            className="pf-preview-frame"
            title="Preview of the built plan"
            src={box.url}
            sandbox="allow-scripts allow-forms allow-popups"
          />
        ) : (
          <p className="pf-caption">
            {box === undefined
              ? "Nothing built yet. “Build & run” builds the worktree's Dockerfile and starts it on loopback."
              : "The container is not running. The build log is below."}
          </p>
        )}
      </div>

      <div className="pf-build-side">
        <h3 className="pf-build-sub">Build log</h3>
        <pre className="pf-build-log pf-mono">
          {view.log.length === 0 ? "No output yet." : view.log.slice(-400).join("\n")}
        </pre>
      </div>
    </div>
  );
}

/** The deploy toggle and the Deploy button. */
function DeployBar(props: {
  view: BuildView | null;
  target: DeployTargetView | null;
  onChange: (id: string) => Promise<void>;
  onAsk: () => void;
  act: (path: string, body?: unknown) => Promise<void>;
}): React.ReactElement {
  const { view, target, onChange, act } = props;
  const chosen = target?.target ?? "";
  const canDeploy =
    view !== null && !isRunning(view) && chosen !== "" && chosen !== "none" && view.target_ready === undefined;

  return (
    <footer className="pf-deploy-bar">
      <div className="pf-deploy-toggle" role="radiogroup" aria-label="Deploy target">
        {(target?.options ?? []).map((option) => (
          <button
            key={option.id}
            type="button"
            role="radio"
            aria-checked={chosen === option.id}
            className="pf-deploy-opt"
            disabled={!option.ok}
            title={option.ok ? targetBlurb(option.id) : option.why}
            onClick={() => void onChange(option.id)}
          >
            {option.name}
          </button>
        ))}
      </div>

      {view?.target_ready !== undefined && (
        <span className="pf-caption pf-deploy-warn">
          <TriangleAlert size={13} /> {view.target_ready}
        </span>
      )}

      {view?.deployment !== undefined && (
        <span className="pf-caption">
          Deployed{view.deployment.url !== undefined && view.deployment.url !== "" ? ": " : ""}
          {view.deployment.url !== undefined && view.deployment.url !== "" && (
            <a href={view.deployment.url} target="_blank" rel="noreferrer">
              {view.deployment.url}
            </a>
          )}{" "}
          {view.deployment.detail}
        </span>
      )}

      <button
        type="button"
        className="pf-btn pf-btn--primary"
        disabled={!canDeploy}
        onClick={() => void act("/api/deploy")}
      >
        <Rocket size={14} /> Deploy
      </button>
    </footer>
  );
}
