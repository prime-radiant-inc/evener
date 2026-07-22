// Narrows TaskListResponse.data (protocol/types.gen.ts types it `unknown` -
// the Go source (appwire/types.go:896-898) is `Data any`, so codegen has no
// named struct to reflect) into a display-ready TaskRow[]. The real runtime
// shape - confirmed by reading the daemon handler chain rather than
// guessing, since the catalog itself says nothing - is a JSON array of
// agent/task/task_store.go's Task struct (json tags: id/type/description/
// prompt/status/depends_on/notes/reasoning_effort/insert/created_at/
// updated_at/completed_at), always non-nil-but-possibly-empty for any
// source that wires SetTasksFunc (every real serf daemon session does,
// cmd/serf/serve.go:596) and unreachable (a rejected request, not a null
// response) for a source that never supports tasks at all (codex_source.go:
// 405-407). `data` is `null`/`undefined` only when no tasksFn is registered
// server-side (server/appwire_runtime.go:713-721) - an old daemon - which
// this parser reports as `null` ("no data"), distinct from a real empty
// list (`[]`, "zero tasks").
//
// insert/created_at/updated_at/completed_at are intentionally not carried
// into TaskRow: the legacy tasks panel (cmd/serf-hub/assets/renderer-panels.js,
// buildTaskDetailList) never displays them, only type/status/depends_on/
// reasoning_effort/prompt/notes alongside id/description.

export type TaskStatus = "open" | "in_progress" | "done" | "cancelled";

export interface TaskRow {
  id: number;
  type: string;
  description: string;
  prompt: string;
  status: TaskStatus;
  dependsOn?: number[];
  notes?: string[];
  reasoningEffort?: string;
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

// A row is usable once it carries the wire's non-omitempty fields with the
// right primitive types (id/type/description/prompt/status are never
// omitted by the Go struct's own json tags, even when zero-valued) -
// anything else (a null entry, a stray string, a shape missing `id`) is
// dropped rather than fabricated or allowed to crash the whole parse.
function parseRow(raw: unknown): TaskRow | null {
  if (!isPlainObject(raw)) return null;
  const { id, type, description, prompt, status, depends_on, notes, reasoning_effort } = raw;
  if (typeof id !== "number" || typeof type !== "string" || typeof description !== "string") return null;
  if (typeof prompt !== "string" || typeof status !== "string") return null;

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
