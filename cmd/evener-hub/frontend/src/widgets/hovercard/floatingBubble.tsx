import {
  Children,
  type CSSProperties,
  cloneElement,
  createElement,
  isValidElement,
  type ReactElement,
  type ReactNode,
  useCallback,
  useId,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import { computeTooltipPosition, type TooltipPosition, TRIGGER_GAP } from "../tooltip/computePosition";
import { useFloatingLabel } from "./useFloatingLabel";

// The scaffold both floating labels sit on: the tooltip and the hover card
// share placement, the portal that carries them out of clipping ancestors, the
// pre-measure placeholder, and the aria association, differing only in the
// element and content they render.
//
// The bubble is portaled to document.body and positioned in viewport
// coordinates (see computePosition.ts for the placement math). Two reasons,
// both measured live rather than assumed:
//
//   1. Collision. Centre-anchored with no shift, an edge-mounted bubble runs
//      off the viewport. The rail's search button sits 28px from the top of a
//      1440x900 window, and its bubble's top edge landed at y = -8 - clipped
//      by the viewport with a one-word label.
//   2. Clipping ancestors. A bubble laid out inside the trigger's own flow is
//      cut by any ancestor with overflow: hidden, and the panes are exactly
//      that. The composer's Send tooltip measured a right edge of 1439.27 in a
//      1440px window - two pixels of viewport margin, and so read as nearly
//      safe - while the pane clipping it ends at x = 1424, so 15.27px of the
//      bubble was already invisible: it rendered as "Send now - Cmd+Ente".
//      Shifting against the viewport alone would not have fixed that; only
//      leaving the clipping subtree does.

export interface FloatingBubble {
  visible: boolean;
  wrapperRef: ReturnType<typeof useFloatingLabel>["wrapperRef"];
  triggerProps: ReturnType<typeof useFloatingLabel>["triggerProps"];
  /** Attach to the portaled bubble, whichever element type it uses. */
  setBubbleRef: (element: HTMLElement | null) => void;
  /** What the bubble renders with: the measured placement, or the placeholder. */
  bubbleStyle: CSSProperties;
  /** The bubble's id, for as long as the bubble is on screen. */
  bubbleID: string;
  /** The trigger's aria-describedby value, undefined while nothing is shown. */
  describedBy: string | undefined;
}

/** The lifecycle, placement and association of one floating label. */
export function useFloatingBubble(): FloatingBubble {
  const bubbleRef = useRef<HTMLElement | null>(null);
  const [position, setPosition] = useState<TooltipPosition | null>(null);
  const bubbleID = useId();

  // offsetWidth/offsetHeight, not getBoundingClientRect(), for the bubble's own
  // size: those report the untransformed layout box, so the placement stays
  // correct if the bubble ever gains a scale-in animation. Popover shipped that
  // exact bug - measured mid-animation at scale(0.96), it clamped a 376px panel
  // as though it were 361px and overran the viewport by 7px. The wrapper keeps
  // its rect: that one is wanted in viewport coordinates, which is what
  // position: fixed consumes.
  // biome-ignore lint/correctness/useExhaustiveDependencies: wrapperRef comes from the hook below and is stable for this component's lifetime
  const measure = useCallback(() => {
    const wrapperEl = wrapperRef.current;
    const bubbleEl = bubbleRef.current;
    if (!wrapperEl || !bubbleEl) return;
    setPosition(
      computeTooltipPosition(
        wrapperEl.getBoundingClientRect(),
        { width: bubbleEl.offsetWidth, height: bubbleEl.offsetHeight },
        { width: window.innerWidth, height: window.innerHeight },
      ),
    );
  }, []);

  const { visible, wrapperRef, triggerProps } = useFloatingLabel({ measure, observe: bubbleRef });

  // Measure the trigger and the bubble and place the bubble. A layout effect,
  // so the measure and the re-render its setPosition causes both complete
  // before the browser's next paint, in the same commit that mounts the portal:
  // the bubble is never visibly painted at the pre-measure placeholder.
  useLayoutEffect(() => {
    if (!visible) {
      setPosition(null);
      return;
    }
    measure();
  }, [measure, visible]);

  const setBubbleRef = useCallback((element: HTMLElement | null) => {
    bubbleRef.current = element;
  }, []);

  return {
    visible,
    wrapperRef,
    triggerProps,
    setBubbleRef,
    // Before the measure lands, the bubble sits at the top-left corner one gap
    // in - off the trigger, but on-screen and laid out at its natural size,
    // which is what gives the layout effect above a real box to read. It is
    // never painted there: that effect's setPosition runs in the same commit.
    bubbleStyle: position ?? { top: TRIGGER_GAP, left: TRIGGER_GAP },
    bubbleID,
    describedBy: visible ? bubbleID : undefined,
  };
}

export interface FloatingBubblePortalProps {
  /** The bubble's element: the tooltip's inline span, the card's block div. */
  as: "div" | "span";
  show: boolean;
  id: string;
  className: string;
  style: CSSProperties;
  setRef: (element: HTMLElement | null) => void;
  children: ReactNode;
}

/** The portaled, role="tooltip" element carrying one floating label. */
export function FloatingBubblePortal({ as, show, id, className, style, setRef, children }: FloatingBubblePortalProps) {
  if (!show) return null;
  return createPortal(
    createElement(as, { ref: setRef, role: "tooltip", id, className, style }, children),
    document.body,
  );
}

interface DescribableProps {
  "aria-describedby"?: string;
}

/**
 * Wires the aria association onto the trigger. It has to land on the trigger
 * element itself to be announced correctly, so it is applied by cloneElement
 * and only when children is exactly one element. This works for a native
 * element (<button>, <a>, ...) or any widget that forwards a ref and spreads
 * unrecognized props onto its own DOM node - Button and IconButton both do (see
 * their own index.tsx); a single-child trigger that does neither still gets the
 * visible show/hide behavior via the wrapper span, just not the association,
 * since cloneElement has nowhere for the extra prop to land.
 */
export function describeChild(child: ReactNode, describedBy: string | undefined): ReactNode {
  return Children.count(child) === 1 && isValidElement(child)
    ? cloneElement(child as ReactElement<DescribableProps>, { "aria-describedby": describedBy })
    : child;
}
