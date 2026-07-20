import type { MouseEvent, ReactNode } from "react";
import styles from "./button.module.css";

export type ButtonVariant = "primary" | "quiet" | "danger";
export type ButtonSize = "sm" | "md";

export interface ButtonProps {
  variant?: ButtonVariant;
  size?: ButtonSize;
  icon?: ReactNode;
  children: ReactNode;
  onClick?: (event: MouseEvent<HTMLButtonElement>) => void;
  disabled?: boolean;
  type?: "button" | "submit" | "reset";
}

// CSS Modules import as an index signature (`{ [key: string]: string }`),
// so under this project's noUncheckedIndexedAccess every styles.foo access
// is typed string | undefined — TypeScript can't know the module actually
// has a "foo" class. requireClass turns a missing class into a loud,
// immediate module-load error instead of a silently-wrong className.
function requireClass(value: string | undefined, name: string): string {
  if (value === undefined) throw new Error(`button.module.css is missing the "${name}" class`);
  return value;
}

const BASE_CLASS = {
  button: requireClass(styles.button, "button"),
  icon: requireClass(styles.icon, "icon"),
};

const VARIANT_CLASS: Record<ButtonVariant, string> = {
  primary: requireClass(styles.primary, "primary"),
  quiet: requireClass(styles.quiet, "quiet"),
  danger: requireClass(styles.danger, "danger"),
};

const SIZE_CLASS: Record<ButtonSize, string> = {
  sm: requireClass(styles.sm, "sm"),
  md: requireClass(styles.md, "md"),
};

export function Button({
  variant = "primary",
  size = "md",
  icon,
  children,
  onClick,
  disabled = false,
  type = "button",
}: ButtonProps) {
  return (
    <button
      type={type}
      className={`${BASE_CLASS.button} ${VARIANT_CLASS[variant]} ${SIZE_CLASS[size]}`}
      onClick={onClick}
      disabled={disabled}
    >
      {icon !== undefined && <span className={BASE_CLASS.icon}>{icon}</span>}
      {children}
    </button>
  );
}
