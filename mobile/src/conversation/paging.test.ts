// Pure-function tests for the prepend-anchor preservation paging state machine.
// No DOM, no React, no network — every transition is a pure function of the
// previous state and the inputs. Anchor preservation must hold within a 2 CSS
// pixel tolerance so prepended older items do not shift the visible content.

import { describe, expect, it } from "vitest";
import {
  adjustAfterPrepend,
  createPagingState,
  type PagingState,
  recordPrepend,
} from "./paging";

describe("createPagingState", () => {
  it("returns an initial state with no anchor and zero offset", () => {
    const state = createPagingState();
    expect(state.anchorId).toBeNull();
    expect(state.anchorOffset).toBe(0);
    expect(state.pendingPrependCount).toBe(0);
  });
});

describe("recordPrepend", () => {
  it("records the anchor id and its scroll offset before prepending", () => {
    const items = [
      { id: "a", kind: "user" as const, text: "a" },
      { id: "b", kind: "user" as const, text: "b" },
    ];
    const state = recordPrepend(createPagingState(), items, 500);
    // The topmost visible item (items[0] at scrollOffset 500 is the anchor).
    expect(state.anchorId).toBe("a");
    expect(state.anchorOffset).toBe(500);
    expect(state.pendingPrependCount).toBe(0);
  });

  it("records the prepend count when items will be prepended", () => {
    const items = [{ id: "a", kind: "user" as const, text: "a" }];
    const state = recordPrepend(createPagingState(), items, 100, 3);
    expect(state.pendingPrependCount).toBe(3);
  });
});

describe("adjustAfterPrepend", () => {
  it("returns the new offset when no anchor was recorded", () => {
    const state = createPagingState();
    const result = adjustAfterPrepend(state, 200);
    expect(result.offset).toBe(200);
    expect(result.withinTolerance).toBe(true);
  });

  it("preserves the anchor within 2px tolerance", () => {
    const items = [
      { id: "a", kind: "user" as const, text: "a" },
      { id: "b", kind: "user" as const, text: "b" },
    ];
    // Anchor is item "a" at offset 100; 3 items prepended each 50px tall
    // means the anchor moved down by 150px, so new offset is 250.
    const recorded = recordPrepend(createPagingState(), items, 100, 3);
    const result = adjustAfterPrepend(recorded, 250, 50);
    // 250 (new) vs 100+150=250 (expected) → within 2px.
    expect(result.withinTolerance).toBe(true);
    expect(result.offset).toBe(250);
  });

  it("flags drift beyond the 2px tolerance", () => {
    const items = [{ id: "a", kind: "user" as const, text: "a" }];
    const recorded = recordPrepend(createPagingState(), items, 100, 3);
    // Expected: 100 + 3*50 = 250; actual: 300 → 50px drift.
    const result = adjustAfterPrepend(recorded, 300, 50);
    expect(result.withinTolerance).toBe(false);
  });

  it("clears the anchor after adjustment so it is one-shot", () => {
    const items = [{ id: "a", kind: "user" as const, text: "a" }];
    const recorded = recordPrepend(createPagingState(), items, 100, 3);
    const { nextState } = adjustAfterPrepend(recorded, 250, 50);
    expect(nextState.anchorId).toBeNull();
    expect(nextState.pendingPrependCount).toBe(0);
  });
});

describe("session/profile switch resets paging state", () => {
  it("createPagingState is the reset state", () => {
    // A reset is just a fresh state: no anchor, zero offset, zero pending.
    const reset: PagingState = createPagingState();
    expect(reset.anchorId).toBeNull();
    expect(reset.anchorOffset).toBe(0);
    expect(reset.pendingPrependCount).toBe(0);
  });
});
