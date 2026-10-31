import { CircleCheck, TriangleAlert } from "lucide-react";
import { useEffect, useState } from "react";

import { loadModels, modelPins, pinSelects } from "../ui/models";
import type { ModelCatalogEntry, ModelPin } from "../ui/models";
import { loadSettings } from "../ui/settings";
import type { SettingsDocument } from "../ui/settings";

/**
 * The Models tab of the Setup dialog: the models each API host can serve and
 * the selections the settings document pins (`default_main_model`,
 * `default_judge_model`, `role_models`), marked on the catalogue rows.
 *
 * Read-only on purpose: the settings endpoint takes the whole document back
 * with its revision, and a per-model click would need merge rules the CLI
 * editor already owns. This tab answers "what can each host serve, and which
 * of it is pinned" — changing the pins stays in `plan-forge config`.
 */

interface ModelsState {
  settings: SettingsDocument | null;
  catalogue: ModelCatalogEntry[] | null;
  message: string | null;
}

/**
 * The settings and the catalogue, read once when the tab opens. The tab only
 * mounts while the dialog shows it, so this is one read per visit, never a
 * poll — and the catalogue is a live provider round trip, which a poll would
 * spend repeatedly.
 */
export function useSetupModels(enabled: boolean): ModelsState {
  const [state, setState] = useState<ModelsState>({ settings: null, catalogue: null, message: null });

  useEffect(() => {
    if (!enabled) return;
    let cancelled = false;
    void (async (): Promise<void> => {
      const [settingsResult, modelsResult] = await Promise.all([loadSettings(), loadModels()]);
      if (cancelled) return;
      const problems = [
        settingsResult.ok ? null : settingsResult.message,
        modelsResult.ok ? null : modelsResult.message,
      ].filter((m): m is string => m !== null);
      setState({
        settings: settingsResult.ok ? settingsResult.settings : null,
        catalogue: modelsResult.ok ? modelsResult.catalogue : null,
        message: problems.length > 0 ? problems.join(" ") : null,
      });
    })();
    return () => {
      cancelled = true;
    };
  }, [enabled]);

  return state;
}

export function ModelsPanel(props: ModelsState): React.ReactElement {
  const { settings, catalogue, message } = props;
  const pins = modelPins(settings);

  return (
    <div className="pf-cap">
      {message !== null && (
        <p className="pf-banner pf-banner--warn" role="status">
          <TriangleAlert size={14} />
          {message}
        </p>
      )}

      <section className="pf-cap-section">
        <h3 className="pf-cap-title">Current selections</h3>
        {settings === null ? (
          message === null ? <p className="pf-caption">Reading the settings…</p> : null
        ) : pins.length === 0 ? (
          <p className="pf-caption">
            No models pinned — every role is spread across the serving hosts, and each host picks its own default.
          </p>
        ) : (
          <ul className="pf-cap-list">
            {pins.map((pin) => (
              <li key={pin.key} className="pf-cap-row" data-tone="optional">
                <span className="pf-cap-name">{pin.label}</span>
                <span className="pf-cap-state pf-mono">{pin.host === "" ? "any host" : pin.host}</span>
                <span className="pf-cap-detail pf-caption">
                  <code className="pf-mono">{pin.key}</code>
                  {" — "}
                  {pin.model === "" ? "the host picks its own default" : <code className="pf-mono">{pin.model}</code>}
                </span>
              </li>
            ))}
          </ul>
        )}
      </section>

      {catalogue === null ? (
        message === null ? <p className="pf-caption">Reading the models…</p> : null
      ) : (
        catalogue.map((entry) => <HostModels key={entry.host} entry={entry} pins={pins} />)
      )}
    </div>
  );
}

/** One host's list, with the pinned models marked as the current selection. */
function HostModels(props: { entry: ModelCatalogEntry; pins: ModelPin[] }): React.ReactElement {
  const { entry, pins } = props;
  // A stale entry certifies nothing: show the built-in catalogue, labelled.
  const live = entry.models.length > 0;
  const shown = live ? entry.models : (entry.fallback ?? []);
  return (
    <section className="pf-cap-section">
      <h3 className="pf-cap-title">
        {entry.label}
        <span className="pf-caption">
          {entry.host}
          {entry.stale === true && " · unverified list"}
        </span>
      </h3>
      {entry.error !== undefined && entry.error !== "" && (
        <p className="pf-caption">The live list could not be fetched ({entry.error}); this is the built-in catalogue.</p>
      )}
      {shown.length === 0 ? (
        <p className="pf-caption">This host lists no models.</p>
      ) : (
        <ul className="pf-models" aria-label={`Models ${entry.label} serves`}>
          {shown.map((m) => {
            const selected = pins.some((pin) => pinSelects(pin, entry.host, m.id));
            return (
              <li key={m.id} className="pf-models-item" data-selected={selected || undefined}>
                {selected && <CircleCheck size={12} aria-label="current selection" />}
                <code className="pf-mono">{m.id}</code>
                {m.label !== m.id && <span className="pf-caption">{m.label}</span>}
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}
