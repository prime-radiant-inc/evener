import { type CSSProperties, type KeyboardEvent, type MouseEvent, type ReactNode, useId, useRef } from "react";
import { LayerFocusScope } from "../focusscope";
import { requireClass } from "../internal/requireClass";
import { usePresence } from "../internal/usePresence";
import { CloseIcon } from "./CloseIcon";
import styles from "./dialog.module.css";

export interface OverlayPanelProps {
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
  footer?: ReactNode;
  /** Caller-composed class for the panel element: a shared base class plus
   * whichever variant geometry/motion class applies (the Float recipe for
   * Dialog, the Slide recipe for Sheet). OverlayPanel itself picks the
   * header, body, and footer classes - those never vary. */
  panelClassName: string;
  /** Which recipe the panel moves by, so the scrim fades on the same
   * durations and the exit is bounded by the matching token: "float" for a
   * centered dialog, "slide" for an edge-attached sheet, "none" for a panel
   * whose caller mounts and unmounts it outright (it neither arrives nor
   * leaves with motion, so its scrim does not fade either). */
  motion: "float" | "slide" | "none";
  /** Optional element rendered before the <header>, e.g. a drag handle for a
   * Sheet on a touch viewport. Defaults to undefined; existing consumers
   * that don't pass it are unchanged. */
  handle?: ReactNode;
  /** Optional extra class appended to the body div's base `.body` class. */
  bodyClassName?: string;
  /** Optional extra class appended to the header element's base `.header` class. */
  headerClassName?: string;
  /** Optional inline style applied to the panel element. Defaults to
   * undefined; existing consumers that don't pass it are unchanged. Used by
   * the expandable Sheet to drive a dynamic peek/full height on the panel
   * itself (not a child wrapper) so a `.bottom` max-height cap can't clamp
   * the full-screen geometry. */
  style?: CSSProperties;
}

const SCRIM = requireClass(styles.scrim, "dialog.module.css", "scrim");
const SCRIM_CLASS: Record<OverlayPanelProps["motion"], string> = {
  none: SCRIM,
  float: `${SCRIM} ${requireClass(styles.scrimFloat, "dialog.module.css", "scrimFloat")}`,
  slide: `${SCRIM} ${requireClass(styles.scrimEdge, "dialog.module.css", "scrimEdge")}`,
};

const CLASS = {
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
export function OverlayPanel({
  open,
  onClose,
  title,
  children,
  footer,
  panelClassName,
  handle,
  bodyClassName,
  headerClassName,
  style,
  motion,
}: OverlayPanelProps) {
  const titleId = useId();
  const scrimRef = useRef<HTMLDivElement>(null);
  const { mounted, phase } = usePresence(open, scrimRef, {
    exit: motion === "slide" ? "--motion-duration-spatial-exit" : "--motion-duration-exit",
  });
  // While the layer leaves it renders what it showed when it closed: a caller
  // that clears its own state on close must not see its dialog empty out
  // mid-exit.
  const shownRef = useRef({ title, children, footer, handle });
  if (open) shownRef.current = { title, children, footer, handle };
  const shown = shownRef.current;
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

  if (!mounted) return null;
  const closing = phase === "closing";

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
    // preventDefault (not stopPropagation - see Menu's handleMenuKeyDown for
    // that stronger precedent, which this deliberately doesn't copy: an
    // ancestor like Settings still needs to SEE this Escape bubble past it,
    // just knowing not to act on it too - it checks event.defaultPrevented
    // rather than never receiving the event at all) marks this Escape as
    // already handled, so an ancestor overlay/pane doesn't also close.
    if (event.key === "Escape" && !event.defaultPrevented) {
      event.preventDefault();
      onClose();
    }
  }

  return (
    // The scrim's click-to-dismiss is a mouse/touch-only convenience, not a
    // keyboard target: WAI-ARIA's Dialog (Modal) pattern specifies Escape
    // (handleKeyDown below, on the panel) as the keyboard dismissal path,
    // never "Tab to the backdrop, then Enter" - that isn't how any modal
    // convention works. Both close the same way (onClose), so keyboard
    // users already have full, more-discoverable equivalence via Escape.
    //
    // A closing layer is inert and drops its dialog semantics at once: focus
    // has gone back to the trigger, the pointer reaches what it uncovers, and
    // nothing that asks whether a modal or dialog is open (an [aria-modal] or
    // [role="dialog"] query) still finds one.
    // biome-ignore lint/a11y/noStaticElementInteractions: mouse-only scrim dismiss; Escape is the keyboard path, see above
    // biome-ignore lint/a11y/useKeyWithClickEvents: mouse-only scrim dismiss; Escape is the keyboard path, see above
    <div
      ref={scrimRef}
      className={SCRIM_CLASS[motion]}
      data-motion={phase}
      inert={closing}
      onMouseDown={handleScrimMouseDown}
      onClick={handleScrimClick}
    >
      <LayerFocusScope trap released={closing}>
        {/* biome-ignore lint/a11y/useAriaPropsSupportedByRole lint/a11y/noStaticElementInteractions: the dialog role and aria-modal are dropped together only while the layer leaves, inert, see above */}
        <div
          role={closing ? undefined : "dialog"}
          aria-modal={closing ? undefined : "true"}
          aria-labelledby={titleId}
          className={panelClassName}
          data-motion={phase}
          onKeyDown={handleKeyDown}
          style={style}
        >
          {shown.handle}
          <header className={headerClassName ? `${CLASS.header} ${headerClassName}` : CLASS.header}>
            <h2 id={titleId} className={CLASS.title}>
              {shown.title}
            </h2>
          </header>
          <div className={bodyClassName ? `${CLASS.body} ${bodyClassName}` : CLASS.body}>{shown.children}</div>
          {shown.footer !== undefined && <div className={CLASS.footer}>{shown.footer}</div>}
          <button type="button" className={CLASS.closeButton} onClick={onClose} aria-label="Close">
            <CloseIcon />
          </button>
        </div>
      </LayerFocusScope>
    </div>
  );
}
