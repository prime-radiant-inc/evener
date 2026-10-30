import type {
  InitializeResponse,
  NavigationReadParams,
  NavigationReadResponse,
  NavigationSessionLocation,
} from "@evener/appwire-client";
import {
  APPWIRE_PROTOCOL_VERSION,
  AppwireClient,
  acquireThreadSubscription,
  type ConnectionState,
} from "@evener/appwire-client";
import { keyID } from "@evener/appwire-client/state/navigation";
import { currentRead } from "@evener/appwire-client/testing/currentTranscript";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { StrictMode } from "react";
import { afterEach, beforeAll, beforeEach, expect, onTestFinished, test, vi } from "vitest";
import { initNotifications, resetNotificationsForTests } from "../notifications";
import { StubResizeObserver } from "../resizeObserverTestUtils";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import { resetAuthStatusesStoreForTests } from "../stores/authStatuses";
import * as backgroundTranscriptsModule from "../stores/backgroundTranscripts";
import { connectionStore } from "../stores/connection";
import { productionEntryTranscriptForTests, setProductionEntryCacheForTests } from "../stores/entryTranscriptRoutes";
import { initNavigation, navigationStore, resetNavigationStoreForTests } from "../stores/navigation/store";
import { resetPrefsStoreForTests } from "../stores/prefs";
import { SessionEntryCache } from "../stores/sessionEntryCache";
import { resetSettingsHostForTests } from "../stores/settingsHost";
import { resetSettingsOverviewStoreForTests } from "../stores/settingsOverview";
import { holdNextWriteTransaction } from "../stores/testing/stalledIndexedDB";
import * as threadsModule from "../stores/threads";
import { resetThreadsStoreForTests, threadsStore } from "../stores/threads";
import { emptyStateReasons } from "../widgets/emptystate/emptyStateTestUtils";
import { AppShell } from "./AppShell";
import { installMobileViewport } from "./appShellTestUtils";
import { paletteStore } from "./palette/paletteController";
import { resetWorkspaceStoreForTests, workspaceStore } from "./workspace";

const ALL_FEATURES_OFF = {
  threadList: false,
  threadTurnsList: false,
  turnStart: false,
  turnSteer: false,
  threadClear: false,
  threadShutdown: false,
  forkFromTurn: false,
  tasks: false,
  transcriptList: false,
  modelList: false,
  directoryComplete: false,
  auth: false,
};

const TREE_SESSION = {
  row_id: "project:proj1:local:s1",
  ref: "local:s1",
  host_id: "local",
  session_id: "s1",
  title: "Session one",
  project: "prime-radiant",
  state: "idle",
  kind: "session",
  live: true,
  children: [],
};

let stoppedTreeSession = false;

const EMPTY_NAV_RESPONSE = {
  generation_id: "generation_test",
  revision: 1,
  sources: [],
  attentionSummary: { needsYou: 0, error: 0, working: 0 },
  sections: { live: { count: 1 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
  catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
};

function navigationRead(params: NavigationReadParams): NavigationReadResponse {
  const treeSession = stoppedTreeSession ? { ...TREE_SESSION, state: "ended", live: false } : TREE_SESSION;
  switch (params.resource) {
    case "manifest":
      return wireSnapshot(params, EMPTY_NAV_RESPONSE, '"test"');
    case "section":
      return wireSnapshot(
        params,
        {
          sessions: params.section === "live" ? [treeSession] : [],
          remaining: 0,
          truncated: false,
        },
        '"test"',
      );
    case "pin_catalog":
      return wireSnapshot(params, { pin_sections: [], remaining: 0 }, '"test"');
    case "pin_section":
      return wireSnapshot(
        params,
        {
          sessions: [],
          remaining: 0,
          truncated: false,
        },
        '"test"',
      );
    case "catalog":
      return wireSnapshot(
        params,
        {
          projects:
            params.catalog === "projects"
              ? [{ key: "proj1", name: "Project one", session_count: 1, working_dir: "" }]
              : [],
          remaining: 0,
        },
        '"test"',
      );
    case "project":
      return wireSnapshot(
        params,
        {
          key: "proj1",
          current: { sessions: [treeSession], remaining: 0 },
          recent: { sessions: [], remaining: 0 },
          archived: { sessions: [], remaining: 0 },
          truncated: false,
        },
        '"test"',
      );
    case "project_page":
      return wireSnapshot(
        params,
        {
          key: params.projectKey,
          tier: params.tier,
          offset: params.offset,
          sessions: [],
          remaining: 0,
          truncated: false,
        },
        '"test"',
      );
    case "location":
      return wireSnapshot(
        params,
        {
          ref: params.ref,
          top_level_ref: params.ref,
          top_level: true,
          session: { ...treeSession, ref: params.ref, session_id: params.ref },
        },
        '"test"',
      );
    case "subagents":
      // Session activity surfaces can demand the scope's subagents page;
      // answer with an empty one (tests that need rows install them into the
      // store directly).
      return wireSnapshot(
        params,
        {
          offset: params.offset,
          sessions: [],
          remaining: 0,
          truncated: false,
        },
        '"test"',
      );
  }
  throw new Error(`unsupported navigation resource: ${params.resource}`);
}

// A FakeClient whose connect() advertises a v2 navigation capability with a
// generation matching EMPTY_NAV_RESPONSE. Tests that render <AppShell/> and
// depend on the navigation store being in mode "v3" (rather than "error")
// must use this instead of a bare `new FakeClient("ready")`, whose default
// InitializeResponse has no navigation capability.
function navClient(initialState: ConnectionState = "ready"): FakeClient {
  const client = new FakeClient(initialState);
  client.on("evener/navigation/read", navigationRead);
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: APPWIRE_PROTOCOL_VERSION,
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "generation_test", sequence: 0, readVersions: [3] },
  }));
  return client;
}

function installLocation(location: NavigationSessionLocation): void {
  const key = { kind: "location", ref: location.ref } as const;
  const resources = new Map(navigationStore.getState().resources);
  resources.set(keyID(key), {
    key,
    data: location,
    loadedRevision: location.revision,
    targetRevision: null,
    forceToken: 0,
    etag: '"test"',
    loading: false,
    stale: false,
    error: null,
    generationID: location.generation_id,
  });
  navigationStore.setState({
    mode: "v3",
    clientGenerationID: location.generation_id,
    resources,
  });
}

function installLocationForRoute(ref: string, ownerRef?: string): void {
  const owner = ownerRef ?? (ref === "local:child" ? "local:owner" : ref === "local:sub1" ? "local:s1" : ref);
  installLocation({
    generation_id: "generation_test",
    revision: 1,
    ref,
    top_level_ref: owner,
    top_level: owner === ref,
    tier: "current",
    session: {
      ref,
      host_id: "local",
      session_id: ref,
      title: ref,
      project: "test-project",
      state: "idle",
      kind: owner === ref ? "session" : "subagent",
      live: false,
      children: [],
    },
  });
}

// Renders a route to completion so both halves of its lazy-loading cost are
// already paid by the time a test measures it. The module cache is only the
// first half: React.lazy keeps a payload of its own that stays uninitialized
// until React first RENDERS the component, so a warm module cache still
// leaves the first render suspending. The welcome route crosses two nested
// boundaries (AppShell's lazy DockHost, then PaneHost's lazy Welcome); each
// other pane crosses one more of its own. Rendering inside an awaited act
// lets each reveal commit as soon as its already-imported chunk resolves;
// outside act, react-dom holds every reveal for FALLBACK_THROTTLE_MS (300ms,
// react-dom 19.2) on a real timer. Mirrors App.test.tsx's own warmRoute.
//
// The landmark wait gets WARM_ROUTE_TRIPWIRE_MS rather than findBy's 1000ms
// default. That default is an assertion window - it exists to hold a test to a
// responsiveness bar - and a warm-up has no such bar to hold, so the deadline
// here is a tripwire for a hung render.
const WARM_ROUTE_TRIPWIRE_MS = 10_000;

async function warmRoute(
  path: string,
  findLandmark: (options: { timeout: number }) => Promise<unknown>,
): Promise<void> {
  window.history.pushState({}, "", path);
  if (path.startsWith("/s/")) {
    const ref = decodeURIComponent(path.slice("/s/".length));
    installLocation({
      generation_id: "generation_test",
      revision: 1,
      ref,
      top_level_ref: ref,
      top_level: true,
      session: {
        ref,
        host_id: "local",
        session_id: ref,
        title: ref,
        project: "",
        state: "idle",
        kind: "session",
        live: false,
        children: [],
      },
    });
  }
  await act(async () => {
    render(<AppShell client={new FakeClient("ready")} />);
  });
  await findLandmark({ timeout: WARM_ROUTE_TRIPWIRE_MS });
  // Unmounting also clears DockHost's pending debounced layout save (its own
  // effect cleanup), so no warm render leaks a write into a later test.
  cleanup();
  resetWorkspaceStoreForTests();
  // The /settings/hub warm route mounts the real HubSection, whose
  // mount effect calls settingsOverviewStore.getState().fetch() for real
  // against this warm-up's own FakeClient (no handler scripted for
  // "evener/settings/overview", so it settles into a scripted-looking
  // "no handler" error) - settingsOverviewStore is a module singleton, so
  // that leftover error/inflight bookkeeping must not survive into this
  // file's own tests.
  resetSettingsOverviewStoreForTests();
  localStorage.clear();
  window.history.pushState({}, "", "/");
}

// Await the welcome/session panes' lazy-loaded modules ONCE up front so
// React.lazy resolves from a warm module cache - mirrors App.test.tsx's own
// beforeAll pattern for the same reason: the slow part of lazy-loading is
// the transform/import work, an awaitable completion, not something to race
// with a widened findBy deadline.
beforeAll(async () => {
  globalThis.ResizeObserver = StubResizeObserver;
  installLocalStorage(new MemoryStorage());
  await import("../panes/welcome/Welcome");
  await import("../panes/session/Session");
  await import("../panes/settings/Settings");
  await import("../panes/spawn/Spawn");
  // AppShell.tsx now React.lazy()s DockHost itself (Task 7's bundle split -
  // dockview is dead weight on the mobile path) - pre-warmed for the same
  // reason as the two panes above.
  await import("./DockHost");

  // Then render each pane once, for the React.lazy half of the cost - see
  // warmRoute above. Awaiting real completion, in a hook whose ceiling is a
  // tripwire, rather than spending it inside a test's assertion window.
  await warmRoute("/", (wait) => screen.findByText("No session open", undefined, wait));
  await warmRoute("/new", (wait) => screen.findByRole("button", { name: "Start" }, wait));
  await warmRoute("/s/local:ref_warm", (wait) => screen.findByText(/loading transcript/i, undefined, wait));
  await warmRoute("/settings/hub", (wait) => screen.findByRole("navigation", { name: "Settings sections" }, wait));
});

beforeEach(() => {
  stoppedTreeSession = false;
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetWorkspaceStoreForTests();
  resetNavigationStoreForTests();
  // notifications/index.ts keeps module state (its init guard, the
  // "reconnect" detector's sawReady, the attention baseline) for every test
  // in this file, and a test that connects a client straight to "ready" arms
  // that detector. Reset and re-init before each test's own fresh connect so
  // every test starts from the state a fresh module evaluation leaves:
  // engine started, seeded from the idle connection and reset navigation
  // store above, with nothing carried over from the previous test.
  resetNotificationsForTests();
  initNotifications();
  navigationStore.setState({ mode: "v3" });
  // afterEach restores Vitest globals; recreate deterministic storage before
  // clearing it so DockHost cannot restore the prior test's layout.
  installLocalStorage(new MemoryStorage());
  localStorage.clear();
  // The settings pane's last-visited-section memory (prefs.ts's
  // lastSettingsSection) is a module-singleton field backed by this same
  // storage: rehydrate against the cleared storage, or one test's settings
  // visit picks the next test's bare-/settings landing section.
  resetPrefsStoreForTests();
  resetSettingsHostForTests();
  // The credential statuses store is a module singleton too: once a test's
  // Providers & credentials section has read it, it re-reads on every later
  // connect, so the next test's client would see an evener/auth/list it never
  // caused.
  resetAuthStatusesStoreForTests();
});

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
  // The command palette store is a module singleton; reset it so one test's
  // open palette never leaks into the next.
  paletteStore.setState({ open: false, query: "" });
  // Several tests below navigate to /settings/* and mount the real
  // HubSection, whose mount effect touches settingsOverviewStore for
  // real - same module-singleton reasoning as warmRoute's own reset above.
  resetSettingsOverviewStoreForTests();
  vi.unstubAllGlobals();
});

test("mounts and renders the welcome pane", async () => {
  render(<AppShell client={new FakeClient("ready")} />);
  await waitFor(() => expect(emptyStateReasons(document.body)).toContain("no-session"));
});

test("wires the injected client into connectionStore (connects on mount)", async () => {
  const fake = new FakeClient("ready");
  const connect = vi.spyOn(fake, "connect");
  try {
    render(<AppShell client={fake} />);
    expect(connectionStore.getState().client).toBe(fake);
    expect(connectionStore.getState().state).toBe("ready");
    expect(connect).toHaveBeenCalled();
    const connectionResult = connect.mock.results[0];
    if (connectionResult?.type !== "return") throw new Error("AppShell did not start the handshake");
    await act(async () => {
      await connectionResult.value;
    });
  } finally {
    connect.mockRestore();
  }
});

test("rerender keeps the injected client's navigation wiring and unmount leaves the client open", async () => {
  const client = navClient();
  const view = render(<AppShell client={client} />);
  await waitFor(() => expect(navigationStore.getState().mode).toBe("v3"));
  const manifest = navigationStore.getState().manifest;
  expect(manifest?.error).toBeNull();
  const manifestReads = () =>
    client.calls.filter(
      (call) =>
        call.method === "evener/navigation/read" && (call.params as NavigationReadParams).resource === "manifest",
    );
  expect(manifestReads()).toHaveLength(1);

  view.rerender(<AppShell client={client} />);
  expect(connectionStore.getState().client).toBe(client);
  expect(navigationStore.getState().manifest).toBe(manifest);
  expect(manifestReads()).toHaveLength(1);

  view.unmount();
  expect(navigationStore.getState().mode).toBe("unknown");
  expect(client.state).toBe("ready");
});

test("shows no banner while the injected client is ready", async () => {
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");
  expect(screen.queryByText(/reconnecting/i)).toBeNull();
  expect(screen.queryByText(/connection closed/i)).toBeNull();
});

test("a banner retry swaps the client useClient consumers call", async () => {
  const user = userEvent.setup();
  const stale = new FakeClient("closed");
  const fresh = new FakeClient("ready");
  fresh.on("evener/search", () => ({ live: [], past: [] }));
  render(<AppShell client={stale} bannerDelayMs={0} bannerCreateClient={() => fresh} />);
  await screen.findByText("No session open");

  // The banner is visible for the closed client; retry wires and adopts fresh.
  await user.click(await screen.findByRole("button", { name: "Retry" }));
  await waitFor(() => expect(connectionStore.getState().client).toBe(fresh));

  // A useClient consumer (the palette search) must reach the fresh client,
  // not the closed original.
  await user.keyboard("{Meta>}k{/Meta}");
  await user.type(await screen.findByRole("combobox"), "hello");
  await waitFor(() => expect(fresh.calls.some((call) => call.method === "evener/search")).toBe(true));
  expect(stale.calls.some((call) => call.method === "evener/search")).toBe(false);
});

// --- background Live transcript ownership (Task 7) ---------------------

// The AppShell-owned background roster owner follows the complete Live roster
// through the real navigation and thread stores. These tests observe its
// actual wire demand: one additive thread membership read (subscribe:true) per
// discovered Live ref, released on the owner's lifetime end.
function currentThreadRead(params: { ref?: string; requestGeneration?: number }) {
  return { ...currentRead(params.ref ?? "local:s1"), requestGeneration: params.requestGeneration };
}

function membershipReads(client: FakeClient): number {
  return client.calls.filter(
    (call) => call.method === "thread/read" && (call.params as { subscribe?: boolean }).subscribe === true,
  ).length;
}

function unsubscribedRefs(client: FakeClient): string[] {
  return client.calls
    .filter((call) => call.method === "thread/unsubscribe")
    .map((call) => (call.params as { ref?: string }).ref ?? "");
}

function backgroundReadyClient(): FakeClient {
  const client = navClient();
  client.on("thread/read", currentThreadRead);
  client.on("thread/unsubscribe", () => ({}));
  return client;
}

test("two mounted panes and an activity holder survive background ownership release", async () => {
  const client = backgroundReadyClient();
  const owners = trackBackgroundOwners();
  const activity = acquireThreadSubscription(client, "local:s1");
  const ensure = vi.spyOn(threadsStore.getState(), "ensureThread");
  let view: ReturnType<typeof render> | undefined;
  try {
    window.history.pushState({}, "", "/s/local:s1");
    installLocationForRoute("local:s1");
    view = render(<AppShell client={client} />);
    await waitFor(() => expect(membershipReads(client)).toBeGreaterThanOrEqual(1));
    act(() => {
      workspaceStore
        .getState()
        .openPane("transcript", { ref: "local:s1", parentRef: "local:owner" }, { slot: "secondary" });
    });
    await waitFor(() =>
      expect(ensure.mock.calls.filter(([ref]) => ref === "local:s1").length).toBeGreaterThanOrEqual(2),
    );
    await activity.read({ includeTurns: false, itemsView: "full", itemLimit: 80 });
    await owners.records[0]!.owner.settled();
    const boundary = client.calls.length;
    await act(async () => {
      await owners.records[0]!.owner.dispose();
    });
    expect(unsubscribedRefs(client)).not.toContain("local:s1");
    expect(
      workspaceStore.getState().panes.filter((pane) => (pane.params as { ref?: string }).ref === "local:s1"),
    ).toHaveLength(2);
    expect(
      client.calls
        .slice(boundary)
        .filter((call) => call.method.startsWith("thread/"))
        .every((call) => call.method === "thread/read" || call.method === "thread/unsubscribe"),
    ).toBe(true);
    view.unmount();
    view = undefined;
    await owners.joinDisposals();
    expect(unsubscribedRefs(client)).not.toContain("local:s1");
    activity.release();
    await waitFor(() => expect(unsubscribedRefs(client)).toContain("local:s1"));
  } finally {
    view?.unmount();
    activity.release();
    await owners.joinDisposals();
    ensure.mockRestore();
    owners.restore();
  }
});

// A coherent completed-entry read carrying `literal`; final release re-reads
// and checkpoints it. Distinct successor bytes carry a newer item version.
function currentVersionedThreadRead(literal: string, version: number) {
  return (params: { ref?: string; requestGeneration?: number }) => {
    const base = currentRead(params.ref ?? "local:s1");
    base.snapshot = { incarnation: "inc-1", length: 1 };
    base.thread.status = { type: "idle" };
    delete base.overlay;
    delete base.olderCursor;
    base.thread.turns = [
      {
        id: "turn_1",
        itemsView: "full",
        status: "completed",
        version,
        items: [
          {
            type: "agentMessage",
            id: "turn_1-item-0",
            transcriptKey: "apptranscript-item-v2:turn_1:0:0",
            turnId: "turn_1",
            position: { entry: 0, item: 0 },
            version,
            text: literal,
          },
        ],
      },
    ];
    return { ...base, requestGeneration: params.requestGeneration };
  };
}

function versionedClient(literal: string, version = 1): FakeClient {
  const client = navClient();
  client.on("thread/read", currentVersionedThreadRead(literal, version));
  client.on("thread/unsubscribe", () => ({}));
  return client;
}

function threadsHistory(ref: string): string {
  return JSON.stringify(threadsStore.getState().threads.get(ref)?.history ?? {});
}

function deferredSignal(): { promise: Promise<void>; resolve: () => void } {
  let resolve: (() => void) | undefined;
  const promise = new Promise<void>((settle) => {
    resolve = settle;
  });
  return { promise, resolve: () => resolve?.() };
}

// Retain a primary failure while still reporting a cleanup failure beside it.
function combineErrors(primary: unknown, cleanup: unknown): unknown {
  if (primary === undefined) return cleanup;
  return new AggregateError([primary, cleanup], "test body and cleanup both failed");
}

// Call-through instrumentation of the real background thread lifetime:
// - Records every real acquisition and whether it happened at/after the
//   predecessor's real release entry (the ownership oracle).
// - For the predecessor's lease (the first acquired after arm()), spies the real
//   release with a SYNCHRONOUS pass-through: it marks release entry and returns
//   the real release promise without delaying entry, so the real synchronous
//   retirement runs and the later real cache flush is attributable to it.
function instrumentBackgroundLifetime() {
  const realAcquire = threadsModule.acquireBackgroundThread;
  const acquisitions: Array<{ ref: string; afterReleaseEntry: boolean }> = [];
  let armed = false;
  let predecessorReleaseSpied = false;
  let releaseEntered = false;
  const spy = vi.spyOn(threadsModule, "acquireBackgroundThread").mockImplementation((ref) => {
    const lease = realAcquire(ref);
    acquisitions.push({ ref, afterReleaseEntry: releaseEntered });
    if (armed && !predecessorReleaseSpied) {
      predecessorReleaseSpied = true;
      const realRelease = lease.release.bind(lease);
      vi.spyOn(lease, "release").mockImplementation(() => {
        releaseEntered = true;
        return realRelease();
      });
    }
    return lease;
  });
  return {
    arm() {
      armed = true;
    },
    acquisitions,
    releaseEntered: () => releaseEntered,
    acquisitionsAfterReleaseEntry: (ref: string) =>
      acquisitions.filter((entry) => entry.ref === ref && entry.afterReleaseEntry).length,
    restore() {
      spy.mockRestore();
    },
  };
}

// A one-shot hold over the predecessor owner's real final-release cache write.
// `disarm()` stops any FUTURE hold from being armed; cleanup must call it
// before releasing the current hold and before unmount/dispose can start a
// fresh flush. A hold armed after cleanup began (i.e. after `beginCleanup()`)
// is recorded and signalled, so a failure proof can detect an orphaned hold and
// release it rather than hanging the suite.
function armReleaseWriteHold(cache: SessionEntryCache, lifetime: ReturnType<typeof instrumentBackgroundLifetime>) {
  let held: ReturnType<typeof holdNextWriteTransaction> | undefined;
  let consumed = false;
  let disarmed = false;
  let cleanupBegan = false;
  let signalArmedDuringCleanup: (() => void) | undefined;
  const armedDuringCleanup = new Promise<void>((resolve) => {
    signalArmedDuringCleanup = resolve;
  });
  const writeEntered = deferredSignal();
  const realCommit = cache.commitBatch.bind(cache);
  const spy = vi.spyOn(cache, "commitBatch").mockImplementation(async (batch) => {
    // One-shot: hold only the predecessor's real release flush, never a later
    // successor teardown write. After disarm no new hold may be armed, so
    // teardown cannot leave an unreleased gate behind.
    if (!disarmed && !consumed && lifetime.releaseEntered()) {
      consumed = true;
      held = holdNextWriteTransaction(["transcriptItems", "transcriptTurns", "transcriptCoverage", "transcriptMeta"]);
      writeEntered.resolve();
      if (cleanupBegan) signalArmedDuringCleanup?.();
    }
    return realCommit(batch);
  });
  return {
    writeEntered,
    armedDuringCleanup,
    consumed: () => consumed,
    held: () => held,
    clear() {
      held = undefined;
    },
    beginCleanup() {
      cleanupBegan = true;
    },
    disarm() {
      disarmed = true;
    },
    release() {
      held?.release();
      held = undefined;
    },
    restore() {
      spy.mockRestore();
    },
  };
}

// The shared held-release cleanup both the success scenario and the failure
// proof exercise: disarm future holds FIRST, release the current hold, unmount
// and join the real owner disposal, then restore observers and dependencies.
async function runHeldCleanup(
  hold: ReturnType<typeof armReleaseWriteHold>,
  steps: { unmount?: () => void; join: () => Promise<void>; restore: () => void },
): Promise<void> {
  hold.beginCleanup();
  hold.disarm();
  hold.release();
  try {
    steps.unmount?.();
    await steps.join();
  } finally {
    steps.restore();
  }
}

// Run the shared cleanup, converting the defect (a hold armed during teardown
// that cleanup never releases) into a clear failure: detect the orphan, release
// it so cleanup can finish, then throw, rather than hanging the suite.
async function runHeldCleanupSafely(
  hold: ReturnType<typeof armReleaseWriteHold>,
  steps: { unmount?: () => void; join: () => Promise<void>; restore: () => void },
): Promise<void> {
  const cleanup = runHeldCleanup(hold, steps);
  const outcome = await Promise.race([
    cleanup.then(() => "cleanup" as const),
    hold.armedDuringCleanup.then(() => "armed" as const),
  ]);
  if (outcome === "armed") {
    hold.release();
    await cleanup;
    throw new Error("held cleanup armed an unreleased write hold during teardown");
  }
  await cleanup;
}

// Shared bounded fixture for the AppShell held-release failure proofs: real
// entry storage, a predecessor's accepted baseline, real owner tracking and
// the real final-read checkpoint hold. Reset storage only after the following
// healthy-operation proof, so that proof cannot hide a leaked owner by reset.
async function heldReleaseFailureFixture() {
  resetThreadsStoreForTests();
  onTestFinished(() => resetThreadsStoreForTests());
  const cache = new SessionEntryCache({
    indexedDB: new IDBFactory(),
    keyRange: IDBKeyRange,
    databaseName: `appshell-bg-fail-${crypto.randomUUID()}`,
  });
  setProductionEntryCacheForTests(cache);
  await cache.getClearEpoch();
  const predecessor = versionedClient("predecessor-content");
  const owners = trackBackgroundOwners();
  const lifetime = instrumentBackgroundLifetime();
  const hold = armReleaseWriteHold(cache, lifetime);
  return { cache, predecessor, owners, lifetime, hold };
}

// The shared teardown for the AppShell failure proofs: release and disarm
// through runHeldCleanupSafely, then join real disposals and restore observers.
// `onRestore` observes that boundary before intercepts are torn down. Storage
// stays usable for the following healthy operation and closes at test finish.
function heldReleaseFailureCleanup(
  fixture: Awaited<ReturnType<typeof heldReleaseFailureFixture>>,
  getView: () => { unmount: () => void } | undefined,
  onRestore?: () => void,
) {
  return {
    unmount: () => getView()?.unmount(),
    join: () => fixture.owners.joinDisposals(),
    restore: () => {
      onRestore?.();
      stoppedTreeSession = false;
      fixture.hold.restore();
      fixture.lifetime.restore();
      fixture.owners.restore();
    },
  };
}

// A following healthy AppShell/background-owner operation on the same stores
// remains usable after a failure proof's cleanup.
async function expectHealthyBackgroundUsable() {
  const healthyOwners = trackBackgroundOwners();
  const healthyClient = backgroundReadyClient();
  let healthyView: ReturnType<typeof render> | undefined;
  try {
    healthyView = render(<AppShell client={healthyClient} />);
    await screen.findByText("No session open");
    await waitFor(() => expect(membershipReads(healthyClient)).toBeGreaterThanOrEqual(1));
    await waitFor(() => expect(productionEntryTranscriptForTests("local:s1").baseline).toBeDefined());
    healthyView.unmount();
    healthyView = undefined;
    await healthyOwners.joinDisposals();
    for (const record of healthyOwners.records) expect(record.disposeStatus).toBe("fulfilled");
  } finally {
    healthyView?.unmount();
    await healthyOwners.joinDisposals();
    healthyOwners.restore();
  }
}

// Captured at module load, before any spy can wrap the export, so the
// call-through below always reaches the real implementation.
const realStartBackgroundTranscripts = backgroundTranscriptsModule.startBackgroundTranscripts;

// Call-through interception of the real owner factory. Each record marks the
// synchronous retirement boundary (dispose() called) separately from the real
// disposal outcome (pending/fulfilled/rejected); the real promise and any
// rejection are retained rather than collapsed into a boolean, so a retired
// predecessor whose cleanup still runs is not confused with an active successor.
function trackBackgroundOwners() {
  const start = realStartBackgroundTranscripts;
  const records: Array<{
    owner: ReturnType<typeof realStartBackgroundTranscripts>;
    disposed: boolean;
    disposeCalls: number;
    disposeStatus: "pending" | "fulfilled" | "rejected";
    disposeError?: unknown;
    settledStatus: "pending" | "fulfilled" | "rejected";
    settledError?: unknown;
    disposePromise?: Promise<void>;
  }> = [];
  const spy = vi.spyOn(backgroundTranscriptsModule, "startBackgroundTranscripts").mockImplementation((navigation) => {
    const owner = start(navigation);
    const record: (typeof records)[number] = {
      owner,
      disposed: false,
      disposeCalls: 0,
      disposeStatus: "pending",
      settledStatus: "pending",
    };
    records.push(record);
    return {
      dispose() {
        record.disposeCalls += 1;
        record.disposed = true;
        const promise = owner.dispose();
        record.disposePromise = promise;
        promise.then(
          () => {
            record.disposeStatus = "fulfilled";
          },
          (error: unknown) => {
            record.disposeStatus = "rejected";
            record.disposeError = error;
          },
        );
        return promise;
      },
      settled() {
        const promise = owner.settled();
        promise.then(
          () => {
            record.settledStatus = "fulfilled";
          },
          (error: unknown) => {
            record.settledStatus = "rejected";
            record.settledError = error;
          },
        );
        return promise;
      },
    };
  });
  async function joinDisposals(): Promise<void> {
    await Promise.all(
      records.filter((record) => record.disposed && record.disposePromise).map((record) => record.disposePromise),
    );
  }
  return {
    records,
    activeCount: () => records.filter((record) => !record.disposed).length,
    retired: () => records.filter((record) => record.disposed),
    active: () => records.filter((record) => !record.disposed),
    joinDisposals,
    restore: () => spy.mockRestore(),
  };
}

test("starts one background transcript owner for the injected client and retires it on unmount", async () => {
  const client = backgroundReadyClient();
  const { rerender, unmount } = render(<AppShell client={client} />);
  await screen.findByText("No session open");
  // The Live session (local:s1) is discovered and read before any pane opens.
  await waitFor(() => expect(membershipReads(client)).toBe(1));

  // A rerender with the same client must not start a second owner.
  rerender(<AppShell client={client} />);
  await act(async () => {});
  expect(membershipReads(client)).toBe(1);

  unmount();
  await waitFor(() => expect(unsubscribedRefs(client)).toEqual(["local:s1"]));
});

test("exactly one active logical background owner across rerenders and unmount", async () => {
  const client = backgroundReadyClient();
  const owners = trackBackgroundOwners();
  let view: ReturnType<typeof render> | undefined;
  let primaryError: unknown;
  try {
    view = render(<AppShell client={client} />);
    await screen.findByText("No session open");
    await waitFor(() => expect(membershipReads(client)).toBe(1));
    // One logical owner, not merely one deduplicated wire demand.
    expect(owners.records.length).toBe(1);
    expect(owners.activeCount()).toBe(1);

    // A rerender with the same client must not start a second owner.
    view.rerender(<AppShell client={client} />);
    await act(async () => {});
    expect(owners.records.length).toBe(1);
    expect(owners.activeCount()).toBe(1);

    // Unmount retires the only owner, and its real disposal fulfills.
    view.unmount();
    view = undefined;
    await waitFor(() => expect(owners.activeCount()).toBe(0));
    expect(owners.records[0]?.disposeCalls).toBe(1);
    await owners.joinDisposals();
    expect(owners.records[0]?.disposeStatus).toBe("fulfilled");
  } catch (error) {
    primaryError = error;
  } finally {
    let cleanupError: unknown;
    try {
      view?.unmount();
      await owners.joinDisposals();
    } catch (error) {
      cleanupError = error;
    } finally {
      owners.restore();
    }
    if (cleanupError !== undefined) primaryError = combineErrors(primaryError, cleanupError);
  }
  if (primaryError !== undefined) throw primaryError;
});

test("a replacement client gets its own background owner and the old demand is retired", async () => {
  const user = userEvent.setup();
  const stale = new FakeClient("closed");
  const fresh = backgroundReadyClient();
  render(<AppShell client={stale} bannerDelayMs={0} bannerCreateClient={() => fresh} />);
  await screen.findByText("No session open");
  // The closed original never became ready, so it owns nothing.
  expect(membershipReads(stale)).toBe(0);

  await user.click(await screen.findByRole("button", { name: "Retry" }));
  await waitFor(() => expect(connectionStore.getState().client).toBe(fresh));
  await waitFor(() => expect(membershipReads(fresh)).toBeGreaterThan(0));
  expect(unsubscribedRefs(stale)).toEqual([]);
});

test.each(["desktop", "narrow"])("one background owner per client lifetime in the %s layout", async (layout) => {
  if (layout === "narrow") {
    vi.stubGlobal("innerWidth", 390);
    installMobileViewport();
  }
  const owners = trackBackgroundOwners();
  let view: ReturnType<typeof render> | undefined;
  let primaryError: unknown;
  try {
    const client = backgroundReadyClient();
    view = render(<AppShell client={client} />);
    await waitFor(() => expect(membershipReads(client)).toBe(1));
    // One logical owner, not merely one deduplicated wire demand.
    expect(owners.records.length).toBe(1);
    expect(owners.activeCount()).toBe(1);
  } catch (error) {
    primaryError = error;
  } finally {
    let cleanupError: unknown;
    try {
      view?.unmount();
      await owners.joinDisposals();
    } catch (error) {
      cleanupError = error;
    } finally {
      owners.restore();
    }
    if (cleanupError !== undefined) primaryError = combineErrors(primaryError, cleanupError);
  }
  if (primaryError !== undefined) throw primaryError;
});

test("a client replacement holds the predecessor's real release cleanup and keeps a useful successor", async () => {
  const user = userEvent.setup();
  resetThreadsStoreForTests();
  onTestFinished(() => resetThreadsStoreForTests());
  // A real open cache adapter: the predecessor owner's real final release
  // drains a real cache write we hold inside the release.
  const cache = new SessionEntryCache({
    indexedDB: new IDBFactory(),
    keyRange: IDBKeyRange,
    databaseName: `appshell-bg-${crypto.randomUUID()}`,
  });
  setProductionEntryCacheForTests(cache);
  await cache.getClearEpoch(); // open the real entry storage before arming
  const predecessor = versionedClient("predecessor-content");
  const fresh = versionedClient("successor-content", 2);
  const owners = trackBackgroundOwners();
  const lifetime = instrumentBackgroundLifetime();

  // Arm the completion hold at the real cache-flush entry and only after the
  // real release has entered, so the held write is attributable to the
  // predecessor's final-release flush (not merely the next cache write).
  const hold = armReleaseWriteHold(cache, lifetime);

  let view: ReturnType<typeof render> | undefined;
  let primaryError: unknown;
  try {
    lifetime.arm();
    view = render(<AppShell client={predecessor} bannerDelayMs={0} bannerCreateClient={() => fresh} />);
    await screen.findByText("No session open");
    // The predecessor owner is active and its resident model has versioned,
    // valid content, so its release drains a real cache write.
    await waitFor(() => expect(membershipReads(predecessor)).toBeGreaterThanOrEqual(1));
    await waitFor(() => expect(threadsStore.getState().threads.get("local:s1")?.history?.incarnation).toBeDefined());
    await waitFor(() => expect(productionEntryTranscriptForTests("local:s1").baseline).toBeDefined());
    expect(owners.activeCount()).toBe(1);
    const predecessorRecord = owners.records[0];
    if (!predecessorRecord) throw new Error("no predecessor owner was tracked");

    // Enter the predecessor owner's real final release while the client is
    // still ready and the model valid: the real release runs its synchronous
    // retirement and its real cache flush, held at the completion boundary.
    let predecessorDisposal!: Promise<void>;
    await act(async () => {
      predecessorDisposal = predecessorRecord.owner.dispose();
    });
    await waitFor(() => expect(hold.held()).toBeDefined());
    const write = hold.held();
    if (!write) throw new Error("predecessor release never entered a real cache flush");
    expect(lifetime.releaseEntered()).toBe(true);
    await write.reached; // transaction committed, its completion delivery held
    expect(predecessorRecord.disposed).toBe(false);

    // Replace the client through the real banner-retry path while the old
    // release cleanup is still held; AppShell's cleanup disposes the same owner
    // again (idempotent) and starts the successor owner.
    await act(async () => {
      predecessor.emitStateChange("closed");
    });
    await user.click(await screen.findByRole("button", { name: "Retry" }));
    await waitFor(() => expect(connectionStore.getState().client).toBe(fresh));

    // A useful successor: a real acquisition of the target ref while held.
    await waitFor(() => expect(lifetime.acquisitionsAfterReleaseEntry("local:s1")).toBeGreaterThanOrEqual(1));
    expect(owners.activeCount()).toBe(1);

    // Distinct successor content from the successor's own real client read: a
    // ready transition refreshes the now-owned ref from the fresh snapshot.
    await act(async () => {
      fresh.emitStateChange("reconnecting");
      fresh.emitReady({
        serverInfo: { name: "fake", version: "1" },
        protocolVersion: "evener-appwire-v6",
        sourceId: "fake",
        features: {} as never,
        navigation: { version: 1, generationId: "generation_test", sequence: 0, readVersions: [3] },
      });
    });
    // Queued successor bytes may already exist from its initial read. Capture
    // only after this fresh-ready resync has actually published its own cut.
    const lastReadParams = () =>
      fresh.calls.filter((call) => call.method === "thread/read").at(-1)?.params as
        | { requestGeneration?: number }
        | undefined;
    await waitFor(() => expect(typeof lastReadParams()?.requestGeneration).toBe("number"));
    const refreshGeneration = lastReadParams()?.requestGeneration;
    await waitFor(() =>
      expect(threadsStore.getState().threads.get("local:s1")?.history).toMatchObject({
        issuedGeneration: refreshGeneration,
        appliedGeneration: refreshGeneration,
      }),
    );
    const historyKey = productionEntryTranscriptForTests("local:s1").historyKey;
    if (!historyKey) throw new Error("successor has no resolved entry history identity");
    await waitFor(() =>
      expect(
        JSON.stringify(productionEntryTranscriptForTests("local:s1").repository?.retainedInventoryForTesting()),
      ).toContain("successor-content"),
    );
    // R36: the accepted ordinary background-only contract excludes bodies from
    // the store model (threads.entryProduction.test.ts:653-656). The old
    // release is still held, so accepted successor bytes belong to its real
    // repository's bounded write inventory until that checkpoint can complete.
    expect(threadsHistory("local:s1")).not.toContain("successor-content");
    const contentWhileHeld = threadsHistory("local:s1");
    const freshReadsWhileHeld = membershipReads(fresh);
    const freshUnsubsWhileHeld = unsubscribedRefs(fresh);

    // Release the real write and await the predecessor's real release
    // completion explicitly (its settled() joins the in-flight release work).
    expect(lifetime.acquisitionsAfterReleaseEntry("local:s1")).toBeGreaterThanOrEqual(1);
    await act(async () => {
      write.release();
      hold.clear();
      await predecessorDisposal;
    });

    // The old cleanup neither retired the successor nor removed its content or
    // demand.
    expect(threadsHistory("local:s1")).toBe(contentWhileHeld);
    expect(threadsHistory("local:s1")).not.toContain("successor-content");
    await threadsModule.settleEntryTranscriptForTests("local:s1");
    expect(JSON.stringify(await cache.getLatest(historyKey, 40))).toContain("successor-content");
    expect(membershipReads(fresh)).toBe(freshReadsWhileHeld);
    expect(unsubscribedRefs(fresh)).toEqual(freshUnsubsWhileHeld);
    expect(owners.activeCount()).toBe(1);
  } catch (error) {
    primaryError = error;
  } finally {
    let cleanupError: unknown;
    try {
      await runHeldCleanupSafely(hold, {
        unmount: () => view?.unmount(),
        join: () => owners.joinDisposals(),
        restore: () => {
          stoppedTreeSession = false;
          hold.restore();
          lifetime.restore();
          owners.restore();
          cache.close();
        },
      });
    } catch (error) {
      cleanupError = error;
    }
    if (cleanupError !== undefined) primaryError = combineErrors(primaryError, cleanupError);
  }
  if (primaryError !== undefined) throw primaryError;
});

test("an early failure before the predecessor release write joins real cleanup without an orphan hold", async () => {
  const fixture = await heldReleaseFailureFixture();
  const { predecessor, owners, lifetime, hold } = fixture;

  let view: ReturnType<typeof render> | undefined;
  let primaryError: unknown;
  try {
    lifetime.arm();
    view = render(<AppShell client={predecessor} />);
    await screen.findByText("No session open");
    await waitFor(() => expect(membershipReads(predecessor)).toBeGreaterThanOrEqual(1));
    await waitFor(() => expect(threadsStore.getState().threads.get("local:s1")?.history?.incarnation).toBeDefined());
    await waitFor(() => expect(productionEntryTranscriptForTests("local:s1").baseline).toBeDefined());
    expect(owners.activeCount()).toBe(1);
    // A valid resident model exists and no release write has been held yet, so
    // the injected failure lands BEFORE the first hold is consumed.
    expect(hold.consumed()).toBe(false);
    throw new Error("injected early failure before the first release write");
  } catch (error) {
    primaryError = error;
  } finally {
    let cleanupError: unknown;
    try {
      await runHeldCleanupSafely(
        hold,
        heldReleaseFailureCleanup(fixture, () => view),
      );
    } catch (error) {
      cleanupError = error;
    }
    if (cleanupError !== undefined) primaryError = combineErrors(primaryError, cleanupError);
  }

  // The injected failure survives cleanup unchanged, and cleanup created no
  // unreleased hold (otherwise runHeldCleanupSafely would have thrown above).
  expect(primaryError).toBeInstanceOf(Error);
  expect((primaryError as Error).message).toContain("injected early failure before the first release write");
  expect(hold.consumed()).toBe(false);
  // The real owner disposal was joined, not abandoned.
  expect(owners.activeCount()).toBe(0);
  for (const record of owners.records) expect(record.disposeStatus).toBe("fulfilled");

  // A following healthy operation on the same stores remains usable.
  await expectHealthyBackgroundUsable();
});

test("a failure after the predecessor release entry joins the held predecessor before restoration", async () => {
  const fixture = await heldReleaseFailureFixture();
  const { predecessor, owners, lifetime, hold } = fixture;

  let view: ReturnType<typeof render> | undefined;
  let primaryError: unknown;
  let disposeFulfilledAtRestore = false;
  try {
    lifetime.arm();
    view = render(<AppShell client={predecessor} />);
    await screen.findByText("No session open");
    await waitFor(() => expect(membershipReads(predecessor)).toBeGreaterThanOrEqual(1));
    await waitFor(() => expect(threadsStore.getState().threads.get("local:s1")?.history?.incarnation).toBeDefined());
    await waitFor(() => expect(productionEntryTranscriptForTests("local:s1").baseline).toBeDefined());
    expect(owners.activeCount()).toBe(1);
    const predecessorRecord = owners.records[0];
    if (!predecessorRecord) throw new Error("no predecessor owner was tracked");

    // Enter the real predecessor final release while the client is still ready
    // and the model valid: its real cache flush is held at the completion
    // boundary (the real IDB transaction committed, delivery held).
    await act(async () => {
      predecessorRecord.owner.dispose();
    });
    await waitFor(() => expect(hold.held()).toBeDefined());
    const write = hold.held();
    if (!write) throw new Error("predecessor release never entered a real cache flush");
    expect(lifetime.releaseEntered()).toBe(true);
    await write.reached;
    expect(hold.consumed()).toBe(true);
    // Inject the failure AFTER the real hold entry, leaving the held write
    // unreleased so cleanup -- not the body -- must release and join it.
    throw new Error("injected failure after the held release entry");
  } catch (error) {
    primaryError = error;
  } finally {
    let cleanupError: unknown;
    try {
      await runHeldCleanupSafely(
        hold,
        heldReleaseFailureCleanup(
          fixture,
          () => view,
          () => {
            disposeFulfilledAtRestore = owners.records[0]?.disposeStatus === "fulfilled";
          },
        ),
      );
    } catch (error) {
      cleanupError = error;
    }
    if (cleanupError !== undefined) primaryError = combineErrors(primaryError, cleanupError);
  }

  // The injected failure survives cleanup, and the held predecessor was
  // released and joined before observers and the cache adapter were restored.
  expect(primaryError).toBeInstanceOf(Error);
  expect((primaryError as Error).message).toContain("injected failure after the held release entry");
  expect(hold.consumed()).toBe(true);
  expect(disposeFulfilledAtRestore).toBe(true);
  expect(owners.records[0]?.disposed).toBe(true);
  expect(owners.records[0]?.disposeStatus).toBe("fulfilled");

  await expectHealthyBackgroundUsable();
});

test("StrictMode mount and cleanup/remount retire their predecessor and complete disposal", async () => {
  const client = backgroundReadyClient();
  const owners = trackBackgroundOwners();
  let strictView: ReturnType<typeof render> | undefined;
  let againView: ReturnType<typeof render> | undefined;
  let primaryError: unknown;
  try {
    strictView = render(
      <StrictMode>
        <AppShell client={client} />
      </StrictMode>,
    );
    await waitFor(() => expect(membershipReads(client)).toBeGreaterThan(0));
    // StrictMode's double-invoked mount retires a predecessor synchronously and
    // leaves exactly one active replacement; join the retired predecessor's real
    // disposal fulfilment before unmount.
    const retired = owners.retired();
    expect(retired.length).toBeGreaterThanOrEqual(1);
    expect(owners.activeCount()).toBe(1);
    await waitFor(() => expect(retired.every((record) => record.disposeStatus !== "pending")).toBe(true));
    for (const record of retired) expect(record.disposeStatus).toBe("fulfilled");

    strictView.unmount();
    strictView = undefined;
    // Every acquired membership is retired: a leaked owner would leave one open.
    await waitFor(() => expect(unsubscribedRefs(client).length).toBe(membershipReads(client)));
    await owners.joinDisposals();
    for (const record of owners.records) expect(record.disposeStatus).toBe("fulfilled");

    // A clean remount of the same client: one owner at a time, none leaked.
    againView = render(<AppShell client={client} />);
    await waitFor(() => expect(membershipReads(client)).toBeGreaterThan(unsubscribedRefs(client).length));
    await waitFor(() => expect(owners.activeCount()).toBe(1));
    againView.unmount();
    againView = undefined;
    await waitFor(() => expect(unsubscribedRefs(client).length).toBe(membershipReads(client)));
    await owners.joinDisposals();
    for (const record of owners.records) expect(record.disposeStatus).toBe("fulfilled");
  } catch (error) {
    primaryError = error;
  } finally {
    let cleanupError: unknown;
    try {
      strictView?.unmount();
      againView?.unmount();
      await owners.joinDisposals();
    } catch (error) {
      cleanupError = error;
    } finally {
      owners.restore();
    }
    if (cleanupError !== undefined) primaryError = combineErrors(primaryError, cleanupError);
  }
  if (primaryError !== undefined) throw primaryError;
});

test("banner reflects reconnecting state when injected", async () => {
  const fake = new FakeClient("ready");
  // bannerDelayMs={0} drives the banner synchronously so the test doesn't
  // wait the production 10s reveal delay (see ConnectionBannerProps.delayMs).
  render(<AppShell client={fake} bannerDelayMs={0} />);
  await screen.findByText("No session open");

  act(() => {
    fake.emitStateChange("reconnecting");
  });

  expect(await screen.findByText(/reconnecting to the server/i)).toBeTruthy();
});

test("populates connectionStore metadata from one injected initialize response", async () => {
  const fake = new FakeClient("connecting");
  const scripted: InitializeResponse = {
    serverInfo: { name: "evener-hub-test", version: "9.9.9" },
    protocolVersion: "1",
    sourceId: "src_test",
    features: ALL_FEATURES_OFF,
  };
  fake.scriptConnect(() => {
    fake.emitReady(scripted);
    return scripted;
  });

  render(<AppShell client={fake} />);
  await screen.findByText("No session open");

  await waitFor(() => {
    expect(connectionStore.getState().serverInfo).toEqual({ name: "evener-hub-test", version: "9.9.9" });
    expect(connectionStore.getState().features).toEqual(scripted.features);
  });
});

test("a late boot connect result cannot overwrite automatic reconnect metadata", async () => {
  const fake = new FakeClient("connecting");
  const initial: InitializeResponse = {
    serverInfo: { name: "initial-hub", version: "1.0.0" },
    protocolVersion: "1",
    sourceId: "initial-source",
    features: ALL_FEATURES_OFF,
  };
  const reconnected = { ...initial, serverInfo: { name: "reconnected-hub", version: "2.0.0" } };
  const pending = deferred<InitializeResponse>();
  fake.scriptConnect(() => {
    fake.emitReady(initial);
    return pending.promise;
  });
  render(<AppShell client={fake} />);
  await waitFor(() => expect(connectionStore.getState().serverInfo).toEqual(initial.serverInfo));

  act(() => {
    fake.emitStateChange("reconnecting");
    fake.emitReady(reconnected);
  });
  await act(async () => {
    pending.resolve(initial);
    await pending.promise;
  });

  expect(connectionStore.getState().serverInfo).toEqual(reconnected.serverInfo);
  expect(connectionStore.getState().features).toEqual(reconnected.features);
});

test("closes the client it constructed itself on unmount", async () => {
  // Supersession, Jesse 2026-10-14: the old same-tick close assertion pinned
  // the defect. Require the disposal's final read to settle before one close.
  resetThreadsStoreForTests();
  const cache = new SessionEntryCache({
    indexedDB: new IDBFactory(),
    keyRange: IDBKeyRange,
    databaseName: `appshell-owned-final-${crypto.randomUUID()}`,
  });
  setProductionEntryCacheForTests(cache);
  await cache.getClearEpoch();
  const finalRead = deferred<ReturnType<typeof currentThreadRead>>();
  const issued = deferred<void>();
  const transport = backgroundReadyClient();
  await transport.connect();
  expect(transport.initializeResult?.navigation?.generationId).toBe("generation_test");
  let final = false;
  transport.on("thread/read", (params) => {
    if (!final) return currentThreadRead(params);
    issued.resolve();
    return finalRead.promise.then((response) => ({ ...response, requestGeneration: params.requestGeneration }));
  });
  // Only the transport is scripted. Discovery, leases, repository retirement,
  // cache writes and background disposal are the production implementations.
  const state = vi.spyOn(AppwireClient.prototype, "state", "get").mockReturnValue("ready");
  const initialize = vi
    .spyOn(AppwireClient.prototype, "initializeResult", "get")
    .mockReturnValue(transport.initializeResult);
  const ready = vi.spyOn(AppwireClient.prototype, "onReady").mockImplementation(transport.onReady.bind(transport));
  const changes = vi
    .spyOn(AppwireClient.prototype, "onStateChange")
    .mockImplementation(transport.onStateChange.bind(transport));
  const notifications = vi
    .spyOn(AppwireClient.prototype, "onNotification")
    .mockImplementation(transport.onNotification.bind(transport));
  const request = vi.spyOn(AppwireClient.prototype, "request").mockImplementation(transport.request.bind(transport));
  const closeSpy = vi.spyOn(AppwireClient.prototype, "close");
  const owners = trackBackgroundOwners();
  const { unmount } = render(<AppShell />);
  const wired = connectionStore.getState().client;
  try {
    await act(async () => {
      await transport.connect();
    });
    // Owned connect is deliberately disabled in test mode. Initialize the
    // real navigation binding with the scripted transport's negotiated v3.
    act(() => {
      resetNavigationStoreForTests();
      initNavigation(wired!, { version: 1, generationId: "generation_test", sequence: 0, readVersions: [3] });
    });
    await waitFor(() => expect(productionEntryTranscriptForTests("local:s1").baseline).toBeDefined());
    await owners.records[0]!.owner.settled();
    final = true;
    const boundary = request.mock.calls.length;
    unmount();
    await act(async () => {
      await issued.promise;
    });
    const finalIndex = request.mock.calls.findIndex(([method], index) => index >= boundary && method === "thread/read");
    expect(finalIndex).toBeGreaterThanOrEqual(boundary);
    expect(request.mock.contexts[finalIndex]).toBe(wired);
    expect(request.mock.calls[finalIndex]?.[1]).toMatchObject({ ref: "local:s1", includeTurns: true });
    expect(closeSpy).not.toHaveBeenCalled();
    await act(async () => {
      finalRead.resolve(currentThreadRead({ ref: "local:s1" }));
      await owners.joinDisposals();
    });
    expect(closeSpy).toHaveBeenCalledTimes(1);
    expect(closeSpy.mock.contexts[0]).toBe(wired);
  } finally {
    finalRead.resolve(currentThreadRead({ ref: "local:s1" }));
    unmount();
    await owners.joinDisposals();
    owners.restore();
    request.mockRestore();
    closeSpy.mockRestore();
    initialize.mockRestore();
    state.mockRestore();
    ready.mockRestore();
    changes.mockRestore();
    notifications.mockRestore();
    resetThreadsStoreForTests();
    cache.close();
  }
});

test.each([false, true])("bounds stuck disposal without closing injected client=%s", async (injected) => {
  const stuck = deferred<void>();
  const start = vi.spyOn(backgroundTranscriptsModule, "startBackgroundTranscripts").mockReturnValue({
    settled: async () => {},
    dispose: () => stuck.promise,
  });
  const close = vi.spyOn(AppwireClient.prototype, "close");
  const client = new FakeClient("ready");
  const injectedClose = vi.spyOn(client, "close");
  const view = render(<AppShell client={injected ? client : undefined} />);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  try {
    view.unmount();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(29_999);
    });
    expect(close).not.toHaveBeenCalled();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });
    expect(close).toHaveBeenCalledTimes(injected ? 0 : 1);
    expect(injectedClose).not.toHaveBeenCalled();
    await act(async () => {
      stuck.resolve();
      await stuck.promise;
    });
    expect(close).toHaveBeenCalledTimes(injected ? 0 : 1);
    expect(injectedClose).not.toHaveBeenCalled();
  } finally {
    stuck.resolve();
    vi.useRealTimers();
    start.mockRestore();
    close.mockRestore();
    injectedClose.mockRestore();
  }
});
