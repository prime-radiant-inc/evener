// Validates untrusted task-list and task-tool payloads and adapts the shared
// snake_case wire shape to display rows. Null is unavailable; [] is an
// authoritative empty list. Tool mutation snapshots may add started/settled.
// The insert template marker has no display consumer and is not projected.

import type { Task } from "./types.gen";
import { asJsonObject } from "./watchRows";

export type TaskStatus = "open" | "in_progress" | "done" | "cancelled";

export interface TaskCounts {
  total: number;
  done: number;
  cancelled?: number;
  remaining?: number;
}

export interface TaskRow extends Pick<Task, "id" | "type" | "description" | "prompt"> {
  status: TaskStatus;
  dependsOn?: number[];
  notes?: string[];
  reasoningEffort?: string;
  // Mutation snapshots mark whether an explicit in_progress update crossed
  // into that status. Ordinary task-list responses omit this side-channel
  // field.
  started?: boolean;
  // The terminal-status analogue of started: mutation snapshots mark
  // whether this call transitioned the task into done or cancelled (the
  // pre-call status differed). The store preserves the original CompletedAt
  // on re-assertions, so this marker distinguishes a fresh settle from an
  // annotation of an old one. Ordinary task-list responses omit it.
  settled?: boolean;
  // Wire timestamps (agent/task/task_store.go), carried as ISO strings.
  // Optional: the parser never drops a row for lacking them, and views omit
  // time displays for absent fields. created_at/updated_at are always present
  // on the real wire; completed_at exists for settled tasks (done or
  // cancelled) - the terminal transition's stamp, absent for rows persisted
  // before terminal stamping.
  createdAt?: string;
  updatedAt?: string;
  completedAt?: string;
}

// The settled count: done plus cancelled, the finished tasks that remaining
// counts the complement of. One derivation for every surface that counts
// finished tasks - the hub's Tasks chips, the inline task card's meter and
// this file's remaining fallback.
export function taskSettledCount(tasks: Pick<TaskCounts, "done" | "cancelled">): number {
  return tasks.done + (tasks.cancelled ?? 0);
}

// One condensed sentence for a task aggregate, shared by the web's inline task
// card and its tasks panel's trigger and body head: "N of M tasks left" while work
// remains, "All M tasks done" when everything finished without a
// cancellation, "All M tasks settled" when the tail is all cancellations
// ("settled" is already the panel's word for the done+cancelled group), and
// "No tasks" for an empty list. A missing remaining falls back to
// total - done - cancelled, the daemon's own computation; a missing
// cancelled reads as zero. The noun pluralizes on the total ("1 of 1 task
// left", "All 1 task done").
export function taskAggregateLabel(tasks: TaskCounts): string {
  const noun = tasks.total === 1 ? "task" : "tasks";
  if (tasks.total === 0) return "No tasks";
  const remaining = tasks.remaining ?? Math.max(0, tasks.total - taskSettledCount(tasks));
  if (remaining > 0) return `${remaining} of ${tasks.total} ${noun} left`;
  if ((tasks.cancelled ?? 0) > 0) return `All ${tasks.total} ${noun} settled`;
  return `All ${tasks.total} ${noun} done`;
}

// The closed status enum the Go store mints (agent/task/task_store.go,
// TaskStatus). A status outside it - a newer daemon's addition, or corrupt
// data - is dropped rather than cast: groupTasks routes every unmatched
// status into the settled group, where the web pane would render an
// unknown-status row as a broken glyph.
const KNOWN_STATUSES = new Set(["open", "in_progress", "done", "cancelled"]);

// A row is usable once it carries the wire's non-omitempty fields with the
// right primitive types (id/type/description/prompt/status are never
// omitted by the Go struct's own json tags, even when zero-valued) and a
// status the enum defines - anything else (a null entry, a stray string, a
// shape missing `id`, an unknown status) is dropped rather than fabricated
// or allowed to crash the whole parse.
function parseRow(raw: unknown): TaskRow | null {
  const fields = asJsonObject(raw);
  if (!fields) return null;
  const {
    id,
    type,
    description,
    prompt,
    status,
    depends_on,
    notes,
    reasoning_effort,
    started,
    settled,
    created_at,
    updated_at,
    completed_at,
  } = fields;
  if (typeof id !== "number" || typeof type !== "string" || typeof description !== "string") return null;
  if (typeof prompt !== "string" || typeof status !== "string") return null;
  if (!KNOWN_STATUSES.has(status)) return null;

  const row: TaskRow = { id, type, description, prompt, status: status as TaskStatus };
  if (Array.isArray(depends_on) && depends_on.every((d) => typeof d === "number")) {
    row.dependsOn = depends_on;
  }
  if (Array.isArray(notes) && notes.every((n) => typeof n === "string")) {
    row.notes = notes;
  }
  if (typeof reasoning_effort === "string" && reasoning_effort !== "") {
    row.reasoningEffort = reasoning_effort;
  }
  if (typeof started === "boolean") {
    row.started = started;
  }
  if (typeof settled === "boolean") {
    row.settled = settled;
  }
  if (typeof created_at === "string" && created_at !== "") row.createdAt = created_at;
  if (typeof updated_at === "string" && updated_at !== "") row.updatedAt = updated_at;
  if (typeof completed_at === "string" && completed_at !== "") row.completedAt = completed_at;
  return row;
}

export function parseTaskListData(data: unknown): TaskRow[] | null {
  if (!Array.isArray(data)) return null;
  const rows: TaskRow[] = [];
  for (const raw of data) {
    const row = parseRow(raw);
    if (row) rows.push(row);
  }
  return rows;
}
