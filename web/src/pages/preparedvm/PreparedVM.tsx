import { useCallback, useMemo, useState } from "react";
import { CircleCheck, ExternalLink, LoaderCircle, Play, Square, TriangleAlert } from "lucide-react";

import {
  costLine,
  failingReceipts,
  previewAvailable,
  sandboxHeadline,
  sandboxLive,
  sourceSummary,
  useRecipe,
  useSandbox,
  type LaunchReport,
  type RecipeCommand,
  type RecipeView,
} from "../../ui/sandbox";

/** How a command reads in the table: argv joined for display only. */
export function commandLine(cmd: RecipeCommand | undefined): string {
  if (cmd === undefined || cmd.argv.length === 0) return "not configured";
  return cmd.argv.join(" ");
}

/**
 * Whether a launch may be sent. The confirmation is the owner's, the recipe has
 * to be recorded, and the sandbox must not already be live: a second click must
 * never buy a second VM.
 */
export function canLaunch(recipe: RecipeView | null, report: LaunchReport | null, confirmed: boolean): boolean {
  if (!confirmed) return false;
  if (recipe === null || !recipe.available) return false;
  if (report !== null && sandboxLive(report)) return false;
  return true;
}

/** A receipt as a row: a step with no receipt reads "not run", never "passed". */
export function receiptRows(report: LaunchReport | null): Array<{ kind: string; command: string; result: string; ok: boolean }> {
  const recorded = report?.receipts ?? [];
  const wanted = ["build", "test", "run"];
  return wanted.map((kind) => {
    const receipt = recorded.find((r) => r.Kind.toLowerCase() === kind || (kind === "run" && r.Kind.toLowerCase() === "readiness"));
    if (receipt === undefined) return { kind, command: "", result: "not run", ok: false };
    return { kind, command: receipt.Command, result: `exit ${String(receipt.ExitCode)}`, ok: receipt.ExitCode === 0 };
  });
}

/** The time left before the provider closes the sandbox. */
export function expiryLine(report: LaunchReport | null, now: number): string {
  if (report?.expiresAt === undefined) return "";
  const at = Date.parse(report.expiresAt);
  if (!Number.isFinite(at)) return "";
  const left = Math.max(0, at - now);
  const minutes = Math.floor(left / 60_000);
  return left === 0 ? "expired" : `${String(minutes)} min left`;
}

/**
 * The prepared-VM panel: the recorded commands, a confirmed launch, the
 * receipts and the authenticated preview. It never claims a step passed: a
 * missing receipt reads "not run", and a failing one is called out.
 */
export function PreparedVM(props: { approvalId: string; enabled: boolean }): React.ReactElement {
  const { approvalId, enabled } = props;
  const { view: recipe } = useRecipe(enabled);
  const sandbox = useSandbox(enabled);
  const [confirmed, setConfirmed] = useState(false);
  const [port, setPort] = useState("8080");
  const [error, setError] = useState<string | null>(null);
  const report = sandbox.report;

  const failing = useMemo(() => failingReceipts(report), [report]);
  const rows = useMemo(() => receiptRows(report), [report]);
  const appPort = Number(port);

  const launch = useCallback(async () => {
    setError(null);
    // The launch pins the recipe the owner reviewed. An empty value means the
    // page had none, and the Go side then refuses rather than guessing.
    const recipeHash = recipe?.hash ?? "";
    const problem = await sandbox.launch({
      confirmed: true,
      approvalId,
      appPort,
      recipeHash,
      costAcknowledged: costLine(null),
    });
    if (problem !== null) setError(problem);
  }, [approvalId, appPort, recipe, sandbox]);

  const sourceLine = sourceSummary(report);

  return (
    <section className="pf-vm" aria-labelledby="pf-vm-title">
      <header className="pf-vm-head">
        <h2 className="pf-section-title" id="pf-vm-title">
          Prepared VM
        </h2>
        <span className="pf-caption" aria-live="polite">
          {sandboxHeadline(report, recipe)}
        </span>
      </header>

      {recipe !== null && !recipe.available && (
        <p className="pf-notice pf-notice--warn" role="status">
          <TriangleAlert size={14} aria-hidden="true" />
          <span>{recipe.problem ?? "No build command is recorded for this project."}</span>
        </p>
      )}

      {sourceLine !== null && (
        <p className="pf-caption pf-vm-source" role="status">
          {sourceLine}
        </p>
      )}

      {recipe?.recipe !== undefined && (
        <table className="pf-vm-commands">
          <caption className="pf-caption">
            Recorded from this repository. The sandbox runs exactly these, in this order.
          </caption>
          <tbody>
            {(["build", "test", "run"] as const).map((step) => {
              const cmd = recipe.recipe?.[step];
              return (
                <tr key={step}>
                  <th scope="row">{step}</th>
                  <td>
                    <code className="pf-mono">{commandLine(cmd)}</code>
                    {cmd !== undefined && cmd.source_ref !== undefined && (
                      <span className="pf-caption"> from {cmd.source_ref}</span>
                    )}
                    {cmd !== undefined && (
                      <span className="pf-caption">
                        {" "}
                        · {cmd.dir} · {String(cmd.timeout_seconds)}s
                      </span>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}

      {recipe?.available === true && !sandboxLive(report) && (
        <div className="pf-vm-launch">
          <label className="pf-caption">
            <input
              type="checkbox"
              checked={confirmed}
              onChange={(e) => setConfirmed(e.currentTarget.checked)}
            />{" "}
            I reviewed these commands and want one sandbox, for one hour.
          </label>
          <label className="pf-caption">
            Port{" "}
            <input
              className="pf-input"
              inputMode="numeric"
              value={port}
              onChange={(e) => setPort(e.currentTarget.value.replace(/[^0-9]/g, ""))}
              aria-label="Port the preview serves"
            />
          </label>
          <span className="pf-caption">{costLine(null)}</span>
          <button
            type="button"
            className="pf-btn pf-btn-primary pf-btn-sm"
            disabled={!canLaunch(recipe, report, confirmed) || !Number.isInteger(appPort) || appPort < 1}
            onClick={() => void launch()}
          >
            <Play size={13} aria-hidden="true" /> Launch sandbox
          </button>
        </div>
      )}

      {error !== null && (
        <p className="pf-notice pf-notice--danger" role="alert">
          {error}
        </p>
      )}

      {report !== null && (
        <>
          {failing.length > 0 && (
            <p className="pf-notice pf-notice--danger" role="alert">
              <TriangleAlert size={14} aria-hidden="true" />
              <span>
                {failing.length} step{failing.length === 1 ? "" : "s"} did not pass. The preview stays closed.
              </span>
            </p>
          )}
          <table className="pf-vm-receipts">
            <caption className="pf-caption">
              Every step, with its own exit code. A step with no receipt was not run.
            </caption>
            <tbody>
              {rows.map((row) => (
                <tr key={row.kind} data-ok={row.ok}>
                  <th scope="row">{row.kind}</th>
                  <td>
                    <code className="pf-mono">{row.command === "" ? "—" : row.command}</code>
                  </td>
                  <td>{row.ok ? <CircleCheck size={13} aria-label="passed" /> : row.result}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <div className="pf-vm-actions">
            {report.state === "ready" && (
              <button type="button" className="pf-btn pf-btn-secondary pf-btn-sm" onClick={() => void sandbox.refresh()}>
                <LoaderCircle size={13} aria-hidden="true" /> Refresh state
              </button>
            )}
            {previewAvailable(report) && report.preview !== undefined && (
              <a
                className="pf-btn pf-btn-primary pf-btn-sm"
                href={report.preview}
                target="_blank"
                rel="noreferrer"
              >
                <ExternalLink size={13} aria-hidden="true" /> Open preview
              </a>
            )}
            {sandboxLive(report) && (
              <button type="button" className="pf-btn pf-btn-secondary pf-btn-sm" onClick={() => void sandbox.stop()}>
                <Square size={13} aria-hidden="true" /> Stop
              </button>
            )}
            {report.expiresAt !== undefined && <span className="pf-caption">{expiryLine(report, Date.now())}</span>}
          </div>
          {report.cleanup !== undefined && report.cleanup !== "" && (
            <p className="pf-notice pf-notice--warn" role="status">
              {report.cleanup}
            </p>
          )}
        </>
      )}
    </section>
  );
}
