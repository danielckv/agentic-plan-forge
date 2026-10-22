/**
 * Readers for the avatar frames as the Go side sends them.
 *
 * `AVATAR_DRAFT.content` is inline JSON (a `json.RawMessage`): an object for
 * pm.v1, architect.v1 and synthesis.v1, never a string. `AVATAR_CRITIQUE`
 * carries the whole critique set, `{run_id, cycle, issues: [Critique]}`
 * (internal/avatars/stage.go). Each reader is total: it returns null or an
 * empty list for a shape it does not recognise, never throws.
 */

/** The draft content as text: strings stay, anything else becomes JSON. */
export function draftContentText(content: unknown): string | undefined {
  if (content === undefined || content === null) return undefined;
  if (typeof content === "string") return content;
  try {
    return JSON.stringify(content);
  } catch {
    return undefined;
  }
}

function asObject(content: unknown): Record<string, unknown> | null {
  let v = content;
  if (typeof v === "string") {
    try {
      v = JSON.parse(v);
    } catch {
      return null;
    }
  }
  return v !== null && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, unknown>) : null;
}

const str = (v: unknown): string => (typeof v === "string" ? v : "");
const strs = (v: unknown): string[] => (Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : []);
const list = (v: unknown): Record<string, unknown>[] =>
  Array.isArray(v) ? v.filter((x): x is Record<string, unknown> => x !== null && typeof x === "object") : [];

/** One critic finding (`contracts.Critique`), as the page shows it. */
export interface Finding {
  issueId: string;
  severity: "CRITICAL" | "HIGH" | "MEDIUM";
  summary: string;
  requiredChange: string;
  targets: string[];
  resolved: boolean;
}

/** The findings of one AVATAR_CRITIQUE payload. */
export function parseCritique(payload: unknown): { cycle: number; findings: Finding[] } {
  const p = asObject(payload);
  if (p === null) return { cycle: 0, findings: [] };
  const findings = list(p.issues).map((c) => {
    const sev = str(c.severity);
    return {
      issueId: str(c.id),
      severity: sev === "CRITICAL" || sev === "HIGH" ? sev : "MEDIUM",
      summary: str(c.evidence),
      requiredChange: str(c.required_change),
      targets: strs(c.target_ids),
      resolved: str(c.status) === "resolved",
    } satisfies Finding;
  });
  return { cycle: typeof p.cycle === "number" ? p.cycle : 0, findings: findings.filter((f) => f.issueId !== "") };
}

/** The Product Manager's draft (`synthesizer.PMDoc`). */
export interface PMDraft {
  epics: Array<{ id: string; title: string; goal: string }>;
  stories: Array<{ id: string; epicId: string; actor: string; need: string; benefit: string; criteria: string[] }>;
}

export function parsePMDraft(content: unknown): PMDraft | null {
  const o = asObject(content);
  if (o === null || !Array.isArray(o.epics)) return null;
  return {
    epics: list(o.epics).map((e) => ({ id: str(e.id), title: str(e.title), goal: str(e.goal) })),
    stories: list(o.stories).map((s) => ({
      id: str(s.id),
      epicId: str(s.epic_id),
      actor: str(s.actor),
      need: str(s.need),
      benefit: str(s.benefit),
      criteria: strs(s.acceptance_criteria),
    })),
  };
}

/** One epic of the PM draft as a Markdown file: goal, stories, criteria. */
export function pmEpicMarkdown(draft: PMDraft, epicId: string): string {
  const epic = draft.epics.find((e) => e.id === epicId);
  if (epic === undefined) return "";
  const stories = draft.stories.filter((s) => s.epicId === epicId);
  const lines = [`# ${epic.id} · ${epic.title}`, ""];
  if (epic.goal !== "") lines.push(`> ${epic.goal}`, "");
  lines.push("## User stories", "");
  if (stories.length === 0) lines.push("_No stories yet._");
  for (const s of stories) {
    lines.push(`### ${s.id}`, "", `As a **${s.actor}**, I want to ${s.need}, so that ${s.benefit}.`, "");
    for (const c of s.criteria) lines.push(`- [ ] ${c}`);
    lines.push("");
  }
  return lines.join("\n").trimEnd() + "\n";
}

/** The Technical Architect's draft (`synthesizer.ArchitectDoc`), as far as the page shows it. */
export interface ArchitectDraft {
  summary: string;
  constraints: string[];
  workstreams: Array<{ id: string; role: string; modules: string[]; dependsOn: string[] }>;
  nodes: Array<{ id: string; label: string }>;
  edges: Array<{ from: string; to: string; label: string }>;
}

export function parseArchitectDraft(content: unknown): ArchitectDraft | null {
  const o = asObject(content);
  if (o === null || typeof o.summary !== "string") return null;
  const arch = asObject(asObject(o.diagrams)?.architecture);
  return {
    summary: str(o.summary),
    constraints: strs(o.constraints),
    workstreams: list(o.workstreams).map((w) => ({
      id: str(w.id),
      role: str(w.role),
      modules: strs(w.module_paths),
      dependsOn: strs(w.depends_on),
    })),
    nodes: list(arch?.nodes).map((n) => ({ id: str(n.id), label: str(n.label) })),
    edges: list(arch?.edges).map((e) => ({ from: str(e.from), to: str(e.to), label: str(e.label) })),
  };
}

/** A Mermaid label: quoted, with anything outside a plain set turned to a space. */
function label(s: string): string {
  const clean = s.replace(/[^A-Za-z0-9 _.,:()/+-]/g, " ").replace(/\s+/g, " ").trim().slice(0, 60);
  return `"${clean === "" ? "unnamed" : clean}"`;
}

/**
 * The architecture as `graph TD`, rendered the way the Go compiler does
 * (internal/synthesizer.RenderGraph): generated node IDs, quoted labels.
 * Null when there is nothing to draw.
 */
export function architectureMermaid(d: ArchitectDraft): string | null {
  if (d.nodes.length === 0) return null;
  const ids = new Map<string, string>();
  const out = ["graph TD"];
  d.nodes.forEach((n, i) => {
    ids.set(n.id, `n${i}`);
    out.push(`    n${i}[${label(n.label)}]`);
  });
  for (const e of d.edges) {
    const from = ids.get(e.from);
    const to = ids.get(e.to);
    if (from === undefined || to === undefined) continue;
    out.push(e.label.trim() === "" ? `    ${from} --> ${to}` : `    ${from} -->|${label(e.label)}| ${to}`);
  }
  return out.join("\n");
}
