// Interprets a task_list tool result's authoritative task snapshot, carried
// end to end as item.raw: agent/session_tools_task.go's task_list executor
// returns tool.StateResult{State: store.View()} on every view/append/update
// call; agent/internal/tool/registry.go's ExecuteCall JSON-marshals State
// straight into ToolState (no wrapping key, no rename - the wire array IS
// item.raw); internal/appprojector and internal/apptranscript both carry it
// onto ThreadItem.raw unchanged; protocol/reducer.ts's wireItemToModel keeps
// it as item.raw verbatim. The shape is therefore the same
// agent/task/task_store.go Task[] the tasks side panel already parses from a
// different wire path (TaskListResponse.data) - chrome/taskData.ts's
// parseTaskListData is reused rather than reimplemented.
//
// Absent/malformed raw - an old daemon predating StateResult.State, or a
// transcript replayed from before it existed - parses to null, same
// contract as parseTaskListData itself: "we don't know the state", never
// "zero tasks". Callers must degrade to argument-only rendering in that
// case, never an empty checklist or a fabricated auto-start.
import { parseTaskListData, type TaskRow } from "../../chrome/taskData";

export type { TaskRow };

export function parseTaskState(raw: unknown): TaskRow[] | null {
  return parseTaskListData(raw);
}

// The label an update row shows for a task: its description when the
// authoritative state names one (mirrors the legacy card's
// buildTaskRowLine, which preferred task.description over a bare id -
// cmd/serf-hub/assets/renderer-format.js), falling back to "#<id>" when
// state is absent or the id isn't found there.
export function taskLabel(tasks: TaskRow[] | null, id: number | undefined): string {
  if (id === undefined) return "(task)";
  const description = tasks?.find((t) => t.id === id)?.description;
  return description || `#${id}`;
}

// The task the daemon auto-started as a side effect of THIS update call -
// the "and now working on X" row docs/superpowers/plans/2026-07-15-inline-
// task-update-cards.md required keeping ("authoritative auto-activation").
// agent/session_tools_task.go auto-advances (store.NextEligible + an
// in_progress transition) exactly when the batch completed something (a
// done or cancelled row - completedAny, matching that Go gate's own name)
// and did not ALSO explicitly start a task itself; either way that decision
// already happened server-side and rides in the SAME State this call
// returns, so it's found rather than re-derived here: the one in_progress
// task the caller's own rows didn't already name. touchedIds is every id
// the caller's own updates already earned a row for (including an explicit
// in_progress one), so an explicit start is never double-counted -
// task_store.go's Update enforces a single in_progress task store-wide, so
// at most one candidate can ever match.
export function autoStartedTask(
  tasks: TaskRow[] | null,
  touchedIds: ReadonlySet<number>,
  completedAny: boolean,
): TaskRow | undefined {
  if (!tasks || !completedAny) return undefined;
  return tasks.find((t) => t.status === "in_progress" && !touchedIds.has(t.id));
}
