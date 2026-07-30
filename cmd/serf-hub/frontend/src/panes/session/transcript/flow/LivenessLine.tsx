// LivenessLine is the honest, quiet liveness indicator for the transcript
// pane: "Quiet ~30s" rolling to "May be stalled - no updates for 3m 5s", or -
// when the wait has a known explanation - that explanation instead of either:
// "Rate limited - retry 9 of 11, next in 60s" while the daemon is retrying a
// model call, or "Waiting on N subagents" while the active turn's own
// delegated children are still running (design brief principle 6: a wait
// explained is not a stall). That decision is driven purely by
// describeLiveness (see liveness.ts); this component's own job is sourcing
// its live inputs: `now`
// (Session.tsx's own useNowTick value, already plumbed there for Cadence, so
// this never starts a second clock - same "no timers, no Date.now()"
// contract as widgets/cadence's own Cadence) and the running-children count,
// a reactive read of subagentModuleStore scoped by
// turnScopeKey(sessionRef, turnId) - see that store's own comment on why a
// bare turn id is never enough (two sessions can share one). Renders nothing
// while level is "none" (fresh/inactive), and deliberately carries no
// animation of its own - Cadence's trace already conveys activity; this
// line's entire job is to say something honest when that activity stops.

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
   * ThreadModel.modelRetry - the daemon's own report of a model call waiting to
   * be retried. Passed straight through: unlike the running-children count,
   * this needs no client-side derivation, and unlike lastFrameAt it is not a
   * clock. Undefined whenever no retry is pending.
   */
  retry?: RetryWait;
}

const CLASS = {
  line: requireClass(styles.line, "livenessline.module.css", "line"),
  stalled: requireClass(styles.stalled, "livenessline.module.css", "stalled"),
};

export function LivenessLine({ lastFrameAt, now, active, sessionRef, turnId, retry }: LivenessLineProps) {
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
  const { level, text } = describeLiveness(now - lastFrameAt, active, runningSubagents, retry);
  if (level === "none" || text === null) return null;

  return (
    <div data-testid="liveness-line" className={level === "stalled" ? `${CLASS.line} ${CLASS.stalled}` : CLASS.line}>
      {text}
    </div>
  );
}
