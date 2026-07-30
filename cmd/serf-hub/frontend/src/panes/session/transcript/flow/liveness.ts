// Pure "honest liveness" logic: quiet ~Nm -> "may be stalled" - or, when the
// wait has a known explanation, that explanation instead of either. Two
// explanations exist, in precedence order: a model call the daemon has told us
// it is retrying ("rate limited - retry 9 of 11, next in 60s", kata 4zn8), and
// the active turn's own delegated children still running ("waiting on N
// subagents"). Both are design brief principle 6: a wait fully explained is not
// a stall. The retry wins when both hold, being the more specific and more
// directly observed of the two. Driven by gap = now -
// ThreadModel.lastFrameAt, active, a running-children COUNT
// (LivenessLine.tsx sources it from subagentModuleStore, scoped by
// turnScopeKey(sessionRef, turnId) - see that store's own comment on why a
// bare turn id is never enough), and ThreadModel.modelRetry (which
// deliberately does NOT restamp lastFrameAt, so the gap this reads keeps
// measuring the real silence the retry is explaining). Display-only - no self-heal/reconnect side
// effect (that's a connection.ts/threads.ts concern, not this pane's), no
// idle animation (Cadence already carries live activity via its trace - see
// widgets/cadence). Session-level thresholds only; the legacy renderer also
// runs a SEPARATE per-subagent-row liveness clock (10s/45s) - that's a
// distinct feature area (T3's subagent module) this deliberately does not
// unify with (borrowing its live COUNT is not adopting its thresholds), per
// parity doc's own Highlights section.

export type LivenessLevel = "none" | "quiet" | "stalled" | "waiting" | "retrying";

export interface LivenessInfo {
  level: LivenessLevel;
  text: string | null;
}

// docs/web-ui/parity/parity-m4-transcript.md §16.
export const QUIET_THRESHOLD_MS = 20_000;
export const STALL_THRESHOLD_MS = 180_000;

const SECOND_MS = 1_000;
const MINUTE_MS = 60 * SECOND_MS;

/**
 * Bucketed, quantized "calm quiet" phrasing - never an exact rising
 * per-second counter, matching the legacy renderer's own
 * formatLivenessQuiet breakpoints exactly (parity doc §16): <45s -> "~30s",
 * <90s -> "~1m", >=90s -> "~2m". The top bucket covers the ENTIRE 90s-180s
 * span (not just up to some smaller ceiling), so the quiet line never
 * climbs to "~3m" in the moment before crossing into stalled.
 */
export function formatQuietBucket(gapMs: number): string {
  if (gapMs < 45_000) return "~30s";
  if (gapMs < 90_000) return "~1m";
  return "~2m";
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
 * narrowed to what this line renders.
 */
export interface RetryWait {
  attempt: number;
  maxAttempts: number;
  delayMs: number;
  errorClass?: string;
}

/**
 * Names the retry's cause for the reader. Only rate limiting gets its own
 * phrasing: it is the one the reader can act on (wait, or switch model), and it
 * is overwhelmingly the common case - one session logged 91 of them in four
 * hours. Everything else retryable reads as a generic provider error rather
 * than leaking an error-class token like "server" into the UI.
 */
export function formatRetryCause(errorClass?: string): string {
  return errorClass === "rate_limit" ? "Rate limited" : "Provider error";
}

/**
 * The retry line's text: cause, position in the retry budget, and the wait.
 * The wait is the load-bearing part - it is what separates "back in 60s" from
 * "wedged", which is the whole reason this line exists.
 */
export function formatRetryWait(retry: RetryWait): string {
  const seconds = Math.max(0, Math.round(retry.delayMs / SECOND_MS));
  return `${formatRetryCause(retry.errorClass)} — retry ${retry.attempt} of ${retry.maxAttempts}, next in ${seconds}s`;
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
 * active turn's delegated children are still running, decides the
 * quiet/stalled/waiting level and its display text.
 *
 * A wait explained by running children is not a stall (design brief principle
 * 6), so running children suppress "quiet" outright.
 *
 * They do NOT suppress the stall report, and that asymmetry is the important
 * part. "A child is running" is a CLAIM this client cannot independently
 * verify: the count comes from subagent rows whose status is itself derived
 * from the wire, and a row that has lost contact with its child reports
 * "running" because that is the honest default when nothing bad is known
 * (subagentModuleStore's classifyJobStatus, and kata g5kf on how a row can
 * hold that claim after the child is gone). If a believed-running child were
 * allowed to suppress the stall report forever, then the single case where a
 * reader most needs the truth — a parent wedged behind a child that died
 * quietly — would be the one case the UI stayed confidently silent about, and
 * the honest-liveness rule would be inverted by its own explanation.
 *
 * So past the stall threshold the line reports both facts at once and asserts
 * neither over the other: children are believed running, AND nothing has
 * arrived for a long time. The reader can act on that; they cannot act on
 * "waiting on 1 subagent" held for nine minutes.
 */
export function describeLiveness(
  gapMs: number,
  active: boolean,
  runningSubagents: number,
  retry?: RetryWait,
): LivenessInfo {
  if (!active || gapMs < QUIET_THRESHOLD_MS) return { level: "none", text: null };
  if (retry) {
    const retrying = formatRetryWait(retry);
    if (gapMs < STALL_THRESHOLD_MS) return { level: "retrying", text: retrying };
    return { level: "stalled", text: `${retrying} — no updates for ${formatExactGap(gapMs)}` };
  }
  if (runningSubagents > 0) {
    const waiting = `Waiting on ${formatSubagentCount(runningSubagents)}`;
    if (gapMs < STALL_THRESHOLD_MS) return { level: "waiting", text: waiting };
    return { level: "stalled", text: `${waiting} — no updates for ${formatExactGap(gapMs)}` };
  }
  if (gapMs < STALL_THRESHOLD_MS) return { level: "quiet", text: `Quiet ${formatQuietBucket(gapMs)}` };
  return { level: "stalled", text: `May be stalled — no updates for ${formatExactGap(gapMs)}` };
}
