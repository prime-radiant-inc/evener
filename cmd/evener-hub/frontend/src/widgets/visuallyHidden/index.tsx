import type { ReactNode } from "react";
import styles from "./visuallyHidden.module.css";

export function VisuallyHidden({ children }: { children: ReactNode }) {
  return <span className={styles.root}>{children}</span>;
}
