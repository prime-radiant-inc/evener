// Edge cases for useTranscriptScroll.ts uncovered lines:
// - isAttentionWorthy with null model (line 182) — pillNeedsYou false when model is undefined
// - isAttentionWorthy with warning status (line 183) — pillNeedsYou true with warning status

import { cleanup, renderHook } from "@testing-library/react";
import { createRef } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { ItemModel, ThreadModel, TurnModel } from "../../../../protocol/model";
import type { ThreadCapabilities } from "../../../../protocol/types.gen";
import { resetThreadsStoreForTests } from "../../../../stores/threads";
import type { VirtualListHandle } from "../../../../widgets/virtuallist";
import type { ScrollMetrics } from "./scrollMetrics";
import { useTranscriptScroll } from "./useTranscriptScroll";

function item(id: string, turnId: string, overrides: Partial<ItemModel> = {}): ItemModel {
  return { id, turnId, type: "agentMessage", text: "x", status: "completed", ...overrides };
}

function turn(id: string, itemIds: string[], overrides: Partial<TurnModel> = {}): TurnModel {
  return { id, status: "completed", items: itemIds.map((iid) => item(iid, id)), ...overrides };
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

const SCROLLED_AWAY: ScrollMetrics = { scrollTop: 0, scrollHeight: 5000, clientHeight: 500 };

function makeListHandle() {
  const el = document.createElement("div");
  const scrollToIndex = vi.fn();
  const visibleRange: { startIndex: number; endIndex: number } | null = null;
  const ref = createRef<VirtualListHandle>() as React.RefObject<VirtualListHandle | null>;
  (ref as { current: VirtualListHandle }).current = {
    scrollToIndex,
    getScrollElement: () => el,
    getVisibleRange: () => visibleRange,
  };
  return { ref, el, scrollToIndex };
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

beforeEach(() => {
  resetThreadsStoreForTests();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

// Line 182: isAttentionWorthy returns false for undefined model
test("pillNeedsYou is false when model is undefined", () => {
  const { ref } = makeListHandle();
  const { measure } = makeMeasure(SCROLLED_AWAY);
  const { result } = renderHook(() =>
    useTranscriptScroll({
      ref: "ref_a",
      model: undefined,
      listRef: ref,
      loadOlder: vi.fn(),
      measure,
    }),
  );
  expect(result.current.pillNeedsYou).toBe(false);
});

// Line 183: isAttentionWorthy returns true for warning status (with pill content)
test("pillNeedsYou is true when model status is warning with new content below", () => {
  const { ref } = makeListHandle();
  const { measure } = makeMeasure(SCROLLED_AWAY);
  const { result, rerender } = renderHook(
    ({ m }) => useTranscriptScroll({ ref: "ref_a", model: m, listRef: ref, loadOlder: vi.fn(), measure }),
    { initialProps: { m: model([turn("t1", ["i1"])], { status: { type: "idle" } }) } },
  );

  // Add content (pill appears) and set status to warning
  rerender({ m: model([turn("t1", ["i1"]), turn("t2", ["i2"])], { status: { type: "warning" } }) });
  // pillNeedsYou requires pillCount > 0 AND isAttentionWorthy(model) === true
  // isAttentionWorthy checks model.askPending || model.status.type === "awaiting" || "warning"
  expect(result.current.pillNeedsYou).toBe(true);
});
