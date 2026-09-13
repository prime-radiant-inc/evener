import type { ActivityDelegate } from "../../appwire-client/typescript/activityData";

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

function snapshotDuration(
  value: number | null | undefined,
): number | undefined {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0
    ? value
    : undefined;
}

function elapsed(now: number, then: number): number | undefined {
  if (!Number.isFinite(now)) return undefined;
  return Math.min(Number.MAX_SAFE_INTEGER, Math.max(0, now - then));
}

export function delegateTiming(
  delegate: ActivityDelegate,
  now: number,
): DelegateTiming {
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
    if (started !== undefined && ended !== undefined && ended >= started)
      result.durationMs = ended - started;

    if (result.durationMs === undefined)
      result.durationMs = snapshotDuration(delegate.durationMs);
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
  const quietAnchor =
    latest !== undefined && started !== undefined
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

export function delegateModel(delegate: ActivityDelegate): {
  model?: string;
  requestedModel?: string;
  reasoning?: string;
} {
  const resolvedModel = nonblank(delegate.resolvedModel);
  const model = nonblank(delegate.model);
  const requestedModel = nonblank(delegate.requestedModel);
  const selected = resolvedModel ?? model ?? requestedModel;
  return {
    ...(selected ? { model: selected } : {}),
    ...(requestedModel !== undefined && requestedModel !== selected
      ? { requestedModel }
      : {}),
    ...(nonblank(delegate.reasoningEffort) !== undefined
      ? { reasoning: nonblank(delegate.reasoningEffort) }
      : {}),
  };
}

export function delegatePacket(
  value: unknown,
  structured = false,
): { text: string; format: "markdown" | "json" } | undefined {
  if (typeof value === "undefined") return undefined;
  if (typeof value === "string" && !structured)
    return { text: value, format: "markdown" };
  try {
    const text = JSON.stringify(value, null, 2);
    return text === undefined ? undefined : { text, format: "json" };
  } catch {
    return undefined;
  }
}
