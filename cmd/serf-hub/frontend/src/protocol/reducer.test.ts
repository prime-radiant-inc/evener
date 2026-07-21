import { test, expect } from "vitest";
import { hydrateThread, applyNotification, prependOlderTurns, notificationTargetsThread } from "./reducer";
import type { ThreadModel, TurnModel, ItemModel } from "./model";
import type { AnyNotification, SandboxEscalationRequested, Thread, ThreadCapabilities, ThreadReadResponse, ThreadTurnsListResponse } from "./types.gen";
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

function testEscalation(overrides: Partial<SandboxEscalationRequested> = {}): SandboxEscalationRequested {
  return {
    threadId: "thr_t",
    ref: "ref_t",
    escalationId: "esc_1",
    mode: "exempt_denied_path",
    tool: "write_file",
    kind: "file_tool",
    deniedPath: "/etc/passwd",
    ...overrides,
  };
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

  // Stream A's own item BEFORE settling — wire-true: the real turn/completed
  // never carries items (see the "turn/completed preserves..." test below),
  // so A's item must already be in the model via item/completed, not
  // smuggled in through the settle payload.
  modelA = applyNotification(
    modelA,
    {
      method: "item/completed",
      params: { threadId: "thr_a", ref: "ref_a", turnId: "turn_1", item: { type: "agentMessage", id: "item_a1", turnId: "turn_1", text: "A's answer", status: "completed" } },
    } as AnyNotification,
    1500,
  );

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

  // The real wire's turn/completed is a bare stamp (see
  // internal/appprojector/appwire_projection.go: EventUserInput,
  // EventGoalContinuation, EventError, EventSessionEnd all emit
  // Turn{ID,Status[,Error]} with Items nil, ItemsView "") — no items key.
  const aTurnCompleted = {
    method: "turn/completed",
    params: { turnId: "turn_1", turn: { id: "turn_1", status: "completed", itemsView: "" } },
  } as AnyNotification;

  // Sanity: the same notification legitimately completes A's own active
  // turn — and A's already-streamed item SURVIVES the bare settle stamp.
  const settledA = applyNotification(modelA, aTurnCompleted, 2000);
  expect(settledA).not.toBe(modelA);
  expect(itemAt(turnAt(settledA, 0), 0).text).toBe("A's answer");

  // But applying it to B — which merely happens to share the turn id — must
  // no-op entirely.
  const result = applyNotification(modelB, aTurnCompleted, 2000);
  expect(result).toBe(modelB);
  expect(itemAt(turnAt(result, 0), 0).text).toBe("B's own answer");
});

// Part A regression coverage: the real wire's turn/completed is a bare
// status/timing stamp with no items (see the case's own comment in
// reducer.ts for the full projector-site citation list). A settled turn
// must KEEP whatever items the model already accumulated via
// item/started + deltas + item/completed, not wipe them.

test("turn/completed with a bare stamp preserves the turn's already-streamed items", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    { method: "turn/started", params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } } } as AnyNotification,
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", item: { type: "agentMessage", id: "item_1", turnId: "turn_1", text: "Hello, world!", status: "completed" } },
    } as AnyNotification,
    1002,
  );
  expect(turnAt(model, 0).items).toHaveLength(1);

  model = applyNotification(model, { method: "turn/completed", params: { turnId: "turn_1", turn: { id: "turn_1", status: "completed", itemsView: "" } } } as AnyNotification, 1003);

  const items = turnAt(model, 0).items;
  expect(items).toHaveLength(1);
  expect(itemAt(turnAt(model, 0), 0).text).toBe("Hello, world!");
});

test("turn/completed's bare stamp fields (status, timing, usage, cost) land on the settled turn", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    { method: "turn/started", params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } } } as AnyNotification,
    1001,
  );

  model = applyNotification(
    model,
    {
      method: "turn/completed",
      params: {
        turnId: "turn_1",
        turn: { id: "turn_1", status: "completed", itemsView: "", startedAt: 5000, completedAt: 6500, durationMs: 1500, usage: { inputTokens: 10, outputTokens: 20 }, cost: "0.01" },
      },
    } as AnyNotification,
    1002,
  );

  const turn = turnAt(model, 0);
  expect(turn.status).toBe("completed");
  expect(turn.startedAt).toBe(new Date(5000).toISOString());
  expect(turn.completedAt).toBe(new Date(6500).toISOString());
  expect(turn.durationMs).toBe(1500);
  expect(turn.usage).toEqual({ inputTokens: 10, outputTokens: 20 });
  expect(turn.cost).toBe("0.01");
});

test('turn/completed with itemsView "full" still replaces items, and mergeReasoning still preserves reasoningSummaries', () => {
  // itemsView "full" is the snapshot/hydration projector's own discriminator
  // (internal/apptranscript/apptranscript.go:58,520); the one live site that
  // sends it on turn/completed is systemAnnouncementWithRaw's no-active-turn
  // branch (internal/appprojector/appwire_projection.go:~963). This payload
  // shape must still fully replace items, same as before this task's fix.
  let model = testHydrate();
  model = applyNotification(
    model,
    { method: "turn/started", params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } } } as AnyNotification,
    1001,
  );
  model = applyNotification(
    model,
    { method: "item/started", params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", item: { type: "reasoning", id: "item_r", turnId: "turn_1", status: "inProgress" } } } as AnyNotification,
    1002,
  );
  model = applyNotification(
    model,
    { method: "item/reasoning/summaryTextDelta", params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", itemId: "item_r", summaryIndex: 0, delta: "thinking..." } } as AnyNotification,
    1003,
  );

  model = applyNotification(
    model,
    {
      method: "turn/completed",
      params: {
        turnId: "turn_1",
        turn: { id: "turn_1", status: "completed", itemsView: "full", items: [{ type: "reasoning", id: "item_r", turnId: "turn_1", status: "completed" }] },
      },
    } as AnyNotification,
    1004,
  );

  const items = turnAt(model, 0).items;
  expect(items).toHaveLength(1);
  expect(itemAt(turnAt(model, 0), 0).reasoningSummaries).toEqual([["thinking..."]]);
});

test('turn/completed with itemsView "full" replaces items outright — a payload carrying a differently-id\'d item drops the streamed one entirely', () => {
  // The test above reuses the streamed item's id in the settle payload, so
  // it cannot distinguish a true replace from a preserve-and-merge that
  // happens to key on id (mergeReasoning's own same-id coverage already
  // exists there — this test targets the "full" branch's replace semantics
  // directly, independent of any id coincidence).
  let model = testHydrate();
  model = applyNotification(
    model,
    { method: "turn/started", params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } } } as AnyNotification,
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", item: { type: "agentMessage", id: "item_x", turnId: "turn_1", text: "X's text", status: "completed" } },
    } as AnyNotification,
    1002,
  );
  expect(turnAt(model, 0).items).toHaveLength(1);

  model = applyNotification(
    model,
    {
      method: "turn/completed",
      params: {
        turnId: "turn_1",
        turn: { id: "turn_1", status: "completed", itemsView: "full", items: [{ type: "agentMessage", id: "item_y", turnId: "turn_1", text: "Y's text", status: "completed" }] },
      },
    } as AnyNotification,
    1003,
  );

  const items = turnAt(model, 0).items;
  expect(items).toHaveLength(1);
  const survivor = itemAt(turnAt(model, 0), 0);
  expect(survivor.id).toBe("item_y");
  expect(survivor.text).toBe("Y's text");
});

test("turn/completed's settle fold joins a mid-stream item's pendingText into text and flips inProgress to completed", () => {
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

  // Settle arrives mid-stream — no item/completed ever landed for item_1
  // (e.g. an interrupt or session end cut the stream short).
  model = applyNotification(
    model,
    { method: "turn/completed", params: { turnId: "turn_1", turn: { id: "turn_1", status: "interrupted", itemsView: "" } } } as AnyNotification,
    1005,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.text).toBe("Hello"); // joined chunks — no authoritative item/completed text ever arrived
  expect(item.pendingText).toBeUndefined();
  expect(item.status).toBe("completed"); // an in-progress item inside a settled turn is a lie
});

test("turn/completed's failed-turn stamp (EventError shape) preserves items and carries the error", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    { method: "turn/started", params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } } } as AnyNotification,
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", item: { type: "agentMessage", id: "item_1", turnId: "turn_1", text: "partial answer", status: "completed" } },
    } as AnyNotification,
    1002,
  );

  const error = { message: "rate limited", source: "provider", title: "Provider error" };
  model = applyNotification(
    model,
    { method: "turn/completed", params: { turnId: "turn_1", turn: { id: "turn_1", status: "failed", itemsView: "", error } } } as AnyNotification,
    1003,
  );

  const turn = turnAt(model, 0);
  expect(turn.status).toBe("failed");
  expect(turn.error).toEqual(error);
  expect(turn.items).toHaveLength(1);
  expect(itemAt(turn, 0).text).toBe("partial answer");
});

test("turn/completed's failed-turn stamp folds a mid-stream item's pendingText AND carries the error together (real EventError shape)", () => {
  // The test above uses an item that was already completed before the
  // error arrived, so it never exercises settleItem's fold — it only
  // proves items survive and the error lands. EventError's actual shape
  // (internal/appprojector/appwire_projection.go:507-572) fires while an
  // item can still be mid-stream (e.g. a rate limit interrupts the agent
  // mid-message); this test uses that shape, asserting the fold and the
  // error land together in one settle.
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
    { method: "item/agentMessage/delta", params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", itemId: "item_1", delta: "partial " } } as AnyNotification,
    1003,
  );
  model = applyNotification(
    model,
    { method: "item/agentMessage/delta", params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", itemId: "item_1", delta: "answer" } } as AnyNotification,
    1004,
  );

  const error = { message: "rate limited", source: "provider", title: "Provider error" };
  model = applyNotification(
    model,
    { method: "turn/completed", params: { turnId: "turn_1", turn: { id: "turn_1", status: "failed", itemsView: "", error } } } as AnyNotification,
    1005,
  );

  const turn = turnAt(model, 0);
  expect(turn.status).toBe("failed");
  expect(turn.error).toEqual(error);
  const item = itemAt(turn, 0);
  expect(item.text).toBe("partial answer"); // joined chunks — no authoritative item/completed text ever arrived
  expect(item.pendingText).toBeUndefined();
  expect(item.status).toBe("completed"); // an in-progress item inside a settled turn is a lie
});

// Part B regression coverage: serf/steering/injected's payload is declared
// `nil` in the AppWire catalog, but the live projector
// (internal/appprojector/appwire_projection.go:573-593) actually sends
// {threadId, ref, text, images, source?} — source present ("user") only for
// human-sent steers, omitted for daemon-originated ones. A live steer into
// an in-flight turn must become a "steering" transcript item, mirroring how
// reload already renders persisted steering turns
// (internal/apptranscript/apptranscript.go:211-229).

test('serf/steering/injected with source "user" appends a steering item to the active turn', () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    { method: "turn/started", params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } } } as AnyNotification,
    1001,
  );

  model = applyNotification(
    model,
    { method: "serf/steering/injected", params: { threadId: "thr_t", ref: "ref_t", text: "please also check X", source: "user" } } as AnyNotification,
    1002,
  );

  const items = turnAt(model, 0).items;
  expect(items).toHaveLength(1);
  expect(items[0]).toMatchObject({
    id: "item_steering_live_turn_1_0",
    turnId: "turn_1",
    type: "steering",
    text: "please also check X",
    status: "completed",
    source: "user",
  });
});

test("serf/steering/injected with no source field appends an item with source undefined", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    { method: "turn/started", params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } } } as AnyNotification,
    1001,
  );

  model = applyNotification(model, { method: "serf/steering/injected", params: { threadId: "thr_t", ref: "ref_t", text: "daemon steer text" } } as AnyNotification, 1002);

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.source).toBeUndefined();
  expect(item.text).toBe("daemon steer text");
});

test("two steers in one turn get distinct ids in arrival order", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    { method: "turn/started", params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } } } as AnyNotification,
    1001,
  );

  model = applyNotification(model, { method: "serf/steering/injected", params: { threadId: "thr_t", ref: "ref_t", text: "first" } } as AnyNotification, 1002);
  model = applyNotification(model, { method: "serf/steering/injected", params: { threadId: "thr_t", ref: "ref_t", text: "second" } } as AnyNotification, 1003);

  const items = turnAt(model, 0).items;
  expect(items.map((it) => it.id)).toEqual(["item_steering_live_turn_1_0", "item_steering_live_turn_1_1"]);
  expect(items.map((it) => it.text)).toEqual(["first", "second"]);
});

test("serf/steering/injected with no active turn only updates lastFrameAt (no turn fabricated client-side)", () => {
  const model = testHydrate();
  expect(model.activeTurnId).toBeUndefined();

  const result = applyNotification(model, { method: "serf/steering/injected", params: { threadId: "thr_t", ref: "ref_t", text: "orphaned steer" } } as AnyNotification, 2000);

  expect(result).toEqual({ ...model, lastFrameAt: 2000 });
  expect(result.turns).toBe(model.turns); // same reference: no turn was touched, none fabricated
});

test("a steering item survives a bare turn/completed settle stamp (composition with Part A)", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    { method: "turn/started", params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } } } as AnyNotification,
    1001,
  );
  model = applyNotification(model, { method: "serf/steering/injected", params: { threadId: "thr_t", ref: "ref_t", text: "mid-turn steer" } } as AnyNotification, 1002);

  model = applyNotification(model, { method: "turn/completed", params: { turnId: "turn_1", turn: { id: "turn_1", status: "completed", itemsView: "" } } } as AnyNotification, 1003);

  const items = turnAt(model, 0).items;
  expect(items).toHaveLength(1);
  expect(items[0]).toMatchObject({ type: "steering", text: "mid-turn steer", status: "completed" });
});

test("serf/steering/injected images populate display-ready strings via the same conversion other item paths use", () => {
  // Steering images use the same appwire.InputItem shape as userMessage
  // images (internal/appprojector/appwire_projection.go's
  // projectUserInputImages: Type "image", MediaType, Data, Name — no
  // Url/Path), so imagesToStrings' url ?? path ?? name fallback resolves to
  // name here, exactly as it would for any other image-bearing item.
  let model = testHydrate();
  model = applyNotification(
    model,
    { method: "turn/started", params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } } } as AnyNotification,
    1001,
  );

  model = applyNotification(
    model,
    {
      method: "serf/steering/injected",
      params: { threadId: "thr_t", ref: "ref_t", text: "", images: [{ type: "image", mediaType: "image/png", data: "iVBORw0KGgo=", name: "screenshot.png" }] },
    } as AnyNotification,
    1002,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.images).toEqual(["screenshot.png"]);
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

// ThreadTurnsListResponse.data is typed as a required Turn[], but that's a
// TS-side promise, not a wire guarantee - mirrors the same defense
// hydrateThread/wireToTurnModel already apply to thread.turns/turn.items
// (both `?? []`). A response missing `data` entirely (the Go zero value for
// a nil slice marshals as JSON `null`, not `[]`) must not crash and must
// behave as "an empty page" rather than losing the turns already in model.
test("prependOlderTurns tolerates a wire-nullable data array (treats it as an empty page)", () => {
  const thread = testThread({ turns: [{ id: "turn_2", status: "completed", itemsView: "full", items: [] }] });
  const model = hydrateThread({ thread, olderCursor: "cursor_1" }, thread.serf.ref, 1000);

  const resp = { nextCursor: "cursor_0" } as unknown as ThreadTurnsListResponse;
  const result = prependOlderTurns(model, resp);

  expect(result.turns.map((t) => t.id)).toEqual(["turn_2"]);
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

// Observed reasoning timing: the wire never carries reasoning timestamps at
// all (neither the live projector nor the historical reader sets
// ThreadItem.StartedAt/CompletedAt for reasoning items), so the reducer
// stamps its own client-observed arrival times from `now` — see
// ItemModel.observedStartedAt/observedCompletedAt's doc comment in
// model.ts and the reducer's appendReasoningDelta/mergeObservedTiming/
// settleItem comments for the full rationale.

test("first reasoning delta stamps observedStartedAt; a later delta does not move it", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    { method: "turn/started", params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } } } as AnyNotification,
    1001,
  );
  model = applyNotification(
    model,
    { method: "item/started", params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", item: { type: "reasoning", id: "item_r", turnId: "turn_1", status: "inProgress" } } } as AnyNotification,
    1002,
  );

  model = applyNotification(
    model,
    { method: "item/reasoning/summaryTextDelta", params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", itemId: "item_r", summaryIndex: 0, delta: "thinking" } } as AnyNotification,
    1003,
  );
  expect(itemAt(turnAt(model, 0), 0).observedStartedAt).toBe(new Date(1003).toISOString());

  model = applyNotification(
    model,
    { method: "item/reasoning/summaryTextDelta", params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", itemId: "item_r", summaryIndex: 0, delta: " more" } } as AnyNotification,
    1050,
  );
  expect(itemAt(turnAt(model, 0), 0).observedStartedAt).toBe(new Date(1003).toISOString()); // unchanged by the second delta
});

test("item/completed stamps observedCompletedAt when observation began; the wire's own (absent) timestamps stay absent", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    { method: "turn/started", params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } } } as AnyNotification,
    1001,
  );
  model = applyNotification(
    model,
    { method: "item/started", params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", item: { type: "reasoning", id: "item_r", turnId: "turn_1", status: "inProgress" } } } as AnyNotification,
    1002,
  );
  model = applyNotification(
    model,
    { method: "item/reasoning/summaryTextDelta", params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", itemId: "item_r", summaryIndex: 0, delta: "thinking" } } as AnyNotification,
    1003,
  );

  model = applyNotification(
    model,
    { method: "item/completed", params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", item: { type: "reasoning", id: "item_r", turnId: "turn_1", status: "completed" } } } as AnyNotification,
    1010,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.observedStartedAt).toBe(new Date(1003).toISOString());
  expect(item.observedCompletedAt).toBe(new Date(1010).toISOString());
  expect(item.startedAt).toBeUndefined();
  expect(item.completedAt).toBeUndefined();
});

test("a reasoning item still in-flight at a bare turn/completed settle gets observedCompletedAt from the settle (composition with R1's preserve path)", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    { method: "turn/started", params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } } } as AnyNotification,
    1001,
  );
  model = applyNotification(
    model,
    { method: "item/started", params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", item: { type: "reasoning", id: "item_r", turnId: "turn_1", status: "inProgress" } } } as AnyNotification,
    1002,
  );
  model = applyNotification(
    model,
    { method: "item/reasoning/summaryTextDelta", params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", itemId: "item_r", summaryIndex: 0, delta: "thinking" } } as AnyNotification,
    1003,
  );

  // Settle arrives mid-stream — no item/completed ever landed for item_r.
  model = applyNotification(model, { method: "turn/completed", params: { turnId: "turn_1", turn: { id: "turn_1", status: "interrupted", itemsView: "" } } } as AnyNotification, 1020);

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.observedStartedAt).toBe(new Date(1003).toISOString());
  expect(item.observedCompletedAt).toBe(new Date(1020).toISOString());
});

test("hydrated items never carry observed timing fields", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [{ type: "reasoning", id: "item_r", turnId: "turn_1", text: "done thinking", status: "completed" }],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.serf.ref, 1000);

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.observedStartedAt).toBeUndefined();
  expect(item.observedCompletedAt).toBeUndefined();
});

test("wire startedAt/completedAt, when present, coexist untouched alongside observed stamps", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    { method: "turn/started", params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } } } as AnyNotification,
    1001,
  );
  model = applyNotification(
    model,
    { method: "item/started", params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", item: { type: "reasoning", id: "item_r", turnId: "turn_1", status: "inProgress" } } } as AnyNotification,
    1002,
  );
  model = applyNotification(
    model,
    { method: "item/reasoning/summaryTextDelta", params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", itemId: "item_r", summaryIndex: 0, delta: "thinking" } } as AnyNotification,
    1003,
  );

  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", item: { type: "reasoning", id: "item_r", turnId: "turn_1", status: "completed", startedAt: 5000, completedAt: 6000 } },
    } as AnyNotification,
    1004,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.startedAt).toBe(new Date(5000).toISOString());
  expect(item.completedAt).toBe(new Date(6000).toISOString());
  expect(item.observedStartedAt).toBe(new Date(1003).toISOString());
  expect(item.observedCompletedAt).toBe(new Date(1004).toISOString());
});

// Warnings reach the model: the reducer's `case "warning"` (see reducer.ts
// for the wire receipts — internal/appprojector/appwire_projection.go's
// EventWarning and EventError's user-cancel branch) folds NotifyWarning
// notifications into the active turn as ordinary items, mirroring
// serf/steering/injected's own shape.

test("warning mid-turn appends an item to the active turn with text=message and the meta populated", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    { method: "turn/started", params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } } } as AnyNotification,
    1001,
  );

  model = applyNotification(
    model,
    { method: "warning", params: { threadId: "thr_t", ref: "ref_t", message: "rate limit approaching", source: "provider", title: "Provider warning", hint: "slow down" } } as AnyNotification,
    1002,
  );

  const items = turnAt(model, 0).items;
  expect(items).toHaveLength(1);
  expect(items[0]).toMatchObject({
    id: "item_warning_live_turn_1_0",
    turnId: "turn_1",
    type: "warning",
    text: "rate limit approaching",
    status: "completed",
    warning: { source: "provider", title: "Provider warning", hint: "slow down" },
  });
});

test("two warnings in one turn get distinct ids in arrival order", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    { method: "turn/started", params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } } } as AnyNotification,
    1001,
  );

  model = applyNotification(model, { method: "warning", params: { threadId: "thr_t", ref: "ref_t", message: "first" } } as AnyNotification, 1002);
  model = applyNotification(model, { method: "warning", params: { threadId: "thr_t", ref: "ref_t", message: "second" } } as AnyNotification, 1003);

  const items = turnAt(model, 0).items;
  expect(items.map((it) => it.id)).toEqual(["item_warning_live_turn_1_0", "item_warning_live_turn_1_1"]);
  expect(items.map((it) => it.text)).toEqual(["first", "second"]);
});

test("warning with no active turn only updates lastFrameAt (no turn fabricated client-side)", () => {
  const model = testHydrate();
  expect(model.activeTurnId).toBeUndefined();

  const result = applyNotification(model, { method: "warning", params: { threadId: "thr_t", ref: "ref_t", message: "orphaned warning" } } as AnyNotification, 2000);

  expect(result).toEqual({ ...model, lastFrameAt: 2000 });
  expect(result.turns).toBe(model.turns); // same reference: no turn was touched, none fabricated
});

test("a warning item survives a bare turn/completed settle stamp (composition with Part A)", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    { method: "turn/started", params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } } } as AnyNotification,
    1001,
  );
  model = applyNotification(model, { method: "warning", params: { threadId: "thr_t", ref: "ref_t", message: "mid-turn warning" } } as AnyNotification, 1002);

  model = applyNotification(model, { method: "turn/completed", params: { turnId: "turn_1", turn: { id: "turn_1", status: "completed", itemsView: "" } } } as AnyNotification, 1003);

  const items = turnAt(model, 0).items;
  expect(items).toHaveLength(1);
  expect(items[0]).toMatchObject({ type: "warning", text: "mid-turn warning", status: "completed" });
});

test("a cancel-shaped warning (cause present) still lands, ignoring cause", () => {
  // EventError's user-cancel branch sends the same inline shape as
  // EventWarning plus `cause` (internal/appprojector/appwire_projection.go:
  // 520-535). cause has no model consumer — assert only that the item
  // lands with its meta; do not invent a field to carry it.
  let model = testHydrate();
  model = applyNotification(
    model,
    { method: "turn/started", params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } } } as AnyNotification,
    1001,
  );

  model = applyNotification(
    model,
    { method: "warning", params: { threadId: "thr_t", ref: "ref_t", message: "context canceled", source: "user", title: "Cancelled", hint: "", cause: "context canceled" } } as AnyNotification,
    1002,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item).toMatchObject({ type: "warning", text: "context canceled", warning: { source: "user", title: "Cancelled", hint: "" } });
});

// Settled tool calls keep their arguments: the live projector's
// EventToolCallEnd (internal/appprojector/appwire_projection.go:414-442)
// resolves argsJSON at :424-427 but uses it only to derive Description -
// the settled ThreadItem it emits carries no ArgumentsJSON, even though the
// streamed item/started item (:373) had it. Historical items DO carry it
// (internal/apptranscript/apptranscript.go:284,312), so this is a
// live-settle-only loss the reducer corrects, mergeReasoning-style.

test("item/completed without argumentsJson keeps the item's original argumentsJSON alongside settled output/status", () => {
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
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "commandExecution", id: "item_tool", turnId: "turn_1", toolName: "bash", callId: "call_1", argumentsJson: '{"command":"ls"}', status: "inProgress" },
      },
    } as AnyNotification,
    1002,
  );

  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "commandExecution", id: "item_tool", turnId: "turn_1", toolName: "bash", callId: "call_1", output: "file1\nfile2", status: "completed" },
      },
    } as AnyNotification,
    1003,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.argumentsJSON).toBe('{"command":"ls"}');
  expect(item.output).toBe("file1\nfile2");
  expect(item.status).toBe("completed");
});

test("item/completed with its own argumentsJson replaces the old value (wire truth wins)", () => {
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
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "commandExecution", id: "item_tool", turnId: "turn_1", toolName: "bash", callId: "call_1", argumentsJson: '{"command":"ls"}', status: "inProgress" },
      },
    } as AnyNotification,
    1002,
  );

  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "commandExecution", id: "item_tool", turnId: "turn_1", toolName: "bash", callId: "call_1", argumentsJson: '{"command":"ls -la"}', status: "completed" },
      },
    } as AnyNotification,
    1003,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.argumentsJSON).toBe('{"command":"ls -la"}');
});

test("item/completed inserting a never-started item has no argumentsJSON (no crash, no fabrication)", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    { method: "turn/started", params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } } } as AnyNotification,
    1001,
  );

  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", item: { type: "userMessage", id: "item_user", turnId: "turn_1", text: "hi", status: "completed" } },
    } as AnyNotification,
    1002,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.argumentsJSON).toBeUndefined();
});

// pendingEscalations (M7): appwire/types.go's ThreadSerf.PendingEscalations
// doc comment calls it the "surface-on-entry snapshot ... so a client
// entering / reconnecting to / not-having-seen-live this session surfaces
// the card(s)" and rules it a HUMAN-CLIENT field only, never part of the
// transcript. hydrateThread must therefore carry it verbatim (or default it
// to [], per the Go wire-nullable-array rule: omitempty absent means empty)
// as a THREAD-level ThreadModel field, not a turn item.

test("hydrateThread maps serf.pendingEscalations verbatim into pendingEscalations", () => {
  const escalation = testEscalation();
  const model = testHydrate({ serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: {}, pendingEscalations: [escalation] } });
  expect(model.pendingEscalations).toEqual([escalation]);
});

test("hydrateThread defaults pendingEscalations to an empty array when serf.pendingEscalations is absent", () => {
  const model = testHydrate();
  expect(model.pendingEscalations).toEqual([]);
});

test("pendingEscalations survives a turn/started notification — thread-level state, untouched by turn machinery", () => {
  const escalation = testEscalation();
  let model = testHydrate({ serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: {}, pendingEscalations: [escalation] } });

  model = applyNotification(
    model,
    { method: "turn/started", params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } } } as AnyNotification,
    1001,
  );

  expect(model.pendingEscalations).toEqual([escalation]);
});

test("pendingEscalations survives a turn/completed bare-stamp settle — thread-level state, untouched by turn machinery", () => {
  const escalation = testEscalation();
  let model = testHydrate({ serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: {}, pendingEscalations: [escalation] } });
  model = applyNotification(
    model,
    { method: "turn/started", params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } } } as AnyNotification,
    1001,
  );

  model = applyNotification(
    model,
    { method: "turn/completed", params: { turnId: "turn_1", turn: { id: "turn_1", status: "completed", itemsView: "" } } } as AnyNotification,
    1002,
  );

  expect(model.pendingEscalations).toEqual([escalation]);
});

test('"serf/sandbox/escalation/requested" appends a new card with full field mapping and stamps lastFrameAt', () => {
  let model = testHydrate();
  const escalation = testEscalation({ command: "rm -rf /tmp/x", outputSoFar: "partial output", partiallyRan: true });

  model = applyNotification(model, { method: "serf/sandbox/escalation/requested", params: escalation } as AnyNotification, 2000);

  expect(model.pendingEscalations).toEqual([escalation]);
  expect(model.lastFrameAt).toBe(2000);
});

test('"serf/sandbox/escalation/requested" with an already-present escalationId replaces that entry instead of growing the list', () => {
  // Snapshot-then-subscribe overlap: hydration's pendingEscalations snapshot
  // and a live requested notification can race and both deliver the same
  // card (appwire/types.go's PendingEscalations doc comment). Last write
  // wins — replace in place, don't drop the update or duplicate the entry.
  const first = testEscalation({ mode: "exempt_denied_path" });
  let model = testHydrate({ serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: {}, pendingEscalations: [first] } });

  const updated = testEscalation({ mode: "exempt_command", partiallyRan: true });
  model = applyNotification(model, { method: "serf/sandbox/escalation/requested", params: updated } as AnyNotification, 2000);

  expect(model.pendingEscalations).toEqual([updated]);
});

test('"serf/sandbox/escalation/requested" for a different thread is a same-reference no-op', () => {
  const model = testHydrate();
  const escalation = testEscalation({ ref: "some_other_ref", threadId: "thr_other" });

  const result = applyNotification(model, { method: "serf/sandbox/escalation/requested", params: escalation } as AnyNotification, 2000);

  expect(result).toBe(model);
});
