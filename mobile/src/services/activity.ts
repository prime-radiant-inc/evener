// ActivityService — projects a wire Thread's diagnostics/tasks/jobs/usage into
// ActivityView view models the ActivitySheet consumes. Pure: given the same
// Thread it produces the same ActivityView. No DOM, no network, no clock.
//
// Raw identifiers and payloads live ONLY behind a diagnostics disclosure. The
// RedactedDiagnostic type carries only metadata-allowlist fields: timestamps,
// redacted profile IDs, operation names, status classes, byte counts,
// connection generations, opaque error identifiers — never URL queries, auth
// headers, bodies, transcript text, filenames, attachment bytes, speech text,
// or provider payloads.

import type {
  EvenerDelegateInfo,
  EvenerDiagnostics,
  EvenerJobInfo,
  EvenerUsage,
  Thread,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { MobileCapabilities, MobileUsage } from "../conversation/model";

// --- view model types --------------------------------------------------------

export type TaskGroupStatus = "active" | "open" | "done";

export interface TaskGroup {
  readonly status: TaskGroupStatus;
  readonly count: number;
}

// The tone of a work entry row, used for visual treatment and accessibility.
// "running" while in progress, "failed" when the wire carried an error / nonzero
// exit, "terminal" when settled cleanly, "idle" when waiting but not running,
// "unknown" for forward-compatible unrecognized statuses.
export type WorkTone = "running" | "failed" | "terminal" | "idle" | "unknown";

export type WorkKind = "delegate" | "job" | "watch";

export interface RedactedDiagnostic {
  // Stable identifier for citation inside diagnostics disclosure only.
  readonly rawId: string;
  // Operation / type name (e.g. "shell", "subagent").
  readonly operationName: string;
  // Raw status class string (forward-compatible — unknown values preserved).
  readonly statusClass: string;
  // Byte counts (output, never payload content).
  readonly outputBytes?: number;
  // Timestamps (ISO strings from the wire).
  readonly startedAt?: string;
  readonly endedAt?: string;
  // Duration in milliseconds.
  readonly durationMs?: number;
  // Redacted profile ID — never the raw value.
  readonly profileId?: "redacted";
  // Opaque error identifier class, not the message text.
  readonly exitCode?: number;
}

export interface WorkEntry {
  readonly kind: WorkKind;
  readonly label: string;
  readonly tone: WorkTone;
  readonly durationMs?: number;
  // Human-readable output size summary (e.g. "2.0 KB").
  readonly outputSummary?: string;
  // Nested child entries (jobs under delegates).
  readonly children?: WorkEntry[];
  // Raw-identifier metadata — only shown inside diagnostics disclosure.
  readonly diagnostics?: RedactedDiagnostic;
}

export interface UsageSummary {
  readonly inputTokens?: number;
  readonly outputTokens?: number;
  readonly cacheReadTokens?: number;
  readonly totalTokens?: number;
  readonly cost?: string;
  readonly contextUsed?: number;
  readonly contextWindow?: number;
  readonly contextRemaining?: number;
  readonly contextPressure?: number;
  readonly durationMs?: number;
}

export interface ActivityView {
  readonly tasks: TaskGroup[];
  readonly work: WorkEntry[];
  readonly usage: UsageSummary;
  readonly capabilities: MobileCapabilities;
  readonly reasoningEffort?: string;
  readonly reasoningEffortLevels?: string[];
  readonly supportsReasoning?: boolean;
}

export interface ActivityService {
  // Project thread diagnostics/tasks/jobs/usage into activity view models.
  projectActivity(thread: Thread): ActivityView;
}

// --- tone classification -----------------------------------------------------

const RUNNING_STATUSES = new Set([
  "running",
  "in_progress",
  "inProgress",
  "active",
]);
const IDLE_STATUSES = new Set(["idle", "waiting", "paused"]);
const TERMINAL_STATUSES = new Set([
  "completed",
  "done",
  "finished",
  "succeeded",
  "success",
]);
const FAILED_STATUSES = new Set([
  "failed",
  "error",
  "errored",
  "cancelled",
  "canceled",
  "exhausted",
  "stopped",
]);

function classifyTone(
  status: string,
  terminal: boolean | undefined,
  exitCode: number | undefined,
  outcome: string | undefined,
): WorkTone {
  // A nonzero exit code is always failed, regardless of status string.
  if (exitCode !== undefined && exitCode !== 0) return "failed";
  // An explicit failed outcome on a terminal delegate is failed.
  if (terminal && outcome !== undefined && FAILED_STATUSES.has(outcome)) {
    return "failed";
  }
  if (FAILED_STATUSES.has(status)) return "failed";
  if (RUNNING_STATUSES.has(status)) return "running";
  if (IDLE_STATUSES.has(status)) return "idle";
  if (terminal || TERMINAL_STATUSES.has(status)) return "terminal";
  return "unknown";
}

// --- output size formatting --------------------------------------------------

function formatOutputBytes(bytes: number): string {
  if (bytes === 0) return "0 B";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) {
    const kb = bytes / 1024;
    return `${kb < 10 ? kb.toFixed(1) : Math.round(kb)} KB`;
  }
  const mb = bytes / (1024 * 1024);
  return `${mb < 10 ? mb.toFixed(1) : Math.round(mb)} MB`;
}

// --- redacted diagnostics ----------------------------------------------------

function redactJob(job: EvenerJobInfo): RedactedDiagnostic {
  return {
    rawId: job.jobId,
    operationName: job.jobType,
    statusClass: job.status,
    outputBytes: job.outputBytes,
    exitCode: job.exitCode,
  };
}

function redactDelegate(dlg: EvenerDelegateInfo): RedactedDiagnostic {
  return {
    rawId: dlg.delegateId,
    operationName: dlg.type,
    statusClass: dlg.status,
    startedAt: dlg.runStartedAt,
    endedAt: dlg.runEndedAt,
    durationMs: dlg.durationMs,
    // Profile ID is redacted to a class marker — never the raw value.
    profileId: dlg.resolvedProfileId !== undefined ? "redacted" : undefined,
  };
}

// --- work projection ---------------------------------------------------------

function projectJobEntry(job: EvenerJobInfo): WorkEntry {
  const tone = classifyTone(job.status, undefined, job.exitCode, undefined);
  const kind: WorkKind = job.fromWatch === true ? "watch" : "job";
  const label = job.jobType;
  return {
    kind,
    label,
    tone,
    outputSummary: formatOutputBytes(job.outputBytes),
    diagnostics: redactJob(job),
  };
}

function projectDelegateEntry(
  dlg: EvenerDelegateInfo,
  childJobs: EvenerJobInfo[],
): WorkEntry {
  const tone = classifyTone(dlg.status, dlg.terminal, undefined, dlg.outcome);
  // Use the type (operation name) only, never description/task prompt — those
  // may carry delegated task text. Fall back to a safe constant when type is
  // absent or empty.
  const label = dlg.type && dlg.type.length > 0 ? dlg.type : "Delegate";
  const children = childJobs.map((cj) => projectJobEntry(cj));
  return {
    kind: "delegate",
    label,
    tone,
    durationMs: dlg.durationMs,
    children: children.length > 0 ? children : undefined,
    diagnostics: redactDelegate(dlg),
  };
}

function projectWork(diagnostics: EvenerDiagnostics | undefined): WorkEntry[] {
  if (diagnostics === undefined) return [];

  const delegates = diagnostics.delegates ?? [];
  const jobs = diagnostics.jobs ?? [];

  // Partition jobs: those with a parentDelegateId nest under that delegate;
  // the rest are top-level entries.
  const byParent = new Map<string, EvenerJobInfo[]>();
  const topLevelJobs: EvenerJobInfo[] = [];
  for (const job of jobs) {
    const parent = job.parentDelegateId;
    if (parent !== undefined && parent !== "") {
      const list = byParent.get(parent);
      if (list !== undefined) list.push(job);
      else byParent.set(parent, [job]);
    } else {
      topLevelJobs.push(job);
    }
  }

  const entries: WorkEntry[] = [];
  for (const dlg of delegates) {
    const childJobs = byParent.get(dlg.delegateId) ?? [];
    entries.push(projectDelegateEntry(dlg, childJobs));
  }
  for (const job of topLevelJobs) {
    entries.push(projectJobEntry(job));
  }

  return entries;
}

// --- task projection ---------------------------------------------------------

function projectTasks(
  tasks: { total: number; done: number } | undefined,
): TaskGroup[] {
  if (tasks === undefined) return [];
  const open = Math.max(0, tasks.total - tasks.done);
  return [
    { status: "active", count: 0 },
    { status: "open", count: open },
    { status: "done", count: tasks.done },
  ];
}

// --- usage projection --------------------------------------------------------

function projectUsage(evener: Thread["evener"]): UsageSummary {
  const usage: EvenerUsage | undefined = evener.usage;
  return {
    inputTokens: usage?.inputTokens,
    outputTokens: usage?.outputTokens,
    cacheReadTokens: usage?.cacheReadTokens,
    totalTokens: usage?.totalTokens,
    cost: evener.cost,
    contextUsed: evener.contextUsed,
    contextWindow: evener.contextWindow,
    contextRemaining: evener.contextRemaining,
    contextPressure: evener.contextPressure,
    durationMs: evener.workMillis,
  };
}

// --- capabilities projection -------------------------------------------------

function projectCapabilities(
  caps: Thread["evener"]["capabilities"],
): MobileCapabilities {
  return { ...caps };
}

// --- top-level projection ----------------------------------------------------

export function createActivityService(): ActivityService {
  return {
    projectActivity(thread: Thread): ActivityView {
      const evener = thread.evener;
      return {
        tasks: projectTasks(evener.tasks),
        work: projectWork(evener.diagnostics),
        usage: projectUsage(evener),
        capabilities: projectCapabilities(evener.capabilities),
        reasoningEffort: evener.reasoningEffort,
        reasoningEffortLevels: evener.reasoningEffortLevels,
        supportsReasoning: evener.supportsReasoning,
      };
    },
  };
}

// Re-export MobileUsage for convenience; UsageSummary is a superset shape.
export type { MobileUsage };
