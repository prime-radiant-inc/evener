// @vitest-environment node
import { describe, expect, test, vi } from "vitest";
import {
  type AskAnswerSender,
  type AskDockThreads,
  type AskDockThreadsSnapshot,
  createAskDockStore,
  nextUnansweredKey,
} from "./askDock";
import type { AskQuestionRef } from "./deriveAskQuestions";
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

// liveAskQuestions reads only the turns; the rest of ThreadModel is noise here.
function threadModel(items: ItemModel[]): ThreadModel {
  return { turns: [{ id: "turn_1", status: "completed", items }] } as unknown as ThreadModel;
}

const DEPLOY = [{ header: "Deploy?", question: "Ship now?", options: [{ label: "Yes", detail: "" }] }];
/** A thread source with the shape the port asks for: publish replaces the map
 * and notifies with the new and previous snapshot, like a store would. */
function fakeThreads(): AskDockThreads & { publish(ref: string, model: ThreadModel): void } {
  let snapshot: AskDockThreadsSnapshot = { threads: new Map() };
  const listeners = new Set<(state: AskDockThreadsSnapshot, previous: AskDockThreadsSnapshot) => void>();
  return {
    subscribe(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    publish(ref, model) {
      const previous = snapshot;
      snapshot = { threads: new Map(previous.threads).set(ref, model) };
      for (const listener of listeners) listener(snapshot, previous);
    },
  };
}

function fakeSender(): AskAnswerSender & ReturnType<typeof vi.fn> {
  return vi.fn(async (_ref: string, _text: string) => {});
}

const first: AskQuestionRef = {
  key: "first:0",
  callId: "first",
  header: "First",
  question: "Choose",
  options: [],
  multiSelect: false,
};
const second: AskQuestionRef = { ...first, key: "second:0", callId: "second" };

// --- two wired instances share nothing --------------------------------------

describe("two wired stores share nothing", () => {
  test("a batch, an answer, a send and its exclusion in one store are invisible to the other", async () => {
    const threadsA = fakeThreads();
    const threadsB = fakeThreads();
    const sendA = fakeSender();
    const sendB = fakeSender();
    const a = createAskDockStore();
    const b = createAskDockStore();
    a.wire(threadsA, sendA);
    b.wire(threadsB, sendB);

    const model = threadModel([askItem("i1", "call_1", DEPLOY)]);
    threadsA.publish("ref_a", model);
    const batch = a.getState().byRef.get("ref_a")?.batches[0];
    expect(batch?.questions.map((q) => q.key)).toEqual(["call_1:0"]);
    expect(b.getState().byRef.size).toBe(0);

    a.getState().setAnswer("ref_a", "call_1:0", { kind: "option", labels: ["Yes"] });
    expect(b.getState().byRef.get("ref_a")).toBeUndefined();

    await expect(a.getState().sendBatch("ref_a", batch?.id ?? "")).resolves.toEqual({ outcome: "sent" });
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

describe("wire", () => {
  test("the disposer stops reconciliation and unwires the sender", async () => {
    const threads = fakeThreads();
    const store = createAskDockStore();
    const dispose = store.wire(threads, fakeSender());
    threads.publish("ref_a", threadModel([askItem("i1", "call_1", DEPLOY)]));
    const batch = store.getState().byRef.get("ref_a")?.batches[0];
    expect(batch).toBeDefined();
    dispose();
    threads.publish("ref_a", threadModel([askItem("i1", "call_1", DEPLOY), askItem("i2", "call_2", DEPLOY)]));
    expect(store.getState().byRef.get("ref_a")?.batches).toEqual([batch]);
    await expect(store.getState().sendBatch("ref_a", batch?.id ?? "")).rejects.toThrow(/wire/);
  });

  test("sendBatch before wire fails loudly rather than dropping the answers", async () => {
    const store = createAskDockStore();
    store.reconcile("ref_a", [first]);
    const batch = store.getState().byRef.get("ref_a")?.batches[0];
    await expect(store.getState().sendBatch("ref_a", batch?.id ?? "")).rejects.toThrow(/wire\(threads, send\)/);
    expect(store.getState().byRef.get("ref_a")?.batches[0]?.sending).toBe(false);
  });

  test("a rejected send leaves the batch intact and retryable, with the sentence the dock shows", async () => {
    const threads = fakeThreads();
    const store = createAskDockStore();
    const send = vi.fn(async () => {
      throw new Error("socket closed");
    });
    store.wire(threads, send);
    threads.publish("ref_a", threadModel([askItem("i1", "call_1", DEPLOY)]));
    const batch = store.getState().byRef.get("ref_a")?.batches[0];
    const outcome = await store.getState().sendBatch("ref_a", batch?.id ?? "");
    expect(outcome).toEqual({ outcome: "error", message: "Couldn't send answers: socket closed" });
    const after = store.getState().byRef.get("ref_a")?.batches[0];
    expect(after?.id).toBe(batch?.id);
    expect(after?.sending).toBe(false);
    await expect(store.getState().sendBatch("ref_a", batch?.id ?? "")).resolves.toEqual({
      outcome: "error",
      message: "Couldn't send answers: socket closed",
    });
    expect(send).toHaveBeenCalledTimes(2);
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
