import type { NavigationReadParams, NavigationReadResponse, NavigationSessionLocation } from "@evener/appwire-client";
import { APPWIRE_PROTOCOL_VERSION, type ConnectionState } from "@evener/appwire-client";
import { keyID } from "@evener/appwire-client/state/navigation";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Suspense } from "react";
import { afterEach, beforeAll, beforeEach, expect, onTestFinished, test, vi } from "vitest";
import { initNotifications, resetNotificationsForTests } from "../notifications";
import { installJobLogRangeGeometry } from "../panes/transcript/JobLogTestUtils";
import { retainedJobLogReader } from "../panes/transcript/jobLogReader";
import { StubResizeObserver } from "../resizeObserverTestUtils";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import { resetAuthStatusesStoreForTests } from "../stores/authStatuses";
import { connectionStore } from "../stores/connection";
import { navigationStore, resetNavigationStoreForTests } from "../stores/navigation/store";
import { resetPrefsStoreForTests } from "../stores/prefs";
import { sessionActivitySnapshot, settleSessionActivityForTests } from "../stores/sessionActivity";
import { activityContext, activityDelegate, answerActivityRead } from "../stores/sessionActivityTestUtils";
import { resetSettingsHostForTests, settingsHostStore } from "../stores/settingsHost";
import { resetSettingsOverviewStoreForTests } from "../stores/settingsOverview";
import { threadsStore } from "../stores/threads";
import { resetDisclosureStoreForTests } from "../widgets/disclosure/disclosureStore";
import { AppShell } from "./AppShell";
import {
  activitySidebarScopeRef,
  activitySidebarStore,
  resetActivitySidebarStoreForTests,
} from "./activitybar/activitySidebarStore";
import { rail, threadStartResponse } from "./appShellTestUtils";
import { paletteStore } from "./palette/paletteController";
import { conversationPaneLifetime } from "./paneLifetime";
import { paneFor } from "./paneRegistry";
import { navigate, refParam } from "./routing";
import { getDockviewApi, type OpenPaneRecord, resetWorkspaceStoreForTests, workspaceStore } from "./workspace";

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

  // The drill opens two contextual panes whose components are React.lazy: the
  // read-only job transcript and the sessionZoom cascade. Pay each chunk's
  // first render here, in an awaited act after its module is already cached,
  // so the drill's own assertions never race a lazy reveal under load.
  await import("../panes/transcript/Transcript");
  await import("../panes/zoom/Zoom");
  for (const id of ["transcript", "sessionZoom"] as const) {
    const Pane = paneFor(id).component;
    await act(async () => {
      render(
        <Suspense fallback={<span data-testid={`warm-fallback-${id}`} />}>
          <Pane params={{ ref: "local:pane_warm" }} paneId={`warm-${id}`} focused={false} />
        </Suspense>,
      );
    });
    // The act must have committed the reveal: a fallback still present means
    // the payload never initialized and the drill would pay it again.
    expect(screen.queryByTestId(`warm-fallback-${id}`)).toBeNull();
    cleanup();
  }

  // Then render each pane once, for the React.lazy half of the cost - see
  // warmRoute above. Awaiting real completion, in a hook whose ceiling is a
  // tripwire, rather than spending it inside a test's assertion window.
  await warmRoute("/", (wait) => screen.findByText("No session open", undefined, wait));
  await warmRoute("/new", (wait) => screen.findByRole("button", { name: "Start" }, wait));
  await warmRoute("/s/local:ref_warm", (wait) => screen.findByText(/loading transcript/i, undefined, wait));
  await warmRoute("/settings/hub", (wait) => screen.findByRole("navigation", { name: "Settings sections" }, wait));
});

beforeEach(async () => {
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
  // The drill opens the activity sidebar and toggles its inactive fold, both
  // module singletons. A case cut off by the test timeout never reaches the
  // journey's own finally, so every case starts from closed and collapsed
  // rather than inheriting the previous case's open scope or disclosure.
  resetActivitySidebarStoreForTests();
  resetDisclosureStoreForTests();
  if (!conversationWarmed) {
    conversationWarmed = true;
    await warmConversationOnce();
  }
}, 60_000);

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

// M1 (round 6): the settings selection must be synchronized with the route at the
// popstate itself, before React mounts the settings pane. Otherwise a Back/Forward
// into a host-scoped settings URL mounts that pane on the previous (local)
// selection and renders - and reads - for a host the route does not name.
test("a popstate into a host-scoped settings URL selects the host before the pane mounts", async () => {
  window.history.pushState({}, "", "/");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");
  expect(settingsHostStore.getState().host).toBe("local");

  // Read the selection during the popstate dispatch: after the shell's own route
  // listener has run, but before React can mount the settings pane.
  let hostAtDispatch: string | null = null;
  const probe = () => {
    hostAtDispatch = settingsHostStore.getState().host;
  };
  window.addEventListener("popstate", probe);
  try {
    act(() => {
      window.history.pushState({}, "", "/settings/credentials?host=beta");
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
  } finally {
    window.removeEventListener("popstate", probe);
  }

  expect(hostAtDispatch).toBe("beta");
  // The pane mounts for beta and settles on the registry's answer (this client
  // scripts none, so the read fails) before the test ends.
  expect(await screen.findByText("Couldn't check beta's registration")).toBeTruthy();
});

let conversationWarmed = false;

// Render one live session conversation once per file, before the drill cases:
// the drill's first cascade mount otherwise pays the process's first
// ConversationBody, composer and transcript-list mount - one-time component
// initialization - inside whichever case runs first, which under load crosses
// the default per-test budget with no bad wait at all (#4187). A hook keeps it
// out of every test's measured window, and still runs when a -t filter selects
// only the drill cases (the filtered reproduction of this flake).
async function warmConversationOnce(): Promise<void> {
  const rangeGeometry = installJobLogRangeGeometry();
  const height = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, get: () => 500 });
  vi.stubGlobal("innerWidth", 1280);
  const client = navClient();
  client.on("thread/read", ({ ref, requestGeneration }) => {
    if (!ref) throw new Error("warm conversation read requires a session ref");
    return {
      requestGeneration,
      thread: {
        ...threadStartResponse(ref).thread,
        name: ref,
        turns: [
          {
            id: "warm",
            status: "completed",
            itemsView: "full",
            items: [
              {
                id: "warm-report",
                turnId: "warm",
                type: "agentMessage",
                text: `report-${ref}`,
                status: "completed",
              },
            ],
          },
        ],
      },
    };
  });
  client.on("evener/thread/activity/read", (params) => ({
    ...answerActivityRead(params),
    context: activityContext(params.ref),
  }));
  client.on("evener/thread/delegates/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    delegates: [],
    page: { complete: true, issues: [] },
  }));
  client.on("evener/thread/jobs/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    jobs: [],
    page: { complete: true, issues: [] },
  }));
  window.history.pushState({}, "", "/s/local:warm_case");
  installLocationForRoute("local:warm_case");
  const view = render(<AppShell client={client} />);
  try {
    await act(async () => {
      await threadsStore.getState().ensureThread("local:warm_case");
    });
    // This is a readiness tripwire, not an assertion window: the case that
    // pays the mount owns a 60 s hook ceiling rather than the 5 s test budget.
    await screen.findByText("report-local:warm_case", undefined, { timeout: 30_000 });
  } finally {
    view.unmount();
    threadsStore.getState().releaseThread("local:warm_case");
    if (height) Object.defineProperty(HTMLElement.prototype, "offsetHeight", height);
    else Reflect.deleteProperty(HTMLElement.prototype, "offsetHeight");
    rangeGeometry();
    vi.unstubAllGlobals();
    resetWorkspaceStoreForTests();
    resetNavigationStoreForTests();
    connectionStore.setState({ client: null, state: "idle" });
    localStorage.clear();
    window.history.pushState({}, "", "/");
  }
}

function installConversationGeometry(): void {
  // The layout affordances the drill cases install: dockview mounts pane
  // content only when its panels have a non-zero box.
  onTestFinished(installJobLogRangeGeometry());
  const height = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, get: () => 500 });
  onTestFinished(() => {
    if (height) Object.defineProperty(HTMLElement.prototype, "offsetHeight", height);
    else Reflect.deleteProperty(HTMLElement.prototype, "offsetHeight");
  });
  vi.stubGlobal("innerWidth", 1280);
}

test.each([
  { retained: false, source: "main" },
  { retained: true, source: "main" },
  { retained: false, source: "nested" },
])(
  "desktop Activity drill keeps grandchild focus from $source with retained ref-only observer=$retained",
  async ({ retained, source }) => {
    installConversationGeometry();
    const routeRef = source === "nested" ? "local:child" : "local:owner";
    window.history.pushState({}, "", `/s/${routeRef}`);
    installLocationForRoute(routeRef);
    const client = navClient();
    if (source === "nested")
      client.on("evener/navigation/read", (params) =>
        params.resource === "location" && params.ref === "local:child"
          ? wireSnapshot(
              params,
              {
                ref: "local:child",
                top_level_ref: "local:owner",
                top_level: false,
                session: { ...TREE_SESSION, ref: "local:child", session_id: "sess_local:child", kind: "subagent" },
              },
              '"nested"',
            )
          : navigationRead(params),
      );
    const context = (ref: string) => ({
      ...activityContext(ref),
      sessionId: `sess_${ref}`,
      rootRef: "local:owner",
      ...(ref !== "local:owner" ? { parentRef: ref === "local:child" ? "local:owner" : "local:child" } : {}),
      ancestors:
        ref === "local:owner"
          ? []
          : [
              { ref: "local:owner", sessionId: "sess_local:owner", title: "Root" },
              ...(ref === "local:grandchild"
                ? [{ ref: "local:child", sessionId: "sess_local:child", title: "Observer" }]
                : []),
            ],
    });
    client.on("thread/read", ({ ref, includeTurns, requestGeneration }) => {
      if (!ref) throw new Error("thread ref required");
      return {
        requestGeneration,
        thread: {
          ...threadStartResponse(ref).thread,
          name: ref,
          turns:
            includeTurns === false
              ? []
              : [
                  {
                    id: "settled",
                    status: "completed",
                    itemsView: "full",
                    items: [
                      {
                        id: "report",
                        turnId: "settled",
                        type: "agentMessage",
                        text: `report-${ref}`,
                        status: "completed",
                      },
                    ],
                  },
                ],
        },
      };
    });
    client.on("evener/thread/activity/read", (params) => ({
      ...answerActivityRead(params),
      context: context(params.ref),
    }));
    client.on("evener/thread/delegates/list", ({ ref }) => ({
      context: context(ref),
      scope: "session",
      delegates:
        ref === "local:grandchild"
          ? []
          : [
              activityDelegate({
                ownerRef: ref,
                rootRef: "local:owner",
                childRef: ref === "local:owner" ? "local:child" : "local:grandchild",
                name: ref === "local:owner" ? "observer" : "grandchild",
                terminal: true,
                lifecycle: "idle",
                phase: "done",
                status: "completed",
              }),
            ],
      page: { complete: true, issues: [] },
    }));
    client.on("evener/thread/jobs/list", ({ ref }) => ({
      context: context(ref),
      scope: "session",
      jobs: [],
      page: { complete: true, issues: [] },
    }));
    client.on("evener/jobs/get", () => {
      throw new Error("retained metadata unavailable");
    });
    client.on("evener/jobs/output", () => ({
      data: {
        offsetBytes: 0,
        bytesReturned: 23,
        totalBytes: 23,
        retainedStartBytes: 0,
        encoding: "utf8",
        data: "retained-history-output",
      },
    }));
    let historyPane: string | undefined;
    // #4187: this journey flaked under heavy load and the failing assertion was
    // never captured, so a recurrence must name the state it saw: which pane
    // held focus and the transitions that moved it, what the retained observer
    // pane and the source conversation held, and the route, workspace and
    // rendered cascade every assertion reads. `describeFailure` starts as a
    // placeholder and is bound to the real report once the journey's identities
    // exist; the catch below appends it to whatever fails - an assertion or a
    // waiting find - so a red run reports its state instead of only
    // "expected 'pane_x' to be 'pane_y'".
    let describeFailure = (): string => "the drill failed before it captured its state";
    let step = "start";
    // A pane's identity for a failure report: id, type, slot and ref, so the
    // node that held focus is named, not just its opaque id. Defined before the
    // recorder so a transition can name the pane on each side of the move.
    const describePane = (pane: OpenPaneRecord | undefined): string =>
      pane === undefined ? "none" : `${pane.id}(${pane.type},${pane.slot},ref=${refParam(pane.params) ?? "-"})`;
    const describeFocused = (panes: readonly OpenPaneRecord[], id: string | null): string => {
      if (id === null) return "none";
      const pane = panes.find((candidate) => candidate.id === id);
      // A pane that is already gone still names itself, so a focus move onto a
      // closed pane is not reported as a bare "none".
      return pane === undefined ? `${id}(gone)` : describePane(pane);
    };
    const focusHistory: string[] = [];
    onTestFinished(
      workspaceStore.subscribe((state, previous) => {
        if (state.focusedPaneId !== previous.focusedPaneId)
          focusHistory.push(
            `${describeFocused(previous.panes, previous.focusedPaneId)}->${describeFocused(state.panes, state.focusedPaneId)}`,
          );
      }),
    );
    try {
      step = "initial render";
      render(<AppShell client={client} />);
      await act(async () => {
        await threadsStore.getState().ensureThread("local:owner");
      });
      onTestFinished(() => threadsStore.getState().releaseThread("local:owner"));
      expect(screen.getByRole("heading", { name: "local:owner" })).toBeTruthy();
      expect(screen.getByText("report-local:owner")).toBeTruthy();
      expect(getDockviewApi()).not.toBeNull();
      if (source === "nested") {
        await act(async () => {
          await threadsStore.getState().ensureThread("local:child");
        });
        onTestFinished(() => threadsStore.getState().releaseThread("local:child"));
        expect(screen.getByRole("heading", { name: "local:child" })).toBeTruthy();
      }
      step = "history pane";
      const sourceId =
        source === "transcript"
          ? await act(async () =>
              workspaceStore
                .getState()
                .openPane("transcript", { ref: "local:owner", parentRef: "local:owner" }, { slot: "secondary" }),
            )
          : workspaceStore.getState().focusedPaneId;
      const sourcePane = () => workspaceStore.getState().panes.find((pane) => pane.id === sourceId);
      const original = sourcePane();
      if (!original) throw new Error("Missing original Activity source");
      const lifetime = conversationPaneLifetime(original);
      let inspectorId = "";
      const inspectorPane = () => workspaceStore.getState().panes.find((pane) => pane.id === inspectorId);
      // The retained ref-only observer pane this journey drills past: an
      // ordinary session pane for the child, beside the cascade.
      const isRefOnlyChild = (pane: OpenPaneRecord): boolean =>
        pane.type === "session" && pane.id !== sourceId && refParam(pane.params) === "local:child";
      const renderedScopeRefs = (testId: string): string =>
        screen
          .queryAllByTestId(testId)
          .map((node) => node.getAttribute("data-scope-ref") ?? "-")
          .join(" ");
      const drillState = (): string => {
        const workspace = workspaceStore.getState();
        const sidebarNode = (): HTMLElement | null =>
          document.querySelector<HTMLElement>('[data-testid="activity-sidebar"]');
        const sidebarCrumbs = (): string => {
          const nav = sidebarNode()?.querySelector('nav[aria-label="Scope"]');
          if (!nav) return "none";
          const ancestors = Array.from(nav.querySelectorAll<HTMLElement>("[data-session-navigation-ref]")).map(
            (node) => node.dataset.sessionNavigationRef ?? "?",
          );
          const leaf = nav.querySelector<HTMLElement>('[aria-current="page"]')?.textContent ?? "-";
          return [...ancestors, leaf].join(">");
        };
        const sidebarRows = (): string => {
          const node = sidebarNode();
          if (!node) return "none";
          return Array.from(node.querySelectorAll<HTMLElement>("[data-activity-anchor]"))
            .map((row) => row.dataset.activityAnchor ?? "?")
            .join(" ");
        };
        return [
          `step=${step}`,
          `path=${window.location.pathname}`,
          `focus=${describeFocused(workspace.panes, workspace.focusedPaneId)}`,
          `dockviewActive=${getDockviewApi()?.activePanel?.id ?? "none"}`,
          `inspector=${inspectorId === "" ? "unset" : describeFocused(workspace.panes, inspectorId)}`,
          `source=${describeFocused(workspace.panes, sourceId)}`,
          `sourceIsOriginal=${String(sourcePane() === original)}`,
          `sourceLifetimeAlive=${String(conversationPaneLifetime(original).alive)}`,
          `retainedObserver=${describePane(workspace.panes.find(isRefOnlyChild))}`,
          `sidebarRef=${activitySidebarScopeRef() ?? "null"}`,
          `sidebarExplicit=${activitySidebarStore.getState().explicitScopeRef ?? "null"}`,
          `sidebarConnected=${String(sidebarNode() !== null)}`,
          `sidebarCrumbs=${sidebarCrumbs()}`,
          `sidebarRows=[${sidebarRows()}]`,
          `retained=${retained}`,
          `case=${source}`,
          `columns=[${renderedScopeRefs("cascade-column")}]`,
          `spines=[${renderedScopeRefs("cascade-spine")}]`,
          `panes=[${workspace.panes.map(describePane).join(" ")}]`,
          `focusHistory=[${focusHistory.join(" ")}]`,
        ].join(" ");
      };
      describeFailure = drillState;
      // #4187: every wait in this journey joins the read it depends on and
      // then queries the committed DOM, instead of polling Testing Library's
      // 1000 ms deadline that a starved worker can outlast. act() commits the
      // publish each read awaited before the next synchronous query.
      const settleDelegates = async (ref: string): Promise<void> => {
        await act(async () => {
          await settleSessionActivityForTests(client, ref, "session", "delegates");
        });
        // The helper resolves immediately when no store is owned, so prove the
        // sidebar body for this scope was mounted and its read settled: a
        // no-op join must fail here, not as an unexplained missing row later.
        const delegates = sessionActivitySnapshot(client, ref, "session")?.delegates;
        expect(delegates?.loading).toBe(false);
      };
      const hydrateThread = async (ref: string): Promise<void> => {
        await act(async () => {
          await threadsStore.getState().ensureThread(ref);
          onTestFinished(() => threadsStore.getState().releaseThread(ref));
          // ensureThread returns without waiting when a model already exists:
          // a cached shell can be published before its turn bodies arrive, and
          // the drill asserts on a rendered turn. Join the model's own publish
          // instead of a DOM deadline.
          const turns = () => threadsStore.getState().threads.get(ref)?.turns?.length ?? 0;
          if (turns() === 0)
            await new Promise<void>((resolve) => {
              const stop = threadsStore.subscribe(() => {
                if (turns() === 0) return;
                stop();
                resolve();
              });
            });
        });
      };
      // Always query the currently mounted sidebar: the aside can be swapped,
      // and a captured node would answer from a detached tree.
      const sidebarNow = () => within(screen.getByTestId("activity-sidebar"));
      const delegateChildRefs = (ref: string): readonly string[] =>
        sessionActivitySnapshot(client, ref, "session")?.delegates.rows.map((row) => row.childRef) ?? [];
      // The Agents body's one expandable control is the inactive fold. Open it
      // when the wanted row is not already current, addressed by structure
      // (aria-expanded), never by its human-readable label copy.
      const revealInactive = async (row: RegExp): Promise<void> => {
        if (sidebarNow().queryByRole("button", { name: row })) return;
        const fold = sidebarNow().queryByRole("button", { expanded: false });
        if (!fold) throw new Error(`No closed inactive-subagent fold while waiting for ${row}`);
        await act(async () => {
          fireEvent.click(fold);
        });
        expect(sidebarNow().getByRole("button", { name: row })).toBeTruthy();
      };
      act(() => {
        historyPane = workspaceStore
          .getState()
          .openPane("transcript", { ref: "job:history", parentRef: "local:owner" }, { slot: "secondary" });
        workspaceStore.getState().focusPane(sourceId ?? "");
      });
      await act(async () => {});
      if (source === "main") {
        const history = workspaceStore.getState().panes.find((pane) => pane.id === historyPane);
        if (!history) throw new Error("Missing history pane");
        const reader = retainedJobLogReader(conversationPaneLifetime(history), "local:owner", "history");
        await act(async () => {
          await reader.settleForTests();
        });
        expect(screen.getByText("retained-history-output")).toBeTruthy();
      }
      expect(workspaceStore.getState().panes.some((pane) => pane.id === historyPane)).toBe(true);
      step = "retained observer";
      if (retained)
        act(() => {
          workspaceStore.getState().openPane("session", { ref: "local:child" }, { slot: "secondary" });
          workspaceStore.getState().focusPane(sourceId ?? "");
        });
      act(() => activitySidebarStore.getState().openWith("agents"));
      step = "sidebar first fold";
      const firstScope = source === "nested" ? "local:child" : "local:owner";
      const first = source === "nested" ? /grandchild/ : /observer/;
      expect(activitySidebarScopeRef()).toBe(firstScope);
      await settleDelegates(firstScope);
      await revealInactive(first);
      step = "first drill";
      await act(async () => {
        fireEvent.click(sidebarNow().getByRole("button", { name: first }));
      });
      const opened = workspaceStore.getState().panes.find((pane) => pane.type === "sessionZoom");
      expect(opened?.slot).toBe("secondary");
      expect(opened?.id).not.toBe(sourceId);
      inspectorId = opened?.id ?? "";
      expect((opened?.params as { ref?: string })?.ref).toBe(source === "nested" ? "local:grandchild" : "local:child");
      expect(workspaceStore.getState().focusedPaneId).toBe(inspectorId);
      expect(sourcePane()).toBe(original);
      expect(window.location.pathname).toBe(`/s/${routeRef}`);
      if (source !== "nested") {
        step = "sidebar second fold";
        act(() => activitySidebarStore.getState().openWith("agents"));
        expect(activitySidebarScopeRef()).toBe("local:child");
        await settleDelegates("local:child");
        expect(delegateChildRefs("local:child")).toContain("local:grandchild");
        await revealInactive(/grandchild/);
        step = "grandchild drill";
        await act(async () => {
          fireEvent.click(sidebarNow().getByRole("button", { name: /grandchild/ }));
        });
      }
      step = "grandchild transcript";
      await hydrateThread("local:grandchild");
      expect(screen.getByText("report-local:grandchild")).toBeTruthy();
      expect(sourcePane()).toBe(original);
      expect(conversationPaneLifetime(original)).toBe(lifetime);
      expect(workspaceStore.getState().focusedPaneId).toBe(inspectorId);
      expect((inspectorPane()?.params as { ref?: string })?.ref).toBe("local:grandchild");
      // The origin conversation never renders in the pane. The nested source
      // (local:child) is the drilled leaf's immediate parent, so removing it
      // leaves the owner as the only spine and the leaf the only column. A
      // main or transcript origin (local:owner) is the path's root, so the
      // child stays the readable parent and no spine renders at all.
      const spines = screen.queryAllByTestId("cascade-spine");
      expect(spines.map((spine) => spine.getAttribute("data-scope-ref"))).toEqual(
        source === "nested" ? ["local:owner"] : [],
      );
      for (const spine of spines) expect(within(spine).queryByText("report-local:owner")).toBeNull();
      expect(screen.getAllByTestId("cascade-column").map((column) => column.getAttribute("data-scope-ref"))).toEqual(
        source === "nested" ? ["local:grandchild"] : ["local:child", "local:grandchild"],
      );
      expect(workspaceStore.getState().panes.some((p) => p.id === historyPane)).toBe(true);
      expect(workspaceStore.getState().panes.filter(isRefOnlyChild)).toHaveLength(retained ? 1 : 0);
      // The cascade column's "Open conversation" link is gone - the column
      // embeds the conversation directly - so the click flow that opened a
      // session pane for the grandchild is deleted with it. The workspace and
      // focus guarantees live on in the crumb-pop assertions below.
      act(() => workspaceStore.getState().focusPane(inspectorId));
      step = "crumb pop";
      // The origin conversation never renders, so its crumb is not offered
      // and the cascade cannot pop to it; the nearest rendered ancestor
      // crumb still pops. Nested: the origin is local:child, so local:owner
      // is the working pop target above it.
      if (source !== "nested") expect(sidebarNow().queryByRole("button", { name: "local:owner" })).toBeNull();
      const target = source === "nested" ? "local:owner" : "local:child";
      await act(async () => {
        fireEvent.click(sidebarNow().getByRole("button", { name: target }));
      });
      step = "popped column";
      expect(workspaceStore.getState().focusedPaneId).toBe(inspectorId);
      expect(sourcePane()).toBe(original);
      expect(inspectorPane()?.type).toBe("sessionZoom");
      expect((inspectorPane()?.params as { ref?: string })?.ref).toBe(target);
      await hydrateThread(target);
      const poppedColumn = screen.getByTestId("cascade-column");
      expect(within(poppedColumn).getByText(`report-${target}`)).toBeTruthy();
      act(() => activitySidebarStore.getState().openWith("agents"));
      step = "disclosure after pop";
      // Disclosure persistence belongs to the retention suite; this journey
      // verifies navigation and focus with the core sidebar's local disclosure.
      const disclosed = source === "nested" ? /observer/ : /grandchild/;
      expect(activitySidebarScopeRef()).toBe(target);
      await settleDelegates(target);
      await revealInactive(disclosed);
      step = "route navigate";
      await act(async () => {
        navigate("/s/local%3As1");
        await navigationStore.getState().lookupLocation("local:s1");
      });
      expect(workspaceStore.getState().mainPane()?.type).toBe("session");
      expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:s1" });
      expect(workspaceStore.getState().panes).toHaveLength(1);
    } catch (error) {
      // Any failure in the journey - an assertion or a waiting find - reports
      // the state above it, so a recurrence names the pane that took focus and
      // what the workspace held instead of only the bare matcher diff.
      if (error instanceof Error)
        error.message = `${error.message}\n\n[#4187 desktop Activity drill state] ${describeFailure()}`;
      throw error;
    }
  },
);

test.each([false, true])(
  "rail session activation opens live chrome with retained transcript and nested=%s",
  async (nested) => {
    const user = userEvent.setup();
    const client = navClient();
    client.on("thread/read", ({ ref }) => {
      if (!ref) throw new Error("session ref required");
      return { thread: threadStartResponse(ref).thread };
    });
    if (nested)
      client.on("evener/navigation/read", (params) =>
        params.resource === "location" && params.ref === "local:s1"
          ? wireSnapshot(
              params,
              {
                ref: "local:s1",
                top_level_ref: "local:owner",
                top_level: false,
                session: { ...TREE_SESSION, kind: "subagent" },
              },
              '"nested"',
            )
          : navigationRead(params),
      );
    window.history.pushState({}, "", "/s/local%3Aowner");
    render(<AppShell client={client} />);
    await rail().findByText("Session one");
    await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:owner" }));
    const owner = workspaceStore.getState().mainPane();
    const transcript = await act(async () =>
      workspaceStore
        .getState()
        .openPane("transcript", { ref: "local:s1", parentRef: "local:owner" }, { slot: "secondary" }),
    );
    await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(transcript));
    if (nested)
      act(() =>
        installLocation({
          generation_id: "generation_test",
          revision: 1,
          ref: "local:s1",
          top_level_ref: "local:owner",
          top_level: false,
          session: { ...TREE_SESSION, kind: "subagent", state: "idle" },
        }),
      );
    await user.click(rail().getByText("Session one"));
    expect(window.location.pathname).toBe("/s/local%3As1");
    await waitFor(() => {
      const state = workspaceStore.getState();
      const focused = state.panes.find((pane) => pane.id === state.focusedPaneId);
      expect(focused?.type).toBe("session");
      expect(focused?.params).toEqual({ ref: "local:s1" });
    });
    expect(workspaceStore.getState().mainPane()?.type).toBe("session");
    if (nested) {
      expect(workspaceStore.getState().mainPane()?.id).toBe(owner?.id);
      expect(
        workspaceStore.getState().panes.find((pane) => pane.id === workspaceStore.getState().focusedPaneId)?.slot,
      ).toBe("secondary");
      expect(await screen.findAllByRole("textbox", { name: "Message" })).toHaveLength(2);
    } else expect(await screen.findByRole("textbox", { name: "Message" })).toBeTruthy();
  },
);
