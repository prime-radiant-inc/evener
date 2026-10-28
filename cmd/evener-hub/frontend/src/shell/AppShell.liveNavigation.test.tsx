import type {
  NavigationReadParams,
  NavigationReadResponse,
  NavigationSessionLocation,
  NavigationSessionSummary,
} from "@evener/appwire-client";
import { keyID } from "@evener/appwire-client/state/navigation";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { afterEach, beforeAll, beforeEach, expect, onTestFinished, test, vi } from "vitest";
import { initNotifications, resetNotificationsForTests } from "../notifications";
import { StubResizeObserver } from "../resizeObserverTestUtils";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import { resetAuthStatusesStoreForTests } from "../stores/authStatuses";
import { connectionStore } from "../stores/connection";
import { navigationStore, resetNavigationStoreForTests } from "../stores/navigation/store";
import { resetPrefsStoreForTests } from "../stores/prefs";
import { resetSettingsHostForTests } from "../stores/settingsHost";
import { resetSettingsOverviewStoreForTests } from "../stores/settingsOverview";
import { AppShell } from "./AppShell";
import { LIVE_CYCLE_B, rail } from "./appShellTestUtils";
import { paletteStore } from "./palette/paletteController";
import { navigate } from "./routing";
import { resetWorkspaceStoreForTests, workspaceStore } from "./workspace";

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

// The zoom system promotes a session pane into a cascade by retyping it in
// place (panes/zoom/actions.ts) - same pane id, same slot - so this is the
// exact main a saved layout restores. Edges are the saved cascade path.
function retypeMainToCascade(
  sourceRef: string,
  leafRef: string,
  edges: { ownerRef: string; childRef: string; delegateId: string }[] = [],
): void {
  const main = workspaceStore.getState().mainPane();
  if (!main) throw new Error("no main pane to zoom");
  if (
    !workspaceStore.getState().retypePane(main, "sessionZoom", {
      ref: leafRef,
      source: { type: "session", params: { ref: sourceRef } },
      edges,
    })
  ) {
    throw new Error("cascade promotion failed");
  }
}

// --- Alt+Shift+ArrowRight/Left live-session navigation
//
// AppShell registers session.liveNext/session.livePrevious against the
// keybindings registry; the actions navigate across the rail's live section
// in server order through shell/rail/liveSessionCycle.ts. These tests pin
// the WIRING (real dispatcher, real defaults, real shell); the cycling
// order/wrap/no-op semantics themselves are liveSessionCycle.test.ts's.

const LIVE_CYCLE_A: NavigationSessionSummary = {
  ref: "local:live-a",
  host_id: "local",
  session_id: "live-a",
  title: "Live A",
  project: "prime-radiant",
  state: "idle",
  kind: "session",
  live: true,
  children: [],
};

// navClient, but with a two-row live section: everything else delegates to
// the shared navigationRead.
function navClientWithLive(sessions: NavigationSessionSummary[]): FakeClient {
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params: NavigationReadParams): NavigationReadResponse => {
    if (params.resource === "section" && params.section === "live") {
      return wireSnapshot(params, { sessions, remaining: 0, truncated: false }, '"test"');
    }
    return navigationRead(params);
  });
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v4",
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "generation_test", sequence: 0, readVersions: [3] },
  }));
  return client;
}

// The rail's live section paginates (limit 50 per page). At the last LOADED
// live session, next must demand-load the following page and continue into
// it — wrapping over the loaded subset would skip every live session behind
// the remaining count (roborev PR #1044 finding 1).
test("Alt+Shift+ArrowRight at the last loaded live session demand-loads the next page instead of wrapping", async () => {
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params: NavigationReadParams): NavigationReadResponse => {
    if (params.resource === "section" && params.section === "live") {
      if ((params.offset ?? 0) === 0) {
        return wireSnapshot(params, { sessions: [LIVE_CYCLE_A], remaining: 1, truncated: false }, '"test"');
      }
      return wireSnapshot(params, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"');
    }
    return navigationRead(params);
  });
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v4",
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "generation_test", sequence: 0, readVersions: [3] },
  }));

  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await rail().findByText("Live A");

  await user.click(rail().getByText("Live A"));
  expect(window.location.pathname).toBe("/s/local%3Alive-a");
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });

  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  await waitFor(() => {
    expect(window.location.pathname).toBe("/s/local%3Alive-b");
  });
});

// Round-2 demand-load lifecycle: the pending-page cache must not brick after
// a failed load, and an in-flight demand must go inert when a newer press or
// an open palette supersedes it (roborev PR #1044 round-2 mediums 1-2).

// A two-page live section whose page-two request is scriptable per test.
function navClientWithDeferredLivePageTwo(script: {
  onPageTwo: (params: NavigationReadParams) => NavigationReadResponse | Promise<NavigationReadResponse>;
}): FakeClient {
  const client = new FakeClient("ready");
  client.on(
    "evener/navigation/read",
    (params: NavigationReadParams): NavigationReadResponse | Promise<NavigationReadResponse> => {
      if (params.resource === "section" && params.section === "live") {
        if ((params.offset ?? 0) === 0) {
          return wireSnapshot(params, { sessions: [LIVE_CYCLE_A], remaining: 1, truncated: false }, '"test"');
        }
        return script.onPageTwo(params);
      }
      return navigationRead(params);
    },
  );
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v4",
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "generation_test", sequence: 0, readVersions: [3] },
  }));
  return client;
}

test("live-next demand-load retries after the page request fails", async () => {
  let pageTwoAttempts = 0;
  const client = navClientWithDeferredLivePageTwo({
    onPageTwo: (params) => {
      pageTwoAttempts++;
      // The background Live roster owner discovers the complete roster and
      // spends the first attempt; the chord's first demand spends the second,
      // and its retry is the third.
      if (pageTwoAttempts <= 2) throw new Error("transient read failure");
      return wireSnapshot(params, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"');
    },
  });
  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await rail().findByText("Live A");
  // Page two was already read once (and failed) by background discovery.
  await waitFor(() => expect(pageTwoAttempts).toBe(1));

  await user.click(rail().getByText("Live A"));
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });

  // The first demand re-requests the failed page and fails again; the view
  // stays and no navigation happens.
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift>}{/Alt}");
  await waitFor(() => expect(pageTwoAttempts).toBe(2));
  expect(window.location.pathname).toBe("/s/local%3Alive-a");

  // A cached "already demanded" entry must not brick the retry.
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift>}{/Alt}");
  await waitFor(() => expect(pageTwoAttempts).toBe(3));
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-b"));
});

test("an in-flight live demand-load goes inert when a newer live-nav press supersedes it", async () => {
  const deferred: { params: NavigationReadParams | null; resolve: ((r: NavigationReadResponse) => void) | null } = {
    params: null,
    resolve: null,
  };
  const client = new FakeClient("ready");
  client.on(
    "evener/navigation/read",
    (params: NavigationReadParams): NavigationReadResponse | Promise<NavigationReadResponse> => {
      if (params.resource === "section" && params.section === "live") {
        if ((params.offset ?? 0) === 0) {
          // Two loaded rows and one more page behind them, so the second press
          // below (previous from the last loaded row) is a DIRECT mid-list
          // step, not a demand - a previous from the FIRST loaded row would
          // itself demand the same in-flight page (round-5 tail rule).
          return wireSnapshot(
            params,
            { sessions: [LIVE_CYCLE_A, LIVE_CYCLE_B], remaining: 1, truncated: false },
            '"test"',
          );
        }
        return new Promise<NavigationReadResponse>((resolve) => {
          deferred.params = params;
          deferred.resolve = resolve;
        });
      }
      return navigationRead(params);
    },
  );
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v4",
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "generation_test", sequence: 0, readVersions: [3] },
  }));
  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await rail().findByText("Live B");

  await user.click(rail().getByText("Live B"));
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-b" });
  });

  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}"); // demand in flight
  await waitFor(() => expect(deferred.params).not.toBeNull());

  // An ordinary press (direct mid-list step to A) is newer intent: it must
  // supersede the demand.
  await user.keyboard("{Alt>}{Shift>}{ArrowLeft}{/Shift}{/Alt}");
  expect(window.location.pathname).toBe("/s/local%3Alive-a");
  const params = deferred.params;
  const resolve = deferred.resolve;
  if (!params || !resolve) throw new Error("page-two request was not issued");
  await act(async () => {
    resolve(wireSnapshot(params, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"'));
  });
  expect(window.location.pathname).toBe("/s/local%3Alive-a");
});

// With NO live rows loaded (the initial section read failed or is still in
// flight), selectSectionRemaining is 0, so the boundary rule alone would
// leave the chord inert until an external refresh. The manifest's live count
// is the authority there: a press re-requests page zero and continues into
// it (roborev PR #1044 round 3; the needs-you handler's manifest-count
// bootstrap).
test("live-next with an unloaded live section re-requests page zero when the manifest reports live sessions", async () => {
  let liveReads = 0;
  let allowLive = false;
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params: NavigationReadParams): NavigationReadResponse => {
    if (params.resource === "section" && params.section === "live") {
      liveReads++;
      // Fail every automatic attempt - the manifest hydration's read and the
      // background roster owner's discovery - so the section stays unloaded
      // until the chord's own demand can be answered.
      if (!allowLive) throw new Error("transient initial failure");
      return wireSnapshot(params, { sessions: [LIVE_CYCLE_A], remaining: 0, truncated: false }, '"test"');
    }
    return navigationRead(params);
  });
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v4",
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "generation_test", sequence: 0, readVersions: [3] },
  }));

  const user = userEvent.setup();
  render(<AppShell client={client} />);
  // The initial hydration read failed: no live rows, nothing focused.
  await screen.findByText("No session open");
  expect(rail().queryByText("Live A")).toBeNull();
  const unloadedReads = liveReads;

  allowLive = true;
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift>}{/Alt}");
  await waitFor(() => expect(liveReads).toBeGreaterThan(unloadedReads));
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-a"));
});

// #4958: once a visible live page has settled with no remaining rows, its
// own answer owns completion. The manifest's live count refreshes on its own
// schedule and may still report rows; neither direction may spend a page read
// on that stale count.
test("live chord does not demand a page once a settled live page owns completion", async () => {
  const liveReads: number[] = [];
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params: NavigationReadParams): NavigationReadResponse => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        { ...EMPTY_NAV_RESPONSE, sections: { ...EMPTY_NAV_RESPONSE.sections, live: { count: 2 } } },
        '"test"',
      );
    }
    if (params.resource === "section" && params.section === "live") {
      liveReads.push(params.offset ?? 0);
      return wireSnapshot(params, { sessions: [], remaining: 0, truncated: false }, '"test"');
    }
    return navigationRead(params);
  });
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v4",
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "generation_test", sequence: 0, readVersions: [3] },
  }));

  // The chord reads the store's current action, so a wrapper counts demands
  // without depending on whether a settled page answers from cache.
  const demands: Array<{ offset?: number }> = [];
  const loadSection = navigationStore.getState().loadSection;
  onTestFinished(() => navigationStore.setState({ loadSection }));
  navigationStore.setState({
    loadSection: (section, offset, limit) => {
      if (section === "live") demands.push({ offset });
      return loadSection(section, offset, limit);
    },
  });

  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await screen.findByText("No session open");
  await waitFor(() => {
    const page = [...navigationStore.getState().resources.values()].find(
      (resource) => resource.key.kind === "section" && resource.key.section === "live",
    );
    expect(page?.loading).toBe(false);
    expect(page?.data).not.toBeNull();
  });
  expect(liveReads.length).toBeGreaterThan(0);
  const demandedBefore = demands.length;

  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  await user.keyboard("{Alt>}{Shift>}{ArrowLeft}{/Shift}{/Alt}");
  expect(demands.length).toBe(demandedBefore);
  expect(window.location.pathname).toBe("/");
});

// Round 4: the previous direction must reach the live TAIL, not the first
// newly loaded row (roborev PR #1044 round-4 medium 1).
test("live-previous with an unloaded live section demand-loads to the tail", async () => {
  const deferred: { params: NavigationReadParams | null; resolve: ((r: NavigationReadResponse) => void) | null } = {
    params: null,
    resolve: null,
  };
  const client = new FakeClient("ready");
  client.on(
    "evener/navigation/read",
    (params: NavigationReadParams): NavigationReadResponse | Promise<NavigationReadResponse> => {
      if (params.resource === "manifest") {
        return wireSnapshot(
          params,
          { ...EMPTY_NAV_RESPONSE, sections: { ...EMPTY_NAV_RESPONSE.sections, live: { count: 2 } } },
          '"test"',
        );
      }
      if (params.resource === "section" && params.section === "live") {
        if ((params.offset ?? 0) === 0) {
          return new Promise<NavigationReadResponse>((resolve) => {
            deferred.params = params;
            deferred.resolve = resolve;
          });
        }
        return wireSnapshot(params, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"');
      }
      return navigationRead(params);
    },
  );
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v4",
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "generation_test", sequence: 0, readVersions: [3] },
  }));

  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await screen.findByText("No session open");
  // Page zero's read (the manifest hydration's) is in flight, deferred.
  await waitFor(() => expect(deferred.params).not.toBeNull());

  await user.keyboard("{Alt>}{Shift>}{ArrowLeft}{/Shift}{/Alt}");
  // Page zero resolves with one row and one more page behind it: the
  // previous direction must keep loading to the tail, not land on A.
  const params = deferred.params;
  const resolve = deferred.resolve;
  if (!params || !resolve) throw new Error("page-zero request was not issued");
  await act(async () => {
    resolve(wireSnapshot(params, { sessions: [LIVE_CYCLE_A], remaining: 1, truncated: false }, '"test"'));
  });
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-b"));
});

// Round 4: a second same-direction press while the demanded page is still in
// flight must not invalidate the demand it is waiting on (roborev PR #1044
// round-4 medium 2).
test("rapid live-next presses at the boundary still navigate when the demand lands", async () => {
  const deferred: { params: NavigationReadParams | null; resolve: ((r: NavigationReadResponse) => void) | null } = {
    params: null,
    resolve: null,
  };
  const client = new FakeClient("ready");
  client.on(
    "evener/navigation/read",
    (params: NavigationReadParams): NavigationReadResponse | Promise<NavigationReadResponse> => {
      if (params.resource === "section" && params.section === "live") {
        if ((params.offset ?? 0) === 0) {
          return wireSnapshot(params, { sessions: [LIVE_CYCLE_A], remaining: 1, truncated: false }, '"test"');
        }
        return new Promise<NavigationReadResponse>((resolve) => {
          deferred.params = params;
          deferred.resolve = resolve;
        });
      }
      return navigationRead(params);
    },
  );
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v4",
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "generation_test", sequence: 0, readVersions: [3] },
  }));

  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await rail().findByText("Live A");

  await user.click(rail().getByText("Live A"));
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });

  // Two presses before the demanded page lands: one demand, kept live.
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  await waitFor(() => expect(deferred.params).not.toBeNull());
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");

  const params = deferred.params;
  const resolve = deferred.resolve;
  if (!params || !resolve) throw new Error("page-two request was not issued");
  await act(async () => {
    resolve(wireSnapshot(params, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"'));
  });
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-b"));
});

// Round 5: with the live section PARTIALLY loaded, a previous press that
// would wrap (from the first loaded row, or from a non-live session) must
// demand-load to the true tail rather than landing on the last loaded row
// (roborev PR #1044 round-5 medium 1).
test("live-previous wrapping with more pages on the server demand-loads to the tail", async () => {
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params: NavigationReadParams): NavigationReadResponse => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        { ...EMPTY_NAV_RESPONSE, sections: { ...EMPTY_NAV_RESPONSE.sections, live: { count: 3 } } },
        '"test"',
      );
    }
    if (params.resource === "section" && params.section === "live") {
      if ((params.offset ?? 0) === 0) {
        return wireSnapshot(params, { sessions: [LIVE_CYCLE_A], remaining: 2, truncated: false }, '"test"');
      }
      return wireSnapshot(params, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"');
    }
    return navigationRead(params);
  });
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v4",
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "generation_test", sequence: 0, readVersions: [3] },
  }));

  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await rail().findByText("Live A");

  // Focus the FIRST loaded live session; previous from here wraps.
  await user.click(rail().getByText("Live A"));
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });

  await user.keyboard("{Alt>}{Shift>}{ArrowLeft}{/Shift}{/Alt}");
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-b"));
});

test("an in-flight live demand-load goes inert while the palette is open", async () => {
  const deferred: { params: NavigationReadParams | null; resolve: ((r: NavigationReadResponse) => void) | null } = {
    params: null,
    resolve: null,
  };
  const client = navClientWithDeferredLivePageTwo({
    onPageTwo: (params) =>
      new Promise<NavigationReadResponse>((resolve) => {
        deferred.params = params;
        deferred.resolve = resolve;
      }),
  });
  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await rail().findByText("Live A");

  await user.click(rail().getByText("Live A"));
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });

  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}"); // demand in flight
  await waitFor(() => expect(deferred.params).not.toBeNull());

  act(() => {
    paletteStore.setState({ open: true, query: "" });
  });
  const params = deferred.params;
  const resolve = deferred.resolve;
  if (!params || !resolve) throw new Error("page-two request was not issued");
  await act(async () => {
    resolve(wireSnapshot(params, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"'));
  });
  expect(window.location.pathname).toBe("/s/local%3Alive-a");
});

// Round 6: the demand dedupe keyed the page alone, so an opposite-direction
// press while that page was in flight returned early WITHOUT bumping the
// intent counter - the stale continuation still owned the navigation. With no
// live rows loaded, Previous then Next while page zero loads must leave the
// Next intent in charge: the first returned row, not the previous direction's
// tail (roborev PR #1044 round-6 medium).
test("an opposite-direction press while a demanded page is in flight supersedes the prior intent", async () => {
  const deferred: { params: NavigationReadParams | null; resolve: ((r: NavigationReadResponse) => void) | null } = {
    params: null,
    resolve: null,
  };
  const client = new FakeClient("ready");
  client.on(
    "evener/navigation/read",
    (params: NavigationReadParams): NavigationReadResponse | Promise<NavigationReadResponse> => {
      if (params.resource === "manifest") {
        return wireSnapshot(
          params,
          { ...EMPTY_NAV_RESPONSE, sections: { ...EMPTY_NAV_RESPONSE.sections, live: { count: 3 } } },
          '"test"',
        );
      }
      if (params.resource === "section" && params.section === "live") {
        if ((params.offset ?? 0) === 0) {
          return new Promise<NavigationReadResponse>((resolve) => {
            deferred.params = params;
            deferred.resolve = resolve;
          });
        }
        return wireSnapshot(params, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"');
      }
      return navigationRead(params);
    },
  );
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v4",
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "generation_test", sequence: 0, readVersions: [3] },
  }));

  const user = userEvent.setup();
  render(<AppShell client={client} />);
  // Page zero (the manifest hydration's) is in flight, deferred.
  await screen.findByText("No session open");
  await waitFor(() => expect(deferred.params).not.toBeNull());

  // Previous first: with nothing loaded it demands page zero toward the tail.
  await user.keyboard("{Alt>}{Shift>}{ArrowLeft}{/Shift}{/Alt}");
  // Then the user flips to Next: the newest intent must own the navigation.
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");

  const params = deferred.params;
  const resolve = deferred.resolve;
  if (!params || !resolve) throw new Error("page-zero request was not issued");
  await act(async () => {
    resolve(wireSnapshot(params, { sessions: [LIVE_CYCLE_A], remaining: 1, truncated: false }, '"test"'));
  });
  // Next from nothing opens the FIRST live row; the previous direction's
  // continuation must not run past it toward the tail.
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-a"));
});

// Round 7: the demand-load continuation never re-checked the navigation
// client generation. A reconnect bumps clientGenerationID while the demanded
// page is in flight; the handler's press-time reset only covers NEW presses,
// so the old promise resolving with retained (provisional) rows navigated
// into the previous generation (roborev PR #1044 round-7 medium 2).
test("an in-flight live demand-load goes inert when the client generation changes", async () => {
  const deferred: { params: NavigationReadParams | null; resolve: ((r: NavigationReadResponse) => void) | null } = {
    params: null,
    resolve: null,
  };
  const client = navClientWithDeferredLivePageTwo({
    onPageTwo: (params) =>
      new Promise<NavigationReadResponse>((resolve) => {
        deferred.params = params;
        deferred.resolve = resolve;
      }),
  });
  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await rail().findByText("Live A");

  await user.click(rail().getByText("Live A"));
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });

  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}"); // demand in flight
  await waitFor(() => expect(deferred.params).not.toBeNull());

  // A reconnect boots a new generation while the demand is still in flight.
  act(() => {
    navigationStore.setState({ clientGenerationID: "generation_reconnected" });
  });
  const params = deferred.params;
  const resolve = deferred.resolve;
  if (!params || !resolve) throw new Error("page-two request was not issued");
  await act(async () => {
    resolve(wireSnapshot(params, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"'));
  });
  // The stale-generation continuation must not navigate.
  expect(window.location.pathname).toBe("/s/local%3Alive-a");
});

// Round 8, medium 3: live navigation must FOCUS the target session even
// when the URL already matches - a secondary panel or another pane can hold
// focus while the route names the session (roborev PR #1044 round-8 medium 3).
test("live-next focuses the session pane even when the URL already matches", async () => {
  // Two live rows on one page: A, B.
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params: NavigationReadParams): NavigationReadResponse => {
    if (params.resource === "section" && params.section === "live") {
      return wireSnapshot(params, { sessions: [LIVE_CYCLE_A, LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"');
    }
    return navigationRead(params);
  });
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v4",
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "generation_test", sequence: 0, readVersions: [3] },
  }));
  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await rail().findByText("Live A");

  await user.click(rail().getByText("Live A"));
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });

  // Focus a secondary session panel over the same session: the URL stays
  // /s/local:live-a while the focused pane is the panel.
  act(() => {
    const panelId = workspaceStore.getState().openPane("sessionTasks", { ref: "local:live-a" }, { slot: "secondary" });
    workspaceStore.getState().focusPane(panelId);
  });
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toMatch(/^pane_sessionTasks_/));

  // Navigate to B so the URL differs from the wrap target, then wrap
  // previous. But focus first: the route's own reconciliation would fight
  // the panel focus, so drive the navigation by CLICKING the row (focus
  // follows), then re-focus the panel.
  await user.click(rail().getByText("Live B"));
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-b"));
  act(() => {
    const panelId = workspaceStore.getState().openPane("sessionTasks", { ref: "local:live-b" }, { slot: "secondary" });
    workspaceStore.getState().focusPane(panelId);
  });
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-b"));
  // The URL now matches B while the PANEL holds focus.
  expect(workspaceStore.getState().focusedPaneId).toMatch(/^pane_sessionTasks_/);

  // The URL-equal press: previous from the panel. focusedSessionRef() is
  // null (the panel is not a session pane), so previous wraps to the list
  // HEAD... which is A, not the panel's B. Hmm - the URL changes to A. The
  // decisive URL-equal case is NEXT from B's panel: null current targets
  // the FIRST live row (A), and the URL is ALREADY /s/local%3Alive-b only
  // if... no. The decisive case as documented: pressing next with the URL
  // already on the TARGET. Target A, URL /s/local%3Alive-b -> differs.
  // Target B, URL on B: previous with current=null targets the LAST row
  // (B): URL EQUAL. That press must still refocus the session pane.
  await user.keyboard("{Alt>}{Shift>}{ArrowLeft}{/Shift}{/Alt}");
  // The press must refocus the session pane (the navigation the user asked
  // for), even though the URL did not move - it already named B.
  const focused = workspaceStore.getState().panes.find((p) => p.id === workspaceStore.getState().focusedPaneId);
  expect(focused?.type).toBe("session");
});

// The same URL-equal press against a CASCADE main: the placement seam now
// preserves a cascade whose session source is the pressed ref (and must keep
// preserving it - and never steal focus on its own, or a restored layout's
// saved focus would not survive boot placement), but the live chord's own
// contract is that the press always ends with the session pane focused. A
// preserved cascade keeps the pane id, so the guarantee has to reach it too.
test("live-previous focuses a preserved cascade main when the URL already matches", async () => {
  const user = userEvent.setup();
  render(<AppShell client={navClientWithLive([LIVE_CYCLE_A, LIVE_CYCLE_B])} />);
  await rail().findByText("Live B");

  // The URL names B with B's plain session pane in main, then the zoom
  // system's in-place retype turns that pane into a cascade on the same
  // source - the exact main a saved layout restores.
  await user.click(rail().getByText("Live B"));
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-b" });
  });
  act(() => {
    retypeMainToCascade("local:live-b", "local:live-child");
  });

  // A secondary panel holds focus while the URL still names B.
  act(() => {
    const panelId = workspaceStore.getState().openPane("sessionTasks", { ref: "local:live-b" }, { slot: "secondary" });
    workspaceStore.getState().focusPane(panelId);
  });
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toMatch(/^pane_sessionTasks_/));

  // URL-equal press: previous from the panel targets the LAST live row (B),
  // whose URL the route already holds. The cascade must survive the press
  // AND take focus.
  await user.keyboard("{Alt>}{Shift>}{ArrowLeft}{/Shift}{/Alt}");
  const state = workspaceStore.getState();
  expect(state.mainPane()?.type).toBe("sessionZoom");
  expect(state.focusedPaneId).toBe(state.mainPane()?.id);
});

// Round 8, low 1: a COMPLETED demand's dedupe key must leave the in-flight
// set. After an invalidation (not a generation change) re-stales the live
// pages, the same page+direction demand must be issuable again instead of
// being silently swallowed by the stale key (roborev PR #1044 round-8 low 1).
test("a live demand can be re-issued after an invalidation restales the pages", async () => {
  let pageTwoLoads = 0;
  const deferred: { params: NavigationReadParams | null; resolve: ((r: NavigationReadResponse) => void) | null } = {
    params: null,
    resolve: null,
  };
  const client = navClientWithDeferredLivePageTwo({
    onPageTwo: (params) =>
      new Promise<NavigationReadResponse>((resolve) => {
        pageTwoLoads++;
        deferred.params = params;
        deferred.resolve = resolve;
      }),
  });
  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await rail().findByText("Live A");

  await user.click(rail().getByText("Live A"));
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });

  // First demand: page two loads and navigates to B.
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  await waitFor(() => expect(pageTwoLoads).toBe(1));
  const params = deferred.params;
  const resolve = deferred.resolve;
  if (!params || !resolve) throw new Error("page-two request was not issued");
  await act(async () => {
    resolve(wireSnapshot(params, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"'));
  });
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-b"));

  // Go back to A, then invalidate the live section (same generation): the
  // loaded pages go stale, so the boundary press must re-demand page two.
  await user.keyboard("{Alt>}{Shift>}{ArrowLeft}{/Shift}{/Alt}");
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-a"));
  const beforeDemandPress = pageTwoLoads; // count BEFORE the invalidation's own refresh
  await act(async () => {
    client.emitNotification({
      method: "evener/navigation/invalidated",
      params: {
        generationId: "generation_test",
        sequence: 1,
        targets: [{ kind: "section", section: "live" }],
      },
    });
  });
  // The invalidation re-requests the stale live pages (the revalidator's
  // own refresh) - wait for that to settle before the boundary press.
  await waitFor(() => expect(pageTwoLoads).toBeGreaterThan(0));
  await waitFor(() => expect(pageTwoLoads).toBeGreaterThanOrEqual(beforeDemandPress));

  // Press next at the same boundary: a fresh demand for the same
  // page+direction must be issued.
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  await waitFor(() => expect(pageTwoLoads).toBe(beforeDemandPress + 1));
  const resolve2 = deferred.resolve;
  const params2 = deferred.params;
  if (!params2 || !resolve2) throw new Error("second page-two request was not issued");
  await act(async () => {
    resolve2(wireSnapshot(params2, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"'));
  });
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-b"));
});

// Round 9, medium 3: the inert early return must clear the in-flight key so
// the set keeps tracking in-flight demands only. A same-key re-demand is not
// reachable end-to-end (a page that resolves with data always advances the
// next-demand offset past itself, and an error/abort resolves through the
// already-deleting error path), so this pins the reachable lifecycle instead:
// a palette-inert completion leaves nothing sticky - after the palette
// closes, the same chord still navigates, here via the page the inert
// completion loaded (roborev PR #1044 round-9 medium 3).
test("a demand that completes inert does not leave the live chord stuck", async () => {
  let pageTwoLoads = 0;
  const deferred: { params: NavigationReadParams | null; resolve: ((r: NavigationReadResponse) => void) | null } = {
    params: null,
    resolve: null,
  };
  const client = navClientWithDeferredLivePageTwo({
    onPageTwo: (params) =>
      new Promise<NavigationReadResponse>((resolve) => {
        pageTwoLoads++;
        deferred.params = params;
        deferred.resolve = resolve;
      }),
  });
  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await rail().findByText("Live A");

  await user.click(rail().getByText("Live A"));
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });

  // First demand: the palette is open when it completes, so it goes inert
  // (no navigation) even though the page resolved with B.
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  await waitFor(() => expect(pageTwoLoads).toBe(1));
  act(() => {
    paletteStore.setState({ open: true, query: "" });
  });
  const params = deferred.params;
  const resolve = deferred.resolve;
  if (!params || !resolve) throw new Error("page-two request was not issued");
  await act(async () => {
    resolve(wireSnapshot(params, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"'));
  });
  expect(window.location.pathname).toBe("/s/local%3Alive-a");
  act(() => {
    paletteStore.setState({ open: false, query: "" });
  });

  // After the palette closes, the same chord must still work - the demand's
  // page is loaded, so this is the direct step to B.
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-b"));
});

// Round 13, low 5: the demand's completion never re-checks editable focus.
// The chord is suppressed while an editable target has focus at PRESS time,
// but a demand pressed from a non-editable target still navigates when it
// completes even if the user focused the composer mid-flight - the keydown
// was swallowed then, so the navigation surprises a typing user. The
// completion must go inert (delete the in-flight key, no navigation) when
// focus is editable (roborev PR #1044 round-13 low 5).
test("a demand that completes while the composer has focus goes inert", async () => {
  let pageTwoLoads = 0;
  const deferred: { params: NavigationReadParams | null; resolve: ((r: NavigationReadResponse) => void) | null } = {
    params: null,
    resolve: null,
  };
  const client = navClientWithDeferredLivePageTwo({
    onPageTwo: (params) =>
      new Promise<NavigationReadResponse>((resolve) => {
        pageTwoLoads++;
        deferred.params = params;
        deferred.resolve = resolve;
      }),
  });
  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await rail().findByText("Live A");

  await user.click(rail().getByText("Live A"));
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });

  // The press comes from a non-editable target: the demand issues.
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  await waitFor(() => expect(pageTwoLoads).toBe(1));

  // The user focuses an editable surface (the composer) while the demand is
  // in flight. The pane's real composer mounts lazily in this fixture, so
  // the test focuses a plain input: the completion guard checks
  // isEditableTarget(document.activeElement), which any editable element
  // satisfies identically.
  const editable = document.createElement("input");
  document.body.appendChild(editable);
  await act(async () => {
    editable.focus();
  });

  // The completion must not navigate: focus is editable.
  const params = deferred.params;
  const resolve = deferred.resolve;
  if (!params || !resolve) throw new Error("page-two request was not issued");
  await act(async () => {
    resolve(wireSnapshot(params, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"'));
  });
  await waitFor(() => expect(pageTwoLoads).toBe(1));
  expect(window.location.pathname).toBe("/s/local%3Alive-a");
});

// Round 9, medium 4: leaving the session and returning before a demand
// resolves restores an identical focused pane and session ref, so the
// press-time guards alone read as "never left" and the stale completion
// navigates under the returned user. The route epoch must invalidate the
// demand instead (roborev PR #1044 round-9 medium 4).
test("an in-flight live demand-load goes inert after leaving the session and returning", async () => {
  let pageTwoLoads = 0;
  const deferred: { params: NavigationReadParams | null; resolve: ((r: NavigationReadResponse) => void) | null } = {
    params: null,
    resolve: null,
  };
  const client = navClientWithDeferredLivePageTwo({
    onPageTwo: (params) =>
      new Promise<NavigationReadResponse>((resolve) => {
        pageTwoLoads++;
        deferred.params = params;
        deferred.resolve = resolve;
      }),
  });
  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await rail().findByText("Live A");

  await user.click(rail().getByText("Live A"));
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });
  const paneAtPress = workspaceStore.getState().focusedPaneId;
  // The route-placement effect re-focuses the session pane on the return
  // leg only when the location resource is present, so the test installs
  // it exactly as the app's own location lookup would have.
  act(() => installLocationForRoute("local:live-a"));

  // Demand in flight from A (the last loaded live row). The leave/return
  // must be a round trip the OLD guards cannot see: going to "/" opens the
  // welcome pane in SECONDARY (openPane never displaces a non-welcome
  // main), and returning re-matches the still-open session pane, so the
  // pane id, focused ref, and pathname are all identical to press time -
  // only the route epoch knows the user left at all.
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  await waitFor(() => expect(pageTwoLoads).toBe(1));

  await act(async () => {
    navigate("/");
  });
  await waitFor(() => expect(window.location.pathname).toBe("/"));
  await act(async () => {
    navigate("/s/local%3Alive-a");
  });
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-a"));
  await waitFor(() => {
    expect(workspaceStore.getState().focusedPaneId).toBe(paneAtPress);
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });

  // The stale completion must NOT navigate: the demand left with the route
  // it was pressed on, and the epoch has since moved.
  const params = deferred.params;
  const resolve = deferred.resolve;
  if (!params || !resolve) throw new Error("page-two request was not issued");
  await act(async () => {
    resolve(wireSnapshot(params, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"'));
  });
  expect(window.location.pathname).toBe("/s/local%3Alive-a");

  // And the chord is not permanently bricked by the inert completion: page
  // two is loaded now, so the same press takes the direct step to B.
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-b"));
});

// Round 11, medium 1: the leave-and-return round trip also stranding a
// SECOND press pressed while the demand is still in flight. The dedupe must
// not treat the in-flight demand as live: its recorded owner's guards are
// stale, so the newer press ADOPTS the pending load by rebinding fresh
// guards, and the load's completion navigates under the returned user.
// Under the old set-based dedupe the second press silently deduped against
// the dead demand, whose completion then went inert - the keypress produced
// no navigation at all (roborev PR #1044 round-11 medium 1).
test("a second press adopts an in-flight demand whose guards went stale", async () => {
  let pageTwoLoads = 0;
  const deferred: { params: NavigationReadParams | null; resolve: ((r: NavigationReadResponse) => void) | null } = {
    params: null,
    resolve: null,
  };
  const client = navClientWithDeferredLivePageTwo({
    onPageTwo: (params) =>
      new Promise<NavigationReadResponse>((resolve) => {
        pageTwoLoads++;
        deferred.params = params;
        deferred.resolve = resolve;
      }),
  });
  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await rail().findByText("Live A");

  await user.click(rail().getByText("Live A"));
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });
  const paneAtPress = workspaceStore.getState().focusedPaneId;
  // The route-placement effect re-focuses the session pane on the return
  // leg only when the location resource is present, so the test installs it
  // exactly as the app's own location lookup would have.
  act(() => installLocationForRoute("local:live-a"));

  // Press one: demand in flight from A (the last loaded live row).
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  await waitFor(() => expect(pageTwoLoads).toBe(1));

  // Leave and return: the pane id, focused ref, and pathname all read
  // identical to press time - only the route epoch knows the user left, so
  // the recorded owner's guards are stale.
  await act(async () => {
    navigate("/");
  });
  await waitFor(() => expect(window.location.pathname).toBe("/"));
  await act(async () => {
    navigate("/s/local%3Alive-a");
  });
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-a"));
  await waitFor(() => {
    expect(workspaceStore.getState().focusedPaneId).toBe(paneAtPress);
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });

  // Press two at the same boundary: it adopts the in-flight load. The
  // revalidator dedupes the concurrent read of the same page, so the
  // adoption issues no second request.
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  expect(pageTwoLoads).toBe(1);

  // The load lands: the displaced owner no-ops on its map-identity check
  // and the adopter's completion navigates to the newly loaded row.
  const params = deferred.params;
  const resolve = deferred.resolve;
  if (!params || !resolve) throw new Error("page-two request was not issued");
  await act(async () => {
    resolve(wireSnapshot(params, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"'));
  });
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-b"));
});
