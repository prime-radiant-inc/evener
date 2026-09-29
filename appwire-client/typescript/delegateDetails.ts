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

function nonblank(value: string | undefined): string | undefined {
  return typeof value === "string" && value.trim() !== "" ? value : undefined;
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

// The daemon's reason codes (agent/delegate_tree_*.go, subagents.go) said
// plainly. A reason is a code; a failed run's cause in words rides beside it
// as `error` (#3327).
const ENDING_WORDS = new Map([
  ["failed", "failed"],
  ["run_error", "failed with an error"],
  ["ended_without_report", "ended without reporting"],
  ["terminal_error", "ended with an error"],
  ["missing_terminal", "ended without reporting"],
  ["runtime_lost", "runtime lost"],
  ["input_persist_failed", "couldn't save its input"],
  ["cancelled", "cancelled"],
  ["stopped_by_parent", "stopped by its coordinator"],
  ["tool_round_budget_exhausted", "ran out of tool rounds"],
  ["turn_budget_exhausted", "ran out of turns"],
]);

// The word for an outcome whose reason is a code this client doesn't know.
const OUTCOME_WORDS = new Map([
  ["failed", "failed"],
  ["exhausted", "failed"],
  ["cancelled", "stopped"],
  ["stopped", "stopped"],
]);

const CODE = /^[a-z0-9]+(?:_[a-z0-9]+)+$/;

export interface DelegateEndingFields {
  outcome?: string;
  reason?: string;
  error?: string;
}

/** How a subagent's last run ended, in words, or undefined when there is
 * nothing to say: the hub's `error` when it sent one, else the reason code
 * said plainly. A code this client doesn't know reads as its outcome's word,
 * so a snake_case code never reaches the screen; a reason already in words
 * (an older hub's) shows as it is. */
export function delegateEndingText(delegate: DelegateEndingFields): string | undefined {
  const error = delegate.error?.trim();
  if (error) return error;
  const reason = delegate.reason?.trim();
  if (!reason) return undefined;
  const words = ENDING_WORDS.get(reason);
  if (words) return words;
  if (!CODE.test(reason)) return reason;
  return OUTCOME_WORDS.get(delegate.outcome ?? "");
}
