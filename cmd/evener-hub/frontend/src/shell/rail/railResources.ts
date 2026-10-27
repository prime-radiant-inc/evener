import type { NavigationCatalogs, NavigationProjectSummary, NavigationSessionSummary } from "@evener/appwire-client";
import { errorText } from "@evener/appwire-client";
import {
  keyID,
  type NormalizedResource,
  navigationOwnedContainerKey,
  navigationRootContainerKey,
  nextNavigationOffset,
  type ResourceKey,
  type ResourceState,
} from "@evener/appwire-client/state/navigation";
import { type ArchivedList, archivedListKey } from "../../stores/archivedList";
import { selectPinSections, selectRailModel } from "../../stores/navigation/selectors";
import type { navigationStore } from "../../stores/navigation/store";
import type { RailProject, RailSession } from "./railNodes";
import type { RailResources } from "./railPending";

type CatalogKind = keyof NavigationCatalogs;
const sessionModelCache = new WeakMap<object, Map<string, RailSession>>();
type ProjectPageDependency = Readonly<{
  id: string;
  tier: "current" | "recent" | "archived";
  graph: object;
}>;
type ProjectModelCacheEntry = Readonly<{
  root: object;
  pages: readonly ProjectPageDependency[];
  result: RailProject;
}>;
const projectModelCache = new WeakMap<object, ProjectModelCacheEntry>();
const summaryProjectCache = new WeakMap<object, { loaded: boolean; error: string | undefined; result: RailProject }>();
const archivedProjectModelCache = new WeakMap<object, RailProject>();

// A resource the store decoded from a snapshot or delta. The store attaches the
// graph to every resource it publishes with data, so the rail reads nothing
// else.
type GraphResource = ResourceState & { normalized: NormalizedResource };
function isGraphResource(resource: ResourceState): resource is GraphResource {
  return resource.normalized !== undefined;
}

function summarySession(
  summary: NavigationSessionSummary,
  scope: string,
  tier?: string,
  pinSectionID?: string,
  projectKey?: string,
): RailSession {
  const context = `${scope}\0${tier ?? ""}\0${pinSectionID ?? ""}\0${projectKey ?? ""}`;
  const cached = sessionModelCache.get(summary as object)?.get(context);
  if (cached) return cached;
  const result = {
    ...summary,
    row_id: `navigation:${scope}:${summary.ref}`,
    tier,
    pin_section_id: pinSectionID,
    project_key: projectKey,
    // Archived lists retain separate fork-original conversations. Delegate
    // activity belongs to the scoped activity owner, not navigation rows.
    children:
      tier === "archived"
        ? summary.children
            .filter((child) => child.kind === "fork")
            .map((child) => summarySession(child, scope, tier, pinSectionID, projectKey))
        : [],
  };
  let entries = sessionModelCache.get(summary as object);
  if (!entries) {
    entries = new Map();
    sessionModelCache.set(summary as object, entries);
  }
  entries.set(context, result);
  return result;
}
function sessions(
  summaries: readonly NavigationSessionSummary[],
  scope: string,
  tier?: string,
  pinSectionID?: string,
  projectKey?: string,
): RailSession[] {
  return summaries.map((summary) => summarySession(summary, scope, tier, pinSectionID, projectKey));
}
function dedupeSessions(rows: readonly RailSession[]): RailSession[] {
  const seen = new Set<string>();
  return rows.filter((row) => {
    if (seen.has(row.ref)) return false;
    seen.add(row.ref);
    return true;
  });
}
export function resourceState(
  state: ReturnType<typeof navigationStore.getState>,
  key: ResourceKey,
): ResourceState | undefined {
  return state.resources.get(keyID(key));
}
export function resourceData<T>(state: ReturnType<typeof navigationStore.getState>, key: ResourceKey): T | null {
  const resource = resourceState(state, key);
  if (resource?.normalized?.presence === "gone") return null;
  return (resource?.data as T | undefined) ?? null;
}
function returnedRootRows(resource: GraphResource, slot: string): number {
  return resource.normalized.graph.containers.get(navigationRootContainerKey(resource.key, slot))?.children.length ?? 0;
}
// The tiers a project resource serves rows for. Archived rows come from the
// project's archived list (withArchivedList), not from navigation.
const PROJECT_TIERS = ["current", "recent"] as const;
function projectPageStates(
  pages: ReadonlyMap<string, ResourceState>,
  projectKey: string,
): Array<GraphResource & { key: Extract<ResourceKey, { kind: "project_page" }> }> {
  const tierOrder = { current: 0, recent: 1, archived: 2 } as const;
  return [...pages.values()]
    .filter(
      (state): state is GraphResource & { key: Extract<ResourceKey, { kind: "project_page" }> } =>
        state.key.kind === "project_page" &&
        state.key.projectKey === projectKey &&
        state.data !== null &&
        isGraphResource(state) &&
        state.normalized.presence !== "gone",
    )
    .sort(
      (a, b) =>
        tierOrder[a.key.tier] - tierOrder[b.key.tier] ||
        a.key.offset - b.key.offset ||
        a.key.limit - b.key.limit ||
        keyID(a.key).localeCompare(keyID(b.key)),
    );
}
function projectPageDependencies(states: readonly GraphResource[]): ProjectPageDependency[] {
  return states.flatMap((state) => {
    if (state.key.kind !== "project_page") return [];
    return [{ id: keyID(state.key), tier: state.key.tier, graph: state.normalized.graph as object }];
  });
}
function sameProjectPageDependencies(
  left: readonly ProjectPageDependency[],
  right: readonly ProjectPageDependency[],
): boolean {
  return (
    left.length === right.length &&
    left.every(
      (dependency, index) =>
        dependency.id === right[index]?.id &&
        dependency.tier === right[index]?.tier &&
        dependency.graph === right[index]?.graph,
    )
  );
}
function cachedProject(
  summary: NavigationProjectSummary,
  root: object,
  pages: readonly ProjectPageDependency[],
): RailProject | undefined {
  const cached = projectModelCache.get(summary as object);
  return cached?.root === root && sameProjectPageDependencies(cached.pages, pages) ? cached.result : undefined;
}
function cacheProject(
  summary: NavigationProjectSummary,
  root: object,
  pages: readonly ProjectPageDependency[],
  result: RailProject,
): RailProject {
  projectModelCache.set(summary as object, { root, pages, result });
  return result;
}
interface LoadedSection {
  sessions: RailSession[];
  remaining: number;
  offset: number;
  limit: number;
}
function loadedSection(
  state: ReturnType<typeof navigationStore.getState>,
  section: "live" | "needs_you",
): LoadedSection {
  const pages = [...state.resources.values()]
    .filter(
      (resource): resource is GraphResource =>
        resource.key.kind === "section" &&
        resource.key.section === section &&
        resource.data !== null &&
        isGraphResource(resource) &&
        state.visibleResourceKeys.has(keyID(resource.key)),
    )
    .sort((a, b) =>
      a.key.kind === "section" && b.key.kind === "section"
        ? a.key.offset - b.key.offset || a.key.limit - b.key.limit
        : 0,
    );
  const seen = new Set<string>();
  const rows = pages.flatMap((resource) =>
    graphSessionsForResource(resource).filter((session) => {
      if (seen.has(session.ref)) return false;
      seen.add(session.ref);
      return true;
    }),
  );
  const last = pages.at(-1);
  const data = last?.data as { remaining?: number } | null;
  const pageKey = last?.key.kind === "section" ? last.key : { offset: 0, limit: 50 };
  return {
    sessions: rows,
    remaining: data?.remaining ?? 0,
    offset: nextNavigationOffset(pageKey.offset, last ? returnedRootRows(last, "sessions") : 0),
    limit: pageKey.limit,
  };
}
// The row a project shows when its graph has no rows to offer: the catalog
// summary alone. `loaded` is whether a root read has settled, and `rootError`
// the failure to retry from.
function projectFromSummary(
  summary: NavigationProjectSummary,
  loaded: boolean,
  rootError: string | undefined,
): RailProject {
  const cached = summaryProjectCache.get(summary as object);
  if (cached && cached.loaded === loaded && cached.error === rootError) return cached.result;
  const result = {
    ...summary,
    loaded,
    resourceError: rootError,
    nextOffsets: { current: 0, recent: 0 },
    sessions: [],
    more_current: summary.more_current ?? 0,
    more_recent: summary.more_recent ?? 0,
  };
  summaryProjectCache.set(summary as object, { loaded, error: rootError, result });
  return result;
}
function graphSessionsForResource(resource: GraphResource): RailSession[] {
  const normalized = resource.normalized;
  const model = selectRailModel(normalized);
  const root = normalized.graph.containers.get(navigationRootContainerKey(resource.key, "sessions"));
  return (root?.children ?? []).flatMap((entityKey) => {
    const session = model.sessions.get(entityKey);
    return session ? [session] : [];
  });
}
function projectFromGraph(
  summary: NavigationProjectSummary,
  resource: GraphResource,
  pages: ReadonlyMap<string, ResourceState>,
): RailProject | null {
  const normalized = resource.normalized;
  if (normalized.presence === "gone") return null;
  const allPageStates = projectPageStates(pages, summary.key);
  const pageDependencies = projectPageDependencies(allPageStates);
  const cached = cachedProject(summary, normalized.graph as object, pageDependencies);
  if (cached) return cached;
  const projectEntity = [...normalized.graph.entities.values()].find(
    (entity) =>
      entity.kind === "project" &&
      entity.value !== null &&
      typeof entity.value === "object" &&
      (entity.value as Record<string, unknown>).key === summary.key,
  );
  if (!projectEntity) return null;
  const metadata = normalized.graph.metadata;
  const all: RailSession[] = [];
  const more: Partial<Record<"current" | "recent", number>> = {};
  const nextOffsets: Partial<Record<"current" | "recent" | "archived", number>> = {};
  for (const tier of PROJECT_TIERS) {
    const container = normalized.graph.containers.get(navigationOwnedContainerKey(projectEntity.key, tier));
    const rootSessions = (container?.children ?? []).flatMap((entityKey) => {
      const session = selectRailModel(normalized).sessions.get(entityKey);
      return session ? [session] : [];
    });
    const pageStates = allPageStates.filter((page) => page.key.tier === tier);
    const seen = new Set(rootSessions.map((session) => session.ref));
    const sessions = [...rootSessions];
    for (const page of pageStates) {
      for (const session of graphSessionsForResource(page)) {
        if (seen.has(session.ref)) continue;
        seen.add(session.ref);
        sessions.push(session);
      }
    }
    all.push(...sessions);
    const lastPage = pageStates.at(-1);
    nextOffsets[tier] =
      lastPage?.key.kind === "project_page"
        ? nextNavigationOffset(lastPage.key.offset, returnedRootRows(lastPage, "sessions"))
        : (container?.children.length ?? 0);
    const pageRemaining = pageStates.at(-1)?.data as { remaining?: number } | undefined;
    const metadataRemaining = metadata[`${tier}_remaining`];
    more[tier] = pageRemaining?.remaining ?? (typeof metadataRemaining === "number" ? metadataRemaining : 0);
  }
  return cacheProject(summary, normalized.graph as object, pageDependencies, {
    ...summary,
    loaded: true,
    sessions: all,
    nextOffsets,
    more_current: more.current,
    more_recent: more.recent,
  });
}
function asArchivedProject(project: RailProject): RailProject {
  if (project.is_archived === true) return project;
  const cached = archivedProjectModelCache.get(project as object);
  if (cached) return cached;
  const archived = { ...project, is_archived: true };
  archivedProjectModelCache.set(project as object, archived);
  return archived;
}
// withArchivedList adds a project's loaded archived rows, from its archived
// list, to the rows navigation served, and leaves more_archived as the archived
// rows not loaded yet. The summary's more_archived is the whole archived count;
// once the list has loaded, its own total is the fresher count. Cached per
// built project and list entry so an unchanged pair keeps one identity.
const archivedListProjectCache = new WeakMap<object, { list: ArchivedList | undefined; result: RailProject }>();
function withArchivedList(project: RailProject, catalog: CatalogKind, list: ArchivedList | undefined): RailProject {
  const cached = archivedListProjectCache.get(project);
  if (cached && cached.list === list) return cached.result;
  const rows = list ? sessions(list.rows, `project:${project.key}:archived`, "archived", undefined, project.key) : [];
  const summaryTotal = project.more_archived ?? 0;
  const total = list?.loaded ? list.total : summaryTotal;
  const result = {
    ...project,
    catalog,
    archived_total: summaryTotal,
    // A session unarchived moments ago can be in navigation's rows and still
    // in the list until the list refreshes.
    sessions: dedupeSessions([...project.sessions, ...rows]),
    more_archived: Math.max(0, total - rows.length),
  };
  archivedListProjectCache.set(project, { list, result });
  return result;
}
export function allRailProjects(resources: RailResources): RailProject[] {
  return [...resources.projects, ...resources.archivedProjects, ...resources.testRuns];
}
export function withArchivedLists(
  resources: RailResources,
  lists: Readonly<Record<string, ArchivedList>>,
): RailResources {
  const apply = (projects: readonly RailProject[], catalog: CatalogKind) =>
    projects.map((project) => withArchivedList(project, catalog, lists[archivedListKey(catalog, project.key)]));
  return {
    ...resources,
    projects: apply(resources.projects, "projects"),
    archivedProjects: apply(resources.archivedProjects, "archived_projects"),
    testRuns: apply(resources.testRuns, "test_runs"),
  };
}
function projectsFor(state: ReturnType<typeof navigationStore.getState>, catalog: CatalogKind): RailProject[] {
  const output: RailProject[] = [];
  const catalogResources = [...state.resources.values()]
    .filter(
      (resource): resource is GraphResource =>
        resource.key.kind === "catalog" &&
        resource.key.catalog === catalog &&
        resource.data !== null &&
        isGraphResource(resource) &&
        resource.normalized.presence !== "gone",
    )
    .sort((a, b) =>
      a.key.kind === "catalog" && b.key.kind === "catalog"
        ? a.key.offset - b.key.offset || a.key.limit - b.key.limit
        : 0,
    );
  for (const resource of catalogResources) {
    const root = resource.normalized.graph.containers.get(navigationRootContainerKey(resource.key, "projects"));
    const summaries = (root?.children ?? []).flatMap((entityKey) => {
      const entity = resource.normalized.graph.entities.get(entityKey);
      if (entity?.kind !== "project" || !entity.value || typeof entity.value !== "object") return [];
      return [entity.value as NavigationProjectSummary];
    });
    for (const summary of summaries) {
      if (output.some((project) => project.key === summary.key)) continue;
      const rootState = state.resources.get(keyID({ kind: "project", projectKey: summary.key }));
      const graphProject =
        rootState && isGraphResource(rootState) ? projectFromGraph(summary, rootState, state.resources) : null;
      if (graphProject) {
        output.push(graphProject);
        continue;
      }
      const rootLoaded = !!rootState && rootState.data !== null && rootState.normalized?.presence !== "gone";
      output.push(projectFromSummary(summary, rootLoaded, rootState?.error ? errorText(rootState.error) : undefined));
    }
  }
  return output;
}
function catalogOverflowFor(
  state: ReturnType<typeof navigationStore.getState>,
  catalog: CatalogKind,
): { remaining: number; offset: number; limit: number } | undefined {
  const pages = [...state.resources.values()]
    .filter(
      (resource): resource is GraphResource =>
        resource.key.kind === "catalog" &&
        resource.key.catalog === catalog &&
        resource.data !== null &&
        isGraphResource(resource) &&
        resource.normalized.presence !== "gone",
    )
    .sort((a, b) =>
      a.key.kind === "catalog" && b.key.kind === "catalog"
        ? a.key.offset - b.key.offset || a.key.limit - b.key.limit
        : 0,
    );
  const last = pages.at(-1);
  if (!last) return undefined;
  const remaining = (last.data as { remaining?: number } | null)?.remaining ?? 0;
  if (remaining <= 0) return undefined;
  const pageKey = last.key.kind === "catalog" ? last.key : { offset: 0, limit: 100 };
  return {
    remaining,
    offset: nextNavigationOffset(pageKey.offset, returnedRootRows(last, "projects")),
    limit: pageKey.limit,
  };
}
export function railResources(state: ReturnType<typeof navigationStore.getState>): RailResources {
  const live = loadedSection(state, "live");
  const needsYou = loadedSection(state, "needs_you");
  const pinCatalog = [...state.resources.values()]
    .filter((resource) => resource.key.kind === "pin_catalog" && resource.data !== null)
    .sort((a, b) => (a.key.kind === "pin_catalog" && b.key.kind === "pin_catalog" ? a.key.offset - b.key.offset : 0))
    .flatMap(
      (resource) =>
        (resource.data as { pin_sections: Array<{ id: string; name: string; count: number }> }).pin_sections,
    );
  const pinCounts = new Map<string, number>();
  for (const descriptor of pinCatalog)
    if (!pinCounts.has(descriptor.id)) pinCounts.set(descriptor.id, descriptor.count);
  const pinSections = selectPinSections(state)
    .map((section) => {
      const pages = [...state.resources.values()]
        .filter(
          (resource): resource is GraphResource =>
            resource.key.kind === "pin_section" &&
            resource.key.sectionId === section.id &&
            resource.data !== null &&
            isGraphResource(resource),
        )
        .sort((a, b) =>
          a.key.kind === "pin_section" && b.key.kind === "pin_section"
            ? a.key.offset - b.key.offset || a.key.limit - b.key.limit
            : 0,
        );
      const last = pages.at(-1);
      const pageKey = last?.key.kind === "pin_section" ? last.key : { offset: 0, limit: 50 };
      const remaining = (last?.data as { remaining?: number } | null)?.remaining ?? 0;
      // Until a section's pages load, the manifest's own rows paint it.
      const graphSessions = pages.length
        ? dedupeSessions(pages.flatMap((resource) => graphSessionsForResource(resource)))
        : null;
      return {
        id: section.id,
        name: section.name,
        member_count: pinCounts.get(section.id) ?? section.sessions.length,
        remaining,
        offset: nextNavigationOffset(pageKey.offset, last ? returnedRootRows(last, "sessions") : 0),
        limit: pageKey.limit,
        sessions:
          graphSessions ?? dedupeSessions(sessions(section.sessions, `pin:${section.id}`, undefined, section.id)),
      };
    })
    .filter((section) => section.sessions.length > 0);
  return {
    live: live.sessions,
    needsYou: needsYou.sessions,
    liveOverflow: { remaining: live.remaining, offset: live.offset, limit: live.limit },
    needsYouOverflow: { remaining: needsYou.remaining, offset: needsYou.offset, limit: needsYou.limit },
    pinSections,
    projects: projectsFor(state, "projects"),
    archivedProjects: projectsFor(state, "archived_projects").map(asArchivedProject),
    testRuns: projectsFor(state, "test_runs"),
    catalogOverflow: {
      projects: catalogOverflowFor(state, "projects"),
      archived_projects: catalogOverflowFor(state, "archived_projects"),
      test_runs: catalogOverflowFor(state, "test_runs"),
    },
  };
}
export const adaptNavigationResources = (
  state: ReturnType<typeof navigationStore.getState>,
  lists: Readonly<Record<string, ArchivedList>> = {},
): RailResources => withArchivedLists(railResources(state), lists);
export function nonEmpty(resources: RailResources): boolean {
  return (
    resources.live.length > 0 ||
    resources.needsYou.length > 0 ||
    resources.pinSections.length > 0 ||
    resources.projects.length > 0 ||
    resources.archivedProjects.length > 0 ||
    resources.testRuns.length > 0
  );
}
