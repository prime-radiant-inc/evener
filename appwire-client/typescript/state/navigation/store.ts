// The navigation store core: one instance owns everything a hub connection's
// navigation state is made of - the capability and generation, the
// revalidator's loaded resources, the attention summary, the boot fan-out
// and the rail's expand state. The host supplies the client to read through
// and where expansion is persisted; the store owns its own boot scheduling
// and the one deadline navigation has, the convergence wait.
//
// Framework-free: no React, no zustand, no DOM, no storage API. Each app
// binds its view layer to the triple and its storage to the persistence port.
import type { AppwireClientLike } from "../../clientLike";
import { createFrameworkFreeStore, type FrameworkFreeStore } from "../../frameworkFreeStore";
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
} from "../../types.gen";
import {
  type DecodedNavigationResponse,
  decodeNavigationResponse,
  materializeNavigationResource,
  type NormalizedResource,
  normalizedGraphFromSnapshot,
  snapshotResource,
} from "./codec";
import { isSequenceGap } from "./invalidation";
import { applyDelta, reconcileSnapshot } from "./merge";
import {
  isGenerationMismatch,
  isRevalidatorDisposed,
  type NavigationInvalidationWaiter,
  NavigationRevalidator,
} from "./revalidator";
import {
  canonicalResourceKey,
  isNavigationUnavailable,
  isSettledGone,
  keyID,
  NAVIGATION_CATALOG_LIMIT,
  NAVIGATION_SECTION_LIMIT,
  NavigationBaseInvalidError,
  type NavigationRequest,
  nextNavigationOffset,
  type ResourceKey,
  type ResourceState,
} from "./types";

/** The client surface the navigation store calls. Measured against the core:
 * a read per resource, the invalidation and attention notifications, the
 * ready edge a reconnect arrives on, and the connect a cold start awaits. */
export type NavigationClient = Pick<AppwireClientLike, "connect" | "request" | "onNotification" | "onReady">;

/** Reads and writes the rail's per-row expand state. The store never names a
 * storage API itself: a browser binds this to localStorage, and a host with
 * nowhere to keep expansion binds one that keeps it in memory. */
export interface NavigationPersistence {
  /** The map the host has kept, or an empty one when it has none. */
  readExpansion(): Map<string, boolean>;
  /** Keeps the map. Best-effort: a host that cannot store it drops it. */
  writeExpansion(expansion: ReadonlyMap<string, boolean>): void;
}

/** The expansion id of a project's row. Namespaced, so the same project can
 * carry independent expand state wherever else it is rendered. */
export function projectNodeExpansionKey(projectKey: string): string {
  return `projectnode:${projectKey}`;
}

type ResourceMap = ReadonlyMap<string, ResourceState>;

/** Bound for waiting on a post-mutation invalidation before falling back to a
 * targeted refresh. Invalidations are local hub notifications and normally
 * arrive in milliseconds; the bound only fires when the hub legitimately
 * emits nothing (e.g. shutting down an already-exited session is a success
 * no-op), so the action still converges instead of hanging forever. */
export const NAVIGATION_INVALIDATION_TIMEOUT_MS = 10_000;

/** True when error is this store's not-initialized rejection (a waiter armed
 * while no revalidator existed — e.g. a shutdown action racing client
 * replacement, with navigation becoming v2 before convergence begins). There
 * is nothing to converge against yet the caller's mutation already committed,
 * so callers treat it as a successful no-op. */
function isNavigationNotInitialized(error: unknown): boolean {
  return error instanceof Error && error.message.includes("navigation is not initialized");
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
const PAGE_LIMIT = NAVIGATION_SECTION_LIMIT;
const CATALOG_LIMIT = NAVIGATION_CATALOG_LIMIT;
const NAVIGATION_CATALOGS = ["projects", "archived_projects", "test_runs"] as const;
const key = (resourceKey: ResourceKey) => Object.freeze(canonicalResourceKey(resourceKey));
function pinCatalogData(page: ResourceState<NavigationPinSectionCatalog>): NavigationPinSectionCatalog {
  if (page.error) throw page.error;
  if (!page.data || page.stale) throw new Error("pin catalog did not load");
  return page.data;
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
function nonemptyCatalogs(manifest: NavigationManifest): Array<(typeof NAVIGATION_CATALOGS)[number]> {
  return NAVIGATION_CATALOGS.filter((catalog) => manifest.catalogs[catalog].count > 0);
}

// Single capability gate for boot and reconnect: envelope version 1 with an
// advertised v2 representation. Returns the protocol error for the store to
// publish, or null when the capability is acceptable.
function navigationCapabilityError(cap: NavigationCapability): Error | null {
  if (cap.version !== 1) return new Error(`unsupported navigation capability version ${cap.version}`);
  if (!cap.readVersions?.includes(2)) return new Error("navigation server does not advertise representation v2");
  return null;
}

/** One hub connection's navigation state, plus the three things only the
 * instance can do: wire a client, wait for a mutation to converge, and go
 * back to its initial state. */
export interface NavigationStore extends FrameworkFreeStore<NavigationStoreState> {
  /** Wires a client and starts boot; returns that wiring's teardown. Called
   * again with the same client and no `initialize`, it is a no-op. */
  init(client: NavigationClient, initialize?: InitializeResponse | NavigationCapability | null): () => void;
  /** Await a matching invalidation, but fall back to converging `targets`
   * directly when none arrives within the timeout. A receipt only ends the
   * wait once `settled` confirms the caller's own change is reflected: the
   * hub commits shutdowns on its own refresh cycle, so an unrelated
   * invalidation can arrive first, and converging on it would return before
   * the session disappears. When `settled` is absent the first receipt wins,
   * so a caller with nothing observable to check takes the first receipt.
   * Resolves once the affected resources are settled either way. When
   * navigation is not initialized or in error, convergence is impossible and
   * the caller's mutation already committed, so this resolves immediately as
   * a successful no-op instead of surfacing a false failure. A generation
   * change also resolves: the reboot refetches everything. */
  awaitConvergence(
    invalidation: NavigationInvalidationWaiter,
    targets: NavigationInvalidationTarget[],
    opts?: {
      timeoutMs?: number;
      settled?: () => boolean;
      /** Re-arm the waiter after an unrelated receipt (same predicate the
       * caller used for the initial waiter). Omit to keep first-receipt wins. */
      rearm?: () => NavigationInvalidationWaiter;
    },
  ): Promise<void>;
  /** Drops the client, disposes the revalidator and returns the state to its
   * initial values, re-reading the host's persisted expansion. */
  reset(): void;
}

export interface NavigationStoreDeps {
  /** Where this host keeps rail expansion between visits. */
  persistence: NavigationPersistence;
}

export function createNavigationStore({ persistence }: NavigationStoreDeps): NavigationStore {
  /** The store's whole state: what the host persisted, plus the actions. */
  const navigationState = (): NavigationStoreState => ({
    capability: null,
    mode: "unknown",
    clientGenerationID: "",
    lastSequence: 0,
    manifest: null,
    resources: new Map(),
    expanded: persistence.readExpansion(),
    attention: initialAttention,
    protocolError: null,
    ...actions(),
  });
  const store = createFrameworkFreeStore<NavigationStoreState>(() => navigationState());

  let activeClient: NavigationClient | null = null;
  let revalidator: NavigationRevalidator | null = null;
  let unsubs: Array<() => void> = [];
  let bootEpoch = 0;
  let bootStartedEpoch = -1;
  let manifestFanout: { key: string; promise: Promise<void> } | null = null;
  function clearClientOwnedState(): void {
    store.setState({
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
  function setResource(state: ResourceState): void {
    if (state.key.kind === "manifest") {
      store.setState({ manifest: state as ResourceState<NavigationManifest> });
      return;
    }
    const resources = new Map(store.getState().resources);
    resources.set(keyID(state.key), state);
    store.setState({ resources });
  }
  function publishResourceState(state: ResourceState): void {
    setResource(state);
    if (state.error instanceof NavigationProtocolError) {
      store.setState({ protocolError: state.error });
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
    store.setState({ attention: { changed: [], summary: state.data.attentionSummary } });
    void fanOutManifestResources(state.data, epoch).catch(() => undefined);
  }
  function requestFor<T>(k: ResourceKey, client: NavigationClient): NavigationRequest<T> {
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
      const state = store.getState();
      const previous = (k.kind === "manifest" ? state.manifest : state.resources.get(keyID(k)))?.normalized ?? null;
      let normalized: NormalizedResource | undefined;
      if (decoded.status === "snapshot") {
        normalized = reconcileSnapshot(previous, snapshotResource(k, decoded));
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
    // Only a settled tombstone triggers catalog recovery: a stale one retained
    // across a generation reset may precede the fresh response, and recovery
    // refetches catalogs rather than the project itself.
    const gone = isSettledGone(first);
    if (!gone && !isNavigationUnavailable(first.error)) return first;
    const state = store.getState();
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
            store
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
    const present = [...store.getState().resources.values()].some((r) => {
      if (r.key.kind !== "catalog" || r.stale || r.error) return false;
      return (r.data as NavigationProjectCatalog | null)?.projects.some((p) => p.key === projectKey) ?? false;
    });
    if (!present) return first;
    if (gone) revalidator?.force([projectResourceKey]);
    return load<NavigationProjectResource>(projectResourceKey);
  }
  /** The one ordered sequence behind both expansion actions: publish, then
   * persist, then hydrate what the row just revealed. Persistence is
   * best-effort and must never cost the caller the in-memory change, so the
   * store publishes before it hands the map to the host. */
  function commitExpansion(projectKey: string, expanded: boolean): void {
    const next = new Map(store.getState().expanded);
    next.set(projectKey, expanded);
    store.setState({ expanded: next });
    persistence.writeExpansion(next);
    if (expanded && store.getState().mode === "v2") void hydrateProject(projectKey, bootEpoch);
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
          const loadedPages = [...store.getState().resources.values()]
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
          store.setState({ clientGenerationID: mutation.generation_id, lastSequence: 0 });
        }
        for (const target of mutation.targets) revalidator.invalidate(target);
        revalidator.forceLocations();
        return revalidator.waitForTargets(mutation.targets, mutation.generation_id);
      },
      setExpanded: (projectKey: string, expanded: boolean) => commitExpansion(projectKey, expanded),
      toggleExpanded: (projectKey: string) =>
        commitExpansion(projectKey, !(store.getState().expanded.get(projectKey) ?? false)),
    };
  }
  async function boot(cap: NavigationCapability, epoch: number, client: NavigationClient): Promise<void> {
    if (client !== activeClient || epoch !== bootEpoch) return;
    const capabilityError = navigationCapabilityError(cap);
    if (capabilityError) {
      store.setState({
        mode: "error",
        capability: cap,
        attention: initialAttention,
        protocolError: capabilityError,
      });
      return;
    }
    const previous = store.getState();
    const generationChanged = !!revalidator && revalidator.generationID !== cap.generationId;
    store.setState({
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
    const manifest = await store
      .getState()
      .loadManifest()
      .catch(() => null);
    if (!manifest || epoch !== bootEpoch || client !== activeClient) return;
    // A reconnect can force the first manifest read while it is still in
    // flight. The revalidator owns the trailing retry, so consume that retry
    // before deciding that boot has no manifest to fan out from.
    if (manifest.error) {
      await store
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
    if (manifest.sections.live.count > 0) jobs.push(() => store.getState().loadSection("live"));
    if (manifest.sections.needs_you.count > 0) jobs.push(() => store.getState().loadSection("needs_you"));
    if (manifest.sections.pin_sections.count > 0) jobs.push(() => store.getState().loadPinCatalog());
    jobs.push(...nonemptyCatalogs(manifest).map((catalog) => () => store.getState().loadCatalog(catalog)));
    await runBounded(jobs, epoch);
    if (epoch !== bootEpoch || activeClient === null) return;
    const pinCatalog = store.getState().resources.get(keyID({ kind: "pin_catalog", offset: 0, limit: CATALOG_LIMIT }));
    const pinSections = (pinCatalog?.data as NavigationPinSectionCatalog | null)?.pin_sections ?? [];
    // Hydrate only visible/default-expanded projects. A small explicit worker
    // pool prevents a large catalog from monopolising the browser connection.
    const projects = selectSummaries();
    const pending = projects
      .filter((p) => {
        const expanded = store.getState().expanded;
        return expanded.get(projectNodeExpansionKey(p.key)) ?? expanded.get(p.key) ?? p.default_expanded;
      })
      .map((p) => p.key);
    await runBounded(
      [
        ...pinSections.filter((p) => p.count > 0).map((p) => () => store.getState().loadPinSection(p.id)),
        ...pending.map((project) => () => hydrateProject(project, epoch)),
      ],
      epoch,
    );
  }
  async function hydrateProject(projectKey: string, epoch: number): Promise<void> {
    // One staleness question, asked identically before and after the read: a
    // reset or a client replacement during the read leaves this hydration
    // owed to a store that has moved on, and so does navigation dropping out
    // of v2 under it.
    const stale = () => epoch !== bootEpoch || store.getState().mode !== "v2";
    if (stale()) return;
    const resource = await store
      .getState()
      .loadProject(projectKey)
      .catch(() => null);
    if (!resource?.data || resource.error || stale()) return;
    const project = resource.data;
    if (!isNavigationProjectResource(project)) {
      store.setState({ protocolError: new Error(`invalid navigation project ${projectKey}`) });
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
    for (const r of store.getState().resources.values()) {
      const d = r.data as { projects?: Array<{ key: string; default_expanded?: boolean }> } | null;
      if (r.key.kind === "catalog" && d?.projects) out.push(...d.projects);
    }
    return out;
  }
  function init(client: NavigationClient, initialize?: InitializeResponse | NavigationCapability | null): () => void {
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
        store.setState({
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
          store.setState({ attention: { changed: n.params.changed, summary: n.params.summary } });
          return;
        }
        if (n.method !== "evener/navigation/invalidated") return;
        const p = n.params as NavigationInvalidatedPayload;
        const s = store.getState();
        if (s.mode !== "v2" || p.generationId !== s.clientGenerationID || p.sequence <= s.lastSequence) {
          store.setState({ protocolError: new Error("navigation sequence or generation mismatch") });
          return;
        }
        const gap = isSequenceGap(s.lastSequence, p.sequence);
        store.setState({ lastSequence: p.sequence });
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
          store.setState({
            mode: "error",
            attention: initialAttention,
            protocolError: new Error("navigation capability not available"),
          });
          return;
        }
        const same = cap.version === 1 && revalidator?.generationID === cap.generationId;
        const reconnectError = navigationCapabilityError(cap);
        if (reconnectError) {
          store.setState({
            mode: "error",
            capability: cap,
            attention: initialAttention,
            protocolError: reconnectError,
          });
          return;
        }
        if (same) {
          const previousSequence = store.getState().lastSequence;
          if (cap.sequence < previousSequence) {
            store.setState({
              protocolError: new Error("navigation sequence moved backward within generation"),
            });
            return;
          }
          store.setState({
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
  async function awaitConvergence(
    invalidation: NavigationInvalidationWaiter,
    targets: NavigationInvalidationTarget[],
    opts: {
      timeoutMs?: number;
      settled?: () => boolean;
      /** Re-arm the waiter after an unrelated receipt (same predicate the
       * caller used for the initial waiter). Omit to keep first-receipt wins. */
      rearm?: () => NavigationInvalidationWaiter;
    } = {},
  ): Promise<void> {
    if (store.getState().mode !== "v2") {
      invalidation.cancel();
      return;
    }
    const timeoutMs = opts.timeoutMs ?? NAVIGATION_INVALIDATION_TIMEOUT_MS;
    const deadline = Date.now() + timeoutMs;
    try {
      for (;;) {
        const remaining = deadline - Date.now();
        if (remaining <= 0) break;
        let timer: ReturnType<typeof setTimeout> | undefined;
        let payload: NavigationInvalidatedPayload | undefined;
        try {
          payload = await Promise.race([
            invalidation.promise,
            new Promise<undefined>((resolve) => {
              timer = setTimeout(() => resolve(undefined), remaining);
            }),
          ]);
        } catch (error) {
          // Generation reset rejects outstanding waiters: the reboot refetches
          // every loaded resource, which converges the caller's change. Client
          // replacement disposes the revalidator outright; the replacement
          // client reboots from scratch with the same effect. A waiter armed
          // while navigation was uninitialized rejects the same way: the mode
          // may have become v2 after arming, but there was never anything to
          // converge against and the mutation already committed.
          if (isGenerationMismatch(error) || isRevalidatorDisposed(error) || isNavigationNotInitialized(error)) return;
          throw error;
        } finally {
          if (timer !== undefined) clearTimeout(timer);
        }
        if (payload === undefined) break;
        await store.getState().applyNavigationMutation({
          generation_id: payload.generationId,
          targets: payload.targets,
        });
        if (!opts.settled || !opts.rearm || opts.settled()) return;
        invalidation.cancel();
        invalidation = opts.rearm();
      }
      // The wait may have outlived navigation itself (teardown mid-shutdown):
      // without an initialized v2 store the fallback has nothing to converge
      // and its rejection would be a false failure for a committed mutation.
      if (store.getState().mode !== "v2") return;
      await store.getState().applyNavigationMutation({
        generation_id: store.getState().clientGenerationID,
        targets,
      });
    } finally {
      invalidation.cancel();
    }
  }
  function reset(): void {
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
    store.setState(navigationState());
  }

  return { ...store, init, awaitConvergence, reset };
}
