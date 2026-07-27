import type { ReactNode } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./promptcard.module.css";

export interface PromptCardProps {
  /** The field itself - a seamless autoGrow Textarea. The card draws the one
   * border and owns the :focus-within ring, so the field inside must draw
   * neither (see widgets/textarea's `seamless`). */
  field: ReactNode;
  /** Leading control-row slot, before the spacer - the attach button. */
  leading?: ReactNode;
  /** Trailing control-row slot, after the spacer - the primary verb and
   * whatever sits beside it. */
  actions?: ReactNode;
  /** Optional caller styling for the control row. Spawn uses this to pin the
   * existing controls into its mobile action band without changing the shared
   * composer layout. */
  controlsClassName?: string;
  /** Optional stable hook for a caller-owned control-row behavior test. */
  controlsTestId?: string;
  /** Forwarded to the card element so a test can address it by a stable hook
   * (the composer's `composer-input-card`, spawn's `spawn-prompt-card`). */
  "data-testid"?: string;
  /** Hides the card AND takes it out of the interaction tree, for a surface
   * that temporarily owns the input instead (the composer while an ask_user
   * question is pending - see panes/session/composer's AskDock). */
  hidden?: boolean;
}

const CLASS = {
  card: requireClass(styles.card, "promptcard.module.css", "card"),
  controls: requireClass(styles.controls, "promptcard.module.css", "controls"),
  actions: requireClass(styles.actions, "promptcard.module.css", "actions"),
};

/**
 * The app's one prompt surface: a bordered card holding a text field over a
 * control row. Both places a person writes a prompt to an agent - the session
 * composer and the spawn form - render THIS, so "message the agent" and "start
 * an agent" are the same object rather than two things that resemble each
 * other.
 *
 * The card owns the border, the radius, the padding, the control-row layout,
 * and the focus ring (via :focus-within, since the seamless field inside draws
 * none of its own). Callers own the field's props and their own buttons - this
 * widget has no opinion about verbs, and no state at all.
 */
export function PromptCard({
  field,
  leading,
  actions,
  hidden,
  controlsClassName,
  controlsTestId,
  "data-testid": testId,
}: PromptCardProps) {
  return (
    <div className={CLASS.card} data-testid={testId} hidden={hidden} inert={hidden}>
      {field}
      {(leading !== undefined || actions !== undefined) && (
        <div className={`${CLASS.controls} ${controlsClassName ?? ""}`} data-testid={controlsTestId}>
          {leading}
          <div className={CLASS.actions}>{actions}</div>
        </div>
      )}
    </div>
  );
}
