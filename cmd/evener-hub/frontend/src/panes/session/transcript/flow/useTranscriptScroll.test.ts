import { act, cleanup, renderHook } from "@testing-library/react";
import { createRef } from "react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import type { ItemModel, ThreadModel, TurnModel } from "../../../../protocol/model";
import type { ThreadCapabilities } from "../../../../protocol/types.gen";
import { resetThreadsStoreForTests } from "../../../../stores/threads";
import type { VirtualListHandle } from "../../../../widgets/virtuallist";
import type { ScrollMetrics } from "./scrollMetrics";
import { resetTranscriptViewRegistryForTests, transitionTranscriptViews } from "./transcriptViewRegistry";
import {
  captureTopAnchor,
  captureTranscriptView,
  restoreTopAnchor,
  useTranscriptScroll,
  useTranscriptViewRegistration,
  type ViewAnchorPosition,
} from "./useTranscriptScroll";

// --- fixtures ------------------------------------------------------------

function item(id: string, turnId: string, overrides: Partial<ItemModel> = {}): ItemModel {
  return { id, turnId, type: "agentMessage", text: "x", status: "completed", ...overrides };
}

function turn(id: string, itemIds: string[], overrides: Partial<TurnModel> = {}): TurnModel {
  return { id, status: "completed", items: itemIds.map((iid) => item(iid, id)), ...overrides };
}

// This suite exercises scroll behavior, not capability gating - every field
// here is false/empty, a plausible-but-inert snapshot.
const NO_CAPABILITIES: ThreadCapabilities = {
  send: false,
  steer: false,
  interrupt: false,
  compact: false,
  clear: false,
  forkFromTurn: false,
  shutdown: false,
  changeModel: false,
  changeVisionModel: false,
  queue: false,
  goal: false,
  rename: false,
};

function model(turns: TurnModel[], overrides: Partial<ThreadModel> = {}): ThreadModel {
  const { jobsTreeRevision = null, ...rest } = overrides;
  return {
    ref: "ref_a",
    threadId: "thr_a",
    name: "test",
    status: { type: "idle" },
    modelProvider: "anthropic/claude",
    model: "anthropic/claude",
    visionModel: "",
    askPending: false,
    turns,
    queue: null,
    tasks: null,
    jobsUpdatedAt: null,
    pendingEscalations: [],
    lastFrameAt: 0,
    capabilities: NO_CAPABILITIES,
    goal: null,
    contextUsed: 0,
    contextWindow: 0,
    contextPressure: 0,
    usage: null,
    workMillis: 0,
    reasoningEffortLevels: [],
    supportsReasoning: false,
    cwd: "/tmp/project",
    ...rest,
    jobsTreeRevision,
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

// jsdom implements no TouchEvent constructor, so a touch is a plain Event
// carrying the one field the hook reads.
function touchEvent(type: string, clientY: number): Event {
  const event = new Event(type, { bubbles: true });
  Object.defineProperty(event, "touches", { value: [{ clientY }] });
  return event;
}

function pointerEvent(type: string, init: PointerEventInit): PointerEvent {
  return new PointerEvent(type, { bubbles: true, ...init });
}

// A mouse drag's events, the shape the pointer path is meant to track.
const MOUSE_DOWN: PointerEventInit = { pointerType: "mouse", button: 0, buttons: 1, isPrimary: true };
const MOUSE_DRAG: PointerEventInit = { pointerType: "mouse", button: -1, buttons: 1, isPrimary: true };

// Native middle-button autoscroll: press, then the port scrolls continuously.
const MIDDLE_DOWN: PointerEventInit = { pointerType: "mouse", button: 1, buttons: 4, isPrimary: true };
const MIDDLE_DRAG: PointerEventInit = { pointerType: "mouse", button: -1, buttons: 4, isPrimary: true };

// jsdom computes no layout, so the port's own geometry is defined outright -
// the marker predicate reads it from the DOM, not through the `measure` seam.
// Kept in step with what `measure` reports, since a browser could not disagree.
function definePort(el: HTMLElement, metrics: ScrollMetrics): void {
  Object.defineProperty(el, "scrollHeight", { value: metrics.scrollHeight, configurable: true });
  Object.defineProperty(el, "clientHeight", { value: metrics.clientHeight, configurable: true });
  el.scrollTop = metrics.scrollTop;
}

// An independently scrollable descendant, like the sandbox-escalation panel
// (tools/sandboxescalation.module.css is overflow-y:auto inside the transcript).
function nestedScroller(port: HTMLElement, metrics: ScrollMetrics): HTMLElement {
  const inner = document.createElement("div");
  inner.style.overflowY = "auto";
  port.appendChild(inner);
  definePort(inner, metrics);
  return inner;
}

const AT_BOTTOM: ScrollMetrics = { scrollTop: 950, scrollHeight: 1000, clientHeight: 50 };
const SCROLLED_AWAY: ScrollMetrics = { scrollTop: 0, scrollHeight: 5000, clientHeight: 500 };

beforeEach(() => {
  resetThreadsStoreForTests();
  resetTranscriptViewRegistryForTests();
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe("stick-to-bottom vs. the new-content pill", () => {
  test("initial end targeting uses the transformed row count", () => {
    const { ref, scrollToIndex } = makeListHandle();
    const { measure } = makeMeasure(AT_BOTTOM);
    renderHook(() =>
      useTranscriptScroll({
        ref: "ref_a",
        model: model([turn("t1", ["i1"]), turn("t2", ["i2"]), turn("t3", ["i3"])]),
        listRef: ref,
        loadOlder: vi.fn(),
        measure,
        renderedRowCount: 1,
        sourceTurnRowIndexes: new Map([
          ["t1", 0],
          ["t2", 0],
          ["t3", 0],
        ]),
      }),
    );

    expect(scrollToIndex).toHaveBeenCalledWith(0, { align: "end" });
  });

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

  test("append-follow targets the final transformed row after three source turns coalesce", () => {
    const { ref, scrollToIndex } = makeListHandle();
    const { measure } = makeMeasure(AT_BOTTOM);
    const { rerender } = renderHook(
      ({ m, rowCount, rowIndexes }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn(),
          measure,
          renderedRowCount: rowCount,
          sourceTurnRowIndexes: rowIndexes,
        }),
      {
        initialProps: {
          m: model([turn("t1", ["i1"]), turn("t2", ["i2"]), turn("t3", ["i3"])]),
          rowCount: 1,
          rowIndexes: new Map([
            ["t1", 0],
            ["t2", 0],
            ["t3", 0],
          ]),
        },
      },
    );
    scrollToIndex.mockClear();

    rerender({
      m: model([turn("t1", ["i1"]), turn("t2", ["i2"]), turn("t3", ["i3"]), turn("t4", ["i4"])]),
      rowCount: 2,
      rowIndexes: new Map([
        ["t1", 0],
        ["t2", 0],
        ["t3", 0],
        ["t4", 1],
      ]),
    });

    expect(scrollToIndex).toHaveBeenCalledWith(1, { align: "end" });
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

// The jump-to-latest pill is a SCROLL-POSITION affordance (docs/web-ui/
// decisions.md: "a jump-to-latest pill when scrolled up"), not a new-content
// counter: it must be on offer whenever the reader is away from the bottom,
// even when nothing new has arrived - and a jump that lands short must leave
// it on offer rather than stranding the reader with no affordance.
describe("the pill while scrolled back (no new content)", () => {
  test("scrolling away from the bottom makes the pill visible even when nothing new arrived", () => {
    const { ref, el } = makeListHandle();
    const { measure, set } = makeMeasure(AT_BOTTOM);
    const { result } = renderHook(() =>
      useTranscriptScroll({
        ref: "ref_a",
        model: model([turn("t1", ["i1"])]),
        listRef: ref,
        loadOlder: vi.fn(() => Promise.resolve()),
        measure,
      }),
    );
    expect(result.current.pillVisible).toBe(false);
    expect(result.current.pillCount).toBe(0);

    act(() => {
      set(SCROLLED_AWAY);
      el.dispatchEvent(new Event("scroll"));
    });

    expect(result.current.pillVisible).toBe(true);
    // Still no count - nothing arrived; the pill is the plain jump-to-latest form.
    expect(result.current.pillCount).toBe(0);
    expect(result.current.pillError).toBe(false);
  });

  test("scrolling back to the bottom hides the pill again", () => {
    const { ref, el } = makeListHandle();
    const { measure, set } = makeMeasure(AT_BOTTOM);
    const { result } = renderHook(() =>
      useTranscriptScroll({
        ref: "ref_a",
        model: model([turn("t1", ["i1"])]),
        listRef: ref,
        loadOlder: vi.fn(() => Promise.resolve()),
        measure,
      }),
    );

    act(() => {
      set(SCROLLED_AWAY);
      el.dispatchEvent(new Event("scroll"));
    });
    expect(result.current.pillVisible).toBe(true);

    act(() => {
      set(AT_BOTTOM);
      el.dispatchEvent(new Event("scroll"));
    });
    expect(result.current.pillVisible).toBe(false);
  });

  test("an attention-worthy thread upgrades the scrolled-back pill to needs-you even at count 0", () => {
    const { ref, el } = makeListHandle();
    const { measure, set } = makeMeasure(AT_BOTTOM);
    const { result, rerender } = renderHook(
      ({ m }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
        }),
      { initialProps: { m: model([turn("t1", ["i1"])]) } },
    );

    act(() => {
      set(SCROLLED_AWAY);
      el.dispatchEvent(new Event("scroll"));
    });
    expect(result.current.pillNeedsYou).toBe(false);

    // The awaiting flip can land after the reader scrolled away (no new
    // items at all) - the visible pill still upgrades in place.
    rerender({ m: model([turn("t1", ["i1"])], { askPending: true }) });
    expect(result.current.pillNeedsYou).toBe(true);
  });

  test("at the bottom, an attention-worthy thread alone does not show the pill", () => {
    const { ref } = makeListHandle();
    const { measure } = makeMeasure(AT_BOTTOM);
    const { result } = renderHook(() =>
      useTranscriptScroll({
        ref: "ref_a",
        model: model([turn("t1", ["i1"])], { askPending: true }),
        listRef: ref,
        loadOlder: vi.fn(() => Promise.resolve()),
        measure,
      }),
    );

    expect(result.current.pillVisible).toBe(false);
    expect(result.current.pillNeedsYou).toBe(false);
  });

  test("a jump that lands short of the bottom leaves the pill on offer instead of stranding the reader", () => {
    const { ref, el } = makeListHandle();
    // Start at the bottom with the pill hidden, then scroll away so the pill
    // appears - the test must prove the JUMP preserves that visibility across
    // a short landing, not merely that a pre-existing pill survives one.
    const { measure, set } = makeMeasure(AT_BOTTOM);
    const { result } = renderHook(() =>
      useTranscriptScroll({
        ref: "ref_a",
        model: model([turn("t1", ["i1"]), turn("t2", ["i2"])]),
        listRef: ref,
        loadOlder: vi.fn(() => Promise.resolve()),
        measure,
      }),
    );

    expect(result.current.pillVisible).toBe(false);

    // Scroll away from the bottom: the pill appears (plain "latest" form).
    act(() => {
      set(SCROLLED_AWAY);
      el.dispatchEvent(new Event("scroll"));
    });
    expect(result.current.pillVisible).toBe(true);

    act(() => result.current.jumpToBottom());
    // The post-jump scroll event reports the short landing - the measure seam
    // stays at SCROLLED_AWAY, simulating the real failure mode, where the
    // virtualizer's estimate-derived landing is corrected by later
    // measurements to somewhere that is NOT the true bottom...
    act(() => {
      el.dispatchEvent(new Event("scroll"));
    });

    // ...and the pill must still be on offer (plain form), not cleared.
    expect(result.current.pillVisible).toBe(true);
  });

  test("an append before the jump's landing is confirmed does not auto-stick on the unconfirmed jump", () => {
    const { ref, el, scrollToIndex } = makeListHandle();
    const { measure, set } = makeMeasure(AT_BOTTOM);
    const { result, rerender } = renderHook(
      ({ m }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
        }),
      { initialProps: { m: model([turn("t1", ["i1"]), turn("t2", ["i2"])]) } },
    );

    // Scroll away: the pill appears and wasAtBottomRef is honestly false.
    act(() => {
      set(SCROLLED_AWAY);
      el.dispatchEvent(new Event("scroll"));
    });
    expect(result.current.pillVisible).toBe(true);

    act(() => result.current.jumpToBottom());
    scrollToIndex.mockClear(); // drop the jump's own scrollToIndex call

    // An item arrives in the click -> landing-confirmation window: it must be
    // counted on the pill, NOT auto-stuck. Auto-sticking here is exactly the
    // yank an optimistic wasAtBottomRef caused - the jump's arrival has not
    // been confirmed by any scroll event yet.
    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"]), turn("t3", ["i3"])]) });
    expect(scrollToIndex).not.toHaveBeenCalled();
    expect(result.current.pillCount).toBe(1);

    // The landing's scroll event confirms arrival at the bottom: the pill
    // clears...
    act(() => {
      set(AT_BOTTOM);
      el.dispatchEvent(new Event("scroll"));
    });
    expect(result.current.pillVisible).toBe(false);
    expect(result.current.pillCount).toBe(0);

    // ...and from then on appends stick to the bottom again.
    scrollToIndex.mockClear();
    rerender({
      m: model([turn("t1", ["i1"]), turn("t2", ["i2"]), turn("t3", ["i3"]), turn("t4", ["i4"])]),
    });
    expect(scrollToIndex).toHaveBeenCalledWith(3, { align: "end" });
  });

  test("a jump with stale at-bottom trackers (DOM moved without a scroll event) still measures the reader as away", () => {
    const { ref, scrollToIndex } = makeListHandle();
    // Mounted at the bottom: both trackers say at-bottom. Then the DOM moves
    // WITHOUT a scroll event (content growth above the viewport, measurement
    // corrections): the seam now reads scrolled-away, but the trackers are
    // stale - exactly the state roborev's race describes.
    const { measure, set } = makeMeasure(AT_BOTTOM);
    const { result, rerender } = renderHook(
      ({ m }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
        }),
      { initialProps: { m: model([turn("t1", ["i1"]), turn("t2", ["i2"])]) } },
    );
    expect(result.current.pillVisible).toBe(false);

    set(SCROLLED_AWAY); // no scroll event: the trackers do not observe this

    // The click's pre-jump measurement is authoritative: the reader is away,
    // so the pill goes on offer immediately...
    act(() => result.current.jumpToBottom());
    expect(result.current.pillVisible).toBe(true);

    // ...and an append in the landing window counts on the pill instead of
    // auto-sticking on the stale at-bottom state.
    scrollToIndex.mockClear();
    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"]), turn("t3", ["i3"])]) });
    expect(scrollToIndex).not.toHaveBeenCalled();
    expect(result.current.pillCount).toBe(1);
  });
});

describe("jumpToBottom landing reliability", () => {
  test("jumpToBottom pins the scroll element to its true DOM maximum, not only the virtualizer's estimate-derived offset", () => {
    const { ref, el, scrollToIndex } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result } = renderHook(() =>
      useTranscriptScroll({
        ref: "ref_a",
        model: model([turn("t1", ["i1"]), turn("t2", ["i2"])]),
        listRef: ref,
        loadOlder: vi.fn(() => Promise.resolve()),
        measure,
      }),
    );
    scrollToIndex.mockClear(); // drop the initial-mount positioning call

    act(() => result.current.jumpToBottom());

    // The virtualizer scroll is still requested (it engages measurement and
    // the end-anchor machinery)...
    expect(scrollToIndex).toHaveBeenCalledWith(1, { align: "end" });
    // ...and the scroll element is pinned to the TRUE bottom by real DOM
    // geometry (scrollHeight - clientHeight), exact regardless of how wrong
    // the virtualizer's estimates for unmeasured rows are.
    expect(el.scrollTop).toBe(SCROLLED_AWAY.scrollHeight - SCROLLED_AWAY.clientHeight);
  });

  // The transcript scroll guard's "pill is visible at mount" failure, root-caused
  // in headless Chrome: the virtualizer's scroll-to-end reconcile loop settles
  // after ONE frame whose target stopped moving (virtual-core 3.17
  // reconcileScroll, STABLE_FRAMES = 1), so a measurement batch landing after
  // that frame grows the content with the loop already torn down. The end-anchor
  // that is meant to hold the end then follows only part of the growth - measured
  // there as scrollHeight 17076 -> 17221 while the offset moved 16374 -> 16432,
  // leaving 87px - and 87px is far past its own 4px threshold, so it disengages
  // for good. The geometry then never moves again (the guard held it still for
  // 3s), and a session the reader has not touched sits short of the latest
  // content offering a jump-to-latest pill.
  test("content measured in below a transcript already at the bottom re-pins to the new bottom rather than reading as the reader leaving", () => {
    const { ref, el } = makeListHandle();
    const { measure, set } = makeMeasure({ scrollTop: 16374, scrollHeight: 17076, clientHeight: 702 });
    const { result } = renderHook(() =>
      useTranscriptScroll({
        ref: "ref_a",
        model: model([turn("t1", ["i1"]), turn("t2", ["i2"])]),
        listRef: ref,
        loadOlder: vi.fn(() => Promise.resolve()),
        measure,
      }),
    );
    el.scrollTop = 16374;
    expect(result.current.pillVisible).toBe(false);

    act(() => {
      el.scrollTop = 16432;
      set({ scrollTop: 16432, scrollHeight: 17221 });
      el.dispatchEvent(new Event("scroll"));
    });

    expect(el.scrollTop).toBe(17221 - 702);
    expect(result.current.pillVisible).toBe(false);
  });

  // The window round 1's geometry-only classifier left open (roborev, medium, on
  // 448e8a4): a native scroll event can coalesce the reader's own upward delta
  // with a virtualizer correction that EXCEEDS it, so the event's net geometry -
  // more content, offset advanced - is byte-identical to a pure correction.
  // Geometry cannot tell those apart at all; only the input that produced the
  // event can, which is why the classifier now takes a reader gesture in the
  // same frame as a veto.
  test("an upward gesture coalesced with a larger forward correction leaves the reader where they scrolled", () => {
    const { ref, el } = makeListHandle();
    const { measure, set } = makeMeasure({ scrollTop: 16374, scrollHeight: 17076, clientHeight: 702 });
    const { result } = renderHook(() =>
      useTranscriptScroll({
        ref: "ref_a",
        model: model([turn("t1", ["i1"]), turn("t2", ["i2"])]),
        listRef: ref,
        loadOlder: vi.fn(() => Promise.resolve()),
        measure,
      }),
    );
    el.scrollTop = 16374;
    expect(result.current.pillVisible).toBe(false);

    act(() => {
      el.dispatchEvent(new WheelEvent("wheel", { deltaY: -120 }));
      // The DOM and the measurement seam move TOGETHER, as they do in a
      // browser: by the time the listener runs, the event's net offset is
      // already committed. The veto's job is not to restore the pre-event
      // offset - nothing can, the reader is where the event left them - it is
      // to leave that offset alone instead of pinning it to the bottom.
      el.scrollTop = 16432;
      set({ scrollTop: 16432, scrollHeight: 17221 });
      el.dispatchEvent(new Event("scroll"));
    });

    expect(el.scrollTop).toBe(16432);
    expect(el.scrollTop).not.toBe(17221 - 702);
    expect(result.current.pillVisible).toBe(true);
  });

  test.each([
    [
      "a touch drag",
      (el: HTMLElement) => {
        el.dispatchEvent(touchEvent("touchstart", 400));
        el.dispatchEvent(touchEvent("touchmove", 460));
      },
    ],
    [
      "a pointer drag",
      (el: HTMLElement) => {
        el.dispatchEvent(pointerEvent("pointerdown", MOUSE_DOWN));
        el.dispatchEvent(pointerEvent("pointermove", MOUSE_DRAG));
      },
    ],
  ])("%s in the same frame vetoes the correction re-pin too", (_label, gesture) => {
    const { ref, el } = makeListHandle();
    const { measure, set } = makeMeasure({ scrollTop: 16374, scrollHeight: 17076, clientHeight: 702 });
    const { result } = renderHook(() =>
      useTranscriptScroll({
        ref: "ref_a",
        model: model([turn("t1", ["i1"]), turn("t2", ["i2"])]),
        listRef: ref,
        loadOlder: vi.fn(() => Promise.resolve()),
        measure,
      }),
    );
    el.scrollTop = 16374;

    act(() => {
      gesture(el);
      el.scrollTop = 16432;
      set({ scrollTop: 16432, scrollHeight: 17221 });
      el.dispatchEvent(new Event("scroll"));
    });

    expect(el.scrollTop).toBe(16432);
    expect(result.current.pillVisible).toBe(true);
  });

  test("a transcript scroll action routed through markGesture vetoes the correction re-pin", () => {
    // The app's own Alt+Arrow scroll chords are dispatched from `window` by
    // useTranscriptScrollKeys, which then writes scrollTop directly - a port
    // listener never sees them (roborev medium 1 on f998582). So the marker is
    // exposed and called at the source instead of inferred from key names.
    const { ref, el } = makeListHandle();
    const { measure, set } = makeMeasure({ scrollTop: 16374, scrollHeight: 17076, clientHeight: 702 });
    const { result } = renderHook(() =>
      useTranscriptScroll({
        ref: "ref_a",
        model: model([turn("t1", ["i1"]), turn("t2", ["i2"])]),
        listRef: ref,
        loadOlder: vi.fn(() => Promise.resolve()),
        measure,
      }),
    );
    el.scrollTop = 16374;

    act(() => {
      result.current.markGesture();
      el.scrollTop = 16432;
      set({ scrollTop: 16432, scrollHeight: 17221 });
      el.dispatchEvent(new Event("scroll"));
    });

    expect(el.scrollTop).toBe(16432);
    expect(result.current.pillVisible).toBe(true);
  });

  test("Space on a focused control inside the transcript is not a scroll - the correction still re-pins", () => {
    // roborev medium 2 on f998582. Space activates a focused button and types a
    // space in the ask dock's input (the port's own trailing row); neither
    // scrolls the transcript. A veto here is a FALSE veto, and a false veto is
    // not neutral - the fall-through marks the reader away from the bottom, so
    // every later correction fails the at-bottom clause and the strand this PR
    // fixes comes back for good.
    const { ref, el } = makeListHandle();
    const { measure, set } = makeMeasure({ scrollTop: 16374, scrollHeight: 17076, clientHeight: 702 });
    renderHook(() =>
      useTranscriptScroll({
        ref: "ref_a",
        model: model([turn("t1", ["i1"]), turn("t2", ["i2"])]),
        listRef: ref,
        loadOlder: vi.fn(() => Promise.resolve()),
        measure,
      }),
    );
    const input = document.createElement("input");
    el.appendChild(input);
    el.scrollTop = 16374;

    act(() => {
      input.dispatchEvent(new KeyboardEvent("keydown", { key: " ", bubbles: true }));
      el.scrollTop = 16432;
      set({ scrollTop: 16432, scrollHeight: 17221 });
      el.dispatchEvent(new Event("scroll"));
    });

    expect(el.scrollTop).toBe(17221 - 702);
  });

  test("a bare click is not a gesture - the correction still re-pins", () => {
    // The veto must stay narrow: a pointerdown with no movement - a click, or
    // the start of a text selection - leaves the mount fix working. A veto that
    // fired on it would put the strand back. (Keystrokes are no longer part of
    // this: the port has no keydown listener at all since the marker moved to
    // useTranscriptScrollKeys, which the Space test above pins.)
    const { ref, el } = makeListHandle();
    const { measure, set } = makeMeasure({ scrollTop: 16374, scrollHeight: 17076, clientHeight: 702 });
    renderHook(() =>
      useTranscriptScroll({
        ref: "ref_a",
        model: model([turn("t1", ["i1"]), turn("t2", ["i2"])]),
        listRef: ref,
        loadOlder: vi.fn(() => Promise.resolve()),
        measure,
      }),
    );
    el.scrollTop = 16374;

    act(() => {
      el.dispatchEvent(pointerEvent("pointerdown", MOUSE_DOWN));
      el.scrollTop = 16432;
      set({ scrollTop: 16432, scrollHeight: 17221 });
      el.dispatchEvent(new Event("scroll"));
    });

    expect(el.scrollTop).toBe(17221 - 702);
  });

  test("a drag released outside the transcript stops marking gestures (roborev low 1)", () => {
    // pointerdown lands on the port, but a mouse pointer gets no implicit
    // capture, so a selection drag released over the composer or another pane
    // never delivers pointerup here. Tracking "a drag is in progress" from those
    // two events alone therefore latches on forever, and every later pointermove
    // over the transcript marks a gesture - which, because a veto marks the
    // reader away from the bottom, disarms the correction for good.
    const { ref, el } = makeListHandle();
    const { measure, set } = makeMeasure({ scrollTop: 16374, scrollHeight: 17076, clientHeight: 702 });
    renderHook(() =>
      useTranscriptScroll({
        ref: "ref_a",
        model: model([turn("t1", ["i1"]), turn("t2", ["i2"])]),
        listRef: ref,
        loadOlder: vi.fn(() => Promise.resolve()),
        measure,
      }),
    );
    el.scrollTop = 16374;

    act(() => {
      el.dispatchEvent(pointerEvent("pointerdown", MOUSE_DOWN));
      // The release happens elsewhere: the port sees no pointerup at all.
    });

    act(() => {
      // A plain move with no button held - the pointer is just passing over.
      el.dispatchEvent(pointerEvent("pointermove", { ...MOUSE_DRAG, buttons: 0 }));
      el.scrollTop = 16432;
      set({ scrollTop: 16432, scrollHeight: 17221 });
      el.dispatchEvent(new Event("scroll"));
    });

    expect(el.scrollTop).toBe(17221 - 702);
  });

  test("a stalled animation frame cannot leave a gesture vetoing later corrections (roborev low 2)", () => {
    // requestAnimationFrame does not run in a hidden tab, so the frame-boundary
    // clear can be arbitrarily late. The gesture is therefore also consumed by
    // the scroll event it explains: here the gesture's own event is still at the
    // bottom (nothing to correct), and the correction that follows must re-pin.
    const raf = vi.spyOn(window, "requestAnimationFrame").mockImplementation(() => 0);
    try {
      const { ref, el } = makeListHandle();
      const { measure, set } = makeMeasure({ scrollTop: 16374, scrollHeight: 17076, clientHeight: 702 });
      renderHook(() =>
        useTranscriptScroll({
          ref: "ref_a",
          model: model([turn("t1", ["i1"]), turn("t2", ["i2"])]),
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
        }),
      );
      el.scrollTop = 16374;

      act(() => {
        el.dispatchEvent(new WheelEvent("wheel", { deltaY: -120 }));
        el.dispatchEvent(new Event("scroll"));
      });

      act(() => {
        el.scrollTop = 16432;
        set({ scrollTop: 16432, scrollHeight: 17221 });
        el.dispatchEvent(new Event("scroll"));
      });

      expect(el.scrollTop).toBe(17221 - 702);
    } finally {
      raf.mockRestore();
    }
  });

  // Every marker that can fire without the port actually moving is a false-veto
  // path, and a false veto disarms the correction until the reader returns to
  // the bottom. These pin the ones that can be made exact from the event alone.
  test.each([
    ["a horizontal wheel", (el: HTMLElement) => el.dispatchEvent(new WheelEvent("wheel", { deltaX: -120, deltaY: 0 }))],
    [
      "a sideways touch drag",
      (el: HTMLElement) => {
        el.dispatchEvent(touchEvent("touchstart", 400));
        el.dispatchEvent(touchEvent("touchmove", 400));
      },
    ],
    [
      // The drag began on the transcript and left it; a later move with a
      // button still held is someone else's drag passing over, not this
      // transcript being scrolled.
      "a drag that left the transcript",
      (el: HTMLElement) => {
        el.dispatchEvent(pointerEvent("pointerdown", MOUSE_DOWN));
        el.dispatchEvent(pointerEvent("pointerleave", MOUSE_DRAG));
        el.dispatchEvent(pointerEvent("pointermove", MOUSE_DRAG));
      },
    ],
  ])("%s does not scroll the transcript - the correction still re-pins", (_label, notAScroll) => {
    const { ref, el } = makeListHandle();
    const { measure, set } = makeMeasure({ scrollTop: 16374, scrollHeight: 17076, clientHeight: 702 });
    renderHook(() =>
      useTranscriptScroll({
        ref: "ref_a",
        model: model([turn("t1", ["i1"]), turn("t2", ["i2"])]),
        listRef: ref,
        loadOlder: vi.fn(() => Promise.resolve()),
        measure,
      }),
    );
    el.scrollTop = 16374;

    act(() => {
      notAScroll(el);
      el.scrollTop = 16432;
      set({ scrollTop: 16432, scrollHeight: 17221 });
      el.dispatchEvent(new Event("scroll"));
    });

    expect(el.scrollTop).toBe(17221 - 702);
  });

  // --- a gesture that cannot move the port is not a gesture ---------------
  //
  // Over-marking is the harmful direction: one false veto records the reader as
  // away from the bottom, which disarms the correction until they return there.
  // These cover input that reaches the port but moves nothing - it is already at
  // that limit, or an independently scrollable descendant consumes it.
  const PORT_AT_BOTTOM: ScrollMetrics = { scrollTop: 16519, scrollHeight: 17221, clientHeight: 702 };
  const PORT_AFTER_GROWTH: ScrollMetrics = { scrollTop: 16577, scrollHeight: 17366, clientHeight: 702 };
  const TRUE_BOTTOM_AFTER_GROWTH = PORT_AFTER_GROWTH.scrollHeight - PORT_AFTER_GROWTH.clientHeight;

  function mountAtBottom(start: ScrollMetrics = PORT_AT_BOTTOM) {
    const { ref, el } = makeListHandle();
    const { measure, set } = makeMeasure(start);
    const view = renderHook(() =>
      useTranscriptScroll({
        ref: "ref_a",
        model: model([turn("t1", ["i1"]), turn("t2", ["i2"])]),
        listRef: ref,
        loadOlder: vi.fn(() => Promise.resolve()),
        measure,
      }),
    );
    definePort(el, start);
    return { el, set, result: view.result };
  }

  /** The late measurement correction the gesture markers must not veto. */
  function landCorrection(el: HTMLElement, set: (m: Partial<ScrollMetrics>) => void) {
    definePort(el, PORT_AFTER_GROWTH);
    set(PORT_AFTER_GROWTH);
    el.dispatchEvent(new Event("scroll"));
  }

  test("a wheel into the bottom the port is already at marks nothing", () => {
    const { el, set } = mountAtBottom();

    act(() => {
      el.dispatchEvent(new WheelEvent("wheel", { deltaY: 120, bubbles: true }));
      landCorrection(el, set);
    });

    expect(el.scrollTop).toBe(TRUE_BOTTOM_AFTER_GROWTH);
  });

  test("a wheel into the top the port is already at marks nothing", () => {
    // A transcript that exactly fits its port is at BOTH limits at once, which
    // is the only way to be at the top with the correction still armed: a
    // scrollable port sitting at the top is not at the bottom, so wasAtBottom is
    // already false and nothing could be vetoed there anyway.
    const fits: ScrollMetrics = { scrollTop: 0, scrollHeight: 702, clientHeight: 702 };
    const { el, set } = mountAtBottom(fits);

    act(() => {
      el.dispatchEvent(new WheelEvent("wheel", { deltaY: -120, bubbles: true }));
      landCorrection(el, set);
    });

    expect(el.scrollTop).toBe(TRUE_BOTTOM_AFTER_GROWTH);
  });

  test("a wheel a nested scroller can still answer marks nothing", () => {
    const { el, set } = mountAtBottom();
    const inner = nestedScroller(el, { scrollTop: 500, scrollHeight: 2000, clientHeight: 400 });

    act(() => {
      inner.dispatchEvent(new WheelEvent("wheel", { deltaY: -120, bubbles: true }));
      landCorrection(el, set);
    });

    expect(el.scrollTop).toBe(TRUE_BOTTOM_AFTER_GROWTH);
  });

  // A nested scroller with only a FEW pixels of room is still a scroller that
  // answers the input. isAtBottom's 4px band is the safe direction on the port
  // (it under-marks), but the unsafe one here: reading "at its limit" lets the
  // walk pass, the port marks, the nested scroller eats the wheel, and the port
  // never moves - the false veto the predicate's own comment rules out.
  //
  // Reachable only downward, and only in this PR's own scenario: the port clause
  // needs room below the port, which means content grew there since the last
  // scroll event while wasAtBottom is still true.
  const NESTED_3PX_FROM_ITS_BOTTOM: ScrollMetrics = { scrollTop: 1597, scrollHeight: 2000, clientHeight: 400 };

  /** Content measured in below the port, with no scroll event yet. */
  function growBelow(el: HTMLElement, set: (m: Partial<ScrollMetrics>) => void) {
    const grown = { ...PORT_AT_BOTTOM, scrollHeight: PORT_AFTER_GROWTH.scrollHeight };
    definePort(el, grown);
    set(grown);
  }

  test("a wheel a nested scroller can still answer by 3px marks nothing", () => {
    const { el, set } = mountAtBottom();
    const inner = nestedScroller(el, NESTED_3PX_FROM_ITS_BOTTOM);

    act(() => {
      growBelow(el, set);
      inner.dispatchEvent(new WheelEvent("wheel", { deltaY: 120, bubbles: true }));
      landCorrection(el, set);
    });

    expect(el.scrollTop).toBe(TRUE_BOTTOM_AFTER_GROWTH);
  });

  test("a finger a nested scroller can still answer by 3px marks nothing", () => {
    const { el, set } = mountAtBottom();
    const inner = nestedScroller(el, NESTED_3PX_FROM_ITS_BOTTOM);

    act(() => {
      growBelow(el, set);
      // Finger moving UP pushes content down: the same direction as the wheel above.
      inner.dispatchEvent(touchEvent("touchstart", 460));
      inner.dispatchEvent(touchEvent("touchmove", 400));
      landCorrection(el, set);
    });

    expect(el.scrollTop).toBe(TRUE_BOTTOM_AFTER_GROWTH);
  });

  test("a wheel over a nested scroller at its OWN limit reaches the port and marks", () => {
    // The suppression must stay narrow: a nested scroller that cannot move in
    // this direction passes the input on, and that really is a reader scroll.
    const { el, set, result } = mountAtBottom();
    const inner = nestedScroller(el, { scrollTop: 0, scrollHeight: 2000, clientHeight: 400 });

    act(() => {
      inner.dispatchEvent(new WheelEvent("wheel", { deltaY: -120, bubbles: true }));
      landCorrection(el, set);
    });

    expect(el.scrollTop).toBe(PORT_AFTER_GROWTH.scrollTop);
    expect(result.current.pillVisible).toBe(true);
  });

  test("a finger swiping into the bottom the port is already at marks nothing", () => {
    const { el, set } = mountAtBottom();

    act(() => {
      // Finger moving UP pushes content down: scrollTop would increase.
      el.dispatchEvent(touchEvent("touchstart", 460));
      el.dispatchEvent(touchEvent("touchmove", 400));
      landCorrection(el, set);
    });

    expect(el.scrollTop).toBe(TRUE_BOTTOM_AFTER_GROWTH);
  });

  test("a finger a nested scroller can still answer marks nothing", () => {
    const { el, set } = mountAtBottom();
    const inner = nestedScroller(el, { scrollTop: 500, scrollHeight: 2000, clientHeight: 400 });

    act(() => {
      inner.dispatchEvent(touchEvent("touchstart", 400));
      inner.dispatchEvent(touchEvent("touchmove", 460));
      landCorrection(el, set);
    });

    expect(el.scrollTop).toBe(TRUE_BOTTOM_AFTER_GROWTH);
  });

  test("a horizontal swipe delivered as BOTH touch and pointer events marks nothing", () => {
    // A finger produces both streams. The touch path ignores sideways movement;
    // the pointer path must not adopt the same finger as a drag and mark it.
    const { el, set } = mountAtBottom();
    const finger: PointerEventInit = { pointerType: "touch", button: 0, buttons: 1, isPrimary: true };

    act(() => {
      el.dispatchEvent(touchEvent("touchstart", 400));
      el.dispatchEvent(pointerEvent("pointerdown", finger));
      el.dispatchEvent(touchEvent("touchmove", 400));
      el.dispatchEvent(pointerEvent("pointermove", { ...finger, button: -1 }));
      landCorrection(el, set);
    });

    expect(el.scrollTop).toBe(TRUE_BOTTOM_AFTER_GROWTH);
  });

  test("a middle-button autoscroll marks, so a correction does not snap the reader back", () => {
    // Middle-button autoscroll produces a STREAM of scroll events, so leaving it
    // unmarked is not a one-frame cost: every correction that lands while the
    // reader is autoscrolling re-pins over them.
    const { el, set, result } = mountAtBottom();

    act(() => {
      el.dispatchEvent(pointerEvent("pointerdown", MIDDLE_DOWN));
      el.dispatchEvent(pointerEvent("pointermove", MIDDLE_DRAG));
      landCorrection(el, set);
    });

    expect(el.scrollTop).toBe(PORT_AFTER_GROWTH.scrollTop);
    expect(result.current.pillVisible).toBe(true);
  });

  test("a ctrl-wheel is zoom, not a scroll - the correction still re-pins", () => {
    // Browser zoom, and what a macOS trackpad pinch arrives as. It never moves
    // the port, so marking it is pure over-marking.
    const { el, set } = mountAtBottom();

    act(() => {
      el.dispatchEvent(new WheelEvent("wheel", { deltaY: -120, ctrlKey: true, bubbles: true }));
      landCorrection(el, set);
    });

    expect(el.scrollTop).toBe(TRUE_BOTTOM_AFTER_GROWTH);
  });

  test("a wheel a descendant already claimed does not reach the port - the correction still re-pins", () => {
    const { el, set } = mountAtBottom();
    // A plain child, deliberately not a scroller: the nested walk must not be
    // what saves this, or the test would pass for the wrong reason.
    const child = document.createElement("div");
    el.appendChild(child);
    child.addEventListener("wheel", (event) => event.preventDefault());

    act(() => {
      child.dispatchEvent(new WheelEvent("wheel", { deltaY: -120, bubbles: true, cancelable: true }));
      landCorrection(el, set);
    });

    expect(el.scrollTop).toBe(TRUE_BOTTOM_AFTER_GROWTH);
  });

  test("a right-button drag marks nothing", () => {
    const { el, set } = mountAtBottom();
    const right: PointerEventInit = { pointerType: "mouse", button: 2, buttons: 2, isPrimary: true };

    act(() => {
      el.dispatchEvent(pointerEvent("pointerdown", right));
      el.dispatchEvent(pointerEvent("pointermove", { ...right, button: -1 }));
      landCorrection(el, set);
    });

    expect(el.scrollTop).toBe(TRUE_BOTTOM_AFTER_GROWTH);
  });

  test("a touch that began outside the port is not marked on its first move, after an earlier touch ended", () => {
    // The "no recorded start" guard only works while lastTouchY is null, so a
    // finished touch has to clear it - otherwise the guard is true exactly once
    // per mount and every later outside-start move compares against a stale Y.
    const { el, set } = mountAtBottom();

    act(() => {
      el.dispatchEvent(touchEvent("touchstart", 400));
      el.dispatchEvent(touchEvent("touchmove", 460));
      // A scroll event that is not a correction consumes that legitimate mark.
      el.dispatchEvent(new Event("scroll"));
      el.dispatchEvent(touchEvent("touchend", 460));
    });

    act(() => {
      el.dispatchEvent(touchEvent("touchmove", 500));
      landCorrection(el, set);
    });

    expect(el.scrollTop).toBe(TRUE_BOTTOM_AFTER_GROWTH);
  });

  // --- a session switch is nobody's gesture -------------------------------
  //
  // A marker exists to veto the event its own gesture caused. The first scroll
  // event a newly-opened session gets is its mount's scroll-to-end - the most
  // consequential one it will ever get, and the one this PR exists to land at
  // the true bottom. No gesture aimed at the previous transcript can be
  // responsible for it, so a switch must not leave a marker standing.
  //
  // requestAnimationFrame is stubbed out in these: the clearing frame not having
  // run is exactly the reachable window (a wheel and a sidebar click in one
  // frame, or a wheel just as the tab is hidden, where rAF does not run at all).
  function mountSwitchable() {
    const { ref, el } = makeListHandle();
    const { measure, set } = makeMeasure(PORT_AT_BOTTOM);
    const view = renderHook(
      ({ r }) =>
        useTranscriptScroll({
          ref: r,
          model: model([turn("t1", ["i1"]), turn("t2", ["i2"])]),
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
        }),
      { initialProps: { r: "ref_a" } },
    );
    definePort(el, PORT_AT_BOTTOM);
    return { el, set, rerender: view.rerender };
  }

  test.each([
    ["a wheel", (el: HTMLElement) => el.dispatchEvent(new WheelEvent("wheel", { deltaY: -120, bubbles: true }))],
    [
      "a finger",
      (el: HTMLElement) => {
        el.dispatchEvent(touchEvent("touchstart", 400));
        el.dispatchEvent(touchEvent("touchmove", 460));
      },
    ],
    [
      "a mouse drag",
      (el: HTMLElement) => {
        el.dispatchEvent(pointerEvent("pointerdown", MOUSE_DOWN));
        el.dispatchEvent(pointerEvent("pointermove", MOUSE_DRAG));
      },
    ],
  ])("%s left pending by a session switch does not veto the new session's correction", (_label, gesture) => {
    const raf = vi.spyOn(window, "requestAnimationFrame").mockImplementation(() => 0);
    try {
      const { el, set, rerender } = mountSwitchable();

      act(() => gesture(el));
      act(() => rerender({ r: "ref_b" }));
      act(() => landCorrection(el, set));

      expect(el.scrollTop).toBe(TRUE_BOTTOM_AFTER_GROWTH);
    } finally {
      raf.mockRestore();
    }
  });

  // The no-op stub the cases above use never schedules a frame, so it cannot see
  // the other half of the switch's cleanup: cancelling the old session's
  // clearing frame AND nulling its handle, so the new session's first gesture
  // can schedule a frame of its own. Without the null, markGesture sees a
  // non-null handle, returns early, and session B's marker is never cleared at
  // all - a permanently pending veto, the failure this PR exists to prevent.
  // This stub keeps the callbacks instead of dropping them, and only ever runs
  // the ones scheduled in a named window, so React's own frames are left alone.
  function captureFrames() {
    const scheduled = new Map<number, FrameRequestCallback>();
    const cancelled: number[] = [];
    let nextHandle = 0;
    const raf = vi.spyOn(window, "requestAnimationFrame").mockImplementation((callback) => {
      nextHandle += 1;
      scheduled.set(nextHandle, callback);
      return nextHandle;
    });
    const caf = vi.spyOn(window, "cancelAnimationFrame").mockImplementation((handle) => {
      cancelled.push(handle);
      scheduled.delete(handle);
    });
    const pending = () => [...scheduled.keys()];
    return {
      cancelled,
      pending,
      /** Handles that appeared while `schedule` ran and are still uncancelled. */
      scheduledBy(schedule: () => void): number[] {
        const before = pending();
        schedule();
        return pending().filter((handle) => !before.includes(handle));
      },
      run(handles: number[]) {
        for (const handle of handles) {
          const callback = scheduled.get(handle);
          scheduled.delete(handle);
          callback?.(0);
        }
      },
      restore() {
        raf.mockRestore();
        caf.mockRestore();
      },
    };
  }

  test("a session switch cancels the old clearing frame and lets the new session schedule its own", () => {
    const frames = captureFrames();
    try {
      const { el, set, rerender } = mountSwitchable();
      const wheelUp = () => el.dispatchEvent(new WheelEvent("wheel", { deltaY: -120, bubbles: true }));

      const forA = frames.scheduledBy(() => act(() => wheelUp()));
      expect(forA).toHaveLength(1);

      act(() => rerender({ r: "ref_b" }));
      expect(frames.cancelled).toContain(forA[0]);

      // The handle was nulled too, so this gesture gets a frame of its own.
      const forB = frames.scheduledBy(() => act(() => wheelUp()));
      expect(forB).toHaveLength(1);

      act(() => frames.run(forB));
      act(() => landCorrection(el, set));

      expect(el.scrollTop).toBe(TRUE_BOTTOM_AFTER_GROWTH);
    } finally {
      frames.restore();
    }
  });

  test("a session switch ends a drag in progress, so a later move marks nothing", () => {
    const raf = vi.spyOn(window, "requestAnimationFrame").mockImplementation(() => 0);
    try {
      const { el, set, rerender } = mountSwitchable();

      act(() => el.dispatchEvent(pointerEvent("pointerdown", MOUSE_DOWN)));
      act(() => rerender({ r: "ref_b" }));
      act(() => {
        // The button is still down, but this drag belongs to the session that
        // is no longer on screen.
        el.dispatchEvent(pointerEvent("pointermove", MOUSE_DRAG));
        landCorrection(el, set);
      });

      expect(el.scrollTop).toBe(TRUE_BOTTOM_AFTER_GROWTH);
    } finally {
      raf.mockRestore();
    }
  });

  test("a session switch forgets the touch position, so a move with no start marks nothing", () => {
    const raf = vi.spyOn(window, "requestAnimationFrame").mockImplementation(() => 0);
    try {
      const { el, set, rerender } = mountSwitchable();

      act(() => el.dispatchEvent(touchEvent("touchstart", 400)));
      act(() => rerender({ r: "ref_b" }));
      act(() => {
        el.dispatchEvent(touchEvent("touchmove", 460));
        landCorrection(el, set);
      });

      expect(el.scrollTop).toBe(TRUE_BOTTOM_AFTER_GROWTH);
    } finally {
      raf.mockRestore();
    }
  });

  test("a reader scrolling back while content is still measuring in keeps their position and gets the pill", () => {
    // The same growth, but the offset moved BACKWARDS - only the reader moves a
    // transcript away from the bottom, so this must not be re-pinned. This is
    // the discriminator the correction above turns on; without it the correction
    // would fire on any scroll-away that happened to carry grown geometry.
    const { ref, el } = makeListHandle();
    const { measure, set } = makeMeasure({ scrollTop: 16374, scrollHeight: 17076, clientHeight: 702 });
    const { result } = renderHook(() =>
      useTranscriptScroll({
        ref: "ref_a",
        model: model([turn("t1", ["i1"]), turn("t2", ["i2"])]),
        listRef: ref,
        loadOlder: vi.fn(() => Promise.resolve()),
        measure,
      }),
    );
    el.scrollTop = 12000;

    act(() => {
      set({ scrollTop: 12000, scrollHeight: 17221 });
      el.dispatchEvent(new Event("scroll"));
    });

    expect(el.scrollTop).toBe(12000);
    expect(result.current.pillVisible).toBe(true);
  });

  test("a scroll port that changed size is not a content correction - the reader keeps the pill", () => {
    // The correction above reads "more content in the same box". A clientHeight
    // that moved means the box itself changed (a resized pane, or geometry
    // measured for the first time), which this must not silently re-pin away
    // from: the reader ends up away from the bottom and needs the offer.
    const { ref, el } = makeListHandle();
    const { measure, set } = makeMeasure({ scrollTop: 16374, scrollHeight: 17076, clientHeight: 702 });
    const { result } = renderHook(() =>
      useTranscriptScroll({
        ref: "ref_a",
        model: model([turn("t1", ["i1"]), turn("t2", ["i2"])]),
        listRef: ref,
        loadOlder: vi.fn(() => Promise.resolve()),
        measure,
      }),
    );
    el.scrollTop = 16374;

    act(() => {
      set({ scrollHeight: 17221, clientHeight: 400 });
      el.dispatchEvent(new Event("scroll"));
    });

    expect(el.scrollTop).toBe(16374);
    expect(result.current.pillVisible).toBe(true);
  });

  test("the error-anchor jump does NOT pin to the bottom - it lands on the failed turn", () => {
    const { ref, el } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ m }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
        }),
      { initialProps: { m: model([turn("t1", ["i1"])]) } },
    );
    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"], { status: "failed" })]) });
    expect(result.current.pillError).toBe(true);

    act(() => result.current.jumpToBottom());

    expect(el.scrollTop).toBe(0);
  });
});

// The error anchor (contracts-transcript-scroll-liveness.md §5, lines
// 113-114): a failed turn arriving while the reader is scrolled away is
// remembered so the pill can point at it and jump straight there, instead
// of the usual "scroll to bottom" - see NewContentPill.tsx for the danger-
// tone rendering this state drives (precedence: error > needs-you > plain
// count, resolved there, not here - the hook exposes independent booleans).
describe("the error anchor (failed turn)", () => {
  test("a failed source turn targets its transformed row, not its source-turn index", () => {
    const { ref, scrollToIndex } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ m, rowCount, rowIndexes }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
          renderedRowCount: rowCount,
          sourceTurnRowIndexes: rowIndexes,
        }),
      {
        initialProps: {
          m: model([turn("t1", ["i1"]), turn("t2", ["i2"]), turn("t3", ["i3"])]),
          rowCount: 1,
          rowIndexes: new Map([
            ["t1", 0],
            ["t2", 0],
            ["t3", 0],
          ]),
        },
      },
    );

    rerender({
      m: model([turn("t1", ["i1"]), turn("t2", ["i2"]), turn("t3", ["i3"], { status: "failed" })]),
      rowCount: 2,
      rowIndexes: new Map([
        ["t1", 0],
        ["t2", 0],
        ["t3", 1],
      ]),
    });

    expect(result.current.pillError).toBe(true);
    act(() => result.current.jumpToBottom());
    expect(scrollToIndex).toHaveBeenCalledWith(1, { align: "start" });
  });

  test("a failed turn appended while scrolled away becomes the error anchor", () => {
    const { ref } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ m }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
        }),
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
      ({ m }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
        }),
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
      ({ m }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
        }),
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
      ({ m }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
        }),
      { initialProps: { m: model([turn("t1", ["i1"])]) } },
    );

    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"], { error: { message: "rate limited" } })]) });

    expect(result.current.pillError).toBe(true);
  });

  test("a failed turn arriving while the reader is at the bottom never creates an anchor", () => {
    const { ref } = makeListHandle();
    const { measure } = makeMeasure(AT_BOTTOM);
    const { result, rerender } = renderHook(
      ({ m }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
        }),
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
      ({ m }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
        }),
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
      ({ m }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
        }),
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

  test("clicking with an active error anchor jumps to the failed turn's index (align start), not the bottom, and clears the error/count state while the pill stays on offer", () => {
    const { ref, scrollToIndex } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ m }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
        }),
      { initialProps: { m: model([turn("t1", ["i1"])]) } },
    );
    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"], { status: "failed" })]) });
    expect(result.current.pillError).toBe(true);
    scrollToIndex.mockClear();

    act(() => result.current.jumpToBottom());

    expect(scrollToIndex).toHaveBeenCalledWith(1, { align: "start" });
    expect(result.current.pillError).toBe(false);
    expect(result.current.pillCount).toBe(0);
    // The anchor jump lands mid-transcript, NOT at the bottom: the plain
    // jump-to-latest pill must remain on offer.
    expect(result.current.pillVisible).toBe(true);
  });

  test("after jumping to an error anchor, the next append does not auto-stick to bottom (the reader is not actually there)", () => {
    const { ref, scrollToIndex } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ m }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
        }),
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
          loadOlder: vi.fn(() => Promise.resolve()).mockResolvedValue(undefined),
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
          loadOlder: vi.fn(() => Promise.resolve()).mockResolvedValue(undefined),
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

  test("the pill's arrow points down when there is no error anchor", () => {
    const { ref, el, setVisibleRange } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ m }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()).mockResolvedValue(undefined),
          measure,
        }),
      { initialProps: { m: model([turn("t1", ["i1"])]) } },
    );

    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"])]) });
    expect(result.current.pillCount).toBe(1);
    expect(result.current.pillError).toBe(false); // No error anchor

    act(() => {
      setVisibleRange({ startIndex: 0, endIndex: 0 });
      el.dispatchEvent(new Event("scroll"));
    });

    expect(result.current.pillArrowDirection).toBe("down");
  });

  test("the pill's arrow points up when the error anchor is above the visible range", () => {
    const { ref, el, setVisibleRange } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ m }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()).mockResolvedValue(undefined),
          measure,
        }),
      { initialProps: { m: model([turn("t1", ["i1"])]) } },
    );

    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"], { status: "failed" })]) });
    expect(result.current.pillError).toBe(true);
    expect(result.current.pillArrowDirection).toBe("down"); // Initially no visible range

    // Scroll so the visible range is far below the anchor (index 1)
    act(() => {
      setVisibleRange({ startIndex: 5, endIndex: 9 });
      el.dispatchEvent(new Event("scroll"));
    });

    expect(result.current.pillArrowDirection).toBe("up"); // Anchor (index 1) is above visible range
  });

  test("clicking the pill clears the error anchor and resets the arrow to down (the next jump heads for the bottom)", () => {
    const { ref, el, setVisibleRange } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ m }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
        }),
      { initialProps: { m: model([turn("t1", ["i1"])]) } },
    );

    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"], { status: "failed" })]) });
    expect(result.current.pillError).toBe(true);

    // Scroll so the anchor (index 1) is above the visible range: arrow up.
    act(() => {
      setVisibleRange({ startIndex: 5, endIndex: 9 });
      el.dispatchEvent(new Event("scroll"));
    });
    expect(result.current.pillArrowDirection).toBe("up");

    // The click jumps to the anchor and clears it; the pill stays visible
    // (still scrolled away) as a plain jump-to-latest pill, whose next jump
    // goes DOWN to the bottom - the arrow must not stay stale at "up".
    act(() => result.current.jumpToBottom());
    expect(result.current.pillError).toBe(false);
    expect(result.current.pillVisible).toBe(true);
    expect(result.current.pillArrowDirection).toBe("down");
  });

  test("the pill's arrow points down when the error anchor is within or below the visible range", () => {
    const { ref, el, setVisibleRange } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ m }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()).mockResolvedValue(undefined),
          measure,
        }),
      { initialProps: { m: model([turn("t1", ["i1"])]) } },
    );

    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"], { status: "failed" })]) });
    expect(result.current.pillError).toBe(true);

    // Scroll so the visible range includes the anchor (index 1)
    act(() => {
      setVisibleRange({ startIndex: 1, endIndex: 1 });
      el.dispatchEvent(new Event("scroll"));
    });

    expect(result.current.pillArrowDirection).toBe("down"); // Anchor is in visible range
  });

  test("the pill's arrow points down when the error anchor is below the visible range", () => {
    const { ref, el, setVisibleRange } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ m }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()).mockResolvedValue(undefined),
          measure,
        }),
      { initialProps: { m: model([turn("t1", ["i1"])]) } },
    );

    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"], { status: "failed" })]) });
    expect(result.current.pillError).toBe(true);

    // Scroll so the visible range is above the anchor (index 1)
    act(() => {
      setVisibleRange({ startIndex: 0, endIndex: 0 });
      el.dispatchEvent(new Event("scroll"));
    });

    expect(result.current.pillArrowDirection).toBe("down"); // Anchor is below visible range
  });
});

describe("the needs-you upgrade", () => {
  test.each(["awaiting", "warning", "restartRequired"] as const)(
    "the %s pill upgrades to needs-you in place when the status flip lands in a LATER render than the content that produced it",
    (statusType) => {
      const { ref } = makeListHandle();
      const { measure } = makeMeasure(SCROLLED_AWAY);
      const { result, rerender } = renderHook(
        ({ m }) =>
          useTranscriptScroll({
            ref: "ref_a",
            model: m,
            listRef: ref,
            loadOlder: vi.fn(() => Promise.resolve()),
            measure,
          }),
        { initialProps: { m: model([turn("t1", ["i1"])], { status: { type: "idle" } }) } },
      );

      rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"])], { status: { type: "idle" } }) });
      expect(result.current.pillCount).toBe(1);
      expect(result.current.pillNeedsYou).toBe(false);

      // Same content, later render: status alone requires attention.
      rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"])], { status: { type: statusType } }) });

      expect(result.current.pillCount).toBe(1); // unchanged - no new content in this render
      expect(result.current.pillNeedsYou).toBe(true);
    },
  );

  test("askPending alone (independent of status.type) also upgrades the pill", () => {
    const { ref } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ m }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
        }),
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
        loadOlder: vi.fn(() => Promise.resolve()),
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
      ({ m }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
        }),
      { initialProps: { m: model([turn("t2", ["i2"])]) } },
    );

    // t1 (3 items) prepended above the existing t2 - this must read as
    // history backfilled by loadOlder, not "3 new items arrived below".
    rerender({ m: model([turn("t1", ["i1a", "i1b", "i1c"]), turn("t2", ["i2"])]) });

    expect(result.current.pillCount).toBe(0);
  });

  test("a prepend does NOT write scrollTop itself - the end-anchored VirtualList owns position anchoring across a prepend", () => {
    // Ownership split (2026-08): the virtualizer's anchorTo:"end" re-anchors
    // the visible row across a prepend using REAL per-item geometry; this
    // hook's old hand-rolled "scrollTop += scrollHeight delta" compensation
    // ran on top of it and double-shifted the viewport. What this hook still
    // owns on a prepend is pure bookkeeping (baseline/pill, error-anchor
    // index shift - the other tests in this block); the DOM's scrollTop is
    // the list's to move, never the hook's. The sentinel below would be
    // stomped by the old math (200 + (800 - 500) = 500); it must survive.
    const { ref, el } = makeListHandle();
    const { measure, set } = makeMeasure({ scrollTop: 200, scrollHeight: 500, clientHeight: 100 });
    const { rerender } = renderHook(
      ({ m }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
        }),
      { initialProps: { m: model([turn("t2", ["i2"])]) } },
    );
    el.scrollTop = 200;

    set({ scrollHeight: 800 });
    rerender({ m: model([turn("t1", ["i1a", "i1b", "i1c"]), turn("t2", ["i2"])]) });

    expect(el.scrollTop).toBe(200);
  });

  test("an append (no first-turn-id change) does NOT run the prepend scroll correction", () => {
    const { ref, el } = makeListHandle();
    const { measure, set } = makeMeasure(AT_BOTTOM);
    const { rerender } = renderHook(
      ({ m }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
        }),
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
      ({ m }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
        }),
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
      ({ m }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
        }),
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
        loadOlder: vi.fn(() => Promise.resolve()),
        measure,
      }),
    );

    expect(scrollToIndex).toHaveBeenCalledWith(1, { align: "end" });
  });

  test("kata cmjb: reopening a session lands at the end even when the viewport was left scrolled away", () => {
    // Pre-cmjb, a per-ref scroll offset persisted across close/reopen
    // (threads.ts scrollPositions + a debounced writer here) and was
    // restored on mount in preference to the bottom. Jesse's call on the
    // kata: clicking into a session defaults to the latest content, always
    // — so the whole persistence was removed, and mount unconditionally
    // scrolls to the last turn. SCROLLED_AWAY metrics stand in for "the
    // reader left this pane mid-history"; el.scrollTop staying 0 proves no
    // stored offset was written back.
    const { ref, el, scrollToIndex } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    renderHook(() =>
      useTranscriptScroll({
        ref: "ref_a",
        model: model([turn("t1", ["i1"]), turn("t2", ["i2"])]),
        listRef: ref,
        loadOlder: vi.fn(() => Promise.resolve()),
        measure,
      }),
    );

    expect(scrollToIndex).toHaveBeenCalledWith(1, { align: "end" });
    expect(el.scrollTop).toBe(0);
  });

  test("hydration opens at the final transcript turn after content becomes available, past an interstitial marker", () => {
    const list = makeListHandle();
    const handle = list.ref.current;
    (list.ref as { current: VirtualListHandle | null }).current = null;
    const { measure } = makeMeasure(AT_BOTTOM);
    const { rerender } = renderHook(
      ({ m }) =>
        useTranscriptScroll({
          ref: "ref_hydrating",
          model: m,
          listRef: list.ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
        }),
      { initialProps: { m: undefined as ThreadModel | undefined } },
    );

    (list.ref as { current: VirtualListHandle | null }).current = handle;
    rerender({
      m: model([turn("t1", ["i1"]), { id: "interstitial", status: "completed", items: [] }, turn("t2", ["i2"])]),
    });

    expect(list.scrollToIndex).toHaveBeenCalledWith(2, { align: "end" });
  });
});

// The Session pane is NOT keyed by ref (DockHost's PaneHost renders
// <Component params=...> with no key), so clicking a different session in the
// sidebar updates params.ref on the SAME mounted component instance. Without
// a reset, useTranscriptScroll's per-mount refs (initializedRef,
// wasAtBottomRef, baselineItemCountRef, firstTurnIdRef,
// resolvedFailedTurnIdsRef, errorAnchorIndex) all persist across the ref
// change, so the new session opens wherever the virtualizer defaults (the
// scroll-to-bottom is skipped) and stick-to-bottom / pill counts are
// computed against the PREVIOUS session's scroll state. These tests prove the
// ref change re-initializes all of that.
describe("ref change on a persistent pane instance (sidebar click to a different session)", () => {
  test("changing ref cancels a pending view anchor before the fresh-open scroll-to-bottom", () => {
    const { ref, el, scrollToIndex } = makeListHandle();
    scrollToIndex.mockImplementation((_index, options) => {
      if (options.align === "end") el.scrollTop = 900;
    });
    const { measure } = makeMeasure({ scrollTop: 300, scrollHeight: 1200, clientHeight: 300 });
    let positions: ViewAnchorPosition[] = [
      { id: "ref-a-tool", sourceIndex: 4, index: 4, offset: -18, height: 40, isMessage: false },
    ];
    const { result, rerender } = renderHook(
      ({ r }) =>
        useTranscriptScroll({
          ref: r,
          model: model([turn("t1", ["i1"])]),
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
          measureAnchors: () => positions,
        }),
      { initialProps: { r: "ref_a" } },
    );

    act(() => result.current.captureViewAnchor());
    positions = [{ id: "ref-b-message", sourceIndex: 5, index: 0, offset: 0, height: 96, isMessage: true }];
    rerender({ r: "ref_b" });
    act(() => result.current.restoreViewAnchorAfterMeasurement());

    expect(el.scrollTop).toBe(900);
  });

  test("changing ref re-runs the scroll-to-bottom for the new session", () => {
    const { ref, scrollToIndex } = makeListHandle();
    const { measure } = makeMeasure(AT_BOTTOM);
    const { rerender } = renderHook(
      ({ r }) =>
        useTranscriptScroll({
          ref: r,
          model: model([turn("t1", ["i1"]), turn("t2", ["i2"])]),
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
        }),
      { initialProps: { r: "ref_a" } },
    );
    // Initial mount scrolled to bottom for ref_a.
    expect(scrollToIndex).toHaveBeenCalledWith(1, { align: "end" });
    scrollToIndex.mockClear();

    // Sidebar click to a different session reuses the same hook instance.
    rerender({ r: "ref_b" });

    // The new session must also land at the bottom.
    expect(scrollToIndex).toHaveBeenCalledWith(1, { align: "end" });
  });

  test("changing ref resets the pill count and baseline, so the new session doesn't inherit the old one's unseen count", () => {
    const { ref, scrollToIndex } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ r, m }) =>
        useTranscriptScroll({ ref: r, model: m, listRef: ref, loadOlder: vi.fn(() => Promise.resolve()), measure }),
      {
        initialProps: {
          r: "ref_a",
          m: model([turn("t1", ["i1"])]),
        },
      },
    );
    // Append while scrolled away -> pill counts for ref_a.
    rerender({ r: "ref_a", m: model([turn("t1", ["i1"]), turn("t2", ["i2"])]) });
    expect(result.current.pillCount).toBe(1);

    // Switch to ref_b (sidebar click). Pill must reset to 0 and the scroll-to-bottom must fire.
    scrollToIndex.mockClear();
    rerender({ r: "ref_b", m: model([turn("t1", ["i1"]), turn("t2", ["i2"])]) });

    expect(result.current.pillCount).toBe(0);
    expect(scrollToIndex).toHaveBeenCalledWith(1, { align: "end" });
  });

  test("changing ref clears an active error anchor so the new session doesn't inherit the old one's failed-turn anchor", () => {
    const { ref, scrollToIndex } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ r, m }) =>
        useTranscriptScroll({ ref: r, model: m, listRef: ref, loadOlder: vi.fn(() => Promise.resolve()), measure }),
      {
        initialProps: {
          r: "ref_a",
          m: model([turn("t1", ["i1"])]),
        },
      },
    );
    // Append a failed turn on ref_a (scrolled away) -> becomes the error anchor.
    rerender({ r: "ref_a", m: model([turn("t1", ["i1"]), turn("t2", ["i2"], { status: "failed" })]) });
    expect(result.current.pillError).toBe(true);

    // Switch to ref_b (no failed turn). The error anchor must clear.
    scrollToIndex.mockClear();
    rerender({ r: "ref_b", m: model([turn("t1", ["i1"]), turn("t2", ["i2"])]) });

    expect(result.current.pillError).toBe(false);
    expect(scrollToIndex).toHaveBeenCalledWith(1, { align: "end" });
  });
});

// A same-ref remount (the model briefly goes undefined - e.g. a store resync
// that clears the thread, or the same ref re-hydrating - so VirtualList
// unmounts then remounts) must also re-run the scroll-to-bottom.
// initializedRef was left true from the first hydration; without a reset the
// remount skips the scroll-to-bottom and strands the reader at the top.
describe("same-ref remount (model undefined -> defined on the same ref)", () => {
  test("re-hydrating the same ref after the model went undefined re-runs the scroll-to-bottom", () => {
    const list = makeListHandle();
    const handle = list.ref.current;
    const { measure } = makeMeasure(AT_BOTTOM);
    const { result, rerender } = renderHook(
      ({ m }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: list.ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
        }),
      { initialProps: { m: model([turn("t1", ["i1"]), turn("t2", ["i2"])]) as ThreadModel | undefined } },
    );
    // Initial mount scrolled to bottom.
    expect(list.scrollToIndex).toHaveBeenCalledWith(1, { align: "end" });
    list.scrollToIndex.mockClear();

    // Model goes undefined (VirtualList unmounts). listRef reads null.
    (list.ref as { current: VirtualListHandle | null }).current = null;
    rerender({ m: undefined as ThreadModel | undefined });
    expect(result.current.pillCount).toBe(0);

    // Model re-hydrates (VirtualList remounts). Must scroll to bottom again.
    (list.ref as { current: VirtualListHandle | null }).current = handle;
    rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"])]) });

    expect(list.scrollToIndex).toHaveBeenCalledWith(1, { align: "end" });
  });
});

describe("view-mode anchor preservation", () => {
  test("exact and nearest restoration stay within transformed row indexes", () => {
    const transformedAnchors: ViewAnchorPosition[] = [
      { id: "tool-1", sourceIndex: 1, index: 0, offset: 0, isMessage: false },
      { id: "agent-2", sourceIndex: 2, index: 0, offset: 0, isMessage: true },
      { id: "agent-4", sourceIndex: 4, index: 1, offset: 0, isMessage: true },
    ];
    const firstAnchor = transformedAnchors[0];
    if (!firstAnchor) throw new Error("missing transformed test anchor");

    expect(restoreTopAnchor(captureTopAnchor(firstAnchor), transformedAnchors)).toEqual({
      id: "tool-1",
      index: 0,
      offset: 0,
    });
    expect(
      restoreTopAnchor(
        captureTopAnchor({ id: "hidden", sourceIndex: 3, index: 0, offset: 18, isMessage: false }),
        transformedAnchors,
      ),
    ).toEqual({ id: "agent-2", index: 0, offset: 18 });
    expect(transformedAnchors.every((anchor) => anchor.index >= 0 && anchor.index < 2)).toBe(true);
  });

  test("an anchor captured on a call that has since folded restores to its run row", () => {
    // roborev on PR #947: only the first folded entry's id used to map to the
    // run; the second and third had no anchor to restore to.
    const positions: ViewAnchorPosition[] = [
      { id: "run:a", sourceIndex: 0, index: 0, offset: 0, isMessage: false, members: ["a", "b", "c"] },
      { id: "agent-4", sourceIndex: 4, index: 1, offset: 0, isMessage: true },
    ];
    expect(
      restoreTopAnchor(
        captureTopAnchor({ id: "b", sourceIndex: 1, index: 0, offset: 12, isMessage: false }),
        positions,
      ),
    ).toEqual({ id: "run:a", index: 0, offset: 12 });
  });

  // roborev on PR #947 (round seven): one source item wears a different id
  // per view - intent:<id> in Intent, <id> in Tools/Full, run:<id> once it
  // folds - so a restore across a view change has to match by source
  // identity (with the source index agreeing), not by the literal id.
  test("a position captured on an intent proxy restores to the same source item in a tool-call view", () => {
    const positions: ViewAnchorPosition[] = [
      { id: "a", sourceIndex: 0, index: 0, offset: 0, isMessage: false },
      { id: "b", sourceIndex: 1, index: 1, offset: 0, isMessage: false },
      { id: "agent-4", sourceIndex: 4, index: 2, offset: 0, isMessage: true },
    ];
    expect(
      restoreTopAnchor(
        captureTopAnchor({ id: "intent:b", sourceIndex: 1, index: 0, offset: 7, isMessage: false }),
        positions,
      ),
    ).toEqual({ id: "b", index: 1, offset: 7 });
  });

  test("a position captured on an intent proxy restores to the folded run that now holds its source item", () => {
    const positions: ViewAnchorPosition[] = [
      { id: "run:a", sourceIndex: 0, index: 0, offset: 0, isMessage: false, members: ["a", "b", "c"] },
      { id: "agent-4", sourceIndex: 4, index: 1, offset: 0, isMessage: true },
    ];
    expect(
      restoreTopAnchor(
        captureTopAnchor({ id: "intent:b", sourceIndex: 1, index: 0, offset: 7, isMessage: false }),
        positions,
      ),
    ).toEqual({ id: "run:a", index: 0, offset: 7 });
  });

  test("a tool-call view position restores to its intent proxy in Intent view", () => {
    const positions: ViewAnchorPosition[] = [
      { id: "intent:b", sourceIndex: 1, index: 3, offset: 0, isMessage: false },
      { id: "agent-4", sourceIndex: 4, index: 4, offset: 0, isMessage: true },
    ];
    expect(
      restoreTopAnchor(captureTopAnchor({ id: "b", sourceIndex: 1, index: 0, offset: 7, isMessage: false }), positions),
    ).toEqual({ id: "intent:b", index: 3, offset: 7 });
  });

  test("the same identity at a different source index is another item: the nearest message wins instead", () => {
    const positions: ViewAnchorPosition[] = [
      { id: "agent-0", sourceIndex: 0, index: 0, offset: 0, isMessage: true },
      { id: "intent:b", sourceIndex: 5, index: 1, offset: 0, isMessage: false },
    ];
    expect(
      restoreTopAnchor(captureTopAnchor({ id: "b", sourceIndex: 1, index: 0, offset: 7, isMessage: false }), positions),
    ).toEqual({ id: "agent-0", index: 0, offset: 7 });
  });

  test("captures and restores the same stable entry and viewport offset", () => {
    const anchor = captureTopAnchor({ id: "turn-4", sourceIndex: 4, index: 4, offset: 18, isMessage: true });

    expect(restoreTopAnchor(anchor, [{ id: "turn-4", sourceIndex: 4, index: 2, offset: 18, isMessage: true }])).toEqual(
      { id: "turn-4", index: 2, offset: 18 },
    );
  });

  test("captures the stable row crossing the viewport top, not the first rendered overscan row", () => {
    const { ref } = makeListHandle();
    const { measure } = makeMeasure({ scrollTop: 500, scrollHeight: 2000, clientHeight: 400 });
    let positions: ViewAnchorPosition[] = [
      { id: "overscan-1", sourceIndex: 1, index: 1, offset: -220, height: 80, isMessage: true },
      { id: "turn-4", sourceIndex: 4, index: 4, offset: -18, height: 96, isMessage: true },
      { id: "turn-5", sourceIndex: 5, index: 5, offset: 78, height: 96, isMessage: true },
    ];
    const { result, rerender } = renderHook(
      ({ viewKey }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: model([turn("t1", ["i1"])]),
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
          viewKey,
          measureAnchors: () => positions,
        }),
      { initialProps: { viewKey: "everything" } },
    );
    const el = ref.current?.getScrollElement();
    if (el) el.scrollTop = 500;

    act(() => result.current.captureViewAnchor());
    positions = [{ id: "turn-4", sourceIndex: 4, index: 2, offset: -70, height: 96, isMessage: true }];
    rerender({ viewKey: "intent" });

    expect(ref.current?.getScrollElement()?.scrollTop).toBe(448);
  });

  test("falls forward when the following user or agent entry is the nearest surviving message", () => {
    const anchor = captureTopAnchor({ id: "tool-4", sourceIndex: 4, index: 4, offset: 18, isMessage: false });

    expect(
      restoreTopAnchor(anchor, [
        { id: "user-2", sourceIndex: 2, index: 1, offset: 70, isMessage: true },
        { id: "agent-5", sourceIndex: 5, index: 2, offset: -30, isMessage: true },
      ]),
    ).toEqual({ id: "agent-5", index: 2, offset: 18 });
  });

  test("a hidden anchor falls back to the preceding message when it is closer than the following message", () => {
    const anchor = captureTopAnchor({
      id: "tool-9",
      sourceIndex: 9,
      index: 9,
      offset: 18,
      height: 40,
      isMessage: false,
    });

    expect(
      restoreTopAnchor(anchor, [
        { id: "user-8", sourceIndex: 8, index: 3, offset: 70, height: 40, isMessage: true },
        { id: "agent-12", sourceIndex: 12, index: 4, offset: -30, height: 40, isMessage: true },
      ]),
    ).toEqual({ id: "user-8", index: 3, offset: 18 });
  });

  test("a mode switch restores the stable entry after hidden tool rows change the list height", () => {
    const { ref, el, scrollToIndex } = makeListHandle();
    const { measure } = makeMeasure({ scrollTop: 300, scrollHeight: 1200, clientHeight: 300 });
    let positions: ViewAnchorPosition[] = [{ id: "turn-4", sourceIndex: 4, index: 4, offset: 18, isMessage: true }];
    const measureAnchors = () => positions;
    const { result, rerender } = renderHook(
      ({ viewKey }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: model([turn("t1", ["i1"]), turn("turn-4", ["i4"])]),
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
          viewKey,
          measureAnchors,
        }),
      { initialProps: { viewKey: "everything" } },
    );
    scrollToIndex.mockClear();
    el.scrollTop = 300;

    act(() => result.current.captureViewAnchor());
    positions = [{ id: "turn-4", sourceIndex: 4, index: 1, offset: -82, isMessage: true }];
    rerender({ viewKey: "intent" });

    expect(scrollToIndex).not.toHaveBeenCalled();
    expect(el.scrollTop).toBe(200);
  });

  test("a mixed turn restores its actual top-visible message instead of the turn's first entry", () => {
    const { ref, el, scrollToIndex } = makeListHandle();
    const { measure } = makeMeasure({ scrollTop: 300, scrollHeight: 1200, clientHeight: 300 });
    let positions: ViewAnchorPosition[] = [
      { id: "user-1", sourceIndex: 8, index: 3, offset: -240, height: 60, isMessage: true },
      { id: "tool-1", sourceIndex: 9, index: 3, offset: -180, height: 162, isMessage: false },
      { id: "agent-1", sourceIndex: 10, index: 3, offset: -18, height: 96, isMessage: true },
    ];
    const anchorEntries = [
      { id: "user-1", sourceIndex: 8, index: 3, isMessage: true },
      { id: "tools:tool-1:tool-1", sourceIndex: 9, index: 3, isMessage: false },
      { id: "agent-1", sourceIndex: 10, index: 3, isMessage: true },
    ];
    const { result, rerender } = renderHook(
      ({ viewKey }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: model([turn("mixed-turn", ["user-1", "tool-1", "agent-1"])]),
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
          viewKey,
          anchorEntries,
          measureAnchors: () => positions,
        }),
      { initialProps: { viewKey: "everything" } },
    );
    scrollToIndex.mockClear();
    el.scrollTop = 300;

    act(() => result.current.captureViewAnchor());
    positions = [
      { id: "user-1", sourceIndex: 8, index: 3, offset: -130, height: 60, isMessage: true },
      { id: "tools:tool-1:tool-1", sourceIndex: 9, index: 3, offset: -70, height: 40, isMessage: false },
      { id: "agent-1", sourceIndex: 10, index: 3, offset: -30, height: 96, isMessage: true },
    ];
    rerender({ viewKey: "intent" });

    expect(scrollToIndex).not.toHaveBeenCalled();
    expect(el.scrollTop).toBe(288);
  });

  test("uses normalized scroll proportion when no surrounding message survives", () => {
    const { ref, el, scrollToIndex } = makeListHandle();
    const metrics = makeMeasure({ scrollTop: 450, scrollHeight: 1200, clientHeight: 300 });
    let positions: ViewAnchorPosition[] = [{ id: "tool-only", sourceIndex: 4, index: 4, offset: 18, isMessage: false }];
    const { result, rerender } = renderHook(
      ({ viewKey }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: model([turn("t1", ["i1"])]),
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure: metrics.measure,
          viewKey,
          measureAnchors: () => positions,
        }),
      { initialProps: { viewKey: "everything" } },
    );
    scrollToIndex.mockClear();

    act(() => result.current.captureViewAnchor());
    positions = [];
    metrics.set({ scrollTop: 0, scrollHeight: 600, clientHeight: 300 });
    rerender({ viewKey: "intent" });

    expect(scrollToIndex).not.toHaveBeenCalled();
    expect(el.scrollTop).toBe(150);
  });

  test("applies the saved pixel offset after an initially unmeasured fallback row is measured", () => {
    const { ref, el, scrollToIndex } = makeListHandle();
    const { measure } = makeMeasure({ scrollTop: 300, scrollHeight: 1200, clientHeight: 300 });
    let positions: ViewAnchorPosition[] = [
      { id: "tool-4", sourceIndex: 4, index: 4, offset: 18, height: 40, isMessage: false },
    ];
    const anchorEntries = [{ id: "agent-5", sourceIndex: 5, index: 5, isMessage: true }];
    const { result, rerender } = renderHook(
      ({ viewKey }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: model([turn("t1", ["i1"])]),
          listRef: ref,
          loadOlder: vi.fn(() => Promise.resolve()),
          measure,
          viewKey,
          anchorEntries,
          measureAnchors: () => positions,
        }),
      { initialProps: { viewKey: "everything" } },
    );

    act(() => result.current.captureViewAnchor());
    positions = [];
    rerender({ viewKey: "intent" });
    expect(scrollToIndex).toHaveBeenCalledWith(5, { align: "start" });

    el.scrollTop = 480;
    positions = [{ id: "agent-5", sourceIndex: 5, index: 5, offset: 0, height: 96, isMessage: true }];
    act(() => result.current.restoreViewAnchorAfterMeasurement());

    expect(el.scrollTop).toBe(462);
  });
});

describe("registered transcript view preservation", () => {
  test("captures the visible anchor, bottom state, and focused entry", () => {
    const el = document.createElement("div");
    const anchor = document.createElement("div");
    anchor.dataset.viewAnchorId = "agent-4";
    anchor.dataset.viewAnchorSourceIndex = "4";
    const focusedDescendant = document.createElement("button");
    anchor.append(focusedDescendant);
    el.append(anchor);
    document.body.append(el);
    focusedDescendant.focus();

    const captured = captureTranscriptView(
      el,
      () => ({ scrollTop: 950, scrollHeight: 1000, clientHeight: 50 }),
      () => [{ id: "agent-4", sourceIndex: 4, index: 2, offset: 18, height: 96, isMessage: true }],
    );

    expect(captured).toMatchObject({
      anchorId: "agent-4",
      anchorOffset: 18,
      normalizedOffset: 1,
      followingBottom: true,
      focusedEntryId: "agent-4",
    });
    el.remove();
  });

  test("restores a surviving focused entry and focuses the stable fallback when it disappears", () => {
    const list = makeListHandle();
    document.body.append(list.el);
    const oldAnchor = document.createElement("div");
    oldAnchor.dataset.viewAnchorId = "tool-old";
    oldAnchor.dataset.viewAnchorSourceIndex = "4";
    const oldEntry = document.createElement("button");
    oldAnchor.append(oldEntry);
    list.el.append(oldAnchor);
    const fallback = document.createElement("div");
    fallback.tabIndex = -1;
    document.body.append(fallback);
    oldEntry.focus();

    let positions: ViewAnchorPosition[] = [
      { id: "tool-old", sourceIndex: 4, index: 1, offset: 18, height: 40, isMessage: false },
    ];
    const anchorEntries = [{ id: "tool-old", sourceIndex: 4, index: 1, isMessage: false }];
    const { rerender } = renderHook(
      ({ viewKey, entries }) =>
        useTranscriptViewRegistration({
          enabled: true,
          id: "pane",
          layout: "desktop",
          viewKey,
          listRef: list.ref,
          measure: () => ({ scrollTop: 300, scrollHeight: 1200, clientHeight: 300 }),
          measureAnchors: () => positions,
          anchorEntries: entries,
          renderedRowCount: 2,
          focusFallback: () => fallback.focus(),
        }),
      { initialProps: { viewKey: "everything", entries: anchorEntries } },
    );

    positions = [{ id: "tool-old", sourceIndex: 4, index: 0, offset: 2, height: 40, isMessage: false }];
    act(() => {
      transitionTranscriptViews(
        () => rerender({ viewKey: "intent", entries: anchorEntries }),
        "Transcript display changed",
      );
    });
    expect(document.activeElement).toBe(oldEntry);

    positions = [{ id: "tool-old", sourceIndex: 4, index: 0, offset: 2, height: 40, isMessage: false }];
    act(() => {
      transitionTranscriptViews(() => {
        oldAnchor.remove();
        positions = [{ id: "agent-new", sourceIndex: 5, index: 1, offset: 0, height: 96, isMessage: true }];
        rerender({
          viewKey: "tools",
          entries: [{ id: "agent-new", sourceIndex: 5, index: 1, isMessage: true }],
        });
      }, "Transcript display changed again");
    });
    expect(document.activeElement).toBe(fallback);
    list.el.remove();
    fallback.remove();
  });

  test("waits for a virtualized source alias and restores the same descendant from Intent to Tools", () => {
    const list = makeListHandle();
    document.body.append(list.el);
    const intentAnchor = document.createElement("div");
    intentAnchor.dataset.viewAnchorId = "intent:tool-1";
    intentAnchor.dataset.viewAnchorSourceIndex = "4";
    const intentButton = document.createElement("button");
    intentAnchor.append(intentButton);
    list.el.append(intentAnchor);
    const focusFallback = vi.fn();
    intentButton.focus();

    let positions: ViewAnchorPosition[] = [
      { id: "intent:tool-1", sourceIndex: 4, index: 0, offset: 18, height: 40, isMessage: false },
    ];
    const intentEntries = [{ id: "intent:tool-1", sourceIndex: 4, index: 0, isMessage: false }];
    const { result, rerender } = renderHook(
      ({ viewKey, entries }) =>
        useTranscriptViewRegistration({
          enabled: true,
          id: "alias-pane",
          layout: "desktop",
          viewKey,
          listRef: list.ref,
          measure: () => ({ scrollTop: 300, scrollHeight: 1200, clientHeight: 300 }),
          measureAnchors: () => positions,
          anchorEntries: entries,
          renderedRowCount: 2,
          focusFallback,
        }),
      { initialProps: { viewKey: "intent", entries: intentEntries } },
    );

    act(() => {
      transitionTranscriptViews(() => {
        intentAnchor.remove();
        positions = [];
        rerender({
          viewKey: "tools",
          entries: [{ id: "tool-1", sourceIndex: 4, index: 1, isMessage: false }],
        });
      }, "Transcript display changed");
    });
    expect(list.scrollToIndex).toHaveBeenCalledWith(1, { align: "start" });
    expect(focusFallback).not.toHaveBeenCalled();

    const toolAnchor = document.createElement("div");
    toolAnchor.dataset.viewAnchorId = "tool-1";
    toolAnchor.dataset.viewAnchorSourceIndex = "4";
    const toolButton = document.createElement("button");
    toolAnchor.append(toolButton);
    list.el.append(toolAnchor);
    positions = [{ id: "tool-1", sourceIndex: 4, index: 1, offset: 18, height: 40, isMessage: false }];
    act(() => result.current.restoreAfterMeasurement());

    expect(document.activeElement).toBe(toolButton);
    expect(focusFallback).not.toHaveBeenCalled();
    list.el.remove();
  });
});

describe("no-model / not-yet-mounted safety", () => {
  test("model undefined (thread still loading): no crash, empty result", () => {
    const { ref } = makeListHandle();
    const { measure } = makeMeasure(AT_BOTTOM);
    const { result } = renderHook(() =>
      useTranscriptScroll({
        ref: "ref_a",
        model: undefined,
        listRef: ref,
        loadOlder: vi.fn(() => Promise.resolve()),
        measure,
      }),
    );

    expect(result.current.pillCount).toBe(0);
    expect(result.current.pillNeedsYou).toBe(false);
    expect(() => result.current.jumpToBottom()).not.toThrow();
  });

  test("listRef.current null (VirtualList not yet mounted, e.g. an empty transcript): no crash", () => {
    const notMountedRef = createRef<VirtualListHandle>() as React.RefObject<VirtualListHandle | null>;
    const { result } = renderHook(() =>
      useTranscriptScroll({
        ref: "ref_a",
        model: model([]),
        listRef: notMountedRef,
        loadOlder: vi.fn(() => Promise.resolve()),
      }),
    );

    expect(result.current.pillCount).toBe(0);
  });
});

describe("ask dock activation edge (roborev PR #854)", () => {
  // The pending-questions dock is a virtual row now (TranscriptBody's
  // trailingRow), and an in-progress ask_user item COMPLETING activates it
  // without any turn/item shape change - neither itemCount nor firstTurnId
  // nor failedTurns moves, so the content-changed effect never fires for
  // it. Without a dedicated edge, the dock would appear invisibly below a
  // scrolled-away reader while the composer's input row is hidden, leaving
  // no visible path to the answer controls.
  test("a dock activating while the reader is scrolled away surfaces the new-content pill as needs-you", () => {
    const { ref, scrollToIndex } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ m, askDockPending, epoch, rowCount }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn(),
          measure,
          askDockPending,
          askDockActivationEpoch: epoch,
          renderedRowCount: rowCount,
        }),
      { initialProps: { m: model([turn("t1", ["i1"])]), askDockPending: false, epoch: 0, rowCount: 1 } },
    );
    expect(result.current.pillCount).toBe(0);

    // The item completes: the transcript's shape is unchanged, only the
    // dock activates (and the wire's askPending flips, which is what makes
    // the pill needs-you). The row count grows by the synthetic dock row.
    rerender({ m: model([turn("t1", ["i1"])], { askPending: true }), askDockPending: true, epoch: 1, rowCount: 2 });

    expect(result.current.pillCount).toBe(1);
    expect(result.current.pillNeedsYou).toBe(true);

    // The pill's jump lands on the dock row itself (the count fix's half).
    act(() => result.current.jumpToBottom());
    expect(scrollToIndex).toHaveBeenLastCalledWith(1, { align: "end" });
    expect(result.current.pillCount).toBe(0);
  });

  test("a dock activating while the reader is at the bottom adds no pill", () => {
    const { ref } = makeListHandle();
    const { measure } = makeMeasure(AT_BOTTOM);
    const { result, rerender } = renderHook(
      ({ m, askDockPending, epoch }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn(),
          measure,
          askDockPending,
          askDockActivationEpoch: epoch,
        }),
      { initialProps: { m: model([turn("t1", ["i1"])]), askDockPending: false, epoch: 0 } },
    );

    rerender({ m: model([turn("t1", ["i1"])], { askPending: true }), askDockPending: true, epoch: 1 });

    // The end-anchored list already followed the appended row into view -
    // a pill would claim there is something unseen when there is not.
    expect(result.current.pillCount).toBe(0);
  });

  test("a session opened with an already-pending ask does not fire the edge", () => {
    const { ref } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result } = renderHook(() =>
      useTranscriptScroll({
        ref: "ref_a",
        model: model([turn("t1", ["i1"])], { askPending: true }),
        listRef: ref,
        loadOlder: vi.fn(),
        measure,
        askDockPending: true,
        askDockActivationEpoch: 1,
      }),
    );
    // Initial mount scrolls to the end (the dock row is visible) - nothing
    // is unseen, so no pill.
    expect(result.current.pillCount).toBe(0);
  });

  test("an atomic pending-set replacement re-fires the edge for a scrolled-away reader", () => {
    const { ref } = makeListHandle();
    const { measure } = makeMeasure(SCROLLED_AWAY);
    const { result, rerender } = renderHook(
      ({ m, epoch }) =>
        useTranscriptScroll({
          ref: "ref_a",
          model: m,
          listRef: ref,
          loadOlder: vi.fn(),
          measure,
          askDockPending: true,
          askDockActivationEpoch: epoch,
        }),
      // Pending throughout: a snapshot resync swapped the old (answered
      // elsewhere) batch for a new one. askDockPending never leaves true,
      // so a boolean edge could never re-fire - the epoch is the signal.
      { initialProps: { m: model([turn("t1", ["i1"])], { askPending: true }), epoch: 1 } },
    );
    expect(result.current.pillCount).toBe(0); // mount: no edge

    rerender({ m: model([turn("t1", ["i1"])], { askPending: true }), epoch: 2 });

    expect(result.current.pillCount).toBe(1);
    expect(result.current.pillNeedsYou).toBe(true);
  });
});

test("the pill is needs-you on the dock edge even when the wire's snapshot-only askPending has not landed", () => {
  const { ref } = makeListHandle();
  const { measure } = makeMeasure(SCROLLED_AWAY);
  const { result, rerender } = renderHook(
    ({ m, askDockPending, epoch }) =>
      useTranscriptScroll({
        ref: "ref_a",
        model: m,
        listRef: ref,
        loadOlder: vi.fn(),
        measure,
        askDockPending,
        askDockActivationEpoch: epoch,
      }),
    // model.askPending stays FALSE throughout: the field is
    // snapshot-authoritative (only hydrateThread sets it - no notification
    // carries it, per reducer.test.ts), so a live-arriving ask leaves it
    // unset until the next snapshot. The dock's own pending signal is the
    // live one.
    { initialProps: { m: model([turn("t1", ["i1"])]), askDockPending: false, epoch: 0 } },
  );

  rerender({ m: model([turn("t1", ["i1"])]), askDockPending: true, epoch: 1 });

  expect(result.current.pillCount).toBe(1);
  expect(result.current.pillNeedsYou).toBe(true);
});
