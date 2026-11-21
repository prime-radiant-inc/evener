// LivenessLine is the honest, quiet liveness indicator for the transcript
// pane: "Quiet ~30s" rolling to "May be stalled - no updates for 3m 5s", or -
// when the wait has a known explanation - that explanation instead of either:
// the shared quota-wait words while a model call waits out a usage limit
// (answering first, outside any quiet gate), "rate limited — attempt 9/11 —
// retrying in 60s — 14m on this call" while the
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
import { quotaWaitState } from "@evener/appwire-client/state/navigation";
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
  // The wait's deadline comes from the thread model itself
  // (ThreadModel.quotaResumesAt): the read's failure summary holds it across
  // a reload with no live frame to have announced it. Read from the same
  // store lookup as the delegates above rather than added to this
  // component's props: the caller already passes the model's other liveness
  // inputs, and this one is model-sourced in a way the ephemeral `retry` prop
  // is not.
  const quotaResumesAt = useThreadsStore((s) => {
    if (sessionRef === undefined) return undefined;
    return (s.threads.get(sessionRef) ?? s.watchedThreads.get(sessionRef))?.quotaResumesAt;
  });
  // The wait's stamp: present only when a stamped producer announced it. The
  // chip's elapsed sample is measured ONLY then - an unstamped daemon (one
  // that predates the stamp) sends a default zero it never measured, so the
  // pane must not display time-since-announcement as the call's elapsed.
  const quotaWaitStartedAt = useThreadsStore((s) => {
    if (sessionRef === undefined) return undefined;
    return (s.threads.get(sessionRef) ?? s.watchedThreads.get(sessionRef))?.quotaWaitStartedAt;
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
  // A retry is in progress once its wait is over: a frame landed since it was
  // reported, or its delay ran out on the pane's clock. A zero-delay retry
  // (the quota wake's announcement) has no wait, so it is in progress on
  // arrival without consulting that clock: the pane ticks on its own
  // schedule, the announcement can land between ticks with `now` still
  // behind `receivedAt`, and the clock test then read as a 0-second
  // countdown until the next tick.
  const retryWait: RetryWait | undefined = retry && {
    attempt: retry.attempt,
    attemptCap: retry.attemptCap,
    delayMs: retry.delayMs,
    errorClass: retry.errorClass,
    model: retry.model && retry.model !== primaryModel ? retry.model : undefined,
    groupElapsedMs: retry.groupElapsedMs,
    inProgress: retry.delayMs <= 0 || lastFrameAt > retry.receivedAt || now - retry.receivedAt >= retry.delayMs,
  };
  // The countdown rule is the shared tri-state one every surface reads, so
  // the pane and the row agree on the wait's state. The deadline the model
  // holds is absolute, and while the model holds it, it is the WHOLE
  // countdown: past it the wait is pending — the daemon keeps serving the
  // wait's own summary with its original instant while the wait stands (a
  // delegate held at its reset while every execution slot is occupied can
  // stay waiting for hours), so a passed deadline is still a live wait, and
  // no client clock may end it.
  const fromRead = quotaWaitState(quotaResumesAt, now);
  // No receipt clock exists: an unstamped start installs no deadline (the
  // reducer's own rule), so a wait with no absolute instant on the model is
  // no wait this line can name. A pane OPEN when the limit hits on a daemon
  // that predates the stamp shows no wait — the documented limitation —
  // while the daemon's own read summary (stamped) carries the instant a
  // reload needs.
  //
  // The wait never ends on `lastFrameAt`: that is RetryWait's rule for a
  // short backoff, but a six-hour wait sees unrelated frames (a finished
  // job, a delegate row) restamp it constantly. Only the daemon ends a wait
  // - the zero-delay wake, a turn boundary, or a read showing none - and
  // the model fields those clear are what this reads. A retry outlives its
  // turn (modelRetry is not cleared on Stop), so the guard against showing
  // a dead turn's wait is the retry's own turn identity, never a frame
  // clock: only a retry naming THIS pane's running turn speaks for it.
  // The wait's own chip metadata: the model tag, the elapsed sample, and the
  // receipt instant the advance keys on, all from the announcement that
  // established the wait. The stamp matching is the reducer's fold (an
  // equal-stamp start fills the chip; the wake retires it with the wait; a
  // replacement retires the replaced turn's chip), so the quota chip the
  // model holds beside a standing wait is that wait's own announcement.
  // This narrows what reaches the line by the ownership the chip itself
  // carries: only a chip naming THIS pane's running turn may speak for the
  // wait. A snapshot-only wait (deadline from a read, no live announcement)
  // carries neither tag nor elapsed - the snapshot cannot attribute them.
  const chipMatchesWait =
    retry !== undefined &&
    retry.errorClass === "quota_exceeded" &&
    fromRead !== null &&
    retry.turnId !== undefined &&
    retry.turnId === turnId;
  // Elapsed advances from the announcement's receipt instant, clamped so it
  // never goes negative across the pane's tick boundary (the tick can land
  // marginally before the receipt stamp a later frame recorded) - but only a
  // MEASURED sample may display, and the stamp is what says the sample was
  // measured: a stamped announcement's sample is real (including a valid
  // zero), where a daemon that predates the stamp sends a default zero that
  // measures nothing. An unstamped frame installs no wait at all (the
  // reducer's rule), so this gate is structural defense for that contract:
  // time-since-announcement must never dress itself up as the call's elapsed.
  const chipElapsedMs =
    chipMatchesWait && quotaWaitStartedAt !== undefined
      ? Math.max(0, retry.groupElapsedMs + (now - retry.receivedAt))
      : undefined;
  const quotaWait: QuotaWait | undefined =
    fromRead === null
      ? undefined
      : {
          passedReset: fromRead.phase === "pending",
          ...(fromRead.phase === "counting" ? { remainingMs: fromRead.remainingMs } : {}),
          ...(chipMatchesWait && retry.model !== undefined && retry.model !== primaryModel
            ? { model: retry.model }
            : {}),
          ...(chipElapsedMs !== undefined ? { elapsedMs: chipElapsedMs } : {}),
        };
  const { level, text } = describeLiveness(now - lastFrameAt, active, runningSubagents, retryWait, quotaWait);
  if (level === "none" || text === null) return null;

  return (
    <div
      data-testid="liveness-line"
      data-level={level}
      // The quota wait's phase, as a fact rather than a sentence: the level
      // says a wait stands, this says which half of it the reader sees.
      data-passed-reset={level === "quota" && quotaWait?.passedReset === true ? "true" : undefined}
      // The computed times as facts beside the sentence: tests assert these
      // rather than the copy, and the level's own phase rules stay pinned by
      // the attributes above.
      data-remaining-ms={
        level === "quota" && quotaWait?.remainingMs !== undefined ? String(quotaWait.remainingMs) : undefined
      }
      data-elapsed-ms={
        level === "quota" && quotaWait?.elapsedMs !== undefined ? String(quotaWait.elapsedMs) : undefined
      }
      data-model={level === "quota" && quotaWait?.model !== undefined ? quotaWait.model : undefined}
      // Whether the retry the line reports is under way or still waiting out
      // its delay, as a fact rather than the "in progress" / "retrying in"
      // words.
      data-retry-in-progress={
        retryWait !== undefined && (level === "retrying" || level === "stalled")
          ? String(retryWait.inProgress)
          : undefined
      }
      className={level === "stalled" ? `${CLASS.line} ${CLASS.stalled}` : CLASS.line}
    >
      {text}
    </div>
  );
}
