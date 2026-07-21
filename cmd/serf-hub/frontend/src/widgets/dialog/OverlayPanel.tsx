import { type KeyboardEvent, type MouseEvent, type ReactNode, useId, useRef } from "react";
import { FocusScope } from "../focusscope";
import { requireClass } from "../internal/requireClass";
import { CloseIcon } from "./CloseIcon";
import styles from "./dialog.module.css";

export interface OverlayPanelProps {
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
  footer?: ReactNode;
  /** Caller-composed class for the panel element: a shared base class plus
   * whichever variant geometry/animation class applies (centered fade-scale
   * for Dialog, slide-in-from-an-edge for Sheet). OverlayPanel itself picks
   * the scrim, header, body, and footer classes - those never vary. */
  panelClassName: string;
}

const CLASS = {
  scrim: requireClass(styles.scrim, "dialog.module.css", "scrim"),
  header: requireClass(styles.header, "dialog.module.css", "header"),
  title: requireClass(styles.title, "dialog.module.css", "title"),
  body: requireClass(styles.body, "dialog.module.css", "body"),
  footer: requireClass(styles.footer, "dialog.module.css", "footer"),
  closeButton: requireClass(styles.closeButton, "dialog.module.css", "closeButton"),
};

/**
 * Shared modal skeleton for Dialog and Sheet ("otherwise the Dialog
 * contract" - see the wave-2 plan): scrim, aria-modal dialog role labelled
 * by its title, Escape-to-close, click-the-scrim-to-close, a trapped
 * FocusScope, and a close button. The close button is placed LAST in DOM
 * order (after body and footer) rather than in the header, so a dialog
 * with focusable body content focuses that content first on open - see
 * this task's report - while CSS pins it to the header's top-right corner
 * regardless of DOM position.
 */
export function OverlayPanel({ open, onClose, title, children, footer, panelClassName }: OverlayPanelProps) {
  const titleId = useId();
  // Radix-style pointer-down-outside semantics: the gesture that decides
  // whether this counts as "outside the panel" is where the PRESS
  // started, not where it ends. scrimPressStartedOnScrimRef records that
  // at mousedown; handleScrimClick only needs to confirm the click event
  // itself also targets the scrim, which - given a real browser computes
  // a cross-element mousedown/mouseup pair's click target as their
  // nearest common ancestor, and the scrim contains the entire panel -
  // it always does whenever the press started on the scrim, regardless of
  // where the release landed (verified live: press on the scrim, drag
  // into the panel, release there - the resulting click's target is still
  // the scrim). So a reverse drag (press on the scrim, release inside the
  // panel) closes it too, by the same mechanism as a plain scrim click,
  // not as a special case. What this guard actually rules out is the
  // FORWARD direction: pressing inside the panel (selecting text, say)
  // and dragging the release point out onto the scrim produces a click
  // whose target is also the scrim - indistinguishable from a genuine
  // backdrop click by target alone - but scrimPressStartedOnScrimRef is
  // false for it, since the press itself never touched the scrim.
  const scrimPressStartedOnScrimRef = useRef(false);

  if (!open) return null;

  function handleScrimMouseDown(event: MouseEvent<HTMLDivElement>) {
    scrimPressStartedOnScrimRef.current = event.target === event.currentTarget;
  }

  function handleScrimClick(event: MouseEvent<HTMLDivElement>) {
    // The event.target check is what actually reads "outside the panel"
    // (a click that bubbled up from a descendant has that descendant as
    // its target, not the scrim); scrimPressStartedOnScrimRef is what
    // rules out the forward-drag case above.
    if (event.target === event.currentTarget && scrimPressStartedOnScrimRef.current) onClose();
    scrimPressStartedOnScrimRef.current = false;
  }

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === "Escape") onClose();
  }

  return (
    <div className={CLASS.scrim} onMouseDown={handleScrimMouseDown} onClick={handleScrimClick}>
      <FocusScope trap>
        <div
          role="dialog"
          aria-modal="true"
          aria-labelledby={titleId}
          className={panelClassName}
          onKeyDown={handleKeyDown}
        >
          <header className={CLASS.header}>
            <h2 id={titleId} className={CLASS.title}>
              {title}
            </h2>
          </header>
          <div className={CLASS.body}>{children}</div>
          {footer !== undefined && <div className={CLASS.footer}>{footer}</div>}
          <button type="button" className={CLASS.closeButton} onClick={onClose} aria-label="Close">
            <CloseIcon />
          </button>
        </div>
      </FocusScope>
    </div>
  );
}
