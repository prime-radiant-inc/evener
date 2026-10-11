// The quiet, collapsed-by-default divider shared by daemon steering and the
// automatic memory refresh. Summary is a rail glyph, a label, optional trailing
// qualifiers, and a chevron; the body is either the verbatim steered text in a
// <pre> (steering's default - never re-rendered as markdown) or a caller's own
// formatted node (a memory refresh's decoded index). Open/closed state lives in
// the shared disclosureStore keyed by session ref plus item id, so an expanded
// divider survives a remount without colliding with another session's item.
//
// Extracted from SteeringItem so the memory renderer can reuse the interaction
// and styling rather than duplicate them; steering's own call sites pass only
// id/label/text/sessionRef and keep their exact prior rendering.

import { type ReactNode, useMemo } from "react";
import { Chevron, SteeringGlyph } from "../../../../widgets";
import { isDisclosureOpen, toggleDisclosure } from "../../../../widgets/disclosure/disclosureStore";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { itemScopeKey } from "../tools/subagentModuleStore";
import styles from "./steeringitem.module.css";

const CLASS = {
  details: requireClass(styles.details, "steeringitem.module.css", "details"),
  summary: requireClass(styles.summary, "steeringitem.module.css", "summary"),
  railIcon: requireClass(styles.railIcon, "steeringitem.module.css", "railIcon"),
  label: requireClass(styles.label, "steeringitem.module.css", "label"),
  chevron: requireClass(styles.chevron, "steeringitem.module.css", "chevron"),
  body: requireClass(styles.body, "steeringitem.module.css", "body"),
};

export interface SteeringDividerProps {
  id: string;
  label: string;
  /** The verbatim body, rendered in a <pre> when `body` is absent. */
  text?: string;
  sessionRef?: string;
  /** A caller-supplied formatted body; replaces the default <pre>. */
  body?: ReactNode;
  /** Extra trailing summary content, before the chevron. */
  qualifiers?: ReactNode;
  /** Keep this disclosure closed at every verbosity, including Full, while an
   * explicit reader choice still wins (a memory refresh's own posture). */
  ignoreBaseline?: boolean;
  /** The details element's data-testid; steering keeps "steering-item". */
  testId?: string;
  /** The label span's data-testid, when a caller needs to pin the exact label. */
  labelTestId?: string;
}

export function SteeringDivider({
  id,
  label,
  text,
  sessionRef,
  body,
  qualifiers,
  ignoreBaseline = false,
  testId = "steering-item",
  labelTestId,
}: SteeringDividerProps) {
  const disclosureKey = useMemo(() => itemScopeKey(sessionRef, id), [sessionRef, id]);
  const options = useMemo(() => (ignoreBaseline ? { ignoreBaseline: true } : undefined), [ignoreBaseline]);
  const open = isDisclosureOpen(disclosureKey, false, options);
  return (
    <details className={CLASS.details} data-testid={testId} open={open}>
      {/* biome-ignore lint/a11y/noStaticElementInteractions: <summary> is natively keyboard-operable; controlled to keep the store the single source of truth (see ToolCallItem.tsx) */}
      <summary
        className={CLASS.summary}
        onClick={(e) => {
          e.preventDefault();
          toggleDisclosure(disclosureKey, false);
        }}
      >
        <span className={CLASS.railIcon} data-testid="steering-rail-icon" aria-hidden="true">
          <SteeringGlyph />
        </span>
        <span className={CLASS.label} data-testid={labelTestId}>
          {label}
        </span>
        {qualifiers}
        <span
          className={CLASS.chevron}
          aria-hidden="true"
          data-open={open ? "true" : "false"}
          data-testid="steering-chevron"
        >
          <Chevron />
        </span>
      </summary>
      {body ?? <pre className={CLASS.body}>{text}</pre>}
    </details>
  );
}
