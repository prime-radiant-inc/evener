// useTranscript selects the ThreadModel for `ref` and exposes older-turn
// paging. It does NOT itself call ensureThread/releaseThread - that
// lifecycle (exactly once on mount/unmount) is SessionPane's own concern
// (see its own comment), so this hook stays a plain, side-effect-free
// selector a caller can use without implicitly acquiring the ref.
import { useCallback, useRef, useState } from "react";
import type { ThreadModel } from "../../../protocol/model";
import { threadsStore, useThreadsStore } from "../../../stores/threads";

export interface UseTranscriptResult {
  model: ThreadModel | undefined;
  loadOlder(): Promise<void>; // thread/turns/list via olderCursor -> prependOlderTurns
  loadingOlder: boolean;
  // The fire-and-forget form paging affordances use: same fetch, but the
  // rejection lands in olderError instead of propagating. Both transcript
  // surfaces (the live session pane and the read-only transcript pane) need
  // exactly this, so it lives here rather than being written twice.
  loadOlderReportingError(): void;
  // The last failed older-page fetch's message, or null when the last attempt
  // succeeded or none has been made. Cleared at the start of every attempt, so
  // a retry begins from a clean state.
  olderError: string | null;
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

export function useTranscript(ref: string): UseTranscriptResult {
  const model = useThreadsStore((s) => s.threads.get(ref));
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [olderError, setOlderError] = useState<string | null>(null);
  // The re-entrancy guard is a REF, not the loadingOlder state: two callers in
  // the same tick both read `false` from a state closure and both fire, which
  // is exactly what happened once paging became automatic - the near-top scroll
  // trigger and the sentinel's IntersectionObserver both fired for one scroll
  // and requested the SAME cursor twice (observed live: cursors 132, 102, 102,
  // 72...). A ref is updated synchronously, so the second caller sees the first
  // one's claim. loadingOlder stays as the RENDER signal it always was.
  const inFlightRef = useRef(false);

  const loadOlder = useCallback(async () => {
    if (inFlightRef.current) return; // already in flight - the store's own action has no de-dupe of its own
    inFlightRef.current = true;
    setLoadingOlder(true);
    try {
      // loadOlderTurns itself no-ops (no RPC at all) when the tracked
      // model has no olderCursor - see stores/threads.ts.
      await threadsStore.getState().loadOlderTurns(ref);
    } finally {
      inFlightRef.current = false;
      setLoadingOlder(false);
    }
  }, [ref]);

  const loadOlderReportingError = useCallback(() => {
    setOlderError(null);
    void loadOlder().catch((err) => setOlderError(errorMessage(err)));
  }, [loadOlder]);

  return { model, loadOlder, loadingOlder, loadOlderReportingError, olderError };
}
