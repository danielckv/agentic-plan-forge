import { useEffect, useRef, useState } from "react";
import {
  Check,
  ChevronRight,
  Circle,
  Clock3,
  Link2,
  MessageSquareText,
  Trash2,
  X,
} from "lucide-react";

import type { BoardPriority, BoardTask } from "./types";

interface BoardDrawerProps {
  task: BoardTask;
  pending: boolean;
  onClose: () => void;
  onFocusTask: (taskID: string) => void;
  onPriority: (priority: BoardPriority) => void;
  onAddNote: (text: string) => Promise<boolean>;
  onDeleteNote: (noteID: string) => void;
}

const PRIORITIES: Array<{ value: BoardPriority; label: string; keyHint: string }> = [
  { value: "high", label: "High", keyHint: "H" },
  { value: "normal", label: "Normal", keyHint: "N" },
  { value: "low", label: "Low", keyHint: "L" },
];

export function BoardDrawer({
  task,
  pending,
  onClose,
  onFocusTask,
  onPriority,
  onAddNote,
  onDeleteNote,
}: BoardDrawerProps): React.ReactElement {
  const [draft, setDraft] = useState("");
  const closeButton = useRef<HTMLButtonElement>(null);
  const characterCount = Array.from(draft).length;

  useEffect(() => {
    setDraft("");
    closeButton.current?.focus();
  }, [task.id]);

  const submitNote = (): void => {
    const note = draft.trim();
    if (note === "" || Array.from(note).length > 2000) return;
    setDraft("");
    void onAddNote(note).then((saved) => {
      if (!saved) setDraft(note);
    });
  };

  return (
    <div className="pf-board-drawer-layer" onMouseDown={onClose}>
      <aside
        className="pf-board-drawer"
        role="dialog"
        aria-modal="true"
        aria-labelledby="pf-board-drawer-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className="pf-board-drawer-header">
          <div>
            <span className="pf-board-drawer-id pf-mono">{task.id}</span>
            <h2 id="pf-board-drawer-title">{task.title}</h2>
          </div>
          <button
            ref={closeButton}
            type="button"
            className="pf-btn pf-btn-ghost pf-board-icon-button"
            onClick={onClose}
            aria-label="Close task details"
          >
            <X size={18} aria-hidden="true" />
          </button>
        </header>

        <div className="pf-board-drawer-scroll pf-scroll-thin">
          {task.blocked ? (
            <div className="pf-notice pf-notice--warn pf-board-drawer-notice">
              <Link2 size={16} aria-hidden="true" />
              <span>This task is blocked by unfinished dependencies.</span>
            </div>
          ) : null}

          <section className="pf-board-drawer-section" aria-labelledby="priority-heading">
            <div className="pf-board-section-heading">
              <h3 id="priority-heading">Priority</h3>
              <span className="pf-caption">Keyboard: H / N / L</span>
            </div>
            <div className="pf-board-priority-control" role="group" aria-label="Task priority">
              {PRIORITIES.map((priority) => (
                <button
                  key={priority.value}
                  type="button"
                  data-active={task.priority === priority.value}
                  onClick={() => onPriority(priority.value)}
                  title={`${priority.label} priority (${priority.keyHint})`}
                >
                  {priority.label}
                </button>
              ))}
            </div>
          </section>

          <section className="pf-board-drawer-section" aria-labelledby="acceptance-heading">
            <h3 id="acceptance-heading">Acceptance criteria</h3>
            {task.acceptance_criteria.length === 0 ? (
              <p className="pf-caption">No acceptance criteria recorded.</p>
            ) : (
              <ul className="pf-board-checklist">
                {task.acceptance_criteria.map((criterion, index) => (
                  <li key={`${task.id}-criterion-${String(index)}`}>
                    <Circle size={14} aria-hidden="true" />
                    <span>{criterion}</span>
                  </li>
                ))}
              </ul>
            )}
          </section>

          <section className="pf-board-drawer-section" aria-labelledby="targets-heading">
            <div className="pf-board-section-heading">
              <h3 id="targets-heading">Targets</h3>
              <span className="pf-caption">
                {task.targets.filter((target) => target.verified).length}/{task.targets.length} verified
              </span>
            </div>
            {task.targets.length === 0 ? (
              <p className="pf-caption">No structural targets.</p>
            ) : (
              <ul className="pf-board-target-list">
                {task.targets.map((target, index) => (
                  <li key={`${target.path}-${target.symbol_id}-${String(index)}`}>
                    <span className={target.verified ? "is-verified" : ""}>
                      {target.verified ? (
                        <Check size={14} aria-label="Verified" />
                      ) : (
                        <Circle size={14} aria-label="Not verified" />
                      )}
                    </span>
                    <span className="pf-board-target-copy">
                      <code>{target.path}</code>
                      <small>
                        {target.operation}
                        {target.symbol_id === "" ? "" : ` · ${target.symbol_id}`}
                      </small>
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </section>

          <section className="pf-board-drawer-section" aria-labelledby="dependencies-heading">
            <h3 id="dependencies-heading">Depends on</h3>
            {task.depends_on.length === 0 ? (
              <p className="pf-caption">No dependencies.</p>
            ) : (
              <div className="pf-board-dependencies">
                {task.depends_on.map((taskID) => (
                  <button key={taskID} type="button" onClick={() => onFocusTask(taskID)}>
                    <span className="pf-mono">{taskID}</span>
                    <ChevronRight size={14} aria-hidden="true" />
                  </button>
                ))}
              </div>
            )}
          </section>

          <section className="pf-board-drawer-section" aria-labelledby="estimates-heading">
            <h3 id="estimates-heading">Estimates</h3>
            <div className="pf-board-estimates">
              <article>
                <span className="pf-label">Human likely</span>
                <strong><Clock3 size={14} aria-hidden="true" />{task.human_estimate.likely}</strong>
                <p>{task.human_estimate.basis}</p>
              </article>
              <article>
                <span className="pf-label">Agent likely</span>
                <strong><Clock3 size={14} aria-hidden="true" />{task.agent_estimate.likely}</strong>
                <p>{task.agent_estimate.basis}</p>
              </article>
            </div>
          </section>

          <section className="pf-board-drawer-section" aria-labelledby="notes-heading">
            <div className="pf-board-section-heading">
              <h3 id="notes-heading">Context notes</h3>
              <span className="pf-caption"><MessageSquareText size={13} aria-hidden="true" />{task.notes.length}</span>
            </div>
            {task.notes.length === 0 ? (
              <p className="pf-caption">No additional context yet.</p>
            ) : (
              <ul className="pf-board-note-list">
                {task.notes.map((note) => (
                  <li key={note.id}>
                    <div>
                      <span className="pf-board-note-text">{note.text}</span>
                      <time dateTime={note.at}>{note.at}</time>
                    </div>
                    <button
                      type="button"
                      className="pf-btn pf-btn-danger pf-board-note-delete"
                      onClick={() => onDeleteNote(note.id)}
                      aria-label={`Delete note ${note.id}`}
                    >
                      <Trash2 size={14} aria-hidden="true" />
                    </button>
                  </li>
                ))}
              </ul>
            )}

            <div className="pf-board-note-compose">
              <label htmlFor="pf-board-note">Add context for the agent</label>
              <textarea
                id="pf-board-note"
                className="pf-textarea"
                rows={4}
                value={draft}
                placeholder="Add constraints, decisions, or implementation context…"
                onChange={(event) => {
                  const characters = Array.from(event.target.value);
                  setDraft(characters.slice(0, 2000).join(""));
                }}
                onKeyDown={(event) => {
                  if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
                    event.preventDefault();
                    submitNote();
                  }
                }}
              />
              <div className="pf-board-note-actions">
                <span className={characterCount >= 1900 ? "is-near-limit" : ""}>
                  {characterCount}/2000
                </span>
                <button
                  type="button"
                  className="pf-btn pf-btn-primary pf-btn-sm"
                  disabled={draft.trim() === "" || pending}
                  onClick={submitNote}
                >
                  Add note
                </button>
              </div>
              <span className="pf-caption">⌘/Ctrl + Enter to add</span>
            </div>
          </section>
        </div>
      </aside>
    </div>
  );
}
