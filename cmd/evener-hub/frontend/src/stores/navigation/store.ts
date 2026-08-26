import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { AppwireClientLike } from "../../protocol/testing/fakeClient";
import type {
  AnyNotification,
  AttentionSummary,
  InitializeResponse,
  NavigationCapability,
  NavigationInvalidatedPayload,
  NavigationManifest,
  NavigationPinSectionCatalog,
  NavigationProjectCatalog,
  NavigationProjectPage,
  NavigationProjectResource,
  NavigationSectionResource,
  NavigationSessionLocation,
} from "../../protocol/types.gen";
import { loadExpansion, saveExpansion } from "../../shell/rail/railExpansion";
import { NavigationRevalidator } from "./revalidator";
import { keyID, type NavigationRequest, type ResourceKey, type ResourceState } from "./types";

export type NavigationValue =
  | NavigationManifest
  | NavigationSectionResource
  | NavigationPinSectionCatalog
  | NavigationProjectCatalog
  | NavigationProjectResource
  | NavigationProjectPage
  | NavigationSessionLocation;
type ResourceMap = ReadonlyMap<string, ResourceState>;
export interface NavigationStoreState {
  capability: NavigationCapability | null;
  clientGenerationID: string;
  lastSequence: number;
  manifest: ResourceState<NavigationManifest> | null;
  resources: ResourceMap;
  expanded: ReadonlyMap<string, boolean>;
  attention: { changed: AnyNotification extends never ? never : unknown[]; summary: AttentionSummary | null };
  protocolError: Error | null;
  loadManifest(): Promise<ResourceState<NavigationManifest>>;
  loadSection(
    section: "live" | "needs_you",
    offset?: number,
    limit?: number,
  ): Promise<ResourceState<NavigationSectionResource>>;
  loadCatalog(
    catalog: "projects" | "archived_projects" | "test_runs",
    offset?: number,
    limit?: number,
  ): Promise<ResourceState<NavigationProjectCatalog>>;
  loadPinCatalog(offset?: number, limit?: number): Promise<ResourceState<NavigationPinSectionCatalog>>;
  loadPinSection(
    sectionId: string,
    offset?: number,
    limit?: number,
  ): Promise<ResourceState<NavigationPinSectionCatalog>>;
  loadProject(projectKey: string): Promise<ResourceState<NavigationProjectResource>>;
  loadProjectPage(
    projectKey: string,
    tier: "current" | "recent" | "archived",
    offset?: number,
    limit?: number,
  ): Promise<ResourceState<NavigationProjectPage>>;
  lookupLocation(ref: string): Promise<ResourceState<NavigationSessionLocation>>;
  setExpanded(projectKey: string, expanded: boolean): void;
  toggleExpanded(projectKey: string): void;
}

const initialAttention = { changed: [], summary: null };
const initial = (): Omit<
  NavigationStoreState,
  | "loadManifest"
  | "loadSection"
  | "loadCatalog"
  | "loadPinCatalog"
  | "loadPinSection"
  | "loadProject"
  | "loadProjectPage"
  | "lookupLocation"
  | "setExpanded"
  | "toggleExpanded"
> => ({
  capability: null,
  clientGenerationID: "",
  lastSequence: 0,
  manifest: null,
  resources: new Map(),
  expanded: loadExpansion(),
  attention: initialAttention,
  protocolError: null,
});
export const navigationStore = createStore<NavigationStoreState>(() => ({ ...initial(), ...actions() }));
export function useNavigationStore<T>(selector: (s: NavigationStoreState) => T): T;
export function useNavigationStore(): NavigationStoreState;
export function useNavigationStore<T>(selector?: (s: NavigationStoreState) => T): T | NavigationStoreState {
  // biome-ignore lint/correctness/useHookAtTopLevel: both branches call the same Zustand hook
  return selector ? useStore(navigationStore, selector) : useStore(navigationStore);
}

let activeClient: AppwireClientLike | null = null;
let revalidator: NavigationRevalidator | null = null;
let unsubs: Array<() => void> = [];
let bootEpoch = 0;
const LIMIT = 50;
const key = (k: ResourceKey) => Object.freeze(k);
function setResource(state: ResourceState): void {
  if (state.key.kind === "manifest") navigationStore.setState({ manifest: state as ResourceState<NavigationManifest> });
  else {
    const resources = new Map(navigationStore.getState().resources);
    resources.set(keyID(state.key), state);
    navigationStore.setState({ resources });
  }
}
function requestFor<T>(k: ResourceKey): NavigationRequest<T> {
  return async (signal, etag) => {
    const url = urlFor(k);
    const headers: HeadersInit = etag ? { "If-None-Match": etag } : {};
    const response = await fetch(url, { credentials: "same-origin", headers, signal });
    if (response.status !== 200 && response.status !== 304)
      throw new NavigationHTTPError(response.status, "unexpected status");
    const contentType = response.headers.get("content-type") ?? "";
    if (response.status === 200 && !/^application\/json(?:\s*;|$)/i.test(contentType))
      throw new NavigationHTTPError(response.status, "missing JSON content type");
    const generationID = response.headers.get("X-Evener-Navigation-Generation") ?? "";
    const revisionText = response.headers.get("X-Evener-Navigation-Revision") ?? "";
    const responseEtag = response.headers.get("etag") ?? "";
    if (!responseEtag) throw new NavigationHTTPError(response.status, "missing ETag");
    const revision = Number(revisionText);
    let data: unknown;
    if (response.status === 200) data = await response.json();
    return { status: response.status, generationID, revision, etag: responseEtag, data: data as T };
  };
}
export class NavigationHTTPError extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(`navigation HTTP ${status}: ${message}`);
    this.status = status;
  }
}
function urlFor(k: ResourceKey): string {
  const q = (offset: number, limit: number) => `?offset=${offset}&limit=${limit}`;
  switch (k.kind) {
    case "manifest":
      return "/api/navigation";
    case "section":
      return `/api/navigation/sections/${k.section === "needs_you" ? "needs-you" : "live"}${q(k.offset, k.limit)}`;
    case "pin_catalog":
      return `/api/navigation/pin-sections${q(k.offset, k.limit)}`;
    case "pin_section":
      return `/api/navigation/pin-sections/${encodeURIComponent(k.sectionId)}${q(k.offset, k.limit)}`;
    case "catalog":
      return `/api/navigation/catalogs/${k.catalog.replace("_", "-")}${q(k.offset, k.limit)}`;
    case "project":
      return `/api/navigation/projects/${encodeURIComponent(k.projectKey)}`;
    case "project_page":
      return `/api/navigation/projects/${encodeURIComponent(k.projectKey)}?tier=${k.tier}&offset=${k.offset}&limit=${k.limit}`;
    case "location":
      return `/api/navigation/sessions/${encodeURIComponent(k.ref)}`;
  }
}
function load<T>(k: ResourceKey): Promise<ResourceState<T>> {
  if (!revalidator) return Promise.reject(new Error("navigation is not initialized"));
  const requestClient = activeClient;
  const requestEpoch = bootEpoch;
  const requestGeneration = revalidator.generationID;
  return revalidator.load<T>(key(k), requestFor<T>(k)).then((s) => {
    if (requestClient !== activeClient || requestEpoch !== bootEpoch || requestGeneration !== revalidator?.generationID)
      return s as ResourceState<T>;
    setResource(s);
    return s as ResourceState<T>;
  });
}
function actions() {
  return {
    loadManifest: () => load<NavigationManifest>({ kind: "manifest" }),
    loadSection: (section: "live" | "needs_you", offset = 0, limit = LIMIT) =>
      load<NavigationSectionResource>({ kind: "section", section, offset, limit }),
    loadCatalog: (catalog: "projects" | "archived_projects" | "test_runs", offset = 0, limit = LIMIT) =>
      load<NavigationProjectCatalog>({ kind: "catalog", catalog, offset, limit }),
    loadPinCatalog: (offset = 0, limit = LIMIT) =>
      load<NavigationPinSectionCatalog>({ kind: "pin_catalog", offset, limit }),
    loadPinSection: (sectionId: string, offset = 0, limit = LIMIT) =>
      load<NavigationPinSectionCatalog>({ kind: "pin_section", sectionId, offset, limit }),
    loadProject: (projectKey: string) => load<NavigationProjectResource>({ kind: "project", projectKey }),
    loadProjectPage: (projectKey: string, tier: "current" | "recent" | "archived", offset = 0, limit = LIMIT) =>
      load<NavigationProjectPage>({ kind: "project_page", projectKey, tier, offset, limit }),
    lookupLocation: (ref: string) => load<NavigationSessionLocation>({ kind: "location", ref }),
    setExpanded: (projectKey: string, expanded: boolean) => {
      const expandedMap = new Map(navigationStore.getState().expanded);
      expandedMap.set(projectKey, expanded);
      saveExpansion(expandedMap);
      navigationStore.setState({ expanded: expandedMap });
    },
    toggleExpanded: (projectKey: string) => {
      const m = new Map(navigationStore.getState().expanded);
      m.set(projectKey, !(m.get(projectKey) ?? false));
      saveExpansion(m);
      navigationStore.setState({ expanded: m });
    },
  };
}

async function boot(cap: NavigationCapability, epoch: number): Promise<void> {
  if (cap.version !== 1) {
    navigationStore.setState({ protocolError: new Error(`unsupported navigation capability version ${cap.version}`) });
    return;
  }
  if (revalidator && revalidator.generationID !== cap.generationId) revalidator.resetGeneration(cap.generationId);
  navigationStore.setState({ capability: cap, clientGenerationID: cap.generationId, lastSequence: cap.sequence });
  const manifest = await navigationStore
    .getState()
    .loadManifest()
    .catch(() => null);
  if (!manifest || epoch !== bootEpoch) return;
  const m = manifest.data;
  if (!m) return;
  const jobs: Promise<unknown>[] = [];
  if (m.sections.live.count > 0) jobs.push(navigationStore.getState().loadSection("live"));
  if (m.sections.needs_you.count > 0) jobs.push(navigationStore.getState().loadSection("needs_you"));
  if (m.sections.pin_sections.count > 0) jobs.push(navigationStore.getState().loadPinCatalog());
  for (const c of ["projects", "archived_projects", "test_runs"] as const)
    if (m.catalogs[c].count > 0) jobs.push(navigationStore.getState().loadCatalog(c));
  await Promise.allSettled(jobs);
  // Hydrate only visible/default-expanded projects. A small explicit worker
  // pool prevents a large catalog from monopolising the browser connection.
  const projects = selectSummaries();
  const pending = projects
    .filter((p) => navigationStore.getState().expanded.get(p.key) ?? p.default_expanded)
    .map((p) => p.key);
  let cursor = 0;
  const worker = async () => {
    while (cursor < pending.length) {
      const project = pending[cursor++];
      if (project)
        await navigationStore
          .getState()
          .loadProject(project)
          .catch(() => undefined);
    }
  };
  await Promise.all(Array.from({ length: Math.min(4, pending.length) }, worker));
}
function selectSummaries(): Array<{ key: string; default_expanded?: boolean }> {
  const out: Array<{ key: string; default_expanded?: boolean }> = [];
  for (const r of navigationStore.getState().resources.values()) {
    const d = r.data as { projects?: Array<{ key: string; default_expanded?: boolean }> } | null;
    if (r.key.kind === "catalog" && d?.projects) out.push(...d.projects);
  }
  return out;
}
export function initNavigation(
  client: AppwireClientLike,
  initialize?: InitializeResponse | NavigationCapability | null,
): () => void {
  if (activeClient === client) return () => {};
  unsubs.forEach((u) => {
    u();
  });
  unsubs = [];
  activeClient = client;
  bootEpoch++;
  const epoch = bootEpoch;
  const start = (info?: InitializeResponse | NavigationCapability | null) => {
    const cap: NavigationCapability | undefined =
      info && "navigation" in info ? info.navigation : info && "version" in info ? info : undefined;
    if (cap) void boot(cap, epoch);
  };
  revalidator = new NavigationRevalidator();
  unsubs.push(revalidator.subscribe(setResource));
  unsubs.push(
    client.onNotification((n) => {
      if (n.method === "evener/attention/changed") {
        navigationStore.setState({ attention: { changed: n.params.changed, summary: n.params.summary } });
        return;
      }
      if (n.method !== "evener/navigation/invalidated") return;
      const p = n.params as NavigationInvalidatedPayload;
      const s = navigationStore.getState();
      if (p.generationId !== s.clientGenerationID || p.sequence <= s.lastSequence) {
        navigationStore.setState({ protocolError: new Error("navigation sequence or generation mismatch") });
        return;
      }
      const gap = p.sequence > s.lastSequence + 1;
      navigationStore.setState({ lastSequence: p.sequence });
      if (revalidator) {
        if (gap) revalidator.force(revalidator.loadedKeys().filter((k) => k.kind !== "location"));
        else
          p.targets.forEach((t) => {
            revalidator?.invalidate(t);
          });
      }
    }),
  );
  unsubs.push(
    client.onReady(() => {
      revalidator?.force(revalidator.loadedKeys().filter((k) => k.kind !== "location"));
      void client
        .connect()
        .then((i) => start(i))
        .catch(() => {});
    }),
  );
  if (initialize) start(initialize);
  else
    void client
      .connect()
      .then((i) => start(i))
      .catch(() => {});
  return () => {
    if (activeClient === client) {
      unsubs.forEach((u) => {
        u();
      });
      unsubs = [];
      revalidator?.dispose();
      revalidator = null;
      activeClient = null;
    }
  };
}
export function resetNavigationStoreForTests(): void {
  bootEpoch++;
  unsubs.forEach((u) => {
    u();
  });
  unsubs = [];
  revalidator?.dispose();
  revalidator = null;
  activeClient = null;
  navigationStore.setState({ ...initial(), ...actions() });
}
