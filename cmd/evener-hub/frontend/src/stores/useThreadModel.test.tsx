import "fake-indexeddb/auto";
import type { ThreadReadResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, render } from "@testing-library/react";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { connectReadyFakeClient } from "../testing/connectionTestUtils";
import { type TestThreadOverrides, testThread } from "../threadTestUtils";
import { connectionStore, resetConnectionStoreForTests } from "./connection";
import { setProductionEntryCacheForTests } from "./entryTranscriptRoutes";
import { SessionEntryCache } from "./sessionEntryCache";
import { foregroundHolds } from "./threadOwners";
import { resetThreadsStoreForTests, threadsStore } from "./threads";
import { useThreadClaim } from "./useThreadModel";

function readResponse(ref: string, overrides: TestThreadOverrides = {}, requestGeneration = 1): ThreadReadResponse {
  return {
    thread: testThread(ref, overrides),
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc-1", length: 0 },
    requestGeneration,
  };
}

function ClaimingSession({ ref }: { ref: string }) {
  useThreadClaim(ref);
  return null;
}

async function flushUntil(done: () => boolean, maxTurns = 50): Promise<void> {
  for (let i = 0; i < maxTurns && !done(); i += 1) {
    await act(async () => {
      await Promise.resolve();
    });
  }
}

let cache: SessionEntryCache;
beforeEach(() => {
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  cache = new SessionEntryCache({
    indexedDB: new IDBFactory(),
    keyRange: IDBKeyRange,
    databaseName: `thread-claim-${crypto.randomUUID()}`,
  });
  setProductionEntryCacheForTests(cache);
});
afterEach(() => {
  resetThreadsStoreForTests();
  cache.close();
  vi.restoreAllMocks();
});

test("an unmount after a rejected claim never releases another reader's hold", async () => {
  const ref = "ref_claim_guard";
  // The doomed reader mounts while the connection reports ready with no
  // wired client behind it - the latent window: tryStart passes the ready
  // check and calls ensureThread, whose first line throws for the missing
  // client, so the hook's ensure promise rejects and nothing is acquired.
  connectionStore.setState({ state: "ready" });
  const doomed = render(<ClaimingSession ref={ref} />);
  await act(async () => {
    await Promise.resolve();
  });
  expect(foregroundHolds(ref)).toBe(0);

  // A second reader acquires the ref on a real client.
  const b = new FakeClient("ready");
  b.on("thread/read", (params: { requestGeneration?: number }) => readResponse(ref, {}, params.requestGeneration ?? 1));
  b.on("thread/unsubscribe", () => ({}));
  await act(async () => {
    await b.connect();
    connectionStore.getState().connect(b);
  });
  const survivor = render(<ClaimingSession ref={ref} />);
  await vi.waitFor(() => expect(threadsStore.getState().threads.has(ref)).toBe(true));
  expect(foregroundHolds(ref)).toBe(1);

  // Unmounting the doomed reader must not touch the survivor's claim: its
  // ensure rejected, so it never acquired anything to release.
  act(() => {
    doomed.unmount();
  });
  expect(foregroundHolds(ref)).toBe(1);
  expect(threadsStore.getState().threads.has(ref)).toBe(true);

  // The survivor still owns its claim and releases it on its own unmount.
  act(() => {
    survivor.unmount();
  });
  expect(foregroundHolds(ref)).toBe(0);
});

test("a reader mounted during reconnect and unmounted before readiness releases nothing", async () => {
  const ref = "ref_claim_reconnect";
  const owner = connectReadyFakeClient((fake) => {
    fake.on("thread/read", (params: { requestGeneration?: number }) =>
      readResponse(ref, {}, params.requestGeneration ?? 1),
    );
    fake.on("thread/unsubscribe", () => ({}));
  });
  const holder = render(<ClaimingSession ref={ref} />);
  await vi.waitFor(() => expect(threadsStore.getState().threads.has(ref)).toBe(true));
  expect(foregroundHolds(ref)).toBe(1);

  // The connection drops: a second reader mounts into the reconnect window
  // and unmounts before readiness ever returns. It never called ensureThread,
  // so its cleanup must not touch the owner's hold.
  act(() => {
    owner.emitStateChange("closed");
  });
  const transient = render(<ClaimingSession ref={ref} />);
  expect(connectionStore.getState().state).not.toBe("ready");
  act(() => {
    transient.unmount();
  });
  expect(foregroundHolds(ref)).toBe(1);
  expect(threadsStore.getState().threads.has(ref)).toBe(true);

  act(() => {
    holder.unmount();
  });
  expect(foregroundHolds(ref)).toBe(0);
});

test("a claim that acquires still releases on unmount", async () => {
  const ref = "ref_claim_release";
  connectReadyFakeClient((fake) => {
    fake.on("thread/read", (params: { requestGeneration?: number }) =>
      readResponse(ref, {}, params.requestGeneration ?? 1),
    );
    fake.on("thread/unsubscribe", () => ({}));
  });
  const holder = render(<ClaimingSession ref={ref} />);
  await flushUntil(() => threadsStore.getState().threads.has(ref));
  expect(foregroundHolds(ref)).toBe(1);

  act(() => {
    holder.unmount();
  });
  expect(foregroundHolds(ref)).toBe(0);
});
