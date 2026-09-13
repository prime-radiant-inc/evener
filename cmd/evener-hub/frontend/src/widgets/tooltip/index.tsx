import {
  Children,
  cloneElement,
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
import { useFloatingLabel } from "../hovercard/useFloatingLabel";
import { requireClass } from "../internal/requireClass";
import { computeTooltipPosition, type TooltipPosition, TRIGGER_GAP } from "./computePosition";
import styles from "./tooltip.module.css";

export interface TooltipProps {
  label: string;
  children: ReactNode;
}

const CLASS = {
  wrapper: requireClass(styles.wrapper, "tooltip.module.css", "wrapper"),
  bubble: requireClass(styles.bubble, "tooltip.module.css", "bubble"),
};

interface DescribableProps {
  "aria-describedby"?: string;
}

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
 * trigger element itself to be announced correctly - so it's applied via
 * cloneElement, and only when children is exactly one element. This works
 * for a native element (<button>, <a>, ...) or any widget that forwards a
 * ref and spreads unrecognized props onto its own DOM node - Button and
 * IconButton both do (see their own index.tsx); a single-child trigger
 * that does neither still gets the visible show/hide behavior via the
 * wrapper span, just not the aria-describedby association, since
 * cloneElement has nowhere for the extra prop to land.
 *
 * The bubble is portaled to document.body and positioned in viewport
 * coordinates (see computePosition.ts for the placement math). Two reasons,
 * both measured live rather than assumed:
 *
 *   1. Collision. Centre-anchored with no shift, an edge-mounted bubble runs
 *      off the viewport. The rail's search button sits 28px from the top of a
 *      1440x900 window, and its bubble's top edge landed at y = -8 - clipped
 *      by the viewport with a one-word label.
 *   2. Clipping ancestors. A bubble laid out inside the trigger's own flow is
 *      cut by any ancestor with overflow: hidden, and the panes are exactly
 *      that. The composer's Send tooltip measured a right edge of 1439.27 in a
 *      1440px window - two pixels of viewport margin, and so read as nearly
 *      safe - while the pane clipping it ends at x = 1424, so 15.27px of the
 *      bubble was already invisible: it rendered as "Send now - Cmd+Ente".
 *      Shifting against the viewport alone would not have fixed that; only
 *      leaving the clipping subtree does.
 */
export function Tooltip({ label, children }: TooltipProps) {
  const bubbleRef = useRef<HTMLSpanElement>(null);
  const [position, setPosition] = useState<TooltipPosition | null>(null);
  const tooltipId = useId();

  // biome-ignore lint/correctness/useExhaustiveDependencies: wrapperRef comes from the hook below and is stable for this component's lifetime
  const measure = useCallback(() => {
    const wrapperEl = wrapperRef.current;
    const bubbleEl = bubbleRef.current;
    if (!wrapperEl || !bubbleEl) return;

    // offsetWidth/offsetHeight, not getBoundingClientRect(), for the
    // bubble's own size: those report the untransformed layout box, so the
    // placement stays correct if the bubble ever gains a scale-in
    // animation. Popover shipped that exact bug - measured mid-animation at
    // scale(0.96), it clamped a 376px panel as though it were 361px and
    // overran the viewport by 7px. The wrapper keeps its rect: that one is
    // wanted in viewport coordinates, which is what position: fixed
    // consumes.
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
  // before the browser's next paint, in the same commit that mounts the
  // portal: the bubble is never visibly painted at the pre-measure placeholder.
  useLayoutEffect(() => {
    if (!visible) {
      setPosition(null);
      return;
    }
    measure();
  }, [measure, visible]);

  const singleChild = Children.count(children) === 1 && isValidElement(children) ? children : null;
  const describedChild = singleChild
    ? cloneElement(singleChild as ReactElement<DescribableProps>, {
        "aria-describedby": visible ? tooltipId : undefined,
      })
    : children;

  return (
    // Already both mouse- AND keyboard-triggered (onFocus/onBlur mirror
    // onMouseEnter/onMouseLeave exactly, per this file's own top comment) -
    // the WAI-ARIA-recommended tooltip pattern. This wrapper isn't becoming
    // a new interactive control needing a role; it's showing/hiding a
    // role="tooltip" description already wired to the real trigger element
    // via aria-describedby (describedChild below).
    // Already dual mouse+keyboard triggered, see above.
    <span ref={wrapperRef} className={CLASS.wrapper} {...triggerProps}>
      {describedChild}
      {visible &&
        createPortal(
          <span
            ref={bubbleRef}
            role="tooltip"
            id={tooltipId}
            className={CLASS.bubble}
            // Before the measure lands, the bubble sits at the top-left corner
            // one gap in - off the trigger, but on-screen and laid out at its
            // natural size, which is what gives the layout effect above a real
            // box to read. It is never painted there: that effect's
            // setPosition runs in the same commit.
            style={position ?? { top: TRIGGER_GAP, left: TRIGGER_GAP }}
          >
            {label}
          </span>,
          document.body,
        )}
    </span>
  );
}
