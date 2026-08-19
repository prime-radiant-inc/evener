// Ported from Beautiful UI (https://www.beautifului.dev, MIT (c) 2026 Shane
// Levine; see LICENSES/beautiful-ui.txt) - adapts their DiffTable showcase
// (AI-proposed tabular edits) into a generic widget.
import { requireClass } from "../internal/requireClass";
import styles from "./difftable.module.css";

export interface DiffTableColumn {
  key: string;
  label: string;
}

export interface DiffTableCell {
  value: string;
  proposed?: string;
}

export interface DiffTableRow {
  key: string;
  cells: Record<string, DiffTableCell>;
}

export interface DiffTableProps {
  columns: DiffTableColumn[];
  rows: DiffTableRow[];
}

const CLASS = {
  scroll: requireClass(styles.scroll, "difftable.module.css", "scroll"),
  table: requireClass(styles.table, "difftable.module.css", "table"),
  headerCell: requireClass(styles.headerCell, "difftable.module.css", "headerCell"),
  row: requireClass(styles.row, "difftable.module.css", "row"),
  cell: requireClass(styles.cell, "difftable.module.css", "cell"),
  old: requireClass(styles.old, "difftable.module.css", "old"),
  proposed: requireClass(styles.proposed, "difftable.module.css", "proposed"),
};

function DiffCell({ cell }: { cell: DiffTableCell | undefined }) {
  if (cell === undefined) return null;
  if (cell.proposed === undefined) return <>{cell.value}</>;
  return (
    <span className={CLASS.proposed}>
      <span className={CLASS.old}>{cell.value}</span>
      <span>{cell.proposed}</span>
    </span>
  );
}

/**
 * A read-only table for AI-proposed tabular edits: a cell with `proposed`
 * shows its old value struck through in --ink-low beside the new value on
 * the neutral --diff-add-bg wash. Not on token-contract's semantic-use
 * allowlist, so it stays on the neutral diff washes + ink scale only, the
 * same exemption DiffBlock uses for the same reason (structural diff
 * notation, not app status). Shares Table's header/row chrome declarations
 * (surface-inset header band, hover rows) duplicated here rather than via
 * `composes:` - the two widgets have no shared CSS module to compose from.
 */
export function DiffTable({ columns, rows }: DiffTableProps) {
  return (
    <div className={CLASS.scroll}>
      <table className={CLASS.table}>
        <thead>
          <tr>
            {columns.map((column) => (
              <th key={column.key} scope="col" className={CLASS.headerCell}>
                {column.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={row.key} className={CLASS.row}>
              {columns.map((column) => (
                <td key={column.key} className={CLASS.cell}>
                  <DiffCell cell={row.cells[column.key]} />
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
