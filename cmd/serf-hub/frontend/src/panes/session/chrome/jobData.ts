// Narrows JobsListResponse.data / JobsOutputResponse.data (both `Data any`
// in appwire/types.go, so types.gen.ts types them `unknown`) into
// display-ready shapes. Wire truth: agent/jobs_panel.go's JobSummary and
// JobOutputTail structs (json tags jobId/type/status/reason/description/
// command/task/background/startedAt/endedAt/exitCode/outputBytes/hasOutput
// and tail/totalBytes/retainedStart/truncated). `data` is `null`/`undefined`
// only when no jobsFn is registered server-side (an old daemon), reported
// as `null` — distinct from a real empty list (`[]`, "zero jobs").

// The statuses this bundle KNOWS (jobstore's Status constants, record.go:23-28)
// - deliberately not a claim about what the wire can carry. JobSummary.Status
// is a plain Go string, so a newer daemon can name a status that is not in
// here. Consumers switch over this union and must say what they do with
// everything else; JobRow.status below is what actually arrives.
export type JobStatus = "running" | "completed" | "failed" | "cancelled" | "stopped" | "exhausted";

// Which of those words mean the job is OVER - jobstore's Status.IsTerminal
// (record.go), the daemon's own "no further runtime work is expected". A
// Record over the union rather than a list of the terminal ones, so a status
// added to JobStatus is a compile error here until someone says which side it
// falls on: a new SETTLED status silently defaulting to unsettled would badge
// a finished job as live forever.
const STATUS_SETTLED: Record<JobStatus, boolean> = {
  running: false,
  completed: true,
  failed: true,
  cancelled: true,
  stopped: true,
  exhausted: true,
};

// A Map, not an object lookup: a wire status of "constructor" would answer off
// Object.prototype (JobsPanel's TONE_BY_STATUS exists for the same reason).
const SETTLED_BY_STATUS = new Map<string, boolean>(Object.entries(STATUS_SETTLED));

// Whether the wire's own word for this job says it has finished. Anything
// outside the vocabulary above is NOT settled, mirroring IsTerminal's own
// `default: false`: the terminal half of the vocabulary is the closed one, so
// a status a newer daemon invents is one this bundle has not been told is
// over. Consumers that would rather show nothing than overclaim liveness ask
// a narrower question instead - JobsPanel's jobDuration keys its ticking
// clock on "running" itself, and says why there.
export function isSettledStatus(status: string): boolean {
  return SETTLED_BY_STATUS.get(status) ?? false;
}

export interface JobRow {
  jobId: string;
  type: string;
  // The wire's status verbatim, NOT narrowed to JobStatus. Narrowing by cast
  // would tell every consumer an unrecognised status had been handled when
  // it had not; dropping the row would hide a job that really ran; remapping
  // it to a known status would misreport that job. Keeping it a string makes
  // the compiler ask each consumer what it does with a status it does not
  // recognise, which is the only one of the four that can't quietly lie.
  status: string;
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
    status,
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
