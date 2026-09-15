import type { ReactNode } from "react";
import { requireClass } from "../internal/requireClass";
import { describeChild, FloatingBubblePortal, useFloatingBubble } from "./floatingBubble";
import styles from "./hovercard.module.css";

export interface HoverCardProps {
  label: ReactNode;
  children: ReactNode | ((association: HoverCardAssociation) => ReactNode);
}

export interface HoverCardAssociation {
  describedBy?: string;
}

const CLASS = {
  wrapper: requireClass(styles.wrapper, "hovercard.module.css", "wrapper"),
  bubble: requireClass(styles.bubble, "hovercard.module.css", "bubble"),
};

/** Rich, non-interactive description composed on the shared floating-label lifecycle. */
export function HoverCard({ label, children }: HoverCardProps) {
  const { visible, wrapperRef, triggerProps, setBubbleRef, bubbleStyle, bubbleID, describedBy } = useFloatingBubble();

  // A card's label is arbitrary content, so unlike the tooltip it also accepts a
  // function child: a caller rendering the trigger itself places the
  // association where it belongs instead of being cloned.
  const describedChild =
    typeof children === "function" ? children({ describedBy }) : describeChild(children, describedBy);

  return (
    <span ref={wrapperRef} className={CLASS.wrapper} {...triggerProps}>
      {describedChild}
      <FloatingBubblePortal
        as="div"
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
