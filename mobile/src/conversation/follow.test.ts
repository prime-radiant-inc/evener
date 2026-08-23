// Pure-function tests for the follow-mode state machine.
// Follow mode tracks whether the timeline should auto-scroll to new content
// at the bottom. When the user scrolls up away from the bottom, follow is
// disabled and a "new activity" pill accumulates unseen counts. Tapping the
// pill re-enables follow and clears the unseen count. A session or profile
// switch resets to following=true, unseen=0.

import { describe, expect, it } from "vitest";
import {
  createFollowState,
  type FollowState,
  onNewItems,
  onReset,
  onScroll,
  onTapNewActivity,
} from "./follow";

describe("createFollowState", () => {
  it("starts following with zero unseen", () => {
    const state = createFollowState();
    expect(state.following).toBe(true);
    expect(state.unseen).toBe(0);
  });
});

describe("onScroll", () => {
  it("enables follow when near the bottom (within threshold)", () => {
    // contentHeight=1000, viewportHeight=600, scrollOffset=300 (bottom at 900
    // → distance from bottom = 1000 - 300 - 600 = 100 < 120 threshold).
    const state = onScroll(createFollowState(), 300, 600, 1000, 120);
    expect(state.following).toBe(true);
  });

  it("disables follow when scrolled away from the bottom", () => {
    // distance from bottom = 1000 - 100 - 600 = 300 > 120 → not following.
    const state = onScroll(createFollowState(), 100, 600, 1000, 120);
    expect(state.following).toBe(false);
  });

  it("uses 120 as the default threshold when omitted", () => {
    // distance = 119 < 120 → following.
    const state = onScroll(createFollowState(), 281, 600, 1000);
    expect(state.following).toBe(true);
  });

  it("does not clear unseen count on scroll (only the pill tap clears it)", () => {
    let state: FollowState = { following: false, unseen: 3 };
    state = onScroll(state, 100, 600, 1000, 120);
    expect(state.following).toBe(false);
    expect(state.unseen).toBe(3);
  });

  it("re-enabling follow via scroll does not clear unseen", () => {
    let state: FollowState = { following: false, unseen: 3 };
    state = onScroll(state, 300, 600, 1000, 120);
    expect(state.following).toBe(true);
    // unseen is only cleared by onTapNewActivity, not by scrolling back down.
    expect(state.unseen).toBe(3);
  });
});

describe("onNewItems", () => {
  it("increments unseen when not following", () => {
    const state: FollowState = { following: false, unseen: 2 };
    const next = onNewItems(state, 3);
    expect(next.following).toBe(false);
    expect(next.unseen).toBe(5);
  });

  it("does not increment unseen when following", () => {
    const state: FollowState = { following: true, unseen: 0 };
    const next = onNewItems(state, 3);
    expect(next.following).toBe(true);
    expect(next.unseen).toBe(0);
  });
});

describe("onTapNewActivity", () => {
  it("resets unseen to zero and re-enables follow", () => {
    const state: FollowState = { following: false, unseen: 5 };
    const next = onTapNewActivity(state);
    expect(next.following).toBe(true);
    expect(next.unseen).toBe(0);
  });

  it("is a no-op when already following with zero unseen", () => {
    const state: FollowState = { following: true, unseen: 0 };
    const next = onTapNewActivity(state);
    expect(next.following).toBe(true);
    expect(next.unseen).toBe(0);
  });
});

describe("onReset", () => {
  it("resets to following=true, unseen=0 on session/profile switch", () => {
    const state: FollowState = { following: false, unseen: 7 };
    const next = onReset(state);
    expect(next.following).toBe(true);
    expect(next.unseen).toBe(0);
  });

  it("returns a fresh state matching createFollowState", () => {
    expect(onReset({ following: false, unseen: 7 })).toEqual(
      createFollowState(),
    );
  });
});
