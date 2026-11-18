// LivenessLine is the honest, quiet liveness indicator for the transcript
// pane: "Quiet ~30s" rolling to "May be stalled - no updates for 3m 5s", or -
// when the wait has a known explanation - that explanation instead of either:
// "rate limited — attempt 9/11 — retrying in 60s — 14m on this call" while the
// daemon is retrying a model call, or "Waiting on N subagents" while the
// session's subagents are still running (design brief principle 6: a wait
// explained is not a stall). That decision is driven
// purely by describeLiveness (see liveness.ts); this component's own job is
// sourcing its live inputs: `now` (Session.tsx's own useNowTick value,
// already plumbed there for Cadence, so this never starts a second clock -
// same "no timers, no Date.now()" contract as widgets/cadence's own Cadence),
// the running-subagents count (see the comment where it is computed below),
// and narrowing the raw retry (ThreadModel.modelRetry) into what liveness.ts
// renders (see retryWait below). Renders nothing while level is "none" (fresh/inactive),
// and deliberately carries no animation of its own - Cadence's trace already
// conveys activity; this line's entire job is to say something honest when
// that activity stops.

import type { ModelRetryState } from "@evener/appwire-client";
import { quotaResumeRemainingMs } from "@evener/appwire-client/state/navigation";
import { useSubagentCounts } from "../../../../stores/sessionActivity";
import { useThreadsStore } from "../../../../stores/threads";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { turnScopeKey, useRunningSubagentCount } from "../tools/subagentModuleStore";
import { describeLiveness, type QuotaWait, type RetryWait } from "./liveness";
import styles from "./livenessline.module.css";

export interface LivenessLineProps {
  /** ThreadModel.lastFrameAt - epoch ms of the most recent live frame. */
  lastFrameAt: number;
  /** Epoch-ms "current" instant; caller-owned clock, same as Cadence's own `now`. */
  now: number;
  /** Only "active" threads show a liveness line at all (matches the legacy gate). */
  active: boolean;
  /** ThreadModel.ref - scopes the running-children lookup alongside turnId. */
  sessionRef: string | undefined;
  /** ThreadModel.activeTurnId - undefined (no active turn yet) reads as zero running children. */
  turnId: string | undefined;
  /**
   * ThreadModel.modelRetry - the daemon's own report of a model call waiting
   * to be retried, unnarrowed. This component derives RetryWait's `model` and
   * `inProgress` fields from it (see retryWait below) before handing it to
   * describeLiveness. Undefined whenever no retry is pending.
   */
  retry?: ModelRetryState;
  /**
   * ThreadModel.model - the session's current primary model. Only read when
   * `retry` is present: names the retry's own model in the chip when a
   * fallback chain walk switched it away from this one (design doc Component
   * 1's model-identity rule - without it the reader cannot tell "same model,
   * still failing" from "now trying a different model").
   */
  primaryModel?: string;
}

const CLASS = {
  line: requireClass(styles.line, "livenessline.module.css", "line"),
  stalled: requireClass(styles.stalled, "livenessline.module.css", "stalled"),
};

export function LivenessLine({ lastFrameAt, now, active, sessionRef, turnId, retry, primaryModel }: LivenessLineProps) {
  const delegates = useThreadsStore((s) => {
    if (sessionRef === undefined) return undefined;
    return (s.threads.get(sessionRef) ?? s.watchedThreads.get(sessionRef))?.delegates;
  });
  // The instant a usage-limit wait resumes comes from the thread read
  // (ThreadModel.quotaResumesAt), so it is here after a reload with no live
  // frame to have announced it. Read from the same store lookup as the
  // delegates above rather than added to this component's props: the caller
  // already passes the model's other liveness inputs, and this one is
  // model-sourced in a way the ephemeral `retry` prop is not.
  const quotaResumesAt = useThreadsStore((s) => {
    if (sessionRef === undefined) return undefined;
    return (s.threads.get(sessionRef) ?? s.watchedThreads.get(sessionRef))?.quotaResumesAt;
  });
  const turnRunning = useRunningSubagentCount(
    turnId === undefined ? undefined : turnScopeKey(sessionRef, turnId),
    delegates,
  );
  // The hub's count is the one rule for a session's running subagents: those
  // whose run is open, at every depth (the subtree activity summary, as the
  // Live row's tally and the phone count them). The active turn's own rows
  // (subagentModuleStore, scoped by turnScopeKey - see that store's comment
  // on why a bare turn id is never enough) stand in only until the hub's
  // count is known (not yet read, an older hub, or a session the hub has not
  // counted), so a launch the transcript already shows can explain the wait.
  // The line holds the count itself whether or not a turn runs, so a starting
  // turn has it without relying on another surface holding it.
  const runningSubagents = useSubagentCounts(sessionRef ?? null)?.active ?? turnRunning;
  // A zero-delay quota frame is the WAKE: the call is going out again, and the
  // model it names is the one that hit the limit, not the one now running (a
  // model switch is exactly what wakes a wait). Tagging the resumed call with
  // the old model outlives the quiet threshold and labels a live call with the
  // wrong provider. The wait's own announcement keeps its tag, because there
  // the named model is the one that failed.
  const retryModelOmitted = retry?.errorClass === "quota_exceeded" && retry.delayMs === 0;
  const retryWait: RetryWait | undefined = retry && {
    attempt: retry.attempt,
    attemptCap: retry.attemptCap,
    delayMs: retry.delayMs,
    errorClass: retry.errorClass,
    model: !retryModelOmitted && retry.model && retry.model !== primaryModel ? retry.model : undefined,
    groupElapsedMs: retry.groupElapsedMs,
    inProgress: lastFrameAt > retry.receivedAt || now - retry.receivedAt >= retry.delayMs,
  };
  // The countdown rule is the shared one the rail row uses, so both surfaces
  // agree on when a wait is over; a wait whose instant has passed explains
  // nothing and leaves the ordinary decision below.
  const fromRead = quotaResumeRemainingMs(quotaResumesAt, now);
  // A pane OPEN when the limit hits gets only the live retry, and that
  // notification never invalidates history, so no read has set an instant for
  // it (ThreadModel.quotaResumesAt is snapshot-only). Without this the wait
  // would still render as the retry line's seconds countdown and then as
  // unexplained silence - the very reading this issue is about - for the whole
  // wait, until something happened to force a reload. `delayMs` is how long
  // the call waits from `receivedAt`, both on this client's clock, so the
  // distance to `now` needs no daemon clock and is off only by delivery
  // latency. The read-sourced instant wins while it is still ahead: it is
  // absolute, where this is a countdown from a message a reconnect could have
  // delivered hours late. A read instant already past says that wait is over,
  // which leaves a later retry free to speak for itself.
  //
  // The wait ends on the retry's OWN facts: its own countdown running out, or
  // the reducer replacing it with the daemon's no-delay retry. It must NOT end
  // on `lastFrameAt`, which is RetryWait's rule for a short backoff but wrong
  // here: a six-hour wait sees frames that have nothing to do with the waiting
  // call, and every thread-scalar frame restamps lastFrameAt (a finished
  // background job, a delegate row update, a queue change - reducer.ts's
  // THREAD_SCALAR_ONLY_METHODS). Keying on it would drop the chip the moment
  // any of those landed and fall back to the retry text and then to silence,
  // which is the reading this issue forbids.
  //
  // A retry outlives its turn (modelRetry is not cleared on Stop), so the
  // guard against showing a dead turn's wait is the retry's own turn identity
  // rather than any frame clock: only a retry naming THIS pane's running turn
  // speaks for it.
  //
  // One time basis per wait, never two. A STAMPED wait has a daemon-issued
  // start, so its instant is the daemon's arithmetic and `fromRead` above is
  // the whole countdown; this receipt-based arm is not a fallback for it. The
  // two bases disagree by the reader's clock skew, so mixing them made a wait
  // expire early on a fast clock and then jump back to the receipt-based
  // remainder - a countdown reaching zero and then leaping to an hour. Only a
  // wait with no usable absolute deadline (an older daemon that issued no
  // stamp, so nothing can put an instant in the model) counts from receipt,
  // where both `now` and `receivedAt` are this client's clock and the
  // difference carries no skew.
  // A stamped quota wait's basis is the DAEMON's instant, and the generic
  // retry line's only wait field is this client's receipt duration - a second
  // basis. While the daemon's countdown is live, `quotaWait` below answers
  // first and this line never speaks. Once that countdown expires (or when no
  // read has named the instant yet) the receipt duration would claim
  // "retrying in 7200s" - and, past the stuck threshold, hang it off a stall
  // warning - for a wait the daemon already ended. So a stamped wait leaves
  // the generic formatter entirely, and the ordinary silence reading stands
  // in: it infers something about the silence, never about the wait. The
  // wake's zero-delay announcement and the resumed call's ordinary retries
  // are not waits and still drive retry-progress text.
  const stampedQuotaWait =
    retry !== undefined &&
    retry.errorClass === "quota_exceeded" &&
    retry.delayMs > 0 &&
    retry.waitStartedAt !== undefined;
  const liveRemaining =
    retry !== undefined &&
    retry.errorClass === "quota_exceeded" &&
    retry.delayMs > 0 &&
    !stampedQuotaWait &&
    retry.turnId !== undefined &&
    retry.turnId === turnId &&
    now - retry.receivedAt < retry.delayMs
      ? retry.delayMs - (now - retry.receivedAt)
      : undefined;
  const remaining = fromRead ?? (liveRemaining !== undefined && liveRemaining > 0 ? liveRemaining : null);
  // Past the reset the wait does not stop existing. The countdown's time basis
  // is the daemon's deadline, and its lifetime is the daemon's WAIT - not this
  // client's clock crossing a reset. The reset is a fact about the schedule:
  // a delegate that reaches it while every execution slot is occupied stays
  // published as waiting until it reacquires one and emits the wake, which can
  // be hours. Dropping the line there renders those hours as a stall warning
  // (or as silence) while the daemon still reports a wait, so the wait is held
  // for as long as the model records one, and the line says which half of it
  // we are in. The model's own route clears `quotaResumesAt` on the wake, a
  // turn boundary, or an authoritative read; only then does ordinary liveness
  // take over.
  const waitStillRecorded = quotaResumesAt !== undefined || stampedQuotaWait;
  // The matching announcement's model tag and the call's elapsed time ride
  // the quota line only when a stamped same-turn announcement established
  // them: the snapshot alone cannot attribute a model or a duration to the
  // waiting call.
  const matchingAnnouncement =
    stampedQuotaWait && retry?.turnId !== undefined && retry.turnId === turnId ? retry : undefined;
  const quotaWait: QuotaWait | undefined =
    remaining !== null
      ? {
          remainingMs: remaining,
          ...(matchingAnnouncement?.model && matchingAnnouncement.model !== primaryModel
            ? { modelTag: matchingAnnouncement.model }
            : {}),
          ...(matchingAnnouncement ? { elapsedMs: matchingAnnouncement.groupElapsedMs } : {}),
        }
      : waitStillRecorded
        ? { remainingMs: 0, passedReset: true }
        : undefined;
  const { level, text } = describeLiveness(
    now - lastFrameAt,
    active,
    runningSubagents,
    stampedQuotaWait ? undefined : retryWait,
    quotaWait,
  );
  if (level === "none" || text === null) return null;

  return (
    <div
      data-testid="liveness-line"
      data-level={level}
      className={level === "stalled" ? `${CLASS.line} ${CLASS.stalled}` : CLASS.line}
    >
      {text}
    </div>
  );
}
