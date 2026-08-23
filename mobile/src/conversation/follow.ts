// Follow mode state machine — pure functions for auto-scroll-to-bottom logic.
//
// Follow mode tracks whether the timeline should keep the most recent content
// in view. When the user is near the bottom (within a threshold), follow is
// enabled and new items appear without a "new activity" pill. When the user
// scrolls up away from the bottom, follow is disabled and unseen new items
// accumulate behind a pill. Tapping the pill re-enables follow and clears the
// unseen count. A session or profile switch resets to following=true, unseen=0.
//
// All functions are pure: given the same inputs they produce the same output.
// No DOM, no clock, no side effects.

export interface FollowState {
  /** True when the timeline should auto-scroll to keep new content visible. */
  readonly following: boolean;
  /** Count of new items accumulated while not following. */
  readonly unseen: number;
}

/** Initial follow state — following with zero unseen. */
export function createFollowState(): FollowState {
  return { following: true, unseen: 0 };
}

/**
 * Update follow mode from a scroll event. Follow is enabled when the scroller
 * is near the bottom (distance from bottom <= threshold) and disabled when
 * scrolled away. The unseen count is never changed by scrolling — only the
 * new-activity pill tap clears it.
 *
 * `threshold` defaults to 120 (CSS pixels from the bottom edge).
 */
export function onScroll(
  state: FollowState,
  scrollOffset: number,
  viewportHeight: number,
  contentHeight: number,
  threshold = 120,
): FollowState {
  const distanceFromBottom = contentHeight - scrollOffset - viewportHeight;
  const following = distanceFromBottom <= threshold;
  return { following, unseen: state.unseen };
}

/**
 * Record new items arriving. When following, unseen stays zero (the user sees
 * them immediately). When not following, the unseen count increments by `count`.
 */
export function onNewItems(state: FollowState, count: number): FollowState {
  if (state.following) {
    return state;
  }
  return { following: state.following, unseen: state.unseen + count };
}

/**
 * Handle a tap on the new-activity pill: re-enable follow and clear unseen.
 */
export function onTapNewActivity(_state: FollowState): FollowState {
  return { following: true, unseen: 0 };
}

/**
 * Reset to the initial state on session or profile switch.
 */
export function onReset(_state: FollowState): FollowState {
  return createFollowState();
}
