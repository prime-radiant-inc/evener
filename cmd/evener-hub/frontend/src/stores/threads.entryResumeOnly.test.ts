import "fake-indexeddb/auto";
import type { AnyNotification, ThreadModel, ThreadReadResponse } from "@evener/appwire-client";
import { currentRead } from "@evener/appwire-client/testing/currentTranscript";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act } from "@testing-library/react";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { resetPendingTurnsStoreForTests } from "../panes/session/composer/queue/pendingTurnsStore";
import { connectionStore } from "./connection";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import {
  isResumeOnly,
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
  "accepted end-read recovery parks Send before resume, changed identity=%s",
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
        threadId: `thr_${destination}`,
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
    fake.on("thread/read", (params) => ({
      ...live(params.ref ?? destination),
      requestGeneration: params.requestGeneration,
    }));
    resume.resolve(live(destination));
    await vi.waitFor(async () => {
      expect(fake.calls.filter(({ method }) => method === "turn/start")).toHaveLength(1);
      expect(await storage.listOutbox(ref)).toEqual([]);
      expect(await storage.listOutbox(destination)).toEqual([]);
    });
    expect(fake.calls.find(({ method }) => method === "turn/start")?.params).toMatchObject({
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
