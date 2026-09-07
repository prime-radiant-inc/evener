import { act, cleanup, fireEvent, render as renderUI, screen, within } from "@testing-library/react";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../protocol/testing/fakeClient";
import type {
  NavigationInvalidatedPayload,
  NavigationManifest,
  NavigationProjectResource,
  NavigationSessionSummary,
  NavigationSnapshot,
} from "../../protocol/types.gen";
import { connectionStore } from "../../stores/connection";
import { type NormalizedResource, normalizedGraphFromSnapshot } from "../../stores/navigation/codec";
import { navigationStore, resetNavigationStoreForTests } from "../../stores/navigation/store";
import {
  keyID,
  navigationOwnedContainerKey,
  navigationRootContainerKey,
  navigationViewScope,
  type ResourceKey,
  type ResourceState,
} from "../../stores/navigation/types";
import { threadsStore } from "../../stores/threads";
import { getToasts, resetToastStoreForTests } from "../../widgets/toast/store";
import { ClientProvider } from "../clientContext";
import { resetWorkspaceStoreForTests } from "../workspace";
import { adaptNavigationResources, Rail } from "./Rail";
import railStyles from "./Rail.module.css";
import { EXPANSION_STORAGE_KEY } from "./railExpansion";
import { projectNodes } from "./railNodes";
import { RailRenderObserver } from "./railRenderObserver";

function summary(overrides: Partial<NavigationSessionSummary> = {}): NavigationSessionSummary {
  return {
    ref: "local:a",
    host_id: "local",
    session_id: "a",
    title: "Session A",
    project: "Proj",
    state: "idle",
    kind: "session",
    live: true,
    children: [],
    ...overrides,
  };
}
function manifest(overrides: Partial<NavigationManifest> = {}): NavigationManifest {
  return {
    generation_id: "g1",
    revision: 1,
    sources: [],
    attentionSummary: { needsYou: 0, error: 0, working: 0 },
    sections: { live: { count: 1 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
    catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
    ...overrides,
  };
}
function emptyManifest(): NavigationManifest {
  return manifest({
    sections: { live: { count: 0 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
    catalogs: { projects: { count: 0 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
  });
}
function resource<T>(key: ResourceKey, data: T): ResourceState<T> {
  return {
    key,
    data,
    loadedRevision: 1,
    targetRevision: null,
    forceToken: 0,
    etag: "e",
    loading: false,
    stale: false,
    error: null,
    generationID: "g1",
  };
}
function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}
function installState(resources: ResourceState[] = [], m = manifest()) {
  navigationStore.setState({
    mode: "v2",
    capability: { version: 1, generationId: "g1", sequence: 1, readVersions: [2] },
    clientGenerationID: "g1",
    manifest: resource({ kind: "manifest" }, m) as ResourceState<NavigationManifest>,
    resources: new Map(resources.map((entry) => [keyID(entry.key), entry])),
    expanded: new Map(),
    attention: { changed: [], summary: m.attentionSummary },
    loadManifest: vi.fn(),
    loadSection: vi.fn(),
    loadCatalog: vi.fn(),
    loadPinCatalog: vi.fn(),
    loadPinSection: vi.fn(),
    loadProject: vi.fn(),
    loadProjectPage: vi.fn(),
    lookupLocation: vi.fn(),
    setExpanded: vi.fn(),
    toggleExpanded: vi.fn(),
  });
}
function sectionResource(section: "live" | "needs_you", rows: NavigationSessionSummary[], remaining = 0) {
  return resource(
    { kind: "section", section, offset: 0, limit: 50 },
    { generation_id: "g1", revision: 1, sessions: rows, remaining, truncated: false },
  );
}
function projectResource(
  key: string,
  rows: NavigationSessionSummary[],
  remaining = 0,
): ResourceState<NavigationProjectResource> {
  return resource(
    { kind: "project", projectKey: key },
    {
      generation_id: "g1",
      revision: 1,
      key,
      current: { sessions: rows, remaining },
      recent: { sessions: [], remaining: 0 },
      archived: { sessions: [], remaining: 0 },
      truncated: false,
    },
  );
}
function catalogResource(
  projects: Array<{ key: string; name: string; session_count: number; default_expanded?: boolean }>,
) {
  return resource(
    { kind: "catalog", catalog: "projects", offset: 0, limit: 100 },
    { generation_id: "g1", revision: 1, projects, remaining: 0 },
  );
}

function normalizedResource<T>(key: ResourceKey, data: T, snapshot: NavigationSnapshot): ResourceState<T> {
  const normalized: NormalizedResource = {
    key,
    graph: normalizedGraphFromSnapshot(snapshot),
    version: { generationId: "g1", revision: 1, etag: "e" },
    presence: "present",
  };
  return { ...resource(key, data), normalized };
}

function scopedEntityKey(key: ResourceKey, digit: string): string {
  return `${navigationViewScope(key)}/entity/${digit.repeat(64)}`;
}

function sessionEntity(key: string, row: NavigationSessionSummary) {
  return { key, kind: "session", value: row } as const;
}

function childContainer(entityKey: string) {
  return {
    key: navigationOwnedContainerKey(entityKey, "children"),
    owner: { kind: "entity" as const, entityKey, slot: "children" },
    children: [],
  };
}

function graphProjectResource(
  projectKey: string,
  projectDigit: string,
  sessionDigit: string,
  session: NavigationSessionSummary,
) {
  const key = { kind: "project", projectKey } as const;
  const projectEntityKey = scopedEntityKey(key, projectDigit);
  const sessionKey = scopedEntityKey(key, sessionDigit);
  return normalizedResource(
    key,
    {
      generation_id: "g1",
      revision: 1,
      key: projectKey,
      current: { sessions: [session], remaining: 0 },
      recent: { sessions: [], remaining: 0 },
      archived: { sessions: [], remaining: 0 },
      truncated: false,
    },
    {
      metadata: { current_remaining: 0, recent_remaining: 0, archived_remaining: 0 },
      entities: [
        { key: projectEntityKey, kind: "project", value: { key: projectKey } },
        sessionEntity(sessionKey, session),
      ],
      containers: [
        ...(["current", "recent", "archived"] as const).map((tier) => ({
          key: navigationOwnedContainerKey(projectEntityKey, tier),
          owner: { kind: "entity" as const, entityKey: projectEntityKey, slot: tier },
          children: tier === "current" ? [sessionKey] : [],
        })),
        childContainer(sessionKey),
      ],
    },
  );
}

function render(ui: ReactElement, client = new FakeClient()) {
  return renderUI(ui, { wrapper: ({ children }) => <ClientProvider client={client}>{children}</ClientProvider> });
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetNavigationStoreForTests();
  resetNavigationStoreForTests();
  resetToastStoreForTests();
  resetWorkspaceStoreForTests();
  localStorage.clear();
});
afterEach(() => {
  cleanup();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  vi.unstubAllGlobals();
});

describe("resource-backed Rail", () => {
  test("places identity and Settings before Search in the top row and preserves navigation", () => {
    installState();
    connectionStore.setState({ serverInfo: { name: "evener-hub", version: "0.0.0" } });
    const onHide = vi.fn();
    render(<Rail onHide={onHide} />);
    const brand = within(screen.getByTestId("rail-brand"));
    const identity = brand.getByText("evener-hub");
    const settings = brand.getByRole("button", { name: "Settings" });
    const search = brand.getByRole("button", { name: "Search" });
    const hide = brand.getByRole("button", { name: "Hide sidebar" });
    for (const [left, right] of [
      [identity, settings],
      [settings, search],
      [search, hide],
    ] as const) {
      expect(left.compareDocumentPosition(right) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    }
    expect(screen.getAllByText("evener-hub")).toHaveLength(1);
    expect(screen.getAllByRole("button", { name: "Settings" })).toHaveLength(1);
    const previousPath = window.location.pathname;
    try {
      window.history.replaceState({}, "", "/new");
      fireEvent.click(settings);
      expect(window.location.pathname).toBe("/settings");
      fireEvent.click(hide);
      expect(onHide).toHaveBeenCalledOnce();
    } finally {
      window.history.replaceState({}, "", previousPath);
    }
  });

  // The header's three icons are drawn, not typed: global.css subsets Inter to
  // Latin, so the text glyphs these buttons carried (the gear, the magnifier,
  // the burger) came from whatever system fallback happened to have those code
  // points, at whatever weight it drew them - right beside the app's own SVG
  // chevrons and open-box icon.
  test("the header icon buttons draw SVG icons, not text glyphs outside the subsetted font", () => {
    installState();

    render(<Rail onHide={vi.fn()} />);

    const brand = within(screen.getByTestId("rail-brand"));
    for (const label of ["Settings", "Search", "Hide sidebar"]) {
      const button = brand.getByRole("button", { name: label });
      expect(button.querySelector("svg")).toBeTruthy();
      expect(button.textContent?.trim()).toBe("");
    }
  });

  test("renders loaded global and project resources without a transport read", () => {
    installState([
      sectionResource("live", [summary({ title: "Live resource" })]),
      catalogResource([{ key: "p", name: "Proj", session_count: 1 }]),
      projectResource("p", [summary({ title: "Project resource" })]),
    ]);
    render(<Rail />);
    expect(screen.getByText("Live resource")).toBeTruthy();
    expect(screen.getAllByText("Proj").length).toBeGreaterThan(0);
  });

  test("can delegate scrolling to its parent container", () => {
    installState([sectionResource("live", [summary({ title: "Live resource" })])]);
    const { container } = render(<Rail scrollOwner="parent" />);
    const body = container.querySelector(`.${railStyles.body}`);

    expect(body).toBeTruthy();
    expect(railStyles.parentScrollBody).toBeTruthy();
    expect(body?.className.split(/\s+/)).toContain(railStyles.parentScrollBody);
  });
  test("shows the settled empty state in v2 mode", () => {
    const empty = emptyManifest();
    installState([], empty);

    render(<Rail />);

    expect(screen.queryByRole("status", { name: "Loading" })).toBeNull();
    expect(screen.getByText(/no sessions yet/i)).toBeTruthy();
  });
  // The empty state sits directly under the rail's own "+ New session" button,
  // so it cannot answer "how do I start one?" with the command line.
  test("the rail's empty state does not send people to the command line", () => {
    installState([], emptyManifest());

    render(<Rail />);

    expect(screen.queryByText(/command line/i)).toBeNull();
    expect(screen.getByText("No sessions yet")).toBeTruthy();
  });
  test.each(["v2"] as const)("shows a visible skeleton for a pending %s manifest until it settles", (mode) => {
    const empty = emptyManifest();
    const pendingManifest = {
      ...resource({ kind: "manifest" }, empty),
      data: null,
      loading: true,
    } as ResourceState<NavigationManifest>;
    installState([], empty);
    navigationStore.setState({ mode, manifest: pendingManifest });

    render(<Rail />);

    expect(screen.getByRole("status", { name: "Loading" })).toBeTruthy();
    expect(screen.queryByText(/no sessions yet/i)).toBeNull();
    act(() => navigationStore.setState({ manifest: resource({ kind: "manifest" }, empty) }));
    expect(screen.queryByRole("status", { name: "Loading" })).toBeNull();
    expect(screen.getByText(/no sessions yet/i)).toBeTruthy();
  });
  test("shows a visible skeleton for a pending v2 resource until it settles", () => {
    const empty = emptyManifest();
    const pendingSection = {
      ...sectionResource("live", []),
      data: null,
      loading: true,
    } as ResourceState;
    installState([pendingSection], empty);
    navigationStore.setState({ mode: "v2" });

    render(<Rail />);

    expect(screen.getByRole("status", { name: "Loading" })).toBeTruthy();
    expect(screen.queryByText(/no sessions yet/i)).toBeNull();
    const settledSection = sectionResource("live", []);
    act(() => navigationStore.setState({ resources: new Map([[keyID(settledSection.key), settledSection]]) }));
    expect(screen.queryByRole("status", { name: "Loading" })).toBeNull();
    expect(screen.getByText(/no sessions yet/i)).toBeTruthy();
  });
  test("does not treat stale resources as pending and keeps last-good rows visible", () => {
    const empty = emptyManifest();
    const staleEmpty = { ...sectionResource("live", []), stale: true };
    installState([staleEmpty], empty);
    navigationStore.setState({ mode: "v2" });

    render(<Rail />);

    expect(screen.queryByRole("status", { name: "Loading" })).toBeNull();
    expect(screen.getByText(/no sessions yet/i)).toBeTruthy();

    const staleLastGood = {
      ...sectionResource("live", [summary({ title: "Last good session" })]),
      stale: true,
    };
    act(() => navigationStore.setState({ resources: new Map([[keyID(staleLastGood.key), staleLastGood]]) }));
    expect(screen.queryByRole("status", { name: "Loading" })).toBeNull();
    expect(screen.getByText("Last good session")).toBeTruthy();
    expect(screen.queryByText(/no sessions yet/i)).toBeNull();
  });
  test("transitions from unknown loading to a terminal navigation error", () => {
    const empty = emptyManifest();
    installState([], empty);
    navigationStore.setState({ mode: "unknown" });

    render(<Rail />);

    expect(screen.getByRole("status", { name: "Loading" })).toBeTruthy();
    expect(screen.queryByText(/no sessions yet/i)).toBeNull();
    act(() => navigationStore.setState({ mode: "error" }));
    expect(screen.queryByRole("status", { name: "Loading" })).toBeNull();
    expect(screen.getByText(/couldn't load sessions/i)).toBeTruthy();
  });
  test("expanding a summary loads one canonical project root", async () => {
    const loadProject = vi.fn().mockResolvedValue(undefined);
    installState([catalogResource([{ key: "p", name: "Proj", session_count: 1 }])]);
    navigationStore.setState({ loadProject });
    render(<Rail />);
    fireEvent.click(screen.getByText("Proj"));
    await act(async () => undefined);
    expect(loadProject).toHaveBeenCalledTimes(1);
    expect(loadProject).toHaveBeenCalledWith("p");
  });
  test.each(["projects", "archived_projects", "test_runs"] as const)(
    "hydrates a default-expanded project discovered on a later v2 %s catalog page exactly once",
    async (catalog) => {
      const projectKey = `${catalog}-late`;
      const catalogKey = { kind: "catalog", catalog, offset: 1, limit: 100 } as const;
      const projectSummary = {
        key: projectKey,
        name: `Late ${catalog}`,
        session_count: 1,
        default_expanded: true,
      };
      const summaryKey = scopedEntityKey(catalogKey, "1");
      const catalogPage = normalizedResource(
        catalogKey,
        { projects: [projectSummary], remaining: 0 },
        {
          metadata: { remaining: 0 },
          entities: [{ key: summaryKey, kind: "project", value: projectSummary }],
          containers: [
            {
              key: navigationRootContainerKey(catalogKey, "projects"),
              owner: { kind: "resource_root", slot: "projects" },
              children: [summaryKey],
            },
          ],
        },
      );
      const loaded = graphProjectResource(
        projectKey,
        "2",
        "3",
        summary({ ref: `local:${projectKey}`, session_id: projectKey, title: `Loaded ${catalog}` }),
      );
      const loadProject = vi.fn(async (_projectKey: string): Promise<ResourceState<NavigationProjectResource>> => {
        navigationStore.setState((state) => ({
          resources: new Map([...state.resources, [keyID(loaded.key), loaded as ResourceState]]),
        }));
        return loaded;
      });
      installState([catalogPage as ResourceState]);
      navigationStore.setState({ mode: "v2", loadProject });

      render(<Rail />);
      await act(async () => undefined);

      expect(loadProject).toHaveBeenCalledTimes(1);
      expect(loadProject).toHaveBeenCalledWith(projectKey);
      const model = adaptNavigationResources(navigationStore.getState());
      const collection =
        catalog === "projects"
          ? model.projects
          : catalog === "archived_projects"
            ? model.archivedProjects
            : model.testRuns;
      expect(collection.find((project) => project.key === projectKey)).toMatchObject({
        loaded: true,
        sessions: [{ title: `Loaded ${catalog}` }],
      });
    },
  );
  test("a settled gone v2 project stays unloaded and is not rehydrated", async () => {
    const catalog = catalogResource([{ key: "gone", name: "Gone", session_count: 1, default_expanded: true }]);
    const stale = graphProjectResource("gone", "4", "5", summary({ ref: "local:gone", title: "Deleted" }));
    const gone = {
      ...stale,
      normalized: { ...stale.normalized!, presence: "gone" as const },
    };
    const loadProject = vi.fn().mockResolvedValue(undefined);
    installState([catalog, gone]);
    navigationStore.setState({ mode: "v2", loadProject });

    render(<Rail />);
    await act(async () => undefined);

    expect(
      adaptNavigationResources(navigationStore.getState()).projects.find((project) => project.key === "gone"),
    ).toMatchObject({
      loaded: false,
      sessions: [],
    });
    expect(loadProject).not.toHaveBeenCalled();
    expect(screen.queryByText("Deleted")).toBeNull();
  });

  test("renders a loaded root's bounded overflow as a canonical page descriptor", () => {
    const loadProjectPage = vi.fn();
    const row = summary({ ref: "local:current", title: "Current", state: "active" });
    installState([catalogResource([{ key: "p", name: "Proj", session_count: 3 }]), projectResource("p", [row], 2)]);
    navigationStore.setState({ loadProjectPage });
    render(<Rail />);
    fireEvent.click(screen.getByText("Proj"));
    fireEvent.click(screen.getByText("+2 older"));
    expect(loadProjectPage).toHaveBeenCalledTimes(1);
    expect(loadProjectPage).toHaveBeenCalledWith("p", "current", 1, 2);
  });
  test("loads one global overflow page and deduplicates repeated activation", async () => {
    const loadSection = vi.fn().mockResolvedValue(undefined);
    installState([sectionResource("live", [summary({ title: "Live" })], 3)]);
    navigationStore.setState({ loadSection });
    render(<Rail />);
    const older = screen.getByText("+3 older");
    fireEvent.click(older);
    fireEvent.click(older);
    await act(async () => undefined);
    expect(loadSection).toHaveBeenCalledTimes(1);
    expect(loadSection).toHaveBeenCalledWith("live", 1, 50);
  });
  test("advances every bounded view by returned top-level rows", () => {
    const globalRows = [summary({ ref: "global:a" }), summary({ ref: "global:b" })];
    const pinRows = [summary({ ref: "pin:a" }), summary({ ref: "pin:b" })];
    const rootRows = [summary({ ref: "root:a" }), summary({ ref: "root:b" })];
    const pageRows = [summary({ ref: "page:a" }), summary({ ref: "page:b" })];
    installState([
      resource(
        { kind: "section", section: "live", offset: 2, limit: 50 },
        { generation_id: "g1", revision: 1, sessions: globalRows, remaining: 3, truncated: true },
      ),
      resource(
        { kind: "pin_catalog", offset: 0, limit: 100 },
        {
          generation_id: "g1",
          revision: 1,
          pin_sections: [{ id: "pins", name: "Pins", count: 5 }],
          remaining: 0,
        },
      ),
      resource(
        { kind: "pin_section", sectionId: "pins", offset: 3, limit: 50 },
        { generation_id: "g1", revision: 1, sessions: pinRows, remaining: 2, truncated: true },
      ),
      resource(
        { kind: "catalog", catalog: "projects", offset: 4, limit: 100 },
        {
          generation_id: "g1",
          revision: 1,
          projects: [
            { key: "p", name: "Project", session_count: 6 },
            { key: "q", name: "Root only", session_count: 6 },
          ],
          remaining: 2,
        },
      ),
      projectResource("p", rootRows, 4),
      projectResource("q", rootRows, 4),
      resource(
        { kind: "project_page", projectKey: "p", tier: "current", offset: 2, limit: 50 },
        {
          generation_id: "g1",
          revision: 1,
          key: "p",
          tier: "current",
          offset: 2,
          sessions: pageRows,
          remaining: 2,
          truncated: true,
        },
      ),
    ]);

    const adapted = adaptNavigationResources(navigationStore.getState());
    expect(adapted.liveOverflow?.offset).toBe(4);
    expect(adapted.pinSections[0]?.offset).toBe(5);
    expect(adapted.catalogOverflow?.projects?.offset).toBe(6);
    expect(adapted.projects.find((project) => project.key === "p")?.nextOffsets).toMatchObject({ current: 4 });
    expect(adapted.projects.find((project) => project.key === "q")?.nextOffsets).toMatchObject({ current: 2 });
  });
  test("project model cache includes ordered project-page dependencies", () => {
    const catalogKey = { kind: "catalog", catalog: "projects", offset: 0, limit: 100 } as const;
    const projectKey = { kind: "project", projectKey: "p" } as const;
    const siblingProjectKey = { kind: "project", projectKey: "q" } as const;
    const projectSummary = { key: "p", name: "Project", session_count: 2 };
    const siblingSummary = { key: "q", name: "Sibling", session_count: 1 };
    const projectSummaryKey = scopedEntityKey(catalogKey, "1");
    const siblingSummaryKey = scopedEntityKey(catalogKey, "2");
    const catalog = normalizedResource(
      catalogKey,
      { projects: [projectSummary, siblingSummary] },
      {
        metadata: {},
        entities: [
          { key: projectSummaryKey, kind: "project", value: projectSummary },
          { key: siblingSummaryKey, kind: "project", value: siblingSummary },
        ],
        containers: [
          {
            key: navigationRootContainerKey(catalogKey, "projects"),
            owner: { kind: "resource_root", slot: "projects" },
            children: [projectSummaryKey, siblingSummaryKey],
          },
        ],
      },
    );
    const projectEntityKey = scopedEntityKey(projectKey, "3");
    const rootSessionKey = scopedEntityKey(projectKey, "4");
    const root = normalizedResource(
      projectKey,
      { current_remaining: 1, recent_remaining: 0, archived_remaining: 0 },
      {
        metadata: { current_remaining: 1, recent_remaining: 0, archived_remaining: 0 },
        entities: [
          { key: projectEntityKey, kind: "project", value: { key: "p" } },
          sessionEntity(rootSessionKey, summary({ ref: "root", session_id: "root", title: "Root" })),
        ],
        containers: [
          {
            key: navigationOwnedContainerKey(projectEntityKey, "current"),
            owner: { kind: "entity", entityKey: projectEntityKey, slot: "current" },
            children: [rootSessionKey],
          },
          {
            key: navigationOwnedContainerKey(projectEntityKey, "recent"),
            owner: { kind: "entity", entityKey: projectEntityKey, slot: "recent" },
            children: [],
          },
          {
            key: navigationOwnedContainerKey(projectEntityKey, "archived"),
            owner: { kind: "entity", entityKey: projectEntityKey, slot: "archived" },
            children: [],
          },
          childContainer(rootSessionKey),
        ],
      },
    );
    const siblingEntityKey = scopedEntityKey(siblingProjectKey, "5");
    const siblingSessionKey = scopedEntityKey(siblingProjectKey, "6");
    const siblingRoot = normalizedResource(
      siblingProjectKey,
      { current_remaining: 0, recent_remaining: 0, archived_remaining: 0 },
      {
        metadata: { current_remaining: 0, recent_remaining: 0, archived_remaining: 0 },
        entities: [
          { key: siblingEntityKey, kind: "project", value: { key: "q" } },
          sessionEntity(siblingSessionKey, summary({ ref: "sibling", session_id: "sibling", title: "Sibling row" })),
        ],
        containers: [
          ...(["current", "recent", "archived"] as const).map((tier) => ({
            key: navigationOwnedContainerKey(siblingEntityKey, tier),
            owner: { kind: "entity" as const, entityKey: siblingEntityKey, slot: tier },
            children: tier === "current" ? [siblingSessionKey] : [],
          })),
          childContainer(siblingSessionKey),
        ],
      },
    );
    const page = (offset: number, digit: string, title: string) => {
      const key = { kind: "project_page", projectKey: "p", tier: "current", offset, limit: 50 } as const;
      const entityKey = scopedEntityKey(key, digit);
      const snapshot: NavigationSnapshot = {
        metadata: { remaining: 0 },
        entities: [sessionEntity(entityKey, summary({ ref: `page-${offset}`, session_id: `page-${offset}`, title }))],
        containers: [
          {
            key: navigationRootContainerKey(key, "sessions"),
            owner: { kind: "resource_root", slot: "sessions" },
            children: [entityKey],
          },
          childContainer(entityKey),
        ],
      };
      return normalizedResource(key, { remaining: 0 }, snapshot);
    };
    const pageOne = page(1, "7", "Page one");
    const baseResources = new Map([
      [keyID(catalog.key), catalog as ResourceState],
      [keyID(root.key), root as ResourceState],
      [keyID(siblingRoot.key), siblingRoot as ResourceState],
    ]);
    const state = { ...navigationStore.getState(), resources: baseResources };
    const initial = adaptNavigationResources(state);
    const repeated = adaptNavigationResources(state);
    expect(repeated.projects[0]).toBe(initial.projects[0]);
    expect(repeated.projects[1]).toBe(initial.projects[1]);

    const withPageState = {
      ...state,
      resources: new Map([...baseResources, [keyID(pageOne.key), pageOne as ResourceState]]),
    };
    const withPage = adaptNavigationResources(withPageState);
    expect(withPage.projects[0]).not.toBe(initial.projects[0]);
    expect(withPage.projects[0]?.sessions.map((row) => row.title)).toEqual(["Root", "Page one"]);

    const stableLookup = (_id: string, defaultExpanded: boolean) => defaultExpanded;
    const beforeNodes = projectNodes(withPage.projects, stableLookup);
    const unrelated = sectionResource("needs_you", [summary({ ref: "unrelated", title: "Unrelated" })]);
    const loadingAndErrorOnly = adaptNavigationResources({
      ...withPageState,
      resources: new Map([
        [keyID(catalog.key), catalog as ResourceState],
        [keyID(root.key), { ...root, loading: true, error: new Error("transient") } as ResourceState],
        [keyID(siblingRoot.key), siblingRoot as ResourceState],
        [keyID(pageOne.key), { ...pageOne, loading: true, error: new Error("transient") } as ResourceState],
        [keyID(unrelated.key), unrelated as ResourceState],
      ]),
    });
    const afterNodes = projectNodes(loadingAndErrorOnly.projects, stableLookup);
    expect(loadingAndErrorOnly.projects[0]).toBe(withPage.projects[0]);
    expect(loadingAndErrorOnly.projects[1]).toBe(withPage.projects[1]);
    expect(afterNodes[0]).toBe(beforeNodes[0]);
    expect(afterNodes[0]?.children).toBe(beforeNodes[0]?.children);
    expect(afterNodes[0]?.children[0]).toBe(beforeNodes[0]?.children[0]);
    expect(afterNodes[1]).toBe(beforeNodes[1]);
    expect(afterNodes[1]?.children).toBe(beforeNodes[1]?.children);

    const graphIdentityChanged = page(1, "7", "Page one");
    const changedGraph = adaptNavigationResources({
      ...state,
      resources: new Map([...baseResources, [keyID(graphIdentityChanged.key), graphIdentityChanged as ResourceState]]),
    });
    expect(changedGraph.projects[0]).not.toBe(withPage.projects[0]);

    const pageTwo = page(2, "8", "Page two");
    const twoPages = adaptNavigationResources({
      ...state,
      resources: new Map([
        ...baseResources,
        [keyID(pageTwo.key), pageTwo as ResourceState],
        [keyID(pageOne.key), pageOne as ResourceState],
      ]),
    });
    expect(twoPages.projects[0]).not.toBe(withPage.projects[0]);
    expect(twoPages.projects[0]?.sessions.map((row) => row.title)).toEqual(["Root", "Page one", "Page two"]);
    const removedPage = adaptNavigationResources({
      ...state,
      resources: new Map([...baseResources, [keyID(pageTwo.key), pageTwo as ResourceState]]),
    });
    expect(removedPage.projects[0]).not.toBe(twoPages.projects[0]);
  });
  test("toasts a global overflow failure and permits a deterministic retry", async () => {
    const loadSection = vi.fn().mockRejectedValueOnce(new Error("offline")).mockResolvedValueOnce(undefined);
    installState([sectionResource("live", [summary({ title: "Live" })], 3)]);
    navigationStore.setState({ loadSection });
    render(<Rail />);
    const older = screen.getByText("+3 older");
    fireEvent.click(older);
    await act(async () => undefined);
    expect(getToasts().some((toast) => /Couldn't load older sessions/i.test(toast.text))).toBe(true);
    fireEvent.click(older);
    await act(async () => undefined);
    expect(loadSection).toHaveBeenCalledTimes(2);
  });
  test("toasts a project overflow failure and retries the same canonical page", async () => {
    const loadProjectPage = vi.fn().mockRejectedValueOnce(new Error("offline")).mockResolvedValueOnce(undefined);
    installState([
      catalogResource([{ key: "p", name: "Project", session_count: 3 }]),
      projectResource("p", [summary({ title: "Current" })], 2),
    ]);
    navigationStore.setState({ loadProjectPage });
    render(<Rail />);
    fireEvent.click(screen.getByText("Project"));
    const older = screen.getByText("+2 older");
    fireEvent.click(older);
    await act(async () => undefined);
    expect(getToasts().some((toast) => /Couldn't load older sessions/i.test(toast.text))).toBe(true);
    fireEvent.click(older);
    await act(async () => undefined);
    expect(loadProjectPage).toHaveBeenCalledTimes(2);
  });
  test("deduplicates overlapping pin pages and keeps the first descriptor count", () => {
    const duplicate = summary({ ref: "pin", title: "first" });
    const later = summary({ ref: "pin", title: "later" });
    installState([
      resource(
        { kind: "pin_catalog", offset: 0, limit: 100 },
        {
          generation_id: "g1",
          revision: 1,
          pin_sections: [
            { id: "p", name: "Pins", count: 4 },
            { id: "p", name: "Later", count: 9 },
          ],
          remaining: 0,
        },
      ),
      resource(
        { kind: "pin_section", sectionId: "p", offset: 0, limit: 1 },
        { generation_id: "g1", revision: 1, sessions: [duplicate], remaining: 1, truncated: true },
      ),
      resource(
        { kind: "pin_section", sectionId: "p", offset: 1, limit: 1 },
        { generation_id: "g1", revision: 1, sessions: [later], remaining: 0, truncated: false },
      ),
    ]);
    const pins = adaptNavigationResources(navigationStore.getState()).pinSections;
    expect(pins).toHaveLength(1);
    expect(pins[0]).toMatchObject({ id: "p", name: "Pins", member_count: 4 });
    expect(pins[0]?.sessions.map((row) => row.title)).toEqual(["first"]);
  });
  test("keeps a dormant durable section out of the visible rail", () => {
    installState([
      resource(
        { kind: "pin_catalog", offset: 0, limit: 100 },
        {
          generation_id: "g1",
          revision: 1,
          pin_sections: [{ id: "dormant", name: "Dormant", count: 2 }],
          remaining: 0,
        },
      ),
      resource(
        { kind: "pin_section", sectionId: "dormant", offset: 0, limit: 50 },
        { generation_id: "g1", revision: 1, sessions: [], remaining: 0, truncated: false },
      ),
    ]);
    expect(adaptNavigationResources(navigationStore.getState()).pinSections).toEqual([]);
  });
  test("tracks an empty section before converging its first pin assignment", async () => {
    const row = summary({ title: "First pin" });
    const order: string[] = [];
    const trackPinSection = vi.fn((sectionID: string) => order.push(`track:${sectionID}`));
    const applyNavigationMutation = vi.fn(async () => {
      order.push("apply");
      navigationStore.setState((state) => {
        const resources = new Map(state.resources);
        const loaded = resource(
          { kind: "pin_section", sectionId: "empty", offset: 0, limit: 50 },
          { generation_id: "g1", revision: 2, sessions: [row], remaining: 0, truncated: false },
        );
        resources.set(keyID(loaded.key), loaded);
        return { resources };
      });
    });
    installState([
      sectionResource("live", [row]),
      resource(
        { kind: "pin_catalog", offset: 0, limit: 100 },
        { generation_id: "g1", revision: 1, pin_sections: [{ id: "empty", name: "Empty", count: 0 }], remaining: 0 },
      ),
    ]);
    navigationStore.setState({
      trackPinSection,
      applyNavigationMutation,
      loadPinCatalogPages: vi.fn().mockResolvedValue(undefined),
    });
    const client = new FakeClient();
    client.on("evener/session-pin/assign", () => ({
      ok: true,
      changed: true,
      assignment: { sessionRef: row.ref, section: { id: "empty", name: "Empty", memberCount: 1 } },
      navigation: {
        generation_id: "g1",
        targets: [{ kind: "pin_section", sectionId: "empty", revision: 2 }],
      },
    }));

    render(<Rail />, client);
    fireEvent.click(screen.getByRole("button", { name: /actions for first pin/i }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Pin this session…" }));
    fireEvent.click(await screen.findByRole("button", { name: "Empty" }));
    await act(async () => undefined);

    expect(order).toEqual(["track:empty", "apply"]);
    const pinnedSection = screen.getByRole("heading", { name: "Empty" }).closest("section");
    if (!pinnedSection) throw new Error("pinned section missing");
    expect(within(pinnedSection).getByText("First pin")).toBeTruthy();
  });
  test("a settled gone v2 location consumes reveal without another lookup", async () => {
    const key = { kind: "location", ref: "local:gone-reveal" } as const;
    const present = normalizedResource(key, null, {
      metadata: {},
      entities: [],
      containers: [
        {
          key: navigationRootContainerKey(key, "session"),
          owner: { kind: "resource_root", slot: "session" },
          children: [],
        },
      ],
    });
    const gone = { ...present, data: null, normalized: { ...present.normalized!, presence: "gone" as const } };
    const lookupLocation = vi.fn().mockResolvedValue(gone);
    const consumed = vi.fn();
    installState([gone as ResourceState]);
    navigationStore.setState({ mode: "v2", lookupLocation });

    const view = render(<Rail revealTarget="local:gone-reveal" onRevealConsumed={consumed} />);
    await act(async () => undefined);
    view.rerender(<Rail revealTarget="local:gone-reveal" onRevealConsumed={consumed} />);
    await act(async () => undefined);

    expect(lookupLocation).not.toHaveBeenCalled();
    expect(consumed).toHaveBeenCalledTimes(1);
  });

  test("uses location lookup to reveal an unloaded project rather than scanning a tree", async () => {
    const lookupLocation = vi.fn().mockResolvedValue(undefined);
    installState([
      sectionResource("live", [summary({ ref: "live", title: "Live" })]),
      catalogResource([{ key: "p", name: "Proj", session_count: 1 }]),
    ]);
    navigationStore.setState({ lookupLocation });
    const consumed = vi.fn();
    render(<Rail revealTarget="deep-ref" onRevealConsumed={consumed} />);
    await act(async () => undefined);
    expect(lookupLocation).toHaveBeenCalledWith("deep-ref");
    expect(consumed).not.toHaveBeenCalled();
  });
  test("retries a failed deferred location lookup for the same reveal target", async () => {
    const firstLookup = deferred<unknown>();
    const lookupLocation = vi.fn().mockReturnValueOnce(firstLookup.promise).mockResolvedValueOnce(undefined);
    installState([sectionResource("live", [summary({ ref: "live", title: "Live" })])]);
    navigationStore.setState({ lookupLocation });
    render(<Rail revealTarget="retry-ref" />);
    await act(async () => undefined);
    expect(lookupLocation).toHaveBeenCalledTimes(1);

    await act(async () => {
      firstLookup.reject(new Error("offline"));
      await firstLookup.promise.catch(() => undefined);
    });
    act(() => {
      navigationStore.setState({ resources: new Map(navigationStore.getState().resources) });
    });
    await act(async () => undefined);

    expect(lookupLocation).toHaveBeenCalledTimes(2);
    expect(lookupLocation).toHaveBeenLastCalledWith("retry-ref");
  });
  test("an old target's rejected resource request cannot clear the newer target's same-key guard", async () => {
    const oldRequest = deferred<unknown>();
    const currentRequest = deferred<unknown>();
    const loadSection = vi
      .fn()
      .mockReturnValueOnce(oldRequest.promise)
      .mockReturnValueOnce(currentRequest.promise)
      .mockResolvedValue(undefined);
    const oldLocation = resource(
      { kind: "location", ref: "old-ref" },
      {
        generation_id: "g1",
        revision: 1,
        ref: "old-ref",
        top_level_ref: "old-ref",
        top_level: true,
        tier: "live",
        session: summary({ ref: "old-ref" }),
      },
    );
    const currentLocation = resource(
      { kind: "location", ref: "current-ref" },
      {
        generation_id: "g1",
        revision: 1,
        ref: "current-ref",
        top_level_ref: "current-ref",
        top_level: true,
        tier: "live",
        session: summary({ ref: "current-ref" }),
      },
    );
    installState([oldLocation, currentLocation]);
    navigationStore.setState({ loadSection });
    const view = render(<Rail revealTarget="old-ref" />);
    await act(async () => undefined);
    expect(loadSection).toHaveBeenCalledTimes(1);

    view.rerender(<Rail revealTarget="current-ref" />);
    await act(async () => undefined);
    expect(loadSection).toHaveBeenCalledTimes(2);

    await act(async () => {
      oldRequest.reject(new Error("old request failed"));
      await oldRequest.promise.catch(() => undefined);
    });
    act(() => {
      navigationStore.setState({ resources: new Map(navigationStore.getState().resources) });
    });
    await act(async () => undefined);
    expect(loadSection).toHaveBeenCalledTimes(2);

    await act(async () => {
      currentRequest.reject(new Error("current request failed"));
      await currentRequest.promise.catch(() => undefined);
    });
    act(() => {
      navigationStore.setState({ resources: new Map(navigationStore.getState().resources) });
    });
    await act(async () => undefined);
    expect(loadSection).toHaveBeenCalledTimes(3);
  });
  test("routes empty-model location reveals to pin catalog/section and needs-you resources", async () => {
    const loadPinCatalog = vi.fn().mockResolvedValue(undefined);
    const loadPinSection = vi.fn().mockResolvedValue(undefined);
    const loadSection = vi.fn().mockResolvedValue(undefined);
    const pinLocation = resource(
      { kind: "location", ref: "pin-ref" },
      {
        generation_id: "g1",
        revision: 1,
        ref: "pin-ref",
        top_level_ref: "pin-ref",
        top_level: true,
        pin_section_id: "pins",
        session: summary({ ref: "pin-ref" }),
      },
    );
    installState([pinLocation]);
    navigationStore.setState({ loadPinCatalog, loadPinSection, loadSection });
    render(<Rail revealTarget="pin-ref" />);
    await act(async () => undefined);
    expect(loadPinCatalog).toHaveBeenCalledTimes(1);
    expect(loadPinSection).toHaveBeenCalledWith("pins");
    expect(loadSection).not.toHaveBeenCalled();
  });
  test("routes a global location to needs-you instead of always loading Live", async () => {
    const loadSection = vi.fn().mockResolvedValue(undefined);
    const location = resource(
      { kind: "location", ref: "needs-ref" },
      {
        generation_id: "g1",
        revision: 1,
        ref: "needs-ref",
        top_level_ref: "needs-ref",
        top_level: true,
        tier: "needs_you",
        session: summary({ ref: "needs-ref" }),
      },
    );
    installState([location]);
    navigationStore.setState({ loadSection });
    render(<Rail revealTarget="needs-ref" />);
    await act(async () => undefined);
    expect(loadSection).toHaveBeenCalledWith("needs_you");
  });
  test("scrolls and consumes a rendered reveal target exactly once across resource updates", async () => {
    const originalScroll = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollIntoView");
    if (!originalScroll) {
      Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
        configurable: true,
        value: () => undefined,
        writable: true,
      });
    }
    const scroll = vi.spyOn(HTMLElement.prototype, "scrollIntoView").mockImplementation(() => undefined);
    const location = resource(
      { kind: "location", ref: "target" },
      {
        generation_id: "g1",
        revision: 1,
        ref: "target",
        top_level_ref: "target",
        top_level: true,
        tier: "live",
        session: summary({ ref: "target", title: "Target" }),
      },
    );
    installState([location]);
    const consumed = vi.fn();
    try {
      const view = render(<Rail revealTarget="target" onRevealConsumed={consumed} />);
      await act(async () => undefined);
      expect(consumed).toHaveBeenCalledTimes(0);
      const live = sectionResource("live", [summary({ ref: "target", title: "Target" })]);
      const nextResources = new Map<string, ResourceState>([
        [keyID(location.key), location],
        [keyID(live.key), live],
      ]);
      navigationStore.setState({ resources: nextResources });
      await act(async () => undefined);
      navigationStore.setState({ attention: { changed: [], summary: { needsYou: 0, error: 0, working: 0 } } });
      await act(async () => undefined);
      expect(scroll).toHaveBeenCalledTimes(1);
      expect(consumed).toHaveBeenCalledTimes(1);
      view.rerender(<Rail revealTarget="target" onRevealConsumed={consumed} />);
      await act(async () => undefined);
      expect(scroll).toHaveBeenCalledTimes(1);
      expect(consumed).toHaveBeenCalledTimes(1);
    } finally {
      scroll.mockRestore();
      if (originalScroll) Object.defineProperty(HTMLElement.prototype, "scrollIntoView", originalScroll);
      else delete (HTMLElement.prototype as Partial<HTMLElement>).scrollIntoView;
    }
  });
  test("authoritative missing reveal consumes once and a changed target re-arms", async () => {
    const consumed = vi.fn();
    const first = resource(
      { kind: "location", ref: "missing" },
      { generation_id: "g1", revision: 1, ref: "missing", top_level_ref: "missing", top_level: true },
    );
    const second = resource(
      { kind: "location", ref: "missing-2" },
      { generation_id: "g1", revision: 1, ref: "missing-2", top_level_ref: "missing-2", top_level: true },
    );
    installState([first, second]);
    const view = render(<Rail revealTarget="missing" onRevealConsumed={consumed} />);
    await act(async () => undefined);
    view.rerender(<Rail revealTarget="missing" onRevealConsumed={consumed} />);
    await act(async () => undefined);
    expect(consumed).toHaveBeenCalledTimes(1);
    view.rerender(<Rail revealTarget="missing-2" onRevealConsumed={consumed} />);
    await act(async () => undefined);
    expect(consumed).toHaveBeenCalledTimes(2);
  });
  test("loads a project catalog and root from an empty-model project location", async () => {
    const loadCatalog = vi.fn().mockResolvedValue(undefined);
    const loadProject = vi.fn().mockResolvedValue(undefined);
    const location = resource(
      { kind: "location", ref: "project-ref" },
      {
        generation_id: "g1",
        revision: 1,
        ref: "project-ref",
        top_level_ref: "project-ref",
        top_level: true,
        project_key: "p",
        tier: "current",
        session: summary({ ref: "project-ref" }),
      },
    );
    installState([location]);
    navigationStore.setState({ loadCatalog, loadProject });
    render(<Rail revealTarget="project-ref" />);
    await act(async () => undefined);
    expect(loadCatalog).toHaveBeenCalledWith("projects");
    expect(loadProject).toHaveBeenCalledWith("p");
  });
  test("preserves last-good rows when a project resource is stale with an error", () => {
    const loaded = {
      ...projectResource("p", [summary({ title: "Last good" })]),
      stale: true,
      error: new Error("offline"),
    };
    installState([catalogResource([{ key: "p", name: "Proj", session_count: 1 }]), loaded]);
    render(<Rail />);
    fireEvent.click(screen.getByText("Proj"));
    expect(screen.getByText("Last good")).toBeTruthy();
  });
  test("a graph-native project refresh error rerenders only its row and retries exactly that project", async () => {
    const catalogKey = { kind: "catalog", catalog: "projects", offset: 0, limit: 100 } as const;
    const affectedSummary = { key: "p", name: "Affected", session_count: 1, default_expanded: true };
    const siblingSummary = { key: "q", name: "Sibling", session_count: 1, default_expanded: true };
    const affectedSummaryKey = scopedEntityKey(catalogKey, "1");
    const siblingSummaryKey = scopedEntityKey(catalogKey, "2");
    const catalog = normalizedResource(
      catalogKey,
      { projects: [affectedSummary, siblingSummary] },
      {
        metadata: {},
        entities: [
          { key: affectedSummaryKey, kind: "project", value: affectedSummary },
          { key: siblingSummaryKey, kind: "project", value: siblingSummary },
        ],
        containers: [
          {
            key: navigationRootContainerKey(catalogKey, "projects"),
            owner: { kind: "resource_root", slot: "projects" },
            children: [affectedSummaryKey, siblingSummaryKey],
          },
        ],
      },
    );
    const affectedRoot = graphProjectResource(
      "p",
      "3",
      "4",
      summary({ ref: "local:affected", session_id: "affected", title: "Last good affected child" }),
    );
    const siblingRoot = graphProjectResource(
      "q",
      "5",
      "6",
      summary({ ref: "local:sibling", session_id: "sibling", title: "Unrelated child" }),
    );
    installState([catalog, affectedRoot, siblingRoot]);
    const loadProject = vi.fn().mockResolvedValue(undefined);
    navigationStore.setState({ loadProject });

    const beforeState = navigationStore.getState();
    const beforeGraph = affectedRoot.normalized?.graph;
    const beforeProject = adaptNavigationResources(beforeState).projects[0];
    const stableLookup = (_id: string, defaultExpanded: boolean) => defaultExpanded;
    const beforeNode = projectNodes(adaptNavigationResources(beforeState).projects, stableLookup)[0];
    const observer = vi.fn();
    render(
      <RailRenderObserver value={observer}>
        <Rail />
      </RailRenderObserver>,
    );
    expect(screen.getByText("Last good affected child")).toBeTruthy();
    expect(screen.getByText("Unrelated child")).toBeTruthy();
    observer.mockClear();

    act(() => {
      const resources = new Map(navigationStore.getState().resources);
      resources.set(keyID(affectedRoot.key), {
        ...affectedRoot,
        stale: true,
        error: new Error("refresh failed"),
      });
      navigationStore.setState({ resources });
    });

    const afterState = navigationStore.getState();
    const failedRoot = afterState.resources.get(keyID(affectedRoot.key));
    const afterProjects = adaptNavigationResources(afterState).projects;
    const afterProject = afterProjects[0];
    const afterNode = projectNodes(afterProjects, stableLookup)[0];
    expect(failedRoot?.normalized?.graph).toBe(beforeGraph);
    expect(afterProject).toBe(beforeProject);
    expect(afterNode).toBe(beforeNode);
    expect(afterNode?.children[0]).toBe(beforeNode?.children[0]);
    expect(screen.getByText("Last good affected child")).toBeTruthy();
    expect(screen.getByText("Unrelated child")).toBeTruthy();
    expect(observer).toHaveBeenCalledTimes(1);
    expect(observer).toHaveBeenCalledWith("projectnode:p");
    expect(observer).not.toHaveBeenCalledWith("navigation:project:p:current:local:affected");
    expect(observer).not.toHaveBeenCalledWith("projectnode:q");
    expect(observer).not.toHaveBeenCalledWith("navigation:project:q:current:local:sibling");

    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await act(async () => undefined);
    expect(loadProject).toHaveBeenCalledTimes(1);
    expect(loadProject).toHaveBeenCalledWith("p");
  });
  test("keeps expansion persistence in the rail-local override map", () => {
    installState([catalogResource([{ key: "p", name: "Proj", session_count: 1 }])]);
    render(<Rail />);
    fireEvent.click(screen.getByText("Proj"));
    expect(localStorage.getItem(EXPANSION_STORAGE_KEY)).toContain("projectnode:p");
  });
  test("retains an archive overlay through AppWire response and removes it at target convergence", async () => {
    let resolveConvergence!: () => void;
    const convergence = new Promise<void>((resolve) => {
      resolveConvergence = resolve;
    });
    const applyNavigationMutation = vi.fn(() => convergence);
    installState([sectionResource("live", [summary({ title: "Archivable", rename: true })])]);
    navigationStore.setState({ applyNavigationMutation });
    const client = new FakeClient();
    client.on("evener/archive/set", (params) => {
      expect(params).toEqual({ kind: "session", id: "a", archived: true });
      return {
        ok: true,
        navigation: { generation_id: "g1", targets: [{ kind: "section", section: "live", revision: 2 }] },
      };
    });
    connectionStore.getState().connect(client);
    render(<Rail />);
    fireEvent.click(screen.getByRole("button", { name: /actions for archivable/i }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Archive" }));
    await act(async () => undefined);
    expect(screen.queryByText("Archivable")).toBeNull();
    expect(applyNavigationMutation).toHaveBeenCalledTimes(1);
    resolveConvergence();
    await act(async () => undefined);
    expect(screen.getByText("Archivable")).toBeTruthy();
  });
  test("rolls back a rejected AppWire archive and leaves the row visible with an error toast", async () => {
    installState([sectionResource("live", [summary({ title: "Rejectable" })])]);
    const client = new FakeClient();
    client.on("evener/archive/set", () => {
      throw new Error("denied");
    });
    connectionStore.getState().connect(client);
    render(<Rail />);
    fireEvent.click(screen.getByRole("button", { name: /actions for rejectable/i }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Archive" }));
    await act(async () => undefined);
    expect(screen.getByText("Rejectable")).toBeTruthy();
    expect(getToasts().some((toast) => /Couldn't update archive state/i.test(toast.text))).toBe(true);
  });
  test("operates the rendered resource-backed tree with keyboard focus, activation, and toggle", () => {
    window.history.replaceState({}, "", "/");
    const child = summary({ ref: "local:child", session_id: "child", title: "Keyboard child" });
    const cluster = summary({
      ref: "local:cluster",
      session_id: "cluster",
      title: "Keyboard cluster",
      kind: "cluster",
      children: [child],
    });
    installState([sectionResource("live", [cluster])]);
    render(<Rail />);

    const clusterRow = screen.getByRole("treeitem", { name: /keyboard cluster/i });
    act(() => clusterRow.focus());
    expect(document.activeElement).toBe(clusterRow);
    expect(clusterRow.getAttribute("aria-expanded")).toBe("false");

    fireEvent.keyDown(clusterRow, { key: "ArrowRight" });
    expect(clusterRow.getAttribute("aria-expanded")).toBe("true");
    const childRow = screen.getByRole("treeitem", { name: /keyboard child/i });
    fireEvent.keyDown(clusterRow, { key: "ArrowRight" });
    expect(document.activeElement).toBe(childRow);

    fireEvent.keyDown(childRow, { key: "Enter" });
    expect(window.location.pathname).toBe("/s/local%3Achild");
    fireEvent.keyDown(childRow, { key: "ArrowLeft" });
    expect(document.activeElement).toBe(clusterRow);
    fireEvent.keyDown(clusterRow, { key: "ArrowLeft" });
    expect(clusterRow.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByRole("treeitem", { name: /keyboard child/i })).toBeNull();
  });
  test("routes rename through the rendered session menu and dialog", async () => {
    installState([sectionResource("live", [summary({ title: "Rename me", rename: true })])]);
    const client = new FakeClient();
    client.on("evener/thread/name/set", () => ({}));
    connectionStore.getState().connect(client);
    render(<Rail />, client);
    fireEvent.click(screen.getByRole("button", { name: /actions for rename me/i }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Rename" }));
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Renamed" } });
    fireEvent.click(screen.getByRole("button", { name: "Rename" }));
    await act(async () => undefined);
    expect(client.calls).toContainEqual({
      method: "evener/thread/name/set",
      params: { ref: "local:a", name: "Renamed" },
    });
  });
  test("routes project favorite through the rendered project menu", async () => {
    const applyNavigationMutation = vi.fn().mockResolvedValue(undefined);
    installState([catalogResource([{ key: "p", name: "Project", session_count: 0 }])]);
    navigationStore.setState({ applyNavigationMutation });
    const client = new FakeClient();
    client.on("evener/favorite/set", (params) => {
      expect(params).toEqual({ kind: "project", id: "p", favorited: true });
      return { ok: true, navigation: { generation_id: "g1", targets: [] } };
    });
    render(<Rail />, client);
    fireEvent.click(screen.getByRole("button", { name: /actions for project/i }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Add to pinned" }));
    await act(async () => undefined);
    expect(client.calls).toEqual([
      { method: "evener/favorite/set", params: { kind: "project", id: "p", favorited: true } },
    ]);
    expect(applyNavigationMutation).toHaveBeenCalledTimes(1);
  });
  test("routes unpin and delete through rendered session dialogs and receipt convergence", async () => {
    const applyNavigationMutation = vi.fn().mockResolvedValue(undefined);
    const row = summary({ title: "Pinned delete" });
    installState([
      resource(
        { kind: "pin_catalog", offset: 0, limit: 100 },
        { generation_id: "g1", revision: 1, pin_sections: [{ id: "pins", name: "Pins", count: 1 }], remaining: 0 },
      ),
      resource(
        { kind: "pin_section", sectionId: "pins", offset: 0, limit: 50 },
        { generation_id: "g1", revision: 1, sessions: [row], remaining: 0, truncated: false },
      ),
    ]);
    navigationStore.setState({ applyNavigationMutation });
    const client = new FakeClient();
    client.on("evener/session-pin/unpin", (params) => {
      expect(params).toEqual({ sessionRef: row.ref });
      return {
        ok: true,
        changed: true,
        assignment: { sessionRef: row.ref },
        navigation: { generation_id: "g1", targets: [] },
      };
    });
    client.on("evener/session/delete", () => ({
      deleted: ["a"],
      skipped: [],
      navigation: { generation_id: "g1", targets: [] },
    }));
    render(<Rail />, client);
    fireEvent.click(screen.getByRole("button", { name: /actions for pinned delete/i }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Unpin" }));
    await act(async () => undefined);
    expect(client.calls).toContainEqual({ method: "evener/session-pin/unpin", params: { sessionRef: row.ref } });
    fireEvent.click(screen.getByRole("button", { name: /actions for pinned delete/i }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Delete…" }));
    fireEvent.click(screen.getByRole("button", { name: "Delete" }));
    await act(async () => undefined);
    expect(client.calls).toContainEqual({ method: "evener/session/delete", params: { ref: "local:a" } });
    expect(applyNavigationMutation).toHaveBeenCalledTimes(2);
  });
  test("keeps AppWire shutdown pending through unrelated invalidation and until relevant target authority", async () => {
    const event = deferred<NavigationInvalidatedPayload>();
    const targetAuthority = deferred<void>();
    let invalidationPredicate: ((payload: NavigationInvalidatedPayload) => boolean) | undefined;
    const awaitNavigationInvalidation = vi.fn((predicate?: (payload: NavigationInvalidatedPayload) => boolean) => {
      invalidationPredicate = predicate;
      return { promise: event.promise, cancel: vi.fn() };
    });
    const deliverInvalidation = (payload: NavigationInvalidatedPayload) => {
      if (invalidationPredicate?.(payload)) event.resolve(payload);
    };
    const applyNavigationMutation = vi.fn(() => targetAuthority.promise);
    const shutdown = vi.fn().mockResolvedValue(undefined);
    installState([
      catalogResource([{ key: "p", name: "Project", session_count: 1 }]),
      projectResource("p", [summary({ title: "Shutdown me", live: true })]),
    ]);
    navigationStore.setState({ awaitNavigationInvalidation, applyNavigationMutation });
    threadsStore.setState({ shutdown });
    render(<Rail />);
    fireEvent.click(screen.getByText("Project"));
    fireEvent.click(screen.getByRole("button", { name: /actions for shutdown me/i }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Shut down" }));
    fireEvent.click(screen.getByRole("button", { name: "Shut down" }));
    await act(async () => undefined);
    expect(awaitNavigationInvalidation).toHaveBeenCalledTimes(1);
    expect(shutdown).toHaveBeenCalledWith("local:a");
    deliverInvalidation({
      generationId: "g1",
      sequence: 2,
      targets: [{ kind: "pin_section", sectionId: "other", revision: 2 }],
    });
    await act(async () => undefined);
    expect(applyNavigationMutation).not.toHaveBeenCalled();
    expect(screen.getByText("Shut down this session?")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Shut down" }).hasAttribute("disabled")).toBe(true);

    deliverInvalidation({
      generationId: "g1",
      sequence: 3,
      targets: [{ kind: "project", projectKey: "p", revision: 2 }],
    });
    await act(async () => undefined);
    expect(applyNavigationMutation).toHaveBeenCalledWith({
      generation_id: "g1",
      targets: [{ kind: "project", projectKey: "p", revision: 2 }],
    });
    expect(screen.getByText("Shut down this session?")).toBeTruthy();

    await act(async () => {
      targetAuthority.resolve(undefined);
      await targetAuthority.promise;
    });
    expect(screen.queryByText("Shut down this session?")).toBeNull();
  });
  test("routes pin-section rename and delete dialogs through receipts and durable member count", async () => {
    const applyNavigationMutation = vi.fn().mockResolvedValue(undefined);
    installState([
      resource(
        { kind: "pin_catalog", offset: 0, limit: 100 },
        { generation_id: "g1", revision: 1, pin_sections: [{ id: "pins", name: "Pins", count: 1 }], remaining: 0 },
      ),
      resource(
        { kind: "pin_section", sectionId: "pins", offset: 0, limit: 50 },
        { generation_id: "g1", revision: 1, sessions: [summary({ title: "Pinned" })], remaining: 0, truncated: false },
      ),
    ]);
    const durableCatalog = resource(
      { kind: "pin_catalog", offset: 0, limit: 100 },
      { generation_id: "g1", revision: 2, pin_sections: [{ id: "pins", name: "Pins", count: 3 }], remaining: 0 },
    );
    const loadPinCatalogPages = vi.fn(async () => {
      navigationStore.setState((state) => {
        const resources = new Map(state.resources);
        resources.set(keyID(durableCatalog.key), durableCatalog);
        return { resources };
      });
    });
    navigationStore.setState({ applyNavigationMutation, loadPinCatalogPages });
    const client = new FakeClient();
    client.on("evener/pin-section/rename", (params) => {
      expect(params).toEqual({ sectionId: "pins", name: "Renamed" });
      return {
        ok: true,
        changed: true,
        section: { id: "pins", name: "Renamed", memberCount: 3 },
        navigation: { generation_id: "g1", targets: [] },
      };
    });
    client.on("evener/pin-section/delete", (params) => {
      expect(params).toEqual({ sectionId: "pins" });
      return {
        ok: true,
        changed: true,
        memberCount: 3,
        navigation: { generation_id: "g1", targets: [] },
      };
    });
    render(<Rail />, client);
    fireEvent.click(screen.getByRole("button", { name: /actions for pins/i }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Rename" }));
    fireEvent.change(screen.getByLabelText("Section name"), { target: { value: "Renamed" } });
    fireEvent.click(screen.getByRole("button", { name: "Rename section" }));
    await act(async () => undefined);
    fireEvent.click(screen.getByRole("button", { name: /actions for pins/i }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Delete" }));
    await act(async () => undefined);
    expect(screen.getByText(/unpin 3 sessions/i)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Delete section" }));
    await act(async () => undefined);
    expect(loadPinCatalogPages).toHaveBeenCalledWith(true);
    expect(client.calls).toEqual([
      { method: "evener/pin-section/rename", params: { sectionId: "pins", name: "Renamed" } },
      { method: "evener/pin-section/delete", params: { sectionId: "pins" } },
    ]);
    expect(applyNavigationMutation).toHaveBeenCalledTimes(2);
  });
  test("shows a project root retry while retaining the summary row after a load error", async () => {
    const loadProject = vi.fn().mockRejectedValue(new Error("offline"));
    const catalog = catalogResource([{ key: "p", name: "Retry project", session_count: 1 }]);
    const before = adaptNavigationResources({
      ...navigationStore.getState(),
      resources: new Map([[keyID(catalog.key), catalog as ResourceState]]),
    }).projects[0];
    const rootError = { ...resource({ kind: "project", projectKey: "p" }, null), error: new Error("offline") };
    installState([catalog, rootError]);
    const after = adaptNavigationResources(navigationStore.getState()).projects[0];

    expect(before?.resourceError).toBeUndefined();
    expect(after).not.toBe(before);
    expect(after?.resourceError).toBe("offline");

    navigationStore.setState({ loadProject });
    render(<Rail />);
    expect(screen.getByText("Retry project")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await act(async () => undefined);
    expect(loadProject).toHaveBeenCalledWith("p");
  });

  test("an unrelated live session update does not reinvoke an unchanged errored project row", () => {
    const observer = vi.fn();
    const catalog = catalogResource([{ key: "p", name: "Retry project", session_count: 1 }]);
    const projectError = { ...resource({ kind: "project", projectKey: "p" }, null), error: new Error("offline") };
    installState([sectionResource("live", []), catalog, projectError]);
    render(
      <RailRenderObserver value={observer}>
        <Rail />
      </RailRenderObserver>,
    );
    expect(observer).toHaveBeenCalledWith("projectnode:p");
    observer.mockClear();

    const live = sectionResource("live", [summary({ ref: "local:sibling", title: "Sibling session" })]);
    act(() => {
      const resources = new Map(navigationStore.getState().resources);
      resources.set(keyID(live.key), live);
      navigationStore.setState({ resources });
    });

    expect(observer).toHaveBeenCalledWith("navigation:live:local:sibling");
    expect(observer).not.toHaveBeenCalledWith("projectnode:p");
  });
});

// Legacy mode tests removed — legacy tree store retired per R50.
