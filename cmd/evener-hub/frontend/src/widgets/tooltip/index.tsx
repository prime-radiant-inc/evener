import { type ReactNode, useLayoutEffect } from "react";
import { describeChild, FloatingBubblePortal, useFloatingBubble } from "../hovercard/floatingBubble";
import { requireClass } from "../internal/requireClass";
import styles from "./tooltip.module.css";

export interface TooltipProps {
  /** An empty label is an inactive tooltip: no bubble, no description, just
   * the wrapper, so a caller can keep its trigger's element tree stable while
   * the description comes and goes (IconButton's disabledReason). */
  label: string;
  children: ReactNode;
}

const CLASS = {
  wrapper: requireClass(styles.wrapper, "tooltip.module.css", "wrapper"),
  bubble: requireClass(styles.bubble, "tooltip.module.css", "bubble"),
};

/**
 * Hover- and focus-triggered label, shown after a 300ms delay and hidden
 * immediately on mouseleave/blur. Never traps focus - it has no focusable
 * content of its own, just a role="tooltip" bubble - and is hidden on
 * touch (no hover capability) via CSS, since a tap has no mouseleave to
 * dismiss it with.
 *
 * The show/hide trigger (mouseenter/mouseleave/focus/blur) is wired on a
 * wrapper <span> so it works for any children via event bubbling,
 * regardless of whether the trigger element itself forwards extra props.
 * The aria-describedby association, by contrast, has to land on the
 * trigger element itself to be announced correctly - see describeChild.
 *
 * Placement, the portal out of clipping ancestors, and the pre-measure
 * placeholder come from the shared floatingBubble scaffold (floatingBubble.tsx
 * documents why a portaled, viewport-positioned bubble is required).
 */
export function Tooltip({ label, children }: TooltipProps) {
  const { visible, wrapperRef, triggerProps, setBubbleRef, bubbleStyle, bubbleID, describedBy, dismiss } =
    useFloatingBubble();
  // An inactive tooltip is inert: it takes no hover/focus handlers that start a
  // reveal, so it never holds a "visible" state the reveal's measurement did
  // not run for, and one that goes inactive while shown dismisses. It keeps the
  // leave/blur handlers, which only clear the lifecycle's hovered/focused flags,
  // so a leave while inactive is still recorded when it reactivates.
  const active = label !== "";
  useLayoutEffect(() => {
    if (!active) dismiss();
  }, [active, dismiss]);

  return (
    // Already both mouse- AND keyboard-triggered (onFocus/onBlur mirror
    // onMouseEnter/onMouseLeave exactly, per this file's own top comment) -
    // the WAI-ARIA-recommended tooltip pattern. This wrapper isn't becoming
    // a new interactive control needing a role; it's showing/hiding a
    // role="tooltip" description already wired to the real trigger element
    // via aria-describedby (describedChild below).
    <span
      ref={wrapperRef}
      className={CLASS.wrapper}
      {...(active ? triggerProps : { onMouseLeave: triggerProps.onMouseLeave, onBlur: triggerProps.onBlur })}
    >
      {describeChild(children, active ? describedBy : undefined)}
      <FloatingBubblePortal
        as="span"
        show={active && visible}
        id={bubbleID}
        className={CLASS.bubble}
        style={bubbleStyle}
        setRef={setBubbleRef}
      >
        {label}
      </FloatingBubblePortal>
    </span>
  );
}
