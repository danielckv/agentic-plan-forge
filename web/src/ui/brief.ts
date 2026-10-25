/**
 * The brief outline: the five things the deliberation asks about, and whether
 * the document mentions each one.
 *
 * A product manager arriving at Page 1 sees one empty textarea. The avatars
 * will go on to ask about audience, outcome, motivation, non-goals and what
 * already exists — the `plan-forge` skill interviews for exactly those five
 * before it ever opens the window — but nothing on the page says so. Someone
 * who types a paragraph and presses Confirm has no way to know what the
 * deliberation was going to want.
 *
 * The outline is the primary thing here and the detection is secondary. Even
 * where the word search is wrong, the five sections and their Add buttons are
 * still a usable scaffold; where it is right, it saves the writer a re-read.
 *
 * Detection is deliberately lexical and local. It makes no model call: whether
 * a brief is good enough is the judgement the four avatars exist to make, and a
 * sixth opinion rendered as a score in the corner of the composer would be
 * slower and less honest than the run itself. What this can report is narrower
 * and falsifiable: the words are there, or they are not.
 */

export type BriefSectionID = "audience" | "outcome" | "motivation" | "nonGoals" | "existing";

export interface BriefSection {
  id: BriefSectionID;
  /** Rail heading, in the writer's language rather than the schema's. */
  label: string;
  /** What the deliberation does with it; shown when the section is absent. */
  need: string;
  /** Markdown inserted by Add. */
  insert: string;
  /** Lower-case substrings that count as mentioning the topic. */
  cues: readonly string[];
}

/**
 * The five sections, in the order the skill interviews for them, so the window
 * and the pre-launch interview ask for the same things in the same order. A
 * writer who answered the interview finds this already ticked.
 */
export const BRIEF_SECTIONS: readonly BriefSection[] = [
  {
    id: "audience",
    label: "Who it is for",
    need: "The Product Manager writes every story as an actor with a need. With no audience named it invents one.",
    insert: "## Who it is for\n\n",
    // Role nouns carry most of the weight: a real brief names the job, not
    // the word "audience". The list is open-ended by nature and will miss
    // titles it does not know, which is the main reason a miss here is worded
    // as "not mentioned" rather than "missing".
    cues: [
      "who it is for",
      "for whom",
      "persona",
      "audience",
      "our users",
      "the user",
      "users who",
      "customer",
      "people who",
      "anyone who",
      "operator",
      "administrator",
      "analyst",
      "engineer",
      "developer",
      "manager",
      "reviewer",
      "approver",
      "the team",
      "staff",
    ],
  },
  {
    id: "outcome",
    label: "What done looks like",
    need: "Acceptance criteria come from this. Without it they describe the feature back to you rather than a result.",
    insert: "## What done looks like\n\n",
    cues: [
      "done looks like",
      "definition of done",
      "acceptance",
      "success",
      "we will know",
      "measured by",
      "so that",
      "outcome",
      "result is",
    ],
  },
  {
    id: "motivation",
    label: "Why now",
    need: "The Red Team critic weighs a risk against what the work is worth. With no stakes it has nothing to weigh.",
    insert: "## Why now\n\n",
    cues: [
      "why now",
      "because",
      "currently",
      "today we",
      "right now",
      "the problem",
      "pain",
      "costs us",
      "losing",
      "deadline",
      "blocked",
    ],
  },
  {
    id: "nonGoals",
    label: "What is out of scope",
    need: "The cheapest thing you can write. Everything excluded here is work the plan will not contain.",
    insert: "## What is out of scope\n\n- ",
    cues: [
      "out of scope",
      "not doing",
      "non-goal",
      "nongoal",
      "we will not",
      "won't",
      "will not be",
      "excluded",
      "not in this",
      "deliberately not",
      "leave for later",
      "another time",
    ],
  },
  {
    id: "existing",
    label: "What already exists",
    need: "The Technical Architect maps the work onto real modules. Naming what to build on stops it proposing a parallel one.",
    insert: "## What already exists\n\n",
    cues: [
      "already exists",
      "already have",
      "existing",
      "current system",
      "built on",
      "today the",
      "legacy",
      "reuse",
      "extend the",
      "in place",
    ],
  },
];

/** One section's state against the document. */
export interface BriefCheck {
  section: BriefSection;
  /** True when some line mentions the topic. Mentioned, not answered. */
  mentioned: boolean;
  /** The line that matched, trimmed and bounded. Empty when none did. */
  evidence: string;
}

/** Longest evidence line shown in the rail before it is cut. */
export const MAX_EVIDENCE_CHARS = 120;

/**
 * Below this many words the document is a sketch, not a brief, and reporting
 * five absent sections against three words is noise rather than help.
 */
export const MIN_WORDS_TO_CHECK = 12;

export function wordCount(text: string): number {
  const trimmed = text.trim();
  if (trimmed === "") return 0;
  return trimmed.split(/\s+/).length;
}

/**
 * readBrief reports, per section, whether the document mentions it and which
 * line said so. Matching is case-insensitive substring over whole lines, so a
 * cue inside a longer sentence counts; a heading counts too, which is what
 * makes an inserted section tick as soon as it is there.
 */
export function readBrief(text: string): BriefCheck[] {
  const lines = text.split("\n");
  const lowered = lines.map((line) => line.toLowerCase());
  return BRIEF_SECTIONS.map((section) => {
    for (let i = 0; i < lowered.length; i++) {
      const line = lowered[i];
      if (line === undefined || line.trim() === "") continue;
      if (!section.cues.some((cue) => line.includes(cue))) continue;
      return { section, mentioned: true, evidence: clamp(lines[i] ?? "") };
    }
    return { section, mentioned: false, evidence: "" };
  });
}

/** How many of the five the document mentions. */
export function mentionedCount(checks: readonly BriefCheck[]): number {
  return checks.filter((c) => c.mentioned).length;
}

/**
 * The rail's one-line summary. It never says a brief is ready, good or
 * complete: this cannot know that, and a writer told they are ready by a word
 * search has been misled by the product rather than helped by it.
 */
export function briefSummary(text: string, checks: readonly BriefCheck[]): string {
  if (wordCount(text) < MIN_WORDS_TO_CHECK) {
    return "Write a few lines and these fill in as you go.";
  }
  const missing = checks.length - mentionedCount(checks);
  if (missing === 0) return "All five topics appear somewhere in your brief.";
  if (missing === 1) return "One topic is not mentioned yet.";
  return `${String(missing)} topics are not mentioned yet.`;
}

function clamp(line: string): string {
  const trimmed = line.replace(/^#+\s*/, "").trim();
  if (trimmed.length <= MAX_EVIDENCE_CHARS) return trimmed;
  return `${trimmed.slice(0, MAX_EVIDENCE_CHARS)}…`;
}
