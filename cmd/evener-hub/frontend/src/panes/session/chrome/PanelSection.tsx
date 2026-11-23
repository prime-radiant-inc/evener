import type { ComponentPropsWithoutRef, ReactNode } from "react";
import { Eyebrow, type EyebrowProps } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./panelsection.module.css";

const CLASS = {
  section: requireClass(styles.section, "panelsection.module.css", "section"),
  heading: requireClass(styles.heading, "panelsection.module.css", "heading"),
};

export interface PanelSectionProps
  extends Omit<ComponentPropsWithoutRef<"section">, "title" | "className" | "children"> {
  /** The group's name; a node when the heading carries its own count. */
  title: ReactNode;
  headingAs?: EyebrowProps["as"];
  action?: ReactNode;
  children: ReactNode;
}

/** A titled group on the session panels' shared rhythm: an Eyebrow heading,
 * the content, and one rule dividing it from the next group. The last section
 * drops the rule. */
export function PanelSection({ title, headingAs = "h3", action, children, ...hooks }: PanelSectionProps) {
  return (
    <section className={CLASS.section} {...hooks}>
      <div className={CLASS.heading}>
        <Eyebrow as={headingAs}>{title}</Eyebrow>
        {action}
      </div>
      {children}
    </section>
  );
}
