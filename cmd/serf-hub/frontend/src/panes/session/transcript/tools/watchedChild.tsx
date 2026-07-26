// WatchedChildIndicator is the subagent module's live view into a
// running child: an additive watchThread(ref) subscription (stores/
// threads.ts's own sanctioned extension for this stream) driving a
// Cadence trace off the watched model's OWN frameTimes - "Cadence per row
// from the watched model's frameTimes" per the wave-4 plan. Deliberately
// lean: watchThread reads with includeTurns:false, so this only ever has
// live status/liveness to show, never turn content - a subagent row that
// wants richer detail already has "open transcript" (subagentModule.tsx)
// for that.
import { useEffect } from "react";
import { threadsStore, useThreadsStore } from "../../../../stores/threads";
import { Cadence } from "../../../../widgets";
import { cadenceStateForStatus, NOW_TICK_MS, useNowTick } from "../../liveness";
import { rowKindFromChildStatus } from "./subagentModule";
import { setWatchedLiveKind } from "./subagentModuleStore";

export interface WatchedChildIndicatorProps {
  ref: string;
  // scopeKey is a turnScopeKey(sessionRef, turnId) - see subagentModuleStore.ts's
  // own doc comment (kata 8525): a bare turnId is not unique across sessions,
  // and setWatchedLiveKind below writes into that same page-lifetime store.
  scopeKey: string;
  rowKey: string;
}

const EMPTY_FRAME_TIMES: number[] = [];

export function WatchedChildIndicator({ ref: childRef, scopeKey, rowKey }: WatchedChildIndicatorProps) {
  useEffect(() => {
    // Best-effort: a failed watch leaves this indicator rendering nothing
    // rather than crashing the whole subagent module over a live-status
    // nicety - the row's own task/kind/preview (subagentModuleStore, not
    // this watch) still reflect whatever the last known-good state was.
    threadsStore
      .getState()
      .watchThread(childRef)
      .catch(() => {});
    return () => threadsStore.getState().releaseWatchedThread(childRef);
  }, [childRef]);

  const model = useThreadsStore((s) => s.watchedThreads.get(childRef));
  const frameTimes = useThreadsStore((s) => s.watchedFrameTimes.get(childRef) ?? EMPTY_FRAME_TIMES);
  const now = useNowTick(NOW_TICK_MS);

  // Write the live child status back onto the row as its liveKind overlay
  // (yd16), through setWatchedLiveKind's guard (dr7e) so a stale "running"
  // read never resurrects a row a serf/job/finished notification already
  // settled into a terminal kind. Effect-guarded and keyed on the derived
  // liveKind so it fires only on an actual status change - never a
  // render-time store write, which would be an infinite re-render loop
  // (updateSubagentRowIfExists returns a fresh useSubagentRows reference
  // every call).
  const liveKind = model ? rowKindFromChildStatus(model.status.type) : undefined;
  useEffect(() => {
    if (liveKind) setWatchedLiveKind(scopeKey, rowKey, liveKind);
  }, [scopeKey, rowKey, liveKind]);

  if (!model) return null;
  return <Cadence state={cadenceStateForStatus(model.status.type)} frameTimes={frameTimes} now={now} />;
}
