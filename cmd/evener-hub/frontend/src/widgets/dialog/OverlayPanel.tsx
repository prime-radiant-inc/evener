import {
  type HTMLAttributes,
  type KeyboardEvent,
  type MouseEvent,
  type ReactNode,
  type RefObject,
  useCallback,
  useId,
  useRef,
} from "react";
import { LayerFocusScope } from "../focusscope";
import { IconButton } from "../iconbutton";
import { requireClass } from "../internal/requireClass";
import { usePresence, useShownWhileClosing } from "../internal/usePresence";
import { CloseIcon } from "./CloseIcon";
import styles from "./dialog.module.css";

export interface OverlayPanelProps {
  open: boolean;
  onClose: () => void;
  title: string;
  /** Internal seam: Sheet actions and Close share a row with the title. */
  headerActions?: ReactNode;
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
  /** Internal seam, not part of the widget surface: the panel element, for a
   * gesture that reads or drives its transform directly. */
  panelRef?: RefObject<HTMLDivElement | null>;
  /** Internal seam: the body element, for a gesture that reads or releases
   * its scroll. */
  bodyRef?: RefObject<HTMLDivElement | null>;
  /** Internal seam: the scrim element. It is assigned alongside OverlayPanel's
   * own ref, which usePresence watches for the exit. */
  scrimRef?: RefObject<HTMLDivElement | null>;
  /** Internal seam: extra props for the header element, where a sheet's drag
   * gesture starts. */
  headerProps?: Pick<HTMLAttributes<HTMLElement>, "onPointerDown">;
}

const SCRIM = requireClass(styles.scrim, "dialog.module.css", "scrim");
const SCRIM_CLASS: Record<OverlayPanelProps["motion"], string> = {
  none: SCRIM,
  float: `${SCRIM} ${requireClass(styles.scrimFloat, "dialog.module.css", "scrimFloat")}`,
  slide: `${SCRIM} ${requireClass(styles.scrimEdge, "dialog.module.css", "scrimEdge")}`,
};

const CLASS = {
  header: requireClass(styles.header, "dialog.module.css", "header"),
  headerWithActions: requireClass(styles.headerWithActions, "dialog.module.css", "headerWithActions"),
  headerActions: requireClass(styles.headerActions, "dialog.module.css", "headerActions"),
  title: requireClass(styles.title, "dialog.module.css", "title"),
  body: requireClass(styles.body, "dialog.module.css", "body"),
  footer: requireClass(styles.footer, "dialog.module.css", "footer"),
  closeSeat: requireClass(styles.closeSeat, "dialog.module.css", "closeSeat"),
};

/**
 * Shared modal skeleton for Dialog and Sheet ("otherwise the Dialog
 * contract" - see the wave-2 plan): scrim, aria-modal dialog role labelled
 * by its title, Escape-to-close, click-the-scrim-to-close, a trapped
 * FocusScope, and one shared IconButton to close. Without header actions,
 * Close comes last in DOM order so initial focus can go to the content.
 * A sheet with header actions groups Close after those controls in the header,
 * in visual and keyboard order.
 */
export function OverlayPanel({
  open,
  onClose,
  title,
  headerActions,
  children,
  footer,
  panelClassName,
  handle,
  bodyClassName,
  headerClassName,
  motion,
  panelRef,
  bodyRef,
  scrimRef,
  headerProps,
}: OverlayPanelProps) {
  const titleId = useId();
  const internalScrimRef = useRef<HTMLDivElement>(null);
  // The scrim node has two readers: usePresence watches it for the exit, and
  // a caller's gesture code may write its opacity. Assigning both refs from
  // the DOM node keeps either one from going without.
  const setScrimNode = useCallback(
    (node: HTMLDivElement | null) => {
      internalScrimRef.current = node;
      if (scrimRef !== undefined) scrimRef.current = node;
    },
    [scrimRef],
  );
  const { mounted, phase } = usePresence(open, internalScrimRef, {
    exit: motion === "slide" ? "--motion-duration-spatial-exit" : "--motion-duration-exit",
  });
  // While the layer leaves it renders what it showed when it closed: a caller
  // that clears its own state on close must not see its dialog empty out
  // mid-exit.
  const shown = useShownWhileClosing(open, { title, headerActions, children, footer, handle });
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
  const hasHeaderActions = Boolean(shown.headerActions);
  const headerClass = [CLASS.header, headerClassName, hasHeaderActions && CLASS.headerWithActions]
    .filter(Boolean)
    .join(" ");
  const closeButton = <IconButton label="Close" icon={<CloseIcon />} variant="quiet" size="xs" onClick={onClose} />;

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
      ref={setScrimNode}
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
          tabIndex={-1}
          aria-modal={closing ? undefined : "true"}
          aria-labelledby={titleId}
          className={panelClassName}
          ref={panelRef}
          data-motion={phase}
          onKeyDown={handleKeyDown}
        >
          {shown.handle}
          <header className={headerClass} {...headerProps}>
            <h2 id={titleId} className={CLASS.title}>
              {shown.title}
            </h2>
            {hasHeaderActions && (
              <div className={CLASS.headerActions}>
                {shown.headerActions}
                {closeButton}
              </div>
            )}
          </header>
          <div ref={bodyRef} className={bodyClassName ? `${CLASS.body} ${bodyClassName}` : CLASS.body}>
            {shown.children}
          </div>
          {shown.footer !== undefined && <div className={CLASS.footer}>{shown.footer}</div>}
          {!hasHeaderActions && <span className={CLASS.closeSeat}>{closeButton}</span>}
        </div>
      </LayerFocusScope>
    </div>
  );
}
