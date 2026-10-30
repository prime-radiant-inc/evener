import { bindFilePath } from "@evener/appwire-client/docContent";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { projectComicConversation } from "../comic/projection";
import { openDocBeside } from "../panes/doc/openDoc";
import { StubResizeObserver } from "../resizeObserverTestUtils";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import { connectionStore, resetConnectionStoreForTests } from "../stores/connection";
import { ref, response, snapshot, sourceIds, sourceItems } from "../stores/conversationHandoffTestUtils";
import * as entryRoutes from "../stores/entryTranscriptRoutes";
import { resetNavigationStoreForTests } from "../stores/navigation/store";
import { activityContext, activitySummary } from "../stores/sessionActivityTestUtils";
import { SessionEntryCache } from "../stores/sessionEntryCache";
import { foregroundHolds } from "../stores/threadOwners";
import {
  acquireBackgroundThread,
  resetThreadsStoreForTests,
  settleEntryTranscriptForTests,
  threadsStore,
} from "../stores/threads";
import {
  initTranscriptDisplay,
  resetTranscriptDisplayStoreForTests,
  transcriptDisplayStore,
} from "../stores/transcriptDisplay";
import { AppShell } from "./AppShell";
import { activitySidebarStore } from "./activitybar/activitySidebarStore";
import { installFailedLocation, installSwitchableViewport } from "./appShellTestUtils";
import { resetDockChunkForTests } from "./DockRegion";
import { type DockHostModule, resetDockHostLoaderForTests, setDockHostImporterForTests } from "./dockHostChunk";
import { conversationPaneLifetime } from "./paneLifetime";
import { navigate } from "./routing";
import { getDockviewApi, type OpenPaneRecord, resetWorkspaceStoreForTests, workspaceStore } from "./workspace";

let cache: SessionEntryCache;
beforeAll(async () => {
  await import("../panes/session/Session");
  await import("../panes/welcome/Welcome");
  await import("../panes/zoom/Zoom");
  await import("./DockHost");
});
beforeEach(() => {
  installLocalStorage(new MemoryStorage());
  vi.stubGlobal("ResizeObserver", StubResizeObserver);
  const indexedDB = new IDBFactory();
  vi.stubGlobal("indexedDB", indexedDB);
  vi.stubGlobal("IDBKeyRange", IDBKeyRange);
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
  resetNavigationStoreForTests();
  resetTranscriptDisplayStoreForTests();
  resetDockHostLoaderForTests();
  resetDockChunkForTests();
  cache = new SessionEntryCache({ indexedDB, keyRange: IDBKeyRange, databaseName: `host-${crypto.randomUUID()}` });
  entryRoutes.setProductionEntryCacheForTests(cache);
  window.history.replaceState({}, "", "/");
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

async function mountHost(
  mobile: boolean,
  presentation: "standard" | "comic" = "comic",
  configure?: (client: FakeClient, pane: OpenPaneRecord) => void,
) {
  const setMobile = installSwitchableViewport();
  setMobile(mobile);
  initTranscriptDisplay();
  transcriptDisplayStore.setState({ presentation: { desktop: presentation, mobile: presentation } });
  const client = new FakeClient("ready");
  client.on("thread/read", (params) => ({ ...response(params.ref), requestGeneration: params.requestGeneration }));
  client.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(client);
  installFailedLocation(ref);
  window.history.replaceState({}, "", `/s/${encodeURIComponent(ref)}`);
  const pane = { id: "claim-pane", type: "session", params: { ref }, slot: "main" } as const;
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  configure?.(client, pane);
  const ensure = vi.spyOn(threadsStore.getState(), "ensureThread");
  const release = vi.spyOn(threadsStore.getState(), "releaseThread");
  let view: ReturnType<typeof render>;
  await act(async () => {
    view = render(<AppShell client={client} />);
  });
  await waitFor(() => expect(snapshot().sources).toEqual(sourceIds));
  await act(async () => {
    await settleEntryTranscriptForTests(ref);
  });
  expect(snapshot().foreground).toBe(1);
  const background = acquireBackgroundThread(ref);
  await act(async () => {
    await background.ready;
    await settleEntryTranscriptForTests(ref);
  });
  const publications: ReturnType<typeof snapshot>[] = [];
  const unsubscribe = threadsStore.subscribe(() => publications.push(snapshot()));
  const localRead = vi.spyOn(cache, "getLatestWindow");
  const reads = client.calls.filter((call) => call.method === "thread/read").length;
  return {
    setMobile,
    client,
    pane,
    ensure,
    release,
    view: view!,
    background,
    publications,
    unsubscribe,
    localRead,
    reads,
  };
}

function assertPreserved(fixture: Awaited<ReturnType<typeof mountHost>>, ordinaryReaders = 1) {
  expect(fixture.publications.map((value) => value.sources)).toEqual(fixture.publications.map(() => sourceIds));
  expect(
    threadsStore
      .getState()
      .threads.get(ref)
      ?.turns.flatMap((turn) => turn.items.map(({ id, text }) => ({ id, text }))),
  ).toEqual(sourceItems);
  expect(fixture.client.calls.filter((call) => call.method === "thread/read")).toHaveLength(fixture.reads);
  expect(fixture.localRead).not.toHaveBeenCalled();
  expect(snapshot().foreground).toBe(ordinaryReaders);
}

test.each(["standard", "comic"] as const)(
  "real desktop to phone preserves %s source at every publication",
  async (presentation) => {
    const fixture = await mountHost(false, presentation);
    expect(getDockviewApi()?.getPanel(fixture.pane.id)).toBeDefined();
    await act(async () => {
      fixture.setMobile(true);
    });
    expect(getDockviewApi()).toBeNull();
    expect(fixture.release).toHaveBeenCalledWith(ref);
    expect(fixture.ensure.mock.calls.length).toBeGreaterThanOrEqual(2);
    await act(async () => {
      await settleEntryTranscriptForTests(ref);
    });
    try {
      assertPreserved(fixture);
    } finally {
      await disposeFixture(fixture);
    }
  },
);

test("held lazy desktop preserves source across separate commits", async () => {
  const { promise: pending, resolve } = deferred<DockHostModule>();
  setDockHostImporterForTests(() => pending);
  const fixture = await mountHost(true);
  await act(async () => {
    fixture.setMobile(false);
  });
  expect(getDockviewApi()).toBeNull();
  expect(fixture.release).toHaveBeenCalledWith(ref);
  const duringLoading = snapshot();
  await act(async () => {
    resolve(await import("./DockHost"));
    await pending;
  });
  await waitFor(() => expect(getDockviewApi()?.getPanel(fixture.pane.id)).toBeDefined());
  await act(async () => {
    await settleEntryTranscriptForTests(ref);
  });
  try {
    expect(duringLoading.sources).toEqual(sourceIds);
    expect(duringLoading.foreground).toBe(1);
    assertPreserved(fixture);
  } finally {
    await disposeFixture(fixture);
  }
});

test.each(["standard", "comic"] as const)("real phone to desktop preserves %s source", async (presentation) => {
  const fixture = await mountHost(true, presentation);
  expect(getDockviewApi()).toBeNull();
  await act(async () => {
    fixture.setMobile(false);
  });
  expect(getDockviewApi()?.getPanel(fixture.pane.id)).toBeDefined();
  expect(fixture.release).toHaveBeenCalledWith(ref);
  try {
    assertPreserved(fixture);
  } finally {
    await disposeFixture(fixture);
  }
});

test.each([
  { mobile: false, host: "desktop" },
  { mobile: true, host: "phone" },
])("same-batch viewport round trip retains the $host host and balances ownership", async ({ mobile }) => {
  const fixture = await mountHost(mobile);
  const desktop = getDockviewApi();
  const phonePane = document.querySelector("[data-stacked-pane-type]");
  const observedBodies: Array<Array<{ id: string; text: string | undefined }>> = [];
  const observeBodies = () => {
    observedBodies.push(
      threadsStore
        .getState()
        .threads.get(ref)
        ?.turns.flatMap((turn) => turn.items.map(({ id, text }) => ({ id, text }))) ?? [],
    );
  };
  const unsubscribeBodies = threadsStore.subscribe(observeBodies);
  try {
    if (mobile) expect(phonePane).not.toBeNull();
    else expect(desktop?.getPanel(fixture.pane.id)).toBeDefined();
    await act(async () => {
      fixture.setMobile(!mobile);
      expect(transcriptDisplayStore.getState().viewport).toBe(mobile ? "desktop" : "mobile");
      expect(snapshot()).toMatchObject({ foreground: 2, sources: sourceIds });
      observeBodies();
      fixture.setMobile(mobile);
      observeBodies();
      expect(snapshot()).toMatchObject({ foreground: 1, sources: sourceIds });
    });
    expect(transcriptDisplayStore.getState().viewport).toBe(mobile ? "mobile" : "desktop");
    expect(getDockviewApi()).toBe(desktop);
    expect(document.querySelector("[data-stacked-pane-type]")).toBe(phonePane);
    expect(observedBodies).toEqual(observedBodies.map(() => sourceItems));
    assertPreserved(fixture);
  } finally {
    unsubscribeBodies();
    await disposeFixture(fixture);
  }
});

test("pre-event media snapshot cannot replace the host before preparation", async () => {
  const fixture = await mountHost(false);
  const desktop = getDockviewApi();
  const media = window.matchMedia("(max-width: 899px)");
  vi.spyOn(media, "matches", "get").mockReturnValue(true);
  fixture.view.rerender(<AppShell client={fixture.client} bannerDelayMs={0} />);
  expect(getDockviewApi()).toBe(desktop);
  expect(snapshot().foreground).toBe(1);
  await act(async () => {
    fixture.setMobile(true);
  });
  expect(getDockviewApi()).toBeNull();
  try {
    assertPreserved(fixture);
  } finally {
    await disposeFixture(fixture);
  }
});

async function heldDestination() {
  const { promise: pending, resolve, reject: rejectPending } = deferred<DockHostModule>();
  const reject = (reason: Error): void => rejectPending(reason);
  setDockHostImporterForTests(() => pending);
  const fixture = await mountHost(true);
  const lifetime = conversationPaneLifetime(fixture.pane);
  await act(async () => {
    fixture.setMobile(false);
  });
  expect(getDockviewApi()).toBeNull();
  expect(snapshot()).toMatchObject({ foreground: 1, sources: sourceIds });
  return {
    ...fixture,
    lifetime,
    reject,
    async finish() {
      await act(async () => {
        resolve(await import("./DockHost"));
        await pending;
      });
    },
  };
}

async function disposeFixture(fixture: Awaited<ReturnType<typeof mountHost>>) {
  fixture.unsubscribe();
  fixture.view.unmount();
  await fixture.background.release();
}

test.each(["close", "same-id", "requested-ref", "route"] as const)(
  "held desktop cancels on %s and fences late completion",
  async (departure) => {
    const fixture = await heldDestination();
    await act(async () => {
      if (departure === "route") navigate("/unknown-handoff-route");
      else if (departure === "close") workspaceStore.getState().closePane(fixture.pane.id);
      else
        workspaceStore.setState({
          panes: [{ ...fixture.pane, params: { ref: departure === "same-id" ? ref : "local:replacement" } }],
        });
      await settleEntryTranscriptForTests(ref);
    });
    expect(snapshot().foreground).toBe(0);
    expect(snapshot().items).toEqual([]);
    if (departure !== "route") expect(fixture.lifetime.alive).toBe(false);
    // Leave the replacement unpresented: resolving obsolete chunk work must not
    // manufacture ownership for the old source or resurrect the disposed view.
    await act(async () => {
      navigate("/unknown-handoff-route");
    });
    await fixture.finish();
    expect(snapshot().foreground).toBe(0);
    expect(snapshot().items).toEqual([]);
    await disposeFixture(fixture);
  },
);

test("held desktop cancels an established logical session replacement", async () => {
  const fixture = await heldDestination();
  const previousIdentity = threadsStore.getState().threads.get(ref)?.imageSessionId;
  expect(previousIdentity).toBeDefined();
  fixture.client.on("thread/read", (params) => {
    const next = response();
    return {
      ...next,
      requestGeneration: params.requestGeneration,
      thread: { ...next.thread, sessionId: "replacement-session" },
    };
  });
  await act(async () => {
    await threadsStore.getState().refreshThread(ref);
  });
  expect(threadsStore.getState().threads.get(ref)?.imageSessionId).not.toBe(previousIdentity);
  expect(snapshot().foreground).toBe(0);
  await act(async () => {
    navigate("/unknown-handoff-route");
  });
  await fixture.finish();
  expect(snapshot().foreground).toBe(0);
  await disposeFixture(fixture);
});

test("rapid reversal transfers a held overlap without a zero-owner publication", async () => {
  const fixture = await heldDestination();
  await act(async () => {
    fixture.setMobile(true);
  });
  expect(getDockviewApi()).toBeNull();
  assertPreserved(fixture);
  await fixture.finish();
  expect(getDockviewApi()).toBeNull();
  assertPreserved(fixture);
  await disposeFixture(fixture);
});

test("failed desktop chunk releases overlap and retry cannot revive it", async () => {
  const fixture = await heldDestination();
  const error = vi.spyOn(console, "error").mockImplementation(() => {});
  await act(async () => {
    fixture.reject(new Error("scripted chunk failure"));
  });
  expect(error).toHaveBeenCalled();
  expect(snapshot().foreground).toBe(0);
  expect(snapshot().items).toEqual([]);
  setDockHostImporterForTests(() => import("./DockHost"));
  await act(async () => {
    fireEvent.click(within(screen.getByTestId("workspace-host")).getByRole("button", { name: "Retry" }));
  });
  await act(async () => {
    await settleEntryTranscriptForTests(ref);
  });
  expect(getDockviewApi()?.getPanel(fixture.pane.id)).toBeDefined();
  expect(snapshot().foreground).toBe(1);
  await act(async () => {
    navigate("/unknown-handoff-route");
  });
  expect(snapshot().foreground).toBe(0);
  await disposeFixture(fixture);
});

test("covered phone conversation keeps ordinary ownership without stranded overlap", async () => {
  const fixture = await mountHost(true);
  await act(async () => {
    activitySidebarStore.getState().openFor(ref, "about");
  });
  // The existing About inspector owns a second, non-conversation claim.
  expect(snapshot().foreground).toBe(2);
  await act(async () => {
    fixture.setMobile(false);
  });
  assertPreserved(fixture, 2);
  await act(async () => {
    activitySidebarStore.getState().close();
  });
  fixture.view.unmount();
  expect(snapshot().foreground).toBe(0);
  await disposeFixture(fixture);
});

test("desktop split visibility is not focus, phone selection rejects the other reader", async () => {
  const fixture = await mountHost(false);
  const otherRef = "local:split-reader";
  let otherPaneId = "";
  await act(async () => {
    otherPaneId = workspaceStore.getState().openPane("session", { ref: otherRef }, { slot: "secondary" });
    await settleEntryTranscriptForTests(otherRef);
  });
  await waitFor(() => expect(foregroundHolds(otherRef)).toBe(1));
  await act(async () => {
    workspaceStore.getState().focusPane(otherPaneId);
  });
  await waitFor(() => {
    const model = threadsStore.getState().threads.get(otherRef);
    expect(model ? projectComicConversation(model).map((entry) => entry.id) : []).toEqual(sourceIds);
  });
  await act(async () => {
    await settleEntryTranscriptForTests(otherRef);
  });
  expect(getDockviewApi()?.getPanel(fixture.pane.id)?.api.isVisible).toBe(true);
  expect(getDockviewApi()?.activePanel?.id).toBe(otherPaneId);
  expect(workspaceStore.getState().focusedPaneId).toBe(otherPaneId);
  expect(snapshot().foreground).toBe(1);
  const publications: string[][] = [];
  const unsubscribe = threadsStore.subscribe(() => {
    const model = threadsStore.getState().threads.get(otherRef);
    publications.push(model ? projectComicConversation(model).map((entry) => entry.id) : []);
  });
  await act(async () => {
    fixture.setMobile(true);
  });
  expect(workspaceStore.getState().focusedPaneId).toBe(otherPaneId);
  expect(snapshot()).toMatchObject({ foreground: 0, items: [] });
  expect(foregroundHolds(otherRef)).toBe(1);
  expect(publications).toEqual(publications.map(() => sourceIds));
  unsubscribe();
  await disposeFixture(fixture);
});

test.each(["doc", "sessionTasks"] as const)(
  "phone selection of %s does not inherit a conversation overlap",
  async (type) => {
    const fixture = await mountHost(false);
    await act(async () => {
      if (type === "doc") {
        const reference = bindFilePath("/tmp/handoff.md", "/tmp");
        if (!reference) throw new Error("fixture document reference missing");
        openDocBeside({ session: ref, reference, sourcePaneId: fixture.pane.id });
      } else workspaceStore.getState().openPane(type, { ref }, { slot: "secondary" });
    });
    // Tasks is an ordinary non-conversation owner of the same ref. Its own
    // claim continues to follow existing semantics, but cannot adopt a bridge.
    const ordinaryReaders = type === "sessionTasks" ? 1 : 0;
    await act(async () => {
      fixture.setMobile(true);
    });
    expect(document.querySelector("[data-stacked-pane-type]")?.getAttribute("data-stacked-pane-type")).toBe(type);
    expect(snapshot().foreground).toBe(ordinaryReaders);
    if (ordinaryReaders === 0) expect(snapshot().items).toEqual([]);
    await disposeFixture(fixture);
  },
);

test("desktop inactive tab cannot keep a rejected pending conversation", async () => {
  const fixture = await mountHost(false);
  let otherId = "";
  await act(async () => {
    otherId = workspaceStore.getState().openPane("session", { ref: "local:inactive" }, { slot: "secondary" });
    await settleEntryTranscriptForTests("local:inactive");
  });
  await act(async () => {
    workspaceStore.getState().openPane("welcome", {}, { slot: "secondary" });
  });
  expect(getDockviewApi()?.getPanel(otherId)?.api.isVisible).toBe(false);
  expect(foregroundHolds("local:inactive")).toBe(1);
  await act(async () => {
    fixture.setMobile(true);
    // Dockview's detached tab keeps its ordinary owner until host teardown,
    // but an invisible reader must not seed a temporary second owner.
    expect(foregroundHolds("local:inactive")).toBe(1);
  });
  expect(snapshot().foreground).toBe(1);
  expect(foregroundHolds("local:inactive")).toBe(0);
  await act(async () => {
    fixture.setMobile(false);
  });
  // Dockview can initialize and then detach restored tabs. That ordinary
  // ownership is unchanged, and must not gain an extra handoff owner.
  expect(getDockviewApi()?.getPanel(otherId)?.api.isVisible).toBe(false);
  expect(foregroundHolds("local:inactive")).toBe(1);
  await disposeFixture(fixture);
});

test("real Zoom preserves the leaf and retires readable parent ownership on phone admission", async () => {
  const ancestor = "local:ancestor";
  const parent = "local:parent";
  const lineage = (requested: string) =>
    requested === ref ? [ancestor, parent] : requested === parent ? [ancestor] : [];
  const context = (requested: string) => ({
    ...activityContext(requested),
    sessionId: response(requested).thread.sessionId,
    rootRef: ancestor,
    ancestors: lineage(requested).map((id) => ({ ref: id, sessionId: response(id).thread.sessionId, title: id })),
  });
  const fixture = await mountHost(false, "comic", (client, pane) => {
    installFailedLocation(ancestor);
    window.history.replaceState({}, "", `/s/${encodeURIComponent(ancestor)}`);
    client.on("evener/thread/activity/read", ({ ref: requested, scope }) => ({
      ...activitySummary(requested, scope),
      context: context(requested),
    }));
    client.on("evener/thread/delegates/list", ({ ref: requested }) => ({
      context: context(requested),
      scope: "session",
      delegates: [],
      page: { complete: true, issues: [] },
    }));
    pane.type = "sessionZoom";
    pane.params = {
      ref,
      source: { type: "session", params: { ref: ancestor } },
      edges: [
        { ownerRef: ancestor, childRef: parent, delegateId: "first" },
        { ownerRef: parent, childRef: ref, delegateId: "second" },
      ],
    };
  });
  await act(async () => {
    await settleEntryTranscriptForTests(parent);
  });
  expect(document.querySelector(`[data-testid="cascade-column"][data-scope-ref="${ref}"]`)).not.toBeNull();
  expect(document.querySelector(`[data-testid="cascade-column"][data-scope-ref="${parent}"]`)).not.toBeNull();
  expect(document.querySelector(`[data-testid="cascade-spine"][data-scope-ref="${ancestor}"]`)).not.toBeNull();
  expect(foregroundHolds(parent)).toBe(1);
  expect(foregroundHolds(ancestor)).toBe(0);
  const parentBackground = acquireBackgroundThread(parent);
  await act(async () => {
    await parentBackground.ready;
    await settleEntryTranscriptForTests(parent);
  });
  expect(
    threadsStore
      .getState()
      .threads.get(parent)
      ?.turns.flatMap((turn) => turn.items),
  ).toHaveLength(2);
  // Include parent setup hydration, then forbid any handoff-triggered read.
  fixture.reads = fixture.client.calls.filter((call) => call.method === "thread/read").length;
  await act(async () => {
    fixture.setMobile(true);
  });
  expect(document.querySelectorAll('[data-testid="cascade-column"]')).toHaveLength(1);
  expect(document.querySelector(`[data-scope-ref="${parent}"]`)).toBeNull();
  expect(foregroundHolds(parent)).toBe(0);
  expect(foregroundHolds(ancestor)).toBe(0);
  expect(
    threadsStore
      .getState()
      .threads.get(parent)
      ?.turns.flatMap((turn) => turn.items),
  ).toEqual([]);
  assertPreserved(fixture);
  await disposeFixture(fixture);
  await parentBackground.release();
});

test("connection loss does not erase a started conversation during replacement", async () => {
  const fixture = await mountHost(false);
  await act(async () => {
    fixture.client.emitStateChange("reconnecting");
  });
  expect(connectionStore.getState().state).toBe("reconnecting");
  await act(async () => {
    fixture.setMobile(true);
  });
  expect(getDockviewApi()).toBeNull();
  try {
    assertPreserved(fixture);
  } finally {
    await disposeFixture(fixture);
  }
});
