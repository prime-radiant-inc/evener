// LivenessLine is the honest, quiet liveness indicator for the transcript
// pane: "Quiet ~30s" rolling to "May be stalled - no updates for 3m 5s", or -
// when the wait has a known explanation - that explanation instead of either:
// "rate limited — attempt 9/11 — retrying in 60s — 14m on this call" while the
// daemon is retrying a model call, or "Waiting on N subagents" while the
// session's subagents are still running (design brief
// principle 6: a wait explained is not a stall). That decision is driven
// purely by describeLiveness (see liveness.ts); this component's own job is
// sourcing its live inputs: `now` (Session.tsx's own useNowTick value,
// already plumbed there for Cadence, so this never starts a second clock -
// same "no timers, no Date.now()" contract as widgets/cadence's own Cadence),
// the running-subagents count - the hub's subtree activity summary once
// known, until then a reactive read of subagentModuleStore scoped by
// turnScopeKey(sessionRef, turnId) (see that store's own comment on why a
// bare turn id is never enough: two sessions can share one) - and narrowing
// the raw retry (ThreadModel.modelRetry) into what liveness.ts renders (see
// retryWait below). Renders nothing while level is "none" (fresh/inactive),
// and deliberately carries no animation of its own - Cadence's trace already
// conveys activity; this line's entire job is to say something honest when
// that activity stops.

import type { ModelRetryState } from "@evener/appwire-client";
import { useSessionActivity } from "../../../../stores/sessionActivity";
import { useThreadsStore } from "../../../../stores/threads";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { turnScopeKey, useRunningSubagentCount } from "../tools/subagentModuleStore";
import { describeLiveness, type RetryWait } from "./liveness";
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
  const turnRunning = useRunningSubagentCount(
    turnId === undefined ? undefined : turnScopeKey(sessionRef, turnId),
    delegates,
  );
  // The hub's count is the one rule for a session's running subagents: those
  // whose run is open, at every depth (the subtree activity summary, as the
  // Live row's tally and the phone count them). The active turn's own rows
  // stand in only until the hub's count is known (not yet read, or an older
  // hub), so a launch the transcript already shows can explain the wait.
  const { snapshot } = useSessionActivity(active && sessionRef !== undefined ? sessionRef : null, "subtree");
  const hubDelegates = snapshot?.summary?.delegates;
  const runningSubagents = hubDelegates?.known ? hubDelegates.active : turnRunning;
  const retryWait: RetryWait | undefined = retry && {
    attempt: retry.attempt,
    attemptCap: retry.attemptCap,
    delayMs: retry.delayMs,
    errorClass: retry.errorClass,
    model: retry.model && retry.model !== primaryModel ? retry.model : undefined,
    groupElapsedMs: retry.groupElapsedMs,
    inProgress: lastFrameAt > retry.receivedAt || now - retry.receivedAt >= retry.delayMs,
  };
  const { level, text } = describeLiveness(now - lastFrameAt, active, runningSubagents, retryWait);
  if (level === "none" || text === null) return null;

  return (
    <div data-testid="liveness-line" className={level === "stalled" ? `${CLASS.line} ${CLASS.stalled}` : CLASS.line}>
      {text}
    </div>
  );
}
