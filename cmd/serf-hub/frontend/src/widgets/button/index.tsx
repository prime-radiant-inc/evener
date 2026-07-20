import type { MouseEvent, ReactNode } from "react";
import { requireClass } from "../internal/requireClass";
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
