// Delegate timing and model derivations both apps' delegate details render
// from. The wire carries a delegate in two shapes - the activity tree's
// ActivityDelegate and the thread snapshot's EvenerDelegateInfo - with the same
// timing and model fields, so each helper takes only the fields it reads and
// either shape satisfies it.
//
// Timing trusts the run window only for a terminal delegate: a resumed run can
// carry the previous run's runEndedAt and outcome, so a non-terminal delegate
// always measures live from runStartedAt. The quiet anchor is the newer of
// latestActivityAt and runStartedAt, and exists only when the daemon sent
// quiet evidence of its own (a latestActivityAt or a quietForMs): a snapshot
// with runStartedAt alone gets no quiet age, not a number the daemon declined
// to compute. Every live measurement falls back to the snapshot's frozen value
// only when no anchor parses or the clock is not a finite number.
import type { ActivityDelegate } from "./activityData";
import { firstLine } from "./displayFormat";
import { nonblank } from "./toolCallText";

export type DelegateTimingFields = Pick<
  ActivityDelegate,
  "runStartedAt" | "runEndedAt" | "latestActivityAt" | "runningForMs" | "quietForMs" | "durationMs" | "terminal"
>;

export type DelegateModelFields = Pick<
  ActivityDelegate,
  "resolvedModel" | "model" | "requestedModel" | "reasoningEffort"
>;

export interface DelegateTiming {
  startedAt?: string;
  endedAt?: string;
  durationMs?: number;
  quietForMs?: number;
  durationLive: boolean;
  quietLive: boolean;
  terminal: boolean;
}

function timestamp(value: string | undefined): number | undefined {
  if (value === undefined) return undefined;
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) ? parsed : undefined;
}

function snapshotDuration(value: number | null | undefined): number | undefined {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0 ? value : undefined;
}

function elapsed(now: number, then: number): number | undefined {
  if (!Number.isFinite(now)) return undefined;
  return Math.min(Number.MAX_SAFE_INTEGER, Math.max(0, now - then));
}

export function delegateTiming(delegate: DelegateTimingFields, now: number): DelegateTiming {
  const started = timestamp(delegate.runStartedAt);
  const ended = timestamp(delegate.runEndedAt);
  const terminal = delegate.terminal === true;
  const result: DelegateTiming = {
    durationLive: false,
    quietLive: false,
    terminal,
  };
  if (started !== undefined) result.startedAt = delegate.runStartedAt;
  if (terminal && ended !== undefined) result.endedAt = delegate.runEndedAt;

  if (terminal) {
    if (started !== undefined && ended !== undefined && ended >= started) result.durationMs = ended - started;

    if (result.durationMs === undefined) result.durationMs = snapshotDuration(delegate.durationMs);
    return result;
  }

  if (started !== undefined) {
    result.durationMs = elapsed(now, started);
    if (result.durationMs !== undefined) result.durationLive = true;
    else result.durationMs = snapshotDuration(delegate.runningForMs);
  } else {
    result.durationMs = snapshotDuration(delegate.runningForMs);
  }

  const latest = timestamp(delegate.latestActivityAt);
  const hasQuietEvidence = delegate.latestActivityAt !== undefined || delegate.quietForMs != null;
  const quietAnchor = !hasQuietEvidence
    ? undefined
    : latest !== undefined && started !== undefined
      ? Math.max(latest, started)
      : (latest ?? started);
  if (quietAnchor !== undefined) {
    result.quietForMs = elapsed(now, quietAnchor);
    if (result.quietForMs !== undefined) result.quietLive = true;
    else result.quietForMs = snapshotDuration(delegate.quietForMs);
  } else {
    result.quietForMs = snapshotDuration(delegate.quietForMs);
  }
  return result;
}

export function delegateModel(delegate: DelegateModelFields): {
  model?: string;
  requestedModel?: string;
  reasoning?: string;
} {
  const resolvedModel = nonblank(delegate.resolvedModel);
  const model = nonblank(delegate.model);
  const requestedModel = nonblank(delegate.requestedModel);
  const selected = resolvedModel ?? model ?? requestedModel;
  const reasoning = nonblank(delegate.reasoningEffort);
  return {
    ...(selected ? { model: selected } : {}),
    ...(requestedModel !== undefined && requestedModel !== selected ? { requestedModel } : {}),
    ...(reasoning !== undefined ? { reasoning } : {}),
  };
}

export function delegatePacket(
  value: unknown,
  structured = false,
): { text: string; format: "markdown" | "json" } | undefined {
  if (typeof value === "undefined") return undefined;
  if (typeof value === "string" && !structured) return { text: value, format: "markdown" };
  try {
    const text = JSON.stringify(value, null, 2);
    return text === undefined ? undefined : { text, format: "json" };
  } catch {
    return undefined;
  }
}

// The daemon's reason codes (agent/delegate_*.go, subagents.go,
// session_budget.go) said plainly. A reason is a code; a failed run's cause
// in words rides beside it as `error` (#3327). delegateDetails.test.ts reads
// the Go source and fails when a code there has no words here; the TUI's
// transcript.SubagentEndingText mirrors this table, and its test holds the two
// alike.
const ENDING_WORDS = new Map([
  // A journal written by an older daemon, before failures named their kind.
  ["failed", "failed"],
  // A run that failed on an error; its cause rides in `error`.
  ["run_error", "failed with an error"],
  // A run that ended with neither an error nor a report.
  ["ended_without_report", "ended without reporting"],
  ["terminal_error", "ended with an error"],
  ["missing_terminal", "ended without reporting"],
  ["runtime_lost", "runtime lost"],
  ["input_persist_failed", "couldn't save its input"],
  ["cancelled", "cancelled"],
  ["stopped_by_parent", "stopped by its coordinator"],
  ["tool_round_budget_exhausted", "ran out of tool rounds"],
  ["turn_budget_exhausted", "ran out of turns"],
  ["launch_failed", "couldn't start"],
  ["construction_failed", "couldn't be set up"],
  ["artifacts_dir_failed", "couldn't create its artifacts folder"],
  ["input_admission_failed", "couldn't take its input"],
  ["attention_consumed_without_report", "finished without a new report"],
]);

// The word for an outcome whose reason is a code this client doesn't know.
const OUTCOME_WORDS = new Map([
  ["failed", "failed"],
  ["exhausted", "ran out of budget"],
  ["cancelled", "stopped"],
  ["stopped", "stopped"],
]);

const CODE = /^[a-z0-9]+(?:_[a-z0-9]+)+$/;

export interface DelegateEndingFields {
  outcome?: string;
  reason?: string;
  error?: string;
}

// How long an ending may run: one line on every surface that shows it.
const ENDING_MAX = 120;

/** How a subagent's last run ended, in words, or undefined when there is
 * nothing to say: the hub's `error` when it sent one, else the reason code
 * said plainly. A code this client doesn't know reads as its outcome's word,
 * so a snake_case code never reaches the screen; a reason already in words
 * (an older hub's) shows as it is. Always one line, bounded, so every
 * consumer shows the same. */
export function delegateEndingText(delegate: DelegateEndingFields): string | undefined {
  const error = firstLine(delegate.error ?? "", ENDING_MAX);
  if (error) return error;
  const reason = delegate.reason?.trim();
  if (!reason) return undefined;
  const words = ENDING_WORDS.get(reason);
  if (words) return words;
  if (!CODE.test(reason)) return firstLine(reason, ENDING_MAX) || undefined;
  return OUTCOME_WORDS.get(delegate.outcome ?? "");
}

// The codes a delegate's resumability closes with that the run-ending
// vocabulary doesn't carry: the eight restore-input codes
// (agent/delegate_runtime.go's notResumable* constants, returned by
// missingDelegateRestoreInputReason) and worktree disposal
// (agent/session_tools_worktree_dispose.go). The other closure codes -
// construction_failed, launch_failed, artifacts_dir_failed,
// input_admission_failed, turn_budget_exhausted - are run-ending reasons too,
// so delegateEndingText already has their words.
const NOT_RESUMABLE_WORDS = new Map([
  ["missing_delegate_resume_metadata", "its resume metadata is missing"],
  ["parent_linkage_unavailable", "its parent linkage is unavailable"],
  ["missing_child_session_meta", "its session metadata is missing"],
  ["corrupt_child_session_meta", "its session metadata is corrupt"],
  ["missing_child_transcript", "its transcript is missing"],
  ["corrupt_child_transcript", "its transcript is corrupt"],
  ["transcript_session_mismatch", "its transcript belongs to another session"],
  ["working_dir_missing", "its working directory is missing"],
  ["isolation_disposed", "its isolation was disposed"],
]);

// A code this client doesn't know still says something rather than a raw code.
const NOT_RESUMABLE_GENERIC = "its resumability was closed";

/** Why a delegate can no longer be resumed, in words, or undefined when there
 * is nothing to say. Shares delegateEndingText's vocabulary for the codes both
 * carry; a code this client doesn't know reads as a generic phrase, so a
 * snake_case code never reaches the screen, and a reason already in words
 * (an older hub's) shows as it is. */
export function delegateNotResumableText(reason: string | undefined): string | undefined {
  const code = reason?.trim();
  if (!code) return undefined;
  const words = NOT_RESUMABLE_WORDS.get(code);
  if (words) return words;
  return delegateEndingText({ reason: code }) ?? NOT_RESUMABLE_GENERIC;
}
