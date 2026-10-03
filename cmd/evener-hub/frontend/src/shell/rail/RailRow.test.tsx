import type { EvenerWatchInfo, NavigationManifest } from "@evener/appwire-client";
import {
  keyID,
  type NormalizedResource,
  navigationOwnedContainerKey,
  navigationRootContainerKey,
  navigationViewScope,
  normalizedGraphFromSnapshot,
  type ResourceKey,
  type ResourceState,
} from "@evener/appwire-client/state/navigation";
import { manifest } from "@evener/appwire-client/testing/navigation";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { lazy } from "react";
import { afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";
import { installMobileViewport } from "../../panes/session/testing/mobileViewport";
import { selectRailModel } from "../../stores/navigation/selectors";
import { navigationStore, resetNavigationStoreForTests } from "../../stores/navigation/store";
import { resetThreadsStoreForTests } from "../../stores/threads";
import { blockBody, mediaBlock, readModuleCss, topRuleBlock } from "../../styles/cssBlock";
import { LONG_PRESS_MS } from "../../widgets/hovercard";
import { installHoverlessMatchMedia } from "../../widgets/hovercard/hovercardTestUtils";
import { hoverForTooltip } from "../../widgets/tooltip/tooltipTestUtils";
import { Tree, type TreeRowInfo } from "../../widgets/tree";
import { activitySidebarStore, resetActivitySidebarStoreForTests } from "../activitybar/activitySidebarStore";
import { registerPaneForTests } from "../paneRegistry";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import {
  cadenceStateFor,
  watchGloss as domainWatchGloss,
  RailRow,
  type RailRowActions,
  watchCadenceLabel,
  watchDurationLabel,
} from "./RailRow";
import railStyles from "./RailRow.module.css";
import type {
  HostRailNode,
  LoadingRailNode,
  OverflowRailNode,
  ProjectRailNode,
  RailProject,
  RailSession,
  SessionRailNode,
} from "./railNodes";
import { RailTickProvider } from "./railNow";
import { RailRenderObserver } from "./railRenderObserver";

const RAIL_CSS = readModuleCss(import.meta.url, "RailRow.module.css").replace(/\/\*[\s\S]*?\*\//g, " ");

function nestedRuleBlock(css: string, selector: string): string {
  const selectorStart = css.indexOf(selector);
  if (selectorStart === -1) throw new Error(`RailRow.module.css is missing ${selector}`);
  const openBrace = css.indexOf("{", selectorStart);
  if (openBrace === -1) throw new Error(`RailRow.module.css has no block for ${selector}`);
  return blockBody(css, openBrace);
}

// "Pin this session…" mounts the real PinSectionPicker, which reads
// pin sections from the navigation store's bounded pin-catalog resource
// (loadPinCatalogPages + selectPinSections). Seed the store with a pin_catalog resource and
// stub loadPinCatalogPages so the picker's mount effect resolves without a
// real network fetch.
const pinKey = { kind: "pin_catalog" as const, offset: 0, limit: 100 };
const generation = "generation_test";

type LoadPinCatalogPages = (force?: boolean) => Promise<void>;

function seedPinCatalogForPicker(): void {
  const resource: ResourceState = {
    key: pinKey,
    data: {
      generation_id: generation,
      revision: 1,
      pin_sections: [{ id: "sec_1", name: "Client", count: 0 }],
      remaining: 0,
    },
    loadedRevision: 1,
    targetRevision: 1,
    forceToken: 0,
    etag: "a",
    loading: false,
    stale: false,
    error: null,
    generationID: generation,
  };
  navigationStore.setState({ mode: "v3", resources: new Map([[keyID(resource.key), resource]]) });
  navigationStore.setState({ loadPinCatalogPages: vi.fn(async () => undefined) as LoadPinCatalogPages });
}

// Seeds the manifest's launch sources, which a row's host badge/offline
// affordance derives from (Component 06b). A row whose host the manifest does
// not name reads as online, so the default empty manifest adds no affordance.
function seedSources(
  sources: NavigationManifest["sources"],
  overrides: Partial<ResourceState<NavigationManifest>> = {},
): void {
  const resource: ResourceState<NavigationManifest> = {
    key: { kind: "manifest" },
    data: manifest({ sources }),
    loadedRevision: 1,
    targetRevision: null,
    forceToken: 0,
    etag: "e",
    loading: false,
    stale: false,
    error: null,
    generationID: generation,
    ...overrides,
  };
  navigationStore.setState({ manifest: resource });
}

function PaneFixture() {
  return <div>pane</div>;
}

beforeAll(() => {
  // Minimal, test-only pane registrations (TreeDrawer.test.tsx's precedent):
  // the workspace store's openPane refuses an unregistered type.
  registerPaneForTests<{ ref: string }>({
    id: "session",
    title: () => "Session",
    component: lazy(() => Promise.resolve({ default: PaneFixture })),
  });
  for (const id of ["sessionTasks", "sessionDetails"] as const) {
    registerPaneForTests<{ ref: string }>({
      id,
      title: () => id,
      component: lazy(() => Promise.resolve({ default: PaneFixture })),
    });
  }
});

beforeEach(() => {
  resetWorkspaceStoreForTests();
  resetNavigationStoreForTests();
  resetThreadsStoreForTests();
  seedPinCatalogForPicker();
  resetActivitySidebarStoreForTests();
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  resetNavigationStoreForTests();
  resetThreadsStoreForTests();
});

function apiNode(overrides: Partial<RailSession> = {}): RailSession {
  return {
    row_id: "project:p1:local:a",
    ref: "local:a",
    host_id: "local",
    session_id: "a",
    title: "Fix flaky test",
    project: "Proj",
    state: "idle",
    kind: "session",
    live: true,
    children: [],
    ...overrides,
  };
}

/** The updated_at anchor a fixture's relative age is measured from: an ISO
 * instant `minutes` before now. Zero is "just updated", which reads as "now". */
function minutesAgo(minutes: number): string {
  return new Date(Date.now() - minutes * 60_000).toISOString();
}

function apiProject(overrides: Partial<RailProject> = {}): RailProject {
  return {
    key: "p1",
    name: "Proj",
    sessions: [],
    more_current: 0,
    more_recent: 0,
    more_archived: 0,
    loaded: false,
    nextOffsets: {},
    ...overrides,
  };
}

/** A project summary stamped with the owning sources the navigation read model
 * carries (component 06a's field; RailProject's own interface on this branch
 * predates it, so the row reads it structurally). */
function withSources(project: RailProject, sources: string[]): RailProject {
  return { ...project, sources } as RailProject;
}

function sessionRailNode(session: RailSession, overrides: Partial<SessionRailNode> = {}): SessionRailNode {
  return { id: session.row_id, kind: "session", session, expanded: false, children: [], ...overrides };
}

function projectRailNode(project: RailProject, children: ProjectRailNode["children"] = []): ProjectRailNode {
  return { id: `projectnode:${project.key}`, kind: "project", project, expanded: false, children };
}

function loadingRailNode(): LoadingRailNode {
  return { id: "loading-1", kind: "loading" };
}

function overflowRailNode(count: number): OverflowRailNode {
  return { id: "projectnode:p1:overflow", kind: "overflow", count, pages: [] };
}

function hostGroupNode(overrides: Partial<HostRailNode> = {}): HostRailNode {
  return {
    id: "host:devbox",
    kind: "host",
    host: { id: "devbox", label: "devbox", online: true },
    expanded: true,
    children: [],
    ...overrides,
  };
}

function watchGloss(info: EvenerWatchInfo): string {
  return domainWatchGloss({
    ownerRef: "ref_owner",
    sourceRef: "ref_owner",
    receiverRef: "ref_owner",
    state: info.active ? "armed" : "ended",
    watch: info,
  });
}

function watchSummary(overrides: Partial<EvenerWatchInfo> = {}): EvenerWatchInfo {
  return {
    id: "watch-1",
    source: "self",
    deliveries: 0,
    createdAt: "2026-09-12T19:00:00Z",
    active: true,
    ...overrides,
  };
}

function info(overrides: Partial<TreeRowInfo> = {}): TreeRowInfo {
  return { depth: 0, expanded: false, hasChildren: false, toggle: vi.fn(), activate: vi.fn(), ...overrides };
}

function actions(overrides: Partial<RailRowActions> = {}): RailRowActions {
  return {
    onOpenOverview: vi.fn(),
    onRenameSession: vi.fn().mockResolvedValue(undefined),
    onShutdownSession: vi.fn().mockResolvedValue(undefined),
    onForceStopSession: vi.fn().mockResolvedValue(undefined),
    onPinSession: vi.fn().mockResolvedValue(undefined),
    onUnpinRequest: vi.fn().mockResolvedValue(undefined),
    onToggleArchiveSession: vi.fn().mockResolvedValue(undefined),
    onDeleteSession: vi.fn().mockResolvedValue(undefined),
    onToggleFavoriteProject: vi.fn(),
    onToggleArchiveProject: vi.fn(),
    onDeleteProjectRequest: vi.fn(),
    ...overrides,
  };
}

// renderRow mounts a top-level local session row with the menu fully
// populated (renameable, live, deletable), the way the rail's own tiers
// would render it.
function renderRow(sessionOverrides: Partial<RailSession> = {}, acts: RailRowActions = actions()) {
  const session = apiNode({ rename: true, ...sessionOverrides });
  render(<RailRow node={sessionRailNode(session)} info={info()} actions={acts} />);
  return session;
}

async function openMenu(name: RegExp | string) {
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name }));
  return user;
}

test("rail menu omits Notes regardless of hydrated capability", async () => {
  renderRow({ state: "ended", live: false });
  await openMenu(/actions for/i);
  expect(screen.queryByRole("menuitem", { name: "Notes" })).toBeNull();
  expect(screen.queryByRole("menuitem", { name: /Tasks/ })).toBeNull();
});

function normalizedRailResource(
  key: Extract<ResourceKey, { kind: "project_page" | "pin_section" }>,
  parentOverrides: Partial<RailSession> = {},
  childOverrides: Partial<RailSession> = {},
): NormalizedResource {
  const parentKey = `${navigationViewScope(key)}/entity/${"1".repeat(64)}`;
  const childKey = `${navigationViewScope(key)}/entity/${"2".repeat(64)}`;
  return {
    key,
    graph: normalizedGraphFromSnapshot({
      metadata: {},
      entities: [
        {
          key: parentKey,
          kind: "session",
          value: apiNode({ ref: "parent", title: "Parent", children: [], ...parentOverrides }),
        },
        {
          key: childKey,
          kind: "session",
          value: apiNode({ ref: "child", title: "Child", children: [], ...childOverrides }),
        },
      ],
      containers: [
        {
          key: navigationRootContainerKey(key, "sessions"),
          owner: { kind: "resource_root", slot: "sessions" },
          children: [parentKey],
        },
        {
          key: navigationOwnedContainerKey(parentKey, "children"),
          owner: { kind: "entity", entityKey: parentKey, slot: "children" },
          children: [childKey],
        },
        {
          key: navigationOwnedContainerKey(childKey, "children"),
          owner: { kind: "entity", entityKey: childKey, slot: "children" },
          children: [],
        },
      ],
    }),
    version: { generationId: generation, revision: 1, etag: "v3" },
    presence: "present",
  };
}

test.each([
  [
    "archived project page",
    { kind: "project_page", projectKey: "project", tier: "archived", offset: 0, limit: 50 },
    { tier: "archived", project_key: "project" },
    "Unarchive",
  ],
  [
    "pinned section",
    { kind: "pin_section", sectionId: "research", offset: 0, limit: 50 },
    { pin_section_id: "research" },
    "Unpin",
  ],
] as const)(
  "normalized V2 %s preserves context through recursive rail projection and actions",
  async (_name, key, context, action) => {
    const resource = normalizedRailResource(key);
    const model = selectRailModel(resource);
    const parent = [...model.sessions.values()].find((session) => session.ref === "parent");
    const child = [...model.sessions.values()].find((session) => session.ref === "child");
    expect(parent).toMatchObject(context);
    expect(child).toMatchObject(context);
    if (!parent) throw new Error("expected projected parent session");

    const acts = actions();
    render(<RailRow node={sessionRailNode(parent)} info={info()} actions={acts} />);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: action }));
    if (action === "Unarchive") expect(acts.onToggleArchiveSession).toHaveBeenCalledWith(parent);
    else expect(acts.onUnpinRequest).toHaveBeenCalledWith(parent);
  },
);

describe("cadenceStateFor", () => {
  test.each([
    ["errored", "failed"],
    ["awaiting", "needs-you"],
    ["active", "working"],
    ["warning", "needs-you"],
    ["idle", "idle"],
    ["ended", "ended"],
    ["notLoaded", "idle"],
    ["", "idle"],
    ["some-unknown-future-state", "idle"],
  ] as const)("maps wire state %s to Cadence state %s", (wireState, expected) => {
    expect(cadenceStateFor(wireState)).toBe(expected);
  });
});

// A row only spends its signal gutter on running, needs-you, or broken.
describe("signal gutter", () => {
  test.each(["ended", "idle", "notLoaded", ""] as const)("state %s shows no dot and holds no slot", (state) => {
    render(<RailRow node={sessionRailNode(apiNode({ state }))} info={info()} actions={actions()} />);
    expect(screen.queryByTestId("rail-row-signal")).toBeNull();
  });

  test("a project row's rollup state follows the same rule", () => {
    const { rerender } = render(
      <RailRow node={projectRailNode(apiProject({ rollup_state: "active" }))} info={info()} actions={actions()} />,
    );
    expect(within(screen.getByTestId("rail-row-signal")).getByTestId("rail-status-spinner")).toBeTruthy();

    rerender(
      <RailRow node={projectRailNode(apiProject({ rollup_state: "ended" }))} info={info()} actions={actions()} />,
    );
    expect(screen.queryByTestId("rail-row-signal")).toBeNull();
  });

  test("a project row with no rollup state at all shows no dot and holds no slot", () => {
    render(<RailRow node={projectRailNode(apiProject())} info={info()} actions={actions()} />);
    expect(screen.queryByTestId("rail-row-signal")).toBeNull();
  });
});

describe("compact session status", () => {
  test.each([
    ["active", "Running", "rail-status-spinner"],
    ["awaiting", "Needs you", "rail-status-dot"],
    ["warning", "Needs you", "rail-status-dot"],
    ["errored", "Broken", "rail-status-dot"],
  ] as const)("state %s renders the %s indicator", (state, label, testID) => {
    render(<RailRow node={sessionRailNode(apiNode({ state }))} info={info()} actions={actions()} />);

    const signal = screen.getByTestId("rail-row-signal");
    expect(within(signal).getByRole("img", { name: label })).toBeTruthy();
    expect(within(signal).getByTestId(testID)).toBeTruthy();
  });

  test("broken descendants outrank needs-you and running work", () => {
    const session = apiNode({
      state: "awaiting",
      running_job_count: 2,
      subagents: { running: 1, failed: 1, done: 0 },
    });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={actions()} />);

    expect(screen.getByRole("img", { name: "Broken" })).toBeTruthy();
    expect(screen.queryByRole("img", { name: "Needs you" })).toBeNull();
    expect(screen.queryByRole("img", { name: "Running" })).toBeNull();
  });

  test("a running descendant gives an otherwise quiet session the spinner", () => {
    const session = apiNode({ state: "idle", subagents: { running: 1, failed: 0, done: 0 } });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={actions()} />);

    expect(screen.getByRole("img", { name: "Running" })).toBeTruthy();
  });

  test("a running job gives an otherwise quiet session the spinner", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "idle", running_job_count: 1 }))}
        info={info()}
        actions={actions()}
      />,
    );

    expect(screen.getByRole("img", { name: "Running" })).toBeTruthy();
  });

  test.each([
    ["awaiting", "job", { running_job_count: 1 }],
    ["warning", "job", { running_job_count: 1 }],
    ["awaiting", "subagent", { subagents: { running: 1, failed: 0, done: 0 } }],
    ["warning", "subagent", { subagents: { running: 1, failed: 0, done: 0 } }],
  ] as const)("a non-blocking %s state yields to running %s work", (state, _workKind, work) => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ state, ask_pending: false, ...work }))}
        info={info()}
        actions={actions()}
      />,
    );

    expect(screen.getByRole("img", { name: "Running" })).toBeTruthy();
    expect(screen.queryByRole("img", { name: "Needs you" })).toBeNull();
    const panel = hoverForTooltip(screen.getByText("Fix flaky test"));
    expect(within(panel).getByText("Running")).toBeTruthy();
  });

  test("a pending question outranks running work", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "awaiting", ask_pending: true, running_job_count: 1 }))}
        info={info()}
        actions={actions()}
      />,
    );

    expect(screen.getByRole("img", { name: "Needs you" })).toBeTruthy();
    expect(screen.queryByRole("img", { name: "Running" })).toBeNull();
    const panel = hoverForTooltip(screen.getByText("Fix flaky test"));
    expect(within(panel).getByText("Question waiting")).toBeTruthy();
  });

  test("a pending approval outranks running work", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "active", approval_pending: true, running_job_count: 1 }))}
        info={info()}
        actions={actions()}
      />,
    );

    expect(screen.getByRole("img", { name: "Needs you" })).toBeTruthy();
    expect(screen.queryByRole("img", { name: "Running" })).toBeNull();
  });

  test("the session's own failure outranks a pending approval", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "errored", approval_pending: true }))}
        info={info()}
        actions={actions()}
      />,
    );

    expect(screen.getByRole("img", { name: "Broken" })).toBeTruthy();
    expect(screen.queryByRole("img", { name: "Needs you" })).toBeNull();
  });

  test("a session row keeps status context out of its visible face", () => {
    const session = apiNode({
      state: "active",
      project: "prime-radiant",
      host_id: "buildbox",
      branch: "sidebar-status-dots",
      running_job_count: 2,
      watch_count: 3,
      armed_watch_count: 1,
      subagents: { running: 1, failed: 0, done: 4 },
      pin_section_id: "research",
      model: "opus",
    });
    render(<RailRow node={sessionRailNode(session, { crossProjectTier: true })} info={info()} actions={actions()} />);

    expect(screen.queryByTestId("rail-row-activity")).toBeNull();
    expect(screen.queryByTestId("rail-row-watches")).toBeNull();
    expect(screen.queryByTestId("rail-row-subagent-tally")).toBeNull();
    expect(screen.queryByTestId("rail-row-host")).toBeNull();
    expect(screen.queryByTestId("favorite-star")).toBeNull();
    expect(screen.queryByText("prime-radiant")).toBeNull();
    expect(screen.queryByText("sidebar-status-dots")).toBeNull();
    expect(screen.queryByText("opus")).toBeNull();
  });

  test("hovering the title opens the session context panel", () => {
    const session = apiNode({
      state: "active",
      project: "prime-radiant",
      host_id: "buildbox",
      branch: "sidebar-status-dots",
      updated_at: minutesAgo(2),
      running_job_count: 2,
      watch_count: 3,
      armed_watch_count: 1,
      subagents: { running: 1, failed: 0, done: 4 },
      tier: "archived",
    });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={actions()} />);

    const panel = hoverForTooltip(screen.getByText("Fix flaky test"));
    expect(within(panel).getByText("Session")).toBeTruthy();
    expect(within(panel).getByText("Running")).toBeTruthy();
    expect(within(panel).getByText("Fix flaky test")).toBeTruthy();
    expect(within(panel).getByText("prime-radiant")).toBeTruthy();
    expect(within(panel).getByText("buildbox")).toBeTruthy();
    expect(within(panel).getByText("sidebar-status-dots")).toBeTruthy();
    expect(within(panel).getByText("2 running")).toBeTruthy();
    expect(within(panel).getByText("1 running, 4 done")).toBeTruthy();
    expect(within(panel).getByText("3 watches · 1 armed")).toBeTruthy();
    expect(within(panel).getByText("archived")).toBeTruthy();
    expect(within(panel).getByText("2m")).toBeTruthy();
  });

  // The bug this pins: on a touch-only device the row's own tap must open the
  // session, and only a long press may expose the context card. A real tap
  // first fires mousedown, which RailRow uses to focus the owning tree row -
  // exactly the focus that used to pop the card on its own.
  test("a hoverless tap opens the session and only a long press opens the context panel", () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    installHoverlessMatchMedia();
    const rowInfo = info();
    render(
      <div role="treeitem" tabIndex={0}>
        <RailRow node={sessionRailNode(apiNode({ state: "active" }))} info={rowInfo} actions={actions()} />
      </div>,
    );
    const title = screen.getByTestId("rail-row-title");

    // Real touch order: the pointer lifts, then the compatibility mousedown
    // focuses the row. That focus must not open the card on its own.
    fireEvent.pointerDown(title, { button: 0 });
    fireEvent.pointerUp(title, { button: 0 });
    fireEvent.mouseDown(title, { button: 0 });
    expect(screen.queryByRole("tooltip")).toBeNull();

    fireEvent.click(title);
    expect(rowInfo.activate).toHaveBeenCalledOnce();
    expect(screen.queryByRole("tooltip")).toBeNull();

    fireEvent.pointerDown(title, { button: 0, clientX: 5, clientY: 5 });
    act(() => {
      vi.advanceTimersByTime(LONG_PRESS_MS);
    });
    expect(screen.getByRole("tooltip")).toBeTruthy();

    fireEvent.pointerUp(title, { clientX: 5, clientY: 5 });
    fireEvent.click(title);
    expect(rowInfo.activate).toHaveBeenCalledOnce();
    expect(screen.getByRole("tooltip")).toBeTruthy();
  });

  test.each([
    ["warning", { state: "warning" }, "Warning"],
    ["plain awaiting", { state: "awaiting", ask_pending: false }, "Your move"],
    ["pending question", { state: "awaiting", ask_pending: true }, "Question waiting"],
    ["pending approval", { state: "active", approval_pending: true }, "Approval waiting"],
  ] as const)("the context panel preserves the %s status vocabulary", (_name, overrides, expected) => {
    render(<RailRow node={sessionRailNode(apiNode(overrides))} info={info()} actions={actions()} />);

    const panel = hoverForTooltip(screen.getByText("Fix flaky test"));
    expect(within(panel).getByText(expected)).toBeTruthy();
  });

  test("the context panel agrees that a quiet dormant ended session has not started", () => {
    render(
      <RailRow node={sessionRailNode(apiNode({ state: "ended", dormant: true }))} info={info()} actions={actions()} />,
    );

    const panel = hoverForTooltip(screen.getByText("Fix flaky test"));
    expect(within(panel).getByText("Not started")).toBeTruthy();
  });

  test("the context panel age advances with the rail clock", () => {
    vi.useFakeTimers();
    try {
      const start = Date.parse("2026-01-01T00:00:00Z");
      vi.setSystemTime(start);
      render(
        <RailTickProvider>
          <RailRow
            node={sessionRailNode(apiNode({ updated_at: new Date(start - 1_000).toISOString() }))}
            info={info()}
            actions={actions()}
          />
        </RailTickProvider>,
      );

      fireEvent.mouseEnter(screen.getByText("Fix flaky test"));
      act(() => vi.advanceTimersByTime(300));
      const panel = screen.getByRole("tooltip");
      expect(within(panel).getByText("now")).toBeTruthy();
      act(() => vi.advanceTimersByTime(60_000));
      expect(within(panel).getByText("1m")).toBeTruthy();
    } finally {
      vi.useRealTimers();
    }
  });

  test("the session context panel identifies its pinned section", () => {
    render(<RailRow node={sessionRailNode(apiNode({ pin_section_id: "sec_1" }))} info={info()} actions={actions()} />);

    const panel = hoverForTooltip(screen.getByText("Fix flaky test"));
    expect(within(panel).getByText("Pinned")).toBeTruthy();
    expect(within(panel).getByText("Client")).toBeTruthy();
  });

  test("a top-level cross-project duplicate keeps its pinned context", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ pin_section_id: "sec_1" }), { crossProjectTier: true })}
        info={info()}
        actions={actions()}
      />,
    );

    const panel = hoverForTooltip(screen.getByText("Fix flaky test"));
    expect(within(panel).getByText("Pinned")).toBeTruthy();
    expect(within(panel).getByText("Client")).toBeTruthy();
  });

  test.each(["subagent", "fork"] as const)("the context panel hides a stale pin assignment on a nested %s", (kind) => {
    render(
      <RailRow node={sessionRailNode(apiNode({ kind, pin_section_id: "sec_1" }))} info={info()} actions={actions()} />,
    );

    const panel = hoverForTooltip(screen.getByText("Fix flaky test"));
    expect(within(panel).queryByText("Pinned")).toBeNull();
    expect(within(panel).queryByText("Client")).toBeNull();
  });

  test("the context panel marks an offline host", () => {
    seedSources([
      { id: "local", label: "Local", kind: "local", online: true },
      { id: "buildbox", label: "buildbox", kind: "ssh", online: false },
    ]);
    render(
      <RailRow
        node={sessionRailNode(apiNode({ ref: "buildbox:abc", host_id: "buildbox" }))}
        info={info()}
        actions={actions()}
      />,
    );

    const panel = hoverForTooltip(screen.getByText("Fix flaky test"));
    expect(within(panel).getByText("buildbox (offline)")).toBeTruthy();
  });

  test("the open context panel keeps an offline host during manifest revalidation", () => {
    const sources: NavigationManifest["sources"] = [
      { id: "local", label: "Local", kind: "local", online: true },
      { id: "buildbox", label: "buildbox", kind: "ssh", online: false },
    ];
    seedSources(sources);
    render(
      <RailRow
        node={sessionRailNode(apiNode({ ref: "buildbox:abc", host_id: "buildbox" }))}
        info={info()}
        actions={actions()}
      />,
    );

    const panel = hoverForTooltip(screen.getByText("Fix flaky test"));
    expect(within(panel).getByText("buildbox (offline)")).toBeTruthy();

    act(() => seedSources(sources, { loading: true, stale: true }));
    expect(within(panel).getByText("buildbox (offline)")).toBeTruthy();

    act(() =>
      seedSources([
        { id: "local", label: "Local", kind: "local", online: true },
        { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
      ]),
    );
    expect(within(panel).getByText("buildbox")).toBeTruthy();
    expect(within(panel).queryByText("buildbox (offline)")).toBeNull();
  });

  test("a truncated title stays recoverable from the context panel", () => {
    const title = "It looks like a lot of the sidebar rows are truncating their titles";
    render(<RailRow node={sessionRailNode(apiNode({ title }))} info={info()} actions={actions()} />);

    const panel = hoverForTooltip(screen.getByText(title));
    expect(within(panel).getByText(title)).toBeTruthy();
  });
});

describe("watchGloss", () => {
  test.each([
    ["every", 600, "every 10m · armed"],
    ["after", 90, "after 1m30s · armed"],
    ["progress", 30, "every 30s · armed"],
  ] as const)("renders a %s cadence as %s", (kind, seconds, expected) => {
    expect(watchGloss(watchSummary({ cadence: [{ kind, seconds }] }))).toBe(expected);
  });

  test("an output watch reads 'on output', not 'every'", () => {
    expect(watchGloss(watchSummary({ cadence: [{ kind: "output" }] }))).toBe("on output · armed");
  });

  test("an event watch reads 'on events'", () => {
    expect(watchGloss(watchSummary({ cadence: [{ kind: "events" }], events: ["turn_complete"] }))).toBe(
      "on events · armed",
    );
  });

  // The events cadence carries the fire-every-Nth count and the filter in the
  // model-facing prose summary's vocabulary, so a throttled or filtered event
  // watch no longer reads identically to one that fires on every match.
  test("an event watch names its count and filter", () => {
    expect(watchCadenceLabel({ kind: "events", every: 3, filter: "tool_name=Bash, status=error" })).toBe(
      "on events every 3 where tool_name=Bash, status=error",
    );
    expect(watchCadenceLabel({ kind: "events", every: 3 })).toBe("on events every 3");
    expect(watchCadenceLabel({ kind: "events", filter: "tool_name=Bash" })).toBe("on events where tool_name=Bash");
  });

  // An absent count or filter - the old-server case and most watches - must
  // leave the label byte-identical to before the fields existed.
  test("an event watch with no count or filter stays byte-identical", () => {
    expect(watchCadenceLabel({ kind: "events" })).toBe("on events");
    expect(watchCadenceLabel({ kind: "events", every: 0, filter: "" })).toBe("on events");
    expect(watchCadenceLabel({ kind: "events", every: 0, filter: "   " })).toBe("on events");
  });

  test("combines every trigger source the wire sent, in order", () => {
    expect(
      watchGloss(
        watchSummary({
          cadence: [{ kind: "output" }, { kind: "every", seconds: 600 }, { kind: "events" }],
          events: ["turn_complete"],
        }),
      ),
    ).toBe("on output · every 10m · on events · armed");
  });

  // A one-shot that already fired but is still registered until its durable
  // teardown lands (firedPendingEnd) is in the list but no longer armed.
  test.each([
    ["after", 90],
    ["output", undefined],
  ] as const)("a watch that is no longer active reads as not armed (%s)", (kind, seconds) => {
    const watch = watchSummary({ active: false, cadence: [{ kind, seconds }] });
    expect(watchGloss(watch)).toMatch(/not armed$/);
    expect(watchGloss(watch)).not.toMatch(/· armed$/);
  });

  test("a watch with no cadence at all still says whether it is armed", () => {
    expect(watchGloss(watchSummary({ cadence: [] }))).toBe("armed");
  });

  test("a periodless cadence still names its kind instead of rendering nothing", () => {
    expect(watchCadenceLabel({ kind: "every" })).toBe("every");
    expect(watchDurationLabel(undefined)).toBe("");
    expect(watchGloss(watchSummary({ cadence: [{ kind: "every" }] }))).toBe("every · armed");
  });
});

describe("loading row", () => {
  test("renders a non-interactive loading indicator", () => {
    render(<RailRow node={loadingRailNode()} info={info()} actions={actions()} />);
    expect(screen.getByText(/loading/i)).toBeTruthy();
    expect(screen.queryByRole("button")).toBeNull();
  });

  test("announces itself via role=status, like the top-level Skeleton", () => {
    render(<RailRow node={loadingRailNode()} info={info()} actions={actions()} />);
    expect(screen.getByRole("status").textContent).toMatch(/loading/i);
  });
});

describe("overflow row", () => {
  test("says how many rows the server capped away", () => {
    render(<RailRow node={overflowRailNode(12)} info={info()} actions={actions()} />);
    expect(screen.getByText("+12 older")).toBeTruthy();
  });

  // Nothing to open and nothing to act on: the rows it counts were never sent
  // to the client, so a chevron or a menu would both be lies.
  test("offers nothing to click", () => {
    render(<RailRow node={overflowRailNode(3)} info={info({ hasChildren: false })} actions={actions()} />);
    expect(screen.queryByRole("button")).toBeNull();
  });
});

describe("row alignment", () => {
  // The outdented-status contract the whole list's alignment rests on:
  // .railRow reserves the leading padding and .signal's negative margin
  // cancels its 8px width plus the title line's gap.
  test("the row reserves the dot's outdent padding", () => {
    expect(topRuleBlock(RAIL_CSS, ".railRow")).toMatch(/padding-left:\s*14px;/);
  });

  test("the signal dot outdents by exactly its own advance", () => {
    expect(topRuleBlock(RAIL_CSS, ".signal")).toMatch(/width:\s*8px;[^}]*margin-left:\s*-12px;/);
  });

  test("status colors are semantic, broken has distinct geometry, and the running ring respects reduced motion", () => {
    expect(topRuleBlock(RAIL_CSS, '.statusDot[data-status="needs-you"]')).toContain("var(--attention)");
    expect(topRuleBlock(RAIL_CSS, '.statusDot[data-status="failed"]')).toContain("var(--danger)");
    expect(topRuleBlock(RAIL_CSS, '.statusDot[data-status="failed"]')).toMatch(
      /border-radius:\s*1px;[^}]*rotate\(45deg\)/,
    );
    const motionRules = mediaBlock(RAIL_CSS, "prefers-reduced-motion: no-preference");
    expect(nestedRuleBlock(motionRules, ".statusSpinner")).toMatch(/animation:/);
    const baseSpinner = topRuleBlock(RAIL_CSS, ".statusSpinner");
    expect(baseSpinner).not.toContain("animation:");
  });

  test("the title button reset preserves the rail label's explicit UI font size", () => {
    const buttonReset = topRuleBlock(RAIL_CSS, ".sessionTitle .label");
    expect(buttonReset).toContain("font-family: inherit");
    expect(buttonReset).not.toMatch(/(?:^|\s)font:\s*inherit/);
  });
});

describe("touch tap floor (RailRow.module.css, pointer: coarse)", () => {
  // shellguard's tap-target pass measures these in a real phone context; these
  // source assertions pin the rules themselves (jsdom evaluates no cascade).
  const coarseBlock = mediaBlock(RAIL_CSS, "pointer: coarse");

  test("row action buttons meet the 44px floor in BOTH dimensions", () => {
    const rule = nestedRuleBlock(coarseBlock, ".actions button");
    expect(rule).toContain("min-width: var(--tap-min)");
    expect(rule).toContain("min-height: var(--tap-min)");
  });

  test("long-press-enabled session titles meet the 44px floor in both dimensions", () => {
    const rule = nestedRuleBlock(coarseBlock, ".sessionTitle button.label");
    expect(rule).toContain("min-width: var(--tap-min)");
    expect(rule).toContain("min-height: var(--tap-min)");
  });

  test("the widened menu trigger centres its glyph instead of hugging an edge", () => {
    const rule = nestedRuleBlock(coarseBlock, '.actions button[aria-haspopup="menu"]');
    expect(rule).toContain("padding: 0");
    expect(rule).toContain("justify-content: center");
  });
});

describe("session row", () => {
  test("renders the session's title and running indicator", () => {
    const session = apiNode({ title: "Fix flaky test", state: "active" });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={actions()} />);
    expect(screen.getByText("Fix flaky test")).toBeTruthy();
    expect(screen.getByRole("img", { name: "Running" })).toBeTruthy();
  });

  test("clicking the label activates the row via info.activate", async () => {
    const rowInfo = info();
    render(<RailRow node={sessionRailNode(apiNode())} info={rowInfo} actions={actions()} />);
    await userEvent.setup().click(screen.getByText("Fix flaky test"));
    expect(rowInfo.activate).toHaveBeenCalledTimes(1);
  });

  test("shows no chevron for a leaf session (info.hasChildren false)", () => {
    render(<RailRow node={sessionRailNode(apiNode())} info={info({ hasChildren: false })} actions={actions()} />);
    expect(screen.queryByTestId("rail-chevron")).toBeNull();
  });

  // The chevron trails the title text, so a leaf row renders nothing for it
  // and reserves nothing either: unlike the old leading gutter (whose
  // conditional fill moved every title after it), a missing TRAILING chevron
  // leaves no hole - the label is the title line's first text either way.
  test("a leaf session renders no chevron and holds no slot for one", () => {
    render(<RailRow node={sessionRailNode(apiNode())} info={info({ hasChildren: false })} actions={actions()} />);
    expect(screen.queryByTestId("rail-chevron")).toBeNull();
    const label = screen.getByText("Fix flaky test");
    expect(label.nextElementSibling).toBeNull();
  });

  // Session rows are leaves in the flat rail - nothing nests under them
  // anymore - so the chevron contract lives on the rows that still branch:
  // projects and host groups.
  test("shows a chevron for a branch row that calls info.toggle", async () => {
    const rowInfo = info({ hasChildren: true, expanded: false });
    render(<RailRow node={projectRailNode(apiProject())} info={rowInfo} actions={actions()} />);
    // The chevron is deliberately aria-hidden (decorative mouse shortcut -
    // see widgets/tree's own doc comment and RailRow.tsx's Chevron), so
    // it's found by test id rather than an accessible role query.
    await userEvent.setup().click(screen.getByTestId("rail-chevron"));
    expect(rowInfo.toggle).toHaveBeenCalledTimes(1);
  });

  // The title line's anatomy is signal-dot (outdented) then label then
  // trailing chevron, in that order, on every row that branches at all -
  // a session row never does, so its title line is dot then label. This is
  // what keeps one title
  // x-position across a mixed tree: the dot hangs in the row's leading
  // padding (see .signal in RailRow.module.css), so it participates in the DOM
  // order without shifting the label.
  test("the title line is signal-dot then label on a (leaf) session row", () => {
    render(<RailRow node={sessionRailNode(apiNode({ state: "active" }))} info={info()} actions={actions()} />);
    const signal = screen.getByTestId("rail-row-signal");
    const label = screen.getByText("Fix flaky test");
    const titleLine = signal.parentElement;
    expect(titleLine).toBeTruthy();
    expect(titleLine?.firstElementChild).toBe(signal);
    expect(signal.nextElementSibling?.contains(label)).toBe(true);
  });

  test("a quiet row holds no signal slot; its title line leads with the label", () => {
    render(<RailRow node={sessionRailNode(apiNode({ state: "idle" }))} info={info()} actions={actions()} />);
    expect(screen.queryByTestId("rail-row-signal")).toBeNull();
    const label = screen.getByText("Fix flaky test");
    expect(label.closest(`.${railStyles.titleLine}`)?.firstElementChild?.contains(label)).toBe(true);
  });

  test("shows no Badge for a leaf session that itself needs you (the Cadence dot already covers it)", () => {
    render(<RailRow node={sessionRailNode(apiNode({ state: "awaiting" }))} info={info()} actions={actions()} />);
    expect(screen.queryByText("1")).toBeNull();
    expect(screen.queryByText("0")).toBeNull();
  });

  // --- a session that has never run says so ------------------------------
  //
  // An empty-prompt spawn starts a session dormant (kata ytpa), and the server
  // reports it "idle" - the same word a session that ran and went quiet gets.
  // Every quiet row is title + age, so the two were the same row. Worse, the
  // age READ as activity: it falls back to the creation time, so a session that
  // has never done anything showed a confident "now" or "4m".
  //
  // The row spends no new space on this. The one slot that was actively lying -
  // the age - is the slot that carries the correction, so a dormant row stays
  // one line and the mobile drawer still shows the same six rows.

  test("a dormant row says it has not started, in place of an age that would read as activity", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "idle", updated_at: minutesAgo(4), dormant: true }))}
        info={info()}
        actions={actions()}
      />,
    );
    expect(screen.getByTestId("rail-row-not-started").textContent).toBe("Not started");
    expect(screen.queryByTestId("rail-row-time")).toBeNull();
  });

  test("a session that has run keeps its age", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "idle", updated_at: minutesAgo(4), dormant: false }))}
        info={info()}
        actions={actions()}
      />,
    );
    expect(screen.getByTestId("rail-row-time").textContent).toBe("4m");
    expect(screen.queryByTestId("rail-row-not-started")).toBeNull();
  });

  // The moment a dormant session is given something to do it is working, and
  // the row must say THAT. Dormancy is only ever the most useful thing to
  // report on a row that is otherwise quiet.
  test.each(["active", "awaiting", "warning", "errored"] as const)(
    "a signal state (%s) outranks dormancy in the right slot",
    (state) => {
      render(
        <RailRow
          node={sessionRailNode(apiNode({ state, updated_at: minutesAgo(0), dormant: true }))}
          info={info()}
          actions={actions()}
        />,
      );
      expect(screen.queryByTestId("rail-row-not-started")).toBeNull();
      expect(screen.getByTestId("rail-row-time").textContent).toBe("now");
    },
  );

  test("shows no timestamp when the session carries no age", () => {
    render(<RailRow node={sessionRailNode(apiNode({ state: "active" }))} info={info()} actions={actions()} />);
    expect(screen.queryByTestId("rail-row-time")).toBeNull();
  });

  // The stamp is a CLOCK, not a snapshot. An idle session produces no further
  // navigation data, so a label derived only when its summary arrives freezes
  // at the value it read then ("now") until a full page refresh. The row has to
  // derive it from the summary's updated_at anchor against the rail clock.
  test("an idle row's age advances with the rail clock, with no new data", () => {
    vi.useFakeTimers();
    try {
      const start = Date.parse("2026-01-01T00:00:00Z");
      vi.setSystemTime(start);
      // One second old at mount: the label reads "now"...
      const session = apiNode({
        state: "idle",
        updated_at: new Date(start - 1_000).toISOString(),
      });
      render(
        <RailTickProvider>
          <RailRow node={sessionRailNode(session)} info={info()} actions={actions()} />
        </RailTickProvider>,
      );
      expect(screen.getByTestId("rail-row-time").textContent).toBe("now");
      // ...and a minute later it must have clicked forward on its own.
      act(() => {
        vi.advanceTimersByTime(60_000);
      });
      expect(screen.getByTestId("rail-row-time").textContent).toBe("1m");
    } finally {
      vi.useRealTimers();
    }
  });

  test("menu offers 'Pin this session…' for an unassigned top-level session, assigning through onPinSession", async () => {
    const acts = actions();
    const session = renderRow({ pin_section_id: undefined, ref: "local:a" }, acts);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Pin this session…" }));
    // The shared SessionMenu owns the picker now (one owner per dialog) -
    // the row's duty stops at feeding the picked target out through
    // onPinSession.
    await user.click(await screen.findByRole("button", { name: "Client" }));
    await waitFor(() =>
      expect(acts.onPinSession).toHaveBeenCalledWith(
        session,
        { section_id: "sec_1" },
        { id: "sec_1", name: "Client", member_count: 0 },
      ),
    );
  });

  test("menu offers 'Unpin' for an assigned top-level session", async () => {
    const acts = actions();
    const session = apiNode({ pin_section_id: "research" });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={acts} />);
    const user = await openMenu(/actions for/i);
    expect(screen.queryByRole("menuitem", { name: "Pin this session…" })).toBeNull();
    await user.click(screen.getByRole("menuitem", { name: "Unpin" }));
    expect(acts.onUnpinRequest).toHaveBeenCalledWith(session);
  });

  // The unified menu keeps one stable item list, so Rename is always LISTED -
  // a session whose wire `rename` flag is absent gets it disabled instead of
  // dropped.
  test("menu disables Rename when the session does not support it", async () => {
    renderRow({ rename: false });
    await openMenu(/actions for/i);
    expect(screen.getByRole("menuitem", { name: "Rename" }).getAttribute("aria-disabled")).toBe("true");
  });

  test("menu offers Rename when the session supports it, saving through onRenameSession", async () => {
    const acts = actions();
    const session = renderRow({ rename: true }, acts);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Rename" }));
    const dialog = screen.getByRole("dialog", { name: "Rename session" });
    const input = within(dialog).getByLabelText("Name");
    await user.clear(input);
    await user.type(input, "New name");
    await user.click(within(dialog).getByRole("button", { name: "Rename" }));
    await waitFor(() => expect(acts.onRenameSession).toHaveBeenCalledWith(session, "New name"));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  test("menu offers 'Archive' for a session outside the archived tier, and calls onToggleArchiveSession", async () => {
    const acts = actions();
    const session = apiNode({ tier: "current" });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={acts} />);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Archive" }));
    expect(acts.onToggleArchiveSession).toHaveBeenCalledWith(session);
  });

  test("menu offers 'Unarchive' for a session already in the archived tier", async () => {
    render(<RailRow node={sessionRailNode(apiNode({ tier: "archived" }))} info={info()} actions={actions()} />);
    await openMenu(/actions for/i);
    expect(screen.getByRole("menuitem", { name: "Unarchive" })).toBeTruthy();
  });

  // Archive is a decision about a TOP-LEVEL row, and only a top-level row can
  // act on it: the server stores one archive decision per session id, and a
  // nested row has no independent existence in the tree its parent isn't
  // already deciding for. hubcore's nodeKind (internal/hubcore/tree.go) names
  // the two kinds that are never top-level - "subagent" (nested under its
  // parent) and "fork" (a snapshotted original nested under the branch that
  // superseded it) - so `kind` is the whole test, at any depth.
  for (const kind of ["subagent", "fork"]) {
    test(`menu omits Archive on a ${kind} row - only top-level sessions are archivable`, async () => {
      render(<RailRow node={sessionRailNode(apiNode({ kind, tier: "current" }))} info={info()} actions={actions()} />);
      // The unified menu is on every session row (the pane items are always
      // meaningful), so the trigger is always there - what the row loses is
      // the organization group.
      await openMenu(/actions for/i);
      expect(screen.queryByRole("menuitem", { name: "Archive" })).toBeNull();
      expect(screen.queryByRole("menuitem", { name: "Unarchive" })).toBeNull();
    });
  }

  // Delete (kata n15j) is a decision about a TOP-LEVEL LOCAL session: it
  // targets a stable local session ref (identifier.ValidateSessionID via
  // cmd/evener-hub/app_session_delete.go), so it is offered unconditionally
  // for a top-level local row - including a live one, which the server
  // refuses via the same skipped/toast path deleteProject already uses for a
  // session that raced back to live (no client-side liveness gate to
  // duplicate the server's own crash-vs-live predicate).
  test("menu offers 'Delete…' for a top-level local session, confirming through onDeleteSession", async () => {
    const acts = actions();
    const session = renderRow({ host_id: "local", state: "ended", live: false }, acts);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Delete…" }));
    const dialog = screen.getByRole("dialog", { name: "Delete session?" });
    await user.click(within(dialog).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(acts.onDeleteSession).toHaveBeenCalledWith(session));
  });

  // "do not offer this capability for remote-source threads" (kata n15j) -
  // the menu itself withholds Delete for a non-local session rather than
  // relying solely on the server's own isLocalRouteID refusal.
  test("menu omits Delete for a remote-source session", async () => {
    render(<RailRow node={sessionRailNode(apiNode({ host_id: "remote" }))} info={info()} actions={actions()} />);
    await openMenu(/actions for/i);
    expect(screen.queryByRole("menuitem", { name: "Delete…" })).toBeNull();
  });

  // Delete is scoped like Archive: only a top-level row names a real,
  // independently deletable session (see the Archive loop's own comment
  // above for why these two kinds are never top-level).
  for (const kind of ["subagent", "fork"]) {
    test(`menu omits Delete on a ${kind} row - only top-level sessions are deletable`, async () => {
      render(<RailRow node={sessionRailNode(apiNode({ kind, host_id: "local" }))} info={info()} actions={actions()} />);
      await openMenu(/actions for/i);
      expect(screen.queryByRole("menuitem", { name: "Delete…" })).toBeNull();
    });
  }

  // Favorite is scoped for the same reason as Archive: session rows use the
  // separate session-pin action.
  for (const kind of ["subagent", "fork"]) {
    test(`menu omits pin and unpin on a ${kind} row`, async () => {
      render(<RailRow node={sessionRailNode(apiNode({ kind }))} info={info()} actions={actions()} />);
      await openMenu(/actions for/i);
      expect(screen.queryByRole("menuitem", { name: "Pin this session…" })).toBeNull();
      expect(screen.queryByRole("menuitem", { name: "Unpin" })).toBeNull();
    });
  }

  // With pin, archive and delete gone, a subagent's unified menu is down to
  // the items that are always meaningful: the pane group, Rename (disabled -
  // the wire withholds `rename` from every nested/synthetic node), and
  // Shut down.
  test("a subagent row's menu is panes + rename + shut down only", async () => {
    renderRow({ kind: "subagent", rename: false });
    await openMenu(/actions for/i);
    const items = screen.getAllByRole("menuitem").map((el) => el.textContent);
    expect(items).toEqual(["Overview", "Rename", "Shut down"]);
  });

  // The row's menu is THE shared SessionMenu now - the same item list, in the
  // same order, the session pane's chrome shows (SessionMenu.test.tsx pins
  // the component's own copy of this contract).
  test("session row menu is the unified menu: panes group first, shut down present", async () => {
    renderRow({ kind: "session", host_id: "local", state: "ended", live: false });
    await openMenu(/actions for/i);
    const items = screen.getAllByRole("menuitem").map((el) => el.textContent);
    expect(items).toEqual([
      "Overview",
      "Rename",
      "Pin this session…",
      "Archive",
      "Shut down",
      "Force shutdown…",
      "Delete…",
    ]);
  });

  test("Overview dispatches the row's session", async () => {
    const acts = actions();
    const session = renderRow({}, acts);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Overview" }));
    expect(acts.onOpenOverview).toHaveBeenCalledWith(session);
  });

  test("a retained Details pane does not mark closed Overview on mobile", async () => {
    const restoreViewport = installMobileViewport();
    try {
      workspaceStore.getState().openPane("sessionDetails", { ref: "local:a" });
      renderRow();
      await openMenu(/actions for/i);
      expect(screen.getByRole("menuitem", { name: "Overview" })).toBeTruthy();
    } finally {
      restoreViewport();
    }
  });

  test("a retained Details pane does not mark closed Overview on desktop", async () => {
    workspaceStore.getState().openPane("sessionDetails", { ref: "local:a" });
    renderRow();
    await openMenu(/actions for/i);
    expect(screen.getByRole("menuitem", { name: "Overview" })).toBeTruthy();
  });

  test("a pure focus move refreshes the Activity check (the ✓ names the session the sidebar shows)", async () => {
    // The sidebar's scope follows workspace focus. Subscribing to the sidebar
    // store alone leaves the ✓ on the session the sidebar showed BEFORE the
    // focus move - and clicking it re-scopes where a close was implied.
    workspaceStore.getState().openPane("session", { ref: "local:a" });
    activitySidebarStore.getState().openWith();
    renderRow();
    await openMenu(/actions for/i);
    expect(screen.getByRole("menuitem", { name: "Overview ✓" })).toBeTruthy();
    act(() => {
      workspaceStore.getState().openPane("session", { ref: "local:other" });
    });
    // The row re-rendered on the focus change: the item is plain again.
    expect(await screen.findByRole("menuitem", { name: "Overview" })).toBeTruthy();
  });

  test("the Tasks menu item is absent while the sidebar shows its tab", async () => {
    workspaceStore.getState().openPane("session", { ref: "local:a" });
    activitySidebarStore.getState().openWith("tasks");
    renderRow();
    await openMenu(/actions for/i);
    expect(screen.queryByRole("menuitem", { name: /Tasks/ })).toBeNull();
  });

  test("the Tasks menu item is absent while the sidebar shows another tab", async () => {
    workspaceStore.getState().openPane("session", { ref: "local:a" });
    activitySidebarStore.getState().openWith("agents");
    renderRow();
    await openMenu(/actions for/i);
    expect(screen.queryByRole("menuitem", { name: /Tasks/ })).toBeNull();
  });

  test("the Tasks menu item is absent when a sessionTasks pane is open", async () => {
    workspaceStore.getState().openPane("sessionTasks", { ref: "local:a" });
    renderRow();
    await openMenu(/actions for/i);
    expect(screen.queryByRole("menuitem", { name: /Tasks/ })).toBeNull();
  });

  test("the Tasks menu item is absent on mobile", async () => {
    const restoreViewport = installMobileViewport();
    try {
      workspaceStore.getState().openPane("sessionTasks", { ref: "local:a" });
      renderRow();
      await openMenu(/actions for/i);
      expect(screen.queryByRole("menuitem", { name: /Tasks/ })).toBeNull();
    } finally {
      restoreViewport();
    }
  });

  test("shut down confirms through onShutdownSession", async () => {
    const acts = actions();
    const session = renderRow({}, acts);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Shut down" }));
    const dialog = screen.getByRole("dialog", { name: "Shut down this session?" });
    await user.click(within(dialog).getByRole("button", { name: "Shut down" }));
    await waitFor(() => expect(acts.onShutdownSession).toHaveBeenCalledWith(session));
  });

  // A live-tier row's own Tier/PinSectionID/Rename fields must all survive the
  // duplicate projection. RailRow reads pin_section_id/rename directly,
  // regardless of the session's real decisions, since the navigation
  // projection stamps the live tier separately. RailRow never gated
  // these on tier itself - it just reads session.favorite/session.rename
  // directly, same as every other row - so once the hub fix landed, this
  // was already correct with no rail-side code change; pinned explicitly
  // here (rather than left to incidental coverage from fixtures that never
  // set tier at all) since a live row is the realistic shape a reviewer
  // would specifically want proof for. The star stays hidden: a
  // cross-project tier root (Live, like a named pin section) never carries
  // the pin star at all.
  test("Unpin and Rename work on a live-tier duplicate, and its pin star stays hidden", async () => {
    const acts = actions();
    const session = apiNode({ tier: "live", pin_section_id: "research", rename: true });
    render(<RailRow node={sessionRailNode(session, { crossProjectTier: true })} info={info()} actions={acts} />);

    expect(screen.queryByTestId("favorite-star")).toBeNull();
    const user = await openMenu(/actions for/i);
    expect(screen.getByRole("menuitem", { name: "Unpin" })).toBeTruthy();
    await user.click(screen.getByRole("menuitem", { name: "Rename" }));
    // The dialog opens prefilled with the current title; saving unchanged
    // still round-trips through onRenameSession.
    const dialog = screen.getByRole("dialog", { name: "Rename session" });
    await user.click(within(dialog).getByRole("button", { name: "Rename" }));
    await waitFor(() => expect(acts.onRenameSession).toHaveBeenCalledWith(session, session.title));
  });
});

describe("project row", () => {
  test("descendant-only project changes do not invoke the project RailRow again", () => {
    // This fails until RailRow's memo boundary compares project rows by the
    // fields ProjectRow renders/captures instead of by ancestor node identity.
    const observer = vi.fn();
    const rowInfo = info({ hasChildren: true });
    const rowActions = actions();
    const firstSession = apiNode({ row_id: "project:p1:local:first", ref: "local:first", session_id: "first" });
    const secondSession = apiNode({ row_id: "project:p1:local:second", ref: "local:second", session_id: "second" });
    const firstProject = apiProject({ sessions: [firstSession] });
    const { rerender } = render(
      <RailRenderObserver value={observer}>
        <RailRow
          node={projectRailNode(firstProject, [sessionRailNode(firstSession)])}
          info={rowInfo}
          actions={rowActions}
        />
      </RailRenderObserver>,
    );
    expect(observer).toHaveBeenCalledTimes(1);
    observer.mockClear();

    rerender(
      <RailRenderObserver value={observer}>
        <RailRow
          node={projectRailNode({ ...firstProject, sessions: [secondSession] }, [sessionRailNode(secondSession)])}
          info={rowInfo}
          actions={rowActions}
        />
      </RailRenderObserver>,
    );

    expect(observer).toHaveBeenCalledTimes(0);
  });

  test.each([
    ["node id", (node: ProjectRailNode) => ({ ...node, id: "projectnode:p1-replaced" })],
    ["display name", (node: ProjectRailNode) => ({ ...node, displayName: "Decorated project" })],
    ["resource error", (node: ProjectRailNode) => ({ ...node, resourceError: "load failed" })],
    ["retry callback", (node: ProjectRailNode) => ({ ...node, retry: vi.fn() })],
    ["project key", (node: ProjectRailNode) => ({ ...node, project: { ...node.project, key: "p2" } })],
    ["project name", (node: ProjectRailNode) => ({ ...node, project: { ...node.project, name: "Renamed" } })],
    [
      "project working directory",
      (node: ProjectRailNode) => ({ ...node, project: { ...node.project, working_dir: "/repo/next" } }),
    ],
    [
      "project rollup state",
      (node: ProjectRailNode) => ({ ...node, project: { ...node.project, rollup_state: "active" } }),
    ],
    ["project attention count", (node: ProjectRailNode) => ({ ...node, project: { ...node.project, rollup_attn: 2 } })],
    ["project favorite", (node: ProjectRailNode) => ({ ...node, project: { ...node.project, favorite: true } })],
    [
      "project archive state",
      (node: ProjectRailNode) => ({ ...node, project: { ...node.project, is_archived: true } }),
    ],
  ] as const)("%s changes still invoke the project RailRow", (_name, change) => {
    const observer = vi.fn();
    const rowInfo = info();
    const rowActions = actions();
    const firstNode = { ...projectRailNode(apiProject()), retry: vi.fn() };
    const { rerender } = render(
      <RailRenderObserver value={observer}>
        <RailRow node={firstNode} info={rowInfo} actions={rowActions} />
      </RailRenderObserver>,
    );
    observer.mockClear();

    rerender(
      <RailRenderObserver value={observer}>
        <RailRow node={change(firstNode)} info={rowInfo} actions={rowActions} />
      </RailRenderObserver>,
    );

    expect(observer).toHaveBeenCalledTimes(1);
  });

  test("changed retry, spawn directory, and project action input replace captured project-row behavior", async () => {
    window.history.replaceState({}, "", "/");
    const observer = vi.fn();
    const rowInfo = info();
    const firstRetry = vi.fn();
    const secondRetry = vi.fn();
    const rowActions = actions();
    const firstProject = apiProject({ working_dir: "/repo/first" });
    const secondProject = { ...firstProject, key: "p2", working_dir: "/repo/next" };
    const { rerender } = render(
      <RailRenderObserver value={observer}>
        <RailRow
          node={{ ...projectRailNode(firstProject), resourceError: "load failed", retry: firstRetry }}
          info={rowInfo}
          actions={rowActions}
        />
      </RailRenderObserver>,
    );
    observer.mockClear();

    rerender(
      <RailRenderObserver value={observer}>
        <RailRow
          node={{
            ...projectRailNode(secondProject),
            id: "projectnode:p1",
            resourceError: "load failed",
            retry: secondRetry,
          }}
          info={rowInfo}
          actions={rowActions}
        />
      </RailRenderObserver>,
    );

    expect(observer).toHaveBeenCalledTimes(1);
    await userEvent.setup().click(screen.getByRole("button", { name: "Retry" }));
    expect(firstRetry).not.toHaveBeenCalled();
    expect(secondRetry).toHaveBeenCalledTimes(1);
    await userEvent.setup().click(screen.getByRole("button", { name: "New session in Proj" }));
    expect(`${window.location.pathname}${window.location.search}`).toBe("/new?dir=%2Frepo%2Fnext");
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Add to pinned" }));
    expect(rowActions.onToggleFavoriteProject).toHaveBeenCalledWith(secondProject);
    window.history.replaceState({}, "", "/");
  });

  // A host-first copy's + button must launch on the host the copy nests
  // under, or every host's copy silently spawns on this hub.
  test("a project copy's New-session button prefills the copy's host", async () => {
    seedSources([
      { id: "local", label: "this host", kind: "local", online: true },
      { id: "devbox", label: "devbox", kind: "appwire", online: true },
    ]);
    render(
      <RailRow
        node={
          {
            ...projectRailNode(apiProject({ working_dir: "/repo/next" })),
            id: "projectnode:p1@devbox",
            spawnHost: "devbox",
          } as ProjectRailNode
        }
        info={info({ hasChildren: true })}
        actions={actions()}
      />,
    );
    await userEvent.setup().click(screen.getByRole("button", { name: "New session in Proj" }));
    expect(`${window.location.pathname}${window.location.search}`).toBe("/new?dir=%2Frepo%2Fnext&host=devbox");
    window.history.replaceState({}, "", "/");
    // The copy's own menu makes the same host claim.
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "New session" }));
    expect(`${window.location.pathname}${window.location.search}`).toBe("/new?dir=%2Frepo%2Fnext&host=devbox");
    window.history.replaceState({}, "", "/");
  });

  // The Spawn picker refuses an offline host; the copy's own affordances
  // must not offer a launch that would silently fall back to this hub with
  // the remote working_dir.
  test("an offline host copy offers no New-session launch", async () => {
    seedSources([
      { id: "local", label: "this host", kind: "local", online: true },
      { id: "ci-runner", label: "ci-runner", kind: "appwire", online: false },
    ]);
    const offlineCopy = {
      ...projectRailNode(apiProject()),
      id: "projectnode:p1@ci-runner",
      spawnHost: "ci-runner",
    } as ProjectRailNode;
    render(<RailRow node={offlineCopy} info={info({ hasChildren: true })} actions={actions()} />);
    expect(screen.queryByRole("button", { name: "New session in Proj" })).toBeNull();
    await openMenu(/actions for/i);
    expect(screen.queryByRole("menuitem", { name: "New session" })).toBeNull();
  });

  // A host the manifest no longer names must not offer a launch either:
  // Spawn's settled list would refuse the prefilled host and silently start
  // the session on this hub, with the remote working_dir. The display
  // default (unknown reads online) is for chips, not launch decisions.
  test("a copy whose host the manifest no longer names offers no New-session launch", async () => {
    seedSources([{ id: "local", label: "this host", kind: "local", online: true }]);
    const removedCopy = {
      ...projectRailNode(apiProject()),
      id: "projectnode:p1@devbox",
      spawnHost: "devbox",
    } as ProjectRailNode;
    render(<RailRow node={removedCopy} info={info({ hasChildren: true })} actions={actions()} />);
    expect(screen.queryByRole("button", { name: "New session in Proj" })).toBeNull();
    await openMenu(/actions for/i);
    expect(screen.queryByRole("menuitem", { name: "New session" })).toBeNull();
  });

  // A local copy's + must claim its host too: the same project's copies
  // share one working_dir, so the draft's last-chosen host (say devbox)
  // must not survive a launch from this hub's copy.
  test("a local copy's New-session button names this hub, not the draft's last choice", async () => {
    seedSources([{ id: "local", label: "this host", kind: "local", online: true }]);
    const localCopy = {
      ...projectRailNode(apiProject({ working_dir: "/repo/next" })),
      id: "projectnode:p1@local",
      spawnHost: "local",
    } as ProjectRailNode;
    render(<RailRow node={localCopy} info={info({ hasChildren: true })} actions={actions()} />);
    await userEvent.setup().click(screen.getByRole("button", { name: "New session in Proj" }));
    expect(`${window.location.pathname}${window.location.search}`).toBe("/new?dir=%2Frepo%2Fnext&host=local");
    window.history.replaceState({}, "", "/");
  });

  test("a local copy's launch survives a manifest in flight (this hub is always launchable)", () => {
    seedSources([], { loading: true });
    const localCopy = {
      ...projectRailNode(apiProject()),
      id: "projectnode:p1@local",
      spawnHost: "local",
    } as ProjectRailNode;
    render(<RailRow node={localCopy} info={info({ hasChildren: true })} actions={actions()} />);
    expect(screen.getByRole("button", { name: "New session in Proj" })).toBeTruthy();
  });

  test("changed TreeRowInfo and actions identities still invoke the project RailRow and replace handlers", async () => {
    const observer = vi.fn();
    const firstInfo = info({ hasChildren: true });
    const secondInfo = info({ hasChildren: true });
    const firstActions = actions();
    const secondActions = actions();
    const node = projectRailNode(apiProject(), [sessionRailNode(apiNode())]);
    const { rerender } = render(
      <RailRenderObserver value={observer}>
        <RailRow node={node} info={firstInfo} actions={firstActions} />
      </RailRenderObserver>,
    );
    observer.mockClear();

    rerender(
      <RailRenderObserver value={observer}>
        <RailRow node={node} info={secondInfo} actions={firstActions} />
      </RailRenderObserver>,
    );
    expect(observer).toHaveBeenCalledTimes(1);
    await userEvent.setup().click(screen.getByText("Proj"));
    expect(firstInfo.activate).not.toHaveBeenCalled();
    expect(secondInfo.activate).toHaveBeenCalledTimes(1);
    observer.mockClear();

    rerender(
      <RailRenderObserver value={observer}>
        <RailRow node={node} info={secondInfo} actions={secondActions} />
      </RailRenderObserver>,
    );
    expect(observer).toHaveBeenCalledTimes(1);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Add to pinned" }));
    expect(firstActions.onToggleFavoriteProject).not.toHaveBeenCalled();
    expect(secondActions.onToggleFavoriteProject).toHaveBeenCalledWith(node.project);
  });

  test("non-project rows retain default memo behavior for a replaced node", () => {
    const observer = vi.fn();
    const rowInfo = info();
    const rowActions = actions();
    const session = apiNode();
    const firstNode = sessionRailNode(session);
    const { rerender } = render(
      <RailRenderObserver value={observer}>
        <RailRow node={firstNode} info={rowInfo} actions={rowActions} />
      </RailRenderObserver>,
    );
    observer.mockClear();

    rerender(
      <RailRenderObserver value={observer}>
        <RailRow
          node={{ ...firstNode, session: { ...session, title: "Replacement" } }}
          info={rowInfo}
          actions={rowActions}
        />
      </RailRenderObserver>,
    );

    expect(observer).toHaveBeenCalledTimes(1);
  });

  test("renders the project's name and compact rollup status", () => {
    const project = apiProject({ name: "prime-radiant", rollup_state: "errored" });
    render(<RailRow node={projectRailNode(project)} info={info()} actions={actions()} />);
    expect(screen.getByText("prime-radiant")).toBeTruthy();
    expect(screen.getByRole("img", { name: "Broken" })).toBeTruthy();
  });

  // UX fix: two projects with the same name are disambiguated upstream in
  // railNodes.ts (projectDisplayLabels), which stamps the decorated label
  // onto ProjectRailNode.displayName - the row just has to prefer it.
  test("prefers node.displayName over the bare project name, when set", () => {
    const project = apiProject({ name: "frontend" });
    render(
      <RailRow
        node={{ ...projectRailNode(project), displayName: "frontend (repoA)" }}
        info={info()}
        actions={actions()}
      />,
    );
    expect(screen.getByText("frontend (repoA)")).toBeTruthy();
    expect(screen.queryByText("frontend", { selector: "span" })).toBeNull();
  });

  test("falls back to the bare project name when displayName is unset (the common, non-colliding case)", () => {
    const project = apiProject({ name: "prime-radiant" });
    render(<RailRow node={projectRailNode(project)} info={info()} actions={actions()} />);
    expect(screen.getByText("prime-radiant")).toBeTruthy();
  });

  test("shows an attention Badge when rollup_attn is nonzero, hides it when zero", () => {
    const { rerender } = render(
      <RailRow node={projectRailNode(apiProject({ rollup_attn: 3 }))} info={info()} actions={actions()} />,
    );
    expect(screen.getByText("3")).toBeTruthy();

    rerender(<RailRow node={projectRailNode(apiProject({ rollup_attn: 0 }))} info={info()} actions={actions()} />);
    expect(screen.queryByText("0")).toBeNull();
  });

  // A host-first copy claims no aggregate rollup: rollup_state and rollup_attn
  // are project-wide wire facts, and an honest per-host count would need wire
  // support the manifest does not carry - the same line the host group row
  // itself draws. The attention rows still surface in Needs-you.
  test("a host copy shows no project rollup signal or badge", () => {
    const copy = {
      ...projectRailNode(apiProject({ rollup_state: "warning", rollup_attn: 3 })),
      id: "projectnode:p1@devbox",
      spawnHost: "devbox",
    } as ProjectRailNode;
    render(<RailRow node={copy} info={info()} actions={actions()} />);
    expect(screen.queryByTestId("rail-row-signal")).toBeNull();
    expect(screen.queryByText("3")).toBeNull();
  });

  // The canonical copy - the first in rail order, the same copy that
  // renders the project's overflow - is the one place the project-wide
  // rollup reads, so a collapsed host-first project still shows its
  // attention without claiming per-host counts under every host.
  test("the canonical copy carries the project's rollup, the one place it reads once", () => {
    const copy = {
      ...projectRailNode(apiProject({ rollup_state: "warning", rollup_attn: 3 })),
      id: "projectnode:p1@local",
      spawnHost: "local",
      canonicalCopy: true,
    } as ProjectRailNode;
    render(<RailRow node={copy} info={info()} actions={actions()} />);
    expect(screen.getByTestId("rail-row-signal")).toBeTruthy();
    expect(screen.getByText("3")).toBeTruthy();
  });

  // railNodes mints a fresh node when the canonical flip happens with the
  // copy's own id unchanged (a host-order change moved the first copy), so
  // the memo comparator must read the fields ProjectRow reads or the
  // memoized row keeps rendering the stale rollup.
  test("a canonical flip re-renders the memoized row, moving the rollup with it", () => {
    const project = apiProject({ rollup_state: "warning", rollup_attn: 3 });
    const before = {
      ...projectRailNode(project),
      id: "projectnode:p1@devbox",
      spawnHost: "devbox",
    } as ProjectRailNode;
    const after = {
      ...projectRailNode(project),
      id: "projectnode:p1@devbox",
      spawnHost: "devbox",
      canonicalCopy: true,
    } as ProjectRailNode;
    // Stable info/actions identities, the way Rail's Tree hands them down:
    // the node is the only thing that changed, so the memo decision rides
    // the node comparator alone.
    const rowInfo = info();
    const acts = actions();
    const { rerender } = render(<RailRow node={before} info={rowInfo} actions={acts} />);
    expect(screen.queryByText("3")).toBeNull();
    rerender(<RailRow node={after} info={rowInfo} actions={acts} />);
    expect(screen.getByText("3")).toBeTruthy();
  });

  // Project mutations are keyed by (source, project ID) - the row's menu
  // closes over the project object - so an ownership change (a host
  // attached or detached) must re-render the row or its actions fire with
  // the stale source list.
  test("an ownership change re-renders the memoized row, so actions carry the new sources", async () => {
    const before = {
      ...projectRailNode(apiProject({ sources: ["local", "devbox"] })),
      id: "projectnode:p1",
    } as ProjectRailNode;
    const afterProject = apiProject({ sources: ["local"] });
    const after = { ...projectRailNode(afterProject), id: "projectnode:p1" } as ProjectRailNode;
    const acts = actions();
    const rowInfo = info();
    const { rerender } = render(<RailRow node={before} info={rowInfo} actions={acts} />);
    rerender(<RailRow node={after} info={rowInfo} actions={acts} />);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Add to pinned" }));
    expect(acts.onToggleFavoriteProject).toHaveBeenCalledWith(afterProject);
  });

  test("menu offers 'Archive project' for an active project and calls onToggleArchiveProject", async () => {
    const acts = actions();
    const project = apiProject({ is_archived: false });
    render(<RailRow node={projectRailNode(project)} info={info()} actions={acts} />);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Archive project" }));
    expect(acts.onToggleArchiveProject).toHaveBeenCalledWith(project);
  });

  test("menu offers 'Unarchive project' for an already-archived project", async () => {
    render(<RailRow node={projectRailNode(apiProject({ is_archived: true }))} info={info()} actions={actions()} />);
    await openMenu(/actions for/i);
    expect(screen.getByRole("menuitem", { name: "Unarchive project" })).toBeTruthy();
  });

  test("menu offers 'Delete project…' and calls onDeleteProjectRequest on select", async () => {
    const acts = actions();
    const project = apiProject({
      loaded: true,
      session_count: 1,
      sessions: [apiNode({ row_id: "project:p1:local:a" })],
    });
    render(<RailRow node={projectRailNode(project)} info={info()} actions={acts} />);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Delete project…" }));
    expect(acts.onDeleteProjectRequest).toHaveBeenCalledWith(project);
  });

  // Deletion is local-only (cmd/evener-hub/project_delete.go refuses any
  // non-local or unknown source), but the row does not withhold the item on
  // that account: the judgement lives one level up, where the Rail's
  // onDeleteProjectRequest refuses a project a remote host also owns, with a
  // toast that names the hosts (Rail.test.tsx pins that). The person gets an
  // explanation instead of an item that is silently missing. These tests pin
  // the row's half of that split: the item is offered whatever the row's
  // ownership state reads as.
  test("menu offers 'Delete project…' for a project row a remote host also owns", async () => {
    const acts = actions();
    const project = apiProject({
      loaded: true,
      session_count: 2,
      sessions: [
        apiNode({ row_id: "project:p1:local:a" }),
        apiNode({ row_id: "project:p1:buildbox:t1", ref: "buildbox:t1", host_id: "buildbox", session_id: "t1" }),
      ],
    });
    render(<RailRow node={projectRailNode(project)} info={info()} actions={acts} />);
    await openMenu(/actions for/i);
    expect(screen.getByRole("menuitem", { name: "Delete project…" })).toBeTruthy();
    // The rest of the project menu is untouched - and the row is still a
    // project row with its own actions.
    expect(screen.getByRole("menuitem", { name: "Archive project" })).toBeTruthy();
    expect(screen.getByRole("menuitem", { name: "New session" })).toBeTruthy();
    expect(acts.onDeleteProjectRequest).not.toHaveBeenCalled();
  });

  test("menu offers 'Delete project…' for a remote-only project row", async () => {
    const project = apiProject({
      loaded: true,
      session_count: 1,
      sessions: [
        apiNode({ row_id: "project:p1:buildbox:t1", ref: "buildbox:t1", host_id: "buildbox", session_id: "t1" }),
      ],
    });
    render(<RailRow node={projectRailNode(project)} info={info()} actions={actions()} />);
    await openMenu(/actions for/i);
    expect(screen.getByRole("menuitem", { name: "Delete project…" })).toBeTruthy();
  });

  // A collapsed project loads its sessions on expand, so an unexpanded row
  // reads sessions: [] whatever a remote host owns - which is exactly why the
  // row's menu no longer reads ownership from them: the item is offered, and
  // the Rail's refusal is the guard.
  test("menu offers 'Delete project…' when the project's sessions have not loaded", async () => {
    const project = apiProject({ loaded: false, session_count: 2, sessions: [] });
    render(<RailRow node={projectRailNode(project)} info={info()} actions={actions()} />);
    await openMenu(/actions for/i);
    expect(screen.getByRole("menuitem", { name: "Delete project…" })).toBeTruthy();
    // The rest of the project menu is unaffected.
    expect(screen.getByRole("menuitem", { name: "Archive project" })).toBeTruthy();
  });

  // The one unloaded row that is still knowably controller-only: a project with
  // no rows at all. session_count counts every source's top-level rows
  // (hubcore.TreeProject.TotalSessionCount), so zero means no host owns a
  // session here and the source-less request cannot address another project.
  test("menu keeps 'Delete project…' for an empty controller project", async () => {
    const project = apiProject({ loaded: false, session_count: 0, sessions: [] });
    render(<RailRow node={projectRailNode(project)} info={info()} actions={actions()} />);
    await openMenu(/actions for/i);
    expect(screen.getByRole("menuitem", { name: "Delete project…" })).toBeTruthy();
  });

  // The navigation summary's own owning sources, which a controller-only
  // project omits: a present list settles ownership, and it lands on the Rail
  // side of the split, not the row's.
  test("menu offers 'Delete project…' when the summary names a remote owner", async () => {
    const project = withSources(apiProject(), ["local", "buildbox"]);
    render(<RailRow node={projectRailNode(project)} info={info()} actions={actions()} />);
    await openMenu(/actions for/i);
    expect(screen.getByRole("menuitem", { name: "Delete project…" })).toBeTruthy();
    expect(screen.getByRole("menuitem", { name: "New session" })).toBeTruthy();
  });

  test("menu keeps 'Delete project…' when the summary names only this controller as an owner", async () => {
    const project = withSources(apiProject(), ["local"]);
    render(<RailRow node={projectRailNode(project)} info={info()} actions={actions()} />);
    await openMenu(/actions for/i);
    expect(screen.getByRole("menuitem", { name: "Delete project…" })).toBeTruthy();
  });

  test("menu keeps 'Delete project…' for a project row only this controller owns", async () => {
    const project = apiProject({
      loaded: true,
      session_count: 1,
      sessions: [apiNode({ row_id: "project:p1:local:a" })],
    });
    render(<RailRow node={projectRailNode(project)} info={info()} actions={actions()} />);
    await openMenu(/actions for/i);
    expect(screen.getByRole("menuitem", { name: "Delete project…" })).toBeTruthy();
  });

  test("a childless project renders no chevron; a parent project's chevron trails its name", () => {
    const { rerender } = render(
      <RailRow node={projectRailNode(apiProject())} info={info({ hasChildren: false })} actions={actions()} />,
    );
    expect(screen.queryByTestId("rail-chevron")).toBeNull();

    rerender(<RailRow node={projectRailNode(apiProject())} info={info({ hasChildren: true })} actions={actions()} />);
    // Inline, right after the project name - the same trailing position a
    // session row's chevron takes, before the star/Badge slots.
    const name = screen.getByText("Proj");
    expect(name.nextElementSibling).toBe(screen.getByTestId("rail-chevron"));
  });

  test("menu never offers Rename for a project row - only sessions can be renamed", async () => {
    render(<RailRow node={projectRailNode(apiProject())} info={info()} actions={actions()} />);
    await openMenu(/actions for/i);
    expect(screen.queryByRole("menuitem", { name: "Rename" })).toBeNull();
  });

  test("shows a favorite star when the project is favorited, hides it otherwise", () => {
    const { rerender } = render(
      <RailRow node={projectRailNode(apiProject({ favorite: true }))} info={info()} actions={actions()} />,
    );
    expect(screen.getByTestId("favorite-star")).toBeTruthy();

    rerender(<RailRow node={projectRailNode(apiProject({ favorite: false }))} info={info()} actions={actions()} />);
    expect(screen.queryByTestId("favorite-star")).toBeNull();
  });

  test("menu offers 'Add to pinned' for an unfavorited project and calls onToggleFavoriteProject on select", async () => {
    const acts = actions();
    const project = apiProject({ favorite: false, key: "p1" });
    render(<RailRow node={projectRailNode(project)} info={info()} actions={acts} />);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Add to pinned" }));
    expect(acts.onToggleFavoriteProject).toHaveBeenCalledWith(project);
  });

  test("menu offers 'Remove from pinned' for a favorited project", async () => {
    render(<RailRow node={projectRailNode(apiProject({ favorite: true }))} info={info()} actions={actions()} />);
    await openMenu(/actions for/i);
    expect(screen.getByRole("menuitem", { name: "Remove from pinned" })).toBeTruthy();
  });

  test("the synthetic '(no project)' bucket gets no actions menu at all - archive/delete always 400 for it server-side", () => {
    render(
      <RailRow
        node={projectRailNode(apiProject({ key: "no-project", name: "(no project)" }))}
        info={info()}
        actions={actions()}
      />,
    );
    expect(screen.queryByRole("button")).toBeNull();
  });
});

// --- fix-wave: nested Menu triggers must not corrupt Tree's roving
// tabindex (Important) -----------------------------------------------
//
// These render RailRow through a REAL Tree (not the hand-built `info`
// double every other test in this file uses) - the bug this covers is
// specifically about Tree's own keyboard/focus machinery interacting with
// RailRow's content, which a fake `info` object can't exercise.
describe("roving-tabindex integration (Tree + RailRow)", () => {
  function twoSessionRows(): [SessionRailNode, SessionRailNode] {
    return [
      sessionRailNode(apiNode({ row_id: "rowA", ref: "local:a", title: "Row A" })),
      sessionRailNode(apiNode({ row_id: "rowB", ref: "local:b", title: "Row B" })),
    ];
  }

  function renderTree(nodes: SessionRailNode[], onActivate: (node: SessionRailNode) => void = () => {}) {
    return render(
      <Tree
        nodes={nodes}
        onToggle={() => {}}
        onActivate={onActivate}
        renderRow={(node, rowInfo) => <RailRow node={node} info={rowInfo} actions={actions()} />}
      />,
    );
  }

  test("only the roving treeitem is a Tab stop - neither row's actions trigger is", () => {
    renderTree(twoSessionRows());
    const treeitems = screen.getAllByRole("treeitem");
    expect(treeitems.map((el) => el.tabIndex)).toEqual([0, -1]); // Row A (first) starts as the roving one

    const triggers = screen.getAllByRole("button", { name: /actions for/i });
    expect(triggers).toHaveLength(2);
    for (const trigger of triggers) expect(trigger.tabIndex).toBe(-1);
  });

  test("focusing the roving treeitem exposes the session context without adding a title Tab stop", () => {
    vi.useFakeTimers();
    try {
      renderTree(twoSessionRows());
      const row = screen.getByRole("treeitem", { name: /Row A/ });
      const title = within(row).getByRole("button", { name: "Row A" });
      expect(title.tabIndex).toBe(-1);

      act(() => row.focus());
      act(() => vi.advanceTimersByTime(300));

      const card = screen.getByRole("tooltip");
      expect(within(card).getByText("Session")).toBeTruthy();
      expect(row.getAttribute("aria-describedby")).toBe(card.id);
    } finally {
      vi.useRealTimers();
    }
  });

  test("clicking a session title activates its row without moving focus into the title control", async () => {
    const activate = vi.fn();
    const user = userEvent.setup();
    renderTree(twoSessionRows(), activate);
    const row = screen.getByRole("treeitem", { name: /Row A/ });
    act(() => row.focus());

    await user.click(within(row).getByRole("button", { name: "Row A" }));

    expect(activate).toHaveBeenCalledOnce();
    expect(document.activeElement).toBe(row);
  });

  test("clicking an unfocused session title moves the tree's roving focus to that row", async () => {
    const activate = vi.fn();
    const user = userEvent.setup();
    renderTree(twoSessionRows(), activate);
    const rowA = screen.getByRole("treeitem", { name: /Row A/ });
    const rowB = screen.getByRole("treeitem", { name: /Row B/ });
    act(() => rowA.focus());

    await user.click(within(rowB).getByRole("button", { name: "Row B" }));

    expect(activate).toHaveBeenCalledOnce();
    expect(document.activeElement).toBe(rowB);
    expect(rowA.tabIndex).toBe(-1);
    expect(rowB.tabIndex).toBe(0);
  });

  test("clicking a title moves focus from another control in the same row to the treeitem", async () => {
    const user = userEvent.setup();
    renderTree(twoSessionRows());
    const row = screen.getByRole("treeitem", { name: /Row A/ });
    const action = within(row).getByRole("button", { name: /actions for/i });
    act(() => action.focus());

    await user.click(within(row).getByRole("button", { name: "Row A" }));

    expect(document.activeElement).toBe(row);
  });

  test("a non-primary press on a title leaves focus and the event's default behavior alone", () => {
    renderTree(twoSessionRows());
    const rowA = screen.getByRole("treeitem", { name: /Row A/ });
    const rowB = screen.getByRole("treeitem", { name: /Row B/ });
    act(() => rowA.focus());

    const allowed = fireEvent.mouseDown(within(rowB).getByRole("button", { name: "Row B" }), { button: 1 });

    expect(allowed).toBe(true);
    expect(document.activeElement).toBe(rowA);
    expect(rowA.tabIndex).toBe(0);
    expect(rowB.tabIndex).toBe(-1);
  });

  test("clicking a session title releases unrelated editable focus", async () => {
    const activate = vi.fn();
    const user = userEvent.setup();
    const input = document.createElement("input");
    document.body.appendChild(input);
    try {
      renderTree(twoSessionRows(), activate);
      act(() => input.focus());

      await user.click(screen.getByRole("button", { name: "Row A" }));

      expect(activate).toHaveBeenCalledOnce();
      expect(document.activeElement).not.toBe(input);
      expect(document.activeElement).not.toBe(screen.getByRole("button", { name: "Row A" }));
    } finally {
      input.remove();
    }
  });

  test("Tab from before the tree lands on the roving treeitem, never a row's own trigger", async () => {
    render(
      <>
        <button type="button">Before</button>
        <Tree
          nodes={twoSessionRows()}
          onToggle={() => {}}
          onActivate={() => {}}
          renderRow={(node, rowInfo) => <RailRow node={node} info={rowInfo} actions={actions()} />}
        />
      </>,
    );
    const user = userEvent.setup();
    act(() => screen.getByRole("button", { name: "Before" }).focus());
    await user.tab();
    expect(document.activeElement).toBe(screen.getByRole("treeitem", { name: /Row A/ }));
  });

  test("ArrowDown on a row's trigger opens the menu; the tree's roving tabindex survives closing it again (post-Escape corruption probe)", () => {
    // Reproduces the reviewer's exact probe, inverted. The corruption is
    // NOT visible right after opening - FocusScope's own mount effect
    // (widgets/focusscope/index.tsx) captures document.activeElement as
    // its restore target, THEN focuses the popup's first item; that
    // second focus move bubbles back up to Row A's own treeitem (the
    // popup is rendered INSIDE it) and reasserts currentId="rowA" as a
    // side effect, momentarily masking the bug. But WITHOUT
    // stopPropagation, Tree's own moveTo("rowB") already ran (and moved
    // real DOM focus to Row B's treeitem) BEFORE that effect captured its
    // restore target - so the restore target FocusScope captured is Row
    // B's treeitem, not Row A's trigger. Closing the menu (Escape unmounts
    // FocusScope, running its cleanup) restores focus to that stale
    // target: Row B, silently stealing the roving tabindex out from under
    // Row A even though the menu that just closed belonged to Row A.
    renderTree(twoSessionRows());
    const rowATreeitem = screen.getByRole("treeitem", { name: /Row A/ });
    const rowBTreeitem = screen.getByRole("treeitem", { name: /Row B/ });
    const rowATrigger = within(rowATreeitem).getByRole("button", { name: /actions for/i });

    act(() => rowATrigger.focus());
    fireEvent.keyDown(rowATrigger, { key: "ArrowDown" });
    expect(screen.getByRole("menu")).toBeTruthy(); // the menu still opens

    fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
    expect(screen.queryByRole("menu")).toBeNull(); // closed

    expect(rowATreeitem.tabIndex).toBe(0); // still Row A's roving tabindex...
    expect(rowBTreeitem.tabIndex).toBe(-1); // ...not silently moved to Row B
    expect(document.activeElement).toBe(rowATrigger); // and focus is back on Row A's own trigger
  });
});

// --- the actions share the right slot with the timestamp -----------------
//
// jsdom applies no stylesheet at all (vite.config.ts's test block enables
// no `css` processing), so "the revealed menu covers the timestamp" is not
// assertable against a rendered tree here. These read RailRow.module.css off
// disk and pin the structure that makes it true - same mechanism as
// styles/display-gates.test.ts and widgets/tooltip's own touch gate.
describe("shared right slot (RailRow.module.css)", () => {
  // 2026-08 sidebar UX rework, successor to the issue #196 fix it keeps the
  // guarantee of. The #196 rework made `.actions` a real in-flow flex item
  // beside the timestamp, so flexbox reserved it space and the trailing
  // disclosure chevron could never be laid out under it (the pre-#196
  // absolutely-positioned overlay swallowed the chevron's clicks, invisibly,
  // because CSS opacity never disables hit-testing). The cost was that every
  // row's title column stayed narrower by the menu's full width at rest as
  // much as on hover. The shared right slot keeps the structural guarantee -
  // the slot is the in-flow item, the chevron lives in .textCol to its LEFT -
  // while the menu and the timestamp share ONE grid cell: the menu borrows
  // the timestamp's space instead of adding its own, and the two swap
  // visibility on reveal. jsdom applies no stylesheet and
  // getBoundingClientRect() always reports zero there, so this describe block
  // can only pin the STYLESHEET CONTRACT (same discipline as
  // token-contract.test.ts); the actual non-overlap geometry and per-state
  // hit-testing, at real sidebar widths with a real truncating title, is
  // what layoutguard's rail-row-chevron-actions-overlap case proves against
  // a real browser.
  test("the actions share one grid cell with the right-slot occupant - never an overlay", () => {
    const slotRule = topRuleBlock(RAIL_CSS, ".rightSlot");
    expect(slotRule).toMatch(/display:\s*grid/);
    // The slot is the in-flow flex item (flexbox reserves its width, so the
    // chevron in .textCol can never be pushed underneath anything in it).
    expect(slotRule).toMatch(/flex:\s*none/);
    // Both children occupy the same area, so the slot is only ever as wide
    // as the WIDER of occupant and menu - the menu borrows the timestamp's
    // space rather than reserving its own beside it.
    const cellRule = topRuleBlock(RAIL_CSS, ".rightSlot > *");
    expect(cellRule).toMatch(/grid-area:\s*1\s*\/\s*1/);

    const actionsRule = topRuleBlock(RAIL_CSS, ".actions");
    expect(actionsRule).not.toMatch(/position:\s*absolute/);
    expect(actionsRule).not.toMatch(/\bright:\s*0/);
    // Hidden means BOTH: opacity never disables hit-testing (issue #196), so
    // visibility is what keeps the at-rest menu from eating the clicks it
    // covers.
    expect(actionsRule).toMatch(/opacity:\s*0/);
    expect(actionsRule).toMatch(/visibility:\s*hidden/);
  });

  test("revealing the menu covers the occupant - and only on a hover-capable desktop pointer", () => {
    // The reveal flips BOTH halves of the shared cell. Menu side (top-level
    // rule): the same three conditions as ever - row hover, treeitem focus,
    // this row's own menu held open.
    const revealStart = RAIL_CSS.indexOf('[role="treeitem"]:focus .actions');
    expect(revealStart, "row hover must reveal the actions").toBeGreaterThanOrEqual(0);
    const revealOpen = RAIL_CSS.indexOf("{", revealStart);
    expect(revealOpen, "the action reveal selectors must own a rule").toBeGreaterThan(revealStart);
    const revealBody = blockBody(RAIL_CSS, revealOpen);
    expect(revealBody).toMatch(/opacity:\s*1/);
    expect(revealBody).toMatch(/visibility:\s*visible/);
    const revealTargets = RAIL_CSS.slice(revealStart, revealOpen)
      .split(",")
      .map((s) => s.trim());
    expect(revealTargets).toEqual(
      expect.arrayContaining([
        '[role="treeitem"]:focus .actions',
        '.actions:has(button[aria-expanded="true"])',
        ".railRow:hover .actions",
      ]),
    );

    // Occupant side: the same three conditions hide the timestamp/Badge
    // under the menu. The flip lives inside a hover-capable desktop media
    // block - on touch the actions are always visible BESIDE the occupant
    // (the 899px block below), and a sticky tap-hover or treeitem focus
    // must never make the timestamp vanish out from under the row. Rules
    // nested in @media mangle the flat matchAll above, so this half is
    // pinned against the media block's own text.
    const flipMedia = mediaBlock(RAIL_CSS, "hover: hover) and (min-width: 900px");
    const flipBlock = nestedRuleBlock(flipMedia, '[role="treeitem"]:focus .rightSlot > :not(.actions)');
    for (const selector of [
      '[role="treeitem"]:focus .rightSlot > :not(.actions)',
      '.rightSlot:has(button[aria-expanded="true"]) > :not(.actions)',
      ".railRow:hover .rightSlot > :not(.actions)",
    ]) {
      expect(flipMedia).toContain(selector);
    }
    expect(flipBlock).toMatch(/opacity:\s*0/);
    expect(flipBlock).toMatch(/visibility:\s*hidden/);
  });

  test("the disclosure chevron carries no stacking-order fix - it doesn't need one", () => {
    // The z-index approach the #196 rework replaced required
    // position:relative + z-index on .chevronButton to win a stacking fight
    // against .actions. Neither is needed (or wanted - see the describe
    // block's own comment) now that the chevron and the menu are
    // layout-disjoint by construction.
    const chevronRule = topRuleBlock(RAIL_CSS, ".chevronButton");
    expect(chevronRule).not.toMatch(/position:\s*relative/);
    expect(chevronRule).not.toMatch(/z-index:/);
  });

  test("nothing paints a mask over .actions - the shared cell needs no background to hide behind", () => {
    // The old absolute overlay needed a background (to match whatever it
    // covered) and a gradient + padding-left (so its leading edge didn't
    // slice covered text mid-glyph). An in-flow grid item covers its own
    // cell and nothing else, so none of that machinery belongs here - its
    // reappearance would be a sign the overlay design crept back in.
    const actionsRule = topRuleBlock(RAIL_CSS, ".actions");
    expect(actionsRule).not.toMatch(/background:/);
    expect(actionsRule).not.toMatch(/linear-gradient/);
    expect(actionsRule).not.toMatch(/padding-left:/);
  });

  test("the row's menu trigger hugs its glyph, right-justified to the slot's edge", () => {
    // The Menu widget's trigger is padded for a standalone button
    // (--space-4 on both sides), which centered the "..." glyph ~16px in
    // from the slot's right edge - the x the timestamp's own text ends at.
    // The row's override keeps padding only on the leading side, so the
    // revealed menu right-justifies to the timestamp's own edge (and the
    // shared cell narrows to the glyph's real width). Scoped by attribute
    // so a project row's "+" IconButton keeps its own square geometry.
    //
    // Anchored to the TOP-LEVEL rule (column 0): the same selector also
    // appears inside @media (pointer: coarse), where the widened tap target
    // centres the glyph instead - that override is the tap-floor describe's
    // own assertion above, not this one's.
    const justifyRule = topRuleBlock(RAIL_CSS, '.actions button[aria-haspopup="menu"]');
    expect(justifyRule).toMatch(/padding:\s*0\s+0\s+0\s+var\(--space-2\)/);
  });

  // The signal dot keeps a FIXED width and refuses to flex: its outdent
  // arithmetic (margin-left cancels width + the title line's gap) only
  // holds if the box it cancels is a constant. jsdom applies no stylesheet,
  // so this is only checkable against the (comment-stripped) stylesheet
  // text.
  test("the .signal slot reserves a fixed width and never flexes", () => {
    const rule = topRuleBlock(RAIL_CSS, ".signal");
    expect(rule).toMatch(/width:\s*(var\(--space-\d+\)|\d+px)/);
    expect(rule).toMatch(/flex:\s*none|flex-shrink:\s*0/);
  });

  test("touch keeps the actions permanently open beside the occupant instead of relying on a hover it doesn't have", () => {
    // Below the mobile breakpoint there's no hover to reveal the actions
    // with, so this block forces them visible AND turns the shared cell back
    // into an ordinary flex row - on touch the occupant and the menu sit
    // side by side; the visibility swap above is a desktop-hover mechanism.
    const mobileRules = mediaBlock(RAIL_CSS, "max-width: 899px");
    const rightSlotBlock = nestedRuleBlock(mobileRules, ".rightSlot");
    const actionsBlock = nestedRuleBlock(mobileRules, ".actions");
    expect(rightSlotBlock).toMatch(/display:\s*flex/);
    expect(actionsBlock).toMatch(/opacity:\s*1/);
    expect(actionsBlock).toMatch(/visibility:\s*visible/);
    expect(actionsBlock).not.toMatch(/position:/);
    expect(actionsBlock).not.toMatch(/background:/);
  });
});

test("an incompatible daemon has an attention signal and restart instruction", () => {
  renderRow({ state: "restartRequired", live: true, branch: "" });
  expect(screen.getByRole("img", { name: "Needs you" })).toBeTruthy();
  const panel = hoverForTooltip(screen.getByText("Fix flaky test"));
  expect(within(panel).getByText("Restart required")).toBeTruthy();
});

test.each([
  ["own activity", { state: "active" }, { state: "idle" }],
  ["job activity", { state: "idle", running_job_count: 1 }, { state: "idle" }],
] as const)("normalized navigation preserves %s in the rendered sidebar", (_name, parentState, childState) => {
  const resource = normalizedRailResource(
    { kind: "project_page", projectKey: "project", tier: "current", offset: 0, limit: 50 },
    { ...parentState, running_job_count: "running_job_count" in parentState ? parentState.running_job_count : 0 },
    childState,
  );
  const parent = [...selectRailModel(resource).sessions.values()].find((session) => session.ref === "parent");
  if (!parent) throw new Error("missing parent");
  render(<RailRow node={sessionRailNode(parent)} info={info()} actions={actions()} />);
  expect(screen.getByRole("img", { name: "Running" })).toBeTruthy();
});

test("restart-required navigation disables daemon actions in the sidebar menu", async () => {
  renderRow({ state: "restartRequired", rename: false, live: true });
  await openMenu(/actions for/i);
  expect(screen.getByRole("menuitem", { name: "Shut down" }).getAttribute("aria-disabled")).toBe("true");
  expect(screen.getByRole("menuitem", { name: "Rename" }).getAttribute("aria-disabled")).toBe("true");
});

test("restart explanation survives job activity", () => {
  renderRow({
    state: "restartRequired",
    live: true,
    branch: "",
    running_job_count: 1,
  });
  expect(screen.getByRole("img", { name: "Needs you" })).toBeTruthy();
  const panel = hoverForTooltip(screen.getByText("Fix flaky test"));
  expect(within(panel).getByText("Restart required")).toBeTruthy();
  expect(within(panel).getByText("1 running")).toBeTruthy();
});

// The rail's organize-by host group row (a "Host, then project" top group, a
// "Project, then host" branch inside a project, or a Live-section
// subheader): a synthetic branch row with the project row's anatomy, a drawn
// host glyph where the signal dot would sit, and the session rows' own
// offline convention.
describe("host group row", () => {
  test("leads with the drawn host glyph and names the host by its id", () => {
    render(<RailRow node={hostGroupNode()} info={info({ hasChildren: true })} actions={actions()} />);
    const glyph = screen.getByTestId("rail-row-host-glyph");
    expect(glyph.tagName.toLowerCase()).toBe("svg");
    expect(glyph.getAttribute("aria-hidden")).toBe("true");
    expect(screen.getByText("devbox")).toBeTruthy();
  });

  test("names a host by its manifest label rather than its id, in the tooltip too", () => {
    const online = render(
      <RailRow
        node={hostGroupNode({ id: "host:local", host: { id: "local", label: "this host", online: true } })}
        info={info({ hasChildren: true })}
        actions={actions()}
      />,
    );
    expect(screen.getByText("this host")).toBeTruthy();
    expect(screen.getByTestId("rail-row-host-group").getAttribute("title")).toBe("Host this host");
    online.unmount();
    render(
      <RailRow
        node={hostGroupNode({ id: "host:local", host: { id: "local", label: "this host", online: false } })}
        info={info({ hasChildren: true })}
        actions={actions()}
      />,
    );
    expect(screen.getByTestId("rail-row-host-group").getAttribute("title")).toBe("Host this host is offline");
  });

  test("an online host reads plain and its tooltip names just the host", () => {
    render(<RailRow node={hostGroupNode()} info={info({ hasChildren: true })} actions={actions()} />);
    const label = screen.getByText("devbox");
    expect(label.className).not.toContain(railStyles.hostOffline as string);
    expect(screen.queryByTestId("rail-row-host-group-offline")).toBeNull();
    expect(screen.getByTestId("rail-row-host-group").getAttribute("title")).toBe("Host devbox");
  });

  test("an offline host reads italic-dimmed with an '(offline)' suffix and says so in its tooltip", () => {
    render(
      <RailRow
        node={hostGroupNode({ id: "host:ci-runner", host: { id: "ci-runner", label: "ci-runner", online: false } })}
        info={info({ hasChildren: true })}
        actions={actions()}
      />,
    );
    const label = screen.getByText("ci-runner");
    expect(label.className).toContain(railStyles.hostOffline as string);
    expect(screen.getByTestId("rail-row-host-group-offline").textContent).toBe(" (offline)");
    expect(screen.getByTestId("rail-row-host-group").getAttribute("title")).toBe("Host ci-runner is offline");
  });

  test("activates on label click, like its sibling rows", async () => {
    const rowInfo = info({ hasChildren: true });
    render(<RailRow node={hostGroupNode()} info={rowInfo} actions={actions()} />);
    await userEvent.setup().click(screen.getByText("devbox"));
    expect(rowInfo.activate).toHaveBeenCalledTimes(1);
  });

  test("carries a trailing chevron that toggles", async () => {
    const rowInfo = info({ hasChildren: true, expanded: false });
    render(<RailRow node={hostGroupNode()} info={rowInfo} actions={actions()} />);
    await userEvent.setup().click(screen.getByTestId("rail-chevron"));
    expect(rowInfo.toggle).toHaveBeenCalledTimes(1);
  });

  // A host is infrastructure, not triage: no state dot, no rollup badge (the
  // manifest carries no per-host attention count), and nothing to act on -
  // the rows under the group keep their own signals and menus.
  test("carries no signal dot, no badge, and nothing to click but itself", () => {
    render(<RailRow node={hostGroupNode()} info={info({ hasChildren: true })} actions={actions()} />);
    expect(screen.queryByTestId("rail-row-signal")).toBeNull();
    expect(screen.queryByRole("button")).toBeNull();
    expect(screen.getByTestId("rail-row-host-group").textContent).toBe("devbox");
  });
});
