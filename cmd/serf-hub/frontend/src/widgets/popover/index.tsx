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
    setPosition(computePopoverPosition(triggerEl.getBoundingClientRect(), panelEl.getBoundingClientRect()));
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
    <span ref={triggerRef} className={CLASS.trigger}>
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
