// Status grouping for a task list's focus-group layout (spec:
// docs/superpowers/specs/2026-08-09-task-list-ui-design.md §Groups).
// Presentational only: a stable partition that never reorders within a
// group - the wire's id order is the session's own chronology and the view
// has no business re-sorting it. done and cancelled share `settled`: the
// per-row glyph and strikethrough keep the distinction visible inside the
// collapsed history group. A status this client does not recognize is
// remaining work, never settled history: the daemon's aggregate counts every
// non-done, non-cancelled task as remaining, and the row carries its raw
// status word so the category is not mistaken for `open` (issue #2256).
// Shared by the web tasks panel and native's tasks sheet.
import type { TaskRow } from "./taskListData";

export interface TaskGroups {
  inProgress: TaskRow[];
  open: TaskRow[];
  settled: TaskRow[];
}

export function groupTasks(rows: TaskRow[]): TaskGroups {
  const groups: TaskGroups = { inProgress: [], open: [], settled: [] };
  for (const row of rows) {
    if (row.status === "in_progress") groups.inProgress.push(row);
    else if (row.status === "done" || row.status === "cancelled") groups.settled.push(row);
    else groups.open.push(row);
  }
  return groups;
}
