// Pure formatting helpers for the status row. Kept dependency-free (no
// React, no ThreadModel) so each is trivially unit-testable - same
// convention as transcript/messages/format.ts, deliberately not shared with
// it (see formatWorkDuration's own comment for why).

// formatWorkDuration mirrors the daemon's own compactDuration/formatWorkMillis
// convention verbatim (cmd/serf-hub/web_format.go:79-102): under a minute
// shows whole seconds (floored, clamped up to a minimum of 1 so a real but
// sub-second duration never reads "0s"); under an hour shows whole minutes
// (floored); an hour or more shows "Nh Nm" (minutes modulo 60). This is a
// SEPARATE convention from transcript/messages/format.ts's formatDurationMs
// (sub-second decimal precision for short tool-call durations, no hour
// bucket at all) - work time is a session-cumulative clock that can span
// multiple hours, so it needs the coarser, longer-range bucketing instead.
export function formatWorkDuration(ms: number): string {
  const clampedMs = Math.max(0, ms);
  const totalSeconds = Math.floor(clampedMs / 1000);

  if (clampedMs < 60_000) {
    const seconds = Math.max(1, totalSeconds);
    return `${seconds}s`;
  }
  const totalMinutes = Math.floor(clampedMs / 60_000);
  if (clampedMs < 3_600_000) {
    return `${totalMinutes}m`;
  }
  const hours = Math.floor(clampedMs / 3_600_000);
  return `${hours}h ${totalMinutes % 60}m`;
}

// modelLabel collapses to a single label when provider and model are still
// the identical string - the cold-hydrate shape (protocol/reducer.ts's
// hydrateThread: "Thread has no separate 'model id' field on the wire
// snapshot - only ModelProvider... this file documents that overload
// rather than re-deriving a real split"). Once a live thread/model/changed
// has actually split them apart, shows "provider/model" - matching the
// legacy model chip's own text.
export function modelLabel(modelProvider: string, model: string): string {
  return model && model !== modelProvider ? `${modelProvider}/${model}` : modelProvider;
}

// totalWorkMillis adds the in-flight turn's live elapsed time (now minus
// activeTurnStartedAt, when a turn is actually active) on top of the
// cumulative workMillis already banked for completed turns, so the status
// row's clock keeps ticking during an active turn instead of freezing at
// the last completed total. Math.max(0, ...) guards a clock-skew edge (now
// read fractionally before the reducer's own now-stamped activeTurnStartedAt)
// the same way widgets/cadence's ticksFor guards its own clock-skew case.
//
// A real turn start is a positive Unix-epoch-ms wall-clock time. An anchor at
// or before the epoch (or an unparseable one) is not a turn that has been
// running since 1970 - it is the wire's "unset" sentinel leaking through. The
// reducer's epochMsToISO now maps non-positive anchors to absent at the
// source, so a 1970 ISO string should never arrive from hydrate; this guard
// stays as defense-in-depth for an anchor reaching the model by any other
// path. Trusting one would clock the whole now-minus-epoch span (~500000h).
// Ignore it and show the banked total instead - no clock beats an absurd one.
export function totalWorkMillis(workMillis: number, activeTurnStartedAt: string | undefined, now: number): number {
  if (!activeTurnStartedAt) return workMillis;
  const startedMs = Date.parse(activeTurnStartedAt);
  if (!Number.isFinite(startedMs) || startedMs <= 0) return workMillis;
  return workMillis + Math.max(0, now - startedMs);
}
