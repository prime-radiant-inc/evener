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
      className={`${styles.button} ${styles[variant]} ${styles[size]}`}
      onClick={onClick}
      disabled={disabled}
    >
      {icon !== undefined && <span className={styles.icon}>{icon}</span>}
      {children}
    </button>
  );
}
