/**
 * The skills already installed for each coding agent on this machine.
 *
 * Mirrors Go `host.SkillScanReport`, which scans the same roots the installer
 * reads: `~/.claude/skills`, `~/.omp/agent/skills`, `~/.agents/skills`,
 * `~/.codex/skills`, the Antigravity roots, the project roots and any OMP
 * custom directory. The point of the list is the answer to "which skills exist
 * here and could help", so it reports what a harness already offers instead of
 * keeping a second registry that drifts from the disk.
 *
 * Everything here is read-only. A skill belonging to another harness is shown,
 * never edited or removed: Plan Forge owns only the skills it installed.
 */

export interface DiscoveredSkill {
  /** The directory name, which is the id the harness resolves. */
  name: string;
  /** The `name:` of the SKILL.md frontmatter, when it differs from the id. */
  title?: string;
  /** The one-line `description:` from the frontmatter: what it does, when to use it. */
  description?: string;
  /** The harness that reads the root. */
  host: string;
  /** The surface that reads the root (`cli`, `app`); empty when a harness has one. */
  surface?: string;
  /** The directory the skill was found in. */
  root: string;
  /** Why that root is read: user, project, opted in, or a custom directory. */
  via: string;
  /** True only for a skill Plan Forge installs. */
  managed: boolean;
  /** False when the SKILL.md sets `enabled: false`, the rule the harness applies. */
  enabled: boolean;
  /**
   * Why this entry could not be read as a skill: a missing or malformed
   * SKILL.md. The entry is still listed so the window can say so.
   */
  problem?: string;
}

/** One root that could not be listed at all. */
export interface SkillRootProblem {
  path: string;
  via: string;
  error: string;
}

export interface SkillScanReport {
  skills: DiscoveredSkill[];
  unreadable?: SkillRootProblem[];
}

/** One harness (and surface) with the skills found under it. */
interface SkillGroup {
  /** Stable key for React and for de-duplication: host plus surface. */
  key: string;
  host: string;
  /** Empty when the harness reads one root and has no separate surface. */
  surface: string;
  skills: DiscoveredSkill[];
}

/**
 * How one entry reads at a glance. `blocked` is the case worth a word: the
 * directory is there and the harness cannot load it. The tones are the ones
 * `.pf-cap-row` already styles.
 */
type SkillTone = "serves" | "blocked" | "optional";

export const SKILL_TONE_LABEL: Record<SkillTone, string> = {
  serves: "Available",
  blocked: "Unreadable",
  optional: "Disabled",
};

/** The reason this entry is not a usable skill; empty when it is one. */
function skillProblem(skill: DiscoveredSkill): string {
  return skill.problem ?? "";
}

export function skillTone(skill: DiscoveredSkill): SkillTone {
  if (skillProblem(skill) !== "") return "blocked";
  if (!skill.enabled) return "optional";
  return "serves";
}

/** The line under the name: what the skill does, in its own words. */
export function skillDescription(skill: DiscoveredSkill): string {
  const described = skill.description ?? "";
  if (described !== "") return described;
  const titled = skill.title ?? "";
  if (titled !== "") return titled;
  return "no description in its SKILL.md";
}

/** The heading for a group: `claude`, or `gemini · app`. */
export function groupLabel(group: SkillGroup): string {
  return group.surface === "" ? group.host : `${group.host} · ${group.surface}`;
}

/**
 * Group the report by harness and surface, sorted so the list is stable across
 * scans. A root that failed to list is not a group: it has no skills to show.
 */
export function groupSkills(report: SkillScanReport | null | undefined): SkillGroup[] {
  const groups = new Map<string, SkillGroup>();
  for (const skill of report?.skills ?? []) {
    const surface = skill.surface ?? "";
    const key = `${skill.host}\u0000${surface}`;
    let group = groups.get(key);
    if (group === undefined) {
      group = { key, host: skill.host, surface, skills: [] };
      groups.set(key, group);
    }
    group.skills.push(skill);
  }
  const ordered = [...groups.values()];
  ordered.sort((a, b) => a.host.localeCompare(b.host) || a.surface.localeCompare(b.surface));
  for (const group of ordered) {
    group.skills.sort((a, b) => a.name.localeCompare(b.name) || a.root.localeCompare(b.root));
  }
  return ordered;
}

/**
 * True when the entry matches a filter query. The path and the reason a root is
 * read are searched too: "why is this here" is answered by `via` as often as by
 * the name.
 */
export function skillMatches(skill: DiscoveredSkill, query: string): boolean {
  const needle = query.trim().toLowerCase();
  if (needle === "") return true;
  return [
    skill.name,
    skill.title ?? "",
    skill.description ?? "",
    skill.host,
    skill.surface ?? "",
    skill.root,
    skill.via,
    skill.problem ?? "",
  ].some((field) => field.toLowerCase().includes(needle));
}

/**
 * Filter the groups, dropping the ones with nothing left. An empty query
 * returns the groups untouched, so clearing the box restores the full list.
 */
export function filterSkillGroups(groups: SkillGroup[], query: string): SkillGroup[] {
  if (query.trim() === "") return groups;
  return groups
    .map((group) => ({ ...group, skills: group.skills.filter((skill) => skillMatches(skill, query)) }))
    .filter((group) => group.skills.length > 0);
}

/** Entries a harness will not load: a missing or malformed SKILL.md. */
export function problemSkills(report: SkillScanReport | null | undefined): DiscoveredSkill[] {
  return (report?.skills ?? []).filter((skill) => skillProblem(skill) !== "");
}

/** Roots that could not be listed, with the reason. A missing root is not one. */
export function unreadableRoots(report: SkillScanReport | null | undefined): SkillRootProblem[] {
  return report?.unreadable ?? [];
}

/** How many skills the report holds; zero for a missing or empty report. */
export function skillCount(report: SkillScanReport | null | undefined): number {
  return (report?.skills ?? []).length;
}

function counted(n: number, one: string, many: string): string {
  return `${String(n)} ${n === 1 ? one : many}`;
}

/** The headline: how many skills, in how many harnesses, and what is wrong. */
export function skillSummary(report: SkillScanReport | null | undefined): string {
  const skills = report?.skills ?? [];
  if (skills.length === 0) return "No skills found in any harness root.";
  const harnesses = new Set(skills.map((skill) => skill.host)).size;
  const parts = [`${counted(skills.length, "skill", "skills")} in ${counted(harnesses, "harness", "harnesses")}`];
  const disabled = skills.filter((skill) => !skill.enabled).length;
  if (disabled > 0) parts.push(`${String(disabled)} disabled`);
  const problems = skills.length - skills.filter((skill) => skillProblem(skill) === "").length;
  if (problems > 0) parts.push(`${String(problems)} unreadable`);
  return `${parts.join(", ")}.`;
}
