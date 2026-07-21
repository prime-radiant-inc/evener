import { useId, useRef, type KeyboardEvent, type MouseEvent, type ReactNode } from "react";
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
  // A click event's target reflects where the mouse was RELEASED, not
  // where the press started - selecting text inside the panel and
  // dragging the release point out onto the scrim produces a click whose
  // target is the scrim, indistinguishable by target alone from a genuine
  // backdrop click. Tracking where the press itself landed and requiring
  // both ends of the gesture to be the scrim closes that gap.
  const scrimPressStartedOnScrimRef = useRef(false);

  if (!open) return null;

  function handleScrimMouseDown(event: MouseEvent<HTMLDivElement>) {
    scrimPressStartedOnScrimRef.current = event.target === event.currentTarget;
  }

  function handleScrimClick(event: MouseEvent<HTMLDivElement>) {
    // Only a press-and-release that both land directly on the scrim
    // itself count as "outside the panel" - a click inside the panel
    // bubbles up through this same handler, but its event.target is the
    // descendant that was actually clicked, not the scrim.
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
