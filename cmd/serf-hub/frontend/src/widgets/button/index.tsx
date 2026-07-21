import { forwardRef, type ButtonHTMLAttributes, type MouseEvent, type ReactNode } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./button.module.css";

export type ButtonVariant = "primary" | "quiet" | "danger";
export type ButtonSize = "sm" | "md";

export interface ButtonProps
  extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, "type" | "onClick" | "disabled" | "children" | "className"> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  icon?: ReactNode;
  children: ReactNode;
  onClick?: (event: MouseEvent<HTMLButtonElement>) => void;
  disabled?: boolean;
  type?: "button" | "submit" | "reset";
}

const BASE_CLASS = {
  button: requireClass(styles.button, "button.module.css", "button"),
  icon: requireClass(styles.icon, "button.module.css", "icon"),
};

const VARIANT_CLASS: Record<ButtonVariant, string> = {
  primary: requireClass(styles.primary, "button.module.css", "primary"),
  quiet: requireClass(styles.quiet, "button.module.css", "quiet"),
  danger: requireClass(styles.danger, "button.module.css", "danger"),
};

const SIZE_CLASS: Record<ButtonSize, string> = {
  sm: requireClass(styles.sm, "button.module.css", "sm"),
  md: requireClass(styles.md, "button.module.css", "md"),
};

/**
 * Forwards its ref and spreads any other native button attribute
 * (aria-*, data-*, id, name, ...) straight onto the underlying <button> -
 * variant/size/icon/children/onClick/disabled/type stay Button's own
 * explicitly-typed, explicitly-controlled props (className is not
 * accepted at all: Button computes it). This is what lets composition -
 * Tooltip's cloneElement aria-describedby wiring, a ref for imperative
 * focus - reach the real DOM node; before this, both were silently
 * dropped since Button rendered from a fixed prop list with no rest
 * spread. See tooltip.test.tsx for the cross-widget integration proof.
 *
 * {...rest} is spread BEFORE type/className/onClick/disabled below, not
 * after: a later JSX attribute always wins a same-key conflict, so this
 * ordering is what actually makes those props caller-non-overridable.
 * ButtonProps' Omit<..., "className"> only blocks an object *literal*
 * like <Button className="x"> - it does not catch a caller spreading a
 * more loosely-typed props object (e.g. from its own wrapper component),
 * which is exactly the case TypeScript's excess-property check does not
 * cover. Getting this order backwards previously let such a className
 * silently replace Button's variant/size/focus-ring classes - see the
 * "arriving via untyped spread" test in button.test.tsx.
 */
export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = "primary", size = "md", icon, children, onClick, disabled = false, type = "button", ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      {...rest}
      type={type}
      className={`${BASE_CLASS.button} ${VARIANT_CLASS[variant]} ${SIZE_CLASS[size]}`}
      onClick={onClick}
      disabled={disabled}
    >
      {icon !== undefined && <span className={BASE_CLASS.icon}>{icon}</span>}
      {children}
    </button>
  );
});
