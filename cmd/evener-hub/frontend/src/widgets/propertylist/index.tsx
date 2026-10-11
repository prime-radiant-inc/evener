import type { ReactNode } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./propertylist.module.css";

export interface PropertyListRow {
  /** Unique within the list; it is also the row's key. */
  label: string;
  value: ReactNode;
  /** Set the value in mono: versions, sources, paths, identifiers. */
  mono?: boolean;
}

export interface PropertyListProps {
  rows: PropertyListRow[];
}

const BASE_CLASS = {
  list: requireClass(styles.list, "propertylist.module.css", "list"),
  row: requireClass(styles.row, "propertylist.module.css", "row"),
  label: requireClass(styles.label, "propertylist.module.css", "label"),
  value: requireClass(styles.value, "propertylist.module.css", "value"),
  mono: requireClass(styles.mono, "propertylist.module.css", "mono"),
};

/** Read-only facts as label/value rows: the meta table idiom
 * (design-system §10). A semantic <dl>; each <div> groups one dt/dd pair,
 * which the HTML content model allows inside a <dl>. */
export function PropertyList({ rows }: PropertyListProps) {
  return (
    <dl className={BASE_CLASS.list}>
      {rows.map((row) => (
        <div className={BASE_CLASS.row} key={row.label}>
          <dt className={BASE_CLASS.label}>{row.label}</dt>
          <dd className={row.mono ? `${BASE_CLASS.value} ${BASE_CLASS.mono}` : BASE_CLASS.value}>{row.value}</dd>
        </div>
      ))}
    </dl>
  );
}
