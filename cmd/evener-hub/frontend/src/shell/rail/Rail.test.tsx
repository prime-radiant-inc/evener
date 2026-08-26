import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import type { NavigationManifest, NavigationProjectResource, NavigationSessionSummary } from "../../protocol/types.gen";
import { navigationStore, resetNavigationStoreForTests } from "../../stores/navigation/store";
import { keyID, type ResourceKey, type ResourceState } from "../../stores/navigation/types";
import { resetTreeStoreForTests, treeStore } from "../../stores/tree";
import { resetToastStoreForTests } from "../../widgets/toast/store";
import { resetWorkspaceStoreForTests } from "../workspace";
import { adaptNavigationResources, Rail } from "./Rail";
import { EXPANSION_STORAGE_KEY } from "./railExpansion";

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
function installState(resources: ResourceState[] = [], m = manifest()) {
  navigationStore.setState({
    mode: "v1",
    capability: { version: 1, generationId: "g1", sequence: 1 },
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

beforeEach(() => {
  resetNavigationStoreForTests();
  resetTreeStoreForTests();
  resetToastStoreForTests();
  resetWorkspaceStoreForTests();
  localStorage.clear();
});
afterEach(() => cleanup());

describe("resource-backed Rail", () => {
  test("renders loaded global and project resources without requesting /api/tree", () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    installState([
      sectionResource("live", [summary({ title: "Live resource" })]),
      catalogResource([{ key: "p", name: "Proj", session_count: 1 }]),
      projectResource("p", [summary({ title: "Project resource" })]),
    ]);
    render(<Rail />);
    expect(screen.getByText("Live resource")).toBeTruthy();
    expect(screen.getAllByText("Proj").length).toBeGreaterThan(0);
    expect(fetchSpy).not.toHaveBeenCalledWith("/api/tree", expect.anything());
    fetchSpy.mockRestore();
  });
  test("shows bounded loading and empty states from manifest/resource state", () => {
    installState(
      [],
      manifest({
        sections: { live: { count: 0 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
        catalogs: { projects: { count: 0 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
      }),
    );
    render(<Rail />);
    expect(screen.getByText(/no sessions yet/i)).toBeTruthy();
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
  test("keeps expansion persistence in the rail-local override map", () => {
    installState([catalogResource([{ key: "p", name: "Proj", session_count: 1 }])]);
    render(<Rail />);
    fireEvent.click(screen.getByText("Proj"));
    expect(localStorage.getItem(EXPANSION_STORAGE_KEY)).toContain("projectnode:p");
  });
});

test("explicit legacy mode renders the legacy store and does not use navigation requests", () => {
  const fetchSpy = vi.spyOn(globalThis, "fetch");
  navigationStore.setState({ mode: "legacy" });
  treeStore.setState({
    tree: {
      generated_at: "2026-01-01T00:00:00Z",
      sources: [],
      live: [
        {
          row_id: "legacy:live",
          ref: "local:live",
          host_id: "local",
          session_id: "live",
          title: "Legacy live",
          project: "P",
          state: "active",
          kind: "session",
          live: true,
          children: [],
        },
      ],
      needs_you: [],
      pin_sections: [],
      projects: [],
      archived_projects: [],
      test_runs: [],
      attentionSummary: { needsYou: 0, error: 0, working: 1 },
    },
    loading: false,
    error: null,
  });
  render(<Rail />);
  expect(screen.getByText("Legacy live")).toBeTruthy();
  expect(fetchSpy).not.toHaveBeenCalledWith("/api/navigation", expect.anything());
  fetchSpy.mockRestore();
});

test("legacy reveal and overflow stay on tree loaders", async () => {
  const loadProjectPage = vi.fn().mockResolvedValue(undefined);
  const lookupLocation = vi.fn();
  const loadSection = vi.fn();
  navigationStore.setState({ mode: "legacy", lookupLocation, loadSection });
  treeStore.setState({
    tree: {
      generated_at: "2026-01-01T00:00:00Z",
      sources: [],
      live: [],
      needs_you: [],
      pin_sections: [],
      archived_projects: [],
      test_runs: [],
      projects: [
        {
          key: "p",
          name: "P",
          sessions: [
            {
              row_id: "r",
              ref: "local:r",
              host_id: "local",
              session_id: "r",
              title: "R",
              project: "P",
              state: "idle",
              kind: "session",
              live: true,
              children: [],
            },
          ],
          more_current: 2,
        },
      ],
      attentionSummary: { needsYou: 0, error: 0, working: 0 },
    },
    loading: false,
    error: null,
    loadProjectPage,
  });
  const consumed = vi.fn();
  render(<Rail revealTarget="unknown" onRevealConsumed={consumed} />);
  await act(async () => undefined);
  expect(consumed).toHaveBeenCalledTimes(1);
  expect(lookupLocation).not.toHaveBeenCalled();
  fireEvent.click(screen.getByText("P"));
  fireEvent.click(screen.getByText("+2 older"));
  expect(loadProjectPage).toHaveBeenCalledWith("p", "current", 1, 2);
  expect(loadSection).not.toHaveBeenCalled();
});
