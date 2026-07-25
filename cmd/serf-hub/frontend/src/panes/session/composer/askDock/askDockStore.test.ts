import { beforeEach, describe, expect, test } from "vitest";
import type { ConnectionState } from "../../../../protocol/client";
import { WireError } from "../../../../protocol/errors";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { AnyNotification, Thread, ThreadCapabilities, ThreadReadResponse } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../../../stores/threads";
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
    serf: { ref, capabilities: CAPABILITIES, queue: {} },
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

// startTurn emits turn/started - every item/started|completed notification
// below requires its turn to already exist in the model
// (reducer.ts's resolveInsertTurnId), exactly like the real wire: a turn
// always starts before any of its items do. Each distinct turnId used by a
// test must be started exactly once.
function startTurn(fake: FakeClient, ref: string, turnId: string): void {
  fake.emitNotification({
    method: "turn/started",
    params: { ref, turn: { id: turnId, status: "inProgress", itemsView: "" } },
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
    params: { ref, turnId, item: { type: "userMessage", id: itemId, turnId, text, status: "completed" } },
  };
}

// ackAskUserCall assumes `turnId` has already been started (startTurn).
function ackAskUserCall(fake: FakeClient, ref: string, turnId: string, itemId: string, callId: string): void {
  fake.emitNotification(askItemNotification(ref, turnId, itemId, callId, "item/started", "inProgress"));
  fake.emitNotification(askItemNotification(ref, turnId, itemId, callId, "item/completed", "completed"));
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetAskDockStoreForTests();
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
    fake.on("turn/start", () => ({ turn: { id: "turn_2", status: "inProgress", itemsView: "" } }));

    const outcome = await askDockStore.getState().sendBatch("ref_a", batchId);

    expect(outcome).toEqual({ outcome: "sent" });
    const call = fake.calls.find((c) => c.method === "turn/start");
    expect(call?.params).toEqual({ ref: "ref_a", input: [{ type: "text", text: '[answers]\n1. [Deploy?] → "Yes"' }] });
  });

  test("an unresolved question composes as skipped, matching the ordinary composer's own send path", async () => {
    const fake = connectFakeClient();
    const batchId = await setupOneBatch(fake);
    fake.on("turn/start", () => ({ turn: { id: "turn_2", status: "inProgress", itemsView: "" } }));

    await askDockStore.getState().sendBatch("ref_a", batchId);

    const call = fake.calls.find((c) => c.method === "turn/start");
    expect(call?.params).toEqual({
      ref: "ref_a",
      input: [{ type: "text", text: "[answers]\n1. [Deploy?] → skipped (no answer)" }],
    });
  });

  test("marks the batch sending for the duration of the request, then removes it on success", async () => {
    const fake = connectFakeClient();
    const batchId = await setupOneBatch(fake);
    let resolveSend!: () => void;
    fake.on(
      "turn/start",
      () =>
        new Promise((resolve) => {
          resolveSend = () => resolve({ turn: { id: "turn_2", status: "inProgress", itemsView: "" } });
        }),
    );

    const pending = askDockStore.getState().sendBatch("ref_a", batchId);
    await Promise.resolve();
    await Promise.resolve();
    expect(askDockStore.getState().byRef.get("ref_a")?.batches[0]?.sending).toBe(true);

    resolveSend();
    await pending;
    expect(askDockStore.getState().byRef.get("ref_a")?.batches ?? []).toEqual([]);
  });

  test("clears that batch's answers once it settles successfully", async () => {
    const fake = connectFakeClient();
    const batchId = await setupOneBatch(fake);
    askDockStore.getState().setAnswer("ref_a", "call_1:0", { kind: "skip" });
    fake.on("turn/start", () => ({ turn: { id: "turn_2", status: "inProgress", itemsView: "" } }));

    await askDockStore.getState().sendBatch("ref_a", batchId);

    expect(askDockStore.getState().byRef.get("ref_a")?.answers["call_1:0"]).toBeUndefined();
  });

  test("a Conflict rejection removes the batch (discarded, not settled) and returns the composed text for fallback", async () => {
    const fake = connectFakeClient();
    const batchId = await setupOneBatch(fake);
    askDockStore.getState().setAnswer("ref_a", "call_1:0", { kind: "skip" });
    fake.on("turn/start", () => {
      throw new WireError("input buffer full", -32013, { serfErrorInfo: "conflict" });
    });

    const outcome = await askDockStore.getState().sendBatch("ref_a", batchId);

    expect(outcome).toEqual({ outcome: "conflict", text: "[answers]\n1. [Deploy?] → skipped (no answer)" });
    expect(askDockStore.getState().byRef.get("ref_a")?.batches ?? []).toEqual([]);
  });

  test("a later ask_user ack after a Conflict starts a completely fresh pending set, not merged into the discarded one", async () => {
    const fake = connectFakeClient();
    const batchId = await setupOneBatch(fake);
    fake.on("turn/start", () => {
      throw new WireError("input buffer full", -32013, { serfErrorInfo: "conflict" });
    });
    await askDockStore.getState().sendBatch("ref_a", batchId);
    expect(askDockStore.getState().byRef.get("ref_a")?.batches ?? []).toEqual([]);

    startTurn(fake, "ref_a", "turn_2");
    ackAskUserCall(fake, "ref_a", "turn_2", "item_2", "call_2");

    const refState = askDockStore.getState().byRef.get("ref_a");
    expect(refState?.batches).toHaveLength(1);
    expect(refState?.batches[0]?.questions.map((q) => q.key)).toEqual(["call_2:0"]);
  });

  test("a later ask_user ack after a SUCCESSFUL send also never resurrects the already-settled question", async () => {
    // The settled call's own ask_user item never changes status in the
    // transcript either (no test double here simulates the eventual
    // userMessage echo) - the same permanent exclusion that protects the
    // Conflict path must also cover the plain success path, since both
    // route through the same removeBatch.
    const fake = connectFakeClient();
    const batchId = await setupOneBatch(fake);
    fake.on("turn/start", () => ({ turn: { id: "turn_2", status: "inProgress", itemsView: "" } }));
    await askDockStore.getState().sendBatch("ref_a", batchId);
    expect(askDockStore.getState().byRef.get("ref_a")?.batches ?? []).toEqual([]);

    startTurn(fake, "ref_a", "turn_3");
    ackAskUserCall(fake, "ref_a", "turn_3", "item_3", "call_3");

    const refState = askDockStore.getState().byRef.get("ref_a");
    expect(refState?.batches).toHaveLength(1);
    expect(refState?.batches[0]?.questions.map((q) => q.key)).toEqual(["call_3:0"]);
  });

  test("a non-Conflict rejection keeps the batch and flips sending back to false (retryable)", async () => {
    const fake = connectFakeClient();
    const batchId = await setupOneBatch(fake);
    fake.on("turn/start", () => {
      throw new Error("network hiccup");
    });

    const outcome = await askDockStore.getState().sendBatch("ref_a", batchId);

    expect(outcome).toEqual({ outcome: "error", message: "network hiccup" });
    const batch = askDockStore.getState().byRef.get("ref_a")?.batches[0];
    expect(batch?.id).toBe(batchId);
    expect(batch?.sending).toBe(false);
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

  test("a second concurrent sendBatch call on the same already-sending batch is a no-op", async () => {
    const fake = connectFakeClient();
    const batchId = await setupOneBatch(fake);
    let resolveSend!: () => void;
    fake.on(
      "turn/start",
      () =>
        new Promise((resolve) => {
          resolveSend = () => resolve({ turn: { id: "turn_2", status: "inProgress", itemsView: "" } });
        }),
    );

    const first = askDockStore.getState().sendBatch("ref_a", batchId);
    await Promise.resolve();
    await Promise.resolve();
    const second = await askDockStore.getState().sendBatch("ref_a", batchId);

    expect(second).toEqual({ outcome: "stale" });
    resolveSend();
    await first;
    expect(fake.calls.filter((c) => c.method === "turn/start")).toHaveLength(1);
  });

  test("a sibling ask_user call acked while this batch's send is in flight forms its own independent batch, never merged into the in-flight one", async () => {
    const fake = connectFakeClient();
    const batchId = await setupOneBatch(fake);
    let resolveSend!: () => void;
    fake.on(
      "turn/start",
      () =>
        new Promise((resolve) => {
          resolveSend = () => resolve({ turn: { id: "turn_2", status: "inProgress", itemsView: "" } });
        }),
    );

    const pending = askDockStore.getState().sendBatch("ref_a", batchId);
    await Promise.resolve();
    await Promise.resolve();
    ackAskUserCall(fake, "ref_a", "turn_1", "item_2", "call_2");

    let refState = askDockStore.getState().byRef.get("ref_a");
    expect(refState?.batches).toHaveLength(2);
    const sibling = refState?.batches.find((b) => b.id !== batchId);
    expect(sibling?.questions.map((q) => q.key)).toEqual(["call_2:0"]);
    expect(sibling?.sending).toBe(false);

    resolveSend();
    await pending;

    refState = askDockStore.getState().byRef.get("ref_a");
    expect(refState?.batches).toHaveLength(1);
    expect(refState?.batches[0]?.id).toBe(sibling?.id);
  });
});
