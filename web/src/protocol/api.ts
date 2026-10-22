import { apiFetch } from "./bridge";

/**
 * Shapes and fetchers for the read-only planning endpoints the bridge serves:
 * the draft epics as Markdown files, the repository's public components, and
 * the project context the technical avatars receive. Each mirrors a Go type
 * in internal/app (planfiles.go, projectcontext.go) or internal/contracts.
 */

/** One public declaration of the repository (`app.ComponentRef`). */
export interface ComponentRef {
  id: string;
  name: string;
  kind: string;
  path: string;
  line: number;
  signature?: string;
  module: string;
  language: string;
}

/** One block: a directory of one language (`app.ComponentModule`). */
export interface ComponentModule {
  path: string;
  language: string;
  symbols: number;
}

/** GET /api/components (`app.ComponentsView`). */
export interface ComponentsView {
  ready: boolean;
  stack: string[];
  modules: ComponentModule[];
  symbols: ComponentRef[];
  truncated: boolean;
}

/** One excerpt of the project's notes (`contracts.ContextHit`). */
export interface ContextHit {
  path: string;
  heading?: string;
  text: string;
  score: number;
}

/** GET /api/context (`contracts.ProjectContext`). */
export interface ProjectContext {
  source: "recall-index" | "legacy-recall-index" | "files" | "none";
  hits: ContextHit[];
  branch?: string;
  head?: string;
  recent_commits?: string[];
  truncated?: boolean;
  notes?: string[];
}

/** One draft epic rendered as a file (`app.PlanFile`). */
export interface PlanFile {
  id: string;
  title: string;
  goal: string;
  filename: string;
  markdown: string;
  requirements: number;
  criteria: number;
  components: ComponentRef[];
  notes: ContextHit[];
}

/** GET /api/plan-files (`app.PlanFilesView`). */
export interface PlanFilesView {
  revision: number;
  files: PlanFile[];
}

async function getJSON<T>(path: string): Promise<T> {
  const res = await apiFetch(path);
  if (!res.ok) throw new Error(`${path} answered ${res.status}`);
  return (await res.json()) as T;
}

export function fetchPlanFiles(): Promise<PlanFilesView> {
  return getJSON<PlanFilesView>("/api/plan-files");
}

export function fetchProjectContext(): Promise<ProjectContext> {
  return getJSON<ProjectContext>("/api/context");
}

/** The blocks (no argument), one block's declarations, or a name search. */
export function fetchComponents(query: { module?: string; q?: string } = {}): Promise<ComponentsView> {
  const params = new URLSearchParams();
  if (query.module !== undefined && query.module !== "") params.set("module", query.module);
  if (query.q !== undefined && query.q.trim() !== "") params.set("q", query.q.trim());
  const suffix = params.toString();
  return getJSON<ComponentsView>(suffix === "" ? "/api/components" : `/api/components?${suffix}`);
}
