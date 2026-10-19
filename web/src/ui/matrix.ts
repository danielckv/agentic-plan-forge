/**
 * Jira matrix model and filter. Epics -> stories -> subtasks come from the
 * synthesis manifest; the filter narrows by subtask ID, title, or target file.
 */

export type Severity = "CRITICAL" | "HIGH" | "MEDIUM";

export interface SubtaskRow {
  id: string;
  title: string;
  severity?: Severity;
  acceptance: string[];
  targets: string[];
  verification: string[];
  humanEstimate: string;
  agentEstimate: string;
}

export interface StoryRow {
  id: string;
  need: string;
  subtasks: SubtaskRow[];
}

export interface EpicRow {
  id: string;
  title: string;
  goal: string;
  stories: StoryRow[];
}

export function subtaskMatches(subtask: Pick<SubtaskRow, "id" | "title" | "targets">, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (q === "") return true;
  return (
    subtask.id.toLowerCase().includes(q) ||
    subtask.title.toLowerCase().includes(q) ||
    subtask.targets.some((t) => t.toLowerCase().includes(q))
  );
}

export interface MatrixCounts {
  epics: number;
  stories: number;
  subtasks: number;
}

export interface FilteredMatrix {
  /** Epics and stories pruned to those holding at least one matching subtask. */
  epics: EpicRow[];
  matched: number;
  total: number;
  /** True when a non-empty query is applied. */
  active: boolean;
}

export function countMatrix(epics: readonly EpicRow[]): MatrixCounts {
  let stories = 0;
  let subtasks = 0;
  for (const e of epics) {
    stories += e.stories.length;
    for (const s of e.stories) subtasks += s.subtasks.length;
  }
  return { epics: epics.length, stories, subtasks };
}

export function filterMatrix(epics: readonly EpicRow[], query: string): FilteredMatrix {
  const total = countMatrix(epics).subtasks;
  if (query.trim() === "") return { epics: [...epics], matched: total, total, active: false };

  const kept: EpicRow[] = [];
  let matched = 0;
  for (const epic of epics) {
    const stories: StoryRow[] = [];
    for (const story of epic.stories) {
      const subtasks = story.subtasks.filter((st) => subtaskMatches(st, query));
      if (subtasks.length === 0) continue;
      matched += subtasks.length;
      stories.push({ ...story, subtasks });
    }
    if (stories.length > 0) kept.push({ ...epic, stories });
  }
  return { epics: kept, matched, total, active: true };
}

/** Every epic and story ID, for "Expand all". */
export function allOpenIds(epics: readonly EpicRow[]): { epics: Set<string>; stories: Set<string> } {
  const openEpics = new Set<string>();
  const openStories = new Set<string>();
  for (const e of epics) {
    openEpics.add(e.id);
    for (const s of e.stories) openStories.add(s.id);
  }
  return { epics: openEpics, stories: openStories };
}
