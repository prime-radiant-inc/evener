import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { AppwireClientLike } from "../../protocol/testing/fakeClient";
import type {
  NavigationReadResponse as AppwireNavigationReadResponse,
  AttentionChanged,
  AttentionSummary,
  InitializeResponse,
  NavigationCapability,
  NavigationInvalidatedPayload,
  NavigationInvalidationTarget,
  NavigationManifest,
  NavigationMutation,
  NavigationPinSectionCatalog,
  NavigationProjectCatalog,
  NavigationProjectPage,
  NavigationProjectResource,
  NavigationReadBase,
  NavigationReadParams,
  NavigationSectionResource,
  NavigationSessionLocation,
} from "../../protocol/types.gen";
import { loadExpansion, projectNodeExpansionKey, saveExpansion } from "../../shell/rail/railExpansion";
import {
  type DecodedNavigationResponse,
  decodeNavigationResponse,
  materializeNavigationResource,
  type NormalizedResource,
  normalizedGraphFromSnapshot,
} from "./codec";
import { applyDelta, reconcileSnapshot } from "./merge";
import { type NavigationInvalidationWaiter, NavigationRevalidator } from "./revalidator";
import {
  canonicalResourceKey,
  isNavigationUnavailable,
  keyID,
  NavigationBaseInvalidError,
  type NavigationRequest,
  nextNavigationOffset,
  type ResourceKey,
  type ResourceState,
} from "./types";

type ResourceMap = ReadonlyMap<string, ResourceState>;

/** Bound for waiting on a post-mutation invalidation before falling back to a
 * targeted refresh. Invalidations are local hub notifications and normally
 * arrive in milliseconds; the bound only fires when the hub legitimately
 * emits nothing (e.g. shutting down an already-exited session is a success
 * no-op), so the action still converges instead of hanging forever. */
export const NAVIGATION_INVALIDATION_TIMEOUT_MS = 10_000;

/** Await a matching invalidation, but fall back to converging `targets`
 * directly when none arrives within the timeout. Resolves once the affected
 * resources are settled either way. */
export async function awaitNavigationConvergence(
  invalidation: NavigationInvalidationWaiter,
  targets: NavigationInvalidationTarget[],
  timeoutMs = NAVIGATION_INVALIDATION_TIMEOUT_MS,
): Promise<void> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    const payload = await Promise.race([
      invalidation.promise,
      new Promise<undefined>((resolve) => {
        timer = setTimeout(() => resolve(undefined), timeoutMs);
      }),
    ]);
    await navigationStore.getState().applyNavigationMutation({
      generation_id: payload?.generationId ?? navigationStore.getState().clientGenerationID,
      targets: payload?.targets ?? targets,
    });
  } finally {
    if (timer !== undefined) clearTimeout(timer);
    invalidation.cancel();
  }
}
export interface NavigationStoreState {
  capability: NavigationCapability | null;
  clientGenerationID: string;
  lastSequence: number;
  manifest: ResourceState<NavigationManifest> | null;
  resources: ResourceMap;
  expanded: ReadonlyMap<string, boolean>;
  attention: { changed: AttentionChanged[]; summary: AttentionSummary | null };
  mode: "unknown" | "v2" | "error";
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
  loadPinCatalogPages(force?: boolean): Promise<void>;
  loadPinSection(sectionId: string, offset?: number, limit?: number): Promise<ResourceState<NavigationSectionResource>>;
  trackPinSection(sectionId: string): void;
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
  awaitNavigationTargets(targets: NavigationInvalidationTarget[], generationID?: string): Promise<void>;
  awaitNavigationInvalidation(
    predicate?: (payload: NavigationInvalidatedPayload) => boolean,
  ): NavigationInvalidationWaiter;
  applyNavigationMutation(mutation: NavigationMutation): Promise<void>;
}

const initialAttention = { changed: [], summary: null };
const initial = (): Omit<
  NavigationStoreState,
  | "loadManifest"
  | "loadSection"
  | "loadCatalog"
  | "loadPinCatalog"
  | "loadPinCatalogPages"
  | "loadPinSection"
  | "trackPinSection"
  | "loadProject"
  | "loadProjectPage"
  | "lookupLocation"
  | "setExpanded"
  | "toggleExpanded"
  | "awaitNavigationTargets"
  | "awaitNavigationInvalidation"
  | "applyNavigationMutation"
> => ({
  capability: null,
  mode: "unknown",
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
let bootStartedEpoch = -1;
let manifestFanout: { key: string; promise: Promise<void> } | null = null;
const PAGE_LIMIT = 50;
const CATALOG_LIMIT = 100;
const NAVIGATION_CATALOGS = ["projects", "archived_projects", "test_runs"] as const;
const key = (resourceKey: ResourceKey) => Object.freeze(canonicalResourceKey(resourceKey));
function clearClientOwnedState(): void {
  navigationStore.setState({
    capability: null,
    clientGenerationID: "",
    lastSequence: 0,
    manifest: null,
    resources: new Map(),
    attention: initialAttention,
    mode: "unknown",
    protocolError: null,
  });
}
function pinCatalogData(page: ResourceState<NavigationPinSectionCatalog>): NavigationPinSectionCatalog {
  if (page.error) throw page.error;
  if (!page.data || page.stale) throw new Error("pin catalog did not load");
  return page.data;
}
function setResource(state: ResourceState): void {
  if (state.key.kind === "manifest") {
    navigationStore.setState({ manifest: state as ResourceState<NavigationManifest> });
    return;
  }
  const resources = new Map(navigationStore.getState().resources);
  resources.set(keyID(state.key), state);
  navigationStore.setState({ resources });
}
function provisionalForGeneration<T>(state: ResourceState<T>, generationID: string): ResourceState<T> {
  if (state.generationID === generationID) return state;
  return Object.freeze({
    ...state,
    generationID,
    loadedRevision: null,
    targetRevision: null,
    etag: null,
    version: undefined,
    stale: true,
    loading: false,
    error: null,
  });
}
function publishResourceState(state: ResourceState): void {
  setResource(state);
  if (state.error instanceof NavigationProtocolError) {
    navigationStore.setState({ protocolError: state.error });
    if (state.key.kind !== "manifest") revalidator?.force([{ kind: "manifest" }]);
  }
}
function coordinateManifestState(state: ResourceState, epoch: number): void {
  if (
    state.key.kind !== "manifest" ||
    state.loading ||
    state.stale ||
    !state.data ||
    !isNavigationManifest(state.data)
  ) {
    return;
  }
  navigationStore.setState({ attention: { changed: [], summary: state.data.attentionSummary } });
  void fanOutManifestResources(state.data, epoch).catch(() => undefined);
}
class NavigationProtocolError extends Error {
  constructor(message: string, options?: ErrorOptions) {
    super(`navigation protocol: ${message}`, options);
  }
}
type RecordValue = Record<string, unknown>;
const record = (value: unknown): value is RecordValue => !!value && typeof value === "object" && !Array.isArray(value);
const string = (value: unknown): value is string => typeof value === "string";
const bool = (value: unknown): value is boolean => typeof value === "boolean";
const count = (value: unknown): value is number => Number.isSafeInteger(value) && (value as number) >= 0;
function isNavigationManifest(value: unknown): value is NavigationManifest {
  if (!value || typeof value !== "object") return false;
  const manifest = value as Partial<NavigationManifest>;
  const descriptor = (candidate: unknown) =>
    !!candidate && typeof candidate === "object" && count((candidate as { count?: unknown }).count);
  return (
    string(manifest.generation_id) &&
    count(manifest.revision) &&
    Array.isArray(manifest.sources) &&
    manifest.sources.every(
      (source) =>
        record(source) && string(source.id) && string(source.label) && string(source.kind) && bool(source.online),
    ) &&
    record(manifest.attentionSummary) &&
    count(manifest.attentionSummary.needsYou) &&
    count(manifest.attentionSummary.error) &&
    count(manifest.attentionSummary.working) &&
    !!manifest.sections &&
    descriptor(manifest.sections.live) &&
    descriptor(manifest.sections.needs_you) &&
    descriptor(manifest.sections.pin_sections) &&
    !!manifest.catalogs &&
    descriptor(manifest.catalogs.projects) &&
    descriptor(manifest.catalogs.archived_projects) &&
    descriptor(manifest.catalogs.test_runs)
  );
}
function isNavigationProjectResource(value: unknown): value is NavigationProjectResource {
  if (!value || typeof value !== "object") return false;
  const project = value as Partial<NavigationProjectResource>;
  return [project.current, project.recent, project.archived].every(
    (tier) => !!tier && Array.isArray(tier.sessions) && Number.isSafeInteger(tier.remaining),
  );
}
function assertNavigationPageProgress(k: ResourceKey, value: unknown): void {
  if (!record(value)) return;
  let rows = 0;
  let remaining = 0;
  switch (k.kind) {
    case "section":
    case "pin_section":
    case "project_page":
      rows = Array.isArray(value.sessions) ? value.sessions.length : 0;
      remaining = count(value.remaining) ? value.remaining : 0;
      break;
    case "pin_catalog":
      rows = Array.isArray(value.pin_sections) ? value.pin_sections.length : 0;
      remaining = count(value.remaining) ? value.remaining : 0;
      break;
    case "catalog":
      rows = Array.isArray(value.projects) ? value.projects.length : 0;
      remaining = count(value.remaining) ? value.remaining : 0;
      break;
    case "project":
      for (const candidate of [value.current, value.recent, value.archived]) {
        if (!record(candidate)) continue;
        rows += Array.isArray(candidate.sessions) ? candidate.sessions.length : 0;
        remaining += count(candidate.remaining) ? candidate.remaining : 0;
      }
      break;
    default:
      return;
  }
  if (rows === 0 && remaining > 0) {
    throw new NavigationProtocolError(`${k.kind} returned no rows with remaining data`);
  }
}
function paramsFor(k: ResourceKey, base: NavigationReadBase | undefined): NavigationReadParams {
  const conditional = { representationVersion: 2 as const, ...(base ? { base } : {}) };
  switch (k.kind) {
    case "manifest":
      return { resource: "manifest", ...conditional };
    case "section":
      return { resource: "section", section: k.section, offset: k.offset, limit: k.limit, ...conditional };
    case "pin_catalog":
      return { resource: "pin_catalog", offset: k.offset, limit: k.limit, ...conditional };
    case "pin_section":
      return { resource: "pin_section", sectionId: k.sectionId, offset: k.offset, limit: k.limit, ...conditional };
    case "catalog":
      return { resource: "catalog", catalog: k.catalog, offset: k.offset, limit: k.limit, ...conditional };
    case "project":
      return { resource: "project", projectKey: k.projectKey, ...conditional };
    case "project_page":
      return {
        resource: "project_page",
        projectKey: k.projectKey,
        tier: k.tier,
        offset: k.offset,
        limit: k.limit,
        ...conditional,
      };
    case "location":
      return { resource: "location", ref: k.ref, ...conditional };
  }
}
function requestFor<T>(k: ResourceKey, client: AppwireClientLike): NavigationRequest<T> {
  return async (_signal, base) => {
    const response = await client.request("evener/navigation/read", paramsFor(k, base));
    if (!response || typeof response !== "object") throw new NavigationProtocolError("invalid response envelope");
    const { generationId, revision, etag: responseEtag } = response as AppwireNavigationReadResponse;
    let decoded: DecodedNavigationResponse;
    try {
      decoded = decodeNavigationResponse(k, base, response);
    } catch (cause) {
      if (cause instanceof NavigationBaseInvalidError) throw cause;
      throw new NavigationProtocolError("invalid v2 response", { cause });
    }
    const state = navigationStore.getState();
    const previous = (k.kind === "manifest" ? state.manifest : state.resources.get(keyID(k)))?.normalized ?? null;
    let normalized: NormalizedResource | undefined;
    if (decoded.status === "snapshot") {
      const incoming: NormalizedResource = {
        key: k,
        graph: normalizedGraphFromSnapshot(decoded.snapshot),
        version: decoded.version,
        presence: "present",
      };
      normalized = reconcileSnapshot(previous, incoming);
    } else if (decoded.status === "delta") {
      if (!previous) throw new NavigationProtocolError("delta has no cached base");
      normalized = applyDelta(previous, decoded.delta, decoded.version);
    } else if (decoded.status === "gone") {
      normalized = Object.freeze({
        key: k,
        graph: normalizedGraphFromSnapshot({ metadata: {}, entities: [], containers: [] }),
        version: Object.freeze({ ...decoded.version }),
        presence: "gone",
      });
    } else {
      normalized = previous ?? undefined;
    }
    if (decoded.status !== "not_modified" && !normalized)
      throw new NavigationProtocolError("not_modified has no cached resource");
    const materialized =
      decoded.status === "not_modified" || decoded.status === "gone" || !normalized
        ? undefined
        : (materializeNavigationResource(normalized) as T);
    if (materialized !== undefined) assertNavigationPageProgress(k, materialized);
    return {
      status: decoded.status === "not_modified" ? 304 : 200,
      generationID: generationId,
      revision,
      etag: responseEtag,
      data: decoded.status === "gone" ? null : materialized,
      v2: decoded,
      normalized,
    };
  };
}
function load<T>(k: ResourceKey): Promise<ResourceState<T>> {
  if (!revalidator) return Promise.reject(new Error("navigation is not initialized"));
  const requestRevalidator = revalidator;
  const requestClient = activeClient;
  if (!requestClient) return Promise.reject(new Error("navigation is not initialized"));
  const resourceKey = key(k);
  return requestRevalidator.load<T>(resourceKey, requestFor<T>(resourceKey, requestClient));
}
async function withProjectRecovery(projectKey: string): Promise<ResourceState<NavigationProjectResource>> {
  const projectResourceKey = { kind: "project", projectKey } as const;
  const first = await load<NavigationProjectResource>(projectResourceKey);
  const gone = first.normalized?.presence === "gone";
  if (!gone && !isNavigationUnavailable(first.error)) return first;
  const state = navigationStore.getState();
  const catalogs = [...state.resources.values()].filter((r) => r.key.kind === "catalog");
  const known = catalogs.filter((r) => {
    const data = r.data as NavigationProjectCatalog | null;
    return data?.projects.some((p) => p.key === projectKey) ?? false;
  });
  const candidates = known.length > 0 ? known : catalogs;
  if (candidates.length === 0) {
    const manifestKey = { kind: "manifest" } as const;
    revalidator?.force([manifestKey]);
    const manifest = await load<NavigationManifest>(manifestKey).catch(() => null);
    if (manifest?.data && !manifest.error && !manifest.stale && isNavigationManifest(manifest.data)) {
      await Promise.all(
        nonemptyCatalogs(manifest.data).map((catalog) =>
          navigationStore
            .getState()
            .loadCatalog(catalog)
            .catch(() => undefined),
        ),
      );
    }
  } else {
    revalidator?.force(candidates.map((r) => r.key));
    await Promise.all(candidates.map((r) => load(r.key).catch(() => undefined)));
  }
  const present = [...navigationStore.getState().resources.values()].some((r) => {
    if (r.key.kind !== "catalog" || r.stale || r.error) return false;
    return (r.data as NavigationProjectCatalog | null)?.projects.some((p) => p.key === projectKey) ?? false;
  });
  if (!present) return first;
  if (gone) revalidator?.force([projectResourceKey]);
  return load<NavigationProjectResource>(projectResourceKey);
}
function actions() {
  return {
    loadManifest: () => load<NavigationManifest>({ kind: "manifest" }),
    loadSection: (section: "live" | "needs_you", offset = 0, limit = PAGE_LIMIT) =>
      load<NavigationSectionResource>({ kind: "section", section, offset, limit }),
    loadCatalog: (catalog: "projects" | "archived_projects" | "test_runs", offset = 0, limit = CATALOG_LIMIT) =>
      load<NavigationProjectCatalog>({ kind: "catalog", catalog, offset, limit }),
    loadPinCatalog: (offset = 0, limit = CATALOG_LIMIT) =>
      load<NavigationPinSectionCatalog>({ kind: "pin_catalog", offset, limit }),
    loadPinCatalogPages: async (force = false) => {
      if (force && revalidator) {
        const loadedPages = [...navigationStore.getState().resources.values()]
          .filter((resource) => resource.key.kind === "pin_catalog")
          .map((resource) => resource.key);
        revalidator.force(loadedPages);
        const refreshedPages = await Promise.all(
          loadedPages.map((resourceKey) => load<NavigationPinSectionCatalog>(resourceKey)),
        );
        for (const page of refreshedPages) pinCatalogData(page);
      }
      let offset = 0;
      while (true) {
        const page = await load<NavigationPinSectionCatalog>({
          kind: "pin_catalog",
          offset,
          limit: CATALOG_LIMIT,
        });
        const data = pinCatalogData(page);
        if (data.remaining === 0) return;
        if (data.pin_sections.length === 0) throw new Error("pin catalog page did not advance");
        offset = nextNavigationOffset(offset, data.pin_sections.length);
      }
    },
    loadPinSection: (sectionId: string, offset = 0, limit = PAGE_LIMIT) =>
      load<NavigationSectionResource>({ kind: "pin_section", sectionId, offset, limit }),
    trackPinSection: (sectionId: string) => {
      if (!revalidator || !activeClient) return;
      const resourceKey = key({ kind: "pin_section", sectionId, offset: 0, limit: PAGE_LIMIT });
      revalidator.track(resourceKey, requestFor(resourceKey, activeClient));
    },
    loadProject: (projectKey: string) => withProjectRecovery(projectKey),
    loadProjectPage: (projectKey: string, tier: "current" | "recent" | "archived", offset = 0, limit = PAGE_LIMIT) =>
      load<NavigationProjectPage>({ kind: "project_page", projectKey, tier, offset, limit }),
    lookupLocation: (ref: string) => load<NavigationSessionLocation>({ kind: "location", ref }),
    awaitNavigationTargets: (targets: NavigationInvalidationTarget[], generationID?: string) => {
      if (!revalidator) return Promise.reject(new Error("navigation is not initialized"));
      return revalidator.waitForTargets(targets, generationID);
    },
    awaitNavigationInvalidation: (predicate?: (payload: NavigationInvalidatedPayload) => boolean) => {
      if (!revalidator)
        return {
          promise: Promise.reject(new Error("navigation is not initialized")),
          cancel: () => {},
        };
      return revalidator.waitForInvalidation(predicate);
    },
    applyNavigationMutation: (mutation: NavigationMutation) => {
      if (!revalidator) return Promise.reject(new Error("navigation is not initialized"));
      if (mutation.generation_id !== revalidator.generationID) {
        revalidator.resetGeneration(mutation.generation_id);
        navigationStore.setState({ clientGenerationID: mutation.generation_id, lastSequence: 0 });
      }
      for (const target of mutation.targets) revalidator.invalidate(target);
      revalidator.forceLocations();
      return revalidator.waitForTargets(mutation.targets, mutation.generation_id);
    },
    setExpanded: (projectKey: string, expanded: boolean) => {
      const expandedMap = new Map(navigationStore.getState().expanded);
      expandedMap.set(projectKey, expanded);
      navigationStore.setState({ expanded: expandedMap });
      saveExpansion(expandedMap);
      const mode = navigationStore.getState().mode;
      if (expanded && mode === "v2") void hydrateProject(projectKey, bootEpoch);
    },
    toggleExpanded: (projectKey: string) => {
      const m = new Map(navigationStore.getState().expanded);
      m.set(projectKey, !(m.get(projectKey) ?? false));
      saveExpansion(m);
      navigationStore.setState({ expanded: m });
      if (m.get(projectKey) && navigationStore.getState().mode === "v2") void hydrateProject(projectKey, bootEpoch);
    },
  };
}
function nonemptyCatalogs(manifest: NavigationManifest): Array<(typeof NAVIGATION_CATALOGS)[number]> {
  return NAVIGATION_CATALOGS.filter((catalog) => manifest.catalogs[catalog].count > 0);
}

async function boot(cap: NavigationCapability, epoch: number, client: AppwireClientLike): Promise<void> {
  if (client !== activeClient || epoch !== bootEpoch) return;
  if (cap.version !== 1) {
    navigationStore.setState({
      mode: "error",
      capability: cap,
      attention: initialAttention,
      protocolError: new Error(`unsupported navigation capability version ${cap.version}`),
    });
    return;
  }
  if (!cap.readVersions?.includes(2)) {
    navigationStore.setState({
      mode: "error",
      capability: cap,
      attention: initialAttention,
      protocolError: new Error("navigation server does not advertise representation v2"),
    });
    return;
  }
  const previous = navigationStore.getState();
  const generationChanged = !!revalidator && revalidator.generationID !== cap.generationId;
  navigationStore.setState({
    capability: cap,
    mode: "v2",
    clientGenerationID: cap.generationId,
    lastSequence: cap.sequence,
    manifest:
      generationChanged && previous.manifest
        ? provisionalForGeneration(previous.manifest, cap.generationId)
        : previous.manifest,
    resources: generationChanged
      ? new Map(
          [...previous.resources].map(([id, resource]) => [id, provisionalForGeneration(resource, cap.generationId)]),
        )
      : previous.resources,
    attention:
      previous.mode === "v2" && previous.clientGenerationID === cap.generationId
        ? previous.attention
        : initialAttention,
  });
  if (generationChanged) revalidator?.resetGeneration(cap.generationId);
  if (bootStartedEpoch === epoch) return;
  bootStartedEpoch = epoch;
  const manifest = await navigationStore
    .getState()
    .loadManifest()
    .catch(() => null);
  if (!manifest || epoch !== bootEpoch || client !== activeClient) return;
  // A reconnect can force the first manifest read while it is still in
  // flight. The revalidator owns the trailing retry, so consume that retry
  // before deciding that boot has no manifest to fan out from.
  if (manifest.error) {
    await navigationStore
      .getState()
      .loadManifest()
      .catch(() => null);
  }
}

async function fanOutManifestResources(manifest: NavigationManifest, epoch: number): Promise<void> {
  if (epoch !== bootEpoch || activeClient === null) return;
  const manifestKey = `${manifest.generation_id}:${manifest.revision}`;
  if (manifestFanout?.key === manifestKey) {
    await manifestFanout.promise;
    return;
  }
  const promise = hydrateManifestResources(manifest, epoch);
  manifestFanout = { key: manifestKey, promise };
  await promise;
}

async function hydrateManifestResources(manifest: NavigationManifest, epoch: number): Promise<void> {
  const jobs: Array<() => Promise<unknown>> = [];
  if (manifest.sections.live.count > 0) jobs.push(() => navigationStore.getState().loadSection("live"));
  if (manifest.sections.needs_you.count > 0) jobs.push(() => navigationStore.getState().loadSection("needs_you"));
  if (manifest.sections.pin_sections.count > 0) jobs.push(() => navigationStore.getState().loadPinCatalog());
  jobs.push(...nonemptyCatalogs(manifest).map((catalog) => () => navigationStore.getState().loadCatalog(catalog)));
  await runBounded(jobs, epoch);
  if (epoch !== bootEpoch || activeClient === null) return;
  const pinCatalog = navigationStore
    .getState()
    .resources.get(keyID({ kind: "pin_catalog", offset: 0, limit: CATALOG_LIMIT }));
  const pinSections = (pinCatalog?.data as NavigationPinSectionCatalog | null)?.pin_sections ?? [];
  // Hydrate only visible/default-expanded projects. A small explicit worker
  // pool prevents a large catalog from monopolising the browser connection.
  const projects = selectSummaries();
  const pending = projects
    .filter((p) => {
      const expanded = navigationStore.getState().expanded;
      return expanded.get(projectNodeExpansionKey(p.key)) ?? expanded.get(p.key) ?? p.default_expanded;
    })
    .map((p) => p.key);
  await runBounded(
    [
      ...pinSections.filter((p) => p.count > 0).map((p) => () => navigationStore.getState().loadPinSection(p.id)),
      ...pending.map((project) => () => hydrateProject(project, epoch)),
    ],
    epoch,
  );
}
async function hydrateProject(projectKey: string, epoch: number): Promise<void> {
  if (epoch !== bootEpoch || navigationStore.getState().mode !== "v2") return;
  const resource = await navigationStore
    .getState()
    .loadProject(projectKey)
    .catch(() => null);
  if (!resource?.data || resource.error || epoch !== bootEpoch) return;
  const project = resource.data;
  if (!isNavigationProjectResource(project)) {
    navigationStore.setState({ protocolError: new Error(`invalid navigation project ${projectKey}`) });
    return;
  }
}
async function runBounded(jobs: Array<() => Promise<unknown>>, epoch: number): Promise<void> {
  let cursor = 0;
  const worker = async () => {
    while (cursor < jobs.length && epoch === bootEpoch) {
      const job = jobs[cursor++];
      if (job) await job().catch(() => undefined);
    }
  };
  await Promise.all(Array.from({ length: Math.min(4, jobs.length) }, worker));
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
  if (activeClient === client && initialize === undefined) return () => {};
  const ownershipChanged = activeClient !== null && activeClient !== client;
  unsubs.forEach((u) => {
    u();
  });
  unsubs = [];
  if (ownershipChanged) {
    revalidator?.dispose();
    revalidator = null;
    clearClientOwnedState();
  }
  activeClient = client;
  bootEpoch++;
  manifestFanout = null;
  const epoch = bootEpoch;
  const ownedClient = client;
  const start = (info?: InitializeResponse | NavigationCapability | null) => {
    if (ownedClient !== activeClient || epoch !== bootEpoch) return;
    let cap: NavigationCapability | undefined;
    if (info && "navigation" in info) cap = info.navigation;
    else if (info && "version" in info) cap = info;
    if (!cap) {
      navigationStore.setState({
        mode: "error",
        attention: initialAttention,
        protocolError: new Error("navigation capability not available"),
      });
      return;
    }
    void boot(cap, epoch, ownedClient);
  };
  revalidator = new NavigationRevalidator();
  unsubs.push(
    revalidator.subscribe((state) => {
      if (ownedClient !== activeClient || epoch !== bootEpoch) return;
      publishResourceState(state);
      coordinateManifestState(state, epoch);
    }),
  );
  unsubs.push(
    client.onNotification((n) => {
      if (ownedClient !== activeClient || epoch !== bootEpoch) return;
      if (n.method === "evener/attention/changed") {
        navigationStore.setState({ attention: { changed: n.params.changed, summary: n.params.summary } });
        return;
      }
      if (n.method !== "evener/navigation/invalidated") return;
      const p = n.params as NavigationInvalidatedPayload;
      const s = navigationStore.getState();
      if (s.mode !== "v2" || p.generationId !== s.clientGenerationID || p.sequence <= s.lastSequence) {
        navigationStore.setState({ protocolError: new Error("navigation sequence or generation mismatch") });
        return;
      }
      const gap = p.sequence > s.lastSequence + 1;
      navigationStore.setState({ lastSequence: p.sequence });
      if (revalidator) {
        if (gap) revalidator.force(revalidator.loadedKeys());
        else {
          p.targets.forEach((t) => {
            revalidator?.invalidate(t);
          });
          revalidator.forceLocations();
        }
      }
      revalidator?.notifyInvalidation(p);
    }),
  );
  unsubs.push(
    client.onReady((initialize) => {
      if (ownedClient !== activeClient || epoch !== bootEpoch) return;
      const cap = initialize.navigation;
      if (!cap) {
        navigationStore.setState({
          mode: "error",
          attention: initialAttention,
          protocolError: new Error("navigation capability not available"),
        });
        return;
      }
      const same = cap.version === 1 && revalidator?.generationID === cap.generationId;
      if (cap.version !== 1 || !cap.readVersions?.includes(2)) {
        navigationStore.setState({
          mode: "error",
          capability: cap,
          attention: initialAttention,
          protocolError: new Error(
            cap.version !== 1
              ? `unsupported navigation capability version ${cap.version}`
              : "navigation server does not advertise representation v2",
          ),
        });
        return;
      }
      if (same) {
        const previousSequence = navigationStore.getState().lastSequence;
        if (cap.sequence < previousSequence) {
          navigationStore.setState({
            protocolError: new Error("navigation sequence moved backward within generation"),
          });
          return;
        }
        navigationStore.setState({
          capability: cap,
          mode: "v2",
          clientGenerationID: cap.generationId,
          lastSequence: cap.sequence,
        });
        if (cap.sequence > previousSequence) revalidator?.force(revalidator.loadedKeys());
        else {
          const retryable = [...(revalidator?.states().values() ?? [])]
            .filter((state) => !state.loading && (state.stale || state.error !== null))
            .map((state) => state.key);
          revalidator?.force(retryable);
        }
      } else start(initialize);
    }),
  );
  if (initialize) start(initialize);
  else
    void client
      .connect()
      .then((i) => {
        if (ownedClient !== activeClient || epoch !== bootEpoch) return;
        start(i);
      })
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
      bootStartedEpoch = -1;
      manifestFanout = null;
      clearClientOwnedState();
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
  bootStartedEpoch = -1;
  manifestFanout = null;
  navigationStore.setState({ ...initial(), ...actions() });
}
