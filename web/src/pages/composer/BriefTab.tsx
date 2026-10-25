import { Check, Plus } from "lucide-react";

import { briefSummary, readBrief } from "../../ui/brief";

/**
 * The Brief tab: the five topics the deliberation will ask about, whether the
 * document mentions each, and a button to add the section it is missing.
 *
 * It is an outline, not a gate. Confirm stays live whatever this says: a word
 * search is not entitled to stop a run, and a writer who knows their brief is
 * complete should never have to argue with a checklist to start one.
 */
export function BriefTab(props: {
  text: string;
  onInsert: (text: string) => void;
}): React.ReactElement {
  const { text, onInsert } = props;
  const checks = readBrief(text);

  return (
    <div className="pf-brief">
      <p className="pf-caption pf-brief-summary">{briefSummary(text, checks)}</p>

      <ul className="pf-brief-list">
        {checks.map(({ section, mentioned, evidence }) => (
          <li key={section.id} className="pf-brief-item" data-mentioned={mentioned}>
            <div className="pf-brief-head">
              <span className="pf-brief-mark" aria-hidden="true">
                {mentioned ? <Check size={13} strokeWidth={3} /> : null}
              </span>
              <span className="pf-brief-label">{section.label}</span>
              {!mentioned && (
                <button
                  type="button"
                  className="pf-brief-add"
                  onClick={() => onInsert(section.insert)}
                  aria-label={`Add a ${section.label} section`}
                >
                  <Plus size={12} /> Add
                </button>
              )}
            </div>
            {mentioned ? (
              <p className="pf-brief-evidence pf-caption" title={evidence}>
                {evidence}
              </p>
            ) : (
              <p className="pf-brief-need pf-caption">{section.need}</p>
            )}
          </li>
        ))}
      </ul>

      <p className="pf-caption pf-brief-foot">
        These are word searches over your document. A tick means the topic is mentioned, not that it
        is answered — and none of it blocks Confirm.
      </p>
    </div>
  );
}
