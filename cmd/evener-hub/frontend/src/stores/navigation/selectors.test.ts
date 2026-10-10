import {
  type NormalizedResource,
  navigationOwnedContainerKey,
  navigationRootContainerKey,
  navigationViewScope,
  normalizedGraphFromSnapshot,
} from "@evener/appwire-client/state/navigation";
import { expect, test } from "vitest";
import { relativeAge, selectRailModel } from "./selectors";

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

test("normalized rail sessions carry the updated_at anchor a row derives its display age from", () => {
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
  // The model carries the ANCHOR, not a label: a row (not the adapter) derives
  // the relative stamp, so it can tick without new data.
  expect(model.sessions.get(firstEntityKey)?.updated_at).toBe(updatedAt);
  expect(relativeAge(updatedAt, Date.now())).toBe("1h");
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
