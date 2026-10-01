import { type ReactNode, useCallback, useEffect, useRef } from "react";
import { requireClass } from "../internal/requireClass";
import { describeChild, FloatingBubblePortal, useFloatingBubble } from "./floatingBubble";
import styles from "./hovercard.module.css";
import { isHoverless } from "./useFloatingLabel";

/** How long a finger must rest on a long-press-enabled trigger on a hoverless device
 * before its card opens. A shorter hold than this is a tap and activates the
 * trigger's own control. */
export const LONG_PRESS_MS = 500;

/** How far a finger may drift during a press before it stops counting as a
 * long press - a drag is a scroll, not a hold. */
const LONG_PRESS_SLOP_PX = 10;

export interface HoverCardProps {
  label: ReactNode;
  children: ReactNode | ((association: HoverCardAssociation) => ReactNode);
  focusTarget?: () => HTMLElement | null;
  longPressEnabled?: boolean;
}

export interface HoverCardAssociation {
  describedBy?: string;
}

const CLASS = {
  wrapper: requireClass(styles.wrapper, "hovercard.module.css", "wrapper"),
  bubble: requireClass(styles.bubble, "hovercard.module.css", "bubble"),
};

/** Rich, non-interactive description composed on the shared floating-label
 * lifecycle: hover or focus on a hover device. On a hoverless device neither
 * can show it (nothing would dismiss it), so `longPressEnabled` turns the trigger
 * into a long press - a plain tap is left to activate the control the trigger
 * wraps, and the click a lifted long press still sends is swallowed. */
export function HoverCard({ label, children, focusTarget, longPressEnabled }: HoverCardProps) {
  const longPressTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const longPressStartRef = useRef<{ x: number; y: number } | null>(null);
  const swallowClickRef = useRef(false);
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

  const cancelLongPress = useCallback(() => {
    clearTimeout(longPressTimerRef.current);
    longPressTimerRef.current = undefined;
    longPressStartRef.current = null;
  }, []);

  // A press still pending when the card goes must not fire later.
  useEffect(() => cancelLongPress, [cancelLongPress]);

  useEffect(() => {
    if (!longPressEnabled || !visible || !isHoverless()) return;
    const handleOutsidePointer = (event: PointerEvent) => {
      if (wrapperRef.current?.contains(event.target as Node | null)) return;
      swallowClickRef.current = false;
      dismiss();
    };
    document.addEventListener("pointerdown", handleOutsidePointer, true);
    return () => document.removeEventListener("pointerdown", handleOutsidePointer, true);
  }, [dismiss, longPressEnabled, visible, wrapperRef]);

  useEffect(() => {
    if (!visible) swallowClickRef.current = false;
  }, [visible]);

  // A card's label is arbitrary content, so unlike the tooltip it also accepts a
  // function child: a caller rendering the trigger itself places the
  // association where it belongs instead of being cloned.
  const describedChild =
    typeof children === "function" ? children({ describedBy }) : describeChild(children, describedBy);

  return (
    // longPressEnabled delegates a hoverless *long press* to the semantic child; a
    // tap falls through to that child's own control. Keyboard users use the
    // child's focus or the explicit external focus target on a hover device.
    // biome-ignore lint/a11y/noStaticElementInteractions: interaction semantics belong to the child trigger
    // biome-ignore lint/a11y/useKeyWithClickEvents: onClick only swallows the click a long press leaves behind, it never reveals the description
    <span
      ref={wrapperRef}
      className={CLASS.wrapper}
      {...triggerProps}
      onPointerDown={(event) => {
        if (!longPressEnabled || !isHoverless() || event.button !== 0) return;
        swallowClickRef.current = false;
        longPressStartRef.current = { x: event.clientX, y: event.clientY };
        clearTimeout(longPressTimerRef.current);
        longPressTimerRef.current = setTimeout(() => {
          longPressTimerRef.current = undefined;
          swallowClickRef.current = true;
          showImmediately();
        }, LONG_PRESS_MS);
      }}
      onPointerMove={(event) => {
        const start = longPressStartRef.current;
        if (longPressTimerRef.current === undefined || start === null) return;
        if (Math.hypot(event.clientX - start.x, event.clientY - start.y) > LONG_PRESS_SLOP_PX) cancelLongPress();
      }}
      onPointerUp={cancelLongPress}
      onPointerCancel={cancelLongPress}
      onPointerLeave={cancelLongPress}
      onClick={(event) => {
        // The lifted long press still sends one click to the trigger's own
        // control; swallow exactly that one, never a plain tap.
        if (!longPressEnabled || !isHoverless() || !swallowClickRef.current) return;
        swallowClickRef.current = false;
        event.stopPropagation();
      }}
      onContextMenu={(event) => {
        // The hold belongs to the card, so its platform callout must not open
        // over it on a hoverless device.
        if (!longPressEnabled || !isHoverless()) return;
        event.preventDefault();
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
        longPressEnabled={longPressEnabled}
      >
        {label}
      </FloatingBubblePortal>
    </span>
  );
}
