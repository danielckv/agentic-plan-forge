/**
 * Question-drawer logic: which questions still wait for a human, which option
 * the model recommends, whether this window may answer at all, and the exact
 * payload each answer travels in.
 *
 * Two kinds of question reach the drawer, and they are not interchangeable:
 *
 * - a `DecisionCard` is a material product decision. It carries the model's
 *   options (label, tradeoff, impact) and one recommendation, and its answer is
 *   a `DECISION_RESOLVE` naming one of them. The authority only accepts the
 *   answer while the card's `input_revision` is still the current one.
 * - an `Ambiguity` is a classification question about a sentence in the brief.
 *   It carries no options and no recommendation: the answer is one of the three
 *   readings in `AMBIGUITY_CHOICES`, sent as `AMBIGUITY_RESOLVE`.
 *
 * Nothing here talks to the network or holds state. `decisionAnswer` and
 * `ambiguityAnswer` are the single source of truth for the wire shape: the
 * store sends exactly what they return, so a change here changes the command.
 * The refusal views (`*ResolveRefusal`) exist for the disabled button and its
 * caption; they mirror the reducer's gates (role, phase, revision, in-flight)
 * so an impossible answer explains itself instead of becoming an ERROR toast.
 */

import type { Ambiguity } from "../protocol/drafts";
import type {
  AmbiguityAnswer,
  AmbiguityChoice,
  ConnState,
  DecisionAnswer,
  DecisionCard,
  Phase,
} from "../store/syncStore";

/**
 * The longest note a classification answer carries. It lives here because the
 * refusal check is here; the store re-exports it for the note input's bound.
 */
export const MAX_AMBIGUITY_NOTE = 500;

/** The three readings a classification question accepts (wire values). */
export const AMBIGUITY_CHOICES: { id: AmbiguityChoice; label: string; explain: string }[] = [
  { id: "product", label: "It changes what we build", explain: "The requirement itself needs a decision." },
  { id: "technical", label: "It changes how we build it", explain: "The design absorbs it without changing the scope." },
  { id: "dismiss", label: "Not a real ambiguity", explain: "The text is clear enough to plan from." },
];

/** `DECISION_RESOLVE` payload. `input_revision` is a decimal string on the wire. */
type DecisionResolvePayload = {
  decision_id: string;
  /** The card's opening revision; the authority rejects a stale one. */
  input_revision: string;
  option_id: string;
};

/** `AMBIGUITY_RESOLVE` payload; `note` is absent when the answer carries none. */
type AmbiguityResolvePayload = {
  ambiguity_id: string;
  choice: AmbiguityChoice;
  note?: string;
};

/** Either the command to send, or the reason this window may not send it. */
type AnswerVerdict<P> = { ok: true; payload: P } | { ok: false; reason: string };

/** The session facts a refusal is decided from. */
export interface DrawerGate {
  role: "owner" | "editor" | "viewer" | null;
  conn: ConnState;
  phase: Phase;
  /** Answers this window already sent, keyed by decision id. */
  decisionAnswers: Record<string, DecisionAnswer>;
  /** Answers this window already sent, keyed by ambiguity id. */
  ambiguityAnswers: Record<string, AmbiguityAnswer>;
}

/** The roles the reducer lets answer a decision card. */
const DECISION_ROLES: readonly string[] = ["owner", "editor"];

/** The phases where `DECISION_RESOLVE` is open. */
const DECISION_PHASES: readonly Phase[] = ["DELIBERATION", "SYNTHESIS"];

/** Decisions the drawer shows: still open, and not superseded by another member. */
export function pendingDecisions(cards: readonly DecisionCard[]): DecisionCard[] {
  return cards.filter((card) => card.resolvedOptionId === undefined && !card.stale);
}

/** Classification questions this window has not answered yet. */
export function pendingAmbiguities(
  questions: readonly Ambiguity[],
  answers: Readonly<Record<string, AmbiguityAnswer>>,
): Ambiguity[] {
  return questions.filter((question) => answers[question.id] === undefined);
}

/**
 * What the unresolved-count action shows: every question the drawer would list,
 * counted whether or not the drawer is open.
 */
export function pendingQuestionCount(
  cards: readonly DecisionCard[],
  questions: readonly Ambiguity[],
  answers: Readonly<Record<string, AmbiguityAnswer>>,
): number {
  return pendingDecisions(cards).length + pendingAmbiguities(questions, answers).length;
}

/**
 * The option the model recommends, or null when it named none or named one the
 * card does not offer (a recommendation is a hint, never an answer).
 */
export function recommendedOptionId(card: DecisionCard): string | null {
  const id = card.recommendedOptionId;
  if (id === undefined) return null;
  return card.options.some((option) => option.optionId === id) ? id : null;
}

export function isRecommended(card: DecisionCard, optionId: string): boolean {
  return recommendedOptionId(card) === optionId;
}

/**
 * The `DECISION_RESOLVE` command for one option, or the reason it cannot be
 * sent now. The revision travels as a decimal string because the authority
 * reads `input_revision` as a string field; sending it as a number is rejected.
 */
export function decisionAnswer(
  card: DecisionCard,
  optionId: string,
  gate: DrawerGate,
): AnswerVerdict<DecisionResolvePayload> {
  const refuse = (reason: string): AnswerVerdict<DecisionResolvePayload> => ({ ok: false, reason });
  if (optionId === "") return refuse("Choose one of the options first.");
  if (!card.options.some((option) => option.optionId === optionId)) {
    return refuse("That option is not on this decision; refresh before answering.");
  }
  const revision = card.inputRevision;
  if (revision === undefined) {
    return refuse("This decision arrived without a revision; refresh before answering.");
  }
  if (!DECISION_ROLES.includes(gate.role ?? "")) {
    return refuse("Only the session owner or an editor can answer this decision.");
  }
  if (gate.conn !== "open") return refuse("Answering needs a live connection to the session.");
  if (!DECISION_PHASES.includes(gate.phase)) {
    return refuse(`This decision is closed in phase ${gate.phase}.`);
  }
  if (card.resolvedOptionId !== undefined) return refuse("Another member already resolved this decision.");
  if (card.stale) return refuse("This decision changed since the window loaded; refresh before answering.");
  if (gate.decisionAnswers[card.decisionId]?.pending === true) {
    return refuse("This window is already sending an answer for this decision.");
  }
  return {
    ok: true,
    payload: { decision_id: card.decisionId, input_revision: String(revision), option_id: optionId },
  };
}

/** Why the drawer disables this decision's options, or null when they are live. */
export function decisionResolveRefusal(
  card: DecisionCard,
  optionId: string,
  gate: DrawerGate,
): string | null {
  const verdict = decisionAnswer(card, optionId, gate);
  return verdict.ok ? null : verdict.reason;
}

/**
 * The `AMBIGUITY_RESOLVE` command for one classification answer, or the reason
 * it cannot be sent now. Only the owner answers, and only while the brief is
 * still being read.
 */
export function ambiguityAnswer(
  gate: DrawerGate,
  id: string,
  choice: AmbiguityChoice,
  note: string,
): AnswerVerdict<AmbiguityResolvePayload> {
  const reason = ambiguityResolveRefusal(gate, id, note);
  if (reason !== null) return { ok: false, reason };
  return { ok: true, payload: note.trim() === "" ? { ambiguity_id: id, choice } : { ambiguity_id: id, choice, note } };
}

/** Why the drawer disables this question's answers, or null when they are live. */
export function ambiguityResolveRefusal(gate: DrawerGate, id: string, note: string): string | null {
  if (id.trim() === "") return "A question needs its id before it can be answered.";
  if (note.length > MAX_AMBIGUITY_NOTE) return `Keep a note under ${String(MAX_AMBIGUITY_NOTE)} characters.`;
  if (gate.role !== "owner") return "Only the session owner can answer the questions.";
  if (gate.conn !== "open") return "Answering needs a live connection to the session.";
  if (gate.phase !== "INGESTION") return "These questions were already answered; the plan has moved on.";
  if (gate.ambiguityAnswers[id] !== undefined) return "This question already has an answer from this window.";
  return null;
}
