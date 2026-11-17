// A row measurement that collapses every visible row to zero height shrinks the
// transcript's sizer to less than its port for a moment. `isAtBottom` reads a
// port whose content fits as "at the bottom" - vacuously true, because there is
// no bottom to be short of - so the scroll listener admitted that collapse as an
// ARRIVAL, took the pill away from a reader who was genuinely away, and then let
// the growth pin carry them to the true end once the rows re-expanded.
//
// The recorded failure (issue #4962, run 40121900553) is exactly this: a native
// wheel left the reader at scrollTop 78328 of 80634 (clientHeight 549), 694.7ms
// later all eight rendered rows measured 0 while the live-edge row held 80px,
// the offset clamped to 0, and the rows re-expanded to 70440 - where the paint
// ended at scrollTop 69802 + clientHeight 638. The New Content pill was then
// never on offer, and the guard timed out waiting for it.
//
// jsdom performs no real layout, so the geometry comes through the hook's
// documented injectable `measure` seam and the port's own properties, the same
// way the rest of this suite stages scroll behavior.
import type { ItemModel, ThreadCapabilities, ThreadModel, TurnModel } from "@evener/appwire-client";
import { act, cleanup, renderHook } from "@testing-library/react";
import { createRef } from "react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { resetThreadsStoreForTests } from "../../../../stores/threads";
import type { VirtualListHandle } from "../../../../widgets/virtuallist";
import type { ScrollMetrics } from "./scrollMetrics";
import { resetTranscriptViewRegistryForTests } from "./transcriptViewRegistry";
import { useTranscriptScroll } from "./useTranscriptScroll";

function item(id: string, turnId: string): ItemModel {
  return { id, turnId, type: "agentMessage", text: "x", status: "completed" };
}

function turn(id: string, itemIds: string[]): TurnModel {
  return { id, status: "completed", items: itemIds.map((iid) => item(iid, id)) };
}

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
  sharedNotes: false,
  rename: false,
};

function model(turns: TurnModel[]): ThreadModel {
  return {
    ref: "ref_a",
    threadId: "thr_a",
    name: "test",
    status: { type: "idle" },
    modelProvider: "anthropic/claude",
    model: "anthropic/claude",
    modelControlState: {
      kind: "uninitialized",
      canonicalDisplay: { modelProvider: "anthropic/claude", model: "anthropic/claude" },
    },
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
    humanNote: "",
    agentNote: "",
    sessionUrls: [],
    contextUsed: 0,
    contextWindow: 0,
    contextPressure: 0,
    contextAssumed: false,
    compactionThreshold: 0,
    usage: null,
    workMillis: 0,
    reasoningEffortLevels: [],
    supportsReasoning: false,
    cwd: "/tmp/project",
    jobsTreeRevision: null,
  };
}

// A fake VirtualListHandle over a real (bare) <div>: jsdom's scrollTop is a
// genuine read/write slot, while scrollHeight/clientHeight/offsetHeight are
// hardcoded zeros with no layout behind them, so they are defined per stage.
function makeListHandle(): { ref: React.RefObject<VirtualListHandle | null>; el: HTMLDivElement } {
  const el = document.createElement("div");
  const ref = createRef<VirtualListHandle>() as React.RefObject<VirtualListHandle | null>;
  (ref as { current: VirtualListHandle }).current = {
    scrollToIndex: vi.fn(),
    getScrollElement: () => el,
    // The collapse is COMMITTED geometry: the virtualizer has measured every
    // rendered row at zero and its sizer agrees, which is what made the scroll
    // listener trust the fit as a real arrival.
    isLayoutCurrent: () => true,
    getVisibleRange: () => null,
  };
  return { ref, el };
}

function makeMeasure(initial: ScrollMetrics) {
  let current = initial;
  return {
    measure: () => current,
    set: (next: Partial<ScrollMetrics>) => {
      current = { ...current, ...next };
    },
  };
}

function definePort(el: HTMLElement, metrics: ScrollMetrics): void {
  Object.defineProperty(el, "scrollHeight", { value: metrics.scrollHeight, configurable: true });
  Object.defineProperty(el, "clientHeight", { value: metrics.clientHeight, configurable: true });
  el.scrollTop = metrics.scrollTop;
}

// The three geometries of the recorded failure. AWAY is the reader's own
// position (80634 - 78328 - 549 = 1757px from the end); COLLAPSED is every row
// measuring zero with the live-edge row's 80px left; RE_EXPANDED is the same
// content the reader was reading, now at the offset the browser clamped to.
const AWAY: ScrollMetrics = { scrollTop: 78328, scrollHeight: 80634, clientHeight: 549 };
const AT_END: ScrollMetrics = { scrollTop: 80085, scrollHeight: 80634, clientHeight: 549 };
const COLLAPSED: ScrollMetrics = { scrollTop: 0, scrollHeight: 80, clientHeight: 549 };
const RE_EXPANDED: ScrollMetrics = { scrollTop: 0, scrollHeight: 80634, clientHeight: 549 };

function mountReader(start: ScrollMetrics = AT_END) {
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
  // One native wheel up from the end, then the movement it caused: the reader
  // leaves the end by their own input, which is what retires the follow intent.
  const wheelAway = (to: ScrollMetrics) =>
    act(() => {
      el.dispatchEvent(new WheelEvent("wheel", { deltaY: -900, bubbles: true }));
      definePort(el, to);
      set(to);
      el.dispatchEvent(new Event("scroll"));
    });
  // A measurement-only geometry change: the port's content shrank or grew and
  // the browser clamped the offset. The browser dispatches a scroll event for
  // that clamp, and the virtualizer's own write produces the same event.
  const measureTo = (to: ScrollMetrics) =>
    act(() => {
      definePort(el, to);
      set(to);
      el.dispatchEvent(new Event("scroll"));
    });
  return { el, set, measure, view, wheelAway, measureTo };
}

beforeEach(() => {
  resetThreadsStoreForTests();
  resetTranscriptViewRegistryForTests();
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe("a row-measurement collapse does not take the pill from an away reader", () => {
  test("the collapse is not an arrival: the pill stays on offer through it and the re-expand", () => {
    const reader = mountReader();
    try {
      // The reader is at the end, following: no pill.
      expect(reader.view.result.current.pillVisible).toBe(false);
      expect(reader.view.result.current.isFollowingBottom()).toBe(true);

      // The reader's own wheel moves them away from the end.
      reader.wheelAway(AWAY);
      expect(reader.view.result.current.pillVisible).toBe(true);
      expect(reader.view.result.current.isFollowingBottom()).toBe(false);

      // Every row measures zero: the sizer collapses below the port and the
      // browser clamps the offset to 0. Content that fits is vacuously "at the
      // bottom", but the reader did not move anywhere.
      reader.measureTo(COLLAPSED);
      expect(reader.view.result.current.isFollowingBottom()).toBe(false);
      expect(reader.view.result.current.pillVisible).toBe(true);

      // The rows re-expand to the content the reader was reading. A reader who
      // is away must not be carried to the end by the growth below them.
      reader.measureTo(RE_EXPANDED);
      expect(reader.view.result.current.isFollowingBottom()).toBe(false);
      expect(reader.view.result.current.pillVisible).toBe(true);
      expect(reader.el.scrollTop).not.toBe(RE_EXPANDED.scrollHeight - RE_EXPANDED.clientHeight);
    } finally {
      reader.view.unmount();
    }
  });

  test("a bottom follower is unaffected: the same collapse still follows the end", () => {
    const reader = mountReader();
    try {
      // No wheel: the reader never left the end.
      reader.measureTo(COLLAPSED);
      expect(reader.view.result.current.isFollowingBottom()).toBe(true);
      expect(reader.view.result.current.pillVisible).toBe(false);

      reader.measureTo(RE_EXPANDED);
      expect(reader.view.result.current.isFollowingBottom()).toBe(true);
      expect(reader.view.result.current.pillVisible).toBe(false);
      expect(reader.el.scrollTop).toBe(RE_EXPANDED.scrollHeight - RE_EXPANDED.clientHeight);
    } finally {
      reader.view.unmount();
    }
  });

  // The rule that admits committed geometry as an arrival is what the collapse
  // abused, so the ordinary arrival it exists for must keep working: a reader
  // who wheels away and then scrolls back to the true end of content that still
  // overflows is following again.
  test("a reader who returns to the true end of overflowing content is following again", () => {
    const reader = mountReader();
    try {
      reader.wheelAway(AWAY);
      expect(reader.view.result.current.isFollowingBottom()).toBe(false);
      expect(reader.view.result.current.pillVisible).toBe(true);

      act(() => {
        reader.el.dispatchEvent(new WheelEvent("wheel", { deltaY: 900, bubbles: true }));
        definePort(reader.el, AT_END);
        reader.set(AT_END);
        reader.el.dispatchEvent(new Event("scroll"));
      });
      expect(reader.view.result.current.isFollowingBottom()).toBe(true);
      expect(reader.view.result.current.pillVisible).toBe(false);
    } finally {
      reader.view.unmount();
    }
  });
});
