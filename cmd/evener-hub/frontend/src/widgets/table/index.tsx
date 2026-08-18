// Ported from Beautiful UI (https://www.beautifului.dev, MIT (c) 2026 Shane
// Levine; see LICENSES/beautiful-ui.txt) - adapts their Records Table and
// Filter Table showcases into a single generic, controlled widget.
import type { ReactNode } from "react";
import { Chip } from "../chip";
import { requireClass } from "../internal/requireClass";
import styles from "./table.module.css";

export interface TableColumn<Row> {
  key: string;
  label: string;
  sortable?: boolean;
  render?: (row: Row) => ReactNode;
}

export interface TableFilter {
  key: string;
  label: string;
  active: boolean;
}

export type TableSortDir = "ascending" | "descending";

export interface TableProps<Row> {
  columns: TableColumn<Row>[];
  rows: Row[];
  rowKey: (row: Row) => string;
  sortKey?: string;
  sortDir?: TableSortDir;
  onSortChange?: (key: string) => void;
  filters?: TableFilter[];
  onFilterToggle?: (key: string) => void;
  empty?: ReactNode;
}

const CLASS = {
  root: requireClass(styles.root, "table.module.css", "root"),
  filters: requireClass(styles.filters, "table.module.css", "filters"),
  filterButton: requireClass(styles.filterButton, "table.module.css", "filterButton"),
  scroll: requireClass(styles.scroll, "table.module.css", "scroll"),
  table: requireClass(styles.table, "table.module.css", "table"),
  headerCell: requireClass(styles.headerCell, "table.module.css", "headerCell"),
  sortButton: requireClass(styles.sortButton, "table.module.css", "sortButton"),
  sortIcon: requireClass(styles.sortIcon, "table.module.css", "sortIcon"),
  row: requireClass(styles.row, "table.module.css", "row"),
  cell: requireClass(styles.cell, "table.module.css", "cell"),
  empty: requireClass(styles.empty, "table.module.css", "empty"),
};

function SortIcon({ dir }: { dir: TableSortDir }) {
  // A single chevron path, flipped by dir - ascending points up (smallest
  // first, reading up the column), descending points down.
  return (
    <svg
      className={CLASS.sortIcon}
      viewBox="0 0 12 12"
      width="10"
      height="10"
      aria-hidden="true"
      style={{ transform: dir === "descending" ? "rotate(180deg)" : undefined }}
    >
      <path
        d="M6 2 L6 10 M2.5 5.5 L6 2 L9.5 5.5"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.4"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

/** Renders a data cell's content: the column's render() if given, else the
 * row's own field at column.key, stringified. Row is an opaque generic
 * here (the locked API's exact shape) so this can only reach the field by
 * an untyped index - render() is the typed escape hatch for anything more
 * than "print the field". */
function cellContent<Row>(column: TableColumn<Row>, row: Row): ReactNode {
  if (column.render) return column.render(row);
  const value = (row as unknown as Record<string, unknown>)[column.key];
  return value === undefined || value === null ? "" : String(value);
}

/**
 * A generic, controlled data table: sort and filter state live in the
 * caller, this only renders the current columns/rows/sort/filters and
 * reports interactions back via callbacks. Adapts Beautiful UI's Records
 * Table (sortable header buttons, hover rows) and Filter Table (chip row
 * above the table) into one widget.
 */
export function Table<Row>({
  columns,
  rows,
  rowKey,
  sortKey,
  sortDir = "ascending",
  onSortChange,
  filters,
  onFilterToggle,
  empty,
}: TableProps<Row>) {
  const showEmpty = rows.length === 0 && empty !== undefined;

  return (
    <div className={CLASS.root}>
      {filters !== undefined && filters.length > 0 && (
        <div className={CLASS.filters}>
          {filters.map((filter) => (
            <button
              key={filter.key}
              type="button"
              aria-pressed={filter.active}
              className={CLASS.filterButton}
              onClick={() => onFilterToggle?.(filter.key)}
            >
              <Chip tone={filter.active ? "alive" : "neutral"}>{filter.label}</Chip>
            </button>
          ))}
        </div>
      )}
      <div className={CLASS.scroll}>
        <table className={CLASS.table}>
          <thead>
            <tr>
              {columns.map((column) => {
                const isActive = column.key === sortKey;
                const ariaSort = column.sortable ? (isActive ? sortDir : "none") : undefined;
                return (
                  <th key={column.key} scope="col" className={CLASS.headerCell} aria-sort={ariaSort}>
                    {column.sortable ? (
                      <button type="button" className={CLASS.sortButton} onClick={() => onSortChange?.(column.key)}>
                        {column.label}
                        {isActive && <SortIcon dir={sortDir} />}
                      </button>
                    ) : (
                      column.label
                    )}
                  </th>
                );
              })}
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={rowKey(row)} className={CLASS.row}>
                {columns.map((column) => (
                  <td key={column.key} className={CLASS.cell}>
                    {cellContent(column, row)}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
        {showEmpty && <div className={CLASS.empty}>{empty}</div>}
      </div>
    </div>
  );
}
