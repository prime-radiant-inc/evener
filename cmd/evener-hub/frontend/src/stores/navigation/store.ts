import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { AppwireClientLike } from "../../protocol/testing/fakeClient";
import type {
  AttentionChanged,
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
  attention: { changed: AttentionChanged[]; summary: AttentionSummary | null };
  mode: "unknown" | "legacy" | "v1" | "error";
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
  loadPinSection(sectionId: string, offset?: number, limit?: number): Promise<ResourceState<NavigationSectionResource>>;
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
const PAGE_LIMIT = 50;
const CATALOG_LIMIT = 100;
const key = (k: ResourceKey) => Object.freeze(k);
function setResource(state: ResourceState, client = activeClient, epoch = bootEpoch): void {
  if (client !== activeClient || epoch !== bootEpoch) return;
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
    const response = await fetch(url, { method: "GET", credentials: "same-origin", headers, signal });
    if (response.status !== 200 && response.status !== 304)
      throw new NavigationHTTPError(response.status, "unexpected status");
    const contentType = response.headers.get("content-type") ?? "";
    if (response.status === 200 && !/^application\/json(?:\s*;|$)/i.test(contentType))
      throw new NavigationHTTPError(response.status, "missing JSON content type");
    const generationID = response.headers.get("X-Evener-Navigation-Generation") ?? "";
    const revisionText = response.headers.get("X-Evener-Navigation-Revision") ?? "";
    const responseEtag = response.headers.get("etag") ?? "";
    if (!generationID) throw new NavigationHTTPError(response.status, "missing generation");
    if (!/^(0|[1-9]\d*)$/.test(revisionText)) throw new NavigationHTTPError(response.status, "invalid revision");
    if (!responseEtag) throw new NavigationHTTPError(response.status, "missing ETag");
    const revision = Number(revisionText);
    if (!Number.isSafeInteger(revision)) throw new NavigationHTTPError(response.status, "invalid revision");
    let data: unknown;
    if (response.status === 200) {
      data = await response.json();
      if (!isNavigationValue(k, data, generationID, revision)) {
        throw new NavigationProtocolError(`invalid ${k.kind} body`);
      }
    }
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
class NavigationProtocolError extends Error {
  constructor(message: string) {
    super(`navigation protocol: ${message}`);
  }
}
type RecordValue = Record<string, unknown>;
const record = (value: unknown): value is RecordValue => !!value && typeof value === "object" && !Array.isArray(value);
const string = (value: unknown): value is string => typeof value === "string";
const bool = (value: unknown): value is boolean => typeof value === "boolean";
const count = (value: unknown): value is number => Number.isSafeInteger(value) && (value as number) >= 0;
const optional = (value: unknown, check: (candidate: unknown) => boolean) => value === undefined || check(value);
function metadata(value: RecordValue, generationID: string, revision: number): boolean {
  return value.generation_id === generationID && value.revision === revision;
}
function sessions(value: unknown): boolean {
  if (!Array.isArray(value)) return false;
  const pending = [...value];
  let nodes = 0;
  while (pending.length > 0) {
    const candidate = pending.pop();
    if (!record(candidate) || ++nodes > 2_000) return false;
    if (
      !string(candidate.ref) ||
      !string(candidate.host_id) ||
      !string(candidate.session_id) ||
      !string(candidate.title) ||
      !string(candidate.project) ||
      !string(candidate.state) ||
      !string(candidate.kind) ||
      !bool(candidate.live) ||
      !Array.isArray(candidate.children) ||
      !optional(candidate.branch, string) ||
      !optional(candidate.cluster_count, count) ||
      !optional(candidate.favorite, bool) ||
      !optional(candidate.rename, bool) ||
      !optional(candidate.ask_pending, bool) ||
      !optional(candidate.dormant, bool) ||
      !optional(candidate.updated_at, string) ||
      !optional(candidate.more_subagents, count) ||
      !optional(candidate.omitted_descendants, count)
    )
      return false;
    pending.push(...candidate.children);
  }
  return true;
}
function tier(value: unknown): boolean {
  return record(value) && sessions(value.sessions) && count(value.remaining);
}
function projectSummary(value: unknown): boolean {
  if (!record(value)) return false;
  return (
    string(value.key) &&
    string(value.name) &&
    count(value.session_count) &&
    optional(value.working_dir, string) &&
    optional(value.rollup_state, string) &&
    optional(value.rollup_live, count) &&
    optional(value.rollup_attn, count) &&
    optional(value.default_expanded, bool) &&
    optional(value.more_current, count) &&
    optional(value.more_recent, count) &&
    optional(value.more_archived, count) &&
    optional(value.worktrees, count) &&
    optional(value.is_archived, bool) &&
    optional(value.favorite, bool)
  );
}
function isNavigationValue(k: ResourceKey, value: unknown, generationID: string, revision: number): boolean {
  if (!record(value) || !metadata(value, generationID, revision)) return false;
  switch (k.kind) {
    case "manifest":
      return isNavigationManifest(value);
    case "section":
    case "pin_section":
      return sessions(value.sessions) && count(value.remaining) && bool(value.truncated);
    case "pin_catalog":
      return (
        Array.isArray(value.pin_sections) &&
        value.pin_sections.every((item) => record(item) && string(item.id) && string(item.name) && count(item.count)) &&
        count(value.remaining)
      );
    case "catalog":
      return Array.isArray(value.projects) && value.projects.every(projectSummary) && count(value.remaining);
    case "project":
      return (
        value.key === k.projectKey &&
        tier(value.current) &&
        tier(value.recent) &&
        tier(value.archived) &&
        bool(value.truncated)
      );
    case "project_page":
      return (
        value.key === k.projectKey &&
        value.tier === k.tier &&
        value.offset === k.offset &&
        sessions(value.sessions) &&
        count(value.remaining) &&
        bool(value.truncated)
      );
    case "location":
      return (
        value.ref === k.ref &&
        string(value.top_level_ref) &&
        bool(value.top_level) &&
        optional(value.project_key, string) &&
        optional(value.tier, string) &&
        optional(value.pin_section_id, string) &&
        optional(value.session, (candidate) => sessions([candidate]))
      );
  }
}
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
  const requestRevalidator = revalidator;
  const requestClient = activeClient;
  const requestEpoch = bootEpoch;
  const requestGeneration = requestRevalidator.generationID;
  return requestRevalidator.load<T>(key(k), requestFor<T>(k)).then((s) => {
    if (
      requestClient !== activeClient ||
      requestEpoch !== bootEpoch ||
      requestRevalidator !== revalidator ||
      requestGeneration !== revalidator.generationID
    )
      return s as ResourceState<T>;
    setResource(s);
    if (s.error instanceof NavigationProtocolError) {
      navigationStore.setState({ protocolError: s.error });
      if (k.kind !== "manifest") requestRevalidator.force([{ kind: "manifest" }]);
    }
    return s as ResourceState<T>;
  });
}
async function withProjectRecovery(projectKey: string): Promise<ResourceState<NavigationProjectResource>> {
  const first = await load<NavigationProjectResource>({ kind: "project", projectKey });
  const error = first.error as { status?: number } | null;
  if (error?.status !== 404) return first;
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
      const manifestData = manifest.data;
      const catalogJobs = (["projects", "archived_projects", "test_runs"] as const)
        .filter((catalog) => manifestData.catalogs[catalog].count > 0)
        .map((catalog) =>
          navigationStore
            .getState()
            .loadCatalog(catalog)
            .catch(() => undefined),
        );
      await Promise.all(catalogJobs);
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
  return load<NavigationProjectResource>({ kind: "project", projectKey });
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
    loadPinSection: (sectionId: string, offset = 0, limit = PAGE_LIMIT) =>
      load<NavigationSectionResource>({ kind: "pin_section", sectionId, offset, limit }),
    loadProject: (projectKey: string) => withProjectRecovery(projectKey),
    loadProjectPage: (projectKey: string, tier: "current" | "recent" | "archived", offset = 0, limit = PAGE_LIMIT) =>
      load<NavigationProjectPage>({ kind: "project_page", projectKey, tier, offset, limit }),
    lookupLocation: (ref: string) => load<NavigationSessionLocation>({ kind: "location", ref }),
    setExpanded: (projectKey: string, expanded: boolean) => {
      const expandedMap = new Map(navigationStore.getState().expanded);
      expandedMap.set(projectKey, expanded);
      navigationStore.setState({ expanded: expandedMap });
      saveExpansion(expandedMap);
      if (expanded && navigationStore.getState().mode === "v1") void hydrateProject(projectKey, bootEpoch);
    },
    toggleExpanded: (projectKey: string) => {
      const m = new Map(navigationStore.getState().expanded);
      m.set(projectKey, !(m.get(projectKey) ?? false));
      saveExpansion(m);
      navigationStore.setState({ expanded: m });
      if (m.get(projectKey) && navigationStore.getState().mode === "v1") void hydrateProject(projectKey, bootEpoch);
    },
  };
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
  if (revalidator && revalidator.generationID !== cap.generationId) revalidator.resetGeneration(cap.generationId);
  const previous = navigationStore.getState();
  navigationStore.setState({
    capability: cap,
    mode: "v1",
    clientGenerationID: cap.generationId,
    lastSequence: cap.sequence,
    attention:
      previous.mode === "v1" && previous.clientGenerationID === cap.generationId
        ? previous.attention
        : initialAttention,
  });
  if (bootStartedEpoch === epoch) return;
  bootStartedEpoch = epoch;
  const manifest = await navigationStore
    .getState()
    .loadManifest()
    .catch(() => null);
  if (!manifest || epoch !== bootEpoch || client !== activeClient) return;
  const m = manifest.data;
  if (!m) return;
  if (!isNavigationManifest(m)) {
    navigationStore.setState({ protocolError: new Error("invalid navigation manifest") });
    return;
  }
  const jobs: Array<() => Promise<unknown>> = [];
  if (m.sections.live.count > 0) jobs.push(() => navigationStore.getState().loadSection("live"));
  if (m.sections.needs_you.count > 0) jobs.push(() => navigationStore.getState().loadSection("needs_you"));
  if (m.sections.pin_sections.count > 0) jobs.push(() => navigationStore.getState().loadPinCatalog());
  for (const c of ["projects", "archived_projects", "test_runs"] as const)
    if (m.catalogs[c].count > 0) jobs.push(() => navigationStore.getState().loadCatalog(c));
  await runBounded(jobs, epoch);
  if (epoch !== bootEpoch || activeClient === null) return;
  const pinCatalog = navigationStore
    .getState()
    .resources.get(keyID({ kind: "pin_catalog", offset: 0, limit: CATALOG_LIMIT }));
  const pinSections = (pinCatalog?.data as NavigationPinSectionCatalog | null)?.pin_sections ?? [];
  await runBounded(
    pinSections.filter((p) => p.count > 0).map((p) => () => navigationStore.getState().loadPinSection(p.id)),
    epoch,
  );
  // Hydrate only visible/default-expanded projects. A small explicit worker
  // pool prevents a large catalog from monopolising the browser connection.
  const projects = selectSummaries();
  const pending = projects
    .filter((p) => navigationStore.getState().expanded.get(p.key) ?? p.default_expanded)
    .map((p) => p.key);
  await runBounded(
    pending.map((project) => () => hydrateProject(project, epoch)),
    epoch,
  );
}
async function hydrateProject(projectKey: string, epoch: number): Promise<void> {
  if (epoch !== bootEpoch || navigationStore.getState().mode !== "v1") return;
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
  unsubs.forEach((u) => {
    u();
  });
  unsubs = [];
  activeClient = client;
  bootEpoch++;
  const epoch = bootEpoch;
  const ownedClient = client;
  const start = (info?: InitializeResponse | NavigationCapability | null) => {
    if (ownedClient !== activeClient || epoch !== bootEpoch) return;
    const cap: NavigationCapability | undefined =
      info && "navigation" in info ? info.navigation : info && "version" in info ? info : undefined;
    if (!cap) {
      navigationStore.setState({
        mode: "legacy",
        capability: null,
        clientGenerationID: "",
        lastSequence: 0,
        attention: initialAttention,
      });
      return;
    }
    void boot(cap, epoch, ownedClient);
  };
  revalidator = new NavigationRevalidator();
  unsubs.push(revalidator.subscribe((state) => setResource(state, ownedClient, epoch)));
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
      if (s.mode !== "v1" || p.generationId !== s.clientGenerationID || p.sequence <= s.lastSequence) {
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
      if (ownedClient !== activeClient || epoch !== bootEpoch) return;
      void client
        .connect()
        .then((i) => {
          if (ownedClient !== activeClient || epoch !== bootEpoch) return;
          const cap = i.navigation;
          if (!cap) {
            navigationStore.setState({ mode: "legacy", capability: null, attention: initialAttention });
            return;
          }
          const same = cap.version === 1 && revalidator?.generationID === cap.generationId;
          if (same) {
            navigationStore.setState({
              capability: cap,
              mode: "v1",
              clientGenerationID: cap.generationId,
              lastSequence: cap.sequence,
            });
            revalidator?.force(revalidator.loadedKeys());
          } else start(i);
        })
        .catch(() => {});
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
  navigationStore.setState({ ...initial(), ...actions() });
}
