import type { ReactNode } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./emptystate.module.css";

export interface EmptyStateProps {
  title: string;
  hint?: string;
  /**
   * The locked API line for this widget (see the wave-2 plan's "Locked
   * widget APIs") omits the `?` present on every other optional field
   * there (`hint?`), but a pane with nothing actionable to offer (e.g. a
   * read-only empty log) is a completely ordinary case, and every sibling
   * slot-style prop in this same wave (PaneScaffold's footer?, Card's
   * plain children) is optional - so this is read as a documentation typo
   * rather than a deliberate requirement, and kept optional.
   */
  action?: ReactNode;
  /** "display" sets the title at --font-size-display: for the one pane
   * whose empty state IS the page (Welcome). Default keeps pane-title.
   * "compact" is the empty message inside a list or section: left-aligned,
   * UI type, the list row's padding. */
  size?: "default" | "display" | "compact";
  /** Why the surface is empty, rendered as `data-reason` so callers and
   * tests can tell one empty state from another without reading its copy. */
  reason?: EmptyStateReason;
}

export type EmptyStateReason = "deleted" | "unavailable" | "no-turns";

const BASE_CLASS = {
  emptyState: requireClass(styles.emptyState, "emptystate.module.css", "emptyState"),
  compact: requireClass(styles.compact, "emptystate.module.css", "compact"),
  title: requireClass(styles.title, "emptystate.module.css", "title"),
  titleDisplay: requireClass(styles.titleDisplay, "emptystate.module.css", "titleDisplay"),
  hint: requireClass(styles.hint, "emptystate.module.css", "hint"),
  action: requireClass(styles.action, "emptystate.module.css", "action"),
};

/** Title/hint/action for a pane or list with nothing in it - centered for a
 * pane, a left-aligned line for `size="compact"`.
 * Passive - no interaction, no focus ring of its own (an `action` button
 * carries its own).
 *
 * The `empty-state` testid exists for the tests that only need to know a
 * pane's body has rendered (shell/DockHost.test.tsx waits on it before
 * reading a tab title, and again after a rename to prove the body never
 * remounted). Those assertions previously matched the session pane's own
 * empty-state copy, which coupled a shared shell test to wording that panes
 * are free to change; the testid says what they actually mean. */
export function EmptyState({ title, hint, action, size = "default", reason }: EmptyStateProps) {
  return (
    <div
      className={size === "compact" ? `${BASE_CLASS.emptyState} ${BASE_CLASS.compact}` : BASE_CLASS.emptyState}
      data-testid="empty-state"
      data-reason={reason}
    >
      <p className={size === "display" ? `${BASE_CLASS.title} ${BASE_CLASS.titleDisplay}` : BASE_CLASS.title}>
        {title}
      </p>
      {hint !== undefined && (
        <p className={BASE_CLASS.hint} data-slot="hint">
          {hint}
        </p>
      )}
      {action !== undefined && <div className={BASE_CLASS.action}>{action}</div>}
    </div>
  );
}
