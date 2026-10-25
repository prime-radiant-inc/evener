import {
  type FocusEventHandler,
  type MouseEventHandler,
  type RefObject,
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import { ownerDocumentOf } from "../internal/documentRealm";
import { useOwnedDocument } from "../internal/useOwnedDocument";
import { watchViewport } from "../internal/viewportWatch";
import { acquirePointerActivity, type PointerActivityHandle } from "./pointerActivity";

export const SHOW_DELAY_MS = 300;

/** How long after a pointer lifts its press still counts as the pointer's own,
 * for the compatibility mousedown/focus/click a touch browser sends after
 * pointerup. */
export const POINTER_CLICK_WINDOW_MS = 350;

/** Whether the pointer that would hover or focus a floating label has no hover
 * capability. Hover shows nothing there: a tap fires it itself, and nothing
 * then fires a mouseleave to dismiss what it showed. The focus a tap takes is
 * ignored the same way, but keyboard and assistive-tech focus still shows a
 * label. Only an explicit touch affordance (HoverCard's long press) reveals a
 * label from a pointer. Tooltip and the un-enabled HoverCard already hide
 * themselves on touch via CSS. Optional-chained, so a browser without
 * matchMedia reads as hover-capable. */
export function isHoverless(): boolean {
  return typeof window !== "undefined" && !!window.matchMedia?.("(hover: none)")?.matches;
}

interface UseFloatingLabelArgs {
  measure: () => void;
  observe: RefObject<HTMLElement | null>;
  focusTarget?: () => HTMLElement | null;
  observeAnchor?: () => HTMLElement | null;
}

interface FloatingLabelTriggerProps {
  onMouseEnter: MouseEventHandler<HTMLSpanElement>;
  onMouseLeave: MouseEventHandler<HTMLSpanElement>;
  onFocus: FocusEventHandler<HTMLSpanElement>;
  onBlur: FocusEventHandler<HTMLSpanElement>;
}

interface UseFloatingLabelResult {
  visible: boolean;
  /** The document the label's own DOM lives in, current across a move. */
  ownerDocument: Document;
  wrapperRef: RefObject<HTMLSpanElement | null>;
  triggerProps: FloatingLabelTriggerProps;
  showImmediately: () => void;
  dismiss: () => void;
}

export function useFloatingLabel({
  measure,
  observe,
  focusTarget,
  observeAnchor,
}: UseFloatingLabelArgs): UseFloatingLabelResult {
  const [visible, setVisible] = useState(false);
  const [pending, setPending] = useState(false);
  const timerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const wrapperRef = useRef<HTMLSpanElement>(null);
  const activeRef = useRef({ hovered: false, wrapperFocused: false, externalFocused: false });
  // Follows Dockview moving this label's DOM between windows without a render.
  // Always on, not only while visible: the pointer tracker has to belong to the
  // destination document before the first tap there, or that tap reads as
  // keyboard focus and pops a hoverless card the person did not ask for.
  const ownerDocument = useOwnedDocument(wrapperRef);

  // A focus that arrives while a pointer is pressed, or just after it lifts
  // (touch browsers send the compatibility mousedown/focus/click only after
  // pointerup), is the focus a press takes, not keyboard or assistive-tech
  // focus; on a hoverless device it must not reveal a label the pointer cannot
  // then dismiss. The tracking is shared per Document, so the listener count
  // does not grow with the number of mounted labels.
  const pointerActivityRef = useRef<PointerActivityHandle | null>(null);
  const pointerTookFocus = useCallback(
    () => isHoverless() && (pointerActivityRef.current?.isPointerFocus(POINTER_CLICK_WINDOW_MS) ?? false),
    [],
  );
  // biome-ignore lint/correctness/useExhaustiveDependencies: ownerDocument is the deliberate rebinding trigger; the node is authoritative at attach time
  useEffect(() => {
    // The wrapper's actual document, not the tracked state: on the first
    // commit the state is still the opener's (the ref attaches during commit),
    // and a card mounted in a popout by a tap has to read that popout's
    // tracker to know the focus came from the tap. Later moves rebind through
    // the dependency below.
    const handle = acquirePointerActivity(ownerDocumentOf(wrapperRef.current));
    pointerActivityRef.current = handle;
    return () => {
      pointerActivityRef.current = null;
      handle.release();
    };
  }, [ownerDocument]);

  useEffect(() => () => clearTimeout(timerRef.current), []);

  // A floating element whose box changes after it is shown needs its placement
  // recomputed, or a shift computed off the old size leaves the new one hanging
  // off the edge. Content can change while the element is on screen; observing
  // the box covers that and every other cause (a late webfont, a re-wrap)
  // without the caller having to enumerate them. Feature-detected: jsdom
  // implements no ResizeObserver, and the caller's show-time measure is the
  // whole behavior without it.
  // biome-ignore lint/correctness/useExhaustiveDependencies: ownerDocument is the deliberate trigger - a document move replaces the observed bubble node
  useEffect(() => {
    if (!visible) return;
    const observedEl = observe.current;
    // The observer belongs to the observed element's own window: a popout's
    // element cannot be observed through the opener's constructor, and
    // delivery would follow the opener's rendering lifecycle.
    const Observer = observedEl?.ownerDocument.defaultView?.ResizeObserver;
    if (!observedEl || typeof Observer === "undefined") return;
    const observer = new Observer(measure);
    observer.observe(observedEl);
    const anchor = observeAnchor?.();
    if (anchor) observer.observe(anchor);
    return () => observer.disconnect();
  }, [measure, observe, observeAnchor, ownerDocument, visible]);

  // A scroll anywhere (capture-phase, so a scrollable ancestor's own scroll
  // counts) or a viewport resize hides the floating element rather than
  // repositioning it: placement is computed once per show, and a fixed-position
  // element left alone through a scroll would visibly detach from its trigger.
  // Re-triggering brings it straight back.
  useEffect(() => {
    if (!visible && !pending) return;
    function dismiss() {
      // Cancels the pending show too, not just the visible element: a trigger
      // that takes focus after a click can already have re-armed the delay, and
      // that timer would otherwise pop the element back 300ms after the scroll
      // that dismissed it.
      clearTimeout(timerRef.current);
      timerRef.current = undefined;
      setPending(false);
      setVisible(false);
    }
    // The wrapper's own window: a Dockview popout scrolls and resizes its own
    // window, and the opener's never sees either.
    return watchViewport(ownerDocument, dismiss);
  }, [ownerDocument, pending, visible]);

  const show = useCallback(() => {
    clearTimeout(timerRef.current);
    setPending(true);
    timerRef.current = setTimeout(() => {
      timerRef.current = undefined;
      setPending(false);
      setVisible(true);
    }, SHOW_DELAY_MS);
  }, []);

  const showImmediately = useCallback(() => {
    clearTimeout(timerRef.current);
    timerRef.current = undefined;
    setPending(false);
    setVisible(true);
  }, []);

  const hide = useCallback(() => {
    clearTimeout(timerRef.current);
    timerRef.current = undefined;
    setPending(false);
    setVisible(false);
  }, []);

  const hideWhenInactive = useCallback(() => {
    const active = activeRef.current;
    if (active.hovered || active.wrapperFocused || active.externalFocused) return;
    hide();
  }, [hide]);

  useEffect(() => {
    const target = focusTarget?.();
    if (!target) return;
    const handleFocus = () => {
      if (pointerTookFocus()) return;
      activeRef.current.externalFocused = true;
      showImmediately();
    };
    const handleBlur = (event: FocusEvent) => {
      if (target.contains(event.relatedTarget as Node | null)) return;
      activeRef.current.externalFocused = false;
      hideWhenInactive();
    };
    target.addEventListener("focusin", handleFocus);
    target.addEventListener("focusout", handleBlur);
    // The target's own document: in a popout the opener's activeElement is the
    // iframe, never the row that already has focus there.
    if (target.contains(ownerDocumentOf(target).activeElement)) handleFocus();
    return () => {
      target.removeEventListener("focusin", handleFocus);
      target.removeEventListener("focusout", handleBlur);
    };
  }, [focusTarget, hideWhenInactive, pointerTookFocus, showImmediately]);

  return {
    visible,
    ownerDocument,
    wrapperRef,
    showImmediately,
    dismiss: hide,
    triggerProps: {
      onMouseEnter: () => {
        if (isHoverless()) return;
        activeRef.current.hovered = true;
        show();
      },
      onMouseLeave: () => {
        activeRef.current.hovered = false;
        hideWhenInactive();
      },
      // Bubbling focus events within a multi-control wrapper must not flicker the label.
      onFocus: (event) => {
        if (pointerTookFocus()) return;
        if (event.currentTarget.contains(event.relatedTarget as Node | null)) return;
        activeRef.current.wrapperFocused = true;
        show();
      },
      onBlur: (event) => {
        if (event.currentTarget.contains(event.relatedTarget as Node | null)) return;
        activeRef.current.wrapperFocused = false;
        hideWhenInactive();
      },
    },
  };
}
