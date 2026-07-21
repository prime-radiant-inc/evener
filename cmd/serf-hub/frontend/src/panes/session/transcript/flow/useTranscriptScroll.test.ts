import { act, cleanup, renderHook } from "@testing-library/react";
import { createRef } from "react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import type { ItemModel, ThreadModel, TurnModel } from "../../../../protocol/model";
import { resetThreadsStoreForTests, threadsStore } from "../../../../stores/threads";
import type { VirtualListHandle } from "../../../../widgets/virtuallist";
import type { ScrollMetrics } from "./scrollMetrics";
import { useTranscriptScroll } from "./useTranscriptScroll";

// --- fixtures ------------------------------------------------------------

function item(id: string, turnId: string, overrides: Partial<ItemModel> = {}): ItemModel {
  return { id, turnId, type: "agentMessage", text: "x", status: "completed", ...overrides };
}

function turn(id: string, itemIds: string[], overrides: Partial<TurnModel> = {}): TurnModel {
  return { id, status: "completed", items: itemIds.map((iid) => item(iid, id)), ...overrides };
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
function makeListHandle(): {
  ref: React.RefObject<VirtualListHandle | null>;
  el: HTMLDivElement;
  scrollToIndex: ReturnType<typeof vi.fn>;
  setVisibleRange: (range: { startIndex: number; endIndex: number } | null) => void;
} {
  const el = document.createElement("div");
  const scrollToIndex = vi.fn();
  // getVisibleRange: scriptable, like makeMeasure below - defaults to null
  // ("unknown/not visible"), which is exactly what every scenario that
  // doesn't care about visibility wants (VirtualList itself already proves
  // the REAL getVirtualItems()-backed answer - see virtuallist.test.tsx;
  // this suite proves what the HOOK does with whatever answer it gets).
  let visibleRange: { startIndex: number; endIndex: number } | null = null;
  const ref = createRef<VirtualListHandle>() as React.RefObject<VirtualListHandle | null>;
  (ref as { current: VirtualListHandle }).current = {
    scrollToIndex,
    getScrollElement: () => el,
    getVisibleRange: () => visibleRange,
  };
  return {
    ref,
    el,
    scrollToIndex,
    setVisibleRange: (range) => {
      visibleRange = range;
    },
  };
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

// The error anchor (contracts-transcript-scroll-liveness.md §5, lines
// 113-114): a failed turn arriving while the reader is scrolled away is
// remembered so the pill can point at it and jump straight there, instead
// of the usual "scroll to bottom" - see NewContentPill.tsx for the danger-
// tone rendering this state drives (precedence: error > needs-you > plain
// count, resolved there, not here - the hook exposes independent booleans).
describe("the error anchor (failed turn)", () => {
  test("a failed turn appended while scrolled away becomes the error anchor", () => {
    const { ref } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ m }) => useTranscriptScroll({ ref: "ref_a", model: m, listRef: ref, loadOlder: vi.fn(), measure }),
      { initialProps: { m: model([turn("t1", ["i1"])]) } },
    );

    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"], { status: "failed" })]) });

    expect(result.current.pillError).toBe(true);
  });

  // Wire-true siblings (review finding): the test above constructs t2
  // already-failed-with-an-item in one step. The REAL turn/completed
  // EventError path settles with a BARE stamp instead - itemsView:"", no
  // items array (see reducer.test.ts's own failed-turn coverage) - so
  // itemCount never grows because of the failing turn itself, either at
  // all (this first test) or at the moment it actually fails (the second,
  // which streams a real item first). Both must still anchor.
  test("a turn that fails via a bare stamp (itemsView:'', no items ever - the real wire's EventError shape) still becomes the error anchor", () => {
    const { ref } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ m }) => useTranscriptScroll({ ref: "ref_a", model: m, listRef: ref, loadOlder: vi.fn(), measure }),
      { initialProps: { m: model([turn("t1", ["i1"])]) } },
    );

    rerender({
      m: model([turn("t1", ["i1"]), { id: "t2", status: "failed", items: [], error: { message: "boom" } }]),
    });

    expect(result.current.pillError).toBe(true);
    // No items ever attached to t2 - count stays 0, the failure alone is
    // the news (NewContentPill's own render gate handles this - see its
    // test file).
    expect(result.current.pillCount).toBe(0);
  });

  test("a turn that streamed an item earlier, then settles via a bare failed stamp (no NEW items at the settle itself), still becomes the error anchor", () => {
    const { ref } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ m }) => useTranscriptScroll({ ref: "ref_a", model: m, listRef: ref, loadOlder: vi.fn(), measure }),
      { initialProps: { m: model([turn("t1", ["i1"])]) } },
    );

    // t2 streams one item while inProgress - itemCount grows, so the
    // EXISTING itemCount dependency fires the effect here too. This render
    // must not "use up" its only chance to notice t2's LATER failure - the
    // regression a position-watermark-based scan would miss (it fires here
    // once, finds t2 not-yet-failed, and would never look at t2 again).
    rerender({ m: model([turn("t1", ["i1"]), { id: "t2", status: "inProgress", items: [item("i2", "t2")] }]) });
    expect(result.current.pillError).toBe(false);

    // t2 settles as failed - the settle stamp itself is bare (itemsView:"",
    // no items), so itemCount is UNCHANGED from the line above even though
    // the turn now fails.
    rerender({
      m: model([
        turn("t1", ["i1"]),
        { id: "t2", status: "failed", items: [item("i2", "t2")], error: { message: "boom" } },
      ]),
    });

    expect(result.current.pillError).toBe(true);
  });

  test("a turn carrying an error object (status not necessarily 'failed') also anchors", () => {
    const { ref } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ m }) => useTranscriptScroll({ ref: "ref_a", model: m, listRef: ref, loadOlder: vi.fn(), measure }),
      { initialProps: { m: model([turn("t1", ["i1"])]) } },
    );

    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"], { error: { message: "rate limited" } })]) });

    expect(result.current.pillError).toBe(true);
  });

  test("a failed turn arriving while the reader is at the bottom never creates an anchor", () => {
    const { ref } = makeListHandle();
    const { measure } = makeMeasure(AT_BOTTOM);
    const { result, rerender } = renderHook(
      ({ m }) => useTranscriptScroll({ ref: "ref_a", model: m, listRef: ref, loadOlder: vi.fn(), measure }),
      { initialProps: { m: model([turn("t1", ["i1"])]) } },
    );

    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"], { status: "failed" })]) });

    expect(result.current.pillError).toBe(false);
    expect(result.current.pillCount).toBe(0);
  });

  test("a bare-stamp failure (no item growth at all) arriving at the bottom still never anchors", () => {
    const { ref } = makeListHandle();
    const { measure } = makeMeasure(AT_BOTTOM);
    const { result, rerender } = renderHook(
      ({ m }) => useTranscriptScroll({ ref: "ref_a", model: m, listRef: ref, loadOlder: vi.fn(), measure }),
      { initialProps: { m: model([turn("t1", ["i1"])]) } },
    );

    rerender({
      m: model([turn("t1", ["i1"]), { id: "t2", status: "failed", items: [], error: { message: "boom" } }]),
    });

    expect(result.current.pillError).toBe(false);
  });

  test("the FIRST failed turn is remembered; a later failure does not overwrite the active anchor", () => {
    const { ref, scrollToIndex } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ m }) => useTranscriptScroll({ ref: "ref_a", model: m, listRef: ref, loadOlder: vi.fn(), measure }),
      { initialProps: { m: model([turn("t1", ["i1"])]) } },
    );
    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"], { status: "failed" })]) });
    rerender({
      m: model([
        turn("t1", ["i1"]),
        turn("t2", ["i2"], { status: "failed" }),
        turn("t3", ["i3"], { status: "failed" }),
      ]),
    });
    scrollToIndex.mockClear();

    act(() => result.current.jumpToBottom());

    expect(scrollToIndex).toHaveBeenCalledWith(1, { align: "start" }); // t2 (first), not t3
  });

  test("clicking with an active error anchor jumps to the failed turn's index (align start), not the bottom, and clears the pill", () => {
    const { ref, scrollToIndex } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ m }) => useTranscriptScroll({ ref: "ref_a", model: m, listRef: ref, loadOlder: vi.fn(), measure }),
      { initialProps: { m: model([turn("t1", ["i1"])]) } },
    );
    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"], { status: "failed" })]) });
    expect(result.current.pillError).toBe(true);
    scrollToIndex.mockClear();

    act(() => result.current.jumpToBottom());

    expect(scrollToIndex).toHaveBeenCalledWith(1, { align: "start" });
    expect(result.current.pillError).toBe(false);
    expect(result.current.pillCount).toBe(0);
  });

  test("after jumping to an error anchor, the next append does not auto-stick to bottom (the reader is not actually there)", () => {
    const { ref, scrollToIndex } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ m }) => useTranscriptScroll({ ref: "ref_a", model: m, listRef: ref, loadOlder: vi.fn(), measure }),
      { initialProps: { m: model([turn("t1", ["i1"])]) } },
    );
    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"], { status: "failed" })]) });
    act(() => result.current.jumpToBottom());
    scrollToIndex.mockClear();

    rerender({
      m: model([turn("t1", ["i1"]), turn("t2", ["i2"], { status: "failed" }), turn("t3", ["i3"])]),
    });

    expect(scrollToIndex).not.toHaveBeenCalled();
    expect(result.current.pillCount).toBe(1);
  });

  test("the anchor clears when the failed row scrolls into the visible range on its own, without clearing the rest of the pill", () => {
    const { ref, el, setVisibleRange } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    // SCROLLED_AWAY's scrollTop (0) is also near-top, so the dispatched
    // scroll below fires the existing loadOlder call too - mockResolvedValue
    // (matching the "near-top triggers loadOlder" describe block's own
    // idiom) so its .catch(() => {}) has a real promise to attach to.
    const { result, rerender } = renderHook(
      ({ m }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn().mockResolvedValue(undefined),
          measure,
        }),
      { initialProps: { m: model([turn("t1", ["i1"])]) } },
    );
    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"], { status: "failed" })]) });
    expect(result.current.pillError).toBe(true);

    act(() => {
      setVisibleRange({ startIndex: 1, endIndex: 1 }); // t2 (index 1) now on screen
      el.dispatchEvent(new Event("scroll"));
    });

    expect(result.current.pillError).toBe(false);
    expect(result.current.pillCount).toBe(1); // still unseen - only the ANCHOR cleared, not the whole pill
  });

  test("a scroll that does not cover the anchor's index leaves it set", () => {
    const { ref, el, setVisibleRange } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    // See the loadOlder comment in the previous test - same reason.
    const { result, rerender } = renderHook(
      ({ m }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn().mockResolvedValue(undefined),
          measure,
        }),
      { initialProps: { m: model([turn("t1", ["i1"])]) } },
    );
    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"], { status: "failed" })]) });

    act(() => {
      setVisibleRange({ startIndex: 5, endIndex: 9 }); // t2 (index 1) not in range
      el.dispatchEvent(new Event("scroll"));
    });

    expect(result.current.pillError).toBe(true);
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
      useTranscriptScroll({
        ref: "ref_a",
        model: model([turn("t1", ["i1"])], { askPending: true }),
        listRef: ref,
        loadOlder: vi.fn(),
        measure,
      }),
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
    renderHook(() =>
      useTranscriptScroll({
        ref: "ref_a",
        model: model([turn("t1", ["i1"])], { olderCursor: "cursor" }),
        listRef: ref,
        loadOlder,
        measure,
      }),
    );

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
    renderHook(() =>
      useTranscriptScroll({
        ref: "ref_a",
        model: model([turn("t1", ["i1"])], { olderCursor: "cursor" }),
        listRef: ref,
        loadOlder,
        measure,
      }),
    );

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

  // Not named in the brief's own test list, but a direct consequence of
  // storing the error anchor as an absolute turn INDEX (see "the error
  // anchor" describe block above): a prepend shifts every existing turn's
  // index by the prepended count, exactly like baselineItemCountRef already
  // does for the item count above - an anchor left un-shifted would silently
  // point at the wrong turn (or the wrong row entirely) the next time it's
  // clicked. Covered here rather than skipped since it's the same file,
  // same function, same class of staleness bug the existing prepend tests
  // already guard against for other refs.
  test("a prepend shifts an active error anchor's index to keep pointing at the same turn", () => {
    const { ref, scrollToIndex } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ m }) => useTranscriptScroll({ ref: "ref_a", model: m, listRef: ref, loadOlder: vi.fn(), measure }),
      { initialProps: { m: model([turn("t1", ["i1"])]) } },
    );
    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"], { status: "failed" })]) });
    expect(result.current.pillError).toBe(true);

    // loadOlder prepends t0 above t1 - t2 (the anchor, at index 1) must now
    // read as index 2.
    rerender({ m: model([turn("t0", ["i0"]), turn("t1", ["i1"]), turn("t2", ["i2"], { status: "failed" })]) });
    scrollToIndex.mockClear();

    act(() => result.current.jumpToBottom());

    expect(scrollToIndex).toHaveBeenCalledWith(2, { align: "start" });
  });

  // Review finding follow-up: the error-anchor scan tracks "already
  // accounted for" turns by ID (not a scan position), so a prepend bringing
  // in ALREADY-failed historical turns must explicitly mark them resolved -
  // otherwise a later, unrelated append-triggered scan would find that old
  // history as "the first unresolved failed turn" and wrongly anchor on
  // stale, already-known history instead of (or ahead of) a genuinely new,
  // live failure.
  test("a prepend bringing in an already-failed historical turn does not retroactively anchor it", () => {
    const { ref, scrollToIndex } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ m }) => useTranscriptScroll({ ref: "ref_a", model: m, listRef: ref, loadOlder: vi.fn(), measure }),
      { initialProps: { m: model([turn("t2", ["i2"])]) } },
    );

    // loadOlder pages in t0 (already-failed HISTORY) and t1 above t2 - a
    // past error the reader is only now scrolling up into, not a live event.
    rerender({
      m: model([
        turn("t0", ["i0"], { status: "failed", error: { message: "old" } }),
        turn("t1", ["i1"]),
        turn("t2", ["i2"]),
      ]),
    });
    expect(result.current.pillError).toBe(false);

    // A genuinely NEW, live failure afterward must still anchor correctly -
    // proving the prepend didn't corrupt tracking, just correctly ignored
    // the historical one.
    rerender({
      m: model([
        turn("t0", ["i0"], { status: "failed", error: { message: "old" } }),
        turn("t1", ["i1"]),
        turn("t2", ["i2"]),
        turn("t3", ["i3"], { status: "failed" }),
      ]),
    });
    expect(result.current.pillError).toBe(true);
    scrollToIndex.mockClear();

    act(() => result.current.jumpToBottom());

    expect(scrollToIndex).toHaveBeenCalledWith(3, { align: "start" }); // t3, not the historical t0
  });
});

describe("mount positioning", () => {
  test("a fresh ref with no saved scroll position starts at the bottom", () => {
    const { ref, scrollToIndex } = makeListHandle();
    const { measure } = makeMeasure(AT_BOTTOM);
    renderHook(() =>
      useTranscriptScroll({
        ref: "ref_never_seen",
        model: model([turn("t1", ["i1"]), turn("t2", ["i2"])]),
        listRef: ref,
        loadOlder: vi.fn(),
        measure,
      }),
    );

    expect(scrollToIndex).toHaveBeenCalledWith(1, { align: "end" });
  });

  test("a ref with a saved scroll position restores it instead of defaulting to the bottom", () => {
    threadsStore.getState().setScrollPosition("ref_a", 777);
    const { ref, el, scrollToIndex } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    renderHook(() =>
      useTranscriptScroll({
        ref: "ref_a",
        model: model([turn("t1", ["i1"])]),
        listRef: ref,
        loadOlder: vi.fn(),
        measure,
      }),
    );

    expect(el.scrollTop).toBe(777);
    expect(scrollToIndex).not.toHaveBeenCalled();
  });
});

describe("persisting scroll position", () => {
  test("scrolling writes the position back to the threads store (debounced)", () => {
    vi.useFakeTimers();
    const { ref, el } = makeListHandle();
    const { measure, set } = makeMeasure(SCROLLED_AWAY);
    renderHook(() =>
      useTranscriptScroll({
        ref: "ref_a",
        model: model([turn("t1", ["i1"])]),
        listRef: ref,
        loadOlder: vi.fn(),
        measure,
      }),
    );

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
    const { unmount } = renderHook(() =>
      useTranscriptScroll({
        ref: "ref_a",
        model: model([turn("t1", ["i1"])]),
        listRef: ref,
        loadOlder: vi.fn(),
        measure,
      }),
    );

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
    const { result } = renderHook(() =>
      useTranscriptScroll({ ref: "ref_a", model: undefined, listRef: ref, loadOlder: vi.fn(), measure }),
    );

    expect(result.current.pillCount).toBe(0);
    expect(result.current.pillNeedsYou).toBe(false);
    expect(() => result.current.jumpToBottom()).not.toThrow();
  });

  test("listRef.current null (VirtualList not yet mounted, e.g. an empty transcript): no crash", () => {
    const notMountedRef = createRef<VirtualListHandle>() as React.RefObject<VirtualListHandle | null>;
    const { result } = renderHook(() =>
      useTranscriptScroll({ ref: "ref_a", model: model([]), listRef: notMountedRef, loadOlder: vi.fn() }),
    );

    expect(result.current.pillCount).toBe(0);
  });
});
