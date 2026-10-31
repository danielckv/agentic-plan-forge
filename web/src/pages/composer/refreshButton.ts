import type { RefreshState } from "../../store/syncStore";

/**
 * The composer's Refresh button and the note beside it.
 *
 * The executor re-reads the brief by itself a few times while it is written.
 * That path is convenient but it is not a promise: it stops after four reads,
 * and a writer who has just replaced half a page wants the plan rail to follow
 * now, not at the next threshold. This button does that, and it switches the
 * automatic path off so the two never re-read the same document at once.
 */

/** What the button says, and whether it can be pressed. */
interface RefreshLabel {
  text: string;
  hint: string;
  disabled: boolean;
}

/**
 * refreshLabel words the button for the current state.
 *
 * `off` is true once the owner has pressed it: the button still re-reads, and
 * the note says the automatic path is off. That is deliberate — a writer who
 * changed their mind must not be stuck with a stale rail.
 */
export function refreshLabel(state: RefreshState | undefined, hasText: boolean): RefreshLabel {
  if (!hasText) {
    return { text: "Refresh", hint: "Write the requirements first.", disabled: true };
  }
  if (state?.automaticOff === true) {
    return {
      text: "Refresh plan",
      hint: "Automatic refresh is off. This re-reads the document now.",
      disabled: false,
    };
  }
  return {
    text: "Refresh plan",
    hint: "Read the document now. This switches automatic refresh off.",
    disabled: false,
  };
}

/**
 * refreshNote explains the automatic path in the writer's terms: what it will
 * do, or why it has stopped. It returns null when the bridge is too old to say,
 * rather than inventing a promise the executor does not keep.
 */
export function refreshNote(state: RefreshState | undefined): string | null {
  if (state === undefined) return null;
  if (state.automaticOff) {
    return "Automatic refresh is off for this session. Use Refresh plan after a change.";
  }
  if (state.refreshes >= state.maxRefreshes) {
    return `Automatic refresh is done: ${String(state.maxRefreshes)} reads. Use Refresh plan after a change.`;
  }
  if (state.refreshes === 0) {
    return "The plan refreshes itself once the brief is past 100 words.";
  }
  const more = state.wordsRemaining === 1 ? "word" : "words";
  return `The plan refreshes itself every ${String(state.wordsRemaining)} more ${more} until ${String(
    state.maxRefreshes,
  )} reads.`;
}
