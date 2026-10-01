import {
  type FocusEventHandler,
  type MouseEventHandler,
  type RefObject,
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";

export const SHOW_DELAY_MS = 300;

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
}

interface FloatingLabelTriggerProps {
  onMouseEnter: MouseEventHandler<HTMLSpanElement>;
  onMouseLeave: MouseEventHandler<HTMLSpanElement>;
  onFocus: FocusEventHandler<HTMLSpanElement>;
  onBlur: FocusEventHandler<HTMLSpanElement>;
}

interface UseFloatingLabelResult {
  visible: boolean;
  wrapperRef: RefObject<HTMLSpanElement | null>;
  triggerProps: FloatingLabelTriggerProps;
  showImmediately: () => void;
  dismiss: () => void;
}

export function useFloatingLabel({ measure, observe, focusTarget }: UseFloatingLabelArgs): UseFloatingLabelResult {
  const [visible, setVisible] = useState(false);
  const [pending, setPending] = useState(false);
  const timerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const wrapperRef = useRef<HTMLSpanElement>(null);
  const activeRef = useRef({ hovered: false, wrapperFocused: false, externalFocused: false });

  // A focus that arrives while a pointer is pressed is the focus the press
  // takes, not keyboard or assistive-tech focus; on a hoverless device it must
  // not reveal a label the pointer cannot then dismiss.
  const pointerPressedRef = useRef(false);
  useEffect(() => {
    const press = () => {
      pointerPressedRef.current = true;
    };
    const release = () => {
      pointerPressedRef.current = false;
    };
    document.addEventListener("pointerdown", press, true);
    document.addEventListener("pointerup", release, true);
    document.addEventListener("pointercancel", release, true);
    return () => {
      document.removeEventListener("pointerdown", press, true);
      document.removeEventListener("pointerup", release, true);
      document.removeEventListener("pointercancel", release, true);
    };
  }, []);

  useEffect(() => () => clearTimeout(timerRef.current), []);

  // A floating element whose box changes after it is shown needs its placement
  // recomputed, or a shift computed off the old size leaves the new one hanging
  // off the edge. Content can change while the element is on screen; observing
  // the box covers that and every other cause (a late webfont, a re-wrap)
  // without the caller having to enumerate them. Feature-detected: jsdom
  // implements no ResizeObserver, and the caller's show-time measure is the
  // whole behavior without it.
  useEffect(() => {
    if (!visible) return;
    const observedEl = observe.current;
    if (!observedEl || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(observedEl);
    return () => observer.disconnect();
  }, [measure, observe, visible]);

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
    window.addEventListener("scroll", dismiss, true);
    window.addEventListener("resize", dismiss);
    return () => {
      window.removeEventListener("scroll", dismiss, true);
      window.removeEventListener("resize", dismiss);
    };
  }, [pending, visible]);

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
      if (isHoverless() && pointerPressedRef.current) return;
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
    if (target.contains(document.activeElement)) handleFocus();
    return () => {
      target.removeEventListener("focusin", handleFocus);
      target.removeEventListener("focusout", handleBlur);
    };
  }, [focusTarget, hideWhenInactive, showImmediately]);

  return {
    visible,
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
        if (isHoverless() && pointerPressedRef.current) return;
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
