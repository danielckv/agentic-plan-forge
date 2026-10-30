import { useEffect, useId, useRef } from "react";
import { createPortal } from "react-dom";
import { X } from "lucide-react";

import { trapTab } from "./focusTrap";

/**
 * Modal dialog. Behavior the window relies on:
 *
 * - `Esc` closes it, and so does a click on the backdrop;
 * - Tab and Shift+Tab stay inside the panel (`trapTab`), and the rest of the
 *   app is `inert` while it is open;
 * - focus moves to the close button on open and returns to the element that
 *   had it (the control that opened the dialog) on close.
 *
 * It renders into `document.body` so the stacking context of the page that
 * opened it can never clip it.
 */

let openDialogs = 0;

/** True while any dialog is open; global shortcuts stand down. */
export function isDialogOpen(): boolean {
  return openDialogs > 0;
}

export interface DialogProps {
  open: boolean;
  onClose: () => void;
  title: string;
  /** Sub-heading under the title. */
  description?: string;
  /**
   * `md`/`doc`/`wide` centre the panel; `drawer` docks it to the right edge at
   * half the viewport, for a panel that stays open beside the workspace; `full`
   * fills the viewport, for a diagram that needs the room; `settings` is the
   * large centred panel the Setup view uses, tall enough for every section.
   */
  size?: "md" | "doc" | "wide" | "drawer" | "full" | "settings";
  children: React.ReactNode;
}

export function Dialog(props: DialogProps): React.ReactElement | null {
  if (!props.open) return null;
  return <DialogPanel {...props} />;
}

function DialogPanel({ onClose, title, description, size = "md", children }: DialogProps): React.ReactElement {
  const panelRef = useRef<HTMLDivElement>(null);
  const closeRef = useRef<HTMLButtonElement>(null);
  const onCloseRef = useRef(onClose);
  const titleId = useId();
  const descId = useId();

  useEffect(() => {
    onCloseRef.current = onClose;
  }, [onClose]);

  useEffect(() => {
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const appRoot = document.getElementById("root");
    openDialogs += 1;
    appRoot?.setAttribute("inert", "");
    closeRef.current?.focus();

    const onKeyDown = (e: KeyboardEvent): void => {
      const panel = panelRef.current;
      if (panel === null) return;
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        onCloseRef.current();
        return;
      }
      if (e.key === "Tab") trapTab(panel, e, document.activeElement);
    };
    document.addEventListener("keydown", onKeyDown, true);

    return () => {
      document.removeEventListener("keydown", onKeyDown, true);
      openDialogs -= 1;
      if (openDialogs === 0) appRoot?.removeAttribute("inert");
      // inert must be lifted first: an inert subtree cannot take focus back.
      if (previous !== null && document.contains(previous)) previous.focus();
    };
  }, []);

  return createPortal(
    <div
      className="pf-overlay"
      data-layout={size === "drawer" ? "drawer" : "center"}
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        ref={panelRef}
        className="pf-dialog"
        data-size={size}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={description === undefined ? undefined : descId}
        tabIndex={-1}
      >
        <header className="pf-dialog-head">
          <div className="min-w-0">
            <h2 id={titleId} className="pf-section-title truncate">
              {title}
            </h2>
            {description !== undefined && (
              <p id={descId} className="pf-caption mt-0.5">
                {description}
              </p>
            )}
          </div>
          <button
            ref={closeRef}
            type="button"
            className="pf-btn pf-btn-ghost pf-btn-sm pf-btn-icon"
            aria-label="Close dialog"
            onClick={onClose}
          >
            <X size={15} />
          </button>
        </header>
        <div className="pf-dialog-body pf-scroll-thin">{children}</div>
      </div>
    </div>,
    document.body,
  );
}
