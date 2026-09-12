import { expect, test } from "vitest";
import { type NormalizedResource, normalizedGraphFromSnapshot } from "./codec";
import { applyDelta, reconcileSnapshot } from "./merge";
import {
  NavigationBaseInvalidError,
  navigationOwnedContainerKey,
  navigationRootContainerKey,
  navigationViewScope,
} from "./types";

const key = { kind: "section", section: "live", offset: 0, limit: 50 } as const;
const version1 = { generationId: "g", revision: 1, etag: "tag-1" };
const version2 = { generationId: "g", revision: 2, etag: "tag-2" };
const scopedKey = (digit: string) => `${navigationViewScope(key)}/entity/${digit.repeat(64)}`;
const value = (ref: string) => ({
  ref,
  host_id: "local",
  session_id: ref.slice(ref.indexOf(":") + 1),
  title: "Session",
  project: "project",
  state: "idle",
  kind: "session",
  live: false,
  children: [],
});
const make = (children: string[]): NormalizedResource => {
  const snapshot = {
    metadata: { generation_id: "g", revision: 1, offset: 0, limit: 50, remaining: 0, truncated: false },
    entities: children.map((item, index) => ({
      key: item,
      kind: "session",
      value: value(`local:s${index + 1}`),
    })),
    containers: [
      {
        key: navigationRootContainerKey(key, "sessions"),
        owner: { kind: "resource_root", slot: "sessions" },
        children,
      },
      ...children.map((item) => ({
        key: navigationOwnedContainerKey(item, "children"),
        owner: { kind: "entity", entityKey: item, slot: "children" },
        children: [] as string[],
      })),
    ],
  };
  return {
    key,
    graph: normalizedGraphFromSnapshot(snapshot),
    version: version1,
    presence: "present",
  };
};

test("equal snapshot fallback preserves graph and entity identity", () => {
  const first = make([scopedKey("1"), scopedKey("2")]);
  const equal = make([scopedKey("1"), scopedKey("2")]);
  const merged = reconcileSnapshot(first, equal);
  expect(merged).not.toBe(first);
  expect(merged.graph).toBe(first.graph);
  expect(merged.graph.entities).toBe(first.graph.entities);
  expect(merged.graph.containers).toBe(first.graph.containers);
  expect(merged.graph.entities.get(scopedKey("1"))).toBe(first.graph.entities.get(scopedKey("1")));
  expect(merged.graph.containers.get(navigationRootContainerKey(key, "sessions"))).toBe(
    first.graph.containers.get(navigationRootContainerKey(key, "sessions")),
  );
});

test("equal delta upserts preserve entity container map and graph identity", () => {
  const entityKey = scopedKey("1");
  const before = make([entityKey]);
  const entity = before.graph.entities.get(entityKey);
  const containerKey = navigationOwnedContainerKey(entityKey, "children");
  expect(entity).toBeTruthy();

  const after = applyDelta(
    before,
    {
      upsertedEntities: [
        {
          key: entityKey,
          kind: "session",
          value: {
            children: [],
            live: false,
            kind: "session",
            state: "idle",
            project: "project",
            title: "Session",
            session_id: "s1",
            host_id: "local",
            ref: "local:s1",
          },
        },
      ],
      removedEntityKeys: [],
      upsertedContainers: [
        {
          key: containerKey,
          owner: { slot: "children", entityKey, kind: "entity" },
          children: [],
        },
      ],
      removedContainerKeys: [],
    },
    version1,
  );

  expect(after).not.toBe(before);
  expect(after.graph).toBe(before.graph);
  expect(after.graph.entities).toBe(before.graph.entities);
  expect(after.graph.containers).toBe(before.graph.containers);
  expect(after.graph.entities.get(entityKey)).toBe(entity);
  expect(after.graph.containers.get(containerKey)).toBe(before.graph.containers.get(containerKey));
});

test("invalid delta preserves the underlying merge cause", () => {
  const cause = new TypeError("merge sentinel");
  let thrown: unknown;
  try {
    applyDelta(
      make([scopedKey("1")]),
      {
        get upsertedEntities(): never {
          throw cause;
        },
        removedEntityKeys: [],
        upsertedContainers: [],
        removedContainerKeys: [],
      },
      version2,
    );
  } catch (error) {
    thrown = error;
  }

  expect(thrown).toBeInstanceOf(NavigationBaseInvalidError);
  expect((thrown as NavigationBaseInvalidError).cause).toBe(cause);
});

test("one changed entity clones only the entity map", () => {
  const firstKey = scopedKey("1");
  const secondKey = scopedKey("2");
  const before = make([firstKey, secondKey]);
  const incomingValue = { ...value("local:s1"), title: "Changed" };
  const after = applyDelta(
    before,
    {
      upsertedEntities: [{ key: firstKey, kind: "session", value: incomingValue }],
      removedEntityKeys: [],
      upsertedContainers: [],
      removedContainerKeys: [],
    },
    version1,
  );

  expect(after.graph).not.toBe(before.graph);
  expect(after.graph.entities).not.toBe(before.graph.entities);
  expect(after.graph.containers).toBe(before.graph.containers);
  expect(after.graph.metadata).toBe(before.graph.metadata);
  expect(after.graph.entities.get(secondKey)).toBe(before.graph.entities.get(secondKey));
  expect(after.graph.entities.get(firstKey)).not.toBe(before.graph.entities.get(firstKey));
  expect(after.graph.entities.get(firstKey)?.value).not.toBe(incomingValue);
});

test("one changed container clones only the container map", () => {
  const firstKey = scopedKey("1");
  const secondKey = scopedKey("2");
  const before = make([firstKey, secondKey]);
  const rootKey = navigationRootContainerKey(key, "sessions");
  const after = applyDelta(
    before,
    {
      upsertedEntities: [],
      removedEntityKeys: [],
      upsertedContainers: [
        {
          key: rootKey,
          owner: { kind: "resource_root", slot: "sessions" },
          children: [secondKey, firstKey],
        },
      ],
      removedContainerKeys: [],
    },
    version1,
  );

  expect(after.graph).not.toBe(before.graph);
  expect(after.graph.entities).toBe(before.graph.entities);
  expect(after.graph.containers).not.toBe(before.graph.containers);
  expect(after.graph.metadata).toBe(before.graph.metadata);
});

test("version-only changes create a resource wrapper and preserve the graph", () => {
  const before = make([scopedKey("1")]);
  const incomingVersion = { ...version1, etag: "replacement-tag" };
  const after = applyDelta(
    before,
    {
      upsertedEntities: [],
      removedEntityKeys: [],
      upsertedContainers: [],
      removedContainerKeys: [],
    },
    incomingVersion,
  );

  expect(after).not.toBe(before);
  expect(after.graph).toBe(before.graph);
  expect(after.version).toEqual(incomingVersion);
  expect(after.version).not.toBe(incomingVersion);
  expect(Object.isFrozen(after.version)).toBe(true);
});

test("generation-reset snapshots reuse equal records and maps under the new version wrapper", () => {
  const entityKey = scopedKey("1");
  const before = make([entityKey]);
  const incoming = make([entityKey]);
  const incomingVersion = { generationId: "replacement-generation", revision: 1, etag: "replacement-tag" };
  const after = reconcileSnapshot(before, {
    ...incoming,
    graph: Object.freeze({
      ...incoming.graph,
      metadata: Object.freeze({
        ...incoming.graph.metadata,
        generation_id: incomingVersion.generationId,
      }),
    }),
    version: incomingVersion,
  });

  expect(after.graph).not.toBe(before.graph);
  expect(after.graph.entities).toBe(before.graph.entities);
  expect(after.graph.containers).toBe(before.graph.containers);
  expect(after.graph.entities.get(entityKey)).toBe(before.graph.entities.get(entityKey));
  expect(after.version).toEqual(incomingVersion);
  expect(after.version).not.toBe(incomingVersion);
  expect(Object.isFrozen(after.version)).toBe(true);
});

test("snapshot and delta reconciliation converge with the same keyed sharing", () => {
  const firstKey = scopedKey("1");
  const secondKey = scopedKey("2");
  const before = make([firstKey, secondKey]);
  const incoming = make([firstKey, secondKey]);
  const incomingEntities = new Map(incoming.graph.entities);
  incomingEntities.set(
    firstKey,
    Object.freeze({
      ...incomingEntities.get(firstKey),
      value: Object.freeze({ ...value("local:s1"), title: "Changed" }),
    }) as NonNullable<ReturnType<typeof incoming.graph.entities.get>>,
  );
  const snapshotResult = reconcileSnapshot(before, {
    ...incoming,
    graph: Object.freeze({ ...incoming.graph, entities: incomingEntities }),
  });
  const deltaResult = applyDelta(
    before,
    {
      upsertedEntities: [{ key: firstKey, kind: "session", value: { ...value("local:s1"), title: "Changed" } }],
      removedEntityKeys: [],
      upsertedContainers: [],
      removedContainerKeys: [],
    },
    version1,
  );

  expect(snapshotResult.graph).toEqual(deltaResult.graph);
  expect(snapshotResult.graph.entities).not.toBe(before.graph.entities);
  expect(deltaResult.graph.entities).not.toBe(before.graph.entities);
  expect(snapshotResult.graph.containers).toBe(before.graph.containers);
  expect(deltaResult.graph.containers).toBe(before.graph.containers);
  expect(snapshotResult.graph.entities.get(secondKey)).toBe(before.graph.entities.get(secondKey));
  expect(deltaResult.graph.entities.get(secondKey)).toBe(before.graph.entities.get(secondKey));
});

test("delta applies complete container order atomically", () => {
  const firstKey = scopedKey("1");
  const secondKey = scopedKey("2");
  const before = make([firstKey, secondKey]);
  const after = applyDelta(
    before,
    {
      metadata: { ...before.graph.metadata, revision: 2 },
      upsertedEntities: [],
      removedEntityKeys: [],
      upsertedContainers: [
        {
          key: navigationRootContainerKey(key, "sessions"),
          owner: { kind: "resource_root", slot: "sessions" },
          children: [secondKey, firstKey],
        },
      ],
      removedContainerKeys: [],
    },
    version2,
  );
  expect(after.graph.containers.get(navigationRootContainerKey(key, "sessions"))?.children).toEqual([
    secondKey,
    firstKey,
  ]);
  expect(after.graph.entities.get(firstKey)).toBe(before.graph.entities.get(firstKey));
  expect(after.version).toEqual(version2);
  expect(after.version).not.toBe(version2);
  expect(Object.isFrozen(after.version)).toBe(true);
});

test("invalid staged delta preserves the complete prior graph identity", () => {
  const before = make([scopedKey("1")]);
  const graph = before.graph;
  const entities = before.graph.entities;
  const containers = before.graph.containers;
  const orphanKey = scopedKey("9");
  expect(() =>
    applyDelta(
      before,
      {
        metadata: { ...before.graph.metadata, revision: 2 },
        upsertedEntities: [
          {
            key: orphanKey,
            kind: "session",
            value: value("local:private-orphan"),
          },
        ],
        removedEntityKeys: [],
        upsertedContainers: [
          {
            key: navigationOwnedContainerKey(orphanKey, "children"),
            owner: { kind: "entity", entityKey: orphanKey, slot: "children" },
            children: [],
          },
        ],
        removedContainerKeys: [],
      },
      version2,
    ),
  ).toThrow("navigation protocol");
  expect(before.graph).toBe(graph);
  expect(before.graph.entities).toBe(entities);
  expect(before.graph.containers).toBe(containers);
  expect(before.graph.entities.has(orphanKey)).toBe(false);
});
