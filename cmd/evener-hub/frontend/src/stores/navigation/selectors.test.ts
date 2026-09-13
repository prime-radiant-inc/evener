import { expect, test } from "vitest";
import type { NavigationSessionSummary, NavigationWatchSummary } from "../../protocol/types.gen";
import { type NormalizedResource, normalizedGraphFromSnapshot } from "./codec";
import {
  relativeAge,
  resetSessionWatchesCacheForTests,
  selectRailModel,
  selectSessionWatches,
  sessionWatchesCacheSizeForTests,
} from "./selectors";
import { navigationStore } from "./store";
import {
  isSettledGone,
  keyID,
  navigationOwnedContainerKey,
  navigationRootContainerKey,
  navigationViewScope,
  type ResourceKey,
  type ResourceState,
} from "./types";

test.each([
  [
    { kind: "project_page", projectKey: "项目/a|b", tier: "recent", offset: 2, limit: 7 },
    "nav2/project_page///6aG555uuL2F8Yg/cmVjZW50/2/7",
  ],
  [{ kind: "location", ref: "源/α:β|?" }, "nav2/location/5rqQL86xOs6yfD8////0/0"],
  [
    { kind: "pin_section", sectionId: "pins/研发|?", offset: 3, limit: 11 },
    "nav2/pin_section//cGlucy_noJTlj5F8Pw///3/11",
  ],
  [{ kind: "section", section: "live", offset: 4, limit: 0 }, "nav2/live/////4/50"],
  [
    { kind: "project_page", projectKey: "project", tier: "current", offset: 5, limit: 51 },
    "nav2/project_page///cHJvamVjdA/Y3VycmVudA/5/50",
  ],
  [{ kind: "pin_catalog", offset: 6, limit: 0 }, "nav2/pin_catalog/////6/100"],
  [{ kind: "catalog", catalog: "projects", offset: 7, limit: 101 }, "nav2/projects/////7/100"],
] as const)("navigation view scope matches Go parity vector %#", (key, expected) => {
  expect(navigationViewScope(key as ResourceKey)).toBe(expected);
});

const key = { kind: "section", section: "live", offset: 0, limit: 50 } as const;
const firstEntityKey = `${navigationViewScope(key)}/entity/${"1".repeat(64)}`;
const secondEntityKey = `${navigationViewScope(key)}/entity/${"2".repeat(64)}`;
const resource = (title: string): NormalizedResource => {
  const snapshot = {
    metadata: {},
    entities: [
      { key: firstEntityKey, kind: "session", value: { ref: "s1", title, children: [] } },
      { key: secondEntityKey, kind: "session", value: { ref: "s2", title: "two", children: [] } },
    ],
    containers: [
      {
        key: navigationRootContainerKey(key, "sessions"),
        owner: { kind: "resource_root", slot: "sessions" },
        children: [firstEntityKey, secondEntityKey],
      },
    ],
  };
  return {
    key,
    graph: normalizedGraphFromSnapshot(snapshot),
    version: { generationId: "g", revision: 1, etag: "tag" },
    presence: "present",
  };
};
test("normalized selector preserves unchanged entity and node identity", () => {
  const state = resource("one");
  const before = selectRailModel(state);
  const after = selectRailModel(state);
  expect(after.sessions.get(secondEntityKey)).toBe(before.sessions.get(secondEntityKey));
  expect(after.nodes.get(secondEntityKey)).toBe(before.nodes.get(secondEntityKey));
});

test("normalized section sessions preserve the section tier like the legacy adapter", () => {
  const model = selectRailModel(resource("one"));
  expect(model.sessions.get(firstEntityKey)?.tier).toBe("live");
  expect(model.sessions.get(secondEntityKey)?.tier).toBe("live");
});

test("normalized rail sessions derive display age from updated_at", () => {
  const updatedAt = new Date(Date.now() - 90 * 60 * 1000).toISOString();
  const snapshot = {
    metadata: {},
    entities: [
      { key: firstEntityKey, kind: "session", value: { ref: "s1", title: "one", updated_at: updatedAt, children: [] } },
    ],
    containers: [
      {
        key: navigationRootContainerKey(key, "sessions"),
        owner: { kind: "resource_root", slot: "sessions" },
        children: [firstEntityKey],
      },
    ],
  };
  const model = selectRailModel({
    key,
    graph: normalizedGraphFromSnapshot(snapshot),
    version: { generationId: "g", revision: 1, etag: "tag" },
    presence: "present",
  });
  expect(model.sessions.get(firstEntityKey)?.age).toBe("1h");
  expect(relativeAge(undefined)).toBeUndefined();
  expect(relativeAge("not-a-timestamp")).toBeUndefined();
});

test("rail materialization is bottom-up, immutable, and preserves unrelated node arrays", () => {
  const keys = {
    grandchild: `${navigationViewScope(key)}/entity/${"3".repeat(64)}`,
    child: `${navigationViewScope(key)}/entity/${"2".repeat(64)}`,
    parent: `${navigationViewScope(key)}/entity/${"1".repeat(64)}`,
    siblingChild: `${navigationViewScope(key)}/entity/${"5".repeat(64)}`,
    sibling: `${navigationViewScope(key)}/entity/${"4".repeat(64)}`,
  };
  const entity = (entityKey: string, ref: string, title: string) =>
    Object.freeze({
      key: entityKey,
      kind: "session",
      value: Object.freeze({ ref, title, children: Object.freeze([]) }),
    });
  const entities = new Map([
    [keys.grandchild, entity(keys.grandchild, "grandchild", "Grandchild")],
    [keys.child, entity(keys.child, "child", "Child")],
    [keys.parent, entity(keys.parent, "parent", "Parent")],
    [keys.siblingChild, entity(keys.siblingChild, "sibling-child", "Sibling child")],
    [keys.sibling, entity(keys.sibling, "sibling", "Sibling")],
  ]);
  const containers = new Map(
    [
      [keys.grandchild, []],
      [keys.child, [keys.grandchild]],
      [keys.parent, [keys.child]],
      [keys.siblingChild, []],
      [keys.sibling, [keys.siblingChild]],
    ].map(([ownerKey, children]) => {
      const container = Object.freeze({
        key: navigationOwnedContainerKey(ownerKey as string, "children"),
        owner: Object.freeze({ kind: "entity" as const, entityKey: ownerKey as string, slot: "children" }),
        children: Object.freeze(children as string[]),
      });
      return [container.key, container] as const;
    }),
  );
  const graph = Object.freeze({ metadata: Object.freeze({}), entities, containers });
  const initial: NormalizedResource = Object.freeze({
    key,
    graph,
    version: Object.freeze({ generationId: "g", revision: 1, etag: "one" }),
    presence: "present",
  });
  const before = selectRailModel(initial);
  const repeated = selectRailModel(initial);

  const changedEntities = new Map(entities);
  changedEntities.set(keys.grandchild, entity(keys.grandchild, "grandchild", "Changed grandchild"));
  const changed: NormalizedResource = Object.freeze({
    ...initial,
    graph: Object.freeze({ ...graph, entities: changedEntities }),
    version: Object.freeze({ generationId: "g", revision: 2, etag: "two" }),
  });
  const after = selectRailModel(changed);

  for (const entityKey of [keys.grandchild, keys.child, keys.parent]) {
    expect(after.sessions.get(entityKey)).not.toBe(before.sessions.get(entityKey));
    expect(after.nodes.get(entityKey)).not.toBe(before.nodes.get(entityKey));
  }
  expect(after.sessions.get(keys.grandchild)?.title).toBe("Changed grandchild");
  expect(before.sessions.get(keys.grandchild)?.title).toBe("Grandchild");
  expect(after.sessions.get(keys.sibling)).toBe(before.sessions.get(keys.sibling));
  expect(after.nodes.get(keys.sibling)).toBe(before.nodes.get(keys.sibling));
  expect(after.nodes.get(keys.sibling)?.children).toBe(before.nodes.get(keys.sibling)?.children);
  expect(Object.isFrozen(after.sessions.get(keys.parent))).toBe(true);
  expect(Object.isFrozen(after.sessions.get(keys.parent)?.children)).toBe(true);
  expect(Object.isFrozen(after.nodes.get(keys.parent))).toBe(true);
  expect(Object.isFrozen(after.nodes.get(keys.parent)?.children)).toBe(true);
  expect(repeated).toBe(before);
  expect(repeated.sessions.get(keys.parent)).toBe(before.sessions.get(keys.parent));
  expect(repeated.sessions.get(keys.parent)?.children).toBe(before.sessions.get(keys.parent)?.children);
  expect(repeated.nodes.get(keys.parent)).toBe(before.nodes.get(keys.parent));
  expect(repeated.nodes.get(keys.parent)?.children).toBe(before.nodes.get(keys.parent)?.children);
});

test("repeated rail selection preserves the model and nested arrays", () => {
  const state = resource("one");
  const before = selectRailModel(state);
  const after = selectRailModel(state);

  expect(after).toBe(before);
  expect(after.sessions.get(firstEntityKey)).toBe(before.sessions.get(firstEntityKey));
  expect(after.sessions.get(firstEntityKey)?.children).toBe(before.sessions.get(firstEntityKey)?.children);
  expect(after.nodes.get(firstEntityKey)).toBe(before.nodes.get(firstEntityKey));
  expect(after.nodes.get(firstEntityKey)?.children).toBe(before.nodes.get(firstEntityKey)?.children);
});

test("normalized node memo includes the expansion lookup identity", () => {
  const state = resource("one");
  const collapsed = (_id: string, defaultExpanded: boolean) => defaultExpanded;
  const before = selectRailModel(state, collapsed);
  const repeated = selectRailModel(state, collapsed);
  const expanded = selectRailModel(state, (id, defaultExpanded) => (id === firstEntityKey ? true : defaultExpanded));

  expect(repeated).toBe(before);
  expect(repeated.nodes.get(firstEntityKey)).toBe(before.nodes.get(firstEntityKey));
  expect(expanded).not.toBe(before);
  expect(expanded.nodes.get(firstEntityKey)).not.toBe(before.nodes.get(firstEntityKey));
  expect(expanded.nodes.get(firstEntityKey)?.expanded).toBe(true);
});

function tombstone(stale: boolean): ResourceState {
  return {
    key,
    data: null,
    loadedRevision: 2,
    targetRevision: null,
    forceToken: 0,
    etag: '"gone"',
    loading: false,
    stale,
    error: null,
    generationID: stale ? "generation_next" : "generation_test",
    normalized: {
      key,
      graph: normalizedGraphFromSnapshot({ metadata: {}, entities: [], containers: [] }),
      version: { generationId: "generation_test", revision: 2, etag: '"gone"' },
      presence: "gone",
    },
  };
}

test("a settled gone tombstone counts, a stale retained one does not", () => {
  expect(isSettledGone(tombstone(false))).toBe(true);
  expect(isSettledGone(tombstone(true))).toBe(false);
  expect(isSettledGone(undefined)).toBe(false);
  expect(isSettledGone(null)).toBe(false);
});

function watchesState(title: string, watches: NavigationWatchSummary[]): ReturnType<typeof navigationStore.getState> {
  const sectionKey = { kind: "section", section: "live", offset: 0, limit: 50 } as const;
  const summary = {
    ref: "local:s",
    host_id: "local",
    session_id: "s",
    title,
    project: "p",
    state: "idle",
    kind: "session",
    live: true,
    watches,
    children: [],
  } as unknown as NavigationSessionSummary;
  const resource: ResourceState = {
    key: sectionKey,
    data: { sessions: [summary] },
    loadedRevision: 1,
    targetRevision: 1,
    forceToken: 0,
    etag: "tag",
    loading: false,
    stale: false,
    error: null,
    generationID: "generation_test",
  };
  return { ...navigationStore.getState(), resources: new Map([[keyID(sectionKey), resource]]) };
}

// SessionPanelPane selects its watches through this helper so it re-renders
// only when its OWN session's watch content changes. That contract is array
// identity: unrelated navigation churn must return the same reference, and a
// real change must return a new one.
test("selectSessionWatches keeps identity across unrelated navigation churn", () => {
  const watchRow: NavigationWatchSummary = {
    id: "w",
    source: "self",
    deliveries: 1,
    created_at: "2026-09-12T10:00:00Z",
    active: true,
  };
  const first = selectSessionWatches("local:s", watchesState("one", [watchRow]));
  // A different title is navigation churn for an unrelated field: the watches
  // are the same content, so the result must keep its identity.
  const second = selectSessionWatches("local:s", watchesState("two", [watchRow]));
  expect(second).toBe(first);

  const changed = selectSessionWatches("local:s", watchesState("two", [{ ...watchRow, deliveries: 2 }]));
  expect(changed).not.toBe(first);
  expect(changed?.[0]?.deliveries).toBe(2);
});

function refWatchesState(ref: string, watches: NavigationWatchSummary[]): ReturnType<typeof navigationStore.getState> {
  const sectionKey = { kind: "section", section: "live", offset: 0, limit: 50 } as const;
  const summary = {
    ref,
    host_id: "local",
    session_id: ref,
    title: ref,
    project: "p",
    state: "idle",
    kind: "session",
    live: true,
    watches,
    children: [],
  } as unknown as NavigationSessionSummary;
  const resource: ResourceState = {
    key: sectionKey,
    data: { sessions: [summary] },
    loadedRevision: 1,
    targetRevision: 1,
    forceToken: 0,
    etag: "tag",
    loading: false,
    stale: false,
    error: null,
    generationID: "generation_test",
  };
  return { ...navigationStore.getState(), resources: new Map([[keyID(sectionKey), resource]]) };
}

// A ref that drops out of the session list must not keep a cache entry for the
// life of the page. Deleting the entry also drops its old array, so the next
// read recomputes instead of serving a stale identity.
test("selectSessionWatches drops the entry when the session summary vanishes", () => {
  const ref = "local:cache-vanished";
  const watchRow: NavigationWatchSummary = {
    id: "w",
    source: "self",
    deliveries: 1,
    created_at: "2026-09-12T10:00:00Z",
    active: true,
  };
  resetSessionWatchesCacheForTests();
  expect(selectSessionWatches(ref, refWatchesState(ref, [watchRow]))).toEqual([watchRow]);
  expect(sessionWatchesCacheSizeForTests()).toBe(1);

  expect(selectSessionWatches(ref, refWatchesState("local:cache-elsewhere", [watchRow]))).toBeUndefined();
  expect(sessionWatchesCacheSizeForTests()).toBe(0);
});

// The cache is bounded, and Map preserves insertion order, so exceeding the cap
// evicts the oldest insertion. That only costs a recomputation, never
// correctness.
test("selectSessionWatches bounds its cache by evicting the oldest insertion", () => {
  const watchRow: NavigationWatchSummary = {
    id: "w",
    source: "self",
    deliveries: 1,
    created_at: "2026-09-12T10:00:00Z",
    active: true,
  };
  resetSessionWatchesCacheForTests();
  const oldestRef = "local:cache-oldest";
  const first = selectSessionWatches(oldestRef, refWatchesState(oldestRef, [watchRow]));
  for (let i = 0; i <= 256; i++) {
    const ref = `local:cache-cap-${i}`;
    selectSessionWatches(ref, refWatchesState(ref, [watchRow]));
  }
  expect(sessionWatchesCacheSizeForTests()).toBeLessThanOrEqual(256);
  const reread = selectSessionWatches(oldestRef, refWatchesState(oldestRef, [watchRow]));
  expect(reread).not.toBe(first);
});
