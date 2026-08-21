// A lean child watch that writes live status back to its delegate row.
import { useEffect } from "react";
import { threadsStore, useThreadsStore } from "../../../../stores/threads";
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

export function WatchedChildIndicator({ ref: childRef, scopeKey, rowKey }: WatchedChildIndicatorProps) {
  useEffect(() => {
    threadsStore
      .getState()
      .watchThread(childRef)
      .catch(() => {});
    return () => threadsStore.getState().releaseWatchedThread(childRef);
  }, [childRef]);

  const model = useThreadsStore((s) => s.watchedThreads.get(childRef));
  const liveKind = model ? rowKindFromChildStatus(model.status.type) : undefined;
  useEffect(() => {
    if (liveKind) setWatchedLiveKind(scopeKey, rowKey, liveKind);
  }, [scopeKey, rowKey, liveKind]);

  return null;
}
