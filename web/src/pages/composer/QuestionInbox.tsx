import { CircleHelp } from "lucide-react";

import { parseNormalizedPRD, type Ambiguity } from "../../protocol/drafts";
import { resolveAmbiguity, useSync, type AmbiguityAnswer } from "../../store/syncStore";

/** What each answer means, in words a product manager can act on. */
const CHOICES: { id: "product" | "technical" | "dismiss"; label: string; explain: string }[] = [
  { id: "product", label: "It changes what we build", explain: "The requirement itself needs a decision." },
  { id: "technical", label: "It changes how we build it", explain: "The design absorbs it without changing the scope." },
  { id: "dismiss", label: "Not a real ambiguity", explain: "The text is clear enough to plan from." },
];

/**
 * The questions the normalizer raised about the brief.
 *
 * They are classification questions, not product decisions: each one says which
 * way a genuinely unclear sentence should be read. The component reads the
 * ingestion draft and the store itself, so mounting it is one line and an
 * observer gets a read-only view of the same list.
 */
export function QuestionInbox(): React.ReactElement {
  const sync = useSync();
  const draft = sync.drafts.find((d) => d.avatarId === "ingestion");
  const questions = parseNormalizedPRD(draft?.data);
  const answers = sync.ambiguityAnswers;
  const open = questions.filter((q) => answers[q.id] === undefined);
  const isOwner = sync.role === "owner";

  return (
    <section className="pf-questions" aria-labelledby="pf-questions-title">
      <header className="pf-questions-head">
        <CircleHelp size={15} aria-hidden="true" />
        <h3 className="pf-section-title" id="pf-questions-title">
          Questions about the brief
        </h3>
        <span className="pf-caption">{open.length === 0 ? "all answered" : `${String(open.length)} open`}</span>
      </header>

      {questions.length === 0 ? (
        <p className="pf-body">No questions right now. Anything unclear in the brief will appear here.</p>
      ) : (
        <ul className="pf-question-list">
          {questions.map((q) => (
            <QuestionCard
              key={q.id}
              question={q}
              isOwner={isOwner}
              connected={sync.conn === "open"}
              phase={sync.phase}
              answer={answers[q.id]}
            />
          ))}
        </ul>
      )}
    </section>
  );
}

function QuestionCard(props: {
  question: Ambiguity;
  isOwner: boolean;
  connected: boolean;
  phase: string;
  answer: AmbiguityAnswer | undefined;
}): React.ReactElement {
  const { question, isOwner, connected, phase, answer } = props;
  const settled = answer !== undefined && !answer.pending;
  const canAnswer = isOwner && connected && phase === "INGESTION" && answer === undefined;
  const disabledReason = !isOwner
    ? "Only the session owner can answer."
    : !connected
      ? "Waiting for the connection."
      : phase !== "INGESTION"
        ? "The plan has moved past the brief."
        : undefined;

  return (
    <li className={`pf-question ${settled ? "pf-question--answered" : ""}`}>
      <p className="pf-question-text">{question.question}</p>
      {question.text !== "" && <blockquote className="pf-question-excerpt">{question.text}</blockquote>}
      {question.guess !== "" && (
        <p className="pf-caption">
          The reader guessed: <em>{question.guess}</em>. Answering replaces the guess.
        </p>
      )}

      {settled ? (
        <p className="pf-question-settled" role="status">
          Answered: {CHOICES.find((c) => c.id === answer.choice)?.label ?? answer.choice}
          {answer.superseded ? " (another member answered first)" : ""}
        </p>
      ) : (
        <fieldset className="pf-question-choices" disabled={!canAnswer}>
          <legend className="pf-caption">
            {disabledReason ?? "This is a classification question: it says how a sentence should be read, not what to build."}
          </legend>
          {CHOICES.map((choice, index) => (
            <button
              key={choice.id}
              type="button"
              className="pf-btn pf-btn-secondary pf-btn-sm"
              autoFocus={index === 0 && canAnswer}
              onClick={() => resolveAmbiguity(question.id, choice.id)}
            >
              <span className="pf-question-choice-label">{choice.label}</span>
              <span className="pf-caption">{choice.explain}</span>
            </button>
          ))}
        </fieldset>
      )}

      {answer?.pending === true && <p className="pf-caption">Sent. Waiting for the session to confirm it.</p>}
      {settled && answer.note !== undefined && answer.note !== "" && (
        <p className="pf-caption">Note: {answer.note}</p>
      )}
    </li>
  );
}
