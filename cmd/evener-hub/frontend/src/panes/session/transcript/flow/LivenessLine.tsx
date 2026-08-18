// LivenessLine is the honest, quiet liveness indicator for the transcript
// pane: "Quiet ~30s" rolling to "May be stalled - no updates for 3m 5s", or -
// when the wait has a known explanation - that explanation instead of either:
// "rate limited — attempt 9/11 — retrying in 60s — 14m on this call" while the
// daemon is retrying a model call, or "Waiting on N subagents" while the
// active turn's own delegated children are still running (design brief
// principle 6: a wait explained is not a stall). That decision is driven
// purely by describeLiveness (see liveness.ts); this component's own job is
// sourcing its live inputs: `now` (Session.tsx's own useNowTick value,
// already plumbed there for Cadence, so this never starts a second clock -
// same "no timers, no Date.now()" contract as widgets/cadence's own Cadence),
// the running-children count, a reactive read of subagentModuleStore scoped
// by turnScopeKey(sessionRef, turnId) - see that store's own comment on why a
// bare turn id is never enough (two sessions can share one) - and narrowing
// the raw retry (ThreadModel.modelRetry) into what liveness.ts renders (see
// retryWait below). Renders nothing while level is "none" (fresh/inactive),
// and deliberately carries no animation of its own - Cadence's trace already
// conveys activity; this line's entire job is to say something honest when
// that activity stops.

import type { ModelRetryState } from "../../../../protocol/model";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { turnScopeKey, useSubagentRows } from "../tools/subagentModuleStore";
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
  const rows = useSubagentRows(turnScopeKey(sessionRef, turnId ?? ""));
  // turnId undefined means there is no active turn to ask about (e.g. the
  // brief window between thread/status/changed flipping "active" and
  // turn/started's own activeTurnId landing - see
  // protocol/sendQueueAvailability.ts's note on that same race): zero
  // running children, not a lookup under the empty-string fallback
  // turnScopeKey reserves for exactly this case. displayKind prefers the
  // live overlay over the frozen tool-output kind, mirroring
  // SubagentRowView's own displayKind (subagentModule.tsx) - a faster live
  // watch/notification can know a child is done, or freshly re-running,
  // before that child's own delegate tool call has settled a new output.
  const runningSubagents =
    turnId === undefined ? 0 : rows.filter((row) => (row.liveKind ?? row.kind) === "running").length;
  // Narrows the raw retry into what describeLiveness renders (RetryWait's own
  // doc comment): `model` only when it names a DIFFERENT model than the
  // session's current one - without this the chip cannot distinguish "same
  // model, still failing" from "now trying a fallback" (design doc Component
  // 1). `inProgress` once either a frame has landed since this retry was
  // reported or its own delay has elapsed - the wait is over, so the wait
  // period's countdown gives way to "in progress" rather than the indicator
  // vanishing (the web-only difference from the TUI half of this feature).
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
