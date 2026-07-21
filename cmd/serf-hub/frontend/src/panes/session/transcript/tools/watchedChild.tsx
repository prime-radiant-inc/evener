// WatchedChildIndicator is the subagent module's live view into a
// running child: an additive watchThread(ref) subscription (stores/
// threads.ts's own sanctioned extension for this stream) driving a
// Cadence trace off the watched model's OWN frameTimes - "Cadence per row
// from the watched model's frameTimes" per the wave-4 plan. Deliberately
// lean: watchThread reads with includeTurns:false, so this only ever has
// live status/liveness to show, never turn content - a subagent row that
// wants richer detail already has "open transcript" (subagentModule.tsx)
// for that.
import { useEffect, useState } from "react";
import { Cadence, type CadenceState } from "../../../../widgets";
import { threadsStore, useThreadsStore } from "../../../../stores/threads";

export interface WatchedChildIndicatorProps {
  ref: string;
}

const EMPTY_FRAME_TIMES: number[] = [];
// Same tick interval as panes/session/Session.tsx's own useNowTick (not
// imported from there - Session.tsx is outside this stream's file
// ownership; this is a deliberate, small, self-contained duplicate of a
// "what time is it" clock, not shared state).
const NOW_TICK_MS = 3_000;

function useNowTick(intervalMs: number): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs]);
  return now;
}

// cadenceStateForStatus mirrors Session.tsx's own wire-status mapping
// (duplicated for the same file-ownership reason as useNowTick above -
// see that function's comment).
function cadenceStateForStatus(type: string): CadenceState {
  switch (type) {
    case "systemError":
      return "failed";
    case "awaiting":
    case "warning":
      return "needs-you";
    case "active":
      return "working";
    case "closed":
      return "ended";
    default:
      return "idle";
  }
}

export function WatchedChildIndicator({ ref: childRef }: WatchedChildIndicatorProps) {
  useEffect(() => {
    // Best-effort: a failed watch leaves this indicator rendering nothing
    // rather than crashing the whole subagent module over a live-status
    // nicety - the row's own task/kind/preview (subagentModuleStore, not
    // this watch) still reflect whatever the last known-good state was.
    threadsStore.getState().watchThread(childRef).catch(() => {});
    return () => threadsStore.getState().releaseWatchedThread(childRef);
  }, [childRef]);

  const model = useThreadsStore((s) => s.watchedThreads.get(childRef));
  const frameTimes = useThreadsStore((s) => s.watchedFrameTimes.get(childRef) ?? EMPTY_FRAME_TIMES);
  const now = useNowTick(NOW_TICK_MS);

  if (!model) return null;
  return <Cadence state={cadenceStateForStatus(model.status.type)} frameTimes={frameTimes} now={now} />;
}
