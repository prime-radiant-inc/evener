// useNearTopLoadOlder: the near-top half of automatic paging, for a surface
// that mounts no scroll coordinator of its own.
//
// The live session pane's useTranscriptScroll owns this rule inline, next to
// the gesture, pill and re-anchor bookkeeping that share its one scroll
// listener. The read-only transcript pane mounts none of that, so it registers
// the same rule here rather than reading the port's geometry only: without it
// an already-overflowing first page's older history is unreachable there,
// because the geometry fill fires only while the port is UNDER-filled.
//
// The rule itself (isNearTop, with NEAR_TOP_THRESHOLD_PX) lives in
// scrollMetrics, so both surfaces page at the same distance from the top.
import { type RefObject, useEffect, useRef } from "react";
import type { VirtualListHandle } from "../../../../widgets/virtuallist";
import { isNearTop, PORT_RETRY_WINDOW_MS } from "./scrollMetrics";

export interface UseNearTopLoadOlderOptions {
  listRef: RefObject<VirtualListHandle | null>;
  loadOlder: () => void;
  /**
   * False until the transcript's list is mounted and has turns to page: the
   * scroll element does not exist before then, and a listener attached to
   * nothing would never re-arm.
   */
  enabled: boolean;
}

export function useNearTopLoadOlder({ listRef, loadOlder, enabled }: UseNearTopLoadOlderOptions): void {
  // Latest-ref so the listener - attached once per mount - never calls a stale
  // loadOlder. Its identity changes on every loadingOlder flip
  // (useTranscript.ts), and a stale closure would read a stale guard.
  const loadOlderRef = useRef(loadOlder);
  loadOlderRef.current = loadOlder;

  useEffect(() => {
    if (!enabled) return undefined;
    let detach: (() => void) | null = null;
    const attach = (): boolean => {
      const el = listRef.current?.getScrollElement();
      if (el === undefined || el === null) return false;
      const onScroll = () => {
        if (isNearTop(el.scrollTop)) loadOlderRef.current();
      };
      el.addEventListener("scroll", onScroll, { passive: true });
      detach = () => el.removeEventListener("scroll", onScroll);
      return true;
    };
    // `enabled` flips in the commit that mounts the list, so the port is
    // normally there already; the bounded retry only keeps a late mount from
    // leaving this surface with no paging trigger at all.
    let frame: number | null = null;
    if (!attach()) {
      const deadline = performance.now() + PORT_RETRY_WINDOW_MS;
      const retry = () => {
        if (attach() || performance.now() > deadline) return;
        frame = requestAnimationFrame(retry);
      };
      frame = requestAnimationFrame(retry);
    }
    return () => {
      if (frame !== null) cancelAnimationFrame(frame);
      detach?.();
    };
  }, [enabled, listRef]);
}
