// @vitest-environment node

import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import type { ConnectionState } from "../../../../protocol/client";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { AnyNotification, Thread, ThreadCapabilities, ThreadReadResponse } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { readMutationPersistence, resetThreadsStoreForTests, threadsStore } from "../../../../stores/threads";
import { askDockStore, resetAskDockStoreForTests } from "./askDockStore";

// --- fixtures (mirrors stores/threads.test.ts's own harness) -------------

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

function testThread(ref: string, overrides: Partial<Thread> = {}): Thread {
  return {
    id: `thr_${ref}`,
    sessionId: `sess_${ref}`,
    preview: "test",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "idle" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "serf",
    serf: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
    ...overrides,
  };
}

function readResponse(ref: string): ThreadReadResponse {
  return { thread: testThread(ref) };
}

function connectFakeClient(state: ConnectionState = "ready"): FakeClient {
  const fake = new FakeClient(state);
  connectionStore.getState().connect(fake);
  return fake;
}

function askArgs(questions: Array<Record<string, unknown>>): string {
  return JSON.stringify({ questions });
}

const ONE_QUESTION = [{ header: "Deploy?", question: "Ship now?", options: [{ label: "Yes", detail: "" }] }];

const TWO_QUESTIONS = [
  { header: "First", question: "q1", options: [{ label: "a", detail: "b" }] },
  { header: "Second", question: "q2", options: [{ label: "c", detail: "d" }] },
];

const THREE_QUESTIONS = [...TWO_QUESTIONS, { header: "Third", question: "q3", options: [{ label: "e", detail: "f" }] }];

const MULTI_THEN_SINGLE = [
  {
    header: "Pick",
    question: "q1",
    multi_select: true,
    options: [
      { label: "a", detail: "b" },
      { label: "c", detail: "d" },
    ],
  },
  { header: "Second", question: "q2", options: [{ label: "e", detail: "f" }] },
];

// startTurn emits turn/started - every item/started|completed notification
// below requires its turn to already exist in the model
// (reducer.ts's resolveInsertTurnId), exactly like the real wire: a turn
// always starts before any of its items do. Each distinct turnId used by a
// test must be started exactly once.
function startTurn(fake: FakeClient, ref: string, turnId: string): void {
  fake.emitNotification({
    method: "turn/started",
    params: { threadId: `thr_${ref}`, ref, turn: { id: turnId, status: "inProgress", itemsView: "" } },
  });
}

function askItemNotification(
  ref: string,
  turnId: string,
  itemId: string,
  callId: string,
  method: "item/started" | "item/completed",
  status: string,
): AnyNotification {
  return {
    method,
    params: {
      threadId: `thr_${ref}`,
      ref,
      turnId,
      item: {
        type: "commandExecution",
        id: itemId,
        turnId,
        toolName: "ask_user",
        callId,
        status,
        argumentsJson: askArgs(ONE_QUESTION),
      },
    },
  };
}

function userMessageNotification(ref: string, turnId: string, itemId: string, text: string): AnyNotification {
  return {
    method: "item/completed",
    params: {
      threadId: `thr_${ref}`,
      ref,
      turnId,
      item: { type: "userMessage", id: itemId, turnId, text, status: "completed" },
    },
  };
}

// ackAskUserCall assumes `turnId` has already been started (startTurn).
function ackAskUserCall(fake: FakeClient, ref: string, turnId: string, itemId: string, callId: string): void {
  fake.emitNotification(askItemNotification(ref, turnId, itemId, callId, "item/started", "inProgress"));
  fake.emitNotification(askItemNotification(ref, turnId, itemId, callId, "item/completed", "completed"));
}

// ackAskUserCallWith is ackAskUserCall with a parameterized question set
// (the ONE_QUESTION-fixture helper above's own shape, generalized for the
// multi-question batches the kata-99yf active-tab tests need).
function ackAskUserCallWith(
  fake: FakeClient,
  ref: string,
  turnId: string,
  itemId: string,
  callId: string,
  questions: Array<Record<string, unknown>>,
): void {
  for (const [method, status] of [
    ["item/started", "inProgress"],
    ["item/completed", "completed"],
  ] as const) {
    fake.emitNotification({
      method,
      params: {
        threadId: `thr_${ref}`,
        ref,
        turnId,
        item: {
          type: "commandExecution",
          id: itemId,
          turnId,
          toolName: "ask_user",
          callId,
          status,
          argumentsJson: askArgs(questions),
        },
      },
    });
  }
}

beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetAskDockStoreForTests();
});

afterEach(() => {
  // Every test here calls ensureThread(ref) directly for setup - nothing in
  // this file's own reconciliation path calls releaseThread, so the ref
  // stays refcounted after the LAST test. Under isolate:false that is what a
  // later file's own connectionStore.connect() re-triggers via rewireClient.
  resetThreadsStoreForTests();
  // Every test here writes real durable outbox records into this file's own
  // globalThis.indexedDB instance - the beforeEach above only replaces it
  // BEFORE each test, so whatever the LAST test wrote stays installed as the
  // global indexedDB after this file finishes. Under isolate:false that
  // leftover, populated database is what a later file's own default
  // getMutationRuntime() (no setMutationStorageForTests override) discovers
  // and re-pins.
  globalThis.indexedDB = new IDBFactory();
});

describe("reconciliation from the live ThreadModel", () => {
  test("hydrating a thread with a live, unresolved ask_user call populates one batch", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => ({
      thread: {
        ...testThread("ref_a"),
        turns: [
          {
            id: "turn_1",
            status: "completed",
            itemsView: "full",
            items: [
              {
                type: "commandExecution",
                id: "item_1",
                turnId: "turn_1",
                toolName: "ask_user",
                callId: "call_1",
                status: "completed",
                argumentsJson: askArgs(ONE_QUESTION),
              },
            ],
          },
        ],
      },
    }));

    await threadsStore.getState().ensureThread("ref_a");

    const refState = askDockStore.getState().byRef.get("ref_a");
    expect(refState?.batches).toHaveLength(1);
    expect(refState?.batches[0]?.questions.map((q) => q.header)).toEqual(["Deploy?"]);
  });

  test("a live ask_user ack (item/started then item/completed) adds a fresh batch", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    expect(askDockStore.getState().byRef.get("ref_a")).toBeUndefined();

    startTurn(fake, "ref_a", "turn_1");
    ackAskUserCall(fake, "ref_a", "turn_1", "item_1", "call_1");

    const refState = askDockStore.getState().byRef.get("ref_a");
    expect(refState?.batches).toHaveLength(1);
    expect(refState?.batches[0]?.questions.map((q) => q.key)).toEqual(["call_1:0"]);
  });

  test("a second ask_user ack with no reply in between merges into the same open batch", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");

    startTurn(fake, "ref_a", "turn_1");
    ackAskUserCall(fake, "ref_a", "turn_1", "item_1", "call_1");
    ackAskUserCall(fake, "ref_a", "turn_1", "item_2", "call_2");

    const refState = askDockStore.getState().byRef.get("ref_a");
    expect(refState?.batches).toHaveLength(1);
    expect(refState?.batches[0]?.questions.map((q) => q.key)).toEqual(["call_1:0", "call_2:0"]);
  });

  test("a foreign user message resolves an open (not-sending) batch entirely", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    startTurn(fake, "ref_a", "turn_1");
    ackAskUserCall(fake, "ref_a", "turn_1", "item_1", "call_1");
    expect(askDockStore.getState().byRef.get("ref_a")?.batches).toHaveLength(1);

    startTurn(fake, "ref_a", "turn_2");
    fake.emitNotification(userMessageNotification("ref_a", "turn_2", "item_2", "[answers]\n1. [Deploy?] -> skip"));

    expect(askDockStore.getState().byRef.get("ref_a")?.batches ?? []).toEqual([]);
  });
});

describe("setAnswer / setNote", () => {
  test("setAnswer records a resolution for a ref+key, independent of other keys", () => {
    askDockStore.getState().setAnswer("ref_a", "call_1:0", { kind: "skip" });
    askDockStore.getState().setAnswer("ref_a", "call_2:0", { kind: "free", text: "hi" });
    expect(askDockStore.getState().byRef.get("ref_a")?.answers).toEqual({
      "call_1:0": { resolution: { kind: "skip" }, note: "" },
      "call_2:0": { resolution: { kind: "free", text: "hi" }, note: "" },
    });
  });

  test("setNote preserves the existing resolution for that key", () => {
    askDockStore.getState().setAnswer("ref_a", "call_1:0", { kind: "skip" });
    askDockStore.getState().setNote("ref_a", "call_1:0", "please confirm");
    expect(askDockStore.getState().byRef.get("ref_a")?.answers["call_1:0"]).toEqual({
      resolution: { kind: "skip" },
      note: "please confirm",
    });
  });

  test("setAnswer preserves an existing note for that key", () => {
    askDockStore.getState().setNote("ref_a", "call_1:0", "will revisit");
    askDockStore.getState().setAnswer("ref_a", "call_1:0", { kind: "skip" });
    expect(askDockStore.getState().byRef.get("ref_a")?.answers["call_1:0"]).toEqual({
      resolution: { kind: "skip" },
      note: "will revisit",
    });
  });
});

describe("sendBatch", () => {
  async function setupOneBatch(fake: FakeClient): Promise<string> {
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    startTurn(fake, "ref_a", "turn_1");
    ackAskUserCall(fake, "ref_a", "turn_1", "item_1", "call_1");
    const batchId = askDockStore.getState().byRef.get("ref_a")?.batches[0]?.id;
    if (!batchId) throw new Error("test setup: expected one batch to exist");
    return batchId;
  }

  test("composes the batch's answers verbatim and sends through the plain send() path", async () => {
    const fake = connectFakeClient();
    const batchId = await setupOneBatch(fake);
    askDockStore.getState().setAnswer("ref_a", "call_1:0", { kind: "option", labels: ["Yes"] });
    fake.on("turn/start", () => new Promise(() => {}));

    const outcome = await askDockStore.getState().sendBatch("ref_a", batchId);

    expect(outcome).toEqual({ outcome: "sent" });
    const [record] = (await readMutationPersistence("ref_a")).outbox;
    expect(record?.payload).toMatchObject({
      ref: "ref_a",
      input: [{ type: "text", text: '[answers]\n1. [Deploy?] → "Yes"' }],
    });
  });

  test("an unresolved question composes as skipped, matching the ordinary composer's own send path", async () => {
    const fake = connectFakeClient();
    const batchId = await setupOneBatch(fake);
    fake.on("turn/start", () => new Promise(() => {}));

    await askDockStore.getState().sendBatch("ref_a", batchId);

    const [record] = (await readMutationPersistence("ref_a")).outbox;
    expect(record?.payload).toMatchObject({
      ref: "ref_a",
      input: [{ type: "text", text: "[answers]\n1. [Deploy?] → skipped (no answer)" }],
    });
  });

  test("clears that batch's answers once it settles successfully", async () => {
    const fake = connectFakeClient();
    const batchId = await setupOneBatch(fake);
    askDockStore.getState().setAnswer("ref_a", "call_1:0", { kind: "skip" });
    fake.on("turn/start", (params) => ({
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: "thread_a",
        projectionState: "reflected",
      },
      turn: { id: "turn_2", status: "inProgress", itemsView: "" },
    }));

    await askDockStore.getState().sendBatch("ref_a", batchId);

    expect(askDockStore.getState().byRef.get("ref_a")?.answers["call_1:0"]).toBeUndefined();
  });

  test("a later ask_user ack after a SUCCESSFUL send also never resurrects the already-settled question", async () => {
    // The settled call's own ask_user item never changes status in the
    // transcript either (no test double here simulates the eventual
    // userMessage echo) - the same permanent exclusion that protects the
    // Conflict path must also cover the plain success path, since both
    // route through the same removeBatch.
    const fake = connectFakeClient();
    const batchId = await setupOneBatch(fake);
    fake.on("turn/start", (params) => ({
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: "thread_a",
        projectionState: "reflected",
      },
      turn: { id: "turn_2", status: "inProgress", itemsView: "" },
    }));
    await askDockStore.getState().sendBatch("ref_a", batchId);
    expect(askDockStore.getState().byRef.get("ref_a")?.batches ?? []).toEqual([]);

    startTurn(fake, "ref_a", "turn_3");
    ackAskUserCall(fake, "ref_a", "turn_3", "item_3", "call_3");

    const refState = askDockStore.getState().byRef.get("ref_a");
    expect(refState?.batches).toHaveLength(1);
    expect(refState?.batches[0]?.questions.map((q) => q.key)).toEqual(["call_3:0"]);
  });

  test("is a no-op (never calls turn/start) when the batch no longer exists - already resolved elsewhere", async () => {
    const fake = connectFakeClient();
    const batchId = await setupOneBatch(fake);
    startTurn(fake, "ref_a", "turn_2");
    fake.emitNotification(userMessageNotification("ref_a", "turn_2", "item_2", "[answers]..."));
    expect(askDockStore.getState().byRef.get("ref_a")?.batches ?? []).toEqual([]);

    const outcome = await askDockStore.getState().sendBatch("ref_a", batchId);

    expect(outcome).toEqual({ outcome: "stale" });
    expect(fake.calls.some((c) => c.method === "turn/start")).toBe(false);
  });
});

describe("active tab (kata 99yf)", () => {
  // setupBatch hydrates one batch holding `questions` and returns its id.
  async function setupBatch(fake: FakeClient, questions: Array<Record<string, unknown>>): Promise<string> {
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    startTurn(fake, "ref_a", "turn_1");
    ackAskUserCallWith(fake, "ref_a", "turn_1", "item_1", "call_1", questions);
    const batchId = askDockStore.getState().byRef.get("ref_a")?.batches[0]?.id;
    if (!batchId) throw new Error("test setup: expected one batch to exist");
    return batchId;
  }

  function activeFor(batchId: string): string | undefined {
    return askDockStore.getState().byRef.get("ref_a")?.active[batchId];
  }

  test("setActive records the visible question and rejects keys outside the batch", async () => {
    const fake = connectFakeClient();
    const batchId = await setupBatch(fake, TWO_QUESTIONS);

    askDockStore.getState().setActive("ref_a", batchId, "call_1:1");
    expect(activeFor(batchId)).toBe("call_1:1");

    askDockStore.getState().setActive("ref_a", batchId, "call_1:99");
    expect(activeFor(batchId)).toBe("call_1:1");

    askDockStore.getState().setActive("ref_a", "ask-batch-999", "call_1:0");
    expect(activeFor("ask-batch-999")).toBeUndefined();
  });

  test("a one-click single-select answer auto-advances to the next unanswered question", async () => {
    const fake = connectFakeClient();
    const batchId = await setupBatch(fake, TWO_QUESTIONS);

    askDockStore.getState().setAnswer("ref_a", "call_1:0", { kind: "option", labels: ["a"] });
    expect(activeFor(batchId)).toBe("call_1:1");

    // Once nothing unanswered remains the reader stays put (and a skip is
    // itself a one-click resolution that would advance if there were
    // anywhere to go).
    askDockStore.getState().setAnswer("ref_a", "call_1:1", { kind: "skip" });
    expect(activeFor(batchId)).toBe("call_1:1");
  });

  test("auto-advance wraps to an earlier unanswered question", async () => {
    const fake = connectFakeClient();
    const batchId = await setupBatch(fake, THREE_QUESTIONS);

    askDockStore.getState().setActive("ref_a", batchId, "call_1:2");
    askDockStore.getState().setAnswer("ref_a", "call_1:2", { kind: "fallback" });
    expect(activeFor(batchId)).toBe("call_1:0");
  });

  test("multi-select, free-text, and let-serf-decide answers never auto-advance", async () => {
    const fake = connectFakeClient();
    const batchId = await setupBatch(fake, MULTI_THEN_SINGLE);

    askDockStore.getState().setAnswer("ref_a", "call_1:0", { kind: "option", labels: ["a"] });
    expect(activeFor(batchId)).toBeUndefined();

    askDockStore.getState().setAnswer("ref_a", "call_1:1", { kind: "free", text: "x" });
    expect(activeFor(batchId)).toBeUndefined();

    askDockStore.getState().setAnswer("ref_a", "call_1:1", { kind: "decide", leaning: "" });
    expect(activeFor(batchId)).toBeUndefined();
  });

  test("answering a question that is not the visible tab does not move the active tab", async () => {
    const fake = connectFakeClient();
    const batchId = await setupBatch(fake, TWO_QUESTIONS);

    askDockStore.getState().setActive("ref_a", batchId, "call_1:1");
    askDockStore.getState().setAnswer("ref_a", "call_1:0", { kind: "option", labels: ["a"] });
    expect(activeFor(batchId)).toBe("call_1:1");
  });

  test("the active entry is pruned when its batch is sent", async () => {
    const fake = connectFakeClient();
    const batchId = await setupBatch(fake, TWO_QUESTIONS);
    askDockStore.getState().setActive("ref_a", batchId, "call_1:1");
    fake.on("turn/start", () => new Promise(() => {}));

    await askDockStore.getState().sendBatch("ref_a", batchId);

    expect(activeFor(batchId)).toBeUndefined();
  });

  test("the active entry is pruned when its batch resolves elsewhere", async () => {
    const fake = connectFakeClient();
    const batchId = await setupBatch(fake, TWO_QUESTIONS);
    askDockStore.getState().setActive("ref_a", batchId, "call_1:1");

    startTurn(fake, "ref_a", "turn_2");
    fake.emitNotification(userMessageNotification("ref_a", "turn_2", "item_2", "[answers]..."));

    expect(askDockStore.getState().byRef.get("ref_a")?.batches ?? []).toEqual([]);
    expect(activeFor(batchId)).toBeUndefined();
  });
});
