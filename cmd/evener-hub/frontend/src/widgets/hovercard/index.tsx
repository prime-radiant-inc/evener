import { type ReactNode, useEffect } from "react";
import { requireClass } from "../internal/requireClass";
import { LONG_PRESS_MS, useLongPress } from "../internal/useLongPress";
import type { SideAnchor } from "./computePosition";
import { describeChild, FloatingBubblePortal, useFloatingBubble } from "./floatingBubble";
import styles from "./hovercard.module.css";
import { isHoverless, POINTER_CLICK_WINDOW_MS } from "./useFloatingLabel";

export { LONG_PRESS_MS, POINTER_CLICK_WINDOW_MS };

/** How far a finger may drift during a press before it stops counting as a
 * long press - a drag is a scroll, not a hold. */
const LONG_PRESS_SLOP_PX = 10;

export interface HoverCardProps {
  label: ReactNode;
  children: ReactNode | ((association: HoverCardAssociation) => ReactNode);
  focusTarget?: () => HTMLElement | null;
  longPressEnabled?: boolean;
  /** Lay the wrapper out as a full-width block instead of shrink-wrapping the
   * trigger, so a flex or grid caller can own the width and clip the trigger
   * without wrapping the card in a span of its own. */
  block?: boolean;
  /** Optional side placement, measured by the caller, default placement is unchanged. */
  sideAnchor?: () => SideAnchor | null;
}

export interface HoverCardAssociation {
  describedBy?: string;
}

const CLASS = {
  wrapper: requireClass(styles.wrapper, "hovercard.module.css", "wrapper"),
  block: requireClass(styles.block, "hovercard.module.css", "block"),
  bubble: requireClass(styles.bubble, "hovercard.module.css", "bubble"),
};

/** Rich, non-interactive description composed on the shared floating-label
 * lifecycle. Hover, or the focus a keyboard or assistive technology takes,
 * shows it on any device. On a hoverless device the focus a tap takes shows
 * nothing (nothing would dismiss it), so `longPressEnabled` turns the trigger
 * into a long press: a plain tap activates the control the trigger wraps, and
 * the one click a lifted long press still sends is swallowed. */
export function HoverCard({ label, children, focusTarget, longPressEnabled, block, sideAnchor }: HoverCardProps) {
  const {
    visible,
    mounted,
    phase,
    wrapperRef,
    triggerProps,
    setBubbleRef,
    bubbleStyle,
    bubbleContainer,
    bubbleID,
    describedBy,
    showImmediately,
    dismiss,
  } = useFloatingBubble(focusTarget, sideAnchor);
  const ownerDocument = bubbleContainer.ownerDocument;
  const longPress = useLongPress({
    onLongPress: showImmediately,
    holdMs: LONG_PRESS_MS,
    slopPx: LONG_PRESS_SLOP_PX,
    enabled: Boolean(longPressEnabled && isHoverless()),
    cancelOnPointerLeave: true,
    cancelOnViewportChange: true,
    capturePointer: false,
    ownerDocument,
  });
  const { cancel: cancelLongPress, clearClickSuppression } = longPress;

  // Turning long-press mode off mid-gesture clears the press and its listeners.
  useEffect(() => {
    if (!longPressEnabled) cancelLongPress();
  }, [cancelLongPress, longPressEnabled]);

  // The card's own outside press dismisses it and gives up the armed click too.
  useEffect(() => {
    if (!longPressEnabled || !visible || !isHoverless()) return;
    const handleOutsidePointer = (event: PointerEvent) => {
      if (wrapperRef.current?.contains(event.target as Node | null)) return;
      clearClickSuppression();
      dismiss();
    };
    ownerDocument.addEventListener("pointerdown", handleOutsidePointer, true);
    return () => ownerDocument.removeEventListener("pointerdown", handleOutsidePointer, true);
  }, [clearClickSuppression, dismiss, longPressEnabled, ownerDocument, visible, wrapperRef]);

  useEffect(() => {
    if (!visible) clearClickSuppression();
  }, [clearClickSuppression, visible]);

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
      className={block ? `${CLASS.wrapper} ${CLASS.block}` : CLASS.wrapper}
      {...triggerProps}
      {...longPress.handlers}
      onClickCapture={(event) => {
        if (!longPressEnabled || !isHoverless()) return;
        // The lifted long press swallows exactly one click in the capture phase;
        // any other tap is the trigger's own and takes the card down with it.
        if (longPress.consumeClick(event)) return;
        if (visible) dismiss();
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
        show={mounted}
        phase={phase}
        id={bubbleID}
        className={CLASS.bubble}
        style={bubbleStyle}
        setRef={setBubbleRef}
        container={bubbleContainer}
        longPressEnabled={longPressEnabled}
      >
        {label}
      </FloatingBubblePortal>
    </span>
  );
}
