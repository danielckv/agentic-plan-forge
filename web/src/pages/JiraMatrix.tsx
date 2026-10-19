import { useEffect, useId, useMemo, useRef, useState } from "react";
import { ChevronRight, ChevronsDownUp, ChevronsUpDown, ListTree, Search, SearchX, X, Zap } from "lucide-react";

import { Collapse } from "../ui/Collapse";
import { CopyButton } from "../ui/CopyButton";
import { allOpenIds, countMatrix, filterMatrix } from "../ui/matrix";
import type { EpicRow, SubtaskRow } from "../ui/matrix";
import { FOCUS_FILTER_EVENT } from "../ui/shortcuts";

/**
 * The Jira matrix: epics -> stories -> subtasks as animated accordions.
 *
 * - Expand all / Collapse all act on what is visible (the filtered set).
 * - The filter matches subtasks by ID, title, or target file. Typing opens
 *   every epic and story that still holds a match, so results are on screen.
 * - `/` focuses the filter from anywhere on the page.
 * - Every verification command has a Copy button.
 */

const SEVERITY_BADGE: Record<NonNullable<SubtaskRow["severity"]>, string> = {
  CRITICAL: "pf-badge--critical",
  HIGH: "pf-badge--high",
  MEDIUM: "pf-badge--medium",
};

export function JiraMatrix({ epics }: { epics: EpicRow[] }): React.ReactElement {
  const [query, setQuery] = useState("");
  const [openEpics, setOpenEpics] = useState<Set<string>>(() => new Set());
  const [openStories, setOpenStories] = useState<Set<string>>(() => new Set());
  const inputRef = useRef<HTMLInputElement>(null);
  const resultsId = useId();

  const totals = useMemo(() => countMatrix(epics), [epics]);
  const view = useMemo(() => filterMatrix(epics, query), [epics, query]);

  useEffect(() => {
    const focusFilter = (e: Event): void => {
      e.preventDefault(); // tells the shortcut handler the key was consumed
      inputRef.current?.focus();
      inputRef.current?.select();
    };
    window.addEventListener(FOCUS_FILTER_EVENT, focusFilter);
    return () => window.removeEventListener(FOCUS_FILTER_EVENT, focusFilter);
  }, []);

  const expandAll = (): void => {
    const ids = allOpenIds(view.epics);
    setOpenEpics((prev) => new Set([...prev, ...ids.epics]));
    setOpenStories((prev) => new Set([...prev, ...ids.stories]));
  };
  const collapseAll = (): void => {
    setOpenEpics(new Set());
    setOpenStories(new Set());
  };

  const onQuery = (value: string): void => {
    setQuery(value);
    if (value.trim() === "") return;
    // Open what the new filter keeps, so a match is never hidden in a closed row.
    const ids = allOpenIds(filterMatrix(epics, value).epics);
    setOpenEpics((prev) => new Set([...prev, ...ids.epics]));
    setOpenStories((prev) => new Set([...prev, ...ids.stories]));
  };

  const toggle = (set: "epic" | "story", id: string): void => {
    const update = (prev: Set<string>): Set<string> => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    };
    if (set === "epic") setOpenEpics(update);
    else setOpenStories(update);
  };

  const allOpen = view.epics.length > 0 && view.epics.every((e) => openEpics.has(e.id) && e.stories.every((s) => openStories.has(s.id)));

  return (
    <div className="flex flex-col gap-3">
      <div className="pf-matrix-toolbar">
        <div className="pf-search">
          <Search size={14} className="pf-search-icon" aria-hidden="true" />
          <input
            ref={inputRef}
            type="search"
            className="pf-search-input"
            value={query}
            placeholder="Filter by ID, title, or file…"
            aria-label="Filter subtasks by ID, title, or target file"
            aria-controls={resultsId}
            onChange={(e) => onQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape" && query !== "") {
                e.stopPropagation();
                setQuery("");
              }
            }}
          />
          {query !== "" ? (
            <button type="button" className="pf-search-clear" aria-label="Clear filter" onClick={() => setQuery("")}>
              <X size={13} />
            </button>
          ) : (
            <kbd className="pf-kbd pf-search-hint" aria-hidden="true">
              /
            </kbd>
          )}
        </div>
        <div className="pf-matrix-actions">
          <button type="button" className="pf-btn pf-btn-secondary pf-btn-sm" onClick={expandAll} disabled={view.epics.length === 0 || allOpen}>
            <ChevronsUpDown size={13} />
            Expand all
          </button>
          <button
            type="button"
            className="pf-btn pf-btn-secondary pf-btn-sm"
            onClick={collapseAll}
            disabled={openEpics.size === 0 && openStories.size === 0}
          >
            <ChevronsDownUp size={13} />
            Collapse all
          </button>
        </div>
      </div>

      <p className="pf-caption" id={resultsId} aria-live="polite">
        {view.active
          ? `${String(view.matched)} of ${String(view.total)} subtasks match “${query.trim()}”`
          : `${String(totals.epics)} ${totals.epics === 1 ? "epic" : "epics"} · ${String(totals.stories)} stories · ${String(totals.subtasks)} subtasks`}
      </p>

      {view.epics.length === 0 ? (
        <div className="pf-empty">
          <span className="pf-empty-icon">
            <SearchX size={20} />
          </span>
          <p className="pf-body font-medium" style={{ color: "var(--pf-ink)" }}>
            No subtasks match “{query.trim()}”
          </p>
          <p className="pf-caption max-w-md">Try a subtask ID such as E1-S1-T1, a word from its title, or part of a file path.</p>
          <button type="button" className="pf-btn pf-btn-secondary pf-btn-sm mt-1" onClick={() => setQuery("")}>
            Clear filter
          </button>
        </div>
      ) : (
        view.epics.map((epic) => (
          <EpicAccordion
            key={epic.id}
            epic={epic}
            open={openEpics.has(epic.id)}
            openStories={openStories}
            onToggleEpic={() => toggle("epic", epic.id)}
            onToggleStory={(id) => toggle("story", id)}
          />
        ))
      )}
    </div>
  );
}

function EpicAccordion(props: {
  epic: EpicRow;
  open: boolean;
  openStories: Set<string>;
  onToggleEpic: () => void;
  onToggleStory: (id: string) => void;
}): React.ReactElement {
  const { epic, open, openStories, onToggleEpic, onToggleStory } = props;
  const bodyId = useId();
  return (
    <div className="pf-accordion" data-open={open}>
      <button type="button" className="pf-accordion-trigger" aria-expanded={open} aria-controls={bodyId} onClick={onToggleEpic}>
        <ChevronRight size={15} className="pf-accordion-chevron" data-open={open} />
        <span className="pf-chip pf-chip--epic">{epic.id}</span>
        <span className="pf-accordion-title">{epic.title}</span>
        <span className="pf-caption ml-auto shrink-0">
          {epic.stories.length} {epic.stories.length === 1 ? "story" : "stories"}
        </span>
      </button>
      <Collapse id={bodyId} open={open}>
        <div className="pf-accordion-body px-5 py-4">
          <p className="pf-caption mb-4">{epic.goal}</p>
          <div className="flex flex-col gap-2">
            {epic.stories.map((story) => (
              <StoryAccordion
                key={story.id}
                story={story}
                open={openStories.has(story.id)}
                onToggle={() => onToggleStory(story.id)}
              />
            ))}
          </div>
        </div>
      </Collapse>
    </div>
  );
}

function StoryAccordion(props: {
  story: EpicRow["stories"][number];
  open: boolean;
  onToggle: () => void;
}): React.ReactElement {
  const { story, open, onToggle } = props;
  const bodyId = useId();
  return (
    <div className="pf-story" data-open={open}>
      <button type="button" className="pf-accordion-trigger" aria-expanded={open} aria-controls={bodyId} onClick={onToggle}>
        <ChevronRight size={14} className="pf-accordion-chevron" data-open={open} />
        <span className="pf-chip">{story.id}</span>
        <span className="pf-accordion-title pf-accordion-title--story">{story.need}</span>
      </button>
      <Collapse id={bodyId} open={open}>
        <div className="pf-story-body">
          {story.subtasks.map((st) => (
            <Subtask key={st.id} subtask={st} />
          ))}
        </div>
      </Collapse>
    </div>
  );
}

function Subtask({ subtask: st }: { subtask: SubtaskRow }): React.ReactElement {
  return (
    <div className="pf-subtask">
      <div className="pf-subtask-head">
        <span className="pf-caption pf-mono font-semibold">{st.id}</span>
        <span className="pf-subtask-title">{st.title}</span>
        {st.severity !== undefined && <span className={`pf-badge ${SEVERITY_BADGE[st.severity]}`}>{st.severity}</span>}
        <span className="pf-subtask-est">
          <span className="pf-chip" title="model estimate, not measured">
            Human: {st.humanEstimate}
          </span>
          <span className="pf-chip pf-chip--ok" title="model estimate, not measured">
            <Zap size={11} />
            Agent: {st.agentEstimate}
          </span>
        </span>
      </div>

      <p className="pf-label mt-3 mb-1">Acceptance criteria</p>
      <ul className="m-0 list-disc pl-5 text-[13px]" style={{ color: "var(--pf-ink-2)" }}>
        {st.acceptance.map((a, i) => (
          <li key={`${String(i)}-${a}`}>{a}</li>
        ))}
      </ul>

      <div className="pf-subtask-meta">
        <div>
          <p className="pf-label mb-1">Targets</p>
          <ul className="pf-chip-list">
            {st.targets.map((t, i) => (
              <li key={`${String(i)}-${t}`}>
                <code className="pf-mono pf-target">
                  <ListTree size={11} aria-hidden="true" />
                  {t}
                </code>
              </li>
            ))}
          </ul>
        </div>
        <div>
          <p className="pf-label mb-1">Verification</p>
          <ul className="pf-command-list">
            {st.verification.map((cmd, i) => (
              <li key={`${String(i)}-${cmd}`} className="pf-command">
                <code className="pf-mono pf-command-text">{cmd}</code>
                <CopyButton text={cmd} label={`Copy command: ${cmd}`} />
              </li>
            ))}
          </ul>
        </div>
      </div>
    </div>
  );
}
