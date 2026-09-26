// The web's navigation selectors: the package's graph-shaped selectors,
// re-exported unchanged, plus the two kinds that cannot leave the browser -
// the session-watch identity cache (the one impure selector here: it mutates
// a cross-call Map to keep a narrow subscription from re-rendering on
// unrelated navigation churn), and the rail model, typed by the rail's own
// node shapes (shell/rail/railNodes.ts), which never enters the package.
import type { NavigationSessionSummary } from "@evener/appwire-client";
import {
  type NavigationStoreState,
  type NormalizedResource,
  navigationOwnedContainerKey,
  navigationViewScope,
  type ResourceKey,
  relativeAge,
  selectSessionSummary,
} from "@evener/appwire-client/state/navigation";
import type { IsExpanded, RailSession, SessionRailNode } from "../../shell/rail/railNodes";
import { navigationStore } from "./store";

export {
  findSessionNode,
  type NavigationPinSectionSummary,
  type NavigationStoreState,
  selectAttentionSummary,
  selectDisplaySources,
  selectExpanded,
  selectGlobalRows,
  selectLiveRows,
  selectLocation,
  selectNeedsYouCount,
  selectNeedsYouRows,
  selectNextSectionOffset,
  selectPinSectionSummaries,
  selectPinSections,
  selectProjectPage,
  selectProjectResource,
  selectProjectSummaries,
  selectSectionRemaining,
  selectSessionOmittedArmedWatches,
  selectSessionOmittedWatches,
  selectSources,
} from "@evener/appwire-client/state/navigation";
export { relativeAge, selectSessionSummary };

type SessionWatchesCacheEntry = Readonly<{ key: string; watches: NavigationSessionSummary["watches"] }>;
// One entry per session ref, holding the LAST result's content key and the
// exact array reference returned for it. A selector consumer comparing with
// Object.is (zustand's default) therefore re-renders only when the rendered
// content actually changes; a navigation update that rebuilds an unrelated
// session's summary leaves this session's array reference intact.
//
// The cache is bounded so a long-lived page cannot accumulate one entry per
// distinct session ref it has ever seen. The limit is well above a session
// list, and Map preserves insertion order, so exceeding it evicts the oldest
// insertion. Eviction only costs a recomputation (the next read misses and
// stores a fresh array); it never changes correctness, because the content key
// still decides what is returned.
const sessionWatchesCache = new Map<string, SessionWatchesCacheEntry>();
const sessionWatchesCacheLimit = 256;

/** Test-only: the number of cached session-watch entries. App code never calls
 * this. */
export function sessionWatchesCacheSizeForTests(): number {
  return sessionWatchesCache.size;
}

/** Test-only: drop every cached session-watch entry. App code never calls
 * this. */
export function resetSessionWatchesCacheForTests(): void {
  sessionWatchesCache.clear();
}

/** The watches on `ref`'s session summary, or undefined when the session is not
 * currently materialized. The returned array keeps its identity while its JSON
 * content is unchanged, so a narrow subscription does not fire on unrelated
 * navigation churn. */
export function selectSessionWatches(
  ref: string,
  state: NavigationStoreState = navigationStore.getState(),
): NavigationSessionSummary["watches"] {
  const summary = selectSessionSummary(ref, state);
  if (summary === null) {
    sessionWatchesCache.delete(ref);
    return undefined;
  }
  const watches = summary.watches;
  const key = watches === undefined ? "" : JSON.stringify(watches);
  const cached = sessionWatchesCache.get(ref);
  if (cached && cached.key === key) return cached.watches;
  sessionWatchesCache.set(ref, { key, watches });
  while (sessionWatchesCache.size > sessionWatchesCacheLimit) {
    const oldest = sessionWatchesCache.keys().next().value;
    if (oldest === undefined) break;
    sessionWatchesCache.delete(oldest);
  }
  return watches;
}

type NormalizedSessionCacheEntry = Readonly<{
  childContainer: object | undefined;
  children: readonly RailSession[];
  contextKey: string;
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
const normalizedRailModelCache = new WeakMap<object, WeakMap<IsExpanded, Map<string, NormalizedRailModel>>>();
const collapsedNodeLookup: IsExpanded = (_id, defaultExpanded) => defaultExpanded;
export interface NormalizedRailModel {
  readonly sessions: ReadonlyMap<string, RailSession>;
  readonly nodes: ReadonlyMap<string, SessionRailNode>;
}
export function selectRailModel(
  resource: NormalizedResource,
  isExpanded: IsExpanded = collapsedNodeLookup,
): NormalizedRailModel {
  const resourceContext = normalizedSessionContext(resource.key);
  const contextKey = `${navigationViewScope(resource.key)}\0${normalizedSessionContextKey(resourceContext)}`;
  const cachedModel = normalizedRailModelCache
    .get(resource.graph as object)
    ?.get(isExpanded)
    ?.get(contextKey);
  if (cachedModel) return cachedModel;
  const sessions = new Map<string, RailSession>();
  const nodes = new Map<string, SessionRailNode>();
  const entityContexts = new Map<string, NormalizedSessionContext>();
  if (resource.key.kind === "project") {
    const projectKey = resource.key.projectKey;
    const projectEntity = [...resource.graph.entities.values()].find(
      (entity) =>
        entity.kind === "project" &&
        entity.value !== null &&
        typeof entity.value === "object" &&
        (entity.value as Record<string, unknown>).key === projectKey,
    );
    const visit = (entityKey: string, context: NormalizedSessionContext): void => {
      entityContexts.set(entityKey, context);
      const children =
        resource.graph.containers.get(navigationOwnedContainerKey(entityKey, "children"))?.children ?? [];
      for (const child of children) visit(child, context);
    };
    if (projectEntity) {
      for (const tier of ["current", "recent", "archived"] as const) {
        const children =
          resource.graph.containers.get(navigationOwnedContainerKey(projectEntity.key, tier))?.children ?? [];
        const context = { ...resourceContext, tier };
        for (const child of children) visit(child, context);
      }
    }
  }
  const sameIdentities = (left: readonly unknown[], right: readonly unknown[]) =>
    left.length === right.length && left.every((item, index) => item === right[index]);
  const buildSession = (
    entityKey: string,
    stack = new Set<string>(),
    context = entityContexts.get(entityKey) ?? resourceContext,
  ): RailSession | undefined => {
    if (stack.has(entityKey)) return undefined;
    const alreadyBuilt = sessions.get(entityKey);
    if (alreadyBuilt) return alreadyBuilt;
    const entity = resource.graph.entities.get(entityKey);
    if (entity?.kind !== "session" || !entity.value || typeof entity.value !== "object") return undefined;
    const value = entity.value as Record<string, unknown>;
    const nextStack = new Set(stack).add(entityKey);
    const childContainer = resource.graph.containers.get(navigationOwnedContainerKey(entityKey, "children"));
    const children = (childContainer?.children ?? []).flatMap((child) => {
      const item = buildSession(child, nextStack, entityContexts.get(child) ?? context);
      return item ? [item] : [];
    });
    const cached = normalizedSessionCache.get(entity as object);
    if (
      cached &&
      cached.childContainer === childContainer &&
      cached.contextKey === normalizedSessionContextKey(context) &&
      sameIdentities(cached.children, children)
    ) {
      sessions.set(entityKey, cached.value);
      return cached.value;
    }
    const frozenChildren = Object.freeze(children);
    const session = Object.freeze({
      ...(value as unknown as RailSession),
      ...context,
      row_id: entity.key,
      children: frozenChildren,
    }) as unknown as RailSession;
    normalizedSessionCache.set(
      entity as object,
      Object.freeze({
        childContainer,
        children: frozenChildren,
        contextKey: normalizedSessionContextKey(context),
        value: session,
      }),
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
    const session = buildSession(entityKey, stack, entityContexts.get(entityKey) ?? resourceContext);
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
  let contextModels = models.get(isExpanded);
  if (!contextModels) {
    contextModels = new Map();
    models.set(isExpanded, contextModels);
  }
  contextModels.set(contextKey, model);
  return model;
}

type NormalizedSessionContext = Readonly<Pick<RailSession, "tier" | "project_key" | "pin_section_id">>;

function normalizedSessionContext(key: ResourceKey): NormalizedSessionContext {
  switch (key.kind) {
    case "section":
      return { tier: key.section };
    case "project":
      return { project_key: key.projectKey };
    case "project_page":
      return { project_key: key.projectKey, tier: key.tier };
    case "pin_section":
      return { pin_section_id: key.sectionId };
    default:
      return {};
  }
}

function normalizedSessionContextKey(context: NormalizedSessionContext): string {
  return `${context.tier ?? ""}\0${context.project_key ?? ""}\0${context.pin_section_id ?? ""}`;
}
