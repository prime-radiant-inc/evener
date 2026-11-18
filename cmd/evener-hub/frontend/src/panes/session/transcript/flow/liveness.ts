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

import { isStuck, RUNNING_QUIET_AFTER_MS } from "@evener/appwire-client";
import { waitingForQuotaText } from "@evener/appwire-client/state/navigation";

export type LivenessLevel = "none" | "quiet" | "stalled" | "waiting" | "retrying" | "quota";

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
  /** Whether the wait is over: a frame has landed since this retry was reported, or its own delay has elapsed. */
  inProgress: boolean;
}

/**
 * A model call waiting out a provider usage limit that named its reset, as the
 * line renders it. Like RetryWait, the caller (LivenessLine.tsx) does the
 * narrowing: ThreadModel.quotaResumesAt is an ISO instant and this type's one
 * field is the distance to it, already decided against the shared countdown
 * rule so that a wait whose instant has passed is absent rather than negative.
 */
export interface QuotaWait {
  remainingMs: number;
  /**
   * The daemon's deadline has passed on this client's clock while the daemon
   * still reports the wait. Absent for a wait still ahead of its reset.
   */
  passedReset?: true;
}

/**
 * The wait's own line once its deadline is behind us.
 *
 * The countdown's time basis is the daemon's deadline, and its lifetime is
 * the daemon's wait - not the local clock crossing a reset. The reset is a
 * fact about the schedule, not about whether the wait still exists: a
 * delegate that reaches its provider reset while every execution slot is
 * occupied stays published as waiting until it reacquires one and emits the
 * wake, which can be hours. Reading that state as silence turns a scheduled
 * wait into a stall warning while the daemon says otherwise, so say what is
 * actually known - the reset has passed, the retry is still pending - and
 * leave the ordinary liveness reading for the wake, a turn boundary, or an
 * authoritative read that ends the wait.
 */
export function quotaWaitPassedResetText(): string {
  return "Waiting for quota · reset passed, retry still pending";
}

/**
 * Names the retry's cause for the reader. Only rate limiting gets its own
 * phrasing: it is the one the reader can act on (wait, or switch model), and it
 * is overwhelmingly the common case - one session logged 91 of them in four
 * hours. Everything else retryable reads as a generic provider error rather
 * than leaking an error-class token like "server" into the UI. Lowercase,
 * matching the design doc's own literal chip string (Component 1,
 * Jesse 2026-08-08 rendering note) character for character.
 */
export function formatRetryCause(errorClass?: string): string {
  if (errorClass === "rate_limit") return "rate limited";
  // A usage limit is not a provider fault the reader should read as one: the
  // call is waiting for an allowance to return, which the line says with its
  // own level (describeLiveness's quota branch) rather than through this
  // cause alone.
  if (errorClass === "quota_exceeded") return "usage limit";
  return "provider error";
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
export function formatRetryWait(retry: RetryWait): string {
  const modelTag = retry.model ? ` (${retry.model})` : "";
  const attempt = retry.attemptCap > 0 ? `attempt ${retry.attempt}/${retry.attemptCap}` : `attempt ${retry.attempt}`;
  const wait = retry.inProgress ? "in progress" : `retrying in ${Math.max(0, Math.round(retry.delayMs / SECOND_MS))}s`;
  const elapsed = `${formatExactGap(retry.groupElapsedMs)} on this call`;
  return `${formatRetryCause(retry.errorClass)}${modelTag} — ${attempt} — ${wait} — ${elapsed}`;
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
  quotaWait?: QuotaWait,
): LivenessInfo {
  if (!active) return { level: "none", text: null };
  // A call waiting out a usage limit that named its reset is the one wait this
  // line can state outright: the session is working, the daemon said when it
  // resumes, and that instant came from the read, so it survives a reload the
  // retry notification beside it cannot. It goes FIRST, ahead of every
  // silence-derived level, and takes no part in the quiet gate: a wait of six
  // hours is not 20 seconds of unexplained silence, and counting it in seconds
  // (as the retry line's delay does) would read as a wedged session. Past the
  // instant the wait is over, so it stops explaining anything and the ordinary
  // decision below applies until the next read settles the status.
  if (quotaWait) {
    return {
      level: "quota",
      text: quotaWait.passedReset ? quotaWaitPassedResetText() : waitingForQuotaText(quotaWait.remainingMs),
    };
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
