// Pure "honest liveness" logic: quiet ~Nm -> "may be stalled" - or, when the
// wait has a known explanation, that explanation instead of either. Two
// explanations exist, in precedence order: a model call the daemon has told us
// it is retrying ("rate limited — attempt 9/11 — retrying in 60s", kata 4zn8), and
// the session's subagents still running ("waiting on N subagents"). Both are
// design brief principle 6: a wait fully explained is not a stall. The retry
// wins when both hold, being the more specific and more directly observed of
// the two. Driven by gap = now - ThreadModel.lastFrameAt, active, a
// running-subagents COUNT (LivenessLine.tsx sources it - see there), and
// ThreadModel.modelRetry (which deliberately does NOT restamp lastFrameAt, so
// the gap this reads keeps measuring the real silence the retry is
// explaining). The retry does NOT, however, share the ordinary 20s quiet gate
// past its first attempt - see describeLiveness's own doc comment (kata gw2c).
// Display-only - no self-heal/reconnect side effect (that's a
// connection.ts/threads.ts concern, not this pane's), no idle animation
// (Cadence already carries live activity via its trace - see
// widgets/cadence). These are session-level thresholds only.

import { compactDuration, isStuck, RUNNING_QUIET_AFTER_MS } from "@evener/appwire-client";

export type LivenessLevel = "none" | "quiet" | "stalled" | "waiting" | "retrying";

export interface LivenessInfo {
  level: LivenessLevel;
  text: string | null;
}

// The short quiet hint. "Stalled" is the shared stuck rule (isStuck). The
// threshold is the shared client's RUNNING_QUIET_AFTER_MS (#4520), named for the
// transcript's liveness vocabulary.
export const QUIET_THRESHOLD_MS = RUNNING_QUIET_AFTER_MS;

const SECOND_MS = 1_000;
const MINUTE_MS = 60 * SECOND_MS;

/**
 * Bucketed, quantized "calm quiet" phrasing - never an exact rising
 * per-second counter, matching the legacy renderer's own
 * formatLivenessQuiet breakpoints (parity doc §16): <45s -> "~30s",
 * <90s -> "~1m", <3m -> "~2m", then whole minutes up to stalled, so the quiet
 * line never understates a long silence.
 */
export function formatQuietBucket(gapMs: number): string {
  if (gapMs < 45_000) return "~30s";
  if (gapMs < 90_000) return "~1m";
  if (gapMs < 3 * MINUTE_MS) return "~2m";
  return `~${Math.floor(gapMs / MINUTE_MS)}m`;
}

/**
 * Exact-ish (not bucketed) gap phrasing for the stalled/concern level,
 * matching the legacy renderer's own formatLivenessGap (parity doc §16):
 * under 60s as whole seconds, otherwise whole minutes plus a trailing
 * " Ss" only when the remainder is non-zero.
 */
export function formatExactGap(gapMs: number): string {
  const totalSeconds = Math.floor(gapMs / SECOND_MS);
  if (totalSeconds < 60) return `${totalSeconds}s`;
  const minutes = Math.floor(gapMs / MINUTE_MS);
  const remainderSeconds = totalSeconds - minutes * 60;
  return remainderSeconds === 0 ? `${minutes}m` : `${minutes}m ${remainderSeconds}s`;
}

/**
 * The pending model-call retry the daemon reported (ThreadModel.modelRetry),
 * narrowed to what this line renders. Unlike the raw ModelRetryState, `model`
 * and `inProgress` arrive already decided: LivenessLine.tsx narrows `model`
 * to defined-only-when-it-differs-from-the-session's-primary-model, and
 * derives `inProgress` from receivedAt/delayMs vs lastFrameAt/now (design doc
 * Component 1: "clients can derive waiting-vs-in-progress locally"). This
 * type stays a plain renderer of whatever it's given, with no comparison of
 * its own.
 */
export interface RetryWait {
  attempt: number;
  // The honest denominator (ModelRetryState.attemptCap) - render this, not
  // the raw retry-policy budget, once a consume-phase failure has dropped it.
  attemptCap: number;
  delayMs: number;
  errorClass?: string;
  /** The retry's model, already narrowed to "only when it differs from the session's primary". */
  model?: string;
  /** Elapsed wall-clock ms for the current retry group (one model call), not the whole turn. */
  groupElapsedMs: number;
  /**
   * Whether the wait is over. For an ordinary retry: a frame has landed
   * since this retry was reported, or its own delay has elapsed. For a
   * quota retry: the zero-delay wake announcement only - the request has
   * gone out - never a frame landing or the deadline passing (see
   * resetPassed for that case).
   */
  inProgress: boolean;
  /**
   * A quota wait whose reset time has passed while the wake (the zero-delay
   * retry announcement) has not come: the daemon may queue the retry behind
   * other work for minutes past the reset. Derived by the caller (the only
   * place with a clock); the countdown must stop at the deadline rather than
   * count down a reset that already passed, and the line must not claim the
   * request is in progress when it has not started.
   */
  resetPassed?: boolean;
}

/**
 * Names the retry's cause for the reader. Rate limiting and a usage-limit
 * wait get their own phrasing: they are the ones the reader can act on (wait,
 * or switch model). Rate limits are overwhelmingly the common case - one
 * session logged 91 of them in four hours. A usage-limit wait is not a
 * malfunction at all: the provider named a reset time and the call is holding
 * until then, so reading it as an error misnames a scheduled wait.
 * Everything else retryable reads as a generic provider error rather than
 * leaking an error-class token like "server" into the UI. Lowercase, matching
 * the design doc's own literal chip string (Component 1, Jesse 2026-08-08
 * rendering note) character for character.
 */
export function formatRetryCause(errorClass?: string): string {
  if (errorClass === "rate_limit") return "rate limited";
  return errorClass === "quota_exceeded" ? "waiting for quota" : "provider error";
}

/**
 * The retry line's text: cause, optional model tag, position in the retry
 * budget, wait-or-progress, and the call's elapsed time. The wait is the
 * load-bearing part - it is what separates "back in 60s" from "wedged",
 * which is the whole reason this line exists - but once deltas are flowing
 * (`inProgress`) that wait is over, so it reads "in progress" instead of a
 * stale countdown rather than disappearing (design doc Component 1's web-only
 * rule: clearing here would re-create the vanishing-chip bug). Field order and
 * wording match the design doc's literal chip string character for character:
 * "provider error — attempt 3/4 — retrying in 32s — 14m on this call".
 * A hub too old to send AttemptCap reports it as 0; rendering "attempt 3/0"
 * would be exactly the dishonest denominator AttemptCap exists to eliminate,
 * so the denominator is omitted entirely rather than substituted.
 */
export function formatRetryWait(retry: RetryWait | QuotaWait): string {
  // A quota wait's attempt count carries no information (the daemon's wait
  // announcement always reads attempt 1 and never spends a budget), and its
  // delay runs hours, not seconds: the wait is the line's whole content, the
  // same wall-clock phrasing every other surface gives it (the rail row and
  // the phone Board's compactDuration), with the call's elapsed time beside
  // it so the reader can tell a fresh wait from one hours in.
  // A quota retry is binary by its own announcement: still waiting reads as
  // the wait (or, past its reset with no wake yet, as overdue - the daemon
  // may queue the retry behind other work for minutes), and the zero-delay
  // wake reads as motion. The clock never decides: a frame landing or the
  // deadline passing is not the request starting.
  if (retry.errorClass === "quota_exceeded") {
    // The snapshot's instant knows no elapsed time; inventing "0s on this
    // call" would claim a fresh wait a reload cannot prove. Only a wait
    // that actually reported one renders the elapsed suffix.
    const elapsed = retry.groupElapsedMs > 0 ? ` — ${formatExactGap(retry.groupElapsedMs)} on this call` : "";
    if (!retry.inProgress && retry.resetPassed) {
      return `${formatRetryCause(retry.errorClass)} — reset passed, retry not started yet${elapsed}`;
    }
    if (!retry.inProgress) {
      return `${formatRetryCause(retry.errorClass)} — resumes in ${compactDuration(retry.delayMs)}${elapsed}`;
    }
    // The wait is over and the request is out (the zero-delay wake): the
    // line describes the resumed request as motion, and the wait label
    // above stays reserved for the pending countdown - a cause that says
    // "waiting" over a running call is the mislabel this branch exists to
    // avoid. No model tag: the wake event carries the model of the request
    // that FAILED, not the one the retry runs on - the daemon applies a
    // queued model switch before emitting the wake - so a tag here would
    // label the resumed request with the old model and make the switch
    // look ineffective.
    return `quota wait ended — in progress — ${formatExactGap(retry.groupElapsedMs)} on this call`;
  }
  const modelTag = "model" in retry && retry.model ? ` (${retry.model})` : "";
  const attempt =
    "attempt" in retry
      ? retry.attemptCap > 0
        ? `attempt ${retry.attempt}/${retry.attemptCap}`
        : `attempt ${retry.attempt}`
      : "";
  const wait = retry.inProgress ? "in progress" : `retrying in ${Math.max(0, Math.round(retry.delayMs / SECOND_MS))}s`;
  const elapsed = `${formatExactGap(retry.groupElapsedMs)} on this call`;
  return `${formatRetryCause(retry.errorClass)}${modelTag} — ${attempt} — ${wait} — ${elapsed}`;
}

// The quota wait a resume instant names, narrowed to the fields the quota
// branch of formatRetryWait reads: the instant alone carries no attempt count
// (the wait announcement always reads attempt 1 and spends no budget) and no
// call elapsed time, so neither is invented.
export type QuotaWait = Pick<RetryWait, "errorClass" | "inProgress" | "delayMs" | "groupElapsedMs" | "resetPassed">;

export function quotaWaitFrom(resumesInMs: number): QuotaWait {
  return {
    delayMs: resumesInMs,
    errorClass: "quota_exceeded",
    groupElapsedMs: 0,
    inProgress: false,
    resetPassed: false,
  };
}

/**
 * Pluralizes the running-children count for the "waiting" level's text -
 * singular "1 subagent", plural otherwise - matching the design brief
 * mockup's own worked example (liveness indicator: "waiting on 1 subagent").
 */
export function formatSubagentCount(count: number): string {
  return count === 1 ? "1 subagent" : `${count} subagents`;
}

/**
 * The single entry point LivenessLine.tsx renders from: given the gap since
 * the model's last frame, whether the thread is currently "active" (liveness
 * only ever evaluates while active - any other status always reads as level
 * "none", matching the legacy renderer's own gate), and how many of the
 * session's subagents are still running, decides the quiet/stalled/waiting
 * level and its display text.
 *
 * A wait explained by running children is not a stall (design brief principle
 * 6), so running children suppress "quiet" outright, and a session is never
 * stalled while any run (the shared isStuck rule, ten minutes of silence
 * otherwise).
 *
 * A retry does not wait for the quiet threshold past its FIRST attempt
 * (kata gw2c). The retry is KNOWN information the daemon reported, not an
 * inference from absence, so once we are on attempt 2 or later it should
 * not share ordinary silence's 20s clock at all - a fast retry storm
 * (e.g. Retry-After: 2, 11 attempts) would otherwise burn nearly its whole
 * budget invisibly and fail right as the quiet threshold was about to
 * explain it. Attempt 1 stays fully gated: that is the case the 20s gate
 * originally existed for (a retry that resolves on its very next try must
 * not flicker a message on and off for nobody's benefit), and it still
 * applies unchanged.
 */
export function describeLiveness(
  gapMs: number,
  active: boolean,
  runningSubagents: number,
  retry?: RetryWait,
  quotaRemainingMs?: number,
  quotaOverdueFromSnapshot?: boolean,
): LivenessInfo {
  if (!active) return { level: "none", text: null };
  // A quota wait is known information the same way a retry past its first
  // attempt is (the rule below): the provider named the reset, so the wait
  // shows the moment the client knows it and never escalates to stalled
  // while the reset is still ahead. The wait announcement always reads
  // attempt 1, so the attempt-count gate below cannot cover it, and its
  // silence runs hours: behind the quiet gate the line would read "no
  // updates for 3h", a stall report for a scheduled pause. The instant form
  // is the read snapshot's (a pane that re-hydrated, with no notification);
  // the retry form is the live announcement, whose delay runs to the reset.
  const liveQuotaWait = retry !== undefined && retry.errorClass === "quota_exceeded" && !retry.inProgress;
  // A snapshot whose deadline has passed is still evidence of a pending
  // wait: the daemon keeps the wait while the retry queues behind other
  // work. The overdue line never invents the elapsed time the snapshot
  // cannot know, and never reads as a stall for a wait the snapshot names.
  if (quotaOverdueFromSnapshot && !liveQuotaWait) {
    return { level: "retrying", text: formatRetryWait({ ...quotaWaitFrom(0), resetPassed: true }) };
  }
  // A live quota retry and the instant its reducer stamped name the same
  // wait. The instant's remaining time is the honest countdown (measured
  // from this render's now, while the retry's delayMs was measured when the
  // announcement landed); the retry's own elapsed time is the one the
  // instant cannot know. Either source alone renders alone: the instant
  // without the retry (a reload) invents no elapsed time, the retry without
  // the instant (a caller handing only the notification) keeps its own delay.
  if (quotaRemainingMs !== undefined) {
    const elapsed = liveQuotaWait ? retry.groupElapsedMs : 0;
    return {
      level: "retrying",
      text: formatRetryWait({ ...quotaWaitFrom(quotaRemainingMs), groupElapsedMs: elapsed }),
    };
  }
  if (liveQuotaWait) {
    return { level: "retrying", text: formatRetryWait(retry) };
  }
  // See this function's own doc comment above (kata gw2c): a retry past its
  // first attempt is known information, not an inference from silence, so
  // it bypasses the ordinary quiet gate below rather than waiting on it.
  const retryKnownEarly = retry !== undefined && retry.attempt >= 2;
  if (gapMs < QUIET_THRESHOLD_MS && !retryKnownEarly) return { level: "none", text: null };
  if (retry) {
    const retrying = formatRetryWait(retry);
    if (!isStuck(gapMs, runningSubagents)) return { level: "retrying", text: retrying };
    return { level: "stalled", text: `${retrying} — no updates for ${formatExactGap(gapMs)}` };
  }
  if (runningSubagents > 0) {
    const waiting = `Waiting on ${formatSubagentCount(runningSubagents)}`;
    return { level: "waiting", text: waiting };
  }
  if (!isStuck(gapMs, runningSubagents)) return { level: "quiet", text: `Quiet ${formatQuietBucket(gapMs)}` };
  return { level: "stalled", text: `May be stalled — no updates for ${formatExactGap(gapMs)}` };
}
