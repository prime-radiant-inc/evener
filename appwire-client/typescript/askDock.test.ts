// @vitest-environment node
import { describe, expect, test, vi } from "vitest";
import { type AskAnswerSender, type AskDockThreadsSnapshot, createAskDockStore, nextUnansweredKey } from "./askDock";
import type { AskQuestionRef } from "./deriveAskQuestions";
import { createFrameworkFreeStore } from "./frameworkFreeStore";
import type { ItemModel, ThreadModel } from "./model";
import { pruneBackgroundHistoryBodies } from "./reducer";

// --- fixtures -------------------------------------------------------------

function askItem(id: string, callId: string, questions: Array<Record<string, unknown>>): ItemModel {
  return {
    id,
    turnId: "turn_1",
    type: "commandExecution",
    toolName: "ask_user",
    status: "completed",
    text: "",
    callId,
    argumentsJSON: JSON.stringify({ questions }),
  };
}

// liveAskQuestions reads the wire's askPending plus the turns; the rest of
// ThreadModel is noise here. askPending: true matches every fixture below
// actually carrying an unanswered ask_user item - the wire gate itself has
// its own coverage in deriveAskQuestions.test.ts.
function threadModel(items: ItemModel[]): ThreadModel {
  return { askPending: true, turns: [{ id: "turn_1", status: "completed", items }] } as unknown as ThreadModel;
}

function recordedThread(items: ItemModel[]): ThreadModel {
  const model = threadModel(items);
  return {
    ...model,
    ref: "ref_a",
    threadId: "thread_a",
    history: {
      bootGeneration: "1",
      epoch: 1,
      length: items.length,
      appliedGeneration: 1,
      issuedGeneration: 1,
      deferredPages: [],
      turns: model.turns,
    },
  };
}

const DEPLOY = [{ header: "Deploy?", question: "Ship now?", options: [{ label: "Yes", detail: "" }] }];

/** A thread source: a plain store whose state carries `threads`, which is all
 * the port asks for; publish replaces the map. */
function fakeThreads() {
  const store = createFrameworkFreeStore<AskDockThreadsSnapshot>(() => ({ threads: new Map() }));
  return {
    ...store,
    publish: (ref: string, model: ThreadModel) =>
      store.setState((s) => ({ threads: new Map(s.threads).set(ref, model) })),
  };
}

const fakeSender = () => vi.fn<AskAnswerSender>(async () => {});

const first: AskQuestionRef = {
  key: "first:0",
  callId: "first",
  header: "First",
  question: "Choose",
  options: [],
  multiSelect: false,
};
const second: AskQuestionRef = { ...first, key: "second:0", callId: "second" };

// --- action shape ------------------------------------------------------------

describe("store-bound actions", () => {
  test("state carries only the data; every action lives on the store object", () => {
    const store = createAskDockStore({ send: fakeSender() });
    expect(Object.keys(store.getState()).sort()).toEqual(["byRef", "mintedBatches"]);
    for (const action of ["setAnswer", "setNote", "setActive", "markPendingGreeted", "sendBatch"] as const) {
      expect(typeof store[action]).toBe("function");
    }
    // The already store-bound actions keep their home beside the five.
    for (const action of ["reconcile", "beginSend", "finishSend", "followThreads"] as const) {
      expect(typeof store[action]).toBe("function");
    }
  });
});

// --- two instances share nothing ---------------------------------------------

describe("two stores share nothing", () => {
  test("a batch, an answer, a send and its exclusion in one store are invisible to the other", async () => {
    const threadsA = fakeThreads();
    const threadsB = fakeThreads();
    const sendA = fakeSender();
    const sendB = fakeSender();
    const a = createAskDockStore({ send: sendA });
    const b = createAskDockStore({ send: sendB });
    a.followThreads(threadsA);
    b.followThreads(threadsB);

    const model = threadModel([askItem("i1", "call_1", DEPLOY)]);
    threadsA.publish("ref_a", model);
    const batch = a.getState().byRef.get("ref_a")?.batches[0];
    expect(batch?.questions.map((q) => q.key)).toEqual(["call_1:0"]);
    expect(b.getState().byRef.size).toBe(0);

    a.setAnswer("ref_a", "call_1:0", { kind: "option", labels: ["Yes"] });
    expect(b.getState().byRef.get("ref_a")).toBeUndefined();

    await expect(a.sendBatch("ref_a", batch?.id ?? "")).resolves.toEqual({ outcome: "sent" });
    expect(sendA).toHaveBeenCalledTimes(1);
    expect(sendA).toHaveBeenCalledWith("ref_a", '[answers]\n1. [Deploy?] → "Yes"');
    expect(sendB).not.toHaveBeenCalled();
    expect(a.getState().byRef.get("ref_a")?.batches).toEqual([]);

    // The same model reaches both: A remembers it settled call_1:0, B has no such memory.
    threadsA.publish("ref_a", threadModel([askItem("i1", "call_1", DEPLOY)]));
    threadsB.publish("ref_a", model);
    expect(a.getState().byRef.get("ref_a")?.batches).toEqual([]);
    expect(
      b
        .getState()
        .byRef.get("ref_a")
        ?.batches.map((batch) => batch.questions[0]?.key),
    ).toEqual(["call_1:0"]);
  });
});

// --- wiring -----------------------------------------------------------------

describe("ports", () => {
  test("body pruning before a local-opening token preserves pending drafts and activation through restoration", () => {
    const threads = fakeThreads();
    const store = createAskDockStore();
    store.followThreads(threads);
    const model = recordedThread([
      { id: "earlier-user", turnId: "turn_1", type: "userMessage", text: "EARLIER_SENTINEL" },
      askItem("i1", "call_1", [...DEPLOY, ...DEPLOY]),
    ]);
    threads.publish("ref_a", model);
    const batch = store.getState().byRef.get("ref_a")?.batches[0];
    if (!batch) throw new Error("Missing batch");
    store.setAnswer("ref_a", "call_1:0", { kind: "free", text: "DRAFT_SENTINEL" });
    store.setNote("ref_a", "call_1:0", "NOTE_SENTINEL");
    store.setActive("ref_a", batch.id, "call_1:1");
    store.markPendingGreeted("ref_a");
    const before = store.getState().byRef.get("ref_a");
    const pruned = pruneBackgroundHistoryBodies(model);
    expect(pruned.turns.flatMap((t) => t.items)).toEqual([]);
    expect(pruned.history?.turns[0]?.items[1]).toMatchObject({ id: "i1", callId: "call_1" });
    // The source has no local-opening token: pruning is published before it exists.
    threads.publish("ref_a", pruned);
    expect(store.getState().byRef.get("ref_a")).toEqual(before);
    threads.publish("ref_a", model);
    expect(store.getState().byRef.get("ref_a")).toEqual(before);
  });

  test("followThreads' disposer stops reconciliation", () => {
    const threads = fakeThreads();
    const store = createAskDockStore();
    const dispose = store.followThreads(threads);
    threads.publish("ref_a", threadModel([askItem("i1", "call_1", DEPLOY)]));
    const batch = store.getState().byRef.get("ref_a")?.batches[0];
    expect(batch).toBeDefined();
    dispose();
    threads.publish("ref_a", threadModel([askItem("i1", "call_1", DEPLOY), askItem("i2", "call_2", DEPLOY)]));
    expect(store.getState().byRef.get("ref_a")?.batches).toEqual([batch]);
  });

  test.each([
    "false",
    "removed",
    "display-only",
    "new-item-id",
    "new-call-id",
    "new-thread",
    "new-instance",
    "resident-empty",
    "resident-error",
  ] as const)("pruned pending questions clear on %s rather than sticking to the pending flag", (change) => {
    const threads = fakeThreads();
    const store = createAskDockStore();
    store.followThreads(threads);
    const model = recordedThread([askItem("i1", "call_1", DEPLOY)]);
    threads.publish("ref_a", model);
    const pruned = pruneBackgroundHistoryBodies(model);
    threads.publish("ref_a", pruned);
    expect(store.getState().byRef.get("ref_a")?.batches).toHaveLength(1);
    let next = pruned;
    if (change === "false") next = { ...pruned, askPending: false };
    if (change === "new-thread") next = { ...pruned, threadId: "replacement" };
    if (change === "new-instance") next = { ...pruned, instanceId: "replacement" };
    if (change === "removed") next = pruneBackgroundHistoryBodies(recordedThread([]));
    if (change === "display-only") next = { ...recordedThread([]), turns: pruned.history?.turns ?? [] };
    if (change === "new-item-id")
      next = pruneBackgroundHistoryBodies(recordedThread([askItem("i2", "call_1", DEPLOY)]));
    if (change === "new-call-id")
      next = pruneBackgroundHistoryBodies(recordedThread([askItem("i1", "call_2", DEPLOY)]));
    if (change === "resident-empty") next = recordedThread([askItem("i1", "call_1", [])]);
    if (change === "resident-error")
      next = recordedThread([{ ...askItem("i1", "call_1", DEPLOY), error: "ERROR_SENTINEL" }]);
    threads.publish("ref_a", next);
    expect(store.getState().byRef.get("ref_a")?.batches).toEqual([]);
    expect(store.getState().byRef.get("ref_a")?.answers).toEqual({});
  });

  test.each([
    { type: "userMessage" },
    { type: "steering", source: "user" },
    { type: "steering", steeringKind: "interrupted" },
  ])("a real $type resolution replaces a retained question with the new question", (resolution) => {
    const threads = fakeThreads();
    const store = createAskDockStore();
    store.followThreads(threads);
    const model = recordedThread([askItem("i1", "call_1", DEPLOY)]);
    threads.publish("ref_a", model);
    store.setNote("ref_a", "call_1:0", "OLD_NOTE");
    store.markPendingGreeted("ref_a");
    const pruned = pruneBackgroundHistoryBodies(model);
    threads.publish("ref_a", pruned);
    const identity = pruned.history?.turns[0]?.items[0];
    if (!identity) throw new Error("Missing canonical identity");
    const reply: ItemModel = { id: "reply", turnId: "turn_1", text: "REPLY_SENTINEL", ...resolution };
    const replacement = recordedThread([identity, reply, askItem("i2", "call_2", DEPLOY)]);
    threads.publish("ref_a", replacement);
    const state = store.getState().byRef.get("ref_a");
    expect(state?.batches.flatMap((batch) => batch.questions.map((q) => q.key))).toEqual(["call_2:0"]);
    expect(state?.answers).toEqual({});
    expect(state?.activationEpoch).toBe(2);
    expect(state?.pendingGreeted).toBe(false);
    threads.publish("ref_a", pruneBackgroundHistoryBodies(replacement));
    expect(store.getState().byRef.get("ref_a")).toEqual(state);
    threads.publish("ref_a", replacement);
    expect(store.getState().byRef.get("ref_a")).toEqual(state);
  });

  test("a resolving overlay clears retained questions while an earlier positioned notice does not", () => {
    const threads = fakeThreads();
    const store = createAskDockStore();
    store.followThreads(threads);
    const model = recordedThread([{ ...askItem("i1", "call_1", DEPLOY), position: { entry: 2, item: 0 } }]);
    threads.publish("ref_a", model);
    const pruned = pruneBackgroundHistoryBodies(model);
    threads.publish("ref_a", pruned);
    const reply: ItemModel = { id: "reply", turnId: "turn_1", type: "steering", steeringKind: "interrupted", text: "" };
    const turn = model.turns[0];
    if (!turn) throw new Error("Missing turn");
    threads.publish("ref_a", {
      ...pruned,
      turns: [{ ...turn, items: [{ ...reply, position: { entry: 1, item: 0 } }] }],
    });
    expect(store.getState().byRef.get("ref_a")?.batches).toHaveLength(1);
    threads.publish("ref_a", { ...pruned, turns: [{ ...turn, items: [reply] }] });
    expect(store.getState().byRef.get("ref_a")?.batches).toEqual([]);
  });

  test("unknown omitted calls are not invented, and submitted or in-flight batches retain their existing rules", () => {
    const threads = fakeThreads();
    const store = createAskDockStore();
    store.followThreads(threads);
    const model = recordedThread([askItem("i1", "call_1", DEPLOY)]);
    const pruned = pruneBackgroundHistoryBodies(model);
    threads.publish("ref_a", pruned);
    expect(store.getState().byRef.get("ref_a")).toBeUndefined();
    threads.publish("ref_a", model);
    const batch = store.getState().byRef.get("ref_a")?.batches[0];
    if (!batch) throw new Error("Missing batch");
    store.beginSend("ref_a", batch.id);
    threads.publish("ref_a", pruned);
    expect(store.getState().byRef.get("ref_a")?.batches[0]?.sending).toBe(true);
    threads.publish("ref_a", { ...pruned, askPending: false });
    expect(store.getState().byRef.get("ref_a")?.batches[0]?.sending).toBe(true);
    store.finishSend("ref_a", batch.id, true);
    threads.publish("ref_a", pruned);
    threads.publish("ref_a", model);
    expect(store.getState().byRef.get("ref_a")?.batches).toEqual([]);
    expect(store.getState().byRef.get("ref_a")?.excludedKeys.has("call_1:0")).toBe(true);
  });

  test.each([{ source: "user", steeringKind: "human-note" }, { source: "daemon" }])(
    "non-resolving $source steering keeps the omitted question and a rejected send retryable",
    (steering) => {
      const threads = fakeThreads();
      const store = createAskDockStore();
      store.followThreads(threads);
      const model = recordedThread([askItem("i1", "call_1", DEPLOY)]);
      threads.publish("ref_a", model);
      const batch = store.getState().byRef.get("ref_a")?.batches[0];
      if (!batch) throw new Error("Missing batch");
      store.setNote("ref_a", "call_1:0", "NOTE_SENTINEL");
      store.beginSend("ref_a", batch.id);
      const pruned = pruneBackgroundHistoryBodies(model);
      threads.publish("ref_a", pruned);
      store.finishSend("ref_a", batch.id, false);
      const before = store.getState().byRef.get("ref_a");
      const identity = pruned.history?.turns[0]?.items[0];
      if (!identity) throw new Error("Missing canonical identity");
      threads.publish(
        "ref_a",
        recordedThread([identity, { id: "note", turnId: "turn_1", type: "steering", text: "", ...steering }]),
      );
      expect(store.getState().byRef.get("ref_a")).toEqual(before);
      threads.publish("ref_a", model);
      expect(store.getState().byRef.get("ref_a")).toEqual(before);
      expect(store.beginSend("ref_a", batch.id)).toBe(true);
    },
  );

  test("sendBatch on a store built without a sender fails loudly rather than dropping the answers", async () => {
    const store = createAskDockStore();
    store.reconcile("ref_a", [first]);
    const batch = store.getState().byRef.get("ref_a")?.batches[0];
    await expect(store.sendBatch("ref_a", batch?.id ?? "")).rejects.toThrow(/\{ send \}/);
    expect(store.getState().byRef.get("ref_a")?.batches[0]?.sending).toBe(false);
  });

  test("a rejected send leaves the batch intact and retryable, with the sentence the dock shows", async () => {
    const threads = fakeThreads();
    const send = vi.fn<AskAnswerSender>(async () => {
      throw new Error("socket closed");
    });
    const store = createAskDockStore({ send });
    store.followThreads(threads);
    threads.publish("ref_a", threadModel([askItem("i1", "call_1", DEPLOY)]));
    const batch = store.getState().byRef.get("ref_a")?.batches[0];
    const outcome = await store.sendBatch("ref_a", batch?.id ?? "");
    expect(outcome).toEqual({ outcome: "error", message: "Couldn't send answers: socket closed" });
    const after = store.getState().byRef.get("ref_a")?.batches[0];
    expect(after?.id).toBe(batch?.id);
    expect(after?.sending).toBe(false);
    await expect(store.sendBatch("ref_a", batch?.id ?? "")).resolves.toEqual({
      outcome: "error",
      message: "Couldn't send answers: socket closed",
    });
    expect(send).toHaveBeenCalledTimes(2);
  });
  test("a compose failure never freezes the batch: it stays retryable once the answer is fixed", async () => {
    const send = fakeSender();
    const store = createAskDockStore({ send });
    store.reconcile("ref_a", [first]);
    const batch = store.getState().byRef.get("ref_a")?.batches[0];
    if (!batch) throw new Error("Missing batch");
    // A note the types forbid is the one way to make composeAskAnswers throw
    // (note.trim()); nothing on the wire can produce it, so a throw here is a
    // programming error that must not strand the batch mid-send.
    store.setNote("ref_a", first.key, null as unknown as string);
    await expect(store.sendBatch("ref_a", batch.id)).rejects.toThrow(TypeError);
    expect(send).not.toHaveBeenCalled();
    expect(store.getState().byRef.get("ref_a")?.batches[0]?.sending).toBe(false);
    store.setNote("ref_a", first.key, "");
    await expect(store.sendBatch("ref_a", batch.id)).resolves.toEqual({ outcome: "sent" });
    expect(send).toHaveBeenCalledTimes(1);
  });
});

// --- question-list reconciliation and the send primitives (the contract the
// native sheet drives directly, without a thread source) ----------------------

describe("reconcile / beginSend / finishSend", () => {
  test("a sending batch is frozen and settles only its own questions", () => {
    const store = createAskDockStore();
    const batchesOf = () => store.getState().byRef.get("c")?.batches ?? [];
    store.reconcile("c", [first]);
    const batch = batchesOf()[0];
    if (!batch) throw new Error("Missing batch");
    expect(store.beginSend("c", batch.id)).toBe(true);
    store.reconcile("c", [first, second]);
    expect(batchesOf().map((b) => b.questions.map((q) => q.key))).toEqual([[first.key], [second.key]]);
    store.reconcile("c", [second]);
    expect(batchesOf()[0]?.questions).toEqual([first]);
    store.finishSend("c", batch.id, true);
    store.reconcile("c", [first, second]);
    expect(batchesOf().flatMap((b) => b.questions)).toEqual([second]);
  });

  test("beginSend refuses a repeated or unknown batch; a failed finish retains it", () => {
    const store = createAskDockStore();
    const batchesOf = () => store.getState().byRef.get("c")?.batches ?? [];
    store.reconcile("c", [first, second]);
    const batch = batchesOf()[0];
    if (!batch) throw new Error("Missing batch");
    expect(store.beginSend("c", "no-such-batch")).toBe(false);
    expect(store.beginSend("c", batch.id)).toBe(true);
    expect(store.beginSend("c", batch.id)).toBe(false);
    store.finishSend("c", batch.id, false);
    expect(batchesOf()[0]?.sending).toBe(false);
    expect(batchesOf()[0]?.questions).toEqual([first, second]);
    store.finishSend("c", batch.id, false); // not sending: a no-op, never an exclusion
    store.reconcile("c", []);
    expect(batchesOf()).toEqual([]);
  });

  test("a reconcile that changes nothing does not notify", () => {
    const store = createAskDockStore();
    const listener = vi.fn();
    store.subscribe(listener);
    store.reconcile("c", []);
    expect(listener).not.toHaveBeenCalled();
    store.reconcile("c", [first]);
    expect(listener).toHaveBeenCalledTimes(1);
    store.reconcile("c", [first]);
    expect(listener).toHaveBeenCalledTimes(1);
  });
});

// --- store shape ------------------------------------------------------------

describe("store shape", () => {
  test("setState back to getInitialState empties every ref and restarts the batch ids", () => {
    const store = createAskDockStore();
    store.reconcile("c", [first]);
    store.reconcile("d", [second]);
    expect(store.getState().byRef.get("d")?.batches[0]?.id).toBe("ask-batch-2");
    store.setState(store.getInitialState());
    expect(store.getState().byRef.size).toBe(0);
    store.reconcile("c", [first]);
    expect(store.getState().byRef.get("c")?.batches[0]?.id).toBe("ask-batch-1");
  });

  test("nextUnansweredKey walks forward from the answered tab and wraps", () => {
    const store = createAskDockStore();
    store.reconcile("c", [first, second]);
    const batch = store.getState().byRef.get("c")?.batches[0];
    if (!batch) throw new Error("Missing batch");
    expect(nextUnansweredKey(batch, {}, 0)).toBe(second.key);
    expect(nextUnansweredKey(batch, { [first.key]: { resolution: { kind: "skip" }, note: "" } }, 1)).toBeUndefined();
  });
});
