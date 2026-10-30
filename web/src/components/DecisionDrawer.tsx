import { useState } from "react";
import { CircleHelp } from "lucide-react";

import { parseNormalizedPRD, type Ambiguity } from "../protocol/drafts";
import { closeDecisionDrawer, resolveAmbiguity, resolveDecision, useSync } from "../store/syncStore";
import type { DecisionCard } from "../store/syncStore";
import { Dialog } from "../ui/Dialog";
import {
  AMBIGUITY_CHOICES,
  MAX_AMBIGUITY_NOTE,
  ambiguityResolveRefusal,
  decisionResolveRefusal,
  isRecommended,
  pendingAmbiguities,
  pendingDecisions,
} from "../ui/decisionDrawer";
import type { DrawerGate } from "../ui/decisionDrawer";

/**
 * The question drawer: everything the fused candidate could not decide alone.
 *
 * It opens only once a candidate is fused, and it is the one place a human
 * answers. Two kinds of question live here, and they are answered differently:
 *
 * - a decision card offers the model's options and one recommendation; the
 *   answer is a `DECISION_RESOLVE` naming an option;
 * - a brief question is a classification with no options; the answer is one of
 *   the three readings in `AMBIGUITY_CHOICES`.
 *
 * The drawer reads and writes the store directly, so the same list is reachable
 * from every page. Closing it answers nothing: the unresolved-count action
 * reopens it. Every button is gated on `ui/decisionDrawer`'s refusal rules, so
 * an answer this window may not send says why instead of failing at the session.
 */
export function DecisionDrawer(): React.ReactElement | null {
  const sync = useSync();
  const ingestion = sync.drafts.find((d) => d.avatarId === "ingestion");
  const decisions = pendingDecisions(sync.decisions);
  const questions = pendingAmbiguities(parseNormalizedPRD(ingestion?.data), sync.ambiguityAnswers);
  const total = decisions.length + questions.length;
  const gate: DrawerGate = {
    role: sync.role,
    conn: sync.conn,
    phase: sync.phase,
    decisionAnswers: sync.decisionAnswers,
    ambiguityAnswers: sync.ambiguityAnswers,
  };

  return (
    <Dialog
      open={sync.decisionDrawerOpen}
      onClose={closeDecisionDrawer}
      size="drawer"
      title="Questions for you"
      description={
        total === 0
          ? "Nothing waits on an answer"
          : `${String(total)} open question${total === 1 ? "" : "s"}`
      }
    >
      {total === 0 ? (
        <p className="pf-caption pf-drawer-empty">
          Every question this run raised has an answer. The workspace continues on its own.
        </p>
      ) : (
        <div className="pf-drawer">
          {decisions.length > 0 && (
            <section className="pf-drawer-section" aria-label="Decisions">
              <h3 className="pf-section-title">
                <CircleHelp size={14} aria-hidden="true" /> Decisions
              </h3>
              <p className="pf-caption">
                Each one changes what the plan builds. The recommended option is marked; answering
                replaces nothing else in the candidate.
              </p>
              <div className="pf-drawer-list">
                {decisions.map((card) => (
                  <DecisionBlock key={card.decisionId} card={card} gate={gate} />
                ))}
              </div>
            </section>
          )}
          {questions.length > 0 && (
            <section className="pf-drawer-section" aria-label="Brief questions">
              <h3 className="pf-section-title">Reading the brief</h3>
              <p className="pf-caption">
                These are classification questions, not decisions: each one says how a sentence
                should be read.
              </p>
              <ul className="pf-drawer-list">
                {questions.map((question) => (
                  <QuestionBlock key={question.id} question={question} gate={gate} />
                ))}
              </ul>
            </section>
          )}
        </div>
      )}
    </Dialog>
  );
}

/** One decision card: the question, its options, and the recommendation. */
function DecisionBlock(props: { card: DecisionCard; gate: DrawerGate }): React.ReactElement {
  const { card, gate } = props;
  const first = card.options[0]?.optionId ?? "";
  const refusal = decisionResolveRefusal(card, first, gate);
  const attempt = gate.decisionAnswers[card.decisionId];
  const sending = attempt?.pending === true;

  return (
    <article className="pf-card pf-card-pad pf-decision" data-state={card.stale ? "stale" : "open"}>
      <p className="pf-section-title">{card.question}</p>
      <div className="pf-drawer-options">
        {card.options.map((option) => {
          const recommended = isRecommended(card, option.optionId);
          // Only an in-flight answer marks its option: a rejected one must not
          // read as settled while the card still waits.
          const chosen = sending && attempt?.optionId === option.optionId;
          return (
            <button
              key={option.optionId}
              type="button"
              className="pf-option"
              disabled={refusal !== null}
              data-recommended={recommended}
              data-chosen={chosen}
              onClick={() => resolveDecision(card.decisionId, option.optionId)}
            >
              <span className="pf-drawer-option-head">
                {option.label}
                {recommended && <span className="pf-badge pf-badge--recommended">Recommended</span>}
              </span>
              <span className="pf-caption">{option.tradeoff}</span>
              {option.impact !== undefined && option.impact !== "" && (
                <span className="pf-caption pf-drawer-impact">Impact: {option.impact}</span>
              )}
            </button>
          );
        })}
      </div>
      {sending && (
        <p className="pf-caption" role="status">
          Sending your answer to the session…
        </p>
      )}
      {!sending && attempt?.error !== undefined && (
        <p className="pf-chip pf-chip--warn" role="alert">
          {attempt.error}
        </p>
      )}
      {refusal !== null && !sending && <p className="pf-caption">{refusal}</p>}
    </article>
  );
}

/** One brief question: three readings, and an optional note. */
function QuestionBlock(props: { question: Ambiguity; gate: DrawerGate }): React.ReactElement {
  const { question, gate } = props;
  const [note, setNote] = useState("");
  const refusal = ambiguityResolveRefusal(gate, question.id, note);

  return (
    <li className="pf-question">
      <p className="pf-question-text">{question.question}</p>
      {question.text !== "" && <blockquote className="pf-question-excerpt">{question.text}</blockquote>}
      {question.guess !== "" && (
        <p className="pf-caption">
          The reader guessed: <em>{question.guess}</em>. Answering replaces the guess.
        </p>
      )}
      <fieldset className="pf-question-choices" disabled={refusal !== null}>
        <legend className="pf-caption">
          {refusal ?? "This is a classification question: it says how a sentence should be read."}
        </legend>
        {AMBIGUITY_CHOICES.map((choice) => (
          <button
            key={choice.id}
            type="button"
            className="pf-btn pf-btn-secondary pf-btn-sm"
            onClick={() => resolveAmbiguity(question.id, choice.id, note)}
          >
            <span className="pf-question-choice-label">{choice.label}</span>
            <span className="pf-caption">{choice.explain}</span>
          </button>
        ))}
      </fieldset>
      <label className="pf-caption pf-drawer-note">
        Note (optional)
        <input
          type="text"
          className="pf-input"
          maxLength={MAX_AMBIGUITY_NOTE}
          value={note}
          onChange={(e) => setNote(e.target.value)}
        />
      </label>
    </li>
  );
}
