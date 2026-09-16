import type { ReactNode } from "react";
import { describeChild, FloatingBubblePortal, useFloatingBubble } from "../hovercard/floatingBubble";
import { requireClass } from "../internal/requireClass";
import styles from "./tooltip.module.css";

export interface TooltipProps {
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
  const { visible, wrapperRef, triggerProps, setBubbleRef, bubbleStyle, bubbleID, describedBy } = useFloatingBubble();

  return (
    // Already both mouse- AND keyboard-triggered (onFocus/onBlur mirror
    // onMouseEnter/onMouseLeave exactly, per this file's own top comment) -
    // the WAI-ARIA-recommended tooltip pattern. This wrapper isn't becoming
    // a new interactive control needing a role; it's showing/hiding a
    // role="tooltip" description already wired to the real trigger element
    // via aria-describedby (describedChild below).
    <span ref={wrapperRef} className={CLASS.wrapper} {...triggerProps}>
      {describeChild(children, describedBy)}
      <FloatingBubblePortal
        as="span"
        show={visible}
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
