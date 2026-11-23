import type { ReactNode } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./propertylist.module.css";

/** Extra attributes for a row's wrapper: the stable hook a caller or test
 * uses to find a row without reading its label (data-field, data-testid, id). */
export type PropertyListRowProps = Record<`data-${string}`, string> & { id?: string };

export interface PropertyListRow {
  /** Unique within the list; it is also the row's key. */
  label: string;
  value: ReactNode;
  /** Set the value in mono: versions, sources, paths, identifiers. */
  mono?: boolean;
  /** Wrap the value in full instead of truncating it: for a value the reader
   * is confirming before acting on it (a deploy plan's target, a session's
   * context card), where an ellipsis would hide the part that matters. */
  wrap?: boolean;
  /** Set the value in the secondary ink: a fact that qualifies the row's
   * primary value rather than standing on its own (a delegate's isolation). */
  muted?: boolean;
  /** Render the label as a real <label> for this control id. */
  htmlFor?: string;
  /** A sentence under the value explaining what the fact is or how to change
   * it. */
  help?: ReactNode;
  /** A refusal or state that follows the help line: value, then help, then
   * the notice. */
  notice?: ReactNode;
  /** A trailing action cell, for a row whose local control acts on the fact. */
  actions?: ReactNode;
  /** Extra attributes for the row's wrapper. */
  rowProps?: PropertyListRowProps;
}

export interface StackedPropertyListRow {
  /** Unique within the list; it is also the row's key. */
  label: string;
  value: ReactNode;
  /** Set the value in mono: versions, sources, paths, identifiers. */
  mono?: boolean;
  /** A sentence under the value explaining what the fact is or how to change
   * it. */
  help?: ReactNode;
  /** Extra attributes for the row's wrapper. */
  rowProps?: PropertyListRowProps;
}

export type PropertyListProps =
  | {
      /** Label column beside the value; values truncate unless a row wraps. */
      layout?: "inline";
      /** Label each row with the eyebrow recipe (design-system §2): the
       * uppercase-caption idiom a card's read-only fact rows carry. */
      eyebrowLabels?: boolean;
      rows: PropertyListRow[];
    }
  | {
      /** Label over value, each value wrapping on a line of its own, with
       * optional help under it: for a narrow sheet or a settings page whose
       * values are long and need explaining. */
      layout: "stacked";
      rows: StackedPropertyListRow[];
    }
  | {
      /** Label and control on the row's first line, with optional help under
       * the control: the connection editor's two-column settings grid. */
      layout: "settings";
      rows: PropertyListRow[];
    };

const BASE_CLASS = {
  list: requireClass(styles.list, "propertylist.module.css", "list"),
  row: requireClass(styles.row, "propertylist.module.css", "row"),
  rowWithActions: requireClass(styles.rowWithActions, "propertylist.module.css", "rowWithActions"),
  label: requireClass(styles.label, "propertylist.module.css", "label"),
  eyebrowLabel: requireClass(styles.eyebrowLabel, "propertylist.module.css", "eyebrowLabel"),
  value: requireClass(styles.value, "propertylist.module.css", "value"),
  mono: requireClass(styles.mono, "propertylist.module.css", "mono"),
  muted: requireClass(styles.muted, "propertylist.module.css", "muted"),
  wrap: requireClass(styles.wrap, "propertylist.module.css", "wrap"),
  stackedList: requireClass(styles.stackedList, "propertylist.module.css", "stackedList"),
  stackedRow: requireClass(styles.stackedRow, "propertylist.module.css", "stackedRow"),
  stackedLabel: requireClass(styles.stackedLabel, "propertylist.module.css", "stackedLabel"),
  stackedValue: requireClass(styles.stackedValue, "propertylist.module.css", "stackedValue"),
  help: requireClass(styles.help, "propertylist.module.css", "help"),
  valueHelp: requireClass(styles.valueHelp, "propertylist.module.css", "valueHelp"),
  actions: requireClass(styles.actions, "propertylist.module.css", "actions"),
  controlRow: requireClass(styles.controlRow, "propertylist.module.css", "controlRow"),
  controlLabel: requireClass(styles.controlLabel, "propertylist.module.css", "controlLabel"),
  settingsList: requireClass(styles.settingsList, "propertylist.module.css", "settingsList"),
  settingsRow: requireClass(styles.settingsRow, "propertylist.module.css", "settingsRow"),
  settingsLabel: requireClass(styles.settingsLabel, "propertylist.module.css", "settingsLabel"),
  settingsValue: requireClass(styles.settingsValue, "propertylist.module.css", "settingsValue"),
};

function classes(...names: (string | false | undefined)[]): string {
  return names.filter(Boolean).join(" ");
}

/** Matches FormRow's documented help-id scheme (widgets/formrow/index.tsx):
 * a field's help is `${htmlFor}-help`. */
function helpIdFor(htmlFor: string | undefined): string | undefined {
  return htmlFor === undefined ? undefined : `${htmlFor}-help`;
}

/** Read-only facts as label/value rows: the meta table idiom
 * (design-system §10). A semantic <dl>; each <div> groups one dt with its
 * dd(s), which the HTML content model allows inside a <dl>. A stacked row's
 * help is a second dd of the same term, so it is read with the fact it
 * explains. */
export function PropertyList(props: PropertyListProps) {
  if (props.layout === "stacked") {
    return (
      <dl className={classes(BASE_CLASS.list, BASE_CLASS.stackedList)}>
        {props.rows.map((row) => (
          <div {...row.rowProps} className={classes(BASE_CLASS.row, BASE_CLASS.stackedRow)} key={row.label}>
            <dt className={classes(BASE_CLASS.label, BASE_CLASS.stackedLabel)}>{row.label}</dt>
            <dd className={classes(BASE_CLASS.value, BASE_CLASS.stackedValue, row.mono && BASE_CLASS.mono)}>
              {row.value}
            </dd>
            {row.help !== undefined && <dd className={BASE_CLASS.help}>{row.help}</dd>}
          </div>
        ))}
      </dl>
    );
  }
  if (props.layout === "settings") {
    return (
      <div className={classes(BASE_CLASS.list, BASE_CLASS.settingsList)}>
        {props.rows.map((row) => {
          const helpId = helpIdFor(row.htmlFor);
          return (
            <div {...row.rowProps} className={BASE_CLASS.settingsRow} key={row.label}>
              {row.htmlFor !== undefined ? (
                <label className={BASE_CLASS.settingsLabel} htmlFor={row.htmlFor}>
                  {row.label}
                </label>
              ) : (
                <span className={BASE_CLASS.settingsLabel}>{row.label}</span>
              )}
              <span className={BASE_CLASS.settingsValue}>
                {row.value}
                {row.help !== undefined && (
                  <span className={classes(BASE_CLASS.help, BASE_CLASS.valueHelp)} id={helpId}>
                    {row.help}
                  </span>
                )}
              </span>
            </div>
          );
        })}
      </div>
    );
  }
  return (
    <dl className={BASE_CLASS.list}>
      {props.rows.map((row) => {
        // An editable fact stacks below its label on a phone instead of
        // sharing the row with the fixed label column; a read-only fact keeps
        // the compact side-by-side layout.
        const controlRow = row.htmlFor !== undefined && row.actions === undefined;
        return (
          <div
            {...row.rowProps}
            className={classes(
              BASE_CLASS.row,
              row.actions !== undefined && BASE_CLASS.rowWithActions,
              controlRow && BASE_CLASS.controlRow,
            )}
            key={row.label}
          >
            <dt
              className={classes(
                BASE_CLASS.label,
                props.eyebrowLabels && BASE_CLASS.eyebrowLabel,
                controlRow && BASE_CLASS.controlLabel,
              )}
            >
              {row.htmlFor !== undefined ? <label htmlFor={row.htmlFor}>{row.label}</label> : row.label}
            </dt>
            <dd
              className={classes(
                BASE_CLASS.value,
                row.mono && BASE_CLASS.mono,
                row.muted && BASE_CLASS.muted,
                (row.wrap || controlRow) && BASE_CLASS.wrap,
              )}
            >
              {row.value}
              {row.help !== undefined && (
                <span className={classes(BASE_CLASS.help, BASE_CLASS.valueHelp)} id={helpIdFor(row.htmlFor)}>
                  {row.help}
                </span>
              )}
              {row.notice}
            </dd>
            {row.actions !== undefined && <dd className={BASE_CLASS.actions}>{row.actions}</dd>}
          </div>
        );
      })}
    </dl>
  );
}
