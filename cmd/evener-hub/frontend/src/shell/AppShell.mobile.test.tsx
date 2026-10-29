import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { NavigationReadParams, NavigationReadResponse, NavigationSessionLocation } from "@evener/appwire-client";
import { APPWIRE_PROTOCOL_VERSION, type ConnectionState } from "@evener/appwire-client";
import { bindFilePath } from "@evener/appwire-client/docContent";
import { keyID } from "@evener/appwire-client/state/navigation";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { publishComicPaneCoverage } from "../comic/comicPaneCoverage";
import { initNotifications, resetNotificationsForTests } from "../notifications";
import { openDocBeside } from "../panes/doc/openDoc";
import { OpenTranscriptButton } from "../panes/session/transcript/openTranscript";
import { StubResizeObserver } from "../resizeObserverTestUtils";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import { resetAuthStatusesStoreForTests } from "../stores/authStatuses";
import { connectionStore } from "../stores/connection";
import { navigationStore, resetNavigationStoreForTests } from "../stores/navigation/store";
import { resetPrefsStoreForTests } from "../stores/prefs";
import { activityDetailsThread, answerActivityRead } from "../stores/sessionActivityTestUtils";
import { resetSettingsHostForTests } from "../stores/settingsHost";
import { resetSettingsOverviewStoreForTests } from "../stores/settingsOverview";
import { readModuleCss } from "../styles/cssBlock";
import { AppShell } from "./AppShell";
import { installMobileViewport, installSwitchableViewport, threadStartResponse } from "./appShellTestUtils";
import { paletteStore } from "./palette/paletteController";
import { navigate } from "./routing";
import { getDockviewApi, resetWorkspaceStoreForTests, workspaceStore } from "./workspace";

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

const paneFor = (ref: string) =>
  workspaceStore.getState().panes.find((p) => (p.params as { ref?: string }).ref === ref);

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

// --- mobile full-bleed shell (2026-07-30-mobile-session-layout-design.md, decision 1) ---
// jsdom performs no layout, so media-query rules are verified by reading the
// CSS module's own source, the same way panescaffold.test.tsx verifies its
// truncation rule.

test("mobile: the shell content frame drops its padding so the workspace is full-bleed", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "AppShell.module.css"), "utf8");
  const mobile = css.match(/@media \(max-width: 899px\) \{([\s\S]*?)\n\}/);
  expect(mobile).not.toBeNull();
  const contentRule = mobile![1]!.match(/\.content \{([^}]*)\}/);
  expect(contentRule).not.toBeNull();
  expect(contentRule![1]).toContain("padding: 0");
  // The flush sidebar is the rule at EVERY width now, so the mobile block
  // carries no gap reset of its own; a gap: here would mean someone added
  // one back on .content above, which the full-bleed contract test forbids.
  expect(contentRule![1]).not.toContain("gap:");
});

test("mobile: the shell spends --keyboard-inset as bottom padding so the composer clears the on-screen keyboard", () => {
  // See useKeyboardInset.ts's header for the mechanism. Comments are stripped
  // before matching (testing.md: a comment quoting the declaration must not
  // satisfy the assertion).
  const css = readModuleCss(import.meta.url, "AppShell.module.css");
  const mobile = css.match(/@media \(max-width: 899px\) \{([\s\S]*?)\n\}/);
  expect(mobile).not.toBeNull();
  const shellRule = mobile![1]!.match(/\.shell \{([\s\S]*?)\n[ ]{2}\}/);
  expect(shellRule).not.toBeNull();
  expect(shellRule![0]).toContain("padding-bottom: var(--keyboard-inset, 0px)");
});

test("mobile: the shared shell follows the visible viewport while retaining a vh fallback", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "AppShell.module.css"), "utf8");
  const shellRule = css.match(/(^|\n)\.shell \{([\s\S]*?)\n\}/);
  expect(shellRule).not.toBeNull();
  expect(shellRule![0]).toContain("height: 100vh");
  expect(shellRule![0]).not.toContain("height: 100dvh");

  const supportsBlock = css.match(/@supports \(height: 100dvh\) \{([\s\S]*?)\n\}/);
  expect(supportsBlock).not.toBeNull();
  expect(supportsBlock!.index).toBeGreaterThan(shellRule!.index ?? -1);

  const supportsShellRule = supportsBlock![1]!.match(/\.shell \{([\s\S]*?)\n[ ]{2}\}/);
  expect(supportsShellRule).not.toBeNull();
  expect(supportsShellRule![0]).toContain("height: 100dvh");
  expect(supportsShellRule![0]).not.toContain("height: 100vh");
});

// --- useKeyboardInset wiring (pins the AppShell -> hook call) --------------
// The hook's own tests pin that it sets --keyboard-inset given a visualViewport,
// and the CSS test above pins that the mobile .shell rule consumes it, but
// nothing connects the two through the real component: jsdom has no
// visualViewport, so every AppShell render in this file never exercises it.
// This test stubs one and renders the real AppShell to close that gap.

test("AppShell mounts useKeyboardInset, so a visualViewport resize sets --keyboard-inset on the root", async () => {
  // A real EventTarget so the hook's add/removeEventListener round-trips; the
  // hook reads height/offsetTop/scale off the same object.
  class FakeVisualViewport extends EventTarget {
    height = 768;
    offsetTop = 0;
    scale = 1;
  }
  const fake = new FakeVisualViewport();
  vi.stubGlobal("visualViewport", fake);
  Object.defineProperty(window, "innerHeight", { value: 768, configurable: true, writable: true });
  const { unmount } = render(<AppShell client={new FakeClient("ready")} />);
  // Mount sets the initial value (0px: the fake viewport covers the layout).
  expect(document.documentElement.style.getPropertyValue("--keyboard-inset")).toBe("0px");
  // Keyboard opens: visualViewport shrinks; the hook writes the occluded strip.
  fake.height = 400;
  fake.dispatchEvent(new Event("resize"));
  expect(document.documentElement.style.getPropertyValue("--keyboard-inset")).toBe("368px");
  unmount();
  vi.unstubAllGlobals();
});

test("Comic child document keeps phone focus after reader unmount and Back returns to its containing pane", async () => {
  vi.stubGlobal("innerWidth", 390);
  installMobileViewport();
  installLocationForRoute("local:origin");
  window.history.pushState({}, "", "/s/local%3Aorigin");
  const client = navClient();
  client.on("thread/read", () => ({ thread: threadStartResponse("local:origin").thread }));
  render(<AppShell client={client} />);
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.type).toBe("session"));
  const source = workspaceStore.getState().mainPane()!;
  await act(async () => {
    workspaceStore.getState().openPane("session", { ref: "local:child" });
    workspaceStore.getState().focusPane(source.id);
  });
  const release = publishComicPaneCoverage(source, "local:origin", ["local:child"]);
  await act(async () => {
    openDocBeside({
      session: "local:child",
      sourcePaneId: source.id,
      reference: bindFilePath("docs/child.md", "/child")!,
    });
    release();
  });
  const doc = workspaceStore.getState().panes.find((pane) => pane.type === "doc")!;
  expect(workspaceStore.getState().focusedPaneId).toBe(doc.id);
  await act(async () => {
    installLocationForRoute("local:origin");
  });
  expect(workspaceStore.getState().focusedPaneId).toBe(doc.id);
  await userEvent.click(await screen.findByRole("button", { name: "Back" }));
  expect(workspaceStore.getState().focusedPaneId).toBe(source.id);
});

test("ordinary mobile toolbar Back publishes only the Welcome transition", async () => {
  vi.stubGlobal("innerWidth", 390);
  installMobileViewport();
  installLocationForRoute("local:origin");
  window.history.pushState({}, "", "/s/local%3Aorigin");
  const client = navClient();
  client.on("thread/read", () => ({ thread: threadStartResponse("local:origin").thread }));
  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await screen.findByRole("textbox", { name: "Message" });
  expect(workspaceStore.getState().mainPane()).toMatchObject({
    type: "session",
    params: { ref: "local:origin" },
  });
  expect(window.location.pathname).toBe("/s/local%3Aorigin");
  expect(getDockviewApi()).toBeNull();

  const events: { method: "push" | "replace"; path: string }[] = [];
  const pushState = window.history.pushState.bind(window.history);
  const replaceState = window.history.replaceState.bind(window.history);
  const push = vi.spyOn(window.history, "pushState").mockImplementation((...args) => {
    pushState(...args);
    events.push({ method: "push", path: window.location.pathname });
  });
  const replace = vi.spyOn(window.history, "replaceState").mockImplementation((...args) => {
    replaceState(...args);
    events.push({ method: "replace", path: window.location.pathname });
  });
  try {
    await user.click(screen.getByRole("button", { name: "Back" }));
    await waitFor(() => {
      const workspace = workspaceStore.getState();
      expect(workspace.panes.find((pane) => pane.id === workspace.focusedPaneId)?.type).toBe("welcome");
      expect(window.location.pathname).toBe("/");
      expect(document.querySelector("[data-stacked-pane-type]")?.getAttribute("data-stacked-pane-type")).toBe(
        "welcome",
      );
    });
    expect(screen.queryByRole("button", { name: "Back" })).toBeNull();
    expect(events).toEqual([{ method: "push", path: "/" }]);
  } finally {
    push.mockRestore();
    replace.mockRestore();
  }
});

test("mobile unencoded session mount still reconciles later location ownership", async () => {
  vi.stubGlobal("innerWidth", 390);
  installMobileViewport();
  installLocationForRoute("local:origin");
  window.history.pushState({}, "", "/s/local:origin");
  const client = navClient();
  client.on("thread/read", () => ({ thread: threadStartResponse("local:origin").thread }));
  render(<AppShell client={client} />);
  await screen.findByRole("textbox", { name: "Message" });
  expect(window.location.pathname).toBe("/s/local%3Aorigin");
  expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:origin" });

  act(() => installLocationForRoute("local:origin", "local:owner"));
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:owner" }));
  expect(paneFor("local:origin")).toMatchObject({ type: "session", slot: "secondary" });
  expect(workspaceStore.getState().focusedPaneId).toBe(paneFor("local:origin")?.id);
  expect(window.location.pathname).toBe("/s/local%3Aorigin");
});

test.each(["phone", "desktop"])(
  "%s keeps an opened child focused after the already placed parent location settles",
  async (host) => {
    if (host === "phone") {
      vi.stubGlobal("innerWidth", 390);
      installMobileViewport();
    }
    let releaseLocation = () => {};
    const locationReady = new Promise<void>((resolve) => {
      releaseLocation = resolve;
    });
    const client = navClient();
    // Opening the child conversation makes the phone's URL sync publish the
    // child's own /s/{ref} route, so its location must say the child is
    // nested under the settled owner - the same ancestry the thread fixtures
    // describe - or the route placement would treat it as a top-level session.
    installLocationForRoute("local:child");
    client.on("evener/navigation/read", async (params) => {
      if (params.resource === "location" && params.ref === "local:owner") await locationReady;
      return navigationRead(params);
    });
    client.on("thread/read", ({ ref }) => activityDetailsThread(ref, { name: ref }));
    client.on("thread/unsubscribe", () => ({}));
    client.on("evener/thread/activity/read", answerActivityRead);
    const user = userEvent.setup();
    render(
      <>
        <AppShell client={client} />
        <OpenTranscriptButton transcriptRef="local:child" parentRef="local:owner" label="Open child" />
      </>,
    );
    await screen.findByText("No session open");
    act(() => {
      workspaceStore.getState().openPane("session", { ref: "local:owner" });
      navigate("/s/local%3Aowner");
    });
    await screen.findByRole("textbox", { name: "Message" });
    await waitFor(() =>
      expect(client.calls).toContainEqual(
        expect.objectContaining({
          method: "evener/navigation/read",
          params: expect.objectContaining({ resource: "location", ref: "local:owner" }),
        }),
      ),
    );
    await act(async () => {
      releaseLocation();
      await navigationStore.getState().lookupLocation("local:owner");
    });
    const owner = workspaceStore.getState().mainPane();
    expect(owner?.params).toEqual({ ref: "local:owner" });
    await user.click(screen.getByRole("button", { name: "Open child" }));
    const child = paneFor("local:child");
    expect(child).toMatchObject({ type: "session", params: { ref: "local:child" } });
    expect(workspaceStore.getState().focusedPaneId).toBe(child?.id);
    expect(workspaceStore.getState().mainPane()).toEqual(owner);
    // On the phone the address bar names the focused conversation pane; the
    // desktop host never publishes a URL, so the owner's route stays.
    expect(window.location.pathname).toBe(host === "phone" ? "/s/local%3Achild" : "/s/local%3Aowner");
  },
);

// Back from a child opened on phone uses StackHost's own history. The walk
// below covers children opened on desktop, where the host swap discards it.
test("phone Back from an opened child returns to its owner", async () => {
  vi.stubGlobal("innerWidth", 390);
  installMobileViewport();
  const client = navClient();
  // The child conversation pane has a deep link, and the phone's URL sync
  // publishes it when the child is opened, so the child's location must be
  // loaded and say it is nested under its owner.
  installLocationForRoute("local:child");
  client.on("thread/read", ({ ref }) => {
    if (ref !== "local:owner" && ref !== "local:child") {
      throw new Error(`Unexpected transcript ref: ${ref}`);
    }
    return { thread: { ...threadStartResponse(ref).thread, name: ref } };
  });
  window.history.pushState({}, "", "/s/local%3Aowner");
  const user = userEvent.setup();
  render(
    <>
      <AppShell client={client} />
      <OpenTranscriptButton transcriptRef="local:child" parentRef="local:owner" label="Open child" />
    </>,
  );
  await screen.findByRole("textbox", { name: "Message" });
  const owner = paneFor("local:owner");
  expect(owner).toMatchObject({ type: "session", slot: "main", params: { ref: "local:owner" } });
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(owner?.id));
  expect(getDockviewApi()).toBeNull();

  await user.click(screen.getByRole("button", { name: "Open child" }));
  const child = paneFor("local:child");
  expect(child).toMatchObject({ type: "session", params: { ref: "local:child" } });
  expect(workspaceStore.getState().focusedPaneId).toBe(child?.id);
  expect(workspaceStore.getState().mainPane()).toEqual(owner);
  const back = await screen.findByRole("button", { name: "Back" });
  await waitFor(() => expect(screen.getByTestId("topbar-title").textContent).toBe("local:child"));
  expect(window.location.pathname).toBe("/s/local%3Achild");

  await user.click(back);
  expect(workspaceStore.getState().focusedPaneId).toBe(owner?.id);
  expect(screen.getByTestId("topbar-title").textContent).toBe("local:owner");
  expect(paneFor("local:owner")).toEqual(owner);
  expect(workspaceStore.getState().mainPane()).toEqual(owner);
  expect(window.location.pathname).toBe("/s/local%3Aowner");
  expect(screen.queryByText("No session open")).toBeNull();
});

// Back must use retained immediate-parent context even when Open ran before
// StackHost mounted. A fresh host falling through to Welcome instead of that
// parent is the bug.
test.each(["local:owner", "local:child"])(
  "responsive retained-parent walk: mixed chain from routed session %s keeps exact parents and owner",
  async (routeRef) => {
    vi.stubGlobal("innerWidth", 1280);
    const setMobile = installSwitchableViewport();
    window.history.pushState({}, "", `/s/${encodeURIComponent(routeRef)}`);
    installLocationForRoute(routeRef);
    // Every conversation pane the funnel opens is deep-linkable, and the
    // phone's URL sync publishes each one's route, so each descendant needs a
    // location naming the owner as its top level.
    installLocationForRoute("local:child");
    installLocationForRoute("local:grandchild", "local:owner");
    installLocationForRoute("local:great-grandchild", "local:owner");
    const client = navClient();
    client.on("thread/read", ({ ref }) => {
      if (
        typeof ref !== "string" ||
        !["local:owner", "local:child", "local:grandchild", "local:great-grandchild"].includes(ref)
      ) {
        throw new Error(`Unexpected transcript ref: ${ref}`);
      }
      return { thread: { ...threadStartResponse(ref).thread, name: ref } };
    });
    const user = userEvent.setup();
    render(
      <>
        <AppShell client={client} />
        <OpenTranscriptButton transcriptRef="local:child" parentRef="local:owner" label="Open child" />
        <OpenTranscriptButton transcriptRef="local:grandchild" parentRef="local:child" label="Open grandchild" />
        <OpenTranscriptButton
          transcriptRef="local:great-grandchild"
          parentRef="local:grandchild"
          label="Open great-grandchild"
        />
      </>,
    );
    await screen.findByRole("heading", { name: routeRef });
    await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(paneFor(routeRef)?.id));
    const owner = paneFor("local:owner");
    expect(owner).toMatchObject({ type: "session", slot: "main", params: { ref: "local:owner" } });
    expect(getDockviewApi()).not.toBeNull();
    if (routeRef === "local:owner") await user.click(screen.getByRole("button", { name: "Open child" }));
    const child = paneFor("local:child");
    expect(child).toMatchObject({
      type: "session",
      slot: "secondary",
      params: { ref: "local:child" },
    });
    await user.click(screen.getByRole("button", { name: "Open grandchild" }));
    const grandchild = paneFor("local:grandchild");
    expect(grandchild).toMatchObject({
      type: "session",
      slot: "secondary",
      params: { ref: "local:grandchild" },
    });
    await user.click(screen.getByRole("button", { name: "Open great-grandchild" }));
    const descendant = paneFor("local:great-grandchild");
    expect(descendant).toMatchObject({
      type: "session",
      slot: "secondary",
      params: { ref: "local:great-grandchild" },
    });
    expect(workspaceStore.getState().focusedPaneId).toBe(descendant?.id);
    const retained = workspaceStore.getState().panes;
    act(() => {
      vi.stubGlobal("innerWidth", 390);
      setMobile(true);
    });
    await waitFor(() => expect(screen.getByTestId("topbar-title").textContent).toBe("local:great-grandchild"));
    expect(getDockviewApi()).toBeNull();
    expect(workspaceStore.getState().focusedPaneId).toBe(descendant?.id);
    // Literal return order is independent of the production parent resolver.
    const returnTargets = [
      { target: grandchild, title: "local:grandchild" },
      { target: child, title: "local:child" },
      // A routed child carries the same recorded parent context a drilled
      // one does, so Back returns to the retained owner before Welcome.
      { target: owner, title: "local:owner" },
    ];
    for (const { target, title } of returnTargets) {
      expect(target).toBeDefined();
      await user.click(screen.getByRole("button", { name: "Back" }));
      expect(workspaceStore.getState().focusedPaneId).toBe(target?.id);
      expect(screen.getByTestId("topbar-title").textContent).toBe(title);
      expect(workspaceStore.getState().panes).toEqual(retained);
      expect(workspaceStore.getState().mainPane()).toEqual(owner);
      // The address bar names the focused conversation pane on the phone;
      // the nested location reconciles without disturbing the placement.
      expect(window.location.pathname).toBe(`/s/${encodeURIComponent(title)}`);
    }
    await user.click(screen.getByRole("button", { name: "Back" }));
    expect(await screen.findByText("No session open")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Back" })).toBeNull();
  },
);

test("Opening the conversation retains focused child after settled parent route reconciliation", async () => {
  vi.stubGlobal("innerWidth", 390);
  installMobileViewport();
  window.history.pushState({}, "", "/s/local:owner");
  installLocationForRoute("local:owner");
  // The opened conversation pane is deep-linkable and the phone's URL sync
  // publishes its route, so the child's location must be loaded and say it
  // is nested under the owner.
  installLocationForRoute("local:child");
  const user = userEvent.setup();

  render(
    <>
      <AppShell client={navClient()} />
      <OpenTranscriptButton transcriptRef="local:child" parentRef="local:owner" />
    </>,
  );
  await screen.findByText(/loading transcript/i);
  const parent = paneFor("local:owner");
  expect(parent).toMatchObject({ type: "session", slot: "main", params: { ref: "local:owner" } });
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(parent?.id));

  // userEvent's pointer interaction is wrapped in React act, so the assertion
  // below observes the AppShell workspace-panes effect, not the handler's
  // immediate pre-effect focus.
  const openButton = screen.getByRole("button", { name: "Open conversation" });
  await user.click(openButton);

  const child = workspaceStore
    .getState()
    .panes.find((pane) => pane.type === "session" && (pane.params as { ref?: unknown }).ref === "local:child");
  expect(child).toMatchObject({ type: "session", slot: "secondary" });
  expect(workspaceStore.getState().focusedPaneId).toBe(child?.id);

  await user.click(openButton);
  const repeatedChildren = workspaceStore
    .getState()
    .panes.filter((pane) => pane.type === "session" && (pane.params as { ref?: unknown }).ref === "local:child");
  expect(repeatedChildren).toHaveLength(1);
  expect(repeatedChildren[0]?.id).toBe(child?.id);
  expect(workspaceStore.getState().focusedPaneId).toBe(child?.id);
});

test("Opening the conversation retains focused child on desktop until explicit navigation takes precedence", async () => {
  window.history.pushState({}, "", "/s/local:owner");
  installLocationForRoute("local:owner");
  const user = userEvent.setup();

  render(
    <>
      <AppShell client={navClient()} />
      <OpenTranscriptButton transcriptRef="local:child" parentRef="local:owner" />
    </>,
  );
  await screen.findByText(/loading transcript/i);
  const parent = paneFor("local:owner");
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(parent?.id));

  await user.click(screen.getByRole("button", { name: "Open conversation" }));
  const child = workspaceStore
    .getState()
    .panes.find((pane) => pane.type === "session" && (pane.params as { ref?: unknown }).ref === "local:child");
  expect(workspaceStore.getState().focusedPaneId).toBe(child?.id);

  act(() => {
    window.history.pushState({}, "", "/s/local:next");
    installLocationForRoute("local:next");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:next" }));
  expect(workspaceStore.getState().focusedPaneId).toBe(workspaceStore.getState().mainPane()?.id);
  expect(paneFor("local:child")).toBeUndefined();
});

test("Opening a nested conversation's job log retains focus on the settled parent route", async () => {
  window.history.pushState({}, "", "/s/local:owner");
  installLocationForRoute("local:owner");
  const user = userEvent.setup();

  render(
    <>
      <AppShell client={navClient()} />
      <OpenTranscriptButton transcriptRef="local:child" parentRef="local:owner" label="Open child" />
      <OpenTranscriptButton transcriptRef="job:child-run" parentRef="local:child" label="Open job log" />
    </>,
  );
  await screen.findByText(/loading transcript/i);
  const parent = paneFor("local:owner");
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(parent?.id));

  await user.click(screen.getByRole("button", { name: "Open child" }));
  const child = workspaceStore
    .getState()
    .panes.find((pane) => pane.type === "session" && (pane.params as { ref?: unknown }).ref === "local:child");
  expect(workspaceStore.getState().focusedPaneId).toBe(child?.id);

  // The child's job log: its parent context follows the child's recorded
  // conversation parent (the routed owner), so the settled route must treat
  // the focused job log as a companion instead of refocusing the owner.
  await user.click(screen.getByRole("button", { name: "Open job log" }));
  const job = paneFor("job:child-run");
  expect(job).toMatchObject({ type: "transcript", slot: "secondary" });
  expect(workspaceStore.getState().focusedPaneId).toBe(job?.id);
});

test.each(["local:owner", "local:child"])(
  "Opening the conversation retains focused grandchild on route %s with owner and immediate parent context",
  async (routeRef) => {
    vi.stubGlobal("innerWidth", 390);
    installMobileViewport();
    window.history.pushState({}, "", `/s/${routeRef}`);
    installLocationForRoute(routeRef);
    // Every conversation pane the funnel opens is deep-linkable, and the
    // phone's URL sync publishes each one's route, so each descendant needs a
    // location naming the owner as its top level.
    installLocationForRoute("local:child");
    installLocationForRoute("local:grandchild", "local:owner");
    installLocationForRoute("local:great-grandchild", "local:owner");
    const user = userEvent.setup();
    render(
      <>
        <AppShell client={navClient()} />
        {routeRef === "local:owner" && (
          <OpenTranscriptButton transcriptRef="local:child" parentRef="local:owner" label="Open child" />
        )}
        <OpenTranscriptButton transcriptRef="local:grandchild" parentRef="local:child" />
      </>,
    );
    await screen.findAllByText(/loading transcript/i);
    await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(paneFor(routeRef)?.id));
    const owner = workspaceStore.getState().mainPane();
    expect(owner).toMatchObject({ type: "session", params: { ref: "local:owner" } });
    if (routeRef === "local:owner") {
      await user.click(screen.getByRole("button", { name: "Open child" }));
      expect(workspaceStore.getState().focusedPaneId).toBe(paneFor("local:child")?.id);
    }
    const parent = paneFor("local:child");
    await user.click(screen.getByRole("button", { name: "Open conversation" }));
    const grandchild = workspaceStore
      .getState()
      .panes.find((pane) => pane.type === "session" && (pane.params as { ref?: unknown }).ref === "local:grandchild");
    expect(grandchild).toBeDefined();
    expect(workspaceStore.getState().focusedPaneId).toBe(grandchild?.id);
    expect(grandchild).toMatchObject({
      slot: "secondary",
      params: { ref: "local:grandchild" },
    });
    expect(workspaceStore.getState().mainPane()).toEqual(owner);
    expect(paneFor("local:child")).toEqual(parent);
    if (routeRef === "local:owner") expect(parent?.params).toEqual({ ref: "local:child" });

    await user.click(screen.getByRole("button", { name: "Open conversation" }));
    expect(
      workspaceStore
        .getState()
        .panes.filter(
          (pane) => pane.type === "session" && (pane.params as { ref?: unknown }).ref === "local:grandchild",
        ),
    ).toEqual([grandchild]);
    expect(workspaceStore.getState().focusedPaneId).toBe(grandchild?.id);

    await user.click(screen.getByRole("button", { name: "Back" }));
    await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(parent?.id));
    expect(workspaceStore.getState().mainPane()).toEqual(owner);
    await user.click(screen.getByRole("button", { name: "Open conversation" }));
    expect(workspaceStore.getState().focusedPaneId).toBe(grandchild?.id);

    // An explicit new nested route wins even when it shares the same owner.
    act(() => {
      window.history.pushState({}, "", "/s/local:next-child");
      installLocationForRoute("local:next-child");
      const resource = navigationStore.getState().resources.get(keyID({ kind: "location", ref: "local:next-child" }));
      installLocation({
        ...(resource?.data as NavigationSessionLocation),
        top_level: false,
        top_level_ref: "local:owner",
      });
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(paneFor("local:next-child")?.id));
    expect(workspaceStore.getState().mainPane()).toEqual(owner);
    expect(workspaceStore.getState().focusedPaneId).not.toBe(grandchild?.id);
  },
);

test.each([
  ["local:owner", "local:other-owner"],
  ["local:child", "local:other-owner"],
  ["local:child", "local:owner"],
])("settled route %s rejects an unrelated focused transcript context under %s", async (routeRef, parentRef) => {
  window.history.pushState({}, "", `/s/${routeRef}`);
  installLocationForRoute(routeRef);
  render(<AppShell client={navClient()} />);
  await screen.findAllByText(/loading transcript/i);
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(paneFor(routeRef)?.id));
  let unrelated!: string;
  act(() => {
    unrelated = workspaceStore
      .getState()
      .openPane("transcript", { ref: "local:unrelated", parentRef }, { slot: "secondary" });
    workspaceStore.getState().focusPane(unrelated);
  });
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(paneFor(routeRef)?.id));
  expect(workspaceStore.getState().focusedPaneId).not.toBe(unrelated);
  expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:owner" });
});

test.each(["local:owner", "local:child"])(
  "Open next descendant retains mixed-chain context on route %s",
  async (routeRef) => {
    vi.stubGlobal("innerWidth", 390);
    installMobileViewport();
    window.history.pushState({}, "", `/s/${routeRef}`);
    installLocationForRoute(routeRef);
    // Every conversation pane the funnel opens is deep-linkable, and the
    // phone's URL sync publishes each one's route, so each descendant needs a
    // location naming the owner as its top level.
    installLocationForRoute("local:child");
    installLocationForRoute("local:grandchild", "local:owner");
    installLocationForRoute("local:great-grandchild", "local:owner");
    const user = userEvent.setup();
    render(
      <>
        <AppShell client={navClient()} />
        {routeRef === "local:owner" && (
          <OpenTranscriptButton transcriptRef="local:child" parentRef="local:owner" label="Open child" />
        )}
        <OpenTranscriptButton transcriptRef="local:grandchild" parentRef="local:child" />
        <OpenTranscriptButton
          transcriptRef="local:great-grandchild"
          parentRef="local:grandchild"
          label="Open next descendant"
        />
      </>,
    );
    await screen.findAllByText(/loading transcript/i);
    await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(paneFor(routeRef)?.id));
    if (routeRef === "local:owner") {
      await user.click(screen.getByRole("button", { name: "Open child" }));
      expect(workspaceStore.getState().focusedPaneId).toBe(paneFor("local:child")?.id);
    }
    await user.click(screen.getByRole("button", { name: "Open conversation" }));
    const grandchild = workspaceStore
      .getState()
      .panes.find((pane) => pane.type === "session" && (pane.params as { ref?: unknown }).ref === "local:grandchild");
    expect(grandchild).toBeDefined();
    expect(workspaceStore.getState().focusedPaneId).toBe(grandchild?.id);
    const originalOwner = workspaceStore.getState().mainPane();
    expect(originalOwner).toMatchObject({ type: "session", params: { ref: "local:owner" } });
    await user.click(screen.getByRole("button", { name: "Open next descendant" }));
    const next = workspaceStore
      .getState()
      .panes.find(
        (pane) => pane.type === "session" && (pane.params as { ref?: unknown }).ref === "local:great-grandchild",
      );
    expect(next).toBeDefined();
    expect(workspaceStore.getState().focusedPaneId).toBe(next?.id);
    expect(workspaceStore.getState().mainPane()).toEqual(originalOwner);
    expect(next?.params).toEqual({ ref: "local:great-grandchild" });
    expect(paneFor("local:grandchild")).toEqual(grandchild);
    await user.click(screen.getByRole("button", { name: "Open next descendant" }));
    expect(
      workspaceStore
        .getState()
        .panes.filter(
          (pane) => pane.type === "session" && (pane.params as { ref?: unknown }).ref === "local:great-grandchild",
        ),
    ).toEqual([next]);
    expect(workspaceStore.getState().focusedPaneId).toBe(next?.id);
    await user.click(screen.getByRole("button", { name: "Back" }));
    await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(grandchild?.id));
    expect(workspaceStore.getState().mainPane()).toEqual(originalOwner);
  },
);

test.each(["missing", "cycle"])(
  "Open next descendant preserves the workspace when %s context cannot resolve an owner",
  async (context) => {
    window.history.pushState({}, "", "/s/local:child");
    installLocationForRoute("local:child");
    render(
      <>
        <AppShell client={navClient()} />
        <OpenTranscriptButton transcriptRef="local:next" parentRef="local:detached" />
      </>,
    );
    await screen.findAllByText(/loading transcript/i);
    await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(paneFor("local:child")?.id));
    const originalMain = workspaceStore.getState().mainPane();
    act(() => {
      workspaceStore
        .getState()
        .openPane("transcript", { ref: "local:detached", parentRef: "local:bridge" }, { slot: "secondary" });
      if (context === "cycle")
        workspaceStore
          .getState()
          .openPane("transcript", { ref: "local:bridge", parentRef: "local:detached" }, { slot: "secondary" });
      fireEvent.click(screen.getByRole("button", { name: "Open conversation" }));
    });
    // The detached/cyclic context has no loaded location, so the owner cannot
    // be proven. The workspace is preserved: the retained main session stays,
    // and the transcript opens beside it rather than promoting an unproven
    // parent that would discard the restored panes.
    await waitFor(() => expect(paneFor("local:next")).toBeDefined());
    expect(workspaceStore.getState().mainPane()).toEqual(originalMain);
    expect(paneFor("local:next")?.params).toEqual({ ref: "local:next" });
  },
);

// A /s/{ref} route can remain deferred until the AppWire location read says
// whether the ref is nested, and no read can resolve inside the first commit.
// StackHost fills an empty stack with welcome and publishes the focused pane's
// URL, which used to overwrite the address bar with "/" during exactly that
// beat: the deep link was gone before the location arrived, and no later
// evener/changed push could name it again.
test("mobile: a /s/{ref} deep link still opens once the tree lands, instead of being overwritten by welcome", async () => {
  navigationStore.setState({ mode: "v3" });
  installMobileViewport();

  window.history.pushState({}, "", "/s/local:s1");
  render(<AppShell client={new FakeClient("ready")} />);

  // The mobile host settles on its welcome fallback while the location is
  // still unavailable, but must preserve the deep-link URL.
  await screen.findByText("No session open");
  expect(window.location.pathname).toBe("/s/local%3As1");

  act(() => installLocationForRoute("local:s1"));

  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:s1" }));
  // And the address bar now names it in paneToURL's own canonical form.
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3As1"));
});

test("crossing desktop → mobile → desktop preserves a focused panel and its return path", async () => {
  const setMobile = installSwitchableViewport();
  window.history.pushState({}, "", "/s/local:s1");
  installLocationForRoute("local:s1");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText(/loading transcript/i);

  let panelId!: string;
  act(() => {
    panelId = workspaceStore.getState().openPane("sessionDetails", { ref: "local:s1" }, { slot: "secondary" });
    workspaceStore.getState().focusPane(panelId);
  });
  await screen.findByRole("status", { name: "Loading session panel" });
  await waitFor(() => expect(getDockviewApi()?.panels.some((panel) => panel.id === panelId)).toBe(true));

  act(() => setMobile(true));
  expect(await screen.findByRole("status", { name: "Loading session panel" })).toBeTruthy();
  expect(workspaceStore.getState().focusedPaneId).toBe(panelId);
  // The scaffold header is display:none below 900px - StackHost's top-bar
  // Back is the VISIBLE mobile return path, wired to the parent session for
  // panel panes.
  expect(screen.getByRole("button", { name: "Back" })).toBeTruthy();

  act(() => {
    setMobile(false);
    // Still the mobile host, after the event but before React commits it.
    // DockRegion's shared lazy payload mounts DockHost immediately at commit.
    expect(getDockviewApi()).toBeNull();
  });
  await waitFor(() => expect(getDockviewApi()?.panels.some((panel) => panel.id === panelId)).toBe(true));
  expect(workspaceStore.getState().focusedPaneId).toBe(panelId);
  expect(getDockviewApi()).not.toBeNull();

  act(() => setMobile(true));
  await screen.findByRole("status", { name: "Loading session panel" });
  await userEvent.setup().click(screen.getByRole("button", { name: "Back" }));
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).not.toBe(panelId));
  expect(
    workspaceStore.getState().panes.find((pane) => pane.id === workspaceStore.getState().focusedPaneId)?.type,
  ).toBe("session");
});

// --- kata 098n: a /thread share link is not rewritten into /s ------------

// A /thread/{ref} share link is a single-pane route: isSinglePaneRoute
// (singlePane.ts) keys the chrome-stripped layout off the PATHNAME, so the
// mode survives only as long as the address bar keeps naming it. On mobile
// StackHost publishes the focused pane's URL, and a session pane serializes
// back to /s/{ref} - so the share link used to be rewritten out from under
// the user the moment the routed pane took focus, silently turning
// single-pane mode off. Desktop never rewrites it (DockHost writes no URL at
// all), which is the behaviour this pins mobile to.
test("kata 098n: on mobile a /thread/{ref} share link keeps its URL and its single-pane chrome", async () => {
  installMobileViewport();
  window.history.pushState({}, "", "/thread/local:s1");
  render(<AppShell client={new FakeClient("ready")} />);
  // Mobile never mounts RailHost, so there is no rail-owned navigation read;
  // the notification engine's baseline AppWire read is independent of that
  // chrome and already covers this path in production.
  await act(async () => {
    await Promise.resolve();
  });

  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:s1" }));
  await waitFor(() => expect(window.location.pathname).toBe("/thread/local:s1"));
  expect(document.querySelector("[data-single-pane]")).not.toBeNull();
  expect(screen.queryByRole("button", { name: "Sessions" })).toBeNull();
});

// --- kata p5w9: one boot, one baseline AppWire navigation read -------------------------

// A desktop boot has two consumers of the same navigation snapshot:
// initNotifications()'s baseline and the rail. Both must share the typed
// AppWire read seam. Publishing ready with its matching metadata must not
// trigger an extra connection event for the reconnect subscriber.
//
// The notifications engine is a module singleton already initialized by
// AppShell.tsx's own import, so a REAL boot is modelled by resetting and
// re-initializing it here - and it is deliberately left initialized
// afterwards, exactly as module evaluation leaves it for every other test in
// this file.
test("desktop boot uses the typed AppWire navigation read seam", async () => {
  const client = navClient();
  resetNotificationsForTests();
  initNotifications();
  render(<AppShell client={client} />);

  await screen.findByText("No session open");
  await waitFor(() => expect(navigationStore.getState().resources).not.toBeNull());
  await waitFor(() => expect(connectionStore.getState().serverInfo).toBeDefined());

  // evener/command/list is the command catalog's connection-driven read
  // (stores/commandCatalog.ts): a ready client loads it without waiting for
  // a palette open.
  // thread/read and thread/unsubscribe are the background Live roster owner's
  // additive membership reads (it discovers the complete Live section), which
  // run alongside the typed navigation reads this test guards.
  expect(
    client.calls.every(
      ({ method }) =>
        method === "evener/navigation/read" ||
        method === "evener/instance/list" ||
        method === "evener/command/list" ||
        method === "thread/read" ||
        method === "thread/unsubscribe",
    ),
  ).toBe(true);
  expect(client.calls).toContainEqual({
    method: "evener/navigation/read",
    params: { resource: "manifest", representationVersion: 3 },
  });
  // Required, not just allowed above: a regression that stops loading the
  // catalog on connect must fail this test, not slip past the allow-list.
  expect(client.calls).toContainEqual({ method: "evener/command/list", params: {} });
});
