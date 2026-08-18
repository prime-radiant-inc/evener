import {
  type KeyboardEvent,
  type ReactElement,
  type ReactNode,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import { FocusScope } from "../focusscope";
import { requireClass } from "../internal/requireClass";
import { computePopoverPosition, type PopoverPosition, TRIGGER_GAP } from "./computePosition";
import styles from "./popover.module.css";

const CLASS = {
  trigger: requireClass(styles.trigger, "popover.module.css", "trigger"),
  triggerStretch: requireClass(styles.triggerStretch, "popover.module.css", "triggerStretch"),
  panel: requireClass(styles.panel, "popover.module.css", "panel"),
};

export interface PopoverProps {
  open: boolean;
  /** Fired on outside click, Escape, and — unless closeOnScroll is false —
   * scroll or resize. The trigger's own toggle is the consumer's job; Popover
   * never fires onClose for a click on the trigger itself (it's inside the
   * trigger wrapper, see below). */
  onClose: () => void;
  /** Rendered in-flow inside a measuring wrapper; its rect anchors the panel. */
  trigger: ReactElement;
  /** The floating panel content, portaled to document.body when open. */
  children: ReactNode;
  /** When false, opening the panel does NOT move focus into it (combobox
   * pattern: the anchoring input keeps focus for continued typing). Tab
   * still cycles within the panel once focus moves there. Default true. */
  autoFocus?: boolean;
  /** When false, neither a window scroll (capture-phase) nor a viewport
   * resize closes the panel. For a panel whose own content scrolls and whose
   * interaction must survive a page scroll behind it — the model picker.
   * Trade-off: without the close, a page scroll can visually detach the
   * panel from its trigger, since placement is computed once per open.
   * Default true (Menu-shaped behavior). */
  closeOnScroll?: boolean;
  /** When true, the in-flow trigger wrapper fills its container's width
   * instead of hugging the trigger. For a popover whose trigger IS a form
   * control (the model picker) and so must line up with the Input/Select
   * fields beside it. Default false (hug — Menu, Tooltip, chrome triggers). */
  stretchTrigger?: boolean;
  "data-testid"?: string;
}

/**
 * The shared floating-popover primitive (§3.4): a trigger rendered in-flow
 * plus a panel that, when open, portals to document.body at a position:fixed
 * coordinate computed off the trigger's own getBoundingClientRect() - so
 * opening it NEVER pushes page content down (no reflow), and a clipping
 * ancestor (a scrollable rail row, a pane hard against the viewport edge)
 * can't cut it off. The placement math (flip-then-clamp) lives in
 * computePosition.ts, shared verbatim with widgets/menu.
 *
 * Behavior mirrors Menu: reopening always re-measures rather than trusting a
 * stale position; a scroll anywhere (capture-phase, so a scrollable
 * ancestor's own scroll counts too) or a viewport resize closes it rather
 * than continuously repositioning; an outside click or Escape closes it.
 * Built on FocusScope with trap so Tab/Shift+Tab cycle within the panel while
 * open, matching Menu.
 */
export function Popover({
  open,
  onClose,
  trigger,
  children,
  autoFocus = true,
  closeOnScroll = true,
  stretchTrigger = false,
  ...rest
}: PopoverProps) {
  const triggerRef = useRef<HTMLSpanElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);
  const [position, setPosition] = useState<PopoverPosition | null>(null);

  // Measure the trigger and the panel's own natural size and set the actual
  // flipped/clamped position - see computePopoverPosition. This is a layout
  // effect, so the measure and the re-render its setPosition causes both
  // complete before the browser's next paint, in the same commit as the
  // portal's mount: the panel is never visibly painted at the pre-measure
  // placeholder (and never visibility:hidden, which would make it unreachable
  // to FocusScope's tabbable() scan - see Menu's own note). Closing clears the
  // position so the next open re-measures from scratch.
  useLayoutEffect(() => {
    if (!open) {
      setPosition(null);
      return;
    }
    const triggerEl = triggerRef.current;
    const panelEl = panelRef.current;
    if (!triggerEl || !panelEl) return;

    function measure() {
      if (!triggerEl || !panelEl) return;
      // offsetWidth/offsetHeight, NOT getBoundingClientRect(), for the panel's
      // own size: a rect is the box AFTER transforms, and this measurement
      // happens in the same commit that mounts the panel - the frame where
      // popoverFadeScale is still at its `from` keyframe, scale(0.96). A 376px
      // panel therefore rects as 361px, computePopoverPosition clamps to the
      // 361px width, and the panel then grows to 376px in place and overruns
      // EDGE_MARGIN. Measured live at a 390px viewport: left 21.03 / right
      // 397.03, seven pixels past the viewport's right edge; with the
      // animation disabled the same panel landed at left 8 / right 384.
      // The offset* pair reports the untransformed layout box, so it reads the
      // settled size on the first frame with no wait and no reposition flash.
      // (The trigger keeps its rect: that one is wanted in viewport
      // coordinates, and the trigger is never mid-animation here.)
      setPosition(
        computePopoverPosition(triggerEl.getBoundingClientRect(), {
          width: panelEl.offsetWidth,
          height: panelEl.offsetHeight,
        }),
      );
    }
    measure();

    // A panel whose content arrives asynchronously (the model picker's
    // catalog fetch: a narrow loading skeleton, then a full-width list) is a
    // DIFFERENT size than the one just measured, and a flipped placement
    // computed off the small size leaves the grown panel hanging off the
    // viewport edge - measured live: a 98px-wide skeleton right-aligned to
    // its trigger, then grown to 368px, pushed 6px past a 390px viewport.
    // Re-measure whenever the panel's own box changes so the placement always
    // describes the panel actually on screen. Feature-detected: jsdom (the
    // test environment) implements no ResizeObserver, and the open-time
    // measure above is the whole behavior without it.
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(panelEl);
    return () => observer.disconnect();
  }, [open]);

  // Outside click closes. A click on the trigger is inside triggerRef, so this
  // never fights the consumer's own trigger toggle; a click inside the panel
  // is inside panelRef, which the trigger wrapper alone can't see now that the
  // panel renders through a portal elsewhere in the DOM.
  useEffect(() => {
    if (!open) return;
    function onDocumentMouseDown(event: MouseEvent) {
      const target = event.target as Node;
      if (triggerRef.current?.contains(target) || panelRef.current?.contains(target)) return;
      onClose();
    }
    document.addEventListener("mousedown", onDocumentMouseDown);
    return () => document.removeEventListener("mousedown", onDocumentMouseDown);
  }, [open, onClose]);

  // A scroll anywhere (capture-phase) or a viewport resize closes the popover -
  // simpler than continuously repositioning, matching Menu. Consumers whose
  // panel content scrolls opt out with closeOnScroll={false}.
  useEffect(() => {
    if (!open || !closeOnScroll) return;
    window.addEventListener("scroll", onClose, true);
    window.addEventListener("resize", onClose);
    return () => {
      window.removeEventListener("scroll", onClose, true);
      window.removeEventListener("resize", onClose);
    };
  }, [open, onClose, closeOnScroll]);

  function handlePanelKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === "Escape") {
      // Stop the event from also reaching an enclosing overlay (e.g. a Dialog
      // this popover is nested in), mirroring Menu's Escape handling.
      event.preventDefault();
      event.stopPropagation();
      onClose();
    }
  }

  return (
    <span ref={triggerRef} className={`${CLASS.trigger} ${stretchTrigger ? CLASS.triggerStretch : ""}`}>
      {trigger}
      {open &&
        createPortal(
          <FocusScope trap autoFocus={autoFocus}>
            {/* biome-ignore lint/a11y/noStaticElementInteractions: keydown only closes on Escape; the panel is a portaled overlay container, not itself interactive */}
            <div
              ref={panelRef}
              className={CLASS.panel}
              data-testid={rest["data-testid"]}
              style={position ? { top: position.top, left: position.left } : { top: TRIGGER_GAP, left: 0 }}
              onKeyDown={handlePanelKeyDown}
            >
              {children}
            </div>
          </FocusScope>,
          document.body,
        )}
    </span>
  );
}
