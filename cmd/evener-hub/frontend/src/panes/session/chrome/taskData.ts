// Narrows TaskListResponse.data (protocol/types.gen.ts types it `unknown` -
// the Go source (appwire/types.go:896-898) is `Data any`, so codegen has no
// named struct to reflect) into a display-ready TaskRow[]. The real runtime
// shape - confirmed by reading the daemon handler chain rather than
// guessing, since the catalog itself says nothing - is a JSON array of
// agent/task/task_store.go's Task struct (json tags: id/type/description/
// prompt/status/depends_on/notes/reasoning_effort/insert/created_at/
// updated_at/completed_at), always non-nil-but-possibly-empty for any
// source that wires SetTasksFunc (every real evener daemon session does,
// cmd/evener/serve.go:596) and unreachable (a rejected request, not a null
// response) for a source that never supports tasks at all (codex_source.go:
// 405-407). `data` is `null`/`undefined` only when no tasksFn is registered
// server-side (server/appwire_runtime.go:713-721) - an old daemon - which
// this parser reports as `null` ("no data"), distinct from a real empty
// list (`[]`, "zero tasks").
//
// created_at/updated_at/completed_at ARE carried (as createdAt/updatedAt/
// completedAt): the 2026-08-09 panel redesign (docs/superpowers/specs/
// 2026-08-09-task-list-ui-design.md) shows per-task recency and completion
// times, which the legacy panel's field set predates; `insert` remains
// intentionally uncarried because the panel has no consumer for it.

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
  // Mutation snapshots mark whether an explicit in_progress update crossed
  // into that status. Ordinary task-list responses omit this side-channel
  // field.
  started?: boolean;
  // Wire timestamps (agent/task/task_store.go), carried as ISO strings.
  // Optional: the parser never drops a row for lacking them, and views omit
  // time displays for absent fields. created_at/updated_at are always present
  // on the real wire; completed_at exists only for done tasks.
  createdAt?: string;
  updatedAt?: string;
  completedAt?: string;
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
    created_at,
    updated_at,
    completed_at,
  } = raw;
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
  if (typeof started === "boolean") {
    row.started = started;
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
