import { forwardRef, type ButtonHTMLAttributes, type MouseEvent, type ReactNode } from "react";
import { requireClass } from "../internal/requireClass";
import type { ButtonVariant, ButtonSize } from "../button";
import buttonStyles from "../button/button.module.css";
import styles from "./iconbutton.module.css";

export interface IconButtonProps
  extends Omit<
    ButtonHTMLAttributes<HTMLButtonElement>,
    "type" | "onClick" | "disabled" | "children" | "className" | "aria-label"
  > {
  label: string;
  icon: ReactNode;
  variant?: ButtonVariant;
  size?: ButtonSize;
  onClick?: (event: MouseEvent<HTMLButtonElement>) => void;
  disabled?: boolean;
  type?: "button" | "submit" | "reset";
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

const VARIANT_CLASS: Record<ButtonVariant, string> = {
  primary: requireClass(buttonStyles.primary, "button.module.css", "primary"),
  quiet: requireClass(buttonStyles.quiet, "button.module.css", "quiet"),
  danger: requireClass(buttonStyles.danger, "button.module.css", "danger"),
};

const SIZE_CLASS: Record<ButtonSize, string> = {
  sm: requireClass(styles.sm, "iconbutton.module.css", "sm"),
  md: requireClass(styles.md, "iconbutton.module.css", "md"),
};

/**
 * An icon-only Button: same variants/sizes/states, square instead of
 * padded for a text label. label is required and becomes the button's
 * only accessible name (aria-label) - there is no visible text. Forwards
 * its ref and spreads any other native button attribute onto the
 * underlying <button>, mirroring Button (see that widget's index.tsx) -
 * class-reuse between the two is CSS-only and doesn't carry this over on
 * its own.
 */
export const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(function IconButton(
  { label, icon, variant = "primary", size = "md", onClick, disabled = false, type = "button", ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      type={type}
      aria-label={label}
      className={`${BASE_CLASS.button} ${VARIANT_CLASS[variant]} ${SIZE_CLASS[size]}`}
      onClick={onClick}
      disabled={disabled}
      {...rest}
    >
      <span className={BASE_CLASS.icon}>{icon}</span>
    </button>
  );
});
