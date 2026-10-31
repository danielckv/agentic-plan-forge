import { useEffect, useRef } from "react";

import { isDialogOpen } from "./Dialog";
import { FOCUS_FILTER_EVENT, shortcutFor } from "./shortcuts";

interface ShortcutHandlers {
  toggleSidebar: () => void;
  openHelp: () => void;
}

/**
 * One document-level key listener for the window-wide shortcuts. It stands
 * down while a dialog is open (the dialog owns the keyboard then) and for
 * keys already handled elsewhere.
 */
export function useGlobalShortcuts(handlers: ShortcutHandlers): void {
  const ref = useRef(handlers);
  useEffect(() => {
    ref.current = handlers;
  }, [handlers]);

  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent): void => {
      if (e.defaultPrevented || e.repeat || isDialogOpen()) return;
      const action = shortcutFor(e);
      if (action === null) return;
      if (action === "toggle-sidebar") {
        e.preventDefault();
        ref.current.toggleSidebar();
      } else if (action === "help") {
        e.preventDefault();
        ref.current.openHelp();
      } else {
        // A page that owns a filter cancels the event after focusing it;
        // only then is the "/" keystroke swallowed instead of typed.
        const unhandled = window.dispatchEvent(new CustomEvent(FOCUS_FILTER_EVENT, { cancelable: true }));
        if (!unhandled) e.preventDefault();
      }
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, []);
}
