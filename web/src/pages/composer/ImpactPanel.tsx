import { AlertTriangle } from "lucide-react";

import type { ImpactLink, ImpactMap, ImpactVerdict } from "../../protocol/api";

/**
 * What the requirements touch, for a reader who does not read code.
 *
 * Every row leads with the part of the project in plain language and the
 * sentence explaining what happens to it. The file path is present but
 * secondary: a developer can follow it, and nobody else has to.
 */

/** The verdict as a reader who does not read code reads it. */
export const VERDICT_LABEL: Record<ImpactVerdict, string> = {
  "": "Possibly involved",
  change: "Changes",
  add: "New",
  reuse: "Already there",
  conflicts_with: "Clashes",
};

/** The chip tone per verdict; the suffix of a `pf-tag--` class. */
export const VERDICT_TONE: Record<ImpactVerdict, string> = {
  "": "muted",
  change: "change",
  add: "add",
  reuse: "reuse",
  conflicts_with: "conflict",
};

/** The order the groups read in: what changes, what is new, what is reused. */
const GROUP_ORDER: ImpactVerdict[] = ["change", "add", "reuse", ""];

/** Groups links by verdict, in GROUP_ORDER, dropping empty groups. */
export function groupByVerdict(links: ImpactLink[]): Array<{ verdict: ImpactVerdict; links: ImpactLink[] }> {
  return GROUP_ORDER.map((verdict) => ({
    verdict,
    links: links.filter((l) => (l.verdict satisfies ImpactVerdict) === verdict),
  })).filter((g) => g.links.length > 0);
}

/** The part of the project a link names, in words before code. */
export function linkTitle(link: ImpactLink): string {
  if (link.area !== "") return link.area;
  if (link.symbol_name !== undefined && link.symbol_name !== "") return link.symbol_name;
  if (link.heading !== undefined && link.heading !== "") return link.heading;
  return link.path ?? "";
}

/** Where a link points, for the reader who wants the file. */
export function linkWhere(link: ImpactLink): string {
  if (link.path === undefined || link.path === "") return "";
  const at = link.line !== undefined && link.line > 0 ? `:${link.line}` : "";
  const symbol = link.symbol_name !== undefined && link.symbol_name !== "" ? `${link.symbol_name} — ` : "";
  return `${symbol}${link.path}${at}`;
}

export function ImpactPanel({ map }: { map: ImpactMap | null }): React.ReactElement {
  if (map === null || map.state === "pending") {
    return (
      <div className="pf-rail-empty">
        <p className="pf-body">Working out what this touches…</p>
        <p className="pf-caption">Reading the project's code, documentation and past decisions.</p>
      </div>
    );
  }

  const groups = groupByVerdict(map.whole);
  const nothing = map.whole.length === 0 && map.conflicts.length === 0;

  return (
    <div className="pf-impact">
      {map.summary !== undefined && map.summary !== "" && <p className="pf-body pf-impact-summary">{map.summary}</p>}

      {map.state === "lexical" && (
        <p className="pf-caption">Matched by name so far. The full read of the project is still running.</p>
      )}
      {map.state === "failed" && (
        <p className="pf-caption pf-impact-warn">
          The project could not be read this time{map.notes !== undefined && map.notes.length > 0 ? `: ${map.notes[map.notes.length - 1]}` : "."}
        </p>
      )}

      {map.conflicts.length > 0 && (
        <section className="pf-impact-conflicts" aria-label="Clashes with decisions already made">
          <h3 className="pf-impact-heading">
            <AlertTriangle size={13} aria-hidden="true" />
            {map.conflicts.length === 1
              ? "One decision already recorded says something different"
              : `${map.conflicts.length} decisions already recorded say something different`}
          </h3>
          {map.conflicts.map((link) => (
            <ImpactRow key={link.id} link={link} />
          ))}
        </section>
      )}

      {nothing && map.state === "ready" && (
        <div className="pf-rail-empty">
          <p className="pf-body">Nothing in this project matches these requirements yet.</p>
          <p className="pf-caption">That usually means the work is new rather than a change to what exists.</p>
        </div>
      )}

      {groups.map((group) => (
        <section key={group.verdict} className="pf-impact-group" aria-label={VERDICT_LABEL[group.verdict]}>
          <h3 className="pf-impact-heading">{VERDICT_LABEL[group.verdict]}</h3>
          {group.links.map((link) => (
            <ImpactRow key={link.id} link={link} />
          ))}
        </section>
      ))}
    </div>
  );
}

function ImpactRow({ link }: { link: ImpactLink }): React.ReactElement {
  const where = linkWhere(link);
  return (
    <article className="pf-impact-row">
      <header className="pf-impact-row-head">
        <span className="pf-impact-area">{linkTitle(link)}</span>
        <span className={`pf-tag pf-tag--${VERDICT_TONE[link.verdict]}`}>{VERDICT_LABEL[link.verdict]}</span>
      </header>
      {link.reason !== undefined && link.reason !== "" && <p className="pf-body">{link.reason}</p>}
      {where !== "" && <code className="pf-mono pf-caption">{where}</code>}
    </article>
  );
}
