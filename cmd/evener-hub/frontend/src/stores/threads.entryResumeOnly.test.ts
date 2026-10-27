import "fake-indexeddb/auto";
import type { AnyNotification, ThreadModel, ThreadReadResponse, TurnStartParams } from "@evener/appwire-client";
import { currentRead } from "@evener/appwire-client/testing/currentTranscript";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act } from "@testing-library/react";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { resetPendingTurnsStoreForTests } from "../panes/session/composer/queue/pendingTurnsStore";
import { connectionStore } from "./connection";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { isResumeOnly } from "./recoveryFence";
import {
  notifyReadyForMutationDispatch,
  productionEntryTranscriptForTests,
  resetThreadsStoreForTests,
  setMutationStorageForTests,
  settleEntryTranscriptForTests,
  threadsStore,
} from "./threads";

const cleanupStops: Array<() => Promise<void>> = [];
beforeEach(() => {
  connectionStore.setState({ client: null, state: "idle", serverInfo: undefined });
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
  vi.stubGlobal("indexedDB", new IDBFactory());
  vi.stubGlobal("IDBKeyRange", IDBKeyRange);
});
afterEach(async () => {
  for (const finish of cleanupStops.splice(0)) await finish();
  resetThreadsStoreForTests();
  connectionStore.setState({ client: null, state: "idle" });
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  fake.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(fake);
  return fake;
}
function starts(fake: FakeClient): TurnStartParams[] {
  return fake.calls.filter(({ method }) => method === "turn/start").map(({ params }) => params as TurnStartParams);
}
function resumeOnlySnapshot(ref: string, foldable = false): ThreadReadResponse {
  const response = currentRead(ref);
  response.thread.status = { type: "notLoaded" };
  response.thread.turns = [];
  response.thread.evener.capabilities = { ...response.thread.evener.capabilities, send: false };
  response.thread.evener.mutationStateAuthoritative = false;
  response.thread.evener.resumeRequired = true;
  response.thread.evener.resumeOnlyFoldable = foldable;
  response.thread.evener.queue = { revision: 0 };
  response.overlay = [];
  response.snapshot = { incarnation: "inc-1", length: 0 };
  delete response.olderCursor;
  return response;
}
async function flushUntil(done: () => boolean, maxTurns = 20): Promise<void> {
  for (let i = 0; i < maxTurns && !done(); i += 1) await Promise.resolve();
}
function model(ref: string): ThreadModel {
  const current = threadsStore.getState().threads.get(ref);
  expect(current).toBeDefined();
  return current!;
}
function status(ref: string, type: "active" | "notLoaded"): AnyNotification {
  return { method: "thread/status/changed", params: { threadId: `thr_${ref}`, ref, status: { type } } };
}
function publishAgain(fake: FakeClient, ref: string): void {
  fake.emitNotification({
    method: "evener/jobs/treeUpdated",
    params: { threadId: `thr_${ref}`, ref, revision: 7 },
  });
}

function endRound(fake: FakeClient, ref: string): void {
  act(() => {
    fake.emitNotification({
      method: "overlay/upserted",
      params: {
        ref,
        threadId: `thr_${ref}`,
        item: {
          key: "ending",
          kind: "stream",
          streamId: "ending",
          roundId: "ending",
          turnId: "turn_1",
          item: { id: "ending", type: "agentMessage", text: "ending" },
        },
      },
    });
    fake.emitNotification({ method: "overlay/end", params: { ref, threadId: `thr_${ref}`, roundId: "ending" } });
  });
}

test("an ordinary off-shutdown transition changes the consuming resume predicate and survives publication", async () => {
  const ref = "local:resume-transition";
  const fake = connectFakeClient();
  fake.on("thread/read", (params) => ({
    ...resumeOnlySnapshot(ref, true),
    requestGeneration: params.requestGeneration,
  }));
  await threadsStore.getState().ensureThread(ref);
  expect(isResumeOnly(model(ref))).toBe(true);
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
  act(() => fake.emitNotification(status(ref, "active")));
  expect(model(ref).status.type).toBe("active");
  expect(isResumeOnly(model(ref))).toBe(false);
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(false);
  act(() => publishAgain(fake, ref));
  expect(isResumeOnly(model(ref))).toBe(false);
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(false);
  expect(productionEntryTranscriptForTests(ref).model?.resumeOnlyFoldable).toBe(false);
});

// The original Stop-owned case keeps every original observation and its status
// boundary. Added consuming-path checks do not wait or move the original bit
// assertion. Cleanup also settles its real Stop when a before-fix assertion fails.
test("an off-shut-down status fold leaves a stop-owned fence armed", async () => {
  const ref = "local:stop-owned-off-shutdown";
  const fake = connectFakeClient();
  fake.on("thread/read", (params) => ({
    ...resumeOnlySnapshot(ref, true),
    requestGeneration: params.requestGeneration,
  }));
  await threadsStore.getState().ensureThread(ref);
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
  expect(isResumeOnly(model(ref))).toBe(true);

  const stop = deferred<void>();
  fake.on("evener/thread/forceStop", () => stop.promise);
  const pending = threadsStore.getState().forceStop(ref);
  cleanupStops.push(async () => {
    stop.resolve();
    await pending;
  });
  await flushUntil(() => threadsStore.getState().stoppingRefs.has(ref));
  expect(threadsStore.getState().stoppingRefs.has(ref)).toBe(true);

  act(() => {
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: `thr_${ref}`, ref, status: { type: "active" } },
    });
  });

  // The stop group still owns the obligation: the fold did not drop its fence.
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
  expect(threadsStore.getState().threads.get(ref)?.resumeOnlyFoldable).toBe(false);
  expect(isResumeOnly(model(ref))).toBe(false);
  expect(threadsStore.getState().stoppingRefs.has(ref)).toBe(true);
  expect(isResumeOnly(model(ref), { stopInFlight: true })).toBe(false);
  act(() => publishAgain(fake, ref));
  expect(isResumeOnly(model(ref))).toBe(false);
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
  expect(threadsStore.getState().stoppingRefs.has(ref)).toBe(true);

  stop.resolve();
  await pending;
  expect(threadsStore.getState().stoppingRefs.has(ref)).toBe(false);
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
});

test("ordinary status publication preserves a never-resume-only recovery fence", async () => {
  const ref = "local:never-resume-only";
  const fake = connectFakeClient();
  fake.on("thread/read", (params) => ({ ...resumeOnlySnapshot(ref), requestGeneration: params.requestGeneration }));
  await threadsStore.getState().ensureThread(ref);
  expect(isResumeOnly(model(ref))).toBe(false);
  act(() => fake.emitNotification(status(ref, "active")));
  expect(isResumeOnly(model(ref))).toBe(false);
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
});

test("an authoritative live foldable snapshot remains foldable without an off-shutdown transition", async () => {
  const ref = "local:live-resume-only";
  const fake = connectFakeClient();
  fake.on("thread/read", (params) => {
    const response = resumeOnlySnapshot(ref, true);
    response.thread.status = { type: "active" };
    return { ...response, requestGeneration: params.requestGeneration };
  });
  await threadsStore.getState().ensureThread(ref);
  expect(isResumeOnly(model(ref))).toBe(true);
  act(() => publishAgain(fake, ref));
  expect(isResumeOnly(model(ref))).toBe(true);
  act(() => fake.emitNotification(status(ref, "notLoaded")));
  expect(isResumeOnly(model(ref))).toBe(true);
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
});

test.each([false, true])(
  "explicit resume fences a reconciled reconnect foldable read, changed identity=%s",
  async (changed) => {
    const ref = "local:end-read-recovery";
    const destination = changed ? "local:end-read-resumed" : ref;
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectFakeClient();
    const live = (target: string) => {
      const response = currentRead(target);
      response.thread.evener.instanceId = `instance-${target}`;
      response.thread.evener.mutationStateAuthoritative = true;
      response.overlay = [];
      return response;
    };
    fake.on("thread/read", (params) => ({ ...live(ref), requestGeneration: params.requestGeneration }));
    await threadsStore.getState().ensureThread(ref);
    await settleEntryTranscriptForTests(ref);
    const repository = productionEntryTranscriptForTests(ref).repository;
    expect(repository).toBeDefined();
    expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(false);

    const entered = deferred<void>();
    const endRead = deferred<ThreadReadResponse>();
    fake.on("thread/read", async (params) => {
      entered.resolve();
      return { ...(await endRead.promise), requestGeneration: params.requestGeneration };
    });
    endRound(fake, ref);
    await entered.promise;
    const recovery = live(ref);
    recovery.thread.status = { type: "notLoaded" };
    recovery.thread.evener.resumeRequired = true;
    recovery.thread.evener.mutationStateAuthoritative = false;
    recovery.thread.evener.capabilities.send = false;
    endRead.resolve(recovery);
    await settleEntryTranscriptForTests(ref);
    expect(productionEntryTranscriptForTests(ref).repository).toBe(repository);
    expect(model(ref).resumeRequired).toBe(true);
    expect(model(ref).capabilities?.send).toBe(false);
    expect(isResumeOnly(model(ref))).toBe(false);
    // Acceptance has happened through the repository's end-read, not ensure/refresh.
    expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
    expect(threadsStore.getState().mutationAuthorityRefs.has(ref)).toBe(false);
    const obligation = threadsStore.getState().restartBlockingObligations.get(ref);
    act(() => publishAgain(fake, ref));
    expect(threadsStore.getState().restartBlockingObligations.get(ref)).toBe(obligation);
    expect(fake.calls.filter(({ method }) => method === "thread/resume" || method === "turn/start")).toEqual([]);

    const resume = deferred<ThreadReadResponse>();
    fake.on("thread/resume", () => resume.promise);
    fake.on("turn/start", (params) => ({
      receipt: {
        clientMutationId: params.clientMutationId,
        threadId: `thr_${params.ref}`,
        disposition: "applied",
        projectionState: "notProjected",
      },
      turn: { id: "sent", status: "inProgress", itemsView: "full" },
    }));
    await threadsStore.getState().send(ref, "durable recovery input");
    expect(fake.calls.filter(({ method }) => method === "thread/resume")).toHaveLength(1);
    expect(fake.calls.filter(({ method }) => method === "turn/start")).toEqual([]);
    const parked = await storage.listOutbox(ref);
    expect(parked).toHaveLength(1);
    expect(parked[0]).toMatchObject({
      method: "turn/start",
      payload: { expectedInstanceId: `instance-${ref}`, input: [{ type: "text", text: "durable recovery input" }] },
    });

    // FakeClient does not reconnect in resumeThread. Drive that external
    // boundary explicitly while its response is held, as AppwireClient does.
    const overlapRead = deferred<void>();
    const control = "local:ordinary-folded-send";
    const original = parked[0];
    if (!original) throw new Error("Missing durable send");
    // A sibling tab's committed Send is discovered through the same real
    // outbox on ready, with no explicit resume owner for this separate ref.
    const ordinary = await storage.enqueueIntent({
      method: "turn/start",
      attachments: [],
      optimisticDisplay: original.optimisticDisplay,
      targetRef: control,
      threadId: `thr_${control}`,
      instanceId: `instance-${control}`,
      payload: {
        input: [{ type: "text", text: "ordinary folded input" }],
        ref: control,
        expectedInstanceId: `instance-${control}`,
      },
    });
    fake.on("thread/read", (params) => {
      const target = params.ref ?? ref;
      const response = resumeOnlySnapshot(target, true);
      response.thread.evener.instanceId = `instance-${target}`;
      if (target === ref) overlapRead.resolve();
      return { ...response, requestGeneration: params.requestGeneration };
    });
    act(() => {
      fake.emitStateChange("reconnecting");
      fake.emitReady();
    });
    await overlapRead.promise;
    await settleEntryTranscriptForTests(ref);
    expect(productionEntryTranscriptForTests(ref).repository).toBe(repository);
    expect(model(ref).resumeOnlyFoldable).toBe(true);
    expect(model(ref).resumeRequired).toBe(true);
    expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);

    // A separate persisted foldable Send proves the ready generation really
    // opened, without globally disabling folded Send to hide the race.
    await vi.waitFor(async () => {
      expect(starts(fake).filter((params) => params.ref === control)).toHaveLength(1);
      expect(await storage.listOutbox(control)).toEqual([]);
    });
    expect(fake.calls.filter(({ method }) => method === "thread/resume")).toHaveLength(1);
    expect(starts(fake).find((params) => params.ref === control)).toMatchObject({
      clientMutationId: ordinary.clientMutationId,
      expectedInstanceId: `instance-${control}`,
    });

    expect(starts(fake).filter((params) => params.ref === ref)).toEqual([]);

    // Explicitly offer this ref to the real dispatcher and await its storage
    // tail. This passes through IndexedDB, not a timer-only absence check or
    // injected store state. Removing the owner fence sends the parked row.
    const dispatched = deferred<void>();
    const listOptimistic = storage.listOptimistic.bind(storage);
    const observeTail = vi.spyOn(storage, "listOptimistic").mockImplementation(async (target) => {
      const rows = await listOptimistic(target);
      if (target === ref) dispatched.resolve();
      return rows;
    });
    notifyReadyForMutationDispatch([ref]);
    await dispatched.promise;
    observeTail.mockRestore();
    expect(starts(fake).filter((params) => params.ref === ref)).toEqual([]);
    expect(await storage.listOutbox(ref)).toMatchObject([
      { clientMutationId: parked[0]?.clientMutationId, attempted: false },
    ]);
    fake.on("thread/read", (params) => ({
      ...live(params.ref ?? destination),
      requestGeneration: params.requestGeneration,
    }));
    resume.resolve(live(destination));
    await vi.waitFor(async () => {
      expect(starts(fake).filter((params) => params.ref !== control)).toHaveLength(1);
      expect(await storage.listOutbox(ref)).toEqual([]);
      expect(await storage.listOutbox(destination)).toEqual([]);
    });
    expect(starts(fake).find((params) => params.ref !== control)).toMatchObject({
      ref: destination,
      expectedInstanceId: `instance-${destination}`,
      clientMutationId: parked[0]?.clientMutationId,
      input: [{ type: "text", text: "durable recovery input" }],
    });
  },
);

test("an accepted live end-read does not clear the Stop-owned obligation", async () => {
  const ref = "local:end-read-stop";
  const fake = connectFakeClient();
  fake.on("thread/read", (params) => ({ ...resumeOnlySnapshot(ref), requestGeneration: params.requestGeneration }));
  await threadsStore.getState().ensureThread(ref);
  const stop = deferred<void>();
  fake.on("evener/thread/forceStop", () => stop.promise);
  const pending = threadsStore.getState().forceStop(ref);
  cleanupStops.push(async () => {
    stop.resolve();
    await pending;
  });
  await flushUntil(() => threadsStore.getState().stoppingRefs.has(ref));
  expect(threadsStore.getState().stoppingRefs.has(ref)).toBe(true);
  const obligation = threadsStore.getState().restartBlockingObligations.get(ref);
  expect(obligation).toBeDefined();
  const entered = deferred<void>();
  fake.on("thread/read", (params) => {
    entered.resolve();
    const response = currentRead(ref);
    response.overlay = [];
    delete response.olderCursor;
    response.thread.evener.mutationStateAuthoritative = true;
    return { ...response, requestGeneration: params.requestGeneration };
  });
  endRound(fake, ref);
  await entered.promise;
  await settleEntryTranscriptForTests(ref);
  expect(model(ref).resumeRequired).toBe(false);
  expect(threadsStore.getState().mutationAuthorityRefs.has(ref)).toBe(true);
  expect(threadsStore.getState().restartBlockingObligations.get(ref)).toBe(obligation);
  expect(threadsStore.getState().stoppingRefs.has(ref)).toBe(true);
  expect(fake.calls.filter(({ method }) => method === "thread/resume" || method === "turn/start")).toEqual([]);
});

test.each(["source", "destination"])(
  "Stop on the %s cancels a retargeted Send before resume hydration ends",
  async (stopped) => {
    const ref = "local:resume-stop-source";
    const destination = "local:resume-stop-destination";
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => ({
      ...resumeOnlySnapshot(params.ref ?? ref),
      requestGeneration: params.requestGeneration,
    }));
    await threadsStore.getState().ensureThread(ref);
    await threadsStore.getState().ensureThread(destination);
    const resume = deferred<ThreadReadResponse>();
    fake.on("thread/resume", () => resume.promise);
    await threadsStore.getState().send(ref, "cancel this resumed send");
    const completion = threadsStore.getState().resumeSession(ref);
    const entered = deferred<void>();
    const hydration = deferred<ThreadReadResponse>();
    fake.on("thread/read", async (params) => {
      entered.resolve();
      return { ...(await hydration.promise), requestGeneration: params.requestGeneration };
    });
    resume.resolve(currentRead(destination));
    await entered.promise;
    const moved = await storage.listOutbox(destination);
    expect(moved).toHaveLength(1);
    expect(moved[0]?.attempted).toBe(false);
    fake.on("evener/thread/forceStop", () => ({}));
    await threadsStore.getState().forceStop(stopped === "source" ? ref : destination);
    hydration.resolve(currentRead(destination));
    await completion;
    await settleEntryTranscriptForTests(destination);
    expect(threadsStore.getState().resumeFailures.get(ref)).toMatchObject({ stopCanceled: true });
    expect(threadsStore.getState().resumedIdentities.has(ref)).toBe(false);
    expect(fake.calls.filter(({ method }) => method === "turn/start")).toEqual([]);
    expect((await storage.listOutbox(ref)).every((row) => row.state === "canceled")).toBe(true);
    expect((await storage.listOutbox(destination)).every((row) => row.state === "canceled")).toBe(true);
  },
);

test("a failed explicit resume preserves the parked mutation and a later drive releases it", async () => {
  const ref = "local:failed-resume";
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectFakeClient();
  fake.on("thread/read", (params) => ({ ...resumeOnlySnapshot(ref), requestGeneration: params.requestGeneration }));
  await threadsStore.getState().ensureThread(ref);
  const resume = deferred<ThreadReadResponse>();
  fake.on("thread/resume", () => resume.promise);
  await threadsStore.getState().send(ref, "preserved after resume failure");
  const parked = await storage.listOutbox(ref);
  const completion = threadsStore.getState().resumeSession(ref);
  resume.reject(new Error("transport unavailable"));
  await completion;
  expect(threadsStore.getState().resumeFailures.get(ref)).toMatchObject({ stopCanceled: false });
  expect(fake.calls.filter(({ method }) => method === "turn/start")).toEqual([]);
  expect(await storage.listOutbox(ref)).toMatchObject([
    { clientMutationId: parked[0]?.clientMutationId, attempted: false },
  ]);
  fake.on("thread/resume", () => currentRead(ref));
  fake.on("thread/read", (params) => ({ ...currentRead(ref), requestGeneration: params.requestGeneration }));
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      threadId: `thr_${ref}`,
      disposition: "applied",
      projectionState: "notProjected",
    },
    turn: { id: "sent", status: "inProgress", itemsView: "full" },
  }));
  await threadsStore.getState().resumeSession(ref);
  await vi.waitFor(async () => {
    expect(fake.calls.filter(({ method }) => method === "turn/start")).toHaveLength(1);
    expect(await storage.listOutbox(ref)).toEqual([]);
  });
  expect(starts(fake)[0]?.clientMutationId).toBe(parked[0]?.clientMutationId);
  expect(threadsStore.getState().resumeFailures.has(ref)).toBe(false);
});

test("resume completion drains after a held lifecycle discovery result", async () => {
  const ref = "local:retiring-resume-dispatch";
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectFakeClient();
  fake.on("thread/read", (params) => ({ ...resumeOnlySnapshot(ref), requestGeneration: params.requestGeneration }));
  await threadsStore.getState().ensureThread(ref);
  const failedResume = deferred<ThreadReadResponse>();
  fake.on("thread/resume", () => failedResume.promise);
  await threadsStore.getState().send(ref, "deliver after the owner releases");
  const failedCompletion = threadsStore.getState().resumeSession(ref);
  failedResume.reject(new Error("retry this transport failure"));
  await failedCompletion;
  expect(threadsStore.getState().resumeFailures.has(ref)).toBe(true);
  const parked = await storage.listOutbox(ref);
  expect(parked).toHaveLength(1);

  // Hold a real lifecycle scan's storage result. Its rows come from IndexedDB,
  // and only delivery of that result is controlled, not discovery or dispatch.
  const scanEntered = deferred<void>();
  const releaseScan = deferred<void>();
  const listTargetRefs = storage.listTargetRefs.bind(storage);
  const scan = vi.spyOn(storage, "listTargetRefs").mockImplementationOnce(async () => {
    const refs = await listTargetRefs();
    expect(refs).toContain(ref);
    scanEntered.resolve();
    await releaseScan.promise;
    return refs;
  });
  window.dispatchEvent(new Event("focus"));
  await scanEntered.promise;

  const live = currentRead(ref);
  live.thread.evener.mutationStateAuthoritative = true;
  fake.on("thread/resume", () => live);
  fake.on("thread/read", (params) => ({ ...live, requestGeneration: params.requestGeneration }));
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      threadId: live.thread.id,
      disposition: "applied",
      projectionState: "notProjected",
    },
    turn: { id: "sent", status: "inProgress", itemsView: "full" },
  }));
  let releasedAtCompletion = 0;
  const unsubscribe = threadsStore.subscribe((state, previous) => {
    if (previous.resumeFailures.has(ref) && !state.resumeFailures.has(ref)) {
      // The real successful drive publishes this immediately before returning.
      // Release the external result at completion so discovery can contend
      // with owner release, without injecting the dispatcher's private state.
      releasedAtCompletion += 1;
      expect(starts(fake)).toEqual([]);
      releaseScan.resolve();
    }
  });
  try {
    await threadsStore.getState().resumeSession(ref);
    expect(releasedAtCompletion).toBe(1);
    expect(scan).toHaveBeenCalledTimes(1);
    // Bounded below the outbox's 2000ms periodic scan: no second Send, ready,
    // focus, or manual dispatcher notification is allowed to rescue the row.
    await vi.waitFor(
      async () => {
        expect(starts(fake)).toHaveLength(1);
        expect(await storage.listOutbox(ref)).toEqual([]);
      },
      { timeout: 500 },
    );
    expect(starts(fake)[0]?.clientMutationId).toBe(parked[0]?.clientMutationId);
  } finally {
    unsubscribe();
    releaseScan.resolve();
  }
});

test("a Send committed during destination hydration follows the same resume owner", async () => {
  const ref = "local:late-resume-source";
  const destination = "local:late-resume-destination";
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectFakeClient();
  fake.on("thread/read", (params) => ({ ...resumeOnlySnapshot(ref), requestGeneration: params.requestGeneration }));
  await threadsStore.getState().ensureThread(ref);
  const resume = deferred<ThreadReadResponse>();
  fake.on("thread/resume", () => resume.promise);
  await threadsStore.getState().send(ref, "first send");
  const first = (await storage.listOutbox(ref))[0];
  const completion = threadsStore.getState().resumeSession(ref);
  const entered = deferred<void>();
  const hydration = deferred<ThreadReadResponse>();
  fake.on("thread/read", async (params) => {
    entered.resolve();
    return { ...(await hydration.promise), requestGeneration: params.requestGeneration };
  });
  const live = currentRead(destination);
  live.thread.evener.instanceId = "late-resumed-instance";
  resume.resolve(live);
  await entered.promise;
  await threadsStore.getState().send(ref, "late send");
  const late = (await storage.listOutbox(ref))[0];
  expect(first?.clientMutationId).toBeDefined();
  expect(late?.clientMutationId).toBeDefined();
  expect(late?.clientMutationId).not.toBe(first?.clientMutationId);
  expect(fake.calls.filter(({ method }) => method === "thread/resume")).toHaveLength(1);
  expect(fake.calls.filter(({ method }) => method === "turn/start")).toEqual([]);
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      threadId: live.thread.id,
      disposition: "applied",
      projectionState: "notProjected",
    },
    turn: { id: "sent", status: "inProgress", itemsView: "full" },
  }));
  hydration.resolve(live);
  await completion;
  await vi.waitFor(async () => {
    expect(fake.calls.filter(({ method }) => method === "turn/start")).toHaveLength(2);
    expect(await storage.listOutbox(ref)).toEqual([]);
    expect(await storage.listOutbox(destination)).toEqual([]);
  });
  expect(fake.calls.filter(({ method }) => method === "turn/start").map(({ params }) => params)).toMatchObject([
    { ref: destination, expectedInstanceId: "late-resumed-instance", clientMutationId: first?.clientMutationId },
    { ref: destination, expectedInstanceId: "late-resumed-instance", clientMutationId: late?.clientMutationId },
  ]);
});
