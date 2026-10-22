import { useState } from "react";
import { BookOpen, Boxes, Layers, LoaderCircle } from "lucide-react";

import { EpicCard } from "../../components/epics/EpicCard";
import { EpicModal } from "../../components/epics/EpicModal";
import type { ComponentRef, ComponentsView, PlanFile, ProjectContext } from "../../protocol/api";
import { componentTerm } from "../../ui/editor/markdownEdits";
import { ComponentBrowser } from "./ComponentBrowser";
import { NotesList } from "./NotesList";

type RailTab = "epics" | "code" | "notes";

/**
 * The right side of Page 1: the plan as it forms. Epics are files grouped
 * from the requirements; Codebase browses the real components; Notes shows
 * what the repository already says. The rail never edits the document; it
 * hands terms to `onInsert`.
 */
export function PlanRail(props: {
  files: PlanFile[];
  /** True while the requirements are written but not yet interpreted. */
  interpreting: boolean;
  hasText: boolean;
  blocks: ComponentsView | null;
  scanning: boolean;
  ctx: ProjectContext | null;
  onInsert: (text: string) => void;
}): React.ReactElement {
  const { files, interpreting, hasText, blocks, scanning, ctx, onInsert } = props;
  const [tab, setTab] = useState<RailTab>("epics");
  const [open, setOpen] = useState<number | null>(null);
  const noteCount = ctx?.hits.length ?? 0;

  return (
    <aside className="pf-rail" aria-label="Plan">
      <div className="pf-rail-tabs" role="tablist" aria-label="Plan views">
        <RailTabButton id="epics" current={tab} onSelect={setTab} icon={Layers} label="Epics" count={files.length} />
        <RailTabButton id="code" current={tab} onSelect={setTab} icon={Boxes} label="Codebase" />
        <RailTabButton id="notes" current={tab} onSelect={setTab} icon={BookOpen} label="Notes" count={noteCount} />
      </div>
      <div className="pf-rail-body pf-scroll-thin" role="tabpanel">
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
          if (file === undefined || file.components.length === 0) return null;
          return <span className="pf-caption">{file.components.length} linked components</span>;
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
            { label: ["component", "components"], value: f.components.length },
          ]}
          tags={f.components.map((c) => c.name)}
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
