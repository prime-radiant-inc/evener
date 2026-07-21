import {
  Children,
  cloneElement,
  isValidElement,
  type ReactElement,
  type ReactNode,
  useEffect,
  useId,
  useRef,
  useState,
} from "react";
import { requireClass } from "../internal/requireClass";
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
 */
export function Tooltip({ label, children }: TooltipProps) {
  const [visible, setVisible] = useState(false);
  const timerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const tooltipId = useId();

  useEffect(() => () => clearTimeout(timerRef.current), []);

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
    <span className={CLASS.wrapper} onMouseEnter={show} onMouseLeave={hide} onFocus={show} onBlur={hide}>
      {describedChild}
      {visible && (
        <span role="tooltip" id={tooltipId} className={CLASS.bubble}>
          {label}
        </span>
      )}
    </span>
  );
}
