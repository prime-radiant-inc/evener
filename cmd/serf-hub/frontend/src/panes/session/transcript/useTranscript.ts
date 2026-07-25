// useTranscript selects the ThreadModel for `ref` and exposes older-turn
// paging. It does NOT itself call ensureThread/releaseThread - that
// lifecycle (exactly once on mount/unmount) is SessionPane's own concern
// (see its own comment), so this hook stays a plain, side-effect-free
// selector a caller can use without implicitly acquiring the ref.
import { useCallback, useState } from "react";
import type { ThreadModel } from "../../../protocol/model";
import { threadsStore, useThreadsStore } from "../../../stores/threads";

export interface UseTranscriptResult {
  model: ThreadModel | undefined;
  loadOlder(): Promise<void>; // thread/turns/list via olderCursor -> prependOlderTurns
  loadingOlder: boolean;
}

export function useTranscript(ref: string): UseTranscriptResult {
  const model = useThreadsStore((s) => s.threads.get(ref));
  const [loadingOlder, setLoadingOlder] = useState(false);

  const loadOlder = useCallback(async () => {
    if (loadingOlder) return; // already in flight - the store's own action has no de-dupe of its own
    setLoadingOlder(true);
    try {
      // loadOlderTurns itself no-ops (no RPC at all) when the tracked
      // model has no olderCursor - see stores/threads.ts.
      await threadsStore.getState().loadOlderTurns(ref);
    } finally {
      setLoadingOlder(false);
    }
  }, [ref, loadingOlder]);

  return { model, loadOlder, loadingOlder };
}
