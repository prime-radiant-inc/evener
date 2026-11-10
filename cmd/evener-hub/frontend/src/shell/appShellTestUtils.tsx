import type {
  NavigationReadParams,
  NavigationReadResponse,
  NavigationSessionLocation,
  NavigationSessionSummary,
  ThreadStartResponse,
} from "@evener/appwire-client";
import { APPWIRE_PROTOCOL_VERSION, type ConnectionState } from "@evener/appwire-client";
import { keyID } from "@evener/appwire-client/state/navigation";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { act, cleanup, render, screen, within } from "@testing-library/react";

import { vi } from "vitest";
import { navigationStore } from "../stores/navigation/store";
import { resetSettingsOverviewStoreForTests } from "../stores/settingsOverview";
import { AppShell } from "./AppShell";
import { MOBILE_QUERY } from "./useIsMobile";
import { resetWorkspaceStoreForTests, workspaceStore } from "./workspace";

// Session-title queries in this file target navigation, so keep them scoped to
// the rail rather than matching the same session in workspace content.
export const rail = () => within(screen.getByTestId("rail"));

export const THREAD_CAPABILITIES = {
  send: false,
  steer: false,
  interrupt: false,
  compact: false,
  clear: false,
  forkFromTurn: false,
  shutdown: false,
  changeModel: false,
  changeVisionModel: false,
  queue: false,
  goal: false,
  sharedNotes: false,
  rename: false,
};

export function threadStartResponse(ref: string): ThreadStartResponse {
  return {
    thread: {
      id: ref.slice(ref.indexOf(":") + 1),
      sessionId: `sess_${ref}`,
      preview: "test",
      ephemeral: false,
      modelProvider: "anthropic/claude-sonnet-4-5",
      createdAt: 1000,
      updatedAt: 1000,
      status: { type: "idle" },
      cwd: "/tmp/project",
      cliVersion: "1.0.0",
      source: "local",
      evener: { ref, capabilities: THREAD_CAPABILITIES, queue: { revision: 0 } },
    },
    turn: { id: "turn_1", itemsView: "full", status: "idle" },
  };
}

// A first location read that failed outright: no retained data, so the
// route's placement must decide from the workspace it already holds.
export function installFailedLocation(ref: string): void {
  const key = { kind: "location", ref } as const;
  const resources = new Map(navigationStore.getState().resources);
  resources.set(keyID(key), {
    key,
    data: null,
    loadedRevision: null,
    targetRevision: null,
    forceToken: 0,
    etag: null,
    loading: false,
    stale: false,
    error: new Error("location read failed"),
    generationID: "generation_test",
  });
  navigationStore.setState({ mode: "v3", resources });
}

// jsdom implements no matchMedia at all (useIsMobile.test.ts's own header
// comment documents the probe), so a mobile-layout test installs one: the
// mobile query matches, every other query does not. Only useIsMobile reads
// this surface, and only ever at mount here - no test in this file crosses
// the breakpoint mid-run, so the listeners are inert stubs.
export function installMobileViewport(): void {
  vi.stubGlobal(
    "matchMedia",
    vi.fn((media: string) => ({
      media,
      matches: media === MOBILE_QUERY,
      addEventListener: () => {},
      removeEventListener: () => {},
    })),
  );
}

export function installSwitchableViewport(): (mobile: boolean) => void {
  let mobile = false;
  const listeners = new Set<(event: MediaQueryListEvent) => void>();
  const mediaQuery = {
    media: MOBILE_QUERY,
    get matches() {
      return mobile;
    },
    addEventListener: (_type: string, listener: EventListenerOrEventListenerObject) => {
      listeners.add(listener as (event: MediaQueryListEvent) => void);
    },
    removeEventListener: (_type: string, listener: EventListenerOrEventListenerObject) => {
      listeners.delete(listener as (event: MediaQueryListEvent) => void);
    },
  } as MediaQueryList;
  vi.stubGlobal(
    "matchMedia",
    vi.fn((query: string) =>
      query === mediaQuery.media
        ? mediaQuery
        : { matches: false, media: query, addEventListener: () => {}, removeEventListener: () => {} },
    ),
  );
  return (nextMobile: boolean) => {
    mobile = nextMobile;
    const event = { matches: mobile, media: mediaQuery.media } as MediaQueryListEvent;
    for (const listener of listeners) listener(event);
  };
}

export const LIVE_CYCLE_B: NavigationSessionSummary = {
  ref: "local:live-b",
  host_id: "local",
  session_id: "live-b",
  title: "Live B",
  project: "prime-radiant",
  state: "idle",
  kind: "session",
  live: true,
  children: [],
};

export const TREE_SESSION = {
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

// When set, navigationRead serves TREE_SESSION as an ended, non-live session.
// Each test file runs in its own module registry, so this flag is per file;
// a file that sets it resets it in beforeEach.
let stoppedTreeSession = false;

export function setStoppedTreeSession(stopped: boolean): void {
  stoppedTreeSession = stopped;
}

export const EMPTY_NAV_RESPONSE = {
  generation_id: "generation_test",
  revision: 1,
  sources: [],
  attentionSummary: { needsYou: 0, error: 0, working: 0 },
  sections: { live: { count: 1 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
  catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
};

export function navigationRead(params: NavigationReadParams): NavigationReadResponse {
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
export function navClient(initialState: ConnectionState = "ready"): FakeClient {
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

export function installLocation(location: NavigationSessionLocation): void {
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

export function installLocationForRoute(ref: string, ownerRef?: string): void {
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

export function installNeedsYouRows(refs = ["local:ny1", "local:ny2"]): void {
  const key = { kind: "section", section: "needs_you", offset: 0, limit: 50 } as const;
  const rows = refs.map((ref, index) => ({
    ...TREE_SESSION,
    ref,
    session_id: ref,
    title: `Needs you ${index + 1}`,
    state: "awaiting",
  }));
  const resources = new Map(navigationStore.getState().resources);
  resources.set(keyID(key), {
    key,
    data: { generation_id: "generation_test", revision: 1, sessions: rows, remaining: 0, truncated: false },
    loadedRevision: 1,
    targetRevision: null,
    forceToken: 0,
    etag: '"test"',
    loading: false,
    stale: false,
    error: null,
    generationID: "generation_test",
  });
  const visibleResourceKeys = new Set(navigationStore.getState().visibleResourceKeys);
  visibleResourceKeys.add(keyID(key));
  navigationStore.setState({ mode: "v3", resources, visibleResourceKeys });
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

export async function warmRoute(
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

export const LIVE_CYCLE_A: NavigationSessionSummary = {
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
export function navClientWithLive(sessions: NavigationSessionSummary[]): FakeClient {
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

// The zoom system promotes a session pane into a cascade by retyping it in
// place (panes/zoom/actions.ts) - same pane id, same slot - so this is the
// exact main a saved layout restores. Edges are the saved cascade path.
export function retypeMainToCascade(
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
