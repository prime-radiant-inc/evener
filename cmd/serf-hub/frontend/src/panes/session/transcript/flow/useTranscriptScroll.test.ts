import { afterEach, beforeEach, describe, test, expect, vi } from "vitest";
import { act, cleanup, renderHook } from "@testing-library/react";
import { createRef } from "react";
import type { VirtualListHandle } from "../../../../widgets/virtuallist";
import type { ItemModel, ThreadModel, TurnModel } from "../../../../protocol/model";
import { resetThreadsStoreForTests, threadsStore } from "../../../../stores/threads";
import type { ScrollMetrics } from "./scrollMetrics";
import { useTranscriptScroll } from "./useTranscriptScroll";

// --- fixtures ------------------------------------------------------------

function item(id: string, turnId: string, overrides: Partial<ItemModel> = {}): ItemModel {
  return { id, turnId, type: "agentMessage", text: "x", status: "completed", ...overrides };
}

function turn(id: string, itemIds: string[]): TurnModel {
  return { id, status: "completed", items: itemIds.map((iid) => item(iid, id)) };
}

function model(turns: TurnModel[], overrides: Partial<ThreadModel> = {}): ThreadModel {
  return {
    ref: "ref_a",
    threadId: "thr_a",
    name: "test",
    status: { type: "idle" },
    modelProvider: "anthropic/claude",
    model: "anthropic/claude",
    askPending: false,
    turns,
    queue: null,
    tasks: null,
    pendingEscalations: [],
    lastFrameAt: 0,
    ...overrides,
  };
}

// A fake VirtualListHandle: getScrollElement returns a real (bare) <div> so
// scrollTop assignments are genuinely observable (jsdom's scrollTop is a
// plain, real read/write slot - unlike offsetHeight/scrollHeight/
// clientHeight, which jsdom hardcodes to 0 with no real layout behind them;
// see VirtualList's own test suite doc comment). scrollToIndex is a spy -
// this suite proves WHAT the hook asks the widget to do, not react-virtual's
// own offset math (already covered by virtuallist.test.tsx).
function makeListHandle(): { ref: React.RefObject<VirtualListHandle | null>; el: HTMLDivElement; scrollToIndex: ReturnType<typeof vi.fn> } {
  const el = document.createElement("div");
  const scrollToIndex = vi.fn();
  const ref = createRef<VirtualListHandle>() as React.RefObject<VirtualListHandle | null>;
  (ref as { current: VirtualListHandle }).current = { scrollToIndex, getScrollElement: () => el };
  return { ref, el, scrollToIndex };
}

// The injectable measurement seam (this task's own binding constraint:
// "design the hook so the measurement seam is injectable and honestly test
// the logic" - jsdom performs no real layout). Ignores the element argument
// entirely and returns from test-controlled, freely-mutable state instead of
// jsdom's fixed zeros.
function makeMeasure(initial: ScrollMetrics) {
  let current = initial;
  return {
    measure: () => current,
    set: (next: Partial<ScrollMetrics>) => {
      current = { ...current, ...next };
    },
  };
}

const AT_BOTTOM: ScrollMetrics = { scrollTop: 950, scrollHeight: 1000, clientHeight: 50 };
const SCROLLED_AWAY: ScrollMetrics = { scrollTop: 0, scrollHeight: 5000, clientHeight: 500 };

beforeEach(() => {
  resetThreadsStoreForTests();
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe("stick-to-bottom vs. the new-content pill", () => {
  test("at the bottom before a mutation: the viewport sticks to the newly-last turn, no pill", () => {
    const { ref, scrollToIndex } = makeListHandle();
    const { measure } = makeMeasure(AT_BOTTOM);
    const { result, rerender } = renderHook(
      ({ m }) => useTranscriptScroll({ ref: "ref_a", model: m, listRef: ref, loadOlder: vi.fn(), measure }),
      { initialProps: { m: model([turn("t1", ["i1"])]) } },
    );
    scrollToIndex.mockClear(); // drop the initial-mount positioning call

    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"])]) });

    expect(scrollToIndex).toHaveBeenCalledWith(1, { align: "end" });
    expect(result.current.pillCount).toBe(0);
  });

  test("scrolled away before a mutation: the viewport does not move, and the pill counts the newly-added items", () => {
    const { ref, scrollToIndex } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ m }) => useTranscriptScroll({ ref: "ref_a", model: m, listRef: ref, loadOlder: vi.fn(), measure }),
      { initialProps: { m: model([turn("t1", ["i1"])]) } },
    );
    scrollToIndex.mockClear();

    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2", "i3"])]) });

    expect(scrollToIndex).not.toHaveBeenCalled();
    expect(result.current.pillCount).toBe(2);
  });

  test("scrolled away: consecutive append batches accumulate the pill count", () => {
    const { ref } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ m }) => useTranscriptScroll({ ref: "ref_a", model: m, listRef: ref, loadOlder: vi.fn(), measure }),
      { initialProps: { m: model([turn("t1", ["i1"])]) } },
    );

    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"])]) });
    expect(result.current.pillCount).toBe(1);
    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2", "i3"])]) });
    expect(result.current.pillCount).toBe(2);
  });

  test("streaming text growth within an existing item (no new item) never bumps the pill", () => {
    const { ref } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const streamingItem = item("i1", "t1", { status: "inProgress", pendingText: ["he"] });
    const { result, rerender } = renderHook(
      ({ m }) => useTranscriptScroll({ ref: "ref_a", model: m, listRef: ref, loadOlder: vi.fn(), measure }),
      { initialProps: { m: model([{ id: "t1", status: "inProgress", items: [streamingItem] }]) } },
    );

    const grownItem = { ...streamingItem, pendingText: ["he", "llo"] };
    rerender({ m: model([{ id: "t1", status: "inProgress", items: [grownItem] }]) });

    expect(result.current.pillCount).toBe(0);
  });
});

describe("clearing the pill", () => {
  test("jumpToBottom scrolls to the last turn and clears the pill count", () => {
    const { ref, scrollToIndex } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ m }) => useTranscriptScroll({ ref: "ref_a", model: m, listRef: ref, loadOlder: vi.fn(), measure }),
      { initialProps: { m: model([turn("t1", ["i1"])]) } },
    );
    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"])]) });
    expect(result.current.pillCount).toBe(1);
    scrollToIndex.mockClear();

    act(() => result.current.jumpToBottom());

    expect(scrollToIndex).toHaveBeenCalledWith(1, { align: "end" });
    expect(result.current.pillCount).toBe(0);
  });

  test("a manual scroll back to the bottom clears the pill on its own, without calling jumpToBottom", () => {
    const { ref, el } = makeListHandle();
    const { measure, set } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ m }) => useTranscriptScroll({ ref: "ref_a", model: m, listRef: ref, loadOlder: vi.fn(), measure }),
      { initialProps: { m: model([turn("t1", ["i1"])]) } },
    );
    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"])]) });
    expect(result.current.pillCount).toBe(1);

    act(() => {
      set(AT_BOTTOM);
      el.dispatchEvent(new Event("scroll"));
    });

    expect(result.current.pillCount).toBe(0);
  });
});

describe("the needs-you upgrade", () => {
  test("the pill upgrades to needs-you in place when the status flip lands in a LATER render than the content that produced it", () => {
    const { ref } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ m }) => useTranscriptScroll({ ref: "ref_a", model: m, listRef: ref, loadOlder: vi.fn(), measure }),
      { initialProps: { m: model([turn("t1", ["i1"])], { status: { type: "idle" } }) } },
    );

    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"])], { status: { type: "idle" } }) });
    expect(result.current.pillCount).toBe(1);
    expect(result.current.pillNeedsYou).toBe(false);

    // Same content, later render: status alone flips to awaiting.
    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"])], { status: { type: "awaiting" } }) });

    expect(result.current.pillCount).toBe(1); // unchanged - no new content in this render
    expect(result.current.pillNeedsYou).toBe(true);
  });

  test("askPending alone (independent of status.type) also upgrades the pill", () => {
    const { ref } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ m }) => useTranscriptScroll({ ref: "ref_a", model: m, listRef: ref, loadOlder: vi.fn(), measure }),
      { initialProps: { m: model([turn("t1", ["i1"])], { askPending: false }) } },
    );

    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"])], { askPending: true, status: { type: "idle" } }) });

    expect(result.current.pillNeedsYou).toBe(true);
  });

  test("needsYou is false while the pill is empty (nothing to upgrade)", () => {
    const { ref } = makeListHandle();
    const { measure } = makeMeasure(AT_BOTTOM);
    const { result } = renderHook(() =>
      useTranscriptScroll({ ref: "ref_a", model: model([turn("t1", ["i1"])], { askPending: true }), listRef: ref, loadOlder: vi.fn(), measure }),
    );

    expect(result.current.pillCount).toBe(0);
    expect(result.current.pillNeedsYou).toBe(false);
  });
});

describe("near-top triggers loadOlder", () => {
  test("a scroll event landing near the top calls loadOlder", () => {
    const { ref, el } = makeListHandle();
    const { measure, set } = makeMeasure(SCROLLED_AWAY);
    const loadOlder = vi.fn().mockResolvedValue(undefined);
    renderHook(() => useTranscriptScroll({ ref: "ref_a", model: model([turn("t1", ["i1"])], { olderCursor: "cursor" }), listRef: ref, loadOlder, measure }));

    act(() => {
      set({ scrollTop: 50 });
      el.dispatchEvent(new Event("scroll"));
    });

    expect(loadOlder).toHaveBeenCalled();
  });

  // A rejected loadOlder must not escape as an unhandled rejection -
  // useTranscript.ts's own loadOlder has no catch of its own, so this
  // hook's near-top handler adds one (see the .catch(() => {}) at its call
  // site, matching Session.tsx's own ensureThread(ref).catch(() => {})
  // precedent for the identical shape of gap). A dedicated unit test for
  // this was attempted (a plain expect(loadOlder).toHaveBeenCalled() can't
  // tell a caught rejection apart from an uncaught one, so it was written
  // against Node's own unhandledRejection event instead) but abandoned:
  // vitest's own runner intercepts process-level unhandledRejection
  // dispatch in a way a per-test process.on listener couldn't reliably
  // observe here, so it passed identically whether or not the .catch was
  // actually present - unable to discriminate the bug from the fix, kept
  // out rather than left in as a misleading pass. The full-suite exit code
  // DOES catch this class of regression (confirmed empirically: a
  // genuinely uncaught rejection exits 1 even though the individual test
  // that triggered it "passes") - see this task's own report.
  test("a scroll event NOT near the top does not call loadOlder", () => {
    const { ref, el } = makeListHandle();
    const { measure, set } = makeMeasure(SCROLLED_AWAY);
    const loadOlder = vi.fn().mockResolvedValue(undefined);
    renderHook(() => useTranscriptScroll({ ref: "ref_a", model: model([turn("t1", ["i1"])], { olderCursor: "cursor" }), listRef: ref, loadOlder, measure }));

    act(() => {
      set({ scrollTop: 500 });
      el.dispatchEvent(new Event("scroll"));
    });

    expect(loadOlder).not.toHaveBeenCalled();
  });
});

describe("prepend anchoring (loadOlder resolving)", () => {
  test("a prepend (first turn id changes) does not bump the pill, even while scrolled away", () => {
    const { ref } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ m }) => useTranscriptScroll({ ref: "ref_a", model: m, listRef: ref, loadOlder: vi.fn(), measure }),
      { initialProps: { m: model([turn("t2", ["i2"])]) } },
    );

    // t1 (3 items) prepended above the existing t2 - this must read as
    // history backfilled by loadOlder, not "3 new items arrived below".
    rerender({ m: model([turn("t1", ["i1a", "i1b", "i1c"]), turn("t2", ["i2"])]) });

    expect(result.current.pillCount).toBe(0);
  });

  test("a prepend corrects scrollTop by the height delta, keeping the reader's content visually anchored", () => {
    const { ref, el } = makeListHandle();
    const { measure, set } = makeMeasure({ scrollTop: 200, scrollHeight: 500, clientHeight: 100 });
    const { rerender } = renderHook(
      ({ m }) => useTranscriptScroll({ ref: "ref_a", model: m, listRef: ref, loadOlder: vi.fn(), measure }),
      { initialProps: { m: model([turn("t2", ["i2"])]) } },
    );

    // The prepended content grows the sizer's total height by 300px (500 -> 800).
    set({ scrollHeight: 800 });
    rerender({ m: model([turn("t1", ["i1a", "i1b", "i1c"]), turn("t2", ["i2"])]) });

    expect(el.scrollTop).toBe(500); // 200 + (800 - 500)
  });

  test("an append (no first-turn-id change) does NOT run the prepend scroll correction", () => {
    const { ref, el } = makeListHandle();
    const { measure, set } = makeMeasure(AT_BOTTOM);
    const { rerender } = renderHook(
      ({ m }) => useTranscriptScroll({ ref: "ref_a", model: m, listRef: ref, loadOlder: vi.fn(), measure }),
      { initialProps: { m: model([turn("t1", ["i1"])]) } },
    );
    el.scrollTop = 111; // arbitrary sentinel the stick/no-op path must not touch via the prepend math

    set({ scrollHeight: 2000 });
    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"])]) });

    // The stick-to-bottom path uses scrollToIndex (asserted elsewhere), not
    // a raw scrollTop write - this proves the PREPEND correction code path
    // specifically didn't also fire and stomp scrollTop on an append.
    expect(el.scrollTop).toBe(111);
  });
});

describe("mount positioning", () => {
  test("a fresh ref with no saved scroll position starts at the bottom", () => {
    const { ref, scrollToIndex } = makeListHandle();
    const { measure } = makeMeasure(AT_BOTTOM);
    renderHook(() => useTranscriptScroll({ ref: "ref_never_seen", model: model([turn("t1", ["i1"]), turn("t2", ["i2"])]), listRef: ref, loadOlder: vi.fn(), measure }));

    expect(scrollToIndex).toHaveBeenCalledWith(1, { align: "end" });
  });

  test("a ref with a saved scroll position restores it instead of defaulting to the bottom", () => {
    threadsStore.getState().setScrollPosition("ref_a", 777);
    const { ref, el, scrollToIndex } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    renderHook(() => useTranscriptScroll({ ref: "ref_a", model: model([turn("t1", ["i1"])]), listRef: ref, loadOlder: vi.fn(), measure }));

    expect(el.scrollTop).toBe(777);
    expect(scrollToIndex).not.toHaveBeenCalled();
  });
});

describe("persisting scroll position", () => {
  test("scrolling writes the position back to the threads store (debounced)", () => {
    vi.useFakeTimers();
    const { ref, el } = makeListHandle();
    const { measure, set } = makeMeasure(SCROLLED_AWAY);
    renderHook(() => useTranscriptScroll({ ref: "ref_a", model: model([turn("t1", ["i1"])]), listRef: ref, loadOlder: vi.fn(), measure }));

    act(() => {
      set({ scrollTop: 321 });
      el.dispatchEvent(new Event("scroll"));
    });
    // Not written synchronously - debounced.
    expect(threadsStore.getState().scrollPositions.get("ref_a")).not.toBe(321);

    act(() => vi.runAllTimers());

    expect(threadsStore.getState().scrollPositions.get("ref_a")).toBe(321);
  });

  test("unmounting flushes any pending debounced write immediately, so a fast tab-switch doesn't lose it", () => {
    vi.useFakeTimers();
    const { ref, el } = makeListHandle();
    const { measure, set } = makeMeasure(SCROLLED_AWAY);
    const { unmount } = renderHook(() => useTranscriptScroll({ ref: "ref_a", model: model([turn("t1", ["i1"])]), listRef: ref, loadOlder: vi.fn(), measure }));

    act(() => {
      set({ scrollTop: 654 });
      el.dispatchEvent(new Event("scroll"));
    });

    unmount(); // before the debounce timer would otherwise fire

    expect(threadsStore.getState().scrollPositions.get("ref_a")).toBe(654);
  });
});

describe("no-model / not-yet-mounted safety", () => {
  test("model undefined (thread still loading): no crash, empty result", () => {
    const { ref } = makeListHandle();
    const { measure } = makeMeasure(AT_BOTTOM);
    const { result } = renderHook(() => useTranscriptScroll({ ref: "ref_a", model: undefined, listRef: ref, loadOlder: vi.fn(), measure }));

    expect(result.current.pillCount).toBe(0);
    expect(result.current.pillNeedsYou).toBe(false);
    expect(() => result.current.jumpToBottom()).not.toThrow();
  });

  test("listRef.current null (VirtualList not yet mounted, e.g. an empty transcript): no crash", () => {
    const notMountedRef = createRef<VirtualListHandle>() as React.RefObject<VirtualListHandle | null>;
    const { result } = renderHook(() => useTranscriptScroll({ ref: "ref_a", model: model([]), listRef: notMountedRef, loadOlder: vi.fn() }));

    expect(result.current.pillCount).toBe(0);
  });
});
