import type { ConnState } from "./store";
import type { SprintResponse, SprintTask, SprintView } from "./types";

/**
 * The sprint hierarchy: epic, then story, then task, grouped from the plan the
 * forge approved. Tasks whose epic the manifest does not carry group under an
 * "unplanned" heading, which is where accepted discoveries belong.
 */

const COLUMN_LABEL: Record<string, string> = {
  todo: "To do",
  in_progress: "In progress",
  done: "Done",
  drifted: "Needs attention",
};

const COLUMN_TONE: Record<string, string> = {
  todo: "",
  in_progress: " pf-chip--accent",
  done: " pf-center-chip--ok",
  drifted: " pf-center-chip--warn",
};

const UNPLANNED = "__unplanned__";

function group(tasks: SprintTask[]): Map<string, Map<string, SprintTask[]>> {
  const epics = new Map<string, Map<string, SprintTask[]>>();
  for (const task of tasks) {
    const epic = task.epic_id ?? UNPLANNED;
    const story = task.story_id ?? UNPLANNED;
    const stories = epics.get(epic) ?? new Map<string, SprintTask[]>();
    stories.set(story, [...(stories.get(story) ?? []), task]);
    epics.set(epic, stories);
  }
  return epics;
}

function TaskRow({ task }: { task: SprintTask }): React.JSX.Element {
  return (
    <li className="pf-center-task">
      <code className="pf-center-task-id">{task.id}</code>
      <span className="pf-center-task-title">{task.title}</span>
      <span className="pf-center-task-meta">
        {task.targets > 0 ? `${String(task.targets_met)}/${String(task.targets)} targets` : "—"}
      </span>
      {task.blocked ? <span className="pf-badge pf-badge--high">blocked</span> : null}
      <span className={`pf-chip${COLUMN_TONE[task.column] ?? ""}`}>
        {COLUMN_LABEL[task.column] ?? task.column}
      </span>
    </li>
  );
}

function RepoSprint({ view }: { view: SprintView }): React.JSX.Element {
  const epics = group(view.tasks);
  return (
    <section className="pf-center-card" aria-labelledby={`pf-sprint-${view.repo_key}`}>
      <header className="pf-center-card-head">
        <h2 id={`pf-sprint-${view.repo_key}`} className="pf-h3">
          {view.title ?? view.repo_name}
        </h2>
        <span className="pf-caption">
          {String(view.verified)} of {String(view.total)} verified
        </span>
      </header>
      {[...epics.entries()].map(([epic, stories]) => (
        <div key={epic} className="pf-center-epic">
          <h3 className="pf-center-label">{epic === UNPLANNED ? "Outside the plan" : epic}</h3>
          {[...stories.entries()].map(([story, tasks]) => (
            <div key={story} className="pf-center-story">
              {story === UNPLANNED ? null : <h4 className="pf-center-story-id">{story}</h4>}
              <ul className="pf-center-tasks">
                {tasks.map((task) => (
                  <TaskRow key={task.id} task={task} />
                ))}
              </ul>
            </div>
          ))}
        </div>
      ))}
    </section>
  );
}

export function SprintPage({
  sprint,
  conn,
}: {
  sprint: SprintResponse;
  conn: ConnState;
}): React.JSX.Element {
  const approved = sprint.repos.filter((r) => r.approved);
  if (approved.length === 0) {
    return (
      <div className="pf-center-empty pf-center-empty--page">
        <h1 className="pf-h2">No approved plan</h1>
        <p className="pf-body">
          {conn === "live"
            ? "Run /plan-forge in your agent to forge one. Until then, live sessions are still recorded."
            : "Waiting for the command center…"}
        </p>
      </div>
    );
  }
  return (
    <>
      <div className="pf-center-pagehead">
        <h1 className="pf-h2">Sprint</h1>
        <p className="pf-caption">Epic, story and task, from the approved plan.</p>
      </div>
      <div className="pf-center-stack">
        {approved.map((view) => (
          <RepoSprint key={view.repo_key} view={view} />
        ))}
      </div>
    </>
  );
}
