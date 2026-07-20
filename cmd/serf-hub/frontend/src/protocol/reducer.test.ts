import { test, expect } from "vitest";
import { hydrateThread, applyNotification, prependOlderTurns, notificationTargetsThread } from "./reducer";
import type { ThreadModel, TurnModel, ItemModel } from "./model";
import type { AnyNotification, Thread, ThreadCapabilities, ThreadReadResponse, ThreadTurnsListResponse } from "./types.gen";
// Vite's `?raw` import (declared ambiently by the "vite/client" lib already
// in tsconfig.json) loads each fixture's text at build time — this project
// has no @types/node, so node:fs is not an option here.
import basicTurnFixture from "./fixtures/basic-turn.jsonl?raw";
import streamingWithResetFixture from "./fixtures/streaming-with-reset.jsonl?raw";
import toolAndJobsFixture from "./fixtures/tool-and-jobs.jsonl?raw";
import queueAndStatusFixture from "./fixtures/queue-and-status.jsonl?raw";

// Each fixture is newline-delimited JSON: the first line is
// {"hydrate": ThreadReadResponse, "ref": string}, every following line is a
// raw wire notification object ({method, params}).
interface FixtureHeader {
  hydrate: ThreadReadResponse;
  ref: string;
}

const FIXTURE_TEXT: Record<string, string> = {
  "basic-turn": basicTurnFixture,
  "streaming-with-reset": streamingWithResetFixture,
  "tool-and-jobs": toolAndJobsFixture,
  "queue-and-status": queueAndStatusFixture,
};

function readFixture(name: string): unknown[] {
  const text = FIXTURE_TEXT[name];
  if (text === undefined) throw new Error(`no fixture registered for ${name}`);
  return text
    .split("\n")
    .filter((line) => line.trim().length > 0)
    .map((line) => JSON.parse(line) as unknown);
}

function turnAt(model: ThreadModel, index: number): TurnModel {
  const turn = model.turns[index];
  if (!turn) throw new Error(`expected a turn at index ${index}`);
  return turn;
}

function itemAt(turn: TurnModel, index: number): ItemModel {
  const item = turn.items[index];
  if (!item) throw new Error(`expected an item at index ${index}`);
  return item;
}

const CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  queue: true,
  goal: true,
  rename: true,
};

function testThread(overrides: Partial<Thread> = {}): Thread {
  return {
    id: "thr_t",
    sessionId: "sess_t",
    preview: "test",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "idle" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "serf",
    serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: {} },
    ...overrides,
  };
}

function testHydrate(overrides: Partial<Thread> = {}): ThreadModel {
  const thread = testThread(overrides);
  return hydrateThread({ thread }, thread.serf.ref, 1000);
}

for (const f of ["basic-turn", "streaming-with-reset", "tool-and-jobs", "queue-and-status"]) {
  test(`fixture ${f} reduces to the expected model`, () => {
    const lines = readFixture(f);
    const header = lines[0] as FixtureHeader;
    let model = hydrateThread(header.hydrate, header.ref, 1000);
    for (const [i, n] of lines.slice(1).entries()) {
      model = applyNotification(model, n as AnyNotification, 1000 + i);
    }
    expect(model).toMatchSnapshot();
  });
}

test("delta accumulates into pendingText chunks and joins on completion", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    { method: "turn/started", params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } } } as AnyNotification,
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/started",
      params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", item: { type: "agentMessage", id: "item_1", turnId: "turn_1", status: "inProgress" } },
    } as AnyNotification,
    1002,
  );
  model = applyNotification(
    model,
    { method: "item/agentMessage/delta", params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", itemId: "item_1", delta: "Hel" } } as AnyNotification,
    1003,
  );
  model = applyNotification(
    model,
    { method: "item/agentMessage/delta", params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", itemId: "item_1", delta: "lo" } } as AnyNotification,
    1004,
  );

  const streaming = itemAt(turnAt(model, 0), 0);
  expect(streaming.pendingText).toEqual(["Hel", "lo"]);
  expect(streaming.text).toBe(""); // not yet settled — the model never string-concats deltas into text

  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", item: { type: "agentMessage", id: "item_1", turnId: "turn_1", text: "Hello!", status: "completed" } },
    } as AnyNotification,
    1005,
  );

  const settled = itemAt(turnAt(model, 0), 0);
  expect(settled.text).toBe("Hello!"); // payload text ("Hello!") wins over the joined chunks ("Hello")
  expect(settled.pendingText).toBeUndefined();
});

test("item/completed inserts an item that had no preceding item/started", () => {
  // userMessage and systemMessage items go straight to item/completed with
  // no item/started (internal/appprojector/appwire_projection.go: a new user
  // turn emits turn/started with an empty turn, then item/completed for the
  // userMessage — item/started is never sent for it).
  let model = testHydrate();
  model = applyNotification(
    model,
    { method: "turn/started", params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } } } as AnyNotification,
    1001,
  );
  expect(turnAt(model, 0).items).toHaveLength(0);

  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", item: { type: "userMessage", id: "item_user", turnId: "turn_1", text: "Hi there", status: "completed" } },
    } as AnyNotification,
    1002,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.type).toBe("userMessage");
  expect(item.text).toBe("Hi there");
});

test("agentMessage/reset discards the in-flight item", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    { method: "turn/started", params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } } } as AnyNotification,
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/started",
      params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", item: { type: "agentMessage", id: "item_1", turnId: "turn_1", status: "inProgress" } },
    } as AnyNotification,
    1002,
  );
  expect(turnAt(model, 0).items).toHaveLength(1);

  model = applyNotification(
    model,
    { method: "item/agentMessage/reset", params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", itemId: "item_1" } } as AnyNotification,
    1003,
  );
  expect(turnAt(model, 0).items).toHaveLength(0);
});

test("notification for a different thread is ignored (same object returned)", () => {
  const model = testHydrate();
  const result = applyNotification(
    model,
    { method: "thread/status/changed", params: { threadId: "thr_t", ref: "some_other_ref", status: { type: "active" } } } as AnyNotification,
    2000,
  );
  expect(result).toBe(model);
});

test("notification method with no handler leaves the model unchanged", () => {
  // serf/auth/updated is a real, known NotificationName the reducer does not
  // model any state for (ThreadModel has no auth-provider fields); it also
  // carries neither ref nor threadId, so it can never target a thread.
  const model = testHydrate();
  const result = applyNotification(model, { method: "serf/auth/updated", params: {} } as AnyNotification, 2000);
  expect(result).toBe(model);
});

test("notificationTargetsThread matches on ref, falls back to threadId, else false", () => {
  const model = testHydrate();
  expect(
    notificationTargetsThread({ method: "thread/status/changed", params: { threadId: "thr_t", ref: "ref_t", status: { type: "active" } } } as AnyNotification, model),
  ).toBe(true);
  expect(
    notificationTargetsThread({ method: "thread/status/changed", params: { threadId: "thr_t", ref: "not_ref_t", status: { type: "active" } } } as AnyNotification, model),
  ).toBe(false);
  // serf/task/updated's ref is optional; when absent, threadId is the fallback key.
  expect(notificationTargetsThread({ method: "serf/task/updated", params: { threadId: "thr_t", total: 1, done: 0 } } as AnyNotification, model)).toBe(true);
  expect(notificationTargetsThread({ method: "serf/task/updated", params: { threadId: "not_thr_t", total: 1, done: 0 } } as AnyNotification, model)).toBe(false);
  // Neither field present (e.g. serf/auth/updated) targets no thread model.
  expect(notificationTargetsThread({ method: "serf/auth/updated", params: {} } as AnyNotification, model)).toBe(false);
});

test("turn/completed applies even though its payload carries no ref or threadId", () => {
  // TurnCompletedParams is {turnId, turn} on the wire — no ref/threadId field
  // exists to check (confirmed against appwire/types.go and types.gen.ts), so
  // notificationTargetsThread always rejects it. The reducer instead gates
  // turn/completed on model.activeTurnId === turnId (see the sibling-thread
  // immunity test below for why: turn IDs are per-thread sequential, so the
  // same turnId can legitimately belong to a different, unrelated thread).
  let model = testHydrate();
  model = applyNotification(
    model,
    { method: "turn/started", params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } } } as AnyNotification,
    1001,
  );
  const beforeCompletion = model;
  const turnCompleted = { method: "turn/completed", params: { turnId: "turn_1", turn: { id: "turn_1", status: "completed", itemsView: "", items: [] } } } as AnyNotification;

  expect(notificationTargetsThread(turnCompleted, model)).toBe(false);
  model = applyNotification(model, turnCompleted, 1002);

  expect(model).not.toBe(beforeCompletion);
  expect(turnAt(model, 0).status).toBe("completed");
  expect(model.activeTurnId).toBeUndefined();
});

test("turn/completed does not cross-apply to a different thread's same-numbered turn", () => {
  // Turn IDs are per-thread sequential ("turn_%d") and turn/completed carries
  // no ref/threadId, so two unrelated threads can each legitimately have
  // their own "turn_1" — one active on thread A, one long since settled on
  // thread B. Applying A's completion notification to B's model (e.g. a
  // store-layer routing bug, or delivery before the store learns better)
  // must be a true no-op: same reference, B's content untouched.
  const threadA = testThread({ id: "thr_a", serf: { ref: "ref_a", capabilities: CAPABILITIES, queue: {} } });
  let modelA = hydrateThread({ thread: threadA }, threadA.serf.ref, 1000);
  modelA = applyNotification(
    modelA,
    { method: "turn/started", params: { threadId: "thr_a", ref: "ref_a", turn: { id: "turn_1", status: "inProgress", itemsView: "" } } } as AnyNotification,
    1001,
  );
  expect(modelA.activeTurnId).toBe("turn_1");

  const threadB = testThread({
    id: "thr_b",
    serf: { ref: "ref_b", capabilities: CAPABILITIES, queue: {} },
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [{ type: "agentMessage", id: "item_b1", turnId: "turn_1", text: "B's own answer", status: "completed" }],
      },
    ],
  });
  const modelB = hydrateThread({ thread: threadB }, threadB.serf.ref, 1000);
  expect(modelB.activeTurnId).toBeUndefined();

  const aTurnCompleted = {
    method: "turn/completed",
    params: {
      turnId: "turn_1",
      turn: {
        id: "turn_1",
        status: "completed",
        itemsView: "",
        items: [{ type: "agentMessage", id: "item_a1", turnId: "turn_1", text: "A's answer", status: "completed" }],
      },
    },
  } as AnyNotification;

  // Sanity: the same notification legitimately completes A's own active turn.
  const settledA = applyNotification(modelA, aTurnCompleted, 2000);
  expect(settledA).not.toBe(modelA);
  expect(itemAt(turnAt(settledA, 0), 0).text).toBe("A's answer");

  // But applying it to B — which merely happens to share the turn id — must
  // no-op entirely.
  const result = applyNotification(modelB, aTurnCompleted, 2000);
  expect(result).toBe(modelB);
  expect(itemAt(turnAt(result, 0), 0).text).toBe("B's own answer");
});

test("prependOlderTurns keeps order and advances olderCursor", () => {
  const thread = testThread({ turns: [{ id: "turn_2", status: "completed", itemsView: "full", items: [] }] });
  const model = hydrateThread({ thread, olderCursor: "cursor_1" }, thread.serf.ref, 1000);

  const resp: ThreadTurnsListResponse = {
    data: [
      { id: "turn_0", status: "completed", itemsView: "full", items: [] },
      { id: "turn_1", status: "completed", itemsView: "full", items: [] },
    ],
    nextCursor: "cursor_0",
  };
  const result = prependOlderTurns(model, resp);

  expect(result.turns.map((t) => t.id)).toEqual(["turn_0", "turn_1", "turn_2"]);
  expect(result.olderCursor).toBe("cursor_0");
});

test("askPending flips from thread snapshot and item lifecycle", () => {
  const askingModel = testHydrate({ serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: {}, askPending: true } });
  expect(askingModel.askPending).toBe(true);

  let model = testHydrate();
  expect(model.askPending).toBe(false);

  model = applyNotification(
    model,
    { method: "turn/started", params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } } } as AnyNotification,
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/started",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "commandExecution", id: "item_ask", turnId: "turn_1", toolName: "ask_user", callId: "call_ask", status: "inProgress" },
      },
    } as AnyNotification,
    1002,
  );
  expect(model.askPending).toBe(true);

  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "commandExecution", id: "item_ask", turnId: "turn_1", toolName: "ask_user", callId: "call_ask", status: "completed" },
      },
    } as AnyNotification,
    1003,
  );
  expect(model.askPending).toBe(false);
});

test("thread/reasoning-effort/changed updates reasoningEffort", () => {
  let model = testHydrate();
  expect(model.reasoningEffort).toBeUndefined();
  model = applyNotification(
    model,
    { method: "thread/reasoning-effort/changed", params: { threadId: "thr_t", ref: "ref_t", reasoningEffort: "high" } } as AnyNotification,
    2000,
  );
  expect(model.reasoningEffort).toBe("high");
});
