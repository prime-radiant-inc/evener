import { type ButtonHTMLAttributes, forwardRef, type MouseEvent, type ReactNode } from "react";
import { Badge, type BadgeProps } from "../badge";
import { type ButtonSize, type ButtonVariant, isAriaRefusal, refuseClick } from "../button";
import buttonStyles from "../button/button.module.css";
import { requireClass } from "../internal/requireClass";
import { Tooltip } from "../tooltip";
import styles from "./iconbutton.module.css";

/** Button's variants minus link: link is underlined text, and an icon-only
 * control has no text to underline. */
export type IconButtonVariant = Exclude<ButtonVariant, "link">;

export interface IconButtonProps
  extends Omit<
    ButtonHTMLAttributes<HTMLButtonElement>,
    "type" | "onClick" | "disabled" | "children" | "className" | "aria-label"
  > {
  label: string;
  icon: ReactNode;
  variant?: IconButtonVariant;
  size?: ButtonSize;
  onClick?: (event: MouseEvent<HTMLButtonElement>) => void;
  disabled?: boolean;
  /** Why a disabled button is disabled. With disabled, the button refuses
   * through aria-disabled instead of the native attribute, so it keeps focus
   * and a keyboard user can reach the reason, which a Tooltip shows and
   * aria-describedby announces. Ignored while the button is enabled. */
  disabledReason?: string;
  type?: "button" | "submit" | "reset";
  /** A count riding the icon's top-right corner (a needs-you total). It is
   * decoration on the button: `label` stays the accessible name, so put the
   * count in `label` too when a reader needs it. */
  badge?: BadgeProps;
}

// Reuses button.module.css's own base/variant/icon classes directly
// (read-only import, not a copy) rather than rendering <Button> itself:
// Button's props don't include aria-label and its render body doesn't
// forward arbitrary DOM attributes, so composing the component can't put
// aria-label on the native <button> element. Reusing its classes gets the
// identical colors, disabled/hover states, and :focus-visible ring - the
// button directory is otherwise out of scope for this stream - with zero
// duplicated CSS (iconbutton.module.css carries only icon-button-specific
// sizing, no color).
const BASE_CLASS = {
  button: requireClass(buttonStyles.button, "button.module.css", "button"),
  icon: requireClass(buttonStyles.icon, "button.module.css", "icon"),
};

const VARIANT_CLASS: Record<IconButtonVariant, string> = {
  primary: requireClass(buttonStyles.primary, "button.module.css", "primary"),
  secondary: requireClass(buttonStyles.secondary, "button.module.css", "secondary"),
  quiet: requireClass(buttonStyles.quiet, "button.module.css", "quiet"),
  danger: requireClass(buttonStyles.danger, "button.module.css", "danger"),
  dangerQuiet: requireClass(buttonStyles.dangerQuiet, "button.module.css", "dangerQuiet"),
};

const SIZE_CLASS: Record<ButtonSize, string> = {
  xs: requireClass(styles.xs, "iconbutton.module.css", "xs"),
  sm: requireClass(styles.sm, "iconbutton.module.css", "sm"),
  md: requireClass(styles.md, "iconbutton.module.css", "md"),
};

const BADGE_CLASS = {
  badgedButton: requireClass(styles.badgedButton, "iconbutton.module.css", "badgedButton"),
  cornerBadge: requireClass(styles.cornerBadge, "iconbutton.module.css", "cornerBadge"),
};

/**
 * An icon-only Button: same variants/sizes/states, square instead of
 * padded for a text label. label is required and becomes the button's
 * only accessible name (aria-label) - there is no visible text. Forwards
 * its ref and spreads any other native button attribute onto the
 * underlying <button>, mirroring Button (see that widget's index.tsx) -
 * class-reuse between the two is CSS-only and doesn't carry this over on
 * its own.
 *
 * {...rest} is spread BEFORE type/aria-label/className/onClick/disabled
 * below, not after - same merge-order reasoning as Button (see that
 * widget's index.tsx): a later JSX attribute wins a same-key conflict,
 * and IconButtonProps' Omit<..., "aria-label"> only blocks an object
 * *literal*, not a caller spreading a more loosely-typed props object.
 * Getting this backwards previously let a stray aria-label silently
 * replace label as the button's only accessible name - see the
 * "arriving via untyped spread" test in iconbutton.test.tsx.
 */
export const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(function IconButton(
  {
    label,
    icon,
    variant = "primary",
    size = "md",
    onClick,
    disabled = false,
    disabledReason,
    type = "button",
    badge,
    ...rest
  },
  ref,
) {
  // A disabled button with a reason refuses the way an aria-disabled one does:
  // a native disabled control cannot take focus, which would leave the reason
  // reachable by mouse only.
  const reason = disabled && disabledReason ? disabledReason : undefined;
  const explained = reason !== undefined;
  // The same refusal Button makes: aria-disabled keeps the control focusable,
  // so the browser still delivers the click and the widget swallows it here.
  const refused = explained || isAriaRefusal(rest["aria-disabled"]);
  const className = `${BASE_CLASS.button} ${VARIANT_CLASS[variant]} ${SIZE_CLASS[size]}`;
  const button = (
    <button
      ref={ref}
      {...rest}
      type={type}
      aria-label={label}
      aria-disabled={explained || rest["aria-disabled"]}
      className={badge === undefined ? className : `${className} ${BADGE_CLASS.badgedButton}`}
      onClick={refused ? refuseClick : onClick}
      disabled={disabled && !explained}
    >
      <span className={BASE_CLASS.icon}>{icon}</span>
      {badge !== undefined && (
        <span className={BADGE_CLASS.cornerBadge}>
          <Badge {...badge} />
        </span>
      )}
    </button>
  );
  // A caller that passes disabledReason always gets the Tooltip wrapper, so
  // toggling the refusal never swaps the root element and remounts a focused
  // button; while enabled the tooltip is inactive (empty label).
  return disabledReason === undefined ? button : <Tooltip label={reason ?? ""}>{button}</Tooltip>;
});
