import type { ReactNode } from "react";
import { Chevron } from "../chevron";
import { Eyebrow } from "../eyebrow";
import { requireClass } from "../internal/requireClass";
import styles from "./disclosureheading.module.css";

export interface DisclosureHeadingProps {
  level: 2 | 3 | 4 | 5 | 6;
  label: string;
  open: boolean;
  onOpenChange(open: boolean): void;
  /** Sets the label as an Eyebrow group label; leave off for an authored
   * name that keeps its own casing. */
  eyebrow?: boolean;
  /** A control beside the heading (a section's "⋯" menu). Rendered outside
   * the heading and its button. */
  action?: ReactNode;
}

const CLASS = {
  row: requireClass(styles.disclosureHeading, "disclosureheading.module.css", "disclosureHeading"),
  heading: requireClass(styles.headingTitle, "disclosureheading.module.css", "headingTitle"),
  toggle: requireClass(styles.headingToggle, "disclosureheading.module.css", "headingToggle"),
  action: requireClass(styles.headingAction, "disclosureheading.module.css", "headingAction"),
};

/**
 * A heading that folds the group below it: an `<hN>` wrapping an
 * `aria-expanded` button with a trailing chevron, plus an optional action
 * beside it. The caller renders the group itself (only while `open`).
 *
 * Disclosure is `<details>/<summary>`, which cannot be this widget: a
 * heading inside a summary loses its heading role to the summary's button
 * role, and an action control may not live inside a summary. Keeping the
 * heading outermost keeps heading navigation working, and the heading's
 * name comes from the button's text (the chevron is aria-hidden).
 */
export function DisclosureHeading({
  level,
  label,
  open,
  onOpenChange,
  eyebrow = false,
  action,
}: DisclosureHeadingProps) {
  const Heading = `h${level}` as const;
  return (
    <div className={CLASS.row}>
      <Heading className={CLASS.heading}>
        <button type="button" className={CLASS.toggle} aria-expanded={open} onClick={() => onOpenChange(!open)}>
          {eyebrow ? <Eyebrow as="span">{label}</Eyebrow> : label}
          <Chevron direction={open ? "down" : "right"} />
        </button>
      </Heading>
      {action !== undefined && <div className={CLASS.action}>{action}</div>}
    </div>
  );
}
