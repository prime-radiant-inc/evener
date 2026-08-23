// Paging state machine — pure functions for prepend-anchor preservation.
//
// When older items are prepended to the top of the timeline, the scroll offset
// must shift by exactly the height of the prepended content so the visible
// items stay in place. This module records the anchor (the topmost currently
// visible item and its scroll offset) before the prepend, then computes the
// adjusted offset after the virtualizer has laid out the new content.
//
// Tolerance is 2 CSS pixels: if the measured new offset is within 2px of the
// expected offset, the adjustment is treated as correct. A session or profile
// switch resets paging state to the initial value (just call createPagingState).
//
// All functions are pure: given the same inputs they produce the same output.
// No DOM, no clock, no side effects.

// The scroll offset at which the anchor item sits before the prepend. The
// anchor is the topmost visible item; recording it lets the timeline compute
// where that item moved after prepended content pushed it down.
export interface PagingState {
  /** The stable id of the topmost visible item before the prepend, or null. */
  readonly anchorId: string | null;
  /** The scroll offset of the anchor item's top edge before the prepend. */
  readonly anchorOffset: number;
  /** Number of items that will be prepended (for computing the expected shift). */
  readonly pendingPrependCount: number;
}

/** Initial paging state — no anchor, zero offset, zero pending prepend. */
export function createPagingState(): PagingState {
  return { anchorId: null, anchorOffset: 0, pendingPrependCount: 0 };
}

/**
 * Record the anchor before prepending older items. The anchor is the topmost
 * visible item in the current `items` list (items[0]); `scrollOffset` is the
 * current scroll position of the scroller. `itemCount` is the number of items
 * that will be prepended (defaults to 0 for a no-op record).
 */
export function recordPrepend(
  _state: PagingState,
  items: readonly { id: string }[],
  scrollOffset: number,
  itemCount = 0,
): PagingState {
  const anchor = items[0];
  return {
    anchorId: anchor ? anchor.id : null,
    anchorOffset: scrollOffset,
    pendingPrependCount: itemCount,
  };
}

export interface AdjustResult {
  /** The adjusted scroll offset to apply to the scroller. */
  readonly offset: number;
  /** True if the measured offset is within 2px of the expected offset. */
  readonly withinTolerance: boolean;
  /** The next paging state (anchor cleared — adjustment is one-shot). */
  readonly nextState: PagingState;
}

/**
 * Compute the adjusted scroll offset after a prepend. The expected offset is
 * `anchorOffset + pendingPrependCount * itemSize`; if `newScrollOffset` (the
 * measured offset after the virtualizer update) is within 2px of that, the
 * adjustment is within tolerance. The returned `offset` is the expected offset
 * (the value to set on the scroller), and `nextState` clears the anchor so the
 * adjustment is one-shot.
 *
 * When no anchor was recorded (anchorId is null), the new offset is returned
 * as-is and the result is always within tolerance.
 */
export function adjustAfterPrepend(
  state: PagingState,
  newScrollOffset: number,
  itemSize = 0,
): AdjustResult {
  if (state.anchorId === null) {
    return {
      offset: newScrollOffset,
      withinTolerance: true,
      nextState: createPagingState(),
    };
  }
  const expected = state.anchorOffset + state.pendingPrependCount * itemSize;
  const withinTolerance = Math.abs(newScrollOffset - expected) <= 2;
  return {
    offset: expected,
    withinTolerance,
    nextState: createPagingState(),
  };
}
