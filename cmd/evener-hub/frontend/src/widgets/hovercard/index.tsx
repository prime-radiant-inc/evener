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

/** How long after a lifted press the browser's one click is still expected.
 * Past it the swallow is dropped, so a gesture the browser ends without a
 * click cannot leave a later keyboard- or AT-synthesized click swallowed. */
const LONG_PRESS_CLICK_WINDOW_MS = 350;

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
 * lifecycle. Hover, or the focus a keyboard or assistive technology takes,
 * shows it on any device. On a hoverless device the focus a tap takes shows
 * nothing (nothing would dismiss it), so `longPressEnabled` turns the trigger
 * into a long press: a plain tap activates the control the trigger wraps, and
 * the one click a lifted long press still sends is swallowed. */
export function HoverCard({ label, children, focusTarget, longPressEnabled }: HoverCardProps) {
  const longPressTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const longPressStartRef = useRef<{ x: number; y: number } | null>(null);
  const swallowClickRef = useRef(false);
  const swallowBackstopRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
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

  const clearSwallowBackstop = useCallback(() => {
    clearTimeout(swallowBackstopRef.current);
    swallowBackstopRef.current = undefined;
  }, []);

  // A press still pending, or a swallow still armed, when the card goes must
  // not fire later.
  useEffect(
    () => () => {
      cancelLongPress();
      clearSwallowBackstop();
    },
    [cancelLongPress, clearSwallowBackstop],
  );

  // The lifted press still sends one click; keep the swallow armed for it, but
  // bound the arming so a gesture the browser ends without a click cannot leave
  // a later click swallowed.
  const endPress = useCallback(() => {
    cancelLongPress();
    if (!swallowClickRef.current) return;
    clearSwallowBackstop();
    swallowBackstopRef.current = setTimeout(() => {
      swallowBackstopRef.current = undefined;
      swallowClickRef.current = false;
    }, LONG_PRESS_CLICK_WINDOW_MS);
  }, [cancelLongPress, clearSwallowBackstop]);

  const abortPress = useCallback(() => {
    cancelLongPress();
    clearSwallowBackstop();
    swallowClickRef.current = false;
  }, [cancelLongPress, clearSwallowBackstop]);

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
    // tap falls through to that child's own control. Keyboard and assistive-tech
    // focus still reveals the card on any device.
    // biome-ignore lint/a11y/noStaticElementInteractions: interaction semantics belong to the child trigger
    <span
      ref={wrapperRef}
      className={CLASS.wrapper}
      {...triggerProps}
      onPointerDown={(event) => {
        if (!longPressEnabled || !isHoverless() || event.button !== 0) return;
        swallowClickRef.current = false;
        clearSwallowBackstop();
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
      onPointerUp={endPress}
      onPointerCancel={abortPress}
      onPointerLeave={cancelLongPress}
      onClickCapture={(event) => {
        if (!longPressEnabled || !isHoverless()) return;
        if (!swallowClickRef.current) {
          // Any other tap is the trigger's own: let it activate, and take the
          // card down with it so nothing lingers over the next screen.
          if (visible) dismiss();
          return;
        }
        // The lifted long press sends one click to the control the trigger
        // wraps; swallow exactly that one, in the capture phase so a handler
        // on the trigger itself cannot run first, and stop its default action
        // too.
        swallowClickRef.current = false;
        clearSwallowBackstop();
        event.preventDefault();
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
