import {
  type ItemModel,
  SYSTEM_PRELUDE_TURN_ID,
  type ThreadCapabilities,
  type ThreadModel,
  type TurnModel,
} from "@evener/appwire-client";
import { cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { installLocalStorage, MemoryStorage } from "../../../../storageTestUtils";
import { readSeenWatermark, writeSeenWatermark } from "./seenWatermark";
import { type SeenBoundary, unseenOpeningTurnId, useSeenDivider } from "./useSeenDivider";

beforeAll(() => {
  installLocalStorage(new MemoryStorage());
});

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

function item(id: string, turnId: string): ItemModel {
  return { id, turnId, type: "agentMessage", text: "x", status: "completed" };
}

function turn(id: string, status = "completed"): TurnModel {
  return { id, status, items: [item(`${id}-item`, id)] };
}

function running(id: string): TurnModel {
  return turn(id, "inProgress");
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

function model(turns: TurnModel[], runningTurnId?: string): ThreadModel {
  return {
    runningTurnId,
    ref: "local:01AAA",
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
    jobsTreeRevision: null,
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
  };
}

test("no watermark stored (never visited before): no divider", () => {
  const { result } = renderHook(() => useSeenDivider("local:01AAA", model([turn("t1"), turn("t2")])));
  expect(result.current.dividerTurnId).toBeNull();
});

test("watermark is the last loaded turn: no unseen content, no divider", () => {
  writeSeenWatermark("local:01AAA", "t2");
  const { result } = renderHook(() => useSeenDivider("local:01AAA", model([turn("t1"), turn("t2")])));
  expect(result.current.dividerTurnId).toBeNull();
});

test("watermark is an earlier turn: divider lands on the turn right after it", () => {
  writeSeenWatermark("local:01AAA", "t1");
  const { result } = renderHook(() => useSeenDivider("local:01AAA", model([turn("t1"), turn("t2"), turn("t3")])));
  expect(result.current.dividerTurnId).toBe("t2");
});

test("watermark turn not found in loaded turns: no divider (does not guess)", () => {
  writeSeenWatermark("local:01AAA", "not-loaded-turn");
  const { result } = renderHook(() => useSeenDivider("local:01AAA", model([turn("t1"), turn("t2")])));
  expect(result.current.dividerTurnId).toBeNull();
});

test("model undefined (still loading) then becomes available: computes once turns arrive", () => {
  const { result, rerender } = renderHook(({ m }: { m: ThreadModel | undefined }) => useSeenDivider("local:01AAA", m), {
    initialProps: { m: undefined as ThreadModel | undefined },
  });
  expect(result.current.dividerTurnId).toBeNull();
  writeSeenWatermark("local:01AAA", "t1");
  rerender({ m: model([turn("t1"), turn("t2")]) });
  expect(result.current.dividerTurnId).toBe("t2");
});

test("divider is computed once at mount and does not move as new turns stream in during this viewing", () => {
  writeSeenWatermark("local:01AAA", "t1");
  const { result, rerender } = renderHook(({ m }: { m: ThreadModel }) => useSeenDivider("local:01AAA", m), {
    initialProps: { m: model([turn("t1"), turn("t2")]) },
  });
  expect(result.current.dividerTurnId).toBe("t2");
  rerender({ m: model([turn("t1"), turn("t2"), turn("t3"), turn("t4")]) });
  expect(result.current.dividerTurnId).toBe("t2");
});

test("divider stays valid across a loadOlder prepend (id-keyed, not index-keyed)", () => {
  writeSeenWatermark("local:01AAA", "t2");
  const { result, rerender } = renderHook(({ m }: { m: ThreadModel }) => useSeenDivider("local:01AAA", m), {
    initialProps: { m: model([turn("t1"), turn("t2"), turn("t3")]) },
  });
  expect(result.current.dividerTurnId).toBe("t3");
  // loadOlder prepends history in front - t3 keeps its identity.
  rerender({ m: model([turn("t0"), turn("t1"), turn("t2"), turn("t3")]) });
  expect(result.current.dividerTurnId).toBe("t3");
});

test("on unmount, writes the current last turn's id as the new watermark", () => {
  const { unmount } = renderHook(() => useSeenDivider("local:01AAA", model([turn("t1"), turn("t2")])));
  unmount();
  expect(readSeenWatermark("local:01AAA")).toBe("t2");
});

test("on unmount with new turns having streamed in since mount, writes the LATEST last turn (not the mount-time one)", () => {
  const { rerender, unmount } = renderHook(({ m }: { m: ThreadModel }) => useSeenDivider("local:01AAA", m), {
    initialProps: { m: model([turn("t1")]) },
  });
  rerender({ m: model([turn("t1"), turn("t2"), turn("t3")]) });
  unmount();
  expect(readSeenWatermark("local:01AAA")).toBe("t3");
});

test("on unmount with no turns loaded (model still undefined), writes nothing", () => {
  const { unmount } = renderHook(({ m }: { m: ThreadModel | undefined }) => useSeenDivider("local:01AAA", m), {
    initialProps: { m: undefined },
  });
  unmount();
  expect(readSeenWatermark("local:01AAA")).toBeNull();
});

test("watermarks for different refs are independent", () => {
  writeSeenWatermark("local:01AAA", "t1");
  const { unmount } = renderHook(() => useSeenDivider("local:01BBB", model([turn("x1"), turn("x2")])));
  unmount();
  expect(readSeenWatermark("local:01AAA")).toBe("t1");
  expect(readSeenWatermark("local:01BBB")).toBe("x2");
});

test("a turn still running at unmount is not the watermark: the previous settled turn is", () => {
  const { unmount } = renderHook(() =>
    useSeenDivider("local:01AAA", model([turn("t1"), turn("t2"), running("t3")], "t3")),
  );
  unmount();
  expect(readSeenWatermark("local:01AAA")).toBe("t2");
});

test("a wire-inProgress turn the thread does not name as running counts as settled", () => {
  const { unmount } = renderHook(() => useSeenDivider("local:01AAA", model([turn("t1"), running("t2")], undefined)));
  unmount();
  expect(readSeenWatermark("local:01AAA")).toBe("t2");
});

test("the boundary is unsettled while the turn after the watermark is running at open", () => {
  writeSeenWatermark("local:01AAA", "t1");
  const { result } = renderHook(() => useSeenDivider("local:01AAA", model([turn("t1"), running("t2")], "t2")));
  expect(result.current).toEqual({ dividerTurnId: "t2", settled: false });
});

test("the boundary is settled when the turn after the watermark has finished", () => {
  writeSeenWatermark("local:01AAA", "t1");
  const { result } = renderHook(() => useSeenDivider("local:01AAA", model([turn("t1"), turn("t2")])));
  expect(result.current).toEqual({ dividerTurnId: "t2", settled: true });
});

test("changing ref on one hook instance writes the old ref's own latest settled turn and opens the new ref at its own watermark", () => {
  writeSeenWatermark("local:01BBB", "b1");
  const { result, rerender } = renderHook(({ ref, m }: { ref: string; m: ThreadModel }) => useSeenDivider(ref, m), {
    initialProps: { ref: "local:01AAA", m: model([turn("a1"), turn("a2")]) },
  });
  expect(result.current.dividerTurnId).toBeNull();
  rerender({ ref: "local:01BBB", m: model([turn("b1"), turn("b2"), turn("b3")]) });
  expect(readSeenWatermark("local:01AAA")).toBe("a2");
  expect(result.current).toEqual({ dividerTurnId: "b2", settled: true });
  expect(readSeenWatermark("local:01BBB")).toBe("b1");
});

test("leaving a ref that never rendered a settled turn writes nothing for it, never an earlier ref's turn", () => {
  const { rerender } = renderHook(({ ref, m }: { ref: string; m: ThreadModel | undefined }) => useSeenDivider(ref, m), {
    initialProps: { ref: "local:01AAA", m: model([turn("a1")]) as ThreadModel | undefined },
  });
  rerender({ ref: "local:01BBB", m: undefined });
  rerender({ ref: "local:01CCC", m: model([turn("c1")]) });
  expect(readSeenWatermark("local:01AAA")).toBe("a1");
  expect(readSeenWatermark("local:01BBB")).toBeNull();
});

test("a dormant model does not lock the decision: the boundary is computed once real turns arrive", () => {
  writeSeenWatermark("local:01AAA", "t1");
  const prelude = { id: SYSTEM_PRELUDE_TURN_ID, status: "completed", items: [] } as TurnModel;
  const { result, rerender } = renderHook(({ m }: { m: ThreadModel }) => useSeenDivider("local:01AAA", m), {
    initialProps: { m: model([prelude]) },
  });
  expect(result.current).toEqual({ dividerTurnId: null, settled: false });
  rerender({ m: model([prelude, turn("t1"), turn("t2")]) });
  expect(result.current).toEqual({ dividerTurnId: "t2", settled: true });
});

const SETTLED: SeenBoundary = { dividerTurnId: "t2", settled: true };
const QUIET = { askPending: false, pendingEscalations: [] };
const NOTHING_WAITS = { thread: QUIET, askDockPending: false, retained: undefined };

test("unseenOpeningTurnId opens at the divider turn when it is settled and nothing waits", () => {
  expect(unseenOpeningTurnId(SETTLED, NOTHING_WAITS)).toBe("t2");
});

test("unseenOpeningTurnId does not open at an unsettled boundary", () => {
  expect(unseenOpeningTurnId({ dividerTurnId: "t2", settled: false }, NOTHING_WAITS)).toBeNull();
});

test("unseenOpeningTurnId yields to a question the dock is showing", () => {
  expect(unseenOpeningTurnId(SETTLED, { ...NOTHING_WAITS, askDockPending: true })).toBeNull();
});

// A transcript restored without its bodies knows a question is pending before
// the dock has the question to show.
test("unseenOpeningTurnId yields to a pending question the dock has not reconstructed yet", () => {
  expect(unseenOpeningTurnId(SETTLED, { ...NOTHING_WAITS, thread: { ...QUIET, askPending: true } })).toBeNull();
});

test("unseenOpeningTurnId yields to a pending approval", () => {
  expect(unseenOpeningTurnId(SETTLED, { ...NOTHING_WAITS, thread: { ...QUIET, pendingEscalations: [{}] } })).toBeNull();
});

test("unseenOpeningTurnId yields to a retained place away from the bottom", () => {
  expect(unseenOpeningTurnId(SETTLED, { ...NOTHING_WAITS, retained: { followingBottom: false } })).toBeNull();
});

test("unseenOpeningTurnId opens at the divider turn when the retained view was following the bottom", () => {
  expect(unseenOpeningTurnId(SETTLED, { ...NOTHING_WAITS, retained: { followingBottom: true } })).toBe("t2");
});

test("unseenOpeningTurnId is null when there is no boundary turn", () => {
  expect(unseenOpeningTurnId({ dividerTurnId: null, settled: true }, NOTHING_WAITS)).toBeNull();
});
