import { type ButtonHTMLAttributes, forwardRef, type MouseEvent, type ReactNode } from "react";
import { isAriaRefusal, refuseClick } from "../button";
import buttonStyles from "../button/button.module.css";
import { requireClass } from "../internal/requireClass";
import styles from "./countbutton.module.css";

/** row lays the glyph, label, and count out inline (the status bar and the
 * cascade column header); column stacks the glyph over the count for the
 * cascade spine's 52px well. */
export type CountButtonOrientation = "row" | "column";

/** wash is Button's quiet chrome, transparent at rest with a hover fill; pill
 * sets the chip on its own --surface-canvas ground, as the status bar's
 * glance counters do. */
export type CountButtonChrome = "wash" | "pill";

export interface CountButtonProps
  extends Omit<
    ButtonHTMLAttributes<HTMLButtonElement>,
    "type" | "onClick" | "disabled" | "children" | "className" | "aria-label" | "aria-expanded"
  > {
  /** The visible category mark. Decorative: `accessibleLabel` names it. */
  glyph: ReactNode;
  /** The visible category name; dropped from the layout by `compact` but
   * still present for the accessible name's caller-computed suffix. */
  label: string;
  /** The visible count. The caller chooses a fraction or a single count. */
  count: string;
  /** The button's only accessible name; required because the visible label
   * can be compacted away and the count alone does not name the category. */
  accessibleLabel: string;
  /** Drop the visible label, keeping the glyph and count (a phone chip or a
   * cascade column header). The accessible name still carries the label. */
  compact?: boolean;
  orientation?: CountButtonOrientation;
  /** The open state of the surface this count opens: reported as
   * `aria-expanded` and holds the wash while open. */
  pressed?: boolean;
  chrome?: CountButtonChrome;
  onClick?: (event: MouseEvent<HTMLButtonElement>) => void;
  disabled?: boolean;
  type?: "button" | "submit" | "reset";
}

// Reuses button.module.css's base and quiet classes directly (read-only
// import, not a copy), the same composition IconButton uses: Button's props
// have no className slot, and this widget must add its own counter layout and
// chrome classes. The shared classes supply the identical colors, hover and
// pressed states, disabled/refusal look, press transform and :focus-visible
// ring; countbutton.module.css carries only the chip geometry and chrome.
const BUTTON_CLASS = {
  button: requireClass(buttonStyles.button, "button.module.css", "button"),
  quiet: requireClass(buttonStyles.quiet, "button.module.css", "quiet"),
};

const CLASS = {
  root: requireClass(styles.root, "countbutton.module.css", "root"),
  glyph: requireClass(styles.glyph, "countbutton.module.css", "glyph"),
  label: requireClass(styles.label, "countbutton.module.css", "label"),
  count: requireClass(styles.count, "countbutton.module.css", "count"),
};

/**
 * A quiet count button: a decorative glyph, a category label, and a count,
 * for a surface that opens an activity view (the status bar's Overview chips
 * and the cascade's peek triggers). One widget because both surfaces render
 * the same four fields from ACTIVITY_TABS and differ only in activation and
 * chrome.
 *
 * Forwards its ref and spreads native button attributes (aria-*, data-*,
 * title, handlers) onto the underlying `<button>`, so a caller can keep it a
 * Popover trigger or query it by its activity data attributes. `aria-label`
 * and `aria-expanded` are the widget's own, through `accessibleLabel` and
 * `pressed`; the rest of the ARIA surface (`aria-haspopup`, `aria-describedby`,
 * ...) passes through. `{...rest}` is spread before those explicit attributes,
 * the same merge order as Button, so a loosely typed caller spread cannot
 * replace the widget's controlled props.
 */
export const CountButton = forwardRef<HTMLButtonElement, CountButtonProps>(function CountButton(
  {
    glyph,
    label,
    count,
    accessibleLabel,
    compact = false,
    orientation = "row",
    pressed,
    chrome = "wash",
    onClick,
    disabled = false,
    type = "button",
    ...rest
  },
  ref,
) {
  const refused = isAriaRefusal(rest["aria-disabled"]);
  return (
    <button
      ref={ref}
      {...rest}
      type={type}
      aria-label={accessibleLabel}
      aria-expanded={pressed === undefined ? undefined : pressed}
      data-chrome={chrome}
      data-compact={compact ? "true" : undefined}
      data-orientation={orientation}
      data-pressed={pressed === true ? "true" : undefined}
      className={`${BUTTON_CLASS.button} ${BUTTON_CLASS.quiet} ${CLASS.root}`}
      onClick={refused ? refuseClick : onClick}
      disabled={disabled}
    >
      <span className={CLASS.glyph} aria-hidden="true">
        {glyph}
      </span>
      <span className={CLASS.label}>{label}</span>
      <span className={CLASS.count}>{count}</span>
    </button>
  );
});
