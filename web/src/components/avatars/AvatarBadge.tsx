import { Compass, DraftingCompass, FileSearch, GitMerge, ShieldAlert, Sparkles, Wrench } from "lucide-react";

/**
 * The avatars' faces. Each role has a name, a one-line job, a color from the
 * theme tokens and a glyph, drawn as a round badge so a timeline line reads
 * like a message from someone. Domain specialists share one face and carry
 * their workstream in the name.
 */

export interface AvatarSpec {
  id: string;
  name: string;
  job: string;
  color: string;
  soft: string;
  icon: React.ComponentType<{ size?: number; strokeWidth?: number }>;
}

/** The four avatars every run has, in the order they work. */
export const MAIN_AVATARS: AvatarSpec[] = [
  { id: "pm", name: "Product Manager", job: "Turns requirements into epics and stories", color: "var(--pf-accent)", soft: "var(--pf-accent-soft)", icon: Compass },
  { id: "architect", name: "Technical Architect", job: "Maps the work onto the real code", color: "var(--pf-epic)", soft: "var(--pf-epic-soft)", icon: DraftingCompass },
  { id: "critic", name: "Red-Team Critic", job: "Attacks the drafts for failure states", color: "var(--pf-danger)", soft: "var(--pf-danger-soft)", icon: ShieldAlert },
  { id: "synthesizer", name: "Lead Synthesizer", job: "Locks the design into one spec", color: "var(--pf-ok)", soft: "var(--pf-ok-soft)", icon: Sparkles },
  { id: "code_reviewer", name: "Code Reviewer", job: "Checks the plan's code claims against the repository", color: "var(--pf-warn)", soft: "var(--pf-warn-soft)", icon: FileSearch },
  { id: "tech_lead", name: "Technical Leader", job: "Integrates the findings and directs the one repair", color: "var(--pf-accent)", soft: "var(--pf-accent-soft)", icon: GitMerge },
];

const SPECIALIST: Omit<AvatarSpec, "id" | "name"> = {
  job: "Details one workstream",
  color: "var(--pf-warn)",
  soft: "var(--pf-warn-soft)",
  icon: Wrench,
};

/** The spec for any avatar ID the authority sends, including specialist:<workstream>. */
export function avatarSpec(id: string): AvatarSpec {
  const main = MAIN_AVATARS.find((a) => a.id === id);
  if (main !== undefined) return main;
  if (id.startsWith("specialist:")) return { ...SPECIALIST, id, name: `Specialist · ${id.slice("specialist:".length)}` };
  return { ...SPECIALIST, id, name: id };
}

export function AvatarBadge(props: { id: string; size?: number; speaking?: boolean }): React.ReactElement {
  const { id, size = 36, speaking = false } = props;
  const spec = avatarSpec(id);
  const Icon = spec.icon;
  return (
    <span
      className="pf-face"
      data-speaking={speaking}
      style={{ width: size, height: size, color: spec.color, background: spec.soft, ["--pf-face-ring" as string]: spec.color }}
      aria-hidden="true"
    >
      <Icon size={Math.round(size * 0.48)} strokeWidth={2} />
    </span>
  );
}
