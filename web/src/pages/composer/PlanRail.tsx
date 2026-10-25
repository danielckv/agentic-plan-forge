import { useState } from "react";
import { BookOpen, Boxes, ClipboardList, Layers, LoaderCircle, Waypoints } from "lucide-react";

import { EpicCard } from "../../components/epics/EpicCard";
import { EpicModal } from "../../components/epics/EpicModal";
import type { ComponentRef, ComponentsView, ImpactMap, PlanFile, ProjectContext } from "../../protocol/api";
import { componentTerm } from "../../ui/editor/markdownEdits";
import { BriefTab } from "./BriefTab";
import { ComponentBrowser } from "./ComponentBrowser";
import { ImpactPanel, VERDICT_TONE } from "./ImpactPanel";
import { NotesList } from "./NotesList";

type RailTab = "brief" | "impact" | "epics" | "code" | "notes";

/**
 * The right side of Page 1: the plan as it forms. Impact says which parts of
 * the project the requirements touch and what happens to each; Epics are files
 * grouped from the requirements; Codebase browses the real components; Notes
 * shows what the repository already says. The rail never edits the document;
 * it hands terms to `onInsert`.
 */
export function PlanRail(props: {
  files: PlanFile[];
  /** True while the requirements are written but not yet interpreted. */
  interpreting: boolean;
  hasText: boolean;
  blocks: ComponentsView | null;
  scanning: boolean;
  ctx: ProjectContext | null;
  impact: ImpactMap | null;
  /** The composer document, for the Brief outline. */
  text: string;
  onInsert: (text: string) => void;
}): React.ReactElement {
  const { files, interpreting, hasText, blocks, scanning, ctx, impact, text, onInsert } = props;
  // Brief opens first: it is the only tab that helps before anything is
  // written, and the rest are empty until the requirements say something.
  const [tab, setTab] = useState<RailTab>("brief");
  const [open, setOpen] = useState<number | null>(null);
  const noteCount = ctx?.hits.length ?? 0;
  const touchCount = (impact?.whole.length ?? 0) + (impact?.conflicts.length ?? 0);

  return (
    <aside className="pf-rail" aria-label="Plan">
      <div className="pf-rail-tabs" role="tablist" aria-label="Plan views">
        <RailTabButton id="brief" current={tab} onSelect={setTab} icon={ClipboardList} label="Brief" />
        <RailTabButton id="impact" current={tab} onSelect={setTab} icon={Waypoints} label="Impact" count={touchCount} />
        <RailTabButton id="epics" current={tab} onSelect={setTab} icon={Layers} label="Epics" count={files.length} />
        <RailTabButton id="code" current={tab} onSelect={setTab} icon={Boxes} label="Codebase" />
        <RailTabButton id="notes" current={tab} onSelect={setTab} icon={BookOpen} label="Notes" count={noteCount} />
      </div>
      <div className="pf-rail-body pf-scroll-thin" role="tabpanel">
        {tab === "brief" && <BriefTab text={text} onInsert={onInsert} />}
        {tab === "impact" && <ImpactPanel map={impact} />}
        {tab === "epics" && (
          <EpicsTab files={files} interpreting={interpreting} hasText={hasText} onOpen={setOpen} />
        )}
        {tab === "code" && (
          <ComponentBrowser
            blocks={blocks}
            scanning={scanning}
            onPick={(c: ComponentRef) => onInsert(componentTerm(c.name))}
          />
        )}
        {tab === "notes" && <NotesList ctx={ctx} />}
      </div>
      <EpicModal
        docs={files}
        index={open}
        onIndex={setOpen}
        footer={(doc) => {
          const file = files.find((f) => f.id === doc.id);
          if (file === undefined || file.links.length === 0) return null;
          return <span className="pf-caption">{file.links.length} parts of the project</span>;
        }}
      />
    </aside>
  );
}

function EpicsTab(props: {
  files: PlanFile[];
  interpreting: boolean;
  hasText: boolean;
  onOpen: (i: number) => void;
}): React.ReactElement {
  const { files, interpreting, hasText, onOpen } = props;
  if (files.length === 0) {
    return (
      <div className="pf-rail-empty">
        {interpreting ? (
          <>
            <LoaderCircle size={18} className="animate-spin" style={{ color: "var(--pf-accent)" }} aria-hidden="true" />
            <p className="pf-body">Grouping your requirements into epics…</p>
            <p className="pf-caption">This runs a few seconds after you stop typing.</p>
          </>
        ) : hasText ? (
          <>
            <p className="pf-body">No epics yet.</p>
            <p className="pf-caption">Write what should happen as requirements, and they will be grouped into epics here.</p>
          </>
        ) : (
          <>
            <Layers size={20} style={{ color: "var(--pf-epic)" }} aria-hidden="true" />
            <p className="pf-body">Your plan appears here as epic files.</p>
            <p className="pf-caption">
              Describe the work on the left. Each epic becomes a file you can open, linked to the code it touches.
            </p>
          </>
        )}
      </div>
    );
  }
  return (
    <div className="pf-epic-list">
      {interpreting && (
        <p className="pf-caption pf-epic-list-note">
          <LoaderCircle size={12} className="animate-spin" aria-hidden="true" /> Updating from your latest edits…
        </p>
      )}
      {files.map((f, i) => (
        <EpicCard
          key={f.id}
          id={f.id}
          title={f.title}
          goal={f.goal}
          filename={f.filename}
          stats={[
            { label: ["requirement", "requirements"], value: f.requirements },
            { label: ["criterion", "criteria"], value: f.criteria },
            { label: ["part", "parts"], value: f.links.length },
          ]}
          tags={f.links.map((l) => ({
            text: l.symbol_name !== undefined && l.symbol_name !== "" ? l.symbol_name : l.area,
            tone: VERDICT_TONE[l.verdict],
          }))}
          onOpen={() => onOpen(i)}
        />
      ))}
    </div>
  );
}

function RailTabButton(props: {
  id: RailTab;
  current: RailTab;
  onSelect: (t: RailTab) => void;
  icon: React.ComponentType<{ size?: number }>;
  label: string;
  count?: number;
}): React.ReactElement {
  const { id, current, onSelect, icon: Icon, label, count } = props;
  return (
    <button type="button" role="tab" className="pf-rail-tab" aria-selected={id === current} onClick={() => onSelect(id)}>
      <Icon size={14} />
      {label}
      {count !== undefined && count > 0 && <span className="pf-rail-count">{count}</span>}
    </button>
  );
}
