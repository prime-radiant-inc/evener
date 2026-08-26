import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import type {
  NavigationInvalidatedPayload,
  NavigationManifest,
  NavigationProjectResource,
  NavigationSessionSummary,
} from "../../protocol/types.gen";
import { navigationStore, resetNavigationStoreForTests } from "../../stores/navigation/store";
import { keyID, type ResourceKey, type ResourceState } from "../../stores/navigation/types";
import { threadsStore } from "../../stores/threads";
import { getToasts, resetToastStoreForTests } from "../../widgets/toast/store";
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
function jsonResponse(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: status === 200 ? "OK" : "Error",
    json: () => Promise.resolve(body),
  } as Response;
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
  resetNavigationStoreForTests();
  resetToastStoreForTests();
  resetWorkspaceStoreForTests();
  localStorage.clear();
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("resource-backed Rail", () => {
  test("renders loaded global and project resources without requesting /api/navigation", () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    installState([
      sectionResource("live", [summary({ title: "Live resource" })]),
      catalogResource([{ key: "p", name: "Proj", session_count: 1 }]),
      projectResource("p", [summary({ title: "Project resource" })]),
    ]);
    render(<Rail />);
    expect(screen.getByText("Live resource")).toBeTruthy();
    expect(screen.getAllByText("Proj").length).toBeGreaterThan(0);
    expect(fetchSpy).not.toHaveBeenCalledWith("/api/navigation", expect.anything());
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
  test("keeps expansion persistence in the rail-local override map", () => {
    installState([catalogResource([{ key: "p", name: "Proj", session_count: 1 }])]);
    render(<Rail />);
    fireEvent.click(screen.getByText("Proj"));
    expect(localStorage.getItem(EXPANSION_STORAGE_KEY)).toContain("projectnode:p");
  });
  test("retains an archive overlay through REST response and removes it at target convergence", async () => {
    let resolveConvergence!: () => void;
    const convergence = new Promise<void>((resolve) => {
      resolveConvergence = resolve;
    });
    const applyNavigationMutation = vi.fn(() => convergence);
    installState([sectionResource("live", [summary({ title: "Archivable", rename: true })])]);
    navigationStore.setState({ applyNavigationMutation });
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        jsonResponse({
          navigation: { generation_id: "g1", targets: [{ kind: "section", section: "live", revision: 2 }] },
        }),
      ),
    );
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
  test("rolls back a rejected REST archive and leaves the row visible with an error toast", async () => {
    installState([sectionResource("live", [summary({ title: "Rejectable" })])]);
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({ error: "denied" }, 403)));
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
    const applyNavigationMutation = vi.fn().mockResolvedValue(undefined);
    installState([sectionResource("live", [summary({ title: "Rename me", rename: true })])]);
    navigationStore.setState({ applyNavigationMutation });
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ navigation: { generation_id: "g1", targets: [] } }));
    vi.stubGlobal("fetch", fetchMock);
    render(<Rail />);
    fireEvent.click(screen.getByRole("button", { name: /actions for rename me/i }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Rename" }));
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Renamed" } });
    fireEvent.click(screen.getByRole("button", { name: "Rename" }));
    await act(async () => undefined);
    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/sessions/local%3Aa/rename"),
      expect.anything(),
    );
    expect(applyNavigationMutation).toHaveBeenCalledTimes(1);
  });
  test("routes project favorite through the rendered project menu", async () => {
    const applyNavigationMutation = vi.fn().mockResolvedValue(undefined);
    installState([catalogResource([{ key: "p", name: "Project", session_count: 0 }])]);
    navigationStore.setState({ applyNavigationMutation });
    const fetchMock = vi
      .fn()
      .mockResolvedValue(jsonResponse({ ok: true, navigation: { generation_id: "g1", targets: [] } }));
    vi.stubGlobal("fetch", fetchMock);
    render(<Rail />);
    fireEvent.click(screen.getByRole("button", { name: /actions for project/i }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Add to pinned" }));
    await act(async () => undefined);
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/favorite",
      expect.objectContaining({ body: JSON.stringify({ kind: "project", id: "p", favorited: true }) }),
    );
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
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        jsonResponse({ ok: true, changed: true, navigation: { generation_id: "g1", targets: [] } }),
      )
      .mockResolvedValueOnce(
        jsonResponse({ deleted: ["a"], skipped: [], navigation: { generation_id: "g1", targets: [] } }),
      );
    vi.stubGlobal("fetch", fetchMock);
    render(<Rail />);
    fireEvent.click(screen.getByRole("button", { name: /actions for pinned delete/i }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Unpin" }));
    await act(async () => undefined);
    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/session-pin?ref="),
      expect.objectContaining({ method: "DELETE" }),
    );
    fireEvent.click(screen.getByRole("button", { name: /actions for pinned delete/i }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Delete…" }));
    fireEvent.click(screen.getByRole("button", { name: "Delete" }));
    await act(async () => undefined);
    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/sessions/"),
      expect.objectContaining({ method: "POST" }),
    );
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
    const awaitNavigationTargets = vi.fn(() => targetAuthority.promise);
    const shutdown = vi.fn().mockResolvedValue(undefined);
    installState([
      catalogResource([{ key: "p", name: "Project", session_count: 1 }]),
      projectResource("p", [summary({ title: "Shutdown me", live: true })]),
    ]);
    navigationStore.setState({ awaitNavigationInvalidation, awaitNavigationTargets });
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
    expect(awaitNavigationTargets).not.toHaveBeenCalled();
    expect(screen.getByText("Shut down this session?")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Shut down" }).hasAttribute("disabled")).toBe(true);

    deliverInvalidation({
      generationId: "g1",
      sequence: 3,
      targets: [{ kind: "project", projectKey: "p", revision: 2 }],
    });
    await act(async () => undefined);
    expect(awaitNavigationTargets).toHaveBeenCalledWith([{ kind: "project", projectKey: "p", revision: 2 }], "g1");
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
    navigationStore.setState({ applyNavigationMutation });
    const fetchMock = vi.fn(async (_url: string, init?: RequestInit) => {
      if (!init?.method) return jsonResponse([{ id: "pins", name: "Pins", member_count: 3 }]);
      if (init.method === "PATCH")
        return jsonResponse({
          ok: true,
          changed: true,
          section: { id: "pins", name: "Renamed", member_count: 3 },
          navigation: { generation_id: "g1", targets: [] },
        });
      return jsonResponse({
        ok: true,
        changed: true,
        member_count: 3,
        navigation: { generation_id: "g1", targets: [] },
      });
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<Rail />);
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
    expect(fetchMock).toHaveBeenCalledWith("/api/pin-sections/pins", expect.objectContaining({ method: "DELETE" }));
    expect(applyNavigationMutation).toHaveBeenCalledTimes(2);
  });
  test("shows a project root retry while retaining the summary row after a load error", async () => {
    const loadProject = vi.fn().mockRejectedValue(new Error("offline"));
    const rootError = { ...resource({ kind: "project", projectKey: "p" }, null), error: new Error("offline") };
    installState([catalogResource([{ key: "p", name: "Retry project", session_count: 1 }]), rootError]);
    navigationStore.setState({ loadProject });
    render(<Rail />);
    expect(screen.getByText("Retry project")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await act(async () => undefined);
    expect(loadProject).toHaveBeenCalledWith("p");
  });
});

// Legacy mode tests removed — legacy tree store retired per R50.
