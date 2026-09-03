import { readFileSync } from "node:fs";
import { join } from "node:path";
import { expect, test } from "vitest";
import type { NavigationSnapshot } from "../../protocol/types.gen";
import {
  decodeNavigationResponse,
  materializeNavigationResource,
  type NormalizedResource,
  normalizedGraphFromSnapshot,
} from "./codec";
import { applyDelta } from "./merge";
import {
  NavigationBaseInvalidError,
  navigationOwnedContainerKey,
  navigationRootContainerKey,
  navigationViewScope,
  type ResourceKey,
} from "./types";

const key = { kind: "section", section: "live", offset: 0, limit: 50 } as const;
const base = { generationId: "g", revision: 1, etag: "tag-1" };
const privateValues = ["private-generation", "private-body-value", "private-child", "private-owner"];
const timestampFixtures = JSON.parse(
  readFileSync(join("..", "testdata", "navigation", "timestamps.json"), "utf8"),
) as Array<{ value: string; valid: boolean }>;

const entityKey = (resource: ResourceKey, digit: string) =>
  `${navigationViewScope(resource)}/entity/${digit.repeat(64)}`;
const sessionValue = (ref: string) => ({
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
const snapshotResponse = (_resource: ResourceKey, data: NavigationSnapshot) => ({
  status: "ok",
  representation: "snapshot",
  ...base,
  data,
});
function liveSnapshot(resource: ResourceKey = key): NavigationSnapshot {
  const session = entityKey(resource, "1");
  return {
    metadata: { generation_id: "g", revision: 1, offset: 0, limit: 50, remaining: 0, truncated: false },
    entities: [{ key: session, kind: "session", value: sessionValue("local:session") }],
    containers: [
      {
        key: navigationRootContainerKey(resource, "sessions"),
        owner: { kind: "resource_root", slot: "sessions" },
        children: [session],
      },
      {
        key: navigationOwnedContainerKey(session, "children"),
        owner: { kind: "entity", entityKey: session, slot: "children" },
        children: [],
      },
    ],
  };
}

test("decodes a normalized snapshot and rejects dangling children", () => {
  const snapshot = liveSnapshot();
  expect(decodeNavigationResponse(key, undefined, snapshotResponse(key, snapshot)).status).toBe("snapshot");
  expect(() =>
    decodeNavigationResponse(key, undefined, {
      ...snapshotResponse(key, snapshot),
      data: { ...snapshot, containers: [{ ...snapshot.containers[0], children: ["missing"] }] },
    }),
  ).toThrow();
});

test("codec accepts stateless records and rejects obsolete entity and container revisions", () => {
  const stateless = structuredClone(liveSnapshot()) as unknown as {
    entities: Array<Record<string, unknown>>;
    containers: Array<Record<string, unknown>>;
  };
  expect(
    decodeNavigationResponse(key, undefined, snapshotResponse(key, stateless as unknown as NavigationSnapshot)).status,
  ).toBe("snapshot");
  const obsoleteEntity = structuredClone(stateless);
  obsoleteEntity.entities[0]!.revision = 1;
  expect(() =>
    decodeNavigationResponse(key, undefined, snapshotResponse(key, obsoleteEntity as unknown as NavigationSnapshot)),
  ).toThrow();
  const obsoleteContainer = structuredClone(stateless);
  obsoleteContainer.containers[0]!.revision = 1;
  expect(() =>
    decodeNavigationResponse(key, undefined, snapshotResponse(key, obsoleteContainer as unknown as NavigationSnapshot)),
  ).toThrow();
});

test("normalized metadata and entity values are deeply frozen and detached", () => {
  const snapshot = liveSnapshot();
  const metadata = {
    ...(snapshot.metadata as Record<string, unknown>),
    detail: { flags: ["installed"] },
  };
  const entity = snapshot.entities[0];
  const container = snapshot.containers[0];
  expect(entity).toBeTruthy();
  expect(container).toBeTruthy();
  if (!entity || !container) throw new Error("fixture is incomplete");
  const entityValue = entity.value as Record<string, unknown>;
  const runningJob = { job_id: "job-1", job_type: "exec", status: "running", task: "build" };
  const runningJobs = [runningJob];
  entityValue.running_jobs = runningJobs;
  snapshot.metadata = metadata;

  const graph = normalizedGraphFromSnapshot(snapshot);
  const installedEntity = graph.entities.get(entity.key);
  const installedContainer = graph.containers.get(container.key);
  const installedValue = installedEntity?.value as Record<string, unknown>;
  const installedJobs = installedValue.running_jobs as ReadonlyArray<Record<string, unknown>>;
  const installedDetail = graph.metadata.detail as Readonly<{ flags: readonly string[] }>;

  expect(graph.metadata).not.toBe(metadata);
  expect(installedEntity).not.toBe(entity);
  expect(installedValue).not.toBe(entityValue);
  expect(installedJobs).not.toBe(runningJobs);
  expect(installedJobs[0]).not.toBe(runningJobs[0]);
  expect(installedContainer).not.toBe(container);
  expect(installedContainer?.owner).not.toBe(container.owner);
  expect(installedContainer?.children).not.toBe(container.children);

  expect(Object.isFrozen(graph)).toBe(true);
  expect(Object.isFrozen(graph.metadata)).toBe(true);
  expect(Object.isFrozen(installedDetail)).toBe(true);
  expect(Object.isFrozen(installedDetail.flags)).toBe(true);
  expect(Object.isFrozen(installedEntity)).toBe(true);
  expect(Object.isFrozen(installedValue)).toBe(true);
  expect(Object.isFrozen(installedValue.children)).toBe(true);
  expect(Object.isFrozen(installedJobs)).toBe(true);
  expect(Object.isFrozen(installedJobs[0])).toBe(true);
  expect(Object.isFrozen(installedContainer)).toBe(true);
  expect(Object.isFrozen(installedContainer?.owner)).toBe(true);
  expect(Object.isFrozen(installedContainer?.children)).toBe(true);

  (metadata.detail as { flags: string[] }).flags[0] = "mutated";
  entityValue.title = "Mutated";
  runningJob.status = "completed";
  container.owner.slot = "mutated";
  container.children.push(entity.key);

  expect(installedDetail.flags).toEqual(["installed"]);
  expect(installedValue.title).toBe("Session");
  expect(installedJobs[0]?.status).toBe("running");
  expect(installedContainer?.owner.slot).toBe("sessions");
  expect(installedContainer?.children).toEqual([entity.key]);
});

test("requires exact echoed base for delta", () => {
  const delta = { upsertedEntities: [], removedEntityKeys: [], upsertedContainers: [], removedContainerKeys: [] };
  expect(
    decodeNavigationResponse(key, base, { status: "ok", representation: "delta", ...base, base, data: delta }).status,
  ).toBe("delta");
  expect(() =>
    decodeNavigationResponse(key, base, {
      status: "ok",
      representation: "delta",
      ...base,
      base: { ...base, etag: "wrong" },
      data: delta,
    }),
  ).toThrow(NavigationBaseInvalidError);
});

test.each([
  {
    status: "not_modified",
    sentBase: base,
    response: { status: "not_modified", ...base, unknown: true },
  },
  {
    status: "gone",
    sentBase: undefined,
    response: { status: "gone", ...base, unknown: true },
  },
  {
    status: "snapshot",
    sentBase: undefined,
    response: { ...snapshotResponse(key, liveSnapshot()), unknown: true },
  },
  {
    status: "delta",
    sentBase: base,
    response: {
      status: "ok",
      representation: "delta",
      ...base,
      base,
      data: { upsertedEntities: [], removedEntityKeys: [], upsertedContainers: [], removedContainerKeys: [] },
      unknown: true,
    },
  },
])("rejects unknown outer response field for $status", ({ sentBase, response }) => {
  let thrown: unknown;
  try {
    decodeNavigationResponse(key, sentBase, response);
  } catch (error) {
    thrown = error;
  }
  expect(thrown).toBeInstanceOf(Error);
  expect(thrown).not.toBeInstanceOf(NavigationBaseInvalidError);
});

type SnapshotFixture = { key: ResourceKey; snapshot: NavigationSnapshot };
function schemaFixtures(): SnapshotFixture[] {
  const manifest: ResourceKey = { kind: "manifest" };
  const live: ResourceKey = { kind: "section", section: "live", offset: 0, limit: 50 };
  const needsYou: ResourceKey = { kind: "section", section: "needs_you", offset: 0, limit: 50 };
  const pinSection: ResourceKey = { kind: "pin_section", sectionId: "pins", offset: 0, limit: 50 };
  const pinCatalog: ResourceKey = { kind: "pin_catalog", offset: 0, limit: 100 };
  const projects: ResourceKey = { kind: "catalog", catalog: "projects", offset: 0, limit: 100 };
  const archivedProjects: ResourceKey = { kind: "catalog", catalog: "archived_projects", offset: 0, limit: 100 };
  const testRuns: ResourceKey = { kind: "catalog", catalog: "test_runs", offset: 0, limit: 100 };
  const project: ResourceKey = { kind: "project", projectKey: "project" };
  const projectPage: ResourceKey = {
    kind: "project_page",
    projectKey: "project",
    tier: "current",
    offset: 0,
    limit: 50,
  };
  const location: ResourceKey = { kind: "location", ref: "local:session" };
  const pagedMetadata = { generation_id: "g", revision: 1, offset: 0, limit: 50, remaining: 0, truncated: false };
  const sessionSnapshot = (resource: ResourceKey, metadata: Record<string, unknown>): NavigationSnapshot => {
    const session = entityKey(resource, "1");
    return {
      metadata,
      entities: [{ key: session, kind: "session", value: sessionValue("local:session") }],
      containers: [
        {
          key: navigationRootContainerKey(resource, resource.kind === "location" ? "session" : "sessions"),
          owner: { kind: "resource_root", slot: resource.kind === "location" ? "session" : "sessions" },
          children: [session],
        },
        {
          key: navigationOwnedContainerKey(session, "children"),
          owner: { kind: "entity", entityKey: session, slot: "children" },
          children: [],
        },
      ],
    };
  };
  const pinEntity = entityKey(pinCatalog, "2");
  const catalogSnapshot = (resource: ResourceKey): NavigationSnapshot => {
    const projectEntity = entityKey(resource, "3");
    return {
      metadata: { generation_id: "g", revision: 1, offset: 0, limit: 100, remaining: 0 },
      entities: [
        {
          key: projectEntity,
          kind: "project",
          value: { key: "project", name: "Project", session_count: 1 },
        },
      ],
      containers: [
        {
          key: navigationRootContainerKey(resource, "projects"),
          owner: { kind: "resource_root", slot: "projects" },
          children: [projectEntity],
        },
      ],
    };
  };
  const projectAnchor = entityKey(project, "4");
  const projectSession = entityKey(project, "5");
  return [
    {
      key: manifest,
      snapshot: {
        metadata: {
          generation_id: "g",
          revision: 1,
          sources: [],
          attentionSummary: { needsYou: 0, error: 0, working: 0 },
          sections: { live: { count: 1 }, needs_you: { count: 1 }, pin_sections: { count: 1 } },
          catalogs: { projects: { count: 1 }, archived_projects: { count: 1 }, test_runs: { count: 1 } },
        },
        entities: [],
        containers: [
          {
            key: navigationRootContainerKey(manifest, "manifest"),
            owner: { kind: "resource_root", slot: "manifest" },
            children: [],
          },
        ],
      },
    },
    { key: live, snapshot: sessionSnapshot(live, pagedMetadata) },
    { key: needsYou, snapshot: sessionSnapshot(needsYou, pagedMetadata) },
    { key: pinSection, snapshot: sessionSnapshot(pinSection, pagedMetadata) },
    {
      key: pinCatalog,
      snapshot: {
        metadata: { generation_id: "g", revision: 1, offset: 0, limit: 100, remaining: 0 },
        entities: [{ key: pinEntity, kind: "pin_section", value: { id: "pins", name: "Pins", count: 1 } }],
        containers: [
          {
            key: navigationRootContainerKey(pinCatalog, "pin_sections"),
            owner: { kind: "resource_root", slot: "pin_sections" },
            children: [pinEntity],
          },
        ],
      },
    },
    { key: projects, snapshot: catalogSnapshot(projects) },
    { key: archivedProjects, snapshot: catalogSnapshot(archivedProjects) },
    { key: testRuns, snapshot: catalogSnapshot(testRuns) },
    {
      key: project,
      snapshot: {
        metadata: {
          generation_id: "g",
          revision: 1,
          key: "project",
          current_remaining: 0,
          recent_remaining: 0,
          archived_remaining: 0,
          truncated: false,
        },
        entities: [
          { key: projectAnchor, kind: "project", value: { key: "project" } },
          { key: projectSession, kind: "session", value: sessionValue("local:session") },
        ],
        containers: [
          {
            key: navigationOwnedContainerKey(projectAnchor, "current"),
            owner: { kind: "entity", entityKey: projectAnchor, slot: "current" },
            children: [projectSession],
          },
          {
            key: navigationOwnedContainerKey(projectAnchor, "recent"),
            owner: { kind: "entity", entityKey: projectAnchor, slot: "recent" },
            children: [],
          },
          {
            key: navigationOwnedContainerKey(projectAnchor, "archived"),
            owner: { kind: "entity", entityKey: projectAnchor, slot: "archived" },
            children: [],
          },
          {
            key: navigationOwnedContainerKey(projectSession, "children"),
            owner: { kind: "entity", entityKey: projectSession, slot: "children" },
            children: [],
          },
        ],
      },
    },
    {
      key: projectPage,
      snapshot: sessionSnapshot(projectPage, {
        ...pagedMetadata,
        key: "project",
        tier: "current",
      }),
    },
    {
      key: location,
      snapshot: sessionSnapshot(location, {
        generation_id: "g",
        revision: 1,
        ref: "local:session",
        top_level_ref: "local:session",
        top_level: true,
      }),
    },
  ];
}

function cloneSnapshot(snapshot: NavigationSnapshot): NavigationSnapshot {
  return structuredClone(snapshot);
}

function chainSnapshot(fixture: SnapshotFixture, depth: number): NavigationSnapshot {
  const snapshot = cloneSnapshot(fixture.snapshot);
  const keys = Array.from(
    { length: depth },
    (_, index) => `${navigationViewScope(fixture.key)}/entity/${(index + 1).toString(16).padStart(64, "0")}`,
  );
  const sessions = keys.map((sessionKey, index) => ({
    key: sessionKey,
    kind: "session",
    value: sessionValue(`local:depth-${index}`),
  }));
  const owned = keys.map((sessionKey, index) => {
    const child = keys[index + 1];
    return {
      key: navigationOwnedContainerKey(sessionKey, "children"),
      owner: { kind: "entity" as const, entityKey: sessionKey, slot: "children" },
      children: child === undefined ? [] : [child],
    };
  });

  if (fixture.key.kind === "project") {
    const anchor = snapshot.entities.find((item) => item.kind === "project");
    if (!anchor || !keys[0]) throw new Error("missing project chain root");
    const anchorContainers = snapshot.containers.filter(
      (item) => item.owner.kind === "entity" && item.owner.entityKey === anchor.key,
    );
    for (const item of anchorContainers) item.children = item.owner.slot === "current" ? [keys[0]] : [];
    snapshot.entities = [anchor, ...sessions];
    snapshot.containers = [...anchorContainers, ...owned];
    return snapshot;
  }

  const root = snapshot.containers.find((item) => item.owner.kind === "resource_root");
  if (!root || !keys[0]) throw new Error("missing section chain root");
  root.children = [keys[0]];
  snapshot.entities = sessions;
  snapshot.containers = [root, ...owned];
  return snapshot;
}

function sessionBoundSnapshot(resource: ResourceKey, sessionCount: number): NavigationSnapshot {
  if (resource.kind !== "section" && resource.kind !== "project" && resource.kind !== "project_page")
    throw new Error("unsupported bound fixture");
  const scope = navigationViewScope(resource);
  const sessionKeys = Array.from(
    { length: sessionCount },
    (_, index) => `${scope}/entity/${(index + 1).toString(16).padStart(64, "0")}`,
  );
  const rootChildren: string[] = [];
  const childrenByOwner = new Map<string, string[]>();
  for (let next = 0; next < sessionKeys.length; ) {
    const parent = sessionKeys[next];
    if (!parent) throw new Error("missing session key");
    rootChildren.push(parent);
    const end = Math.min(next + 51, sessionKeys.length);
    childrenByOwner.set(parent, sessionKeys.slice(next + 1, end));
    next = end;
  }
  const sessionEntities = sessionKeys.map((sessionKey, index) => ({
    key: sessionKey,
    kind: "session",
    value: sessionValue(`local:session-${index}`),
  }));
  const sessionContainers = sessionKeys.map((sessionKey) => ({
    key: navigationOwnedContainerKey(sessionKey, "children"),
    owner: { kind: "entity" as const, entityKey: sessionKey, slot: "children" },
    children: childrenByOwner.get(sessionKey) ?? [],
  }));
  if (resource.kind === "section" || resource.kind === "project_page")
    return {
      metadata:
        resource.kind === "section"
          ? {
              generation_id: "g",
              revision: 1,
              offset: resource.offset,
              limit: resource.limit,
              remaining: 0,
              truncated: false,
            }
          : {
              generation_id: "g",
              revision: 1,
              key: resource.projectKey,
              tier: resource.tier,
              offset: resource.offset,
              limit: resource.limit,
              remaining: 0,
              truncated: false,
            },
      entities: sessionEntities,
      containers: [
        {
          key: navigationRootContainerKey(resource, "sessions"),
          owner: { kind: "resource_root", slot: "sessions" },
          children: rootChildren,
        },
        ...sessionContainers,
      ],
    };

  const anchorKey = `${scope}/entity/${"f".repeat(64)}`;
  return {
    metadata: {
      generation_id: "g",
      revision: 1,
      key: resource.projectKey,
      current_remaining: 0,
      recent_remaining: 0,
      archived_remaining: 0,
      truncated: false,
    },
    entities: [{ key: anchorKey, kind: "project", value: { key: resource.projectKey } }, ...sessionEntities],
    containers: [
      {
        key: navigationOwnedContainerKey(anchorKey, "current"),
        owner: { kind: "entity", entityKey: anchorKey, slot: "current" },
        children: rootChildren,
      },
      {
        key: navigationOwnedContainerKey(anchorKey, "recent"),
        owner: { kind: "entity", entityKey: anchorKey, slot: "recent" },
        children: [],
      },
      {
        key: navigationOwnedContainerKey(anchorKey, "archived"),
        owner: { kind: "entity", entityKey: anchorKey, slot: "archived" },
        children: [],
      },
      ...sessionContainers,
    ],
  };
}

function expectContentFreeRejection(key: ResourceKey, snapshot: NavigationSnapshot): void {
  let thrown: unknown;
  try {
    decodeNavigationResponse(key, undefined, snapshotResponse(key, snapshot));
  } catch (error) {
    thrown = error;
  }
  expect(thrown).toBeInstanceOf(Error);
  const message = (thrown as Error).message;
  for (const value of privateValues) expect(message).not.toContain(value);
}

test("codec rejects wrong resource metadata, value schema, slots, scope, and orphan entities", () => {
  const fixtures = schemaFixtures();
  for (const fixture of fixtures)
    expect(
      decodeNavigationResponse(fixture.key, undefined, snapshotResponse(fixture.key, fixture.snapshot)).status,
    ).toBe("snapshot");

  const live = fixtures.find((fixture) => fixture.key.kind === "section" && fixture.key.section === "live");
  const manifest = fixtures.find((fixture) => fixture.key.kind === "manifest");
  if (!live || !manifest || live.key.kind !== "section") throw new Error("missing schema fixture");
  const liveKey = live.key;
  const cases: Array<(snapshot: NavigationSnapshot) => void> = [
    (snapshot) => {
      snapshot.metadata = { ...(snapshot.metadata as object), generation_id: "private-generation" };
    },
    (snapshot) => {
      snapshot.metadata = { ...(snapshot.metadata as object), offset: 9 };
    },
    (snapshot) => {
      const first = snapshot.entities[0];
      if (!first) throw new Error("missing entity");
      snapshot.entities[0] = { ...first, kind: "unknown" };
    },
    (snapshot) => {
      const first = snapshot.entities[0];
      if (!first) throw new Error("missing entity");
      const value = { ...(first.value as Record<string, unknown>) };
      delete value.host_id;
      snapshot.entities[0] = { ...first, value };
    },
    (snapshot) => {
      const first = snapshot.entities[0];
      if (!first) throw new Error("missing entity");
      snapshot.entities[0] = {
        ...first,
        value: { ...(first.value as object), unknown: "private-body-value" },
      };
    },
    (snapshot) => {
      const first = snapshot.entities[0];
      if (!first) throw new Error("missing entity");
      snapshot.entities[0] = {
        ...first,
        value: { ...(first.value as object), children: [{ ref: "private-child" }] },
      };
    },
    (snapshot) => {
      const root = snapshot.containers[0];
      if (!root) throw new Error("missing root");
      snapshot.containers[0] = {
        ...root,
        key: navigationRootContainerKey(liveKey, "projects"),
        owner: { kind: "resource_root", slot: "projects" },
      };
    },
    (snapshot) => {
      const owned = snapshot.containers.find((container) => container.owner.kind === "entity");
      if (owned?.owner.kind !== "entity" || !owned.owner.entityKey) throw new Error("missing owned container");
      const ownerKey = owned.owner.entityKey;
      owned.owner = { ...owned.owner, slot: "recent" };
      owned.key = navigationOwnedContainerKey(ownerKey, "recent");
    },
    (snapshot) => {
      const owned = snapshot.containers.find((container) => container.owner.kind === "entity");
      if (owned?.owner.kind !== "entity") throw new Error("missing owned container");
      owned.owner = { ...owned.owner, entityKey: "private-owner" };
      owned.key = navigationOwnedContainerKey("private-owner", "children");
    },
    (snapshot) => {
      const root = snapshot.containers.find((container) => container.owner.kind === "resource_root");
      if (!root) throw new Error("missing root");
      root.children = [];
    },
    (snapshot) => {
      const original = snapshot.entities[0];
      if (!original) throw new Error("missing entity");
      const duplicateKey = entityKey(liveKey, "9");
      snapshot.entities.push({ ...original, key: duplicateKey });
      snapshot.containers[0]?.children.push(duplicateKey);
      snapshot.containers.push({
        key: navigationOwnedContainerKey(duplicateKey, "children"),
        owner: { kind: "entity", entityKey: duplicateKey, slot: "children" },
        children: [],
      });
    },
    (snapshot) => {
      const original = snapshot.entities[0];
      if (!original) throw new Error("missing entity");
      const wrongKey = entityKey({ ...liveKey, offset: 1 }, "1");
      snapshot.entities[0] = { ...original, key: wrongKey };
      for (const container of snapshot.containers) {
        container.children = container.children.map((child) => (child === original.key ? wrongKey : child));
        if (container.owner.kind === "entity" && container.owner.entityKey === original.key && container.owner.slot) {
          const ownerSlot = container.owner.slot;
          container.owner = { ...container.owner, entityKey: wrongKey };
          container.key = navigationOwnedContainerKey(wrongKey, ownerSlot);
        }
      }
    },
  ];
  for (const mutate of cases) {
    const snapshot = cloneSnapshot(live.snapshot);
    mutate(snapshot);
    expectContentFreeRejection(live.key, snapshot);
  }

  const extraRoot = cloneSnapshot(manifest.snapshot);
  extraRoot.containers.push({
    key: navigationRootContainerKey(manifest.key, "sessions"),
    owner: { kind: "resource_root", slot: "sessions" },
    children: [],
  });
  expectContentFreeRejection(manifest.key, extraRoot);
});

test("codec enforces the projector graph-depth boundary", () => {
  const fixtures = schemaFixtures().filter(
    (fixture) => (fixture.key.kind === "section" && fixture.key.section === "live") || fixture.key.kind === "project",
  );
  for (const fixture of fixtures) {
    expect(
      decodeNavigationResponse(fixture.key, undefined, snapshotResponse(fixture.key, chainSnapshot(fixture, 32)))
        .status,
    ).toBe("snapshot");
    expectContentFreeRejection(fixture.key, chainSnapshot(fixture, 33));
  }
});

test.each([
  {
    name: "section",
    resource: { kind: "section", section: "live", offset: 0, limit: 50 } as const,
    entities: 2000,
    containers: 2001,
  },
  {
    name: "project",
    resource: { kind: "project", projectKey: "project" } as const,
    entities: 2001,
    containers: 2003,
  },
  {
    name: "project page",
    resource: { kind: "project_page", projectKey: "project", tier: "current", offset: 0, limit: 50 } as const,
    entities: 2000,
    containers: 2001,
  },
])("codec accepts the maximum $name session graph", ({ resource, entities, containers }) => {
  const snapshot = sessionBoundSnapshot(resource, 2000);
  expect(snapshot.entities).toHaveLength(entities);
  expect(snapshot.containers).toHaveLength(containers);
  expect(decodeNavigationResponse(resource, undefined, snapshotResponse(resource, snapshot)).status).toBe("snapshot");
});

test.each([
  {
    name: "section session count",
    resource: { kind: "section", section: "live", offset: 0, limit: 50 } as const,
    entities: 2001,
    containers: 2002,
  },
  {
    name: "project aggregate graph",
    resource: { kind: "project", projectKey: "project" } as const,
    entities: 2002,
    containers: 2004,
  },
])("codec rejects the $name above the 2,000-session limit", ({ resource, entities, containers }) => {
  const snapshot = sessionBoundSnapshot(resource, 2001);
  expect(snapshot.entities).toHaveLength(entities);
  expect(snapshot.containers).toHaveLength(containers);
  expectContentFreeRejection(resource, snapshot);
});

test.each(timestampFixtures.filter((fixture) => fixture.valid).map((fixture) => fixture.value))(
  "codec accepts Go time.Time timestamp %s",
  (updatedAt) => {
    const snapshot = liveSnapshot();
    const first = snapshot.entities[0];
    if (!first) throw new Error("missing entity");
    snapshot.entities[0] = { ...first, value: { ...(first.value as object), updated_at: updatedAt } };
    expect(decodeNavigationResponse(key, undefined, snapshotResponse(key, snapshot)).status).toBe("snapshot");
  },
);

test.each(timestampFixtures.filter((fixture) => !fixture.valid).map((fixture) => fixture.value))(
  "codec rejects timestamp outside Go time.Time wire grammar %s",
  (updatedAt) => {
    const snapshot = liveSnapshot();
    const first = snapshot.entities[0];
    if (!first) throw new Error("missing entity");
    snapshot.entities[0] = { ...first, value: { ...(first.value as object), updated_at: updatedAt } };
    expectContentFreeRejection(key, snapshot);
  },
);

test.each([
  [
    "pin catalogs",
    (offset: number): ResourceKey => ({ kind: "pin_catalog", offset, limit: 2 }),
    "pin_sections",
    "pin_section",
    (offset: number) => ({ id: `pin-${offset}`, name: `Pin ${offset}`, count: 1 }),
    "pin_sections",
  ],
  [
    "project catalogs",
    (offset: number): ResourceKey => ({ kind: "catalog", catalog: "projects", offset, limit: 2 }),
    "projects",
    "project",
    (offset: number) => ({ key: `project-${offset}`, name: `Project ${offset}`, session_count: 1 }),
    "projects",
  ],
] as const)("materializes scoped %s across distinct pages", (_name, keyFor, slot, entityKind, valueFor, field) => {
  const materialized: unknown[] = [];
  const rootKeys: string[] = [];
  for (const offset of [0, 2]) {
    const resourceKey = keyFor(offset);
    const scopedEntityKey = `${navigationViewScope(resourceKey)}/entity/${String(offset + 1).repeat(64)}`;
    const rootKey = navigationRootContainerKey(resourceKey, slot);
    const resource: NormalizedResource = {
      key: resourceKey,
      graph: normalizedGraphFromSnapshot({
        metadata: { generation_id: "g", revision: 1, offset, limit: 2, remaining: 0 },
        entities: [{ key: scopedEntityKey, kind: entityKind, value: valueFor(offset) }],
        containers: [
          {
            key: rootKey,
            owner: { kind: "resource_root", slot },
            children: [scopedEntityKey],
          },
        ],
      }),
      version: base,
      presence: "present",
    };
    rootKeys.push(rootKey);
    materialized.push(materializeNavigationResource(resource));
  }
  expect(rootKeys[0]).not.toBe(rootKeys[1]);
  expect(materialized.map((item) => (item as Record<string, unknown[]>)[field]?.[0])).toEqual([
    valueFor(0),
    valueFor(2),
  ]);
});

test("grandchild changes invalidate every recursive ancestor materialization", () => {
  const parentKey = entityKey(key, "1");
  const childKey = entityKey(key, "2");
  const grandchildKey = entityKey(key, "3");
  const siblingKey = entityKey(key, "4");
  const siblingChildKey = entityKey(key, "5");
  const value = (ref: string, title: string) => ({ ...sessionValue(ref), title });
  const snapshot: NavigationSnapshot = {
    metadata: { generation_id: "g", revision: 1, offset: 0, limit: 50, remaining: 0, truncated: false },
    entities: [
      { key: parentKey, kind: "session", value: value("local:parent", "Parent") },
      { key: childKey, kind: "session", value: value("local:child", "Child") },
      { key: grandchildKey, kind: "session", value: value("local:grandchild", "Grandchild") },
      { key: siblingKey, kind: "session", value: value("local:sibling", "Sibling") },
      {
        key: siblingChildKey,
        kind: "session",
        value: value("local:sibling-child", "Sibling child"),
      },
    ],
    containers: [
      {
        key: navigationRootContainerKey(key, "sessions"),
        owner: { kind: "resource_root", slot: "sessions" },
        children: [parentKey, siblingKey],
      },
      ...[
        [parentKey, [childKey]],
        [childKey, [grandchildKey]],
        [grandchildKey, []],
        [siblingKey, [siblingChildKey]],
        [siblingChildKey, []],
      ].map(([ownerKey, children]) => ({
        key: navigationOwnedContainerKey(ownerKey as string, "children"),
        owner: { kind: "entity" as const, entityKey: ownerKey as string, slot: "children" },
        children: children as string[],
      })),
    ],
  };
  const initial: NormalizedResource = Object.freeze({
    key,
    graph: normalizedGraphFromSnapshot(snapshot),
    version: Object.freeze(base),
    presence: "present",
  });

  const before = materializeNavigationResource(initial) as {
    sessions: Array<Record<string, unknown> & { children: Array<Record<string, unknown>> }>;
  };
  const repeated = materializeNavigationResource(initial) as typeof before;
  const beforeParent = before.sessions[0];
  const beforeChild = beforeParent?.children[0] as typeof beforeParent;
  const beforeGrandchild = beforeChild?.children[0];
  const beforeSibling = before.sessions[1];
  const beforeSiblingChild = beforeSibling?.children[0];

  const changed = applyDelta(
    initial,
    {
      metadata: { ...(snapshot.metadata as Record<string, unknown>), revision: 2 },
      upsertedEntities: [
        {
          key: grandchildKey,
          kind: "session",
          value: value("local:grandchild", "Changed grandchild"),
        },
      ],
      removedEntityKeys: [],
      upsertedContainers: [],
      removedContainerKeys: [],
    },
    { generationId: "g", revision: 2, etag: "tag-2" },
  );
  const after = materializeNavigationResource(changed) as typeof before;
  const afterParent = after.sessions[0];
  const afterChild = afterParent?.children[0] as typeof beforeParent;
  const afterGrandchild = afterChild?.children[0];
  const afterSibling = after.sessions[1];

  expect(after).not.toBe(before);
  expect(afterParent).not.toBe(beforeParent);
  expect(afterChild).not.toBe(beforeChild);
  expect(afterGrandchild).not.toBe(beforeGrandchild);
  expect(afterGrandchild?.title).toBe("Changed grandchild");
  expect(beforeGrandchild?.title).toBe("Grandchild");
  expect(afterSibling).toBe(beforeSibling);
  expect(afterSibling?.children).toBe(beforeSibling?.children);
  expect(afterSibling?.children[0]).toBe(beforeSiblingChild);
  expect(Object.isFrozen(afterParent)).toBe(true);
  expect(Object.isFrozen(afterParent?.children)).toBe(true);
  expect(repeated).toBe(before);
  expect(repeated.sessions).toBe(before.sessions);
  expect(repeated.sessions[0]).toBe(beforeParent);
  expect(repeated.sessions[0]?.children[0]).toBe(beforeChild);
});

test("repeated compatibility materialization preserves root and nested identity", () => {
  const initial: NormalizedResource = Object.freeze({
    key,
    graph: normalizedGraphFromSnapshot(liveSnapshot()),
    version: Object.freeze(base),
    presence: "present",
  });
  const before = materializeNavigationResource(initial) as { sessions: Array<{ children: unknown[] }> };
  const after = materializeNavigationResource(initial) as typeof before;

  expect(after).toBe(before);
  expect(after.sessions).toBe(before.sessions);
  expect(after.sessions[0]).toBe(before.sessions[0]);
  expect(after.sessions[0]?.children).toBe(before.sessions[0]?.children);
});
