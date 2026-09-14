import { type FocusEventHandler, type MouseEventHandler, type RefObject, useEffect, useRef, useState } from "react";

const SHOW_DELAY_MS = 300;

interface UseFloatingLabelArgs {
  measure: () => void;
  observe: RefObject<HTMLElement | null>;
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
}

export function useFloatingLabel({ measure, observe }: UseFloatingLabelArgs): UseFloatingLabelResult {
  const [visible, setVisible] = useState(false);
  const [pending, setPending] = useState(false);
  const timerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const wrapperRef = useRef<HTMLSpanElement>(null);

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

  function show() {
    clearTimeout(timerRef.current);
    setPending(true);
    timerRef.current = setTimeout(() => {
      timerRef.current = undefined;
      setPending(false);
      setVisible(true);
    }, SHOW_DELAY_MS);
  }

  function hide() {
    clearTimeout(timerRef.current);
    timerRef.current = undefined;
    setPending(false);
    setVisible(false);
  }

  return {
    visible,
    wrapperRef,
    triggerProps: {
      onMouseEnter: show,
      onMouseLeave: hide,
      // Bubbling focus events within a multi-control wrapper must not flicker the label.
      onFocus: (event) => {
        if (event.currentTarget.contains(event.relatedTarget as Node | null)) return;
        show();
      },
      onBlur: (event) => {
        if (event.currentTarget.contains(event.relatedTarget as Node | null)) return;
        hide();
      },
    },
  };
}
