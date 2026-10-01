import { type ReactNode, useEffect, useRef } from "react";
import { requireClass } from "../internal/requireClass";
import { describeChild, FloatingBubblePortal, useFloatingBubble } from "./floatingBubble";
import styles from "./hovercard.module.css";

export interface HoverCardProps {
  label: ReactNode;
  children: ReactNode | ((association: HoverCardAssociation) => ReactNode);
  focusTarget?: () => HTMLElement | null;
  tapEnabled?: boolean;
}

export interface HoverCardAssociation {
  describedBy?: string;
}

const CLASS = {
  wrapper: requireClass(styles.wrapper, "hovercard.module.css", "wrapper"),
  bubble: requireClass(styles.bubble, "hovercard.module.css", "bubble"),
};

/** Rich, non-interactive description composed on the shared floating-label lifecycle. */
export function HoverCard({ label, children, focusTarget, tapEnabled }: HoverCardProps) {
  const tapOpenRef = useRef(false);
  const {
    visible,
    wrapperRef,
    triggerProps,
    setBubbleRef,
    bubbleStyle,
    bubbleID,
    describedBy,
    showImmediately,
    dismiss,
  } = useFloatingBubble(focusTarget);

  useEffect(() => {
    if (!tapEnabled || !visible || !window.matchMedia?.("(hover: none)").matches) return;
    const handleOutsidePointer = (event: PointerEvent) => {
      if (wrapperRef.current?.contains(event.target as Node | null)) return;
      tapOpenRef.current = false;
      dismiss();
    };
    document.addEventListener("pointerdown", handleOutsidePointer, true);
    return () => document.removeEventListener("pointerdown", handleOutsidePointer, true);
  }, [dismiss, tapEnabled, visible, wrapperRef]);

  useEffect(() => {
    if (!visible) tapOpenRef.current = false;
  }, [visible]);

  // A card's label is arbitrary content, so unlike the tooltip it also accepts a
  // function child: a caller rendering the trigger itself places the
  // association where it belongs instead of being cloned.
  const describedChild =
    typeof children === "function" ? children({ describedBy }) : describeChild(children, describedBy);

  return (
    // tapEnabled delegates a hoverless tap from the semantic child; keyboard
    // users use that child's focus or the explicit external focus target.
    // biome-ignore lint/a11y/noStaticElementInteractions: interaction semantics belong to the child trigger
    // biome-ignore lint/a11y/useKeyWithClickEvents: focus already exposes the same non-interactive description
    <span
      ref={wrapperRef}
      className={CLASS.wrapper}
      {...triggerProps}
      onClick={(event) => {
        if (!tapEnabled || !window.matchMedia?.("(hover: none)").matches) return;
        if (tapOpenRef.current) {
          tapOpenRef.current = false;
          dismiss();
          return;
        }
        event.stopPropagation();
        tapOpenRef.current = true;
        showImmediately();
      }}
    >
      {describedChild}
      <FloatingBubblePortal
        as="div"
        show={visible}
        id={bubbleID}
        className={CLASS.bubble}
        style={bubbleStyle}
        setRef={setBubbleRef}
        tapEnabled={tapEnabled}
      >
        {label}
      </FloatingBubblePortal>
    </span>
  );
}
