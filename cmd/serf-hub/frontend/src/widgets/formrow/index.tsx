import type { ReactNode } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./formrow.module.css";

export interface FormRowProps {
  /** Visible row label, and the accessible name of the control below it via
   * a real `<label htmlFor>` - required (not optional), same rationale as
   * Meter/Switch's own required accessible-name props: a row can't ship
   * with a dangling, unassociated label. */
  label: string;
  /** id of the control this row labels. FormRow renders `<label htmlFor>`,
   * not a wrapping `<label>` - the caller places their own control (with a
   * matching `id`) in `children`, mirroring Input/Select's own documented
   * "labeling is the consumer's job via a standard <label htmlFor>"
   * convention. Scope note: this is for controls that DON'T already carry
   * their own visible accessible name (Input, Select, Combobox, PathPicker,
   * a CollectionEditor) - Switch/RadioGroup already require their own
   * visible `label` prop and are used bare, without FormRow, so their
   * label is never rendered twice. */
  htmlFor: string;
  /** Shown below the control when there's no error. */
  help?: string;
  /** Shown below the control INSTEAD of help when present, in the row's
   * error tone (also switches the row into its error-state styling). The
   * rendered message's id is `${htmlFor}-error` (or `${htmlFor}-help` for
   * the help case) - a caller whose own control accepts free-form aria
   * attributes may wire `aria-describedby` to it; Input/Select's current
   * closed prop APIs don't accept one yet, so this is best-effort until a
   * future task threads it through them. */
  error?: string;
  children: ReactNode;
}

const CLASS = {
  root: requireClass(styles.root, "formrow.module.css", "root"),
  errorRoot: requireClass(styles.errorRoot, "formrow.module.css", "errorRoot"),
  label: requireClass(styles.label, "formrow.module.css", "label"),
  control: requireClass(styles.control, "formrow.module.css", "control"),
  help: requireClass(styles.help, "formrow.module.css", "help"),
  errorText: requireClass(styles.errorText, "formrow.module.css", "errorText"),
};

/**
 * The standard settings-field row: a label, its control, and an optional
 * help or error line below - the layout every schema-driven or hand-rolled
 * settings field in this wave builds on (see the wave-7 plan's own naming
 * of this as a shared primitive). Purely a layout+labeling wrapper - it has
 * no state of its own and never mutates `children`.
 */
export function FormRow({ label, htmlFor, help, error, children }: FormRowProps) {
  return (
    <div className={error !== undefined ? `${CLASS.root} ${CLASS.errorRoot}` : CLASS.root}>
      <label htmlFor={htmlFor} className={CLASS.label}>
        {label}
      </label>
      <div className={CLASS.control}>{children}</div>
      {error !== undefined ? (
        <p id={`${htmlFor}-error`} className={CLASS.errorText} role="alert">
          {error}
        </p>
      ) : (
        help !== undefined && (
          <p id={`${htmlFor}-help`} className={CLASS.help}>
            {help}
          </p>
        )
      )}
    </div>
  );
}
