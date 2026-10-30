import { useCallback, useEffect, useState } from "react";
import { TriangleAlert } from "lucide-react";

import { gapAgentLabel, loadSettings, MAX_GAP_AGENTS_CHOICES, saveMaxGapAgents } from "../ui/settings";
import type { SettingsDocument } from "../ui/settings";

/**
 * The bounded runtime limits the window may change, starting with the number of
 * expertise-gap agents a run can add.
 *
 * The value lives in the same settings document `plan-forge config` writes, so
 * this section reads it through `GET /api/settings` and saves the whole
 * document back with the revision it read. A refused save (a stale revision, a
 * value the server rejects) leaves the old value on screen and says why: the
 * control never reports a change the binary did not accept.
 */
export function RuntimeLimitsSection(): React.ReactElement {
  const [settings, setSettings] = useState<SettingsDocument | null>(null);
  const [message, setMessage] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const reload = useCallback(async (): Promise<void> => {
    const result = await loadSettings();
    if (result.ok) {
      setSettings(result.settings);
      setMessage(null);
      return;
    }
    setSettings(null);
    setMessage(result.message);
  }, []);

  // The section mounts only while the settings dialog is open, so this is one
  // read per visit, never a poll.
  useEffect(() => {
    void reload();
  }, [reload]);

  const choose = useCallback(
    async (value: number): Promise<void> => {
      if (settings === null) return;
      setBusy(true);
      const result = await saveMaxGapAgents(settings, value);
      setBusy(false);
      if (result.ok) {
        setSettings(result.settings);
        setMessage(null);
        return;
      }
      setMessage(result.message);
    },
    [settings],
  );

  const limit = settings?.max_gap_agents;

  return (
    <section className="pf-cap-section">
      <h3 className="pf-cap-title">Runtime limits</h3>

      {message !== null && (
        <p className="pf-banner pf-banner--warn" role="status">
          <TriangleAlert size={14} />
          {message}
          {settings !== null && (
            <button type="button" className="pf-btn pf-btn-sm" onClick={() => void reload()}>
              Reload
            </button>
          )}
        </p>
      )}

      {settings === null ? (
        message === null ? (
          <p className="pf-caption">Reading the settings…</p>
        ) : null
      ) : limit === undefined ? (
        <p className="pf-caption">This build keeps no limit on expertise-gap agents.</p>
      ) : (
        <>
          <label className="pf-label" htmlFor="pf-max-gap-agents">
            Expertise-gap agents per run
          </label>
          <select
            id="pf-max-gap-agents"
            className="pf-input"
            value={String(limit)}
            disabled={busy}
            onChange={(event) => void choose(Number(event.target.value))}
          >
            {MAX_GAP_AGENTS_CHOICES.map((value) => (
              <option key={value} value={String(value)}>
                {gapAgentLabel(value)}
              </option>
            ))}
          </select>
          <p className="pf-caption">
            How many extra specialist agents one run may add when the brief needs expertise the fixed roles do not
            cover. Zero keeps only the required workstream owners. A change applies to the next run.
          </p>
        </>
      )}
    </section>
  );
}
