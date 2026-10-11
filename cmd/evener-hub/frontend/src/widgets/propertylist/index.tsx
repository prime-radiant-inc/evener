import type { ReactNode } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./propertylist.module.css";

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
}

export type PropertyListProps =
  | {
      /** Label column beside the value; values truncate unless a row wraps. */
      layout?: "inline";
      rows: PropertyListRow[];
    }
  | {
      /** Label over value, each value wrapping on a line of its own, with
       * optional help under it: for a narrow sheet or a settings page whose
       * values are long and need explaining. */
      layout: "stacked";
      rows: StackedPropertyListRow[];
    };

const BASE_CLASS = {
  list: requireClass(styles.list, "propertylist.module.css", "list"),
  row: requireClass(styles.row, "propertylist.module.css", "row"),
  label: requireClass(styles.label, "propertylist.module.css", "label"),
  value: requireClass(styles.value, "propertylist.module.css", "value"),
  mono: requireClass(styles.mono, "propertylist.module.css", "mono"),
  wrap: requireClass(styles.wrap, "propertylist.module.css", "wrap"),
  stackedList: requireClass(styles.stackedList, "propertylist.module.css", "stackedList"),
  stackedRow: requireClass(styles.stackedRow, "propertylist.module.css", "stackedRow"),
  stackedLabel: requireClass(styles.stackedLabel, "propertylist.module.css", "stackedLabel"),
  stackedValue: requireClass(styles.stackedValue, "propertylist.module.css", "stackedValue"),
  help: requireClass(styles.help, "propertylist.module.css", "help"),
};

function classes(...names: (string | false | undefined)[]): string {
  return names.filter(Boolean).join(" ");
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
          <div className={classes(BASE_CLASS.row, BASE_CLASS.stackedRow)} key={row.label}>
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
  return (
    <dl className={BASE_CLASS.list}>
      {props.rows.map((row) => (
        <div className={BASE_CLASS.row} key={row.label}>
          <dt className={BASE_CLASS.label}>{row.label}</dt>
          <dd className={classes(BASE_CLASS.value, row.mono && BASE_CLASS.mono, row.wrap && BASE_CLASS.wrap)}>
            {row.value}
          </dd>
        </div>
      ))}
    </dl>
  );
}
