import { useMemo, useState } from "react";
import { CircleCheck, CircleSlash, Search, TriangleAlert, X } from "lucide-react";

import {
  filterSkillGroups,
  groupLabel,
  groupSkills,
  SKILL_TONE_LABEL,
  skillCount,
  skillDescription,
  skillSummary,
  skillTone,
  unreadableRoots,
} from "../ui/skills";
import type { SkillScanReport } from "../ui/skills";

/**
 * Every skill the harnesses on this machine already have, grouped by harness
 * and surface.
 *
 * The report comes from `/api/capabilities` (`discovered_skills`), which scans
 * the same roots the installer reads, so what is listed here is what a harness
 * would load. Nothing in this section writes: a skill that belongs to another
 * harness is shown with its description, its root and why that root is read,
 * and left alone. The list is what makes "which skills exist here and could
 * help" answerable without opening five directories by hand.
 */
export function SkillsSection({ report }: { report: SkillScanReport | undefined }): React.ReactElement | null {
  const [query, setQuery] = useState("");

  const groups = useMemo(() => groupSkills(report), [report]);
  const shown = useMemo(() => filterSkillGroups(groups, query), [groups, query]);

  // No report yet: the dialog's own summary already says it is still checking.
  if (report === undefined) return null;

  const total = skillCount(report);
  const broken = unreadableRoots(report);

  return (
    <section className="pf-cap-section">
      <h3 className="pf-cap-title">Discovered skills</h3>
      <p className="pf-caption">{skillSummary(report)}</p>

      {total > 0 && (
        <div className="pf-search">
          <Search size={14} className="pf-search-icon" aria-hidden="true" />
          <input
            type="search"
            className="pf-search-input"
            value={query}
            placeholder="Filter by name, description, harness, or path…"
            aria-label="Filter discovered skills"
            onChange={(event) => setQuery(event.target.value)}
          />
          {query !== "" && (
            <button type="button" className="pf-search-clear" aria-label="Clear filter" onClick={() => setQuery("")}>
              <X size={13} />
            </button>
          )}
        </div>
      )}

      {broken.length > 0 && (
        <ul className="pf-cap-list">
          {broken.map((root) => (
            <li key={`${root.path}-${root.via}`} className="pf-cap-row" data-tone="blocked">
              <span className="pf-cap-mark" aria-hidden="true">
                <TriangleAlert size={15} />
              </span>
              <span className="pf-cap-name">Unreadable root</span>
              <span className="pf-cap-state">Skipped</span>
              <span className="pf-cap-detail pf-caption">
                <code className="pf-mono">{root.path}</code>
                {" — "}
                {root.error} ({root.via})
              </span>
            </li>
          ))}
        </ul>
      )}

      {shown.length === 0 && (
        <p className="pf-caption" role="status">
          {total === 0 ? "No skill directory is readable for the agents found here." : `No skill matches “${query}”.`}
        </p>
      )}

      {shown.map((group) => (
        <div key={group.key}>
          <h4 className="pf-label">{groupLabel(group)}</h4>
          <ul className="pf-cap-list">
            {group.skills.map((skill) => {
              const tone = skillTone(skill);
              return (
                <li key={`${skill.root}-${skill.name}`} className="pf-cap-row" data-tone={tone}>
                  <span className="pf-cap-mark" aria-hidden="true">
                    {tone === "blocked" ? (
                      <TriangleAlert size={15} />
                    ) : tone === "optional" ? (
                      <CircleSlash size={15} />
                    ) : (
                      <CircleCheck size={15} />
                    )}
                  </span>
                  <span className="pf-cap-name pf-mono">{skill.name}</span>
                  <span className="pf-cap-state">{SKILL_TONE_LABEL[tone]}</span>
                  <span className="pf-cap-detail pf-caption">
                    {skillDescription(skill)}
                    {" — "}
                    <code className="pf-mono">{skill.root}</code>
                    {" ("}
                    {skill.via}
                    {skill.managed ? ", installed by Plan Forge" : ", read-only"}
                    {")"}
                    {tone === "blocked" && <> {skill.problem ?? ""}</>}
                  </span>
                </li>
              );
            })}
          </ul>
        </div>
      ))}

      {total > 0 && (
        <p className="pf-caption">
          Read-only: Plan Forge lists skills it does not own and never edits or removes them.
        </p>
      )}
    </section>
  );
}
