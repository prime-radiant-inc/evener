// @vitest-environment node
import { describe, expect, test, vi } from "vitest";
import { type AskAnswerSender, type AskDockThreadsSnapshot, createAskDockStore, nextUnansweredKey } from "./askDock";
import type { AskQuestionRef } from "./deriveAskQuestions";
import { createFrameworkFreeStore } from "./frameworkFreeStore";
import type { ItemModel, ThreadModel } from "./model";

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
