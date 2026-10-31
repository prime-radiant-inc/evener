import * as appwire from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render } from "@testing-library/react";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { ConversationBody, type ConversationSeat } from "../panes/session/ConversationBody";
import { retainedTranscriptReadView } from "../panes/session/transcript/transcriptReadView";
import { ConversationHandoffBoundary } from "../shell/ConversationHandoffBoundary";
import { ClientProvider } from "../shell/clientContext";
import { conversationPaneLifetime } from "../shell/paneLifetime";
import { type OpenPaneRecord, resetWorkspaceStoreForTests, workspaceStore } from "../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import { connectionStore, resetConnectionStoreForTests } from "./connection";
import {
  cancelConversationHandoff,
  completeConversationHandoffPane,
  conversationHandoffGeneration,
} from "./conversationClaimHandoff";
import { ref, response, snapshot, sourceIds, sourceItems } from "./conversationHandoffTestUtils";
import * as entryRoutes from "./entryTranscriptRoutes";
import { resetNavigationStoreForTests } from "./navigation/store";
import { SessionEntryCache } from "./sessionEntryCache";
import {
  acquireBackgroundThread,
  resetThreadsStoreForTests,
  settleEntryTranscriptForTests,
  threadsStore,
  useThreadsStore,
} from "./threads";
import { resetTranscriptDisplayStoreForTests, transcriptDisplayStore } from "./transcriptDisplay";
import { useThreadClaim } from "./useThreadModel";

let cache: SessionEntryCache;

beforeEach(() => {
  installLocalStorage(new MemoryStorage());
  const indexedDB = new IDBFactory();
  vi.stubGlobal("indexedDB", indexedDB);
  vi.stubGlobal("IDBKeyRange", IDBKeyRange);
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
  resetNavigationStoreForTests();
  resetTranscriptDisplayStoreForTests();
  transcriptDisplayStore.setState({ presentation: { desktop: "comic", mobile: "comic" } });
  cache = new SessionEntryCache({ indexedDB, keyRange: IDBKeyRange, databaseName: `claim-${crypto.randomUUID()}` });
  entryRoutes.setProductionEntryCacheForTests(cache);
});

afterEach(() => {
  cleanup();
  resetWorkspaceStoreForTests();
  resetThreadsStoreForTests();
  resetConnectionStoreForTests();
  cache.close();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

// Observe the actual model/projection while ConversationBody still owns the real
// claim. Its render callback avoids unrelated canvas/virtual-list geometry.
function SourceObservation() {
  const model = useThreadsStore((state) => state.threads.get(ref));
  return <output data-source-ids={JSON.stringify(snapshot(model).sources)} />;
}

async function mountedConversation() {
  const client = new FakeClient("ready");
  client.on("thread/read", (params) => ({ ...response(), requestGeneration: params.requestGeneration }));
  client.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(client);
  const pane: OpenPaneRecord = { id: "claim-pane", type: "session", params: { ref }, slot: "main" };
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  const lifetime = conversationPaneLifetime(pane);
  const readView = retainedTranscriptReadView(lifetime, ref, "session");
  const seat: ConversationSeat = {
    readView,
    composerSource: null,
    keepViewRemount: () => workspaceStore.getState().panes.includes(pane),
    viewFallbackId: pane.id,
    focused: true,
    keyboardScrollPaneId: null,
  };
  const ensure = vi.spyOn(threadsStore.getState(), "ensureThread");
  const release = vi.spyOn(threadsStore.getState(), "releaseThread");
  const tree = (host: string) => (
    <ClientProvider client={client}>
      <ConversationHandoffBoundary
        key={host}
        generation={conversationHandoffGeneration(transcriptDisplayStore.getState().viewport)}
        paneId={pane.id}
      >
        <ConversationBody ref={ref} seat={seat} render={() => <SourceObservation />} />
      </ConversationHandoffBoundary>
    </ClientProvider>
  );
  const view = render(tree("desktop"));
  await act(async () => {
    await ensure.mock.results[0]?.value;
    await settleEntryTranscriptForTests(ref);
  });
  expect(ensure).toHaveBeenCalledExactlyOnceWith(ref);
  expect(snapshot()).toEqual({
    foreground: 1,
    background: 0,
    items: sourceItems.map((item) => item.id),
    sources: sourceIds,
  });
  const background = acquireBackgroundThread(ref);
  await act(async () => {
    await background.ready;
    await settleEntryTranscriptForTests(ref);
  });
  expect(snapshot()).toMatchObject({ foreground: 1, background: 1, sources: sourceIds });
  const route = entryRoutes.productionEntryTranscriptForTests(ref);
  expect(route.reader).toBeDefined();
  expect(route.repository).toBeDefined();
  return { client, pane, lifetime, readView, view, tree, ensure, release, background, route };
}

function observePruning() {
  const pruned: {
    before: ReturnType<typeof snapshot>;
    after: ReturnType<typeof snapshot>;
    result: appwire.ThreadModel;
    stack: string | undefined;
  }[] = [];
  const real = appwire.pruneBackgroundHistoryBodies;
  vi.spyOn(appwire, "pruneBackgroundHistoryBodies").mockImplementation((model) => {
    const before = snapshot(model);
    const result = real(model);
    pruned.push({ before, after: snapshot(result), result, stack: new Error("prune caller").stack });
    return result;
  });
  const releaseReader = vi.spyOn(entryRoutes, "releaseEntryTranscriptReader");
  return { pruned, releaseReader };
}

test("same logical pane keeps item-backed source through ConversationBody host replacement", async () => {
  const fixture = await mountedConversation();
  const { pruned, releaseReader } = observePruning();
  const localRead = vi.spyOn(cache, "getLatestWindow");
  const reads = fixture.client.calls.filter((call) => call.method === "thread/read").length;
  const publications: { state: ReturnType<typeof snapshot>; model: appwire.ThreadModel | undefined }[] = [];
  const unsubscribe = threadsStore.subscribe((state, previous) => {
    if (state.threads.get(ref) !== previous.threads.get(ref))
      publications.push({ state: snapshot(state.threads.get(ref)), model: state.threads.get(ref) });
  });
  try {
    act(() => transcriptDisplayStore.getState().setViewport("mobile"));
    fixture.view.rerender(fixture.tree("mobile"));
    await act(async () => {
      await fixture.ensure.mock.results[2]?.value;
      await settleEntryTranscriptForTests(ref);
    });
    // These assert the actual cleanup/replacement, not a no-op rerender.
    expect(fixture.release.mock.calls).toEqual([[ref], [ref]]);
    expect(fixture.ensure.mock.calls).toEqual([[ref], [ref], [ref]]);
    expect(workspaceStore.getState().panes[0]).toBe(fixture.pane);
    expect(conversationPaneLifetime(fixture.pane)).toBe(fixture.lifetime);
    expect(retainedTranscriptReadView(fixture.lifetime, ref, "session")).toBe(fixture.readView);
    expect(fixture.lifetime.alive).toBe(true);
    expect(snapshot()).toEqual({
      foreground: 1,
      background: 1,
      items: sourceItems.map((item) => item.id),
      sources: sourceIds,
    });
    expect(
      threadsStore
        .getState()
        .threads.get(ref)
        ?.turns.flatMap((turn) => turn.items.map(({ id, text }) => ({ id, text }))),
    ).toEqual(sourceItems);
    expect(fixture.client.calls.filter((call) => call.method === "thread/read")).toHaveLength(reads);
    expect(localRead).not.toHaveBeenCalled();
    // Keep the evidence observational: a repair may avoid pruning entirely.
    const evidence = JSON.stringify({
      releaseReader: releaseReader.mock.calls,
      pruning: pruned.map(({ before, after, result, stack }) => ({
        before,
        after,
        stack,
        published: publications.some(({ model }) => model === result),
      })),
      publications: publications.map(({ state }) => state),
      localReads: localRead.mock.calls.length,
      samePane: workspaceStore.getState().panes[0] === fixture.pane,
      sameReadView: retainedTranscriptReadView(fixture.lifetime, ref, "session") === fixture.readView,
    });
    // RED must be lost source availability during the handoff, not failure to
    // hydrate or a fabricated model. Every observed publication stays readable.
    expect(
      publications.map(({ state }) => state.sources),
      evidence,
    ).toEqual(publications.map(() => sourceIds));
  } finally {
    unsubscribe();
    fixture.view.unmount();
    await fixture.background.release();
  }
});

test("final visible departure releases completed bodies while background ownership remains", async () => {
  const fixture = await mountedConversation();
  const { pruned, releaseReader } = observePruning();
  act(() => {
    workspaceStore.setState({ panes: [], focusedPaneId: null });
    fixture.view.unmount();
  });
  await settleEntryTranscriptForTests(ref);
  try {
    expect(fixture.lifetime.alive).toBe(false);
    expect(fixture.readView.alive).toBe(false);
    expect(fixture.release).toHaveBeenCalledExactlyOnceWith(ref);
    expect(releaseReader).toHaveBeenCalledWith(ref, true);
    expect(pruned.length).toBeGreaterThan(0);
    expect(pruned.map(({ before }) => [before.foreground, before.background])).toEqual(pruned.map(() => [0, 1]));
    expect(snapshot()).toEqual({ foreground: 0, background: 1, items: [], sources: ["turn:itemless-error:error"] });
    expect(entryRoutes.productionEntryTranscriptForTests(ref).reader).toBeUndefined();
    expect(fixture.route.repository?.retainedInventoryForTesting()).toMatchObject({
      pendingRows: [],
      inFlightBodies: [],
      retryBodies: [],
      backgroundAcceptBodies: [],
      handoffOverlayBodies: [],
    });
  } finally {
    await fixture.background.release();
  }
});

test("read-view teardown and old-generation completion cannot retire a reversed overlap", async () => {
  const fixture = await mountedConversation();
  act(() => transcriptDisplayStore.getState().setViewport("mobile"));
  const oldGeneration = conversationHandoffGeneration("mobile");
  fixture.view.rerender(null);
  act(() => fixture.readView.setReadable(false));
  expect(snapshot()).toMatchObject({ foreground: 1, sources: sourceIds });
  act(() => transcriptDisplayStore.getState().setViewport("desktop"));
  expect(conversationHandoffGeneration("desktop")).not.toBe(oldGeneration);
  act(() => {
    cancelConversationHandoff(oldGeneration);
    completeConversationHandoffPane(oldGeneration, fixture.pane.id);
  });
  expect(snapshot()).toMatchObject({ foreground: 1, sources: sourceIds });
  fixture.view.rerender(fixture.tree("returned-desktop"));
  expect(snapshot()).toMatchObject({ foreground: 1, sources: sourceIds });
  fixture.view.unmount();
  expect(snapshot()).toMatchObject({ foreground: 0, items: [] });
  await fixture.background.release();
});

test("never-started readers do not seed temporary ownership", () => {
  const client = new FakeClient("reconnecting");
  connectionStore.getState().connect(client);
  const pane: OpenPaneRecord = { id: "not-started", type: "session", params: { ref }, slot: "main" };
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  const readView = retainedTranscriptReadView(conversationPaneLifetime(pane), ref, "session");
  const seat: ConversationSeat = {
    readView,
    composerSource: null,
    keepViewRemount: () => true,
    viewFallbackId: pane.id,
    focused: true,
    keyboardScrollPaneId: null,
  };
  const ensure = vi.spyOn(threadsStore.getState(), "ensureThread");
  const tree = (host: string) => (
    <ClientProvider client={client}>
      <ConversationBody key={host} ref={ref} seat={seat} render={() => <SourceObservation />} />
    </ClientProvider>
  );
  const view = render(tree("desktop"));
  act(() => transcriptDisplayStore.getState().setViewport("mobile"));
  view.rerender(tree("mobile"));
  expect(ensure).not.toHaveBeenCalled();
  expect(snapshot().foreground).toBe(0);
  view.unmount();
});

function RejectionProbe({ readView }: { readView?: ReturnType<typeof retainedTranscriptReadView> }) {
  useThreadClaim(ref, readView);
  return null;
}

async function rejectedConversation(prepareBeforeRejection = false) {
  const pane: OpenPaneRecord = { id: "rejected-reader", type: "session", params: { ref }, slot: "main" };
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  const readView = retainedTranscriptReadView(conversationPaneLifetime(pane), ref, "session");
  const ensure = vi.spyOn(threadsStore.getState(), "ensureThread");
  // An injected missing client rejects before acquiring a foreground hold.
  connectionStore.setState({ state: "ready" });
  const doomed = render(<RejectionProbe readView={readView} />);
  // Preparation must precede rejection settlement so withdrawing the ordinary
  // registration cannot make the overlap rejection scenario disappear.
  if (prepareBeforeRejection) act(() => transcriptDisplayStore.getState().setViewport("mobile"));
  await act(async () => {
    await expect(ensure.mock.results[0]?.value).rejects.toBeDefined();
  });
  expect(snapshot().foreground).toBe(0);
  return { doomed, ensure };
}

async function survivingReader(readView?: ReturnType<typeof retainedTranscriptReadView>) {
  const client = new FakeClient("ready");
  client.on("thread/read", (params) => ({ ...response(), requestGeneration: params.requestGeneration }));
  client.on("thread/unsubscribe", () => ({}));
  act(() => connectionStore.getState().connect(client));
  const survivor = render(<RejectionProbe readView={readView} />);
  await act(async () => {
    // Join the real in-flight hydration, then release only this joiner's claim.
    await threadsStore.getState().ensureThread(ref);
    threadsStore.getState().releaseThread(ref);
    await settleEntryTranscriptForTests(ref);
  });
  const background = acquireBackgroundThread(ref);
  await act(async () => {
    await background.ready;
    await settleEntryTranscriptForTests(ref);
  });
  expect(snapshot()).toMatchObject({ foreground: 1, background: 1, sources: sourceIds });
  return { survivor, background };
}

test("rejected conversation does not seed ownership after another reader acquires", async () => {
  const rejected = await rejectedConversation();
  const healthy = await survivingReader();
  act(() => transcriptDisplayStore.getState().setViewport("mobile"));
  const afterPrepare = snapshot();
  act(() => rejected.doomed.unmount());
  const afterDoomedUnmount = snapshot();
  act(() => cancelConversationHandoff(conversationHandoffGeneration("mobile")));
  const afterCancel = snapshot();
  healthy.survivor.unmount();
  await healthy.background.release();
  expect({ afterPrepare, afterDoomedUnmount, afterCancel }).toMatchObject({
    afterPrepare: { foreground: 1, sources: sourceIds },
    afterDoomedUnmount: { foreground: 1, sources: sourceIds },
    afterCancel: { foreground: 1, sources: sourceIds },
  });
});

test("rejected overlap cancellation never consumes a later reader's ownership or sources", async () => {
  const rejected = await rejectedConversation(true);
  expect(rejected.ensure).toHaveBeenCalledTimes(2);
  await act(async () => {
    await expect(rejected.ensure.mock.results[1]?.value).rejects.toBeDefined();
  });
  expect(snapshot().foreground).toBe(0);
  act(() => rejected.doomed.unmount());
  const healthy = await survivingReader();
  act(() => cancelConversationHandoff(conversationHandoffGeneration("mobile")));
  const afterCancel = snapshot();
  healthy.survivor.unmount();
  await healthy.background.release();
  expect(afterCancel).toMatchObject({ foreground: 1, sources: sourceIds });
});

test("late transport failure after unmount and overlap cancellation preserves replacement ownership", async () => {
  const pending = deferred<appwire.ThreadReadResponse>();
  const requested = deferred<void>();
  const client = new FakeClient("ready");
  client.on("thread/read", () => {
    requested.resolve();
    return pending.promise;
  });
  client.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(client);
  const pane: OpenPaneRecord = { id: "pending-reader", type: "session", params: { ref }, slot: "main" };
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  const readView = retainedTranscriptReadView(conversationPaneLifetime(pane), ref, "session");
  const ensure = vi.spyOn(threadsStore.getState(), "ensureThread");
  const outgoing = render(<RejectionProbe readView={readView} />);
  await act(async () => await requested.promise);
  act(() => transcriptDisplayStore.getState().setViewport("mobile"));
  expect(snapshot().foreground).toBe(2);
  const outgoingClaims = ensure.mock.results.map(({ value }) => value as Promise<void>);
  act(() => {
    cancelConversationHandoff(conversationHandoffGeneration("mobile"));
    outgoing.unmount();
  });
  expect(snapshot().foreground).toBe(0);
  const healthy = await survivingReader(readView);
  act(() => transcriptDisplayStore.getState().setViewport("desktop"));
  expect(snapshot()).toMatchObject({ foreground: 2, sources: sourceIds });
  await act(async () => {
    pending.reject(new Error("late transport failure"));
    // Transport rejection after retirement resolves ensureThread, rather than
    // rejecting its already-released claim. Do not mistake this for catch coverage.
    await expect(Promise.all(outgoingClaims)).resolves.toEqual([undefined, undefined]);
  });
  expect(snapshot()).toMatchObject({ foreground: 2, sources: sourceIds });
  act(() => cancelConversationHandoff(conversationHandoffGeneration("desktop")));
  expect(snapshot()).toMatchObject({ foreground: 1, sources: sourceIds });
  healthy.survivor.unmount();
  expect(snapshot()).toMatchObject({ foreground: 0, items: [] });
  await healthy.background.release();
});
