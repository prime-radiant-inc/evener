// VisuallyHidden: text for assistive tech only - the accessible name of an
// icon-only control, or a live-region announcement with no visible twin.
// Use it instead of a local `.srOnly` recipe.
import type { ReactNode } from "react";
import styles from "./visuallyhidden.module.css";

export interface VisuallyHiddenProps {
  children: ReactNode;
}

export function VisuallyHidden({ children }: VisuallyHiddenProps) {
  return <span className={styles.visuallyHidden}>{children}</span>;
}
