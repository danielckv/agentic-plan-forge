import { describe, expect, it } from "vitest";

import {
  filterSkillGroups,
  groupLabel,
  groupSkills,
  problemSkills,
  skillCount,
  skillDescription,
  skillMatches,
  skillSummary,
  skillTone,
  unreadableRoots,
} from "./skills";
import type { DiscoveredSkill, SkillScanReport } from "./skills";

function skill(part: Partial<DiscoveredSkill> & { name: string; host: string }): DiscoveredSkill {
  return {
    root: `/home/owner/.${part.host}/skills`,
    via: "user",
    managed: false,
    enabled: true,
    ...part,
  };
}

function report(skills: DiscoveredSkill[], unreadable?: SkillScanReport["unreadable"]): SkillScanReport {
  return unreadable === undefined ? { skills } : { skills, unreadable };
}

describe("groupSkills", () => {
  it("groups by harness and surface, ordered, with the skills sorted inside", () => {
    const groups = groupSkills(
      report([
        skill({ name: "zeta", host: "claude", description: "writes release notes" }),
        skill({ name: "alpha", host: "gemini", surface: "app" }),
        skill({ name: "beta", host: "claude", description: "reviews migrations" }),
        skill({ name: "gamma", host: "gemini", surface: "cli" }),
      ]),
    );

    expect(groups.map(groupLabel)).toEqual(["claude", "gemini · app", "gemini · cli"]);
    expect(groups[0]?.skills.map((s) => s.name)).toEqual(["beta", "zeta"]);
    expect(groups[0]?.key).not.toBe(groups[1]?.key);
  });

  it("treats a missing surface and an empty surface as the same harness", () => {
    const groups = groupSkills(
      report([skill({ name: "a", host: "omp" }), skill({ name: "b", host: "omp", surface: "" })]),
    );

    expect(groups).toHaveLength(1);
    expect(groups[0]?.skills.map((s) => s.name)).toEqual(["a", "b"]);
  });

  it("returns nothing for a missing report", () => {
    expect(groupSkills(null)).toEqual([]);
    expect(groupSkills(undefined)).toEqual([]);
    expect(skillCount(null)).toBe(0);
    expect(skillCount(undefined)).toBe(0);
    expect(skillCount(report([skill({ name: "a", host: "claude" })]))).toBe(1);
  });

  it("keeps two skills of the same name in different roots apart", () => {
    const groups = groupSkills(
      report([
        skill({ name: "review", host: "claude", root: "/home/owner/.claude/skills" }),
        skill({ name: "review", host: "claude", root: "/repo/.claude/skills", via: "project" }),
      ]),
    );

    expect(groups[0]?.skills).toHaveLength(2);
  });
});

describe("skillMatches and filterSkillGroups", () => {
  const groups = groupSkills(
    report([
      skill({
        name: "security-boundaries",
        host: "claude",
        description: "Threat models an interface before implementation",
        root: "/home/owner/.claude/skills",
        via: "user",
      }),
      skill({ name: "migrations", host: "omp", surface: "cli", root: "/repo/.omp/skills", via: "opted in" }),
      skill({ name: "broken", host: "codex", root: "/home/owner/.codex/skills", problem: "no SKILL.md" }),
    ]),
  );

  it("matches on name, description, harness, path and reason", () => {
    expect(skillMatches(groups[0]!.skills[0]!, "SECURITY")).toBe(true);
    expect(skillMatches(groups[0]!.skills[0]!, "threat models")).toBe(true);
    expect(skillMatches(groups[0]!.skills[0]!, "codex")).toBe(false);
    // Groups are ordered by harness: claude, codex, then omp · cli.
    expect(skillMatches(groups[1]!.skills[0]!, "no skill.md")).toBe(true);
    expect(skillMatches(groups[2]!.skills[0]!, "opted in")).toBe(true);
    expect(skillMatches(groups[2]!.skills[0]!, "/repo/")).toBe(true);
  });

  it("matches everything on an empty or blank query", () => {
    expect(skillMatches(groups[0]!.skills[0]!, "   ")).toBe(true);
    expect(filterSkillGroups(groups, "   ")).toBe(groups);
  });

  it("drops the groups with nothing left", () => {
    const shown = filterSkillGroups(groups, "migration");

    expect(shown.map(groupLabel)).toEqual(["omp · cli"]);
    expect(shown[0]?.skills.map((s) => s.name)).toEqual(["migrations"]);
  });

  it("returns an empty list when nothing matches", () => {
    expect(filterSkillGroups(groups, "kubernetes")).toEqual([]);
  });

  it("does not mutate the groups it filters", () => {
    filterSkillGroups(groups, "migration");

    expect(groups[0]?.skills).toHaveLength(1);
    expect(groups[1]?.skills).toHaveLength(1);
  });
});

describe("skillTone and skillDescription", () => {
  it("calls a malformed entry unreadable and a disabled one disabled", () => {
    expect(skillTone(skill({ name: "a", host: "claude" }))).toBe("serves");
    expect(skillTone(skill({ name: "b", host: "claude", enabled: false }))).toBe("optional");
    expect(skillTone(skill({ name: "c", host: "claude", problem: "no SKILL.md" }))).toBe("blocked");
  });

  it("prefers the description, falls back to the title, then says so", () => {
    expect(skillDescription(skill({ name: "a", host: "claude", description: "does a thing", title: "A" }))).toBe(
      "does a thing",
    );
    expect(skillDescription(skill({ name: "b", host: "claude", title: "B" }))).toBe("B");
    expect(skillDescription(skill({ name: "c", host: "claude" }))).toBe("no description in its SKILL.md");
  });
});

describe("skillSummary", () => {
  it("says so when no root holds a skill", () => {
    expect(skillSummary(report([]))).toBe("No skills found in any harness root.");
    expect(skillSummary(null)).toBe("No skills found in any harness root.");
  });

  it("counts skills, harnesses, disabled entries and unreadable ones", () => {
    const summary = skillSummary(
      report([
        skill({ name: "a", host: "claude" }),
        skill({ name: "b", host: "claude", enabled: false }),
        skill({ name: "c", host: "omp", problem: "no SKILL.md" }),
      ]),
    );

    expect(summary).toBe("3 skills in 2 harnesses, 1 disabled, 1 unreadable.");
  });

  it("uses the singular for one skill in one harness", () => {
    expect(skillSummary(report([skill({ name: "a", host: "claude" })]))).toBe("1 skill in 1 harness.");
  });
});

describe("problem reporting", () => {
  it("lists the entries a harness cannot load", () => {
    const entries = problemSkills(
      report([skill({ name: "a", host: "claude" }), skill({ name: "b", host: "omp", problem: "no SKILL.md" })]),
    );

    expect(entries.map((s) => s.name)).toEqual(["b"]);
  });

  it("passes the roots that could not be listed through", () => {
    const roots = unreadableRoots(
      report([], [{ path: "/home/owner/.codex/skills", via: "user", error: "permission denied" }]),
    );

    expect(roots).toEqual([{ path: "/home/owner/.codex/skills", via: "user", error: "permission denied" }]);
    expect(unreadableRoots(report([]))).toEqual([]);
    expect(unreadableRoots(null)).toEqual([]);
  });
});
