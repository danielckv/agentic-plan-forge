import {
  defaultKeymap,
  history,
  historyKeymap,
  indentWithTab,
} from "@codemirror/commands";
import { keymap } from "@codemirror/view";
import type { Extension } from "@codemirror/state";

/**
 * Minimal base setup for the shared `prd` editor: the standard editing keys
 * plus history. Collab push/pull lives in the sync store; this module only
 * owns the local editing surface. History keeps undo local — the central
 * authority owns the shared text.
 */

const basicKeymap = [...defaultKeymap, ...historyKeymap, indentWithTab];

/** Complete local editor extension set (history + base keymap). */
export const editorBase: Extension[] = [history(), keymap.of(basicKeymap)];

