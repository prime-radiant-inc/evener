// useRailScrollSync — bidirectional sync between the SessionRail's thumb and
// the VirtualList's scroll element. Follow-live arbitration: manual
// interaction (drag/click) disables follow; scroll-to-bottom re-enables it.
//
// The rail IS the scrollbar: the thumb's position maps to the scroll
// element's scrollTop through the live axis math (vY/vIdxY). Dragging the
// thumb updates scrollTop; scrolling the transcript updates the thumb. The
// follow state machine prevents fights between the rail and anchorToEnd.

import { useCallback, useEffect, useRef, useState } from "react";
import type { VirtualListHandle } from "../../../widgets/virtuallist";
import type { RailEvent, RailView } from "./axis";
import { ROW_H, vYidx } from "./axis";
import type { ThumbState } from "./SessionRail";

export interface UseRailScrollSyncOptions {
  /** VirtualList handle — provides getScrollElement() and getVisibleRange(). */
  listRef: React.RefObject<VirtualListHandle | null>;
  /** The current rail view (axis params + now). */
  view: RailView;
  /** The revealed events. */
  events: RailEvent[];
  /** Called when the user drags the thumb or clicks to jump. */
  onJump?: (eventIndex: number) => void;
  /** Injectable scroll metrics for testing (jsdom performs no real layout). */
  getScrollTop?: () => number;
  getViewportHeight?: () => number;
  getRowCount?: () => number;
  scrollTo?: (scrollTop: number) => void;
}

export interface RailScrollSync {
  /** Current thumb state for the canvas renderer. */
  thumb: ThumbState;
  /** Whether follow-live is active. */
  follow: boolean;
  /** Disable follow (call on manual interaction). */
  disableFollow: () => void;
  /** Re-enable follow (call when scrolled to bottom). */
  enableFollow: () => void;
  /** Called by the rail when the user drags the thumb. */
  onThumbDrag: (eventIndex: number) => void;
}

/**
 * Bidirectional sync between the rail thumb and VirtualList scroll.
 *
 * The thumb maps to scrollTop through the live axis math:
 * - thumb.top = vYidx(view, firstVisibleIndex, H)
 * - thumb.bottom = vYidx(view, lastVisibleIndex, H)
 *
 * Dragging the thumb: vIdxY(view, y - grab, H) → scrollToIndex.
 * Scrolling the transcript: read scrollTop → update thumb.
 *
 * Follow-live arbitration: any manual drag/click disables follow;
 * scroll-to-bottom re-enables it (matching anchorToEnd's behavior).
 */
export function useRailScrollSync(opts: UseRailScrollSyncOptions): RailScrollSync {
  const { listRef, view, events, onJump } = opts;
  const [follow, setFollow] = useState(true);
  const [thumb, setThumb] = useState<ThumbState>({ top: 0, bottom: 16, first: 0, vis: 1 });
  const followRef = useRef(true);

  // Read scroll metrics from the VirtualList's scroll element.
  const getScrollTop =
    opts.getScrollTop ??
    (() => {
      const el = listRef.current?.getScrollElement();
      return el?.scrollTop ?? 0;
    });
  const getViewportHeight =
    opts.getViewportHeight ??
    (() => {
      const el = listRef.current?.getScrollElement();
      return el?.clientHeight ?? 0;
    });
  // getRowCount is reserved for P2 visible-range-aware thumb sizing.
  const _getRowCount =
    opts.getRowCount ??
    (() => {
      const range = listRef.current?.getVisibleRange();
      return range ? range.endIndex - range.startIndex + 1 : 1;
    });
  void _getRowCount;
  // scrollTo is reserved for P2 drag-to-scroll; the rail currently uses
  // onJump (which calls scrollToIndex) instead of raw scrollTop.
  const _scrollTo =
    opts.scrollTo ??
    ((scrollTop: number) => {
      const el = listRef.current?.getScrollElement();
      if (el) el.scrollTop = scrollTop;
    });
  void _scrollTo;

  // Update thumb from scroll position.
  const updateThumb = useCallback(() => {
    const scrollTop = getScrollTop();
    const viewportH = getViewportHeight();
    const vis = Math.max(1, Math.ceil(viewportH / ROW_H));
    const first = Math.max(0, Math.floor(scrollTop / ROW_H));
    const maxFirst = Math.max(0, events.length - vis);
    const clampedFirst = Math.min(first, maxFirst);
    const top = events.length > 0 ? vYidx(view, clampedFirst, viewportH, events) : 0;
    const bottom =
      events.length > 0 ? vYidx(view, Math.min(events.length - 1, clampedFirst + vis), viewportH, events) : 0;
    const next: ThumbState = { top, bottom: Math.max(bottom, top + 16), first: clampedFirst, vis };
    // Bail if nothing changed — prevents an infinite updateThumb → setThumb →
    // re-render → updateThumb loop (jsdom: scrollTop/viewportH are always 0).
    setThumb((prev) =>
      prev.top === next.top && prev.bottom === next.bottom && prev.first === next.first && prev.vis === next.vis
        ? prev
        : next,
    );

    // Follow-live: if scrolled to bottom, re-enable follow.
    const el = listRef.current?.getScrollElement();
    if (el && followRef.current === false) {
      const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 2;
      if (atBottom) {
        setFollow(true);
        followRef.current = true;
      }
    }
  }, [view, events, listRef, getScrollTop, getViewportHeight]);

  // Subscribe to scroll events on the VirtualList's scroll element.
  useEffect(() => {
    const el = listRef.current?.getScrollElement();
    if (!el) return;
    el.addEventListener("scroll", updateThumb, { passive: true });
    updateThumb();
    return () => el.removeEventListener("scroll", updateThumb);
  }, [listRef, updateThumb]);

  const disableFollow = useCallback(() => {
    setFollow(false);
    followRef.current = false;
  }, []);

  const enableFollow = useCallback(() => {
    setFollow(true);
    followRef.current = true;
  }, []);

  const onThumbDrag = useCallback(
    (eventIndex: number) => {
      disableFollow();
      onJump?.(eventIndex);
    },
    [disableFollow, onJump],
  );

  return { thumb, follow, disableFollow, enableFollow, onThumbDrag };
}
