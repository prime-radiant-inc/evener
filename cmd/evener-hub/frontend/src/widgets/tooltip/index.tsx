import {
  Children,
  cloneElement,
  isValidElement,
  type ReactElement,
  type ReactNode,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import { requireClass } from "../internal/requireClass";
import { computeTooltipPosition, type TooltipPosition, TRIGGER_GAP } from "./computePosition";
import styles from "./tooltip.module.css";

export interface TooltipProps {
  label: string;
  children: ReactNode;
}

const SHOW_DELAY_MS = 300;

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
  const [visible, setVisible] = useState(false);
  const timerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const wrapperRef = useRef<HTMLSpanElement>(null);
  const bubbleRef = useRef<HTMLSpanElement>(null);
  const [position, setPosition] = useState<TooltipPosition | null>(null);
  const tooltipId = useId();

  useEffect(() => () => clearTimeout(timerRef.current), []);

  // Measure the trigger and the bubble and place the bubble. A layout effect,
  // so the measure and the re-render its setPosition causes both complete
  // before the browser's next paint, in the same commit that mounts the
  // portal: the bubble is never visibly painted at the pre-measure placeholder.
  useLayoutEffect(() => {
    if (!visible) {
      setPosition(null);
      return;
    }
    const wrapperEl = wrapperRef.current;
    const bubbleEl = bubbleRef.current;
    if (!wrapperEl || !bubbleEl) return;

    function measure() {
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
    }
    measure();

    // A bubble whose box changes after it is shown needs its placement
    // recomputed, or a shift computed off the old size leaves the new one
    // hanging off the edge. It happens for real: the composer swaps its send
    // label between "Send now · <chord>" and the ~100px-longer "Queue until
    // the agent stops · <chord>" the moment a turn starts, which can be while
    // the bubble is on screen. Observing the box covers that and every other
    // cause (a late webfont, a re-wrap) without the component having to
    // enumerate them. Feature-detected: jsdom implements no ResizeObserver,
    // and the show-time measure above is the whole behavior without it.
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(bubbleEl);
    return () => observer.disconnect();
  }, [visible]);

  // A scroll anywhere (capture-phase, so a scrollable ancestor's own scroll
  // counts) or a viewport resize hides the bubble rather than repositioning
  // it: placement is computed once per show, and a fixed-position bubble left
  // alone through a scroll would visibly detach from its trigger. Matches
  // Popover's own close-on-scroll for the same reason; for a tooltip the cost
  // is lower still, since re-hovering brings it straight back.
  useEffect(() => {
    if (!visible) return;
    function dismiss() {
      // Cancels the pending show too, not just the visible bubble: a trigger
      // that takes focus after a click has already re-armed the delay while
      // its own tooltip is up, and that timer would otherwise pop the bubble
      // back 300ms after the scroll that dismissed it.
      clearTimeout(timerRef.current);
      setVisible(false);
    }
    window.addEventListener("scroll", dismiss, true);
    window.addEventListener("resize", dismiss);
    return () => {
      window.removeEventListener("scroll", dismiss, true);
      window.removeEventListener("resize", dismiss);
    };
  }, [visible]);

  function show() {
    clearTimeout(timerRef.current);
    timerRef.current = setTimeout(() => setVisible(true), SHOW_DELAY_MS);
  }

  function hide() {
    clearTimeout(timerRef.current);
    setVisible(false);
  }

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
    // biome-ignore lint/a11y/noStaticElementInteractions: already dual mouse+keyboard triggered, see above
    <span
      ref={wrapperRef}
      className={CLASS.wrapper}
      onMouseEnter={show}
      onMouseLeave={hide}
      onFocus={show}
      onBlur={hide}
    >
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
