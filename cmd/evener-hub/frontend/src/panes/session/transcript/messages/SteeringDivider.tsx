// The quiet, collapsed-by-default divider shared by daemon steering and the
// automatic memory refresh: a Disclosure whose summary is the steering glyph,
// a label, and optional trailing qualifiers; the body is either the verbatim steered text in a
// <pre> (steering's default - never re-rendered as markdown) or a caller's own
// formatted node (a memory refresh's decoded index). Open/closed state lives in
// the shared disclosureStore keyed by session ref plus item id, so an expanded
// divider survives a remount without colliding with another session's item.
//
// Extracted from SteeringItem so the memory renderer can reuse the interaction
// and styling rather than duplicate them; steering's own call sites pass only
// id/label/text/sessionRef and keep their exact prior rendering.

import { type ReactNode, useMemo } from "react";
import { Disclosure, SteeringGlyph } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { itemScopeKey } from "../tools/subagentModuleStore";
import styles from "./steeringitem.module.css";

const CLASS = {
  railIcon: requireClass(styles.railIcon, "steeringitem.module.css", "railIcon"),
  label: requireClass(styles.label, "steeringitem.module.css", "label"),
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
  /** Extra trailing summary content, after the label. */
  qualifiers?: ReactNode;
  /** Keep this disclosure closed at every verbosity, including Full, while an
   * explicit reader choice still wins (a memory refresh's own posture). */
  ignoreBaseline?: boolean;
  /** The disclosure's data-testid; steering keeps "steering-item". */
  testId?: string;
  /** The label span's data-testid, when a caller needs to pin the exact label. */
  labelTestId?: string;
  /** What the label names (a steer's labelled kind, "parent"), as the label
   * span's data-kind. */
  kind?: string;
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
  kind,
}: SteeringDividerProps) {
  const disclosureKey = useMemo(() => itemScopeKey(sessionRef, id), [sessionRef, id]);
  return (
    <Disclosure
      variant="quiet"
      id={disclosureKey}
      ignoreBaseline={ignoreBaseline}
      data-testid={testId}
      summary={
        <>
          <span className={CLASS.railIcon} data-testid="steering-rail-icon" aria-hidden="true">
            <SteeringGlyph />
          </span>
          <span className={CLASS.label} data-testid={labelTestId} data-kind={kind}>
            {label}
          </span>
          {qualifiers}
        </>
      }
    >
      {body ?? <pre className={CLASS.body}>{text}</pre>}
    </Disclosure>
  );
}
