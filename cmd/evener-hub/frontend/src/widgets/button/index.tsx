import { type ButtonHTMLAttributes, forwardRef, type MouseEvent, type ReactNode } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./button.module.css";

/** secondary is the design language's bordered common control (2026-05-22
 * design language §3.1's .btn-secondary): a resting border and surface fill,
 * for a standing action that is not the surface's one primary - e.g. the
 * welcome pane's "New session" beside a primary "Jump back in". quiet stays
 * for chromeless text-level actions.
 * dangerQuiet is a destructive action that is not the primary one on its row
 * (the composer's Stop beside Send): danger on the glyph/label, no filled
 * background.
 * link is an inline text action - a task title in the composer's current-work
 * line, a Retry beside an error - in accent ink with an underline. It takes
 * its font from the text around it, so the size ladder does not apply. */
export type ButtonVariant = "primary" | "secondary" | "quiet" | "danger" | "dangerQuiet" | "link";
/** xs is the density step for a control row INSIDE another control - the
 * prompt card's verb cluster, where the buttons annotate a text field rather
 * than standing alone as a form's action. */
export type ButtonSize = "xs" | "sm" | "md";

export interface ButtonProps
  extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, "type" | "onClick" | "disabled" | "children" | "className"> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  icon?: ReactNode;
  children: ReactNode;
  onClick?: (event: MouseEvent<HTMLButtonElement>) => void;
  disabled?: boolean;
  type?: "button" | "submit" | "reset";
  /** Let a long label break across lines instead of holding one line: for a
   * label that can be the widest thing in its column (the welcome pane's
   * "Jump back in: <session title>"). The size's height becomes a floor.
   * Ignored by variant="link", which already wraps with its prose. */
  wrap?: boolean;
}

const BASE_CLASS = {
  button: requireClass(styles.button, "button.module.css", "button"),
  icon: requireClass(styles.icon, "button.module.css", "icon"),
  wrap: requireClass(styles.wrap, "button.module.css", "wrap"),
};

const VARIANT_CLASS: Record<ButtonVariant, string> = {
  primary: requireClass(styles.primary, "button.module.css", "primary"),
  secondary: requireClass(styles.secondary, "button.module.css", "secondary"),
  quiet: requireClass(styles.quiet, "button.module.css", "quiet"),
  danger: requireClass(styles.danger, "button.module.css", "danger"),
  dangerQuiet: requireClass(styles.dangerQuiet, "button.module.css", "dangerQuiet"),
  link: requireClass(styles.link, "button.module.css", "link"),
};

const SIZE_CLASS: Record<ButtonSize, string> = {
  xs: requireClass(styles.xs, "button.module.css", "xs"),
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
/** Whether an aria-disabled value is a refusal. JSX accepts both spellings of
 * the attribute, so both count. Shared with IconButton, which refuses the same
 * way. */
export function isAriaRefusal(ariaDisabled: ButtonHTMLAttributes<HTMLButtonElement>["aria-disabled"]): boolean {
  return ariaDisabled === true || ariaDisabled === "true";
}

/** The click handler a refused control gets: besides skipping the caller's
 * onClick, it cancels the browser's own default, so a refused submit or reset
 * button never submits or resets its form. */
export function refuseClick(event: MouseEvent<HTMLButtonElement>): void {
  event.preventDefault();
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  {
    variant = "primary",
    size = "md",
    icon,
    children,
    onClick,
    disabled = false,
    type = "button",
    wrap = false,
    ...rest
  },
  ref,
) {
  // aria-disabled is a refusal, not a rendering hint: the control stays
  // focusable (that is why it is not the native attribute - see the rest-spread
  // docs above and button.module.css), so the browser still hands it the click.
  // Refusing that click HERE is what makes a caller's guarded handler
  // defense-in-depth rather than the only thing standing between a refused
  // control and the action it refuses.
  const refused = isAriaRefusal(rest["aria-disabled"]);
  return (
    <button
      ref={ref}
      {...rest}
      type={type}
      className={
        variant === "link"
          ? `${BASE_CLASS.button} ${VARIANT_CLASS.link}`
          : [BASE_CLASS.button, VARIANT_CLASS[variant], SIZE_CLASS[size], wrap && BASE_CLASS.wrap]
              .filter(Boolean)
              .join(" ")
      }
      onClick={refused ? refuseClick : onClick}
      disabled={disabled}
    >
      {icon !== undefined && <span className={BASE_CLASS.icon}>{icon}</span>}
      {children}
    </button>
  );
});
