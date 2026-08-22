import type { ButtonHTMLAttributes, JSX, ReactNode } from "react";

export type ButtonVariant = "primary" | "secondary" | "tertiary" | "danger";

export interface ButtonProps
  extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, "type"> {
  readonly variant?: ButtonVariant;
  readonly children: ReactNode;
}

/**
 * Focused mobile button. 44px minimum via the `--tap-target` token. Variants
 * carry visual prominence without hover dependence: `primary` (accent fill),
 * `secondary` (hairline outline), `tertiary` (quiet), `danger`.
 */
export function Button({
  variant = "primary",
  className,
  children,
  disabled,
  ...rest
}: ButtonProps): JSX.Element {
  const cls = `evener-button ${variant}${className ? ` ${className}` : ""}`;
  return (
    <button type="button" className={cls.trim()} disabled={disabled} {...rest}>
      {children}
    </button>
  );
}
