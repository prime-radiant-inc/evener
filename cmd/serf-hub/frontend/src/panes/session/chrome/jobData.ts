// Narrows JobsListResponse.data / JobsOutputResponse.data (both `Data any`
// in appwire/types.go, so types.gen.ts types them `unknown`) into
// display-ready shapes. Wire truth: agent/jobs_panel.go's JobSummary and
// JobOutputTail structs (json tags jobId/type/status/reason/description/
// command/task/background/startedAt/endedAt/exitCode/outputBytes/hasOutput
// and tail/totalBytes/retainedStart/truncated). `data` is `null`/`undefined`
// only when no jobsFn is registered server-side (an old daemon), reported
// as `null` — distinct from a real empty list (`[]`, "zero jobs").

export type JobStatus = "running" | "completed" | "failed" | "cancelled" | "stopped" | "exhausted";

export interface JobRow {
  jobId: string;
  type: string;
  status: JobStatus;
  reason?: string;
  description: string;
  command?: string;
  task?: string;
  background: boolean;
  startedAt: string;
  endedAt?: string;
  exitCode?: number;
  outputBytes: number;
  hasOutput: boolean;
}

export interface JobOutput {
  tail: string;
  totalBytes: number;
  retainedStart: number;
  truncated: boolean;
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

// A row is usable once it carries the wire's non-omitempty fields with the
// right primitive types (jobId/type/status/description/background/
// startedAt/outputBytes/hasOutput are never omitted by the Go struct's
// json tags) — anything else is dropped rather than crashing the whole
// parse.
function parseRow(raw: unknown): JobRow | null {
  if (!isPlainObject(raw)) return null;
  const {
    jobId,
    type,
    status,
    reason,
    description,
    command,
    task,
    background,
    startedAt,
    endedAt,
    exitCode,
    outputBytes,
    hasOutput,
  } = raw;
  if (typeof jobId !== "string" || typeof type !== "string" || typeof status !== "string") return null;
  if (typeof description !== "string" || typeof background !== "boolean" || typeof startedAt !== "string") return null;
  if (typeof outputBytes !== "number" || typeof hasOutput !== "boolean") return null;

  const row: JobRow = {
    jobId,
    type,
    status: status as JobStatus,
    description,
    background,
    startedAt,
    outputBytes,
    hasOutput,
  };
  if (typeof reason === "string" && reason !== "") row.reason = reason;
  if (typeof command === "string" && command !== "") row.command = command;
  if (typeof task === "string" && task !== "") row.task = task;
  if (typeof endedAt === "string" && endedAt !== "") row.endedAt = endedAt;
  if (typeof exitCode === "number") row.exitCode = exitCode;
  return row;
}

export function parseJobListData(data: unknown): JobRow[] | null {
  if (!Array.isArray(data)) return null;
  const rows: JobRow[] = [];
  for (const raw of data) {
    const row = parseRow(raw);
    if (row) rows.push(row);
  }
  return rows;
}

export function parseJobOutputData(data: unknown): JobOutput | null {
  if (!isPlainObject(data)) return null;
  const { tail, totalBytes, retainedStart, truncated } = data;
  if (
    typeof tail !== "string" ||
    typeof totalBytes !== "number" ||
    typeof retainedStart !== "number" ||
    typeof truncated !== "boolean"
  ) {
    return null;
  }
  return { tail, totalBytes, retainedStart, truncated };
}
