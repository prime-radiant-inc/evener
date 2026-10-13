import type { ReactNode } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./eyebrow.module.css";

export interface EyebrowProps {
  /** "h3" when the label heads a section a reader navigates by heading;
   * "span" when it sits inline beside other text. */
  as?: "div" | "span" | "h3";
  children: ReactNode;
}

const BASE_CLASS = {
  eyebrow: requireClass(styles.eyebrow, "eyebrow.module.css", "eyebrow"),
};

/** The one eyebrow recipe (design-system §2): a short uppercase caption
 * label that titles a group INSIDE a page - never a pane title, never more
 * than two words. The copy stays sentence-case in the source; the
 * uppercase is presentation only. */
export function Eyebrow({ as: Element = "div", children }: EyebrowProps) {
  return <Element className={BASE_CLASS.eyebrow}>{children}</Element>;
}
