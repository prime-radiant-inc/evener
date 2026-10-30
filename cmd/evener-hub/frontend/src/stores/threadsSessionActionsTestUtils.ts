import { appliedReceipt } from "../mutationReceiptTestUtils";
import { ALL_CAPABILITIES as CAPABILITIES, type TestThreadOverrides, testThread } from "../threadTestUtils";
import "fake-indexeddb/auto";
import type { MethodName, MethodTypes, Thread, ThreadClearResponse, ThreadReadResponse } from "@evener/appwire-client";
import { FakeClient, type RequestHandler } from "@evener/appwire-client/testing/fakeClient";
import { nextMacrotask } from "@evener/appwire-client/testing/macrotask";
import { connectionStore } from "./connection";
import { MutationOutboxIndexedDB, MutationStorageTimeoutError } from "./mutationOutboxIndexedDB";
import { threadsStore } from "./threads";

export function readResponse(ref: string, overrides: TestThreadOverrides = {}): ThreadReadResponse {
  const client = connectionStore.getState().client;
  const request = (
    client instanceof FakeClient
      ? client.calls
          .filter(
            (call) =>
              call.method === "thread/read" && (call.params as MethodTypes["thread/read"]["params"]).ref === ref,
          )
          .at(-1)?.params
      : undefined
  ) as MethodTypes["thread/read"]["params"] | undefined;
  return {
    thread: testThread(ref, overrides),
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc-1", length: 0 },
    requestGeneration: request?.requestGeneration,
  };
}

// flushMicrotasks drains a bounded number of microtask turns without waiting on
// any condition - for a caller that only wants pending continuations to run
// before its next assertion. (A predicate that is never true would cost at most
// the same 20 hops; this makes that intent explicit rather than reading as a
// wait on the impossible.)
export async function flushMicrotasks(turns = 20): Promise<void> {
  for (let i = 0; i < turns; i += 1) await Promise.resolve();
}

export async function flushUntil(done: () => boolean, maxTurns = 20): Promise<void> {
  for (let i = 0; i < maxTurns && !done(); i += 1) await nextMacrotask();
}

export function nextHandledRequest<M extends MethodName>(
  fake: FakeClient,
  method: M,
  handler: RequestHandler<M>,
): Promise<MethodTypes[M]["params"]> {
  return new Promise((resolve) => {
    fake.on(method, (params) => {
      resolve(params);
      return handler(params);
    });
  });
}

export function clearResponse(params: { clientMutationId: string }, thread: Thread): ThreadClearResponse {
  return {
    thread,
    ref: thread.evener.ref,
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: thread.id,
      instanceId: thread.evener.instanceId,
      projectionState: "reflected",
    },
  };
}

export function connectMutationClient(): FakeClient {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (params) => {
    if (!params.ref) throw new Error("thread/read test request requires ref");
    return readResponse(params.ref);
  });
  connectionStore.getState().connect(fake);
  return fake;
}

export function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

// onTurnStart answers turn/start with the standard applied mutation: a fresh
// in-progress turn_1 and the applied receipt — the handler 33 of this file's
// tests copy-pasted inline.
export function onTurnStart(fake: FakeClient): void {
  fake.on("turn/start", (params) => ({
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
    receipt: mutationReceipt(params.clientMutationId),
  }));
}

export function mutationReceipt(clientMutationId: string, disposition = "applied") {
  return appliedReceipt(clientMutationId, "thr_ref_a", { disposition });
}

export async function ensureActiveMutationTarget(fake: FakeClient, ref: string): Promise<void> {
  fake.on("thread/read", (params) =>
    readResponse(params.ref ?? ref, {
      turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
      evener: {
        ref: params.ref ?? ref,
        capabilities: CAPABILITIES,
        queue: { revision: 7 },
        activeTurnId: "turn_1",
      },
    }),
  );
  await threadsStore.getState().ensureThread(ref);
}

export async function flushIndexedDBUntil(done: () => boolean | Promise<boolean>, maxTurns = 30): Promise<void> {
  const probe = new MutationOutboxIndexedDB();
  for (let turn = 0; turn < maxTurns && !(await done()); turn += 1) await probe.listTargetRefs();
  probe.close();
}

// settleCallerContinuations yields to the task queue exactly once, which the
// hydration-retry tests use to park an ensureThread/watchThread caller on its
// lifecycle before firing the retry that lifecycle scheduled.
//
// Why a task yield and not `flushUntil(() => false, N)`: a count is an
// assumption about how many microtask turns separate the failed read from the
// caller's own catch, and nothing fails when that assumption stops holding -
// the caller instead converges through the "adopt a replacement read already in
// flight" arm and the test silently stops covering the owner's wait. A task
// yield holds however many turns that path grows (see nextMacrotask).
//
// Its one boundary: it does not cover a future change that parks the caller
// behind a task or I/O of its own (an IndexedDB read on the rejection path,
// say). That would need its own awaited condition, and the mutation proof in
// this task's report - which fires each owner-wait arm and requires the
// matching test to fail - is what would catch it.
export const settleCallerContinuations = nextMacrotask;

// The merely-resumable snapshot the three tests below drive: ResumeRequired with
// no send capability, and optionally the hub's foldable bit. A ref that is not
// foldable is the one whose fence a status fold must NOT clear - the hub stamped
// ResumeRequired for a cause whose turn/start it still refuses.
export function resumeOnlySnapshot(ref: string, foldable = false): ThreadReadResponse {
  return readResponse(ref, {
    status: { type: "notLoaded" },
    evener: {
      ref,
      capabilities: { ...CAPABILITIES, send: false },
      mutationStateAuthoritative: false,
      resumeRequired: true,
      ...(foldable ? { resumeOnlyFoldable: true } : {}),
      queue: { revision: 0 },
    },
  });
}

// The durable write cannot be made: every enqueue attempt rejects with the
// adapter's watchdog error, the wedge shape the retry ladder retries before
// the send fallback answers it.
export function timeOutDurableEnqueues(storage: MutationOutboxIndexedDB): void {
  storage.enqueueIntent = async () => {
    throw new MutationStorageTimeoutError();
  };
}
