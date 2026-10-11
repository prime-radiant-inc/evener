import { type ReactNode, useId } from "react";
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
   * their own visible accessible name (Input, Select, PathField, a
   * CollectionEditor) - Switch/RadioGroup already require their own
   * visible `label` prop and are used bare, without FormRow, so their
   * label is never rendered twice.
   *
   * Omit it for group mode: a row whose children are several controls, or a
   * composite with no single labelable element (ModelCatalog, a
   * CollectionEditor). The row is then `role="group"`, named by the label
   * and described by the help or error line. */
  htmlFor?: string;
  /** Shown below the control when there's no error. */
  help?: string;
  /** Shown below the control INSTEAD of help when present, in the row's
   * error tone (also switches the row into its error-state styling). With
   * `htmlFor`, the rendered message's id is `${htmlFor}-error` (or
   * `${htmlFor}-help` for the help case) - a caller whose own control
   * accepts free-form aria attributes may wire `aria-describedby` to it. In
   * group mode the row wires its own group's aria-describedby instead. */
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
  const groupId = useId();
  const idBase = htmlFor ?? groupId;
  const helpId = `${idBase}-help`;
  const errorId = `${idBase}-error`;
  const labelId = `${idBase}-label`;
  const className = error !== undefined ? `${CLASS.root} ${CLASS.errorRoot}` : CLASS.root;

  const message =
    error !== undefined ? (
      <p id={errorId} className={CLASS.errorText} role="alert">
        {error}
      </p>
    ) : (
      help !== undefined && (
        <p id={helpId} className={CLASS.help}>
          {help}
        </p>
      )
    );

  if (htmlFor === undefined) {
    const describedBy = error !== undefined ? errorId : help !== undefined ? helpId : undefined;
    return (
      // A <fieldset>/<legend> pair would carry the same semantics, but a
      // rendered legend sits outside the row's flex layout (it ignores the
      // gap and is placed in the fieldset's border), so the label could not
      // line up with the htmlFor rows above and below it.
      // biome-ignore lint/a11y/useSemanticElements: role="group" is deliberate, see above
      <div role="group" aria-labelledby={labelId} aria-describedby={describedBy} className={className}>
        <span id={labelId} className={CLASS.label}>
          {label}
        </span>
        <div className={CLASS.control}>{children}</div>
        {message}
      </div>
    );
  }

  return (
    <div className={className}>
      <label htmlFor={htmlFor} className={CLASS.label}>
        {label}
      </label>
      <div className={CLASS.control}>{children}</div>
      {message}
    </div>
  );
}
