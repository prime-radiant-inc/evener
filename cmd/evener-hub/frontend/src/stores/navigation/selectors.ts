import type { NavigationProjectSummary, NavigationSessionSummary } from "../../protocol/types.gen";
import { navigationStore } from "./store";
import {
  isNavigationUnavailable,
  keyID,
  navigationOwnedContainerKey,
  navigationRootContainerKey,
  nextNavigationOffset,
  type ResourceKey,
  type ResourceState,
} from "./types";

export { nextNavigationOffset } from "./types";

function normalizedRootCount(resource: ResourceState, slot: string): number | undefined {
  const normalized = resource.normalized;
  if (!normalized) return undefined;
  return normalized.graph.containers.get(navigationRootContainerKey(resource.key, slot))?.children.length ?? 0;
}
export const selectAttentionSummary = (s: ReturnType<typeof navigationStore.getState>) => s.attention.summary;
export const selectResource = (key: ResourceKey) => (s: ReturnType<typeof navigationStore.getState>) =>
  s.resources.get(keyID(key));
export const selectProjectResource = (projectKey: string) => (s: ReturnType<typeof navigationStore.getState>) =>
  s.resources.get(keyID({ kind: "project", projectKey }));
export const selectProjectPage =
  (projectKey: string, tier: "current" | "recent" | "archived", offset = 0, limit = 50) =>
  (s: ReturnType<typeof navigationStore.getState>) =>
    s.resources.get(keyID({ kind: "project_page", projectKey, tier, offset, limit }));
export const selectLocation = (ref: string) => (s: ReturnType<typeof navigationStore.getState>) =>
  s.resources.get(keyID({ kind: "location", ref }));
export function selectSectionRows(
  section: "live" | "needs_you",
  state = navigationStore.getState(),
): NavigationSessionSummary[] {
  return loadedSectionRows(state, (key) => key.kind === "section" && key.section === section);
}
export function selectNeedsYouRows(state = navigationStore.getState()): NavigationSessionSummary[] {
  return selectSectionRows("needs_you", state);
}
export function selectNeedsYouCount(state = navigationStore.getState()): number {
  return state.manifest?.data?.sections.needs_you.count ?? selectNeedsYouRows(state).length;
}
export function selectSectionRemaining(section: "live" | "needs_you", state = navigationStore.getState()): number {
  const pages = [...state.resources.values()].filter(
    (resource) => resource.key.kind === "section" && resource.key.section === section && resource.data !== null,
  );
  const last = pages
    .sort((a, b) =>
      a.key.kind === "section" && b.key.kind === "section"
        ? a.key.offset - b.key.offset || a.key.limit - b.key.limit
        : 0,
    )
    .at(-1);
  return (last?.data as { remaining?: number } | null)?.remaining ?? 0;
}
export function selectNextSectionOffset(section: "live" | "needs_you", state = navigationStore.getState()): number {
  const pages = [...state.resources.values()].filter(
    (resource) => resource.key.kind === "section" && resource.key.section === section,
  );
  const last = pages
    .sort((a, b) =>
      a.key.kind === "section" && b.key.kind === "section"
        ? a.key.offset - b.key.offset || a.key.limit - b.key.limit
        : 0,
    )
    .at(-1);
  if (last?.key.kind !== "section") return 0;
  const returned =
    normalizedRootCount(last, "sessions") ??
    (last.data as { sessions?: NavigationSessionSummary[] } | null)?.sessions?.length ??
    0;
  return nextNavigationOffset(last.key.offset, returned);
}
export function selectCatalogRemaining(
  catalog: "projects" | "archived_projects" | "test_runs",
  state = navigationStore.getState(),
): number {
  const pages = [...state.resources.values()].filter(
    (resource) => resource.key.kind === "catalog" && resource.key.catalog === catalog && resource.data !== null,
  );
  const last = pages
    .sort((a, b) =>
      a.key.kind === "catalog" && b.key.kind === "catalog"
        ? a.key.offset - b.key.offset || a.key.limit - b.key.limit
        : 0,
    )
    .at(-1);
  return (last?.data as { remaining?: number } | null)?.remaining ?? 0;
}
export function selectNextCatalogOffset(
  catalog: "projects" | "archived_projects" | "test_runs",
  state = navigationStore.getState(),
): number {
  const pages = [...state.resources.values()].filter(
    (resource) => resource.key.kind === "catalog" && resource.key.catalog === catalog,
  );
  const last = pages
    .sort((a, b) =>
      a.key.kind === "catalog" && b.key.kind === "catalog"
        ? a.key.offset - b.key.offset || a.key.limit - b.key.limit
        : 0,
    )
    .at(-1);
  if (last?.key.kind !== "catalog") return 0;
  const returned =
    normalizedRootCount(last, "projects") ??
    (last.data as { projects?: NavigationProjectSummary[] } | null)?.projects?.length ??
    0;
  return nextNavigationOffset(last.key.offset, returned);
}
export function selectLiveRows(state = navigationStore.getState()): NavigationSessionSummary[] {
  return selectSectionRows("live", state);
}
function loadedSectionRows(
  state: ReturnType<typeof navigationStore.getState>,
  predicate: (key: Extract<ResourceKey, { kind: "section" | "pin_section" }>) => boolean,
): NavigationSessionSummary[] {
  const pages: Array<{ offset: number; limit: number; sessions: NavigationSessionSummary[] }> = [];
  for (const resource of state.resources.values()) {
    if (
      (resource.key.kind !== "section" && resource.key.kind !== "pin_section") ||
      !predicate(resource.key) ||
      resource.data === null
    )
      continue;
    pages.push({
      offset: resource.key.offset,
      limit: resource.key.limit,
      sessions: (resource as ResourceState<{ sessions: NavigationSessionSummary[] }>).data?.sessions ?? [],
    });
  }
  const seen = new Set<string>();
  return pages
    .sort((a, b) => a.offset - b.offset || a.limit - b.limit)
    .flatMap((page) => page.sessions.filter((session) => !seen.has(session.ref) && seen.add(session.ref)));
}
export function selectGlobalRows(state = navigationStore.getState()): NavigationSessionSummary[] {
  return [...selectLiveRows(state), ...selectNeedsYouRows(state)];
}
export interface NavigationPinSectionSummary {
  id: string;
  name: string;
  member_count: number;
}
export interface LoadedPinSection extends NavigationPinSectionSummary {
  sessions: NavigationSessionSummary[];
}
export function selectPinSectionSummaries(state = navigationStore.getState()): NavigationPinSectionSummary[] {
  const descriptors = [...state.resources.values()]
    .filter((resource) => resource.key.kind === "pin_catalog" && resource.data !== null)
    .sort((a, b) => {
      const left = a.key.kind === "pin_catalog" ? a.key.offset : 0;
      const right = b.key.kind === "pin_catalog" ? b.key.offset : 0;
      return left - right;
    })
    .flatMap((resource) => {
      const data = resource.data as { pin_sections?: Array<{ id: string; name: string; count: number }> } | null;
      return data?.pin_sections ?? [];
    });
  const seen = new Set<string>();
  return descriptors.flatMap((descriptor) => {
    if (seen.has(descriptor.id)) return [];
    seen.add(descriptor.id);
    return [{ id: descriptor.id, name: descriptor.name, member_count: descriptor.count }];
  });
}
export function selectPinSections(state = navigationStore.getState()): LoadedPinSection[] {
  return selectPinSectionSummaries(state).map((section) => ({
    ...section,
    sessions: loadedSectionRows(state, (key) => key.kind === "pin_section" && key.sectionId === section.id),
  }));
}
export function selectProjectSummaries(state = navigationStore.getState()): NavigationProjectSummary[] {
  const catalogOrder = { projects: 0, archived_projects: 1, test_runs: 2 } as const;
  return [...state.resources.values()]
    .filter((resource) => resource.key.kind === "catalog" && resource.data !== null)
    .sort((a, b) => {
      if (a.key.kind !== "catalog" || b.key.kind !== "catalog") return 0;
      return catalogOrder[a.key.catalog] - catalogOrder[b.key.catalog] || a.key.offset - b.key.offset;
    })
    .flatMap((resource) => {
      const data = resource.data as { projects?: NavigationProjectSummary[] } | null;
      return data?.projects ?? [];
    });
}
export const selectExpanded = (projectKey: string) => (s: ReturnType<typeof navigationStore.getState>) =>
  s.expanded.get(projectKey) ?? selectProjectSummaries(s).find((p) => p.key === projectKey)?.default_expanded ?? false;
export function selectSessionSummary(ref: string, state = navigationStore.getState()): NavigationSessionSummary | null {
  const walk = (xs: NavigationSessionSummary[]): NavigationSessionSummary | null => {
    for (const x of xs) {
      if (x.ref === ref) return x;
      const y = walk(x.children);
      if (y) return y;
    }
    return null;
  };
  const rows = [...selectGlobalRows(state), ...selectPinSections(state).flatMap((section) => section.sessions)];
  for (const resource of state.resources.values()) {
    if (resource.key.kind === "project_page") {
      const data = resource.data as { sessions?: NavigationSessionSummary[] } | null;
      if (data?.sessions) rows.push(...data.sessions);
    }
    if (resource.key.kind === "project") {
      const data = resource.data as {
        current?: { sessions?: NavigationSessionSummary[] };
        recent?: { sessions?: NavigationSessionSummary[] };
        archived?: { sessions?: NavigationSessionSummary[] };
      } | null;
      for (const tier of [data?.current, data?.recent, data?.archived]) if (tier?.sessions) rows.push(...tier.sessions);
    }
    if (resource.key.kind === "location") {
      const data = resource.data as { session?: NavigationSessionSummary } | null;
      if (!isNavigationUnavailable(resource.error) && data?.session) rows.push(data.session);
    }
  }
  return walk(rows);
}
export const findSessionNode = selectSessionSummary;

import type { IsExpanded, RailSession, SessionRailNode } from "../../shell/rail/railNodes";
import type { NormalizedResource } from "./codec";

type NormalizedSessionCacheEntry = Readonly<{
  childContainer: object | undefined;
  children: readonly RailSession[];
  value: RailSession;
}>;
type NormalizedNodeCacheEntry = Readonly<{
  childContainer: object | undefined;
  session: RailSession;
  children: readonly SessionRailNode[];
  expanded: boolean;
  value: SessionRailNode;
}>;

const normalizedSessionCache = new WeakMap<object, NormalizedSessionCacheEntry>();
const normalizedNodeCache = new WeakMap<object, WeakMap<IsExpanded, NormalizedNodeCacheEntry>>();
const normalizedRailModelCache = new WeakMap<object, WeakMap<IsExpanded, NormalizedRailModel>>();
const collapsedNodeLookup: IsExpanded = (_id, defaultExpanded) => defaultExpanded;
export interface NormalizedRailModel {
  readonly sessions: ReadonlyMap<string, RailSession>;
  readonly nodes: ReadonlyMap<string, SessionRailNode>;
}
export function selectRailModel(
  resource: NormalizedResource,
  isExpanded: IsExpanded = collapsedNodeLookup,
): NormalizedRailModel {
  const cachedModel = normalizedRailModelCache.get(resource.graph as object)?.get(isExpanded);
  if (cachedModel) return cachedModel;
  const sessions = new Map<string, RailSession>();
  const nodes = new Map<string, SessionRailNode>();
  const sameIdentities = (left: readonly unknown[], right: readonly unknown[]) =>
    left.length === right.length && left.every((item, index) => item === right[index]);
  const buildSession = (entityKey: string, stack = new Set<string>()): RailSession | undefined => {
    if (stack.has(entityKey)) return undefined;
    const alreadyBuilt = sessions.get(entityKey);
    if (alreadyBuilt) return alreadyBuilt;
    const entity = resource.graph.entities.get(entityKey);
    if (entity?.kind !== "session" || !entity.value || typeof entity.value !== "object") return undefined;
    const value = entity.value as Record<string, unknown>;
    const nextStack = new Set(stack).add(entityKey);
    const childContainer = resource.graph.containers.get(navigationOwnedContainerKey(entityKey, "children"));
    const children = (childContainer?.children ?? []).flatMap((child) => {
      const item = buildSession(child, nextStack);
      return item ? [item] : [];
    });
    const cached = normalizedSessionCache.get(entity as object);
    if (cached && cached.childContainer === childContainer && sameIdentities(cached.children, children)) {
      sessions.set(entityKey, cached.value);
      return cached.value;
    }
    const frozenChildren = Object.freeze(children);
    const session = Object.freeze({
      ...(value as unknown as RailSession),
      row_id: entity.key,
      children: frozenChildren,
    }) as unknown as RailSession;
    normalizedSessionCache.set(
      entity as object,
      Object.freeze({ childContainer, children: frozenChildren, value: session }),
    );
    sessions.set(entityKey, session);
    return session;
  };
  for (const entity of resource.graph.entities.values()) buildSession(entity.key);
  const buildNode = (entityKey: string, stack = new Set<string>()): SessionRailNode | undefined => {
    if (stack.has(entityKey)) return undefined;
    const alreadyBuilt = nodes.get(entityKey);
    if (alreadyBuilt) return alreadyBuilt;
    const entity = resource.graph.entities.get(entityKey);
    if (entity?.kind !== "session") return undefined;
    const session = buildSession(entityKey, stack);
    if (!session) return undefined;
    const nextStack = new Set(stack).add(entityKey);
    const childContainer = resource.graph.containers.get(navigationOwnedContainerKey(entityKey, "children"));
    const children = (childContainer?.children ?? []).flatMap((child) => {
      const childNode = buildNode(child, nextStack);
      return childNode ? [childNode] : [];
    });
    const cacheEntries = normalizedNodeCache.get(entity as object);
    const cached = cacheEntries?.get(isExpanded);
    const expanded = isExpanded(entity.key, false);
    let node: SessionRailNode;
    if (
      cached &&
      cached.session === session &&
      cached.childContainer === childContainer &&
      cached.expanded === expanded &&
      sameIdentities(cached.children, children)
    ) {
      node = cached.value;
    } else {
      const frozenChildren = Object.freeze(children);
      node = Object.freeze({
        id: entity.key,
        kind: "session" as const,
        session,
        expanded,
        children: frozenChildren,
      }) as unknown as SessionRailNode;
      const entries = cacheEntries ?? new WeakMap<IsExpanded, NormalizedNodeCacheEntry>();
      entries.set(
        isExpanded,
        Object.freeze({ childContainer, session, children: frozenChildren, expanded, value: node }),
      );
      if (!cacheEntries) normalizedNodeCache.set(entity as object, entries);
    }
    nodes.set(entityKey, node);
    return node;
  };
  for (const entity of resource.graph.entities.values()) buildNode(entity.key);
  const model = Object.freeze({ sessions, nodes });
  let models = normalizedRailModelCache.get(resource.graph as object);
  if (!models) {
    models = new WeakMap();
    normalizedRailModelCache.set(resource.graph as object, models);
  }
  models.set(isExpanded, model);
  return model;
}
