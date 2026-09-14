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
import { requireClass } from "../internal/requireClass";
import { computeTooltipPosition, type TooltipPosition, TRIGGER_GAP } from "../tooltip/computePosition";
import styles from "./hovercard.module.css";
import { useFloatingLabel } from "./useFloatingLabel";

export interface HoverCardProps {
  label: ReactNode;
  children: ReactNode | ((association: HoverCardAssociation) => ReactNode);
}

export interface HoverCardAssociation {
  describedBy?: string;
}

const CLASS = {
  wrapper: requireClass(styles.wrapper, "hovercard.module.css", "wrapper"),
  bubble: requireClass(styles.bubble, "hovercard.module.css", "bubble"),
};

interface DescribableProps {
  "aria-describedby"?: string;
}

/** Rich, non-interactive description composed on the shared floating-label lifecycle. */
export function HoverCard({ label, children }: HoverCardProps) {
  const bubbleRef = useRef<HTMLDivElement>(null);
  const [position, setPosition] = useState<TooltipPosition | null>(null);
  const cardId = useId();

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

  useLayoutEffect(() => {
    if (!visible) {
      setPosition(null);
      return;
    }
    measure();
  }, [measure, visible]);

  const describedBy = visible ? cardId : undefined;
  const singleChild =
    typeof children !== "function" && Children.count(children) === 1 && isValidElement(children) ? children : null;
  const describedChild =
    typeof children === "function"
      ? children({ describedBy })
      : singleChild
        ? cloneElement(singleChild as ReactElement<DescribableProps>, {
            "aria-describedby": describedBy,
          })
        : children;

  return (
    <span ref={wrapperRef} className={CLASS.wrapper} {...triggerProps}>
      {describedChild}
      {visible &&
        createPortal(
          <div
            ref={bubbleRef}
            role="tooltip"
            id={cardId}
            className={CLASS.bubble}
            style={position ?? { top: TRIGGER_GAP, left: TRIGGER_GAP }}
          >
            {label}
          </div>,
          document.body,
        )}
    </span>
  );
}
