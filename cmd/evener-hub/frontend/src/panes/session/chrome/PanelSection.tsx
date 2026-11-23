import type { ComponentPropsWithoutRef, ReactNode } from "react";
import { Eyebrow } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./panelsection.module.css";

const SECTION_CLASS = requireClass(styles.section, "panelsection.module.css", "section");

export interface PanelSectionProps
  extends Omit<ComponentPropsWithoutRef<"section">, "title" | "className" | "children"> {
  /** The group's name; a node when the heading carries its own count. */
  title: ReactNode;
  headingAs?: "h3" | "h4";
  children: ReactNode;
}

/** A titled group on the session panels' shared rhythm: an Eyebrow heading,
 * the content, and one rule dividing it from the next group. The last section
 * drops the rule. */
export function PanelSection({ title, headingAs = "h3", children, ...hooks }: PanelSectionProps) {
  return (
    <section className={SECTION_CLASS} {...hooks}>
      <Eyebrow as={headingAs}>{title}</Eyebrow>
      {children}
    </section>
  );
}
