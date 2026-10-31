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
  /** Carried only when the impact map put this declaration on an epic. */
  verdict?: ImpactVerdict;
  reason?: string;
  confidence?: string;
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

/** One vertex of the 2D component graph (`app.GraphNode`). */
export interface GraphNode {
  id: string;
  kind: "module" | "file";
  label: string;
  path: string;
  language?: string;
  symbols: number;
  depth: number;
}

/** One directed relationship in the component graph (`app.GraphEdge`). */
export interface GraphEdge {
  from: string;
  to: string;
  kind: "imports" | "contains";
}

/** GET /api/component-graph (`app.GraphView`). */
export interface GraphView {
  ready: boolean;
  stack: string[];
  focus?: string;
  depth: number;
  nodes: GraphNode[];
  edges: GraphEdge[];
  truncated: boolean;
  truncated_at?: number;
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

/**
 * What happens to one part of the project (`contracts.Impact*`). An empty
 * verdict is a keyword match: it names a part and claims nothing more.
 */
export type ImpactVerdict = "" | "change" | "add" | "reuse" | "conflicts_with";

/** One part of the project the requirements touch (`contracts.ImpactLink`). */
export interface ImpactLink {
  id: string;
  area: string;
  kind: "code" | "doc" | "spec" | "decision";
  path?: string;
  line?: number;
  symbol_id?: string;
  symbol_name?: string;
  symbol_kind?: string;
  heading?: string;
  verdict: ImpactVerdict;
  /** One plain-language sentence. Empty on a keyword match. */
  reason?: string;
  confidence?: string;
  source: "lexical" | "model";
  provenance?: string;
}

/** One draft epic's links (`contracts.ImpactEpic`). */
export interface ImpactEpic {
  epic_id: string;
  title: string;
  links: ImpactLink[];
}

/** GET /api/impact (`contracts.ImpactMap`). */
export interface ImpactMap {
  schema_version: number;
  state: "pending" | "ready" | "lexical" | "failed";
  revision: number;
  prd_revision: number;
  prd_hash?: string;
  worktree_hash?: string;
  summary?: string;
  whole: ImpactLink[];
  epics: ImpactEpic[];
  conflicts: ImpactLink[];
  notes?: string[];
  generated_at?: string;
}

/** One host, model and role's share of this session (`contracts.UsageRow`). */
export interface UsageRow {
  host: string;
  model: string;
  role: string;
  requests: number;
  failures: number;
  input_tokens: number;
  output_tokens: number;
  milliseconds: number;
}

/** GET /api/usage (`contracts.UsageReport`). */
export interface UsageReport {
  rows: UsageRow[];
  totals: UsageRow;
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
  links: ImpactLink[];
}

/** GET /api/plan-files (`app.PlanFilesView`). */
interface PlanFilesView {
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

export function fetchImpact(): Promise<ImpactMap> {
  return getJSON<ImpactMap>("/api/impact");
}

export function fetchUsage(): Promise<UsageReport> {
  return getJSON<UsageReport>("/api/usage");
}

/** The blocks (no argument), one block's declarations, or a name search. */
export function fetchComponents(query: { module?: string; q?: string } = {}): Promise<ComponentsView> {
  const params = new URLSearchParams();
  if (query.module !== undefined && query.module !== "") params.set("module", query.module);
  if (query.q !== undefined && query.q.trim() !== "") params.set("q", query.q.trim());
  const suffix = params.toString();
  return getJSON<ComponentsView>(suffix === "" ? "/api/components" : `/api/components?${suffix}`);
}

/** One heading of a planning document (`repowork.Heading`). */
export interface RepositoryHeading {
  level: number;
  text: string;
  line: number;
}

/** One work item with its source (`repowork.Item`). */
export interface RepositoryItem {
  id: string;
  text: string;
  /** `open`, `done` or `unknown`: never inferred from prose. */
  status: string;
  line: number;
  /** `repowork.Item` uses snake_case on the wire. */
  end_line: number;
  source: string;
  depends_on: string[];
}

/** One planning document of the repository (`repowork.Document`). */
export interface RepositoryDocument {
  id: string;
  path: string;
  kind: "state" | "plan" | "spec" | "adr" | "follow-up" | "epic" | "task" | "document";
  sha256: string;
  bytes: number;
  headings: RepositoryHeading[];
  items: RepositoryItem[];
}

/** One file the index left out, with the reason (`repowork.Skipped`). */
export interface RepositorySkipped {
  path: string;
  reason: string;
}

/** GET /api/repository-work (`repowork.Inventory`). */
export interface RepositoryWorkView {
  revision: string;
  partial: boolean;
  skipped: RepositorySkipped[] | null;
  documents: RepositoryDocument[] | null;
  /** The explicit order of the active state file's Next section. */
  next: RepositoryItem[] | null;
  generated_at: string;
}

/** GET /api/repository-work/document (`app.repositoryWorkDocumentView`). */
export interface RepositoryWorkDocument {
  id: string;
  path: string;
  kind: RepositoryDocument["kind"];
  sha256: string;
  bytes: number;
  headings: RepositoryHeading[];
  items: RepositoryItem[];
  markdown: string;
}

export function fetchRepositoryWork(): Promise<RepositoryWorkView> {
  return getJSON<RepositoryWorkView>("/api/repository-work");
}

export function fetchRepositoryDocument(id: string): Promise<RepositoryWorkDocument> {
  return getJSON<RepositoryWorkDocument>(`/api/repository-work/document?id=${encodeURIComponent(id)}`);
}

/** One file a documents-only export would write (`app.bundleFileView`). */
export interface BundleFile {
  path: string;
  kind: "plan" | "spec" | "adr";
  title: string;
}

/** GET /api/documents (`app.BundleView`). A preview: nothing is written. */
export interface BundleView {
  available: boolean;
  problem?: string;
  files: BundleFile[];
}

/** POST /api/documents/export (`app.ExportReport`). */
export interface ExportReport {
  state: "exported" | "confirm" | "unavailable" | "collision" | "blocked" | "mismatch" | "failed";
  committed?: string[];
  manifest?: string;
  problem?: string;
}

export function fetchDocuments(): Promise<BundleView> {
  return getJSON<BundleView>("/api/documents");
}

/**
 * Write the generated documents into the repository. The confirmation is part
 * of the request because the export is the only path that puts generated files
 * on disk, and it never runs as a side effect of opening the page.
 */
export async function exportDocuments(body: { confirmed: boolean; approvalId?: string }): Promise<ExportReport> {
  const res = await apiFetch("/api/documents/export", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const report = (await res.json().catch(() => null)) as ExportReport | null;
  if (report !== null) return report;
  return { state: "failed", problem: `the export answered ${String(res.status)}` };
}
