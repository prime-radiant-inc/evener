// Status grouping for the tasks panel's focus-group layout (spec:
// docs/superpowers/specs/2026-08-09-task-list-ui-design.md §Groups).
// Presentational only: a stable partition that never reorders within a
// group - the wire's id order is the session's own chronology and the panel
// has no business re-sorting it. done and cancelled share `settled`: the
// per-row glyph and strikethrough keep the distinction visible inside the
// collapsed history group.
import type { TaskRow } from "./taskData";

export interface TaskGroups {
  inProgress: TaskRow[];
  open: TaskRow[];
  settled: TaskRow[];
}

export function groupTasks(rows: TaskRow[]): TaskGroups {
  const groups: TaskGroups = { inProgress: [], open: [], settled: [] };
  for (const row of rows) {
    if (row.status === "in_progress") groups.inProgress.push(row);
    else if (row.status === "open") groups.open.push(row);
    else groups.settled.push(row);
  }
  return groups;
}
