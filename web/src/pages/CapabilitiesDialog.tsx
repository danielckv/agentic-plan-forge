import { CircleCheck, CircleSlash, RefreshCw, TriangleAlert, Wrench } from "lucide-react";

import { Dialog } from "../ui/Dialog";
import {
  capabilitySummary,
  HOST_TONE_LABEL,
  hostDetail,
  hostTone,
  useCapabilities,
} from "../ui/capabilities";
import { ProviderSetup } from "./ProviderSetup";
import { RuntimeLimitsSection } from "./RuntimeLimitsSection";
import { SkillsSection } from "./SkillsSection";

/**
 * Setup: the hosts that serve this session's avatars, the tools its features
 * need, the skills installed into each agent, every skill the harnesses on this
 * machine already have, the managed hooks, and the runtime limits a run obeys.
 *
 * Ordered by what can stop a run. Hosts first — with no serving host there is
 * no deliberation at all — then tools, then skills and hooks, which only
 * decide how the session is launched.
 */
export function CapabilitiesDialog(props: {
  open: boolean;
  onClose: () => void;
}): React.ReactElement {
  const { open, onClose } = props;
  const { view, refresh } = useCapabilities(open);

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="Setup"
      description={capabilitySummary(view)}
      size="settings"
    >
      <div className="pf-cap">
        {(view?.pinned ?? []).map((pin) => (
          <p key={pin} className="pf-banner pf-banner--warn" role="status">
            <TriangleAlert size={14} />
            {pin} is being ignored: that host cannot serve a call, so roles go elsewhere.
          </p>
        ))}

        <ProviderSetup providers={view?.providers ?? []} onSaved={refresh} />

        <section className="pf-cap-section">
          <h3 className="pf-cap-title">
            Generation hosts
            <button type="button" className="pf-btn pf-btn-sm" onClick={() => void refresh()}>
              <RefreshCw size={12} /> Re-check
            </button>
          </h3>
          <ul className="pf-cap-list">
            {(view?.hosts ?? []).map((h) => {
              const tone = hostTone(h);
              return (
                <li key={h.host} className="pf-cap-row" data-tone={tone}>
                  <span className="pf-cap-mark" aria-hidden="true">
                    {tone === "serves" ? <CircleCheck size={15} /> : <CircleSlash size={15} />}
                  </span>
                  <span className="pf-cap-name">
                    {h.host}
                    {h.version !== undefined && h.version !== "" && (
                      <span className="pf-caption"> {h.version}</span>
                    )}
                  </span>
                  <span className="pf-cap-state">{HOST_TONE_LABEL[tone]}</span>
                  <span className="pf-cap-detail pf-caption">{hostDetail(h)}</span>
                </li>
              );
            })}
          </ul>
        </section>

        <section className="pf-cap-section">
          <h3 className="pf-cap-title">
            <Wrench size={13} /> Tools
          </h3>
          <ul className="pf-cap-list">
            {(view?.tools ?? []).map((t) => (
              <li key={t.name} className="pf-cap-row" data-tone={t.found ? "serves" : t.essential ? "blocked" : "optional"}>
                <span className="pf-cap-mark" aria-hidden="true">
                  {t.found ? <CircleCheck size={15} /> : <CircleSlash size={15} />}
                </span>
                <span className="pf-cap-name pf-mono">{t.name}</span>
                <span className="pf-cap-state">{t.found ? "Found" : "Missing"}</span>
                <span className="pf-cap-detail pf-caption">
                  {t.purpose}
                  {!t.found && t.remedy !== undefined && (
                    <>
                      {" — "}
                      <code className="pf-mono">{t.remedy}</code>
                    </>
                  )}
                </span>
              </li>
            ))}
          </ul>
        </section>

        {(view?.skills ?? []).length > 0 && (
          <section className="pf-cap-section">
            <h3 className="pf-cap-title">Skills</h3>
            <ul className="pf-cap-list">
              {(view?.skills ?? []).map((root) => (
                <li key={`${root.host}-${root.path}`} className="pf-cap-row" data-tone={root.duplicate === true ? "blocked" : "optional"}>
                  <span className="pf-cap-name">{root.host}</span>
                  <span className="pf-cap-state">{root.scope ?? ""}</span>
                  <span className="pf-cap-detail pf-caption">
                    <code className="pf-mono">{root.path}</code>
                    {" — "}
                    {(root.skills ?? []).length > 0 ? (root.skills ?? []).join(", ") : "no plan-forge skills"}
                  </span>
                </li>
              ))}
            </ul>
          </section>
        )}

        {(view?.hooks ?? []).length > 0 && (
          <section className="pf-cap-section">
            <h3 className="pf-cap-title">Managed hooks</h3>
            <ul className="pf-cap-list">
              {(view?.hooks ?? []).map((hook) => (
                <li key={`${hook.host}-${hook.path}`} className="pf-cap-row" data-tone={hook.state === "installed" ? "serves" : "blocked"}>
                  <span className="pf-cap-mark" aria-hidden="true">
                    {hook.state === "installed" ? <CircleCheck size={15} /> : <TriangleAlert size={15} />}
                  </span>
                  <span className="pf-cap-name">{hook.host}</span>
                  <span className="pf-cap-state">{hook.state}</span>
                  <span className="pf-cap-detail pf-caption">
                    <code className="pf-mono">{hook.path}</code>
                    {hook.remedy !== undefined && hook.remedy !== "" && <> — {hook.remedy}</>}
                  </span>
                </li>
              ))}
            </ul>
          </section>
        )}

        <SkillsSection report={view?.discovered_skills} />
        <RuntimeLimitsSection />
      </div>
    </Dialog>
  );
}
