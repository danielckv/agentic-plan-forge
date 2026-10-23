import { useEffect, useState } from "react";
import { Gauge } from "lucide-react";

import { fetchUsage, type UsageReport } from "../protocol/api";
import { byRole, byVendor, formatDuration, formatTokens, requestShare } from "../ui/usage";

/**
 * What the run has cost so far: the calls each generation host served, the
 * tokens it reported, and which avatar spent them.
 *
 * The numbers come from `GET /api/usage`, which the executor fills from the
 * host receipts. A host that reports no token counts shows dashes rather than
 * zeros, so an unreported number is never read as a measured one.
 */

const POLL_MS = 3000;

export function UsagePanel({ live }: { live: boolean }): React.ReactElement {
  const [report, setReport] = useState<UsageReport | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let stopped = false;
    const load = (): void => {
      fetchUsage()
        .then((r) => {
          if (stopped) return;
          setReport(r);
          setFailed(false);
        })
        .catch(() => {
          if (!stopped) setFailed(true);
        });
    };
    load();
    if (!live) return () => void (stopped = true);
    const timer = window.setInterval(load, POLL_MS);
    return () => {
      stopped = true;
      window.clearInterval(timer);
    };
  }, [live]);

  if (failed && report === null) {
    return (
      <div className="pf-rail-empty">
        <p className="pf-body">The usage report could not be read from this session.</p>
      </div>
    );
  }
  if (report === null || report.totals.requests === 0) {
    return (
      <div className="pf-rail-empty">
        <p className="pf-body">No provider call has finished yet. The first one appears here.</p>
      </div>
    );
  }

  const vendors = byVendor(report);
  const roles = byRole(report);
  const total = report.totals;

  return (
    <div className="pf-usage">
      <div className="pf-usage-total">
        <Gauge size={14} aria-hidden="true" />
        <span className="pf-section-title">
          {total.requests} {total.requests === 1 ? "call" : "calls"}
        </span>
        <span className="pf-caption">
          {formatTokens(total.input_tokens)} in · {formatTokens(total.output_tokens)} out ·{" "}
          {formatDuration(total.milliseconds)}
          {total.failures > 0 && ` · ${total.failures} failed`}
        </span>
      </div>

      <p className="pf-label">Per host</p>
      <ul className="pf-usage-list">
        {vendors.map((v) => (
          <li key={v.host} className="pf-usage-row">
            <div className="pf-usage-row-head">
              <span className="pf-usage-host">{v.host}</span>
              <span className="pf-usage-share">{requestShare(v.requests, total.requests)}%</span>
            </div>
            <div className="pf-usage-bar" aria-hidden="true">
              <span style={{ width: `${requestShare(v.requests, total.requests)}%` }} />
            </div>
            <div className="pf-usage-figures pf-caption">
              <span>
                {v.requests} {v.requests === 1 ? "call" : "calls"}
              </span>
              <span>{formatTokens(v.inputTokens)} in</span>
              <span>{formatTokens(v.outputTokens)} out</span>
              <span>{formatDuration(v.milliseconds)}</span>
              {v.failures > 0 && <span className="pf-usage-failed">{v.failures} failed</span>}
            </div>
            <div className="pf-usage-models">
              {v.models.map((m) => (
                <code key={m} className="pf-tag">
                  {m}
                </code>
              ))}
            </div>
          </li>
        ))}
      </ul>

      <p className="pf-label">Per avatar</p>
      <table className="pf-usage-table">
        <thead>
          <tr>
            <th scope="col">Role</th>
            <th scope="col">Calls</th>
            <th scope="col">In</th>
            <th scope="col">Out</th>
            <th scope="col">Time</th>
          </tr>
        </thead>
        <tbody>
          {roles.map((r) => (
            <tr key={r.role}>
              <th scope="row">{r.role}</th>
              <td>{r.requests}</td>
              <td>{formatTokens(r.inputTokens)}</td>
              <td>{formatTokens(r.outputTokens)}</td>
              <td>{formatDuration(r.milliseconds)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
