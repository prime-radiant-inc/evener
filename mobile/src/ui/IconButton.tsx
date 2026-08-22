import type { ButtonHTMLAttributes, JSX, ReactNode } from "react";

export interface IconButtonProps
  extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, "type"> {
  readonly "aria-label": string;
  readonly children: ReactNode;
}

/**
 * Icon-only button with a guaranteed 44px tap target and an accessible name
 * (no icon-as-label). Used for TopBar back/close and row trailing actions.
 */
export function IconButton({
  className,
  children,
  ...rest
}: IconButtonProps): JSX.Element {
  const cls = `evener-icon-button${className ? ` ${className}` : ""}`;
  return (
    <button type="button" className={cls.trim()} {...rest}>
      {children}
    </button>
  );
}
