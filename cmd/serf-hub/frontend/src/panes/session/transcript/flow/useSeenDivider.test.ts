import { cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import type { ItemModel, ThreadModel, TurnModel } from "../../../../protocol/model";
import type { ThreadCapabilities } from "../../../../protocol/types.gen";
import { readSeenWatermark, writeSeenWatermark } from "./seenWatermark";
import { useSeenDivider } from "./useSeenDivider";

// See draft.test.ts's identical comment: Node 26 shadows jsdom's real
// window.localStorage with its own (non-functional under vitest) global.
class MemoryStorage {
  private store = new Map<string, string>();
  getItem(key: string): string | null {
    return this.store.has(key) ? (this.store.get(key) ?? null) : null;
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value));
  }
  removeItem(key: string): void {
    this.store.delete(key);
  }
  clear(): void {
    this.store.clear();
  }
}

beforeAll(() => {
  // @ts-expect-error see MemoryStorage's own comment for why this is needed
  globalThis.localStorage = new MemoryStorage();
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

function turn(id: string): TurnModel {
  return { id, status: "completed", items: [item(`${id}-item`, id)] };
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
  queue: false,
  goal: false,
  rename: false,
};

function model(turns: TurnModel[]): ThreadModel {
  return {
    ref: "local:01AAA",
    threadId: "thr_a",
    name: "test",
    status: { type: "idle" },
    modelProvider: "anthropic/claude",
    model: "anthropic/claude",
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
  };
}

test("no watermark stored (never visited before): no divider", () => {
  const { result } = renderHook(() => useSeenDivider("local:01AAA", model([turn("t1"), turn("t2")])));
  expect(result.current).toBeNull();
});

test("watermark is the last loaded turn: no unseen content, no divider", () => {
  writeSeenWatermark("local:01AAA", "t2");
  const { result } = renderHook(() => useSeenDivider("local:01AAA", model([turn("t1"), turn("t2")])));
  expect(result.current).toBeNull();
});

test("watermark is an earlier turn: divider lands on the turn right after it", () => {
  writeSeenWatermark("local:01AAA", "t1");
  const { result } = renderHook(() => useSeenDivider("local:01AAA", model([turn("t1"), turn("t2"), turn("t3")])));
  expect(result.current).toBe("t2");
});

test("watermark turn not found in loaded turns: no divider (does not guess)", () => {
  writeSeenWatermark("local:01AAA", "not-loaded-turn");
  const { result } = renderHook(() => useSeenDivider("local:01AAA", model([turn("t1"), turn("t2")])));
  expect(result.current).toBeNull();
});

test("model undefined (still loading) then becomes available: computes once turns arrive", () => {
  const { result, rerender } = renderHook(({ m }: { m: ThreadModel | undefined }) => useSeenDivider("local:01AAA", m), {
    initialProps: { m: undefined as ThreadModel | undefined },
  });
  expect(result.current).toBeNull();
  writeSeenWatermark("local:01AAA", "t1");
  rerender({ m: model([turn("t1"), turn("t2")]) });
  expect(result.current).toBe("t2");
});

test("divider is computed once at mount and does not move as new turns stream in during this viewing", () => {
  writeSeenWatermark("local:01AAA", "t1");
  const { result, rerender } = renderHook(({ m }: { m: ThreadModel }) => useSeenDivider("local:01AAA", m), {
    initialProps: { m: model([turn("t1"), turn("t2")]) },
  });
  expect(result.current).toBe("t2");
  rerender({ m: model([turn("t1"), turn("t2"), turn("t3"), turn("t4")]) });
  expect(result.current).toBe("t2");
});

test("divider stays valid across a loadOlder prepend (id-keyed, not index-keyed)", () => {
  writeSeenWatermark("local:01AAA", "t2");
  const { result, rerender } = renderHook(({ m }: { m: ThreadModel }) => useSeenDivider("local:01AAA", m), {
    initialProps: { m: model([turn("t1"), turn("t2"), turn("t3")]) },
  });
  expect(result.current).toBe("t3");
  // loadOlder prepends history in front - t3 keeps its identity.
  rerender({ m: model([turn("t0"), turn("t1"), turn("t2"), turn("t3")]) });
  expect(result.current).toBe("t3");
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
