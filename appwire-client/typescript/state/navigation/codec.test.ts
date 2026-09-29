import { expect, test, vi } from "vitest";
// Loaded through Vite's `?raw` import, which resolves against this file: a
// filesystem read would resolve against whichever working directory the
// consumer's test runner uses, and package-test-files.mjs refuses one.
import timestampFixture from "../../../../cmd/evener-hub/testdata/navigation/timestamps.json?raw";
import valueRecordsFixture from "../../../../cmd/evener-hub/testdata/navigation/value-records.json?raw";
import { completeSession } from "../../testing/navigation";
import type { NavigationSessionSummary, NavigationSnapshot } from "../../types.gen";
import {
  decodeArchivedListSessions,
  decodeNavigationResponse,
  materializeNavigationResource,
  materializeSnapshot,
  type NormalizedResource,
  normalizedGraphFromSnapshot,
  snapshotResource,
  type ValueRecordKeyTable,
} from "./codec";
import { applyDelta, reconcileSnapshot } from "./merge";
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
const timestampFixtures = JSON.parse(timestampFixture) as Array<{ value: string; valid: boolean }>;
const valueRecords = JSON.parse(valueRecordsFixture) as Record<
  "session" | "project" | "pin_section" | "manifest" | "location",
  Record<string, unknown>
>;

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

// A folded row from an unreachable source carries the hub's offline marker
// (component 06). It is an optional boolean beside dormant: a row that ran on
// an offline host stays a valid session row, and a non-boolean value is still
// a schema error rather than a silently ignored field.
test("codec accepts the offline source marker on a session row", () => {
  const withOffline = (offline: unknown) => {
    const snapshot = liveSnapshot();
    const value = snapshot.entities[0]!.value as Record<string, unknown>;
    value.ref = "remote-offline:offline-thread";
    value.host_id = "remote-offline";
    value.session_id = "offline-thread";
    value.offline = offline;
    return snapshot;
  };
  const snapshot = withOffline(true);
  expect(decodeNavigationResponse(key, undefined, snapshotResponse(key, snapshot)).status).toBe("snapshot");
  const entity = snapshot.entities[0]!;
  const graph = normalizedGraphFromSnapshot(snapshot);
  const installed = graph.entities.get(entity.key);
  if (!installed) throw new Error("offline session entity missing from the normalized graph");
  expect((installed.value as Record<string, unknown>).offline).toBe(true);
  const malformed = withOffline("yes");
  expect(() => decodeNavigationResponse(key, undefined, snapshotResponse(key, malformed))).toThrow();
});

// approval_pending is an optional boolean like ask_pending: a non-boolean value
// is a schema error. The value-records fixture test proves a valid one is kept.
test("codec refuses a non-boolean approval flag on a session row", () => {
  const snapshot = liveSnapshot();
  const first = snapshot.entities[0];
  if (!first) throw new Error("missing entity");
  first.value = { ...(first.value as object), approval_pending: "private-body-value" };
  expectContentFreeRejection(key, snapshot);
});

// approval_tool is an identity (at most 1024 UTF-8 bytes) and approval_target a
// label (at most 512 characters), as the hub bounds them. A value at a bound is
// kept; past one, or of the wrong type, it is a schema error that names none of
// the row's content.
test("codec keeps an approval's tool and target within their bounds and refuses them past", () => {
  const withApproval = (detail: Record<string, unknown>) => {
    const snapshot = liveSnapshot();
    const first = snapshot.entities[0];
    if (!first) throw new Error("missing entity");
    first.value = { ...(first.value as object), state: "active", approval_pending: true, ...detail };
    return snapshot;
  };
  const tool = "t".repeat(1024);
  const target = "😀".repeat(512);
  const rows = materializeSnapshot(
    key,
    decodedSnapshot(key, withApproval({ approval_tool: tool, approval_target: target })),
  ).sessions as Array<Record<string, unknown>>;
  expect(rows[0]?.approval_tool).toBe(tool);
  expect(rows[0]?.approval_target).toBe(target);

  expectContentFreeRejection(key, withApproval({ approval_tool: `${"t".repeat(1007)}private-body-value` }));
  expectContentFreeRejection(key, withApproval({ approval_tool: "" }));
  expectContentFreeRejection(key, withApproval({ approval_tool: 7 }));
  expectContentFreeRejection(key, withApproval({ approval_target: `${"t".repeat(495)}private-body-value` }));
  expectContentFreeRejection(key, withApproval({ approval_target: ["private-body-value"] }));
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

// --- Additive keys on value records ------------------------------------------
// A newer hub adds optional keys to value records without a protocol version
// bump (appwire/types.go ProtocolVersion; #1208, #2453). The codec accepts a
// value record carrying a key it does not know, still validates every key it
// does know, and drops the unknown key before anything installs the record:
// it never reaches the normalized graph, the rendered rows, or merge's
// identity comparison. Structure stays exact (the test.each below).
const futureValue = { nested: ["private-body-value"], count: -1 };

function decodedSnapshot(resource: ResourceKey, snapshot: NavigationSnapshot) {
  const decoded = decodeNavigationResponse(resource, undefined, snapshotResponse(resource, snapshot));
  if (decoded.status !== "snapshot") throw new Error(`expected a snapshot, got ${decoded.status}`);
  return decoded;
}

test("codec drops unknown keys on a session row and on its jobs, watches and cadence", () => {
  const snapshot = liveSnapshot();
  const first = snapshot.entities[0];
  if (!first) throw new Error("missing entity");
  const job = { job_id: "job-1", job_type: "shell", status: "running", task: "build" };
  const cadence = { kind: "every", seconds: 60 };
  const watch = { id: "watch-1", source: "self", deliveries: 0, created_at: "2026-09-26T12:00:00Z", active: true };
  first.value = {
    ...(first.value as object),
    running_jobs: [{ ...job, future_job_key: futureValue }],
    completed_jobs: [{ ...job, status: "completed", future_job_key: futureValue }],
    watches: [{ ...watch, cadence: [{ ...cadence, future_cadence_key: futureValue }], future_watch_key: futureValue }],
    future_session_key: futureValue,
  };
  const rows = materializeSnapshot(key, decodedSnapshot(key, snapshot)).sessions as Array<Record<string, unknown>>;
  expect(rows).toEqual([
    {
      ...sessionValue("local:session"),
      running_jobs: [job],
      completed_jobs: [{ ...job, status: "completed" }],
      watches: [{ ...watch, cadence: [cadence] }],
    },
  ]);
});

test("an unknown key never excuses a malformed known one", () => {
  const snapshot = liveSnapshot();
  const first = snapshot.entities[0];
  if (!first) throw new Error("missing entity");
  first.value = { ...(first.value as object), future_session_key: futureValue, ask_pending: "private-body-value" };
  expectContentFreeRejection(key, snapshot);
});

test("codec drops unknown keys on project, project anchor and pin-section values", () => {
  for (const kind of ["catalog", "project", "pin_catalog"] as const) {
    const { key: resource, snapshot } = schemaFixture(kind);
    for (const item of snapshot.entities) item.value = { ...(item.value as object), future_value_key: futureValue };
    for (const item of decodedSnapshot(resource, snapshot).snapshot.entities)
      expect(item.value).not.toHaveProperty("future_value_key");
  }
});

test("codec drops unknown keys across the manifest and keeps everything it knows", () => {
  const { key: resource, snapshot } = schemaFixture("manifest");
  const source = { id: "local", label: "magic-kingdom", kind: "local", online: true };
  const known = {
    generation_id: "g",
    revision: 1,
    sources: [source],
    attentionSummary: { needsYou: 1, error: 0, working: 2 },
    sections: { live: { count: 3 }, needs_you: { count: 1 }, pin_sections: { count: 0 } },
    catalogs: { projects: { count: 2 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
  };
  snapshot.metadata = {
    ...known,
    notices: futureValue,
    sources: [{ ...source, version: "private-body-value" }],
    attentionSummary: { ...known.attentionSummary, approval: 1 },
    sections: { ...known.sections, live: { count: 3, oldest: "private-body-value" }, finished: { count: 4 } },
    catalogs: { ...known.catalogs, hosts: { count: 2 } },
  };
  expect(materializeSnapshot(resource, decodedSnapshot(resource, snapshot))).toEqual(known);
});

test("codec drops unknown keys on a location's metadata", () => {
  const { key: resource, snapshot } = schemaFixture("location");
  snapshot.metadata = { ...(snapshot.metadata as object), host_label: "private-body-value" };
  expect(decodedSnapshot(resource, snapshot).snapshot.metadata).not.toHaveProperty("host_label");
});

test.each([
  [
    "paging metadata",
    (snapshot: NavigationSnapshot) => {
      snapshot.metadata = { ...(snapshot.metadata as object), total: 9 };
    },
  ],
  [
    "an entity record",
    (snapshot: NavigationSnapshot) => {
      const first = snapshot.entities[0];
      if (!first) throw new Error("missing entity");
      snapshot.entities[0] = { ...first, hint: "private-body-value" } as typeof first;
    },
  ],
  [
    "a container owner",
    (snapshot: NavigationSnapshot) => {
      const owned = snapshot.containers.find((container) => container.owner.kind === "entity");
      if (!owned) throw new Error("missing owned container");
      owned.owner = { ...owned.owner, hint: "private-owner" } as typeof owned.owner;
    },
  ],
  [
    "the snapshot record",
    (snapshot: NavigationSnapshot) => {
      (snapshot as unknown as Record<string, unknown>).hint = "private-body-value";
    },
  ],
] as const)("codec still refuses an unknown key on %s", (_name, mutate) => {
  const snapshot = liveSnapshot();
  mutate(snapshot);
  expectContentFreeRejection(key, snapshot);
});

test("a delta that changes only an unknown key keeps the installed entity", () => {
  const snapshot = liveSnapshot();
  const installed = snapshotResource(key, decodedSnapshot(key, snapshot));
  const entity = snapshot.entities[0];
  if (!entity) throw new Error("missing entity");
  const decoded = decodeNavigationResponse(key, base, {
    status: "ok",
    representation: "delta",
    generationId: "g",
    revision: 2,
    etag: "tag-2",
    base,
    data: {
      metadata: { ...(snapshot.metadata as object), revision: 2 },
      upsertedEntities: [{ ...entity, value: { ...(entity.value as object), future_session_key: futureValue } }],
      removedEntityKeys: [],
      upsertedContainers: [],
      removedContainerKeys: [],
    },
  });
  if (decoded.status !== "delta") throw new Error(`expected a delta, got ${decoded.status}`);
  expect(decoded.delta.upsertedEntities[0]?.value).not.toHaveProperty("future_session_key");
  const applied = applyDelta(installed, decoded.delta, decoded.version);
  expect(applied.graph.entities.get(entity.key)).toBe(installed.graph.entities.get(entity.key));
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

test("invalid delta preserves the underlying decoder cause", () => {
  const cause = new TypeError("decoder sentinel");
  const delta = {
    get upsertedEntities(): never {
      throw cause;
    },
    removedEntityKeys: [],
    upsertedContainers: [],
    removedContainerKeys: [],
  };
  let thrown: unknown;
  try {
    decodeNavigationResponse(key, base, {
      status: "ok",
      representation: "delta",
      ...base,
      base,
      data: delta,
    });
  } catch (error) {
    thrown = error;
  }

  expect(thrown).toBeInstanceOf(NavigationBaseInvalidError);
  expect((thrown as NavigationBaseInvalidError).cause).toBe(cause);
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

// The first schema fixture of a resource kind, as a copy the caller may change.
function schemaFixture(kind: ResourceKey["kind"]): SnapshotFixture {
  const fixture = schemaFixtures().find((candidate) => candidate.key.kind === kind);
  if (!fixture) throw new Error(`missing ${kind} fixture`);
  return { key: fixture.key, snapshot: cloneSnapshot(fixture.snapshot) };
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

// The projector emits a project summary with the sources that own its rows
// ("local" for this hub's own sessions, a configured host name for each remote
// host's) whenever it is not controller-only, because every project-level
// mutation is keyed by (source, project ID). The normalizer emits that summary
// verbatim as the catalog entity's value, and entity validation is fail-closed
// — one unreadable row rejects the resource — so the catalog only decodes at
// all if the key is admitted with the projector's own bounds: at most one
// entry per source the inputs admit, each a non-empty identity.
test("codec decodes a catalog whose project rows carry owning sources", () => {
  const projects: ResourceKey = { kind: "catalog", catalog: "projects", offset: 0, limit: 100 };
  const mergedEntity = entityKey(projects, "3");
  const remoteEntity = entityKey(projects, "4");
  const catalogSnapshot = (projectRows: Array<Record<string, unknown>>): NavigationSnapshot => ({
    metadata: { generation_id: "g", revision: 1, offset: 0, limit: 100, remaining: 0 },
    entities: projectRows.map((value, index) => ({
      key: index === 0 ? mergedEntity : remoteEntity,
      kind: "project",
      value,
    })),
    containers: [
      {
        key: navigationRootContainerKey(projects, "projects"),
        owner: { kind: "resource_root", slot: "projects" },
        children: [mergedEntity, remoteEntity],
      },
    ],
  });
  // The exact summary shape a merged project (a local checkout plus a remote
  // host's clone under the same canonical ID and path) marshals to, beside the
  // remote-only shape that names a single host.
  const mergedProject = {
    key: "p-merged",
    name: "Merged Project",
    working_dir: "/shared/proj",
    rollup_state: "working",
    rollup_live: 2,
    rollup_attn: 1,
    default_expanded: true,
    more_recent: 1,
    worktrees: 2,
    is_archived: false,
    favorite: true,
    sources: ["local", "host-a"],
    session_count: 3,
  };
  const remoteProject = { key: "p-remote", name: "Remote Project", sources: ["host-a"], session_count: 1 };
  const decodeCatalog = (projectRows: Array<Record<string, unknown>>) =>
    decodeNavigationResponse(projects, undefined, snapshotResponse(projects, catalogSnapshot(projectRows)));
  expect(decodeCatalog([mergedProject, remoteProject]).status).toBe("snapshot");
  // A controller-only summary omits the field; an explicitly empty list is the
  // same default the hub's decision readers apply to an empty source list.
  expect(decodeCatalog([mergedProject, { ...remoteProject, sources: [] }]).status).toBe("snapshot");
  // Each entry is a decision key, so the resource is rejected outright rather
  // than one row being dropped: an empty or unbounded identity, a non-string,
  // a bare string rather than a list, and the manifest's object shape (which
  // describes the connected sources, not a project's owners) all fail closed.
  for (const sources of [
    "",
    "local",
    null,
    {},
    [""],
    ["local", ""],
    ["local", 7],
    ["local", undefined],
    ["local", "x".repeat(1025)],
    [{ id: "host-a", label: "Host A", kind: "remote", online: true }],
    Array.from({ length: 66 }, (_, index) => `host-${index}`),
  ]) {
    expect(() => decodeCatalog([mergedProject, { ...remoteProject, sources }])).toThrow();
  }
});

// The manifest's `sources` and a project summary's `sources` share a key name
// but nothing else: the manifest carries connected-source records for the
// source rail, a project carries the owner names its mutation is keyed by. Each
// shape must reject the other so neither test can pass by validating the wrong
// contract.
test("codec keeps the manifest's source records and a project's owner names apart", () => {
  const fixtures = schemaFixtures();
  const manifest = fixtures.find((fixture) => fixture.key.kind === "manifest");
  if (manifest?.key.kind !== "manifest") throw new Error("missing manifest fixture");
  const withOwnerNames = cloneSnapshot(manifest.snapshot);
  withOwnerNames.metadata = { ...(withOwnerNames.metadata as object), sources: ["local", "host-a"] };
  expectContentFreeRejection(manifest.key, withOwnerNames);
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

test("codec rejects an armed omitted count above the omitted watch total", () => {
  const session = entityKey(key, "1");
  const snapshotWithOmitted = (omitted: number | undefined, armed: number | undefined): NavigationSnapshot => ({
    ...liveSnapshot(),
    entities: [
      {
        key: session,
        kind: "session",
        value: {
          ...sessionValue("local:session"),
          ...(omitted === undefined ? {} : { omitted_watches: omitted }),
          ...(armed === undefined ? {} : { omitted_armed_watches: armed }),
        },
      },
    ],
  });
  const statusFor = (omitted: number | undefined, armed: number | undefined) =>
    decodeNavigationResponse(key, undefined, snapshotResponse(key, snapshotWithOmitted(omitted, armed))).status;

  expect(() => statusFor(1, 2)).toThrow();
  expect(statusFor(2, 2)).toBe("snapshot");
  expect(statusFor(3, 1)).toBe("snapshot");
  expect(statusFor(1, undefined)).toBe("snapshot");
  expect(statusFor(undefined, 0)).toBe("snapshot");
  // An absent total is zero, not unknown: the Go schema rejects this shape too.
  expect(() => statusFor(undefined, 1)).toThrow();
});

// S4: a live row carries when its last turn ended and whether that turn is
// unseen. The codec accepts both and refuses a malformed value, which the hub
// never sends.
test("codec accepts a row's turn end and unseen mark and refuses malformed ones", () => {
  const session = entityKey(key, "1");
  const statusFor = (extra: Record<string, unknown>) =>
    decodeNavigationResponse(
      key,
      undefined,
      snapshotResponse(key, {
        ...liveSnapshot(),
        entities: [{ key: session, kind: "session", value: { ...sessionValue("local:session"), ...extra } }],
      }),
    ).status;

  expect(statusFor({ turn_ended_at: "2026-09-26T11:58:00.123Z", unseen: true })).toBe("snapshot");
  expect(() => statusFor({ turn_ended_at: "yesterday" })).toThrow();
  expect(() => statusFor({ unseen: "yes" })).toThrow();
});

// A snapshot whose one session value carries `value` under `field`, for the
// nested value records below.
const snapshotWithSessionField = (field: string, value: unknown): NavigationSnapshot => ({
  ...liveSnapshot(),
  entities: [
    { key: entityKey(key, "1"), kind: "session", value: { ...sessionValue("local:session"), [field]: value } },
  ],
});

// A live session's task-list progress (S13a) is a nested value record the hub
// carries only when the list is non-empty. The codec keeps every key it knows,
// drops a key inside the record that it does not know, and holds the record to
// the hub schema's bounds (navigation_schema.go navigationTaskProgressValid).
test("codec keeps a session's task progress and drops keys inside it that it does not know", () => {
  const tasks = { total: 7, done: 2, cancelled: 1, current_id: 4, current: "Fix the settle/drain race" };
  const rows = materializeSnapshot(
    key,
    decodedSnapshot(key, snapshotWithSessionField("tasks", { ...tasks, future_task_key: futureValue })),
  ).sessions as Array<Record<string, unknown>>;
  expect(rows[0]?.tasks).toEqual(tasks);
  const onTheBounds = {
    total: Number.MAX_SAFE_INTEGER,
    done: Number.MAX_SAFE_INTEGER,
    current_id: Number.MAX_SAFE_INTEGER,
    current: "😀".repeat(512),
  };
  expect(decodedSnapshot(key, snapshotWithSessionField("tasks", onTheBounds)).snapshot.entities[0]?.value).toEqual({
    ...sessionValue("local:session"),
    tasks: onTheBounds,
  });
});

test.each([
  ["null", null],
  ["a list in place of the record", [{ total: 1, done: 0 }]],
  ["a missing total", { done: 0 }],
  ["a missing done count", { total: 1 }],
  ["a negative total", { total: -1, done: 0 }],
  ["a fractional count", { total: 1.5, done: 0 }],
  ["a count beyond the safe range", { total: Number.MAX_SAFE_INTEGER + 1, done: 0 }],
  ["a string count", { total: "7", done: 0 }],
  ["a negative cancelled count", { total: 1, done: 0, cancelled: -1 }],
  ["a negative current id", { total: 1, done: 0, current_id: -1 }],
  ["more done than exist", { total: 2, done: 3 }],
  ["more settled than exist", { total: 2, done: 1, cancelled: 2 }],
  ["an over-long current task", { total: 1, done: 0, current_id: 1, current: "t".repeat(513) }],
] as const)("codec refuses task progress with %s", (_name, tasks) => {
  expectContentFreeRejection(key, snapshotWithSessionField("tasks", tasks));
});

// A live root's whole-tree subagent tally (S3) is a nested value record the hub
// carries only when the tree has a subagent. The codec keeps every count it
// knows, drops a key inside the record that it does not know, and holds each
// count to the hub schema's bound (navigation_schema.go
// navigationSubagentTallyValid).
test("codec keeps a session's subagent tally and drops keys inside it that it does not know", () => {
  const subagents = { running: 2, failed: 1, done: 57 };
  const rows = materializeSnapshot(
    key,
    decodedSnapshot(key, snapshotWithSessionField("subagents", { ...subagents, future_tally_key: futureValue })),
  ).sessions as Array<Record<string, unknown>>;
  expect(rows[0]?.subagents).toEqual(subagents);
  const onTheBounds = { running: Number.MAX_SAFE_INTEGER, failed: 0, done: Number.MAX_SAFE_INTEGER };
  expect(decodedSnapshot(key, snapshotWithSessionField("subagents", onTheBounds)).snapshot.entities[0]?.value).toEqual({
    ...sessionValue("local:session"),
    subagents: onTheBounds,
  });
});

test.each([
  ["null", null],
  ["a number in place of the record", 3],
  ["a list in place of the record", [{ running: 1, failed: 0, done: 0 }]],
  ["a missing running count", { failed: 0, done: 0 }],
  ["a missing failed count", { running: 2, done: 57 }],
  ["a missing done count", { running: 2, failed: 1 }],
  ["a negative count", { running: 2, failed: -1, done: 57 }],
  ["a fractional count", { running: 1.5, failed: 0, done: 0 }],
  ["a count beyond the safe range", { running: Number.MAX_SAFE_INTEGER + 1, failed: 0, done: 0 }],
  ["a string count", { running: "2", failed: 0, done: 0 }],
] as const)("codec refuses a subagent tally with %s", (_name, subagents) => {
  expectContentFreeRejection(key, snapshotWithSessionField("subagents", subagents));
});

// A live row asking a question names it (S1b): a nested value record the hub
// carries only while the row's ask flag is set. The codec keeps it, drops a
// key inside it that it does not know, and holds it to the hub schema's
// bounds (navigation_schema.go navigationQuestionValid).
test("codec keeps a row's pending question and drops keys inside it that it does not know", () => {
  const question = { text: "Keep or drop the implied options?", options: ["Drop them", "Keep them"], count: 2 };
  const rows = materializeSnapshot(
    key,
    decodedSnapshot(key, snapshotWithSessionField("question", { ...question, future_question_key: futureValue })),
  ).sessions as Array<Record<string, unknown>>;
  expect(rows[0]?.question).toEqual(question);
  const onTheBounds = {
    text: "😀".repeat(200),
    options: ["😀".repeat(80), "b", "c", "d", "e"],
    count: Number.MAX_SAFE_INTEGER,
  };
  expect(decodedSnapshot(key, snapshotWithSessionField("question", onTheBounds)).snapshot.entities[0]?.value).toEqual({
    ...sessionValue("local:session"),
    question: onTheBounds,
  });
});

test.each([
  ["null", null],
  ["a string in place of the record", "Keep or drop?"],
  ["a missing text", { count: 1 }],
  ["an empty text", { text: "", count: 1 }],
  ["an over-long text", { text: "t".repeat(201), count: 1 }],
  ["a missing count", { text: "Which?" }],
  ["no question counted", { text: "Which?", count: 0 }],
  ["a fractional count", { text: "Which?", count: 1.5 }],
  ["six options", { text: "Which?", options: ["a", "b", "c", "d", "e", "f"], count: 1 }],
  ["an empty option", { text: "Which?", options: [""], count: 1 }],
  ["an over-long option", { text: "Which?", options: ["o".repeat(81)], count: 1 }],
  ["a non-string option", { text: "Which?", options: [7], count: 1 }],
  ["options that are not a list", { text: "Which?", options: "a", count: 1 }],
] as const)("codec refuses a pending question with %s", (_name, question) => {
  expectContentFreeRejection(key, snapshotWithSessionField("question", question));
});

// A Failed row says why (S1c): a nested value record of the failure's title and
// its cause's kind, provider and status. The codec keeps it, drops a key
// inside it that it does not know, and holds it to the hub schema's bounds
// (navigation_schema.go navigationFailureValid).
test("codec keeps a row's failure summary and drops keys inside it that it does not know", () => {
  const failure = { title: "Provider error", cause_kind: "provider", provider: "codex-jesse-fsck.com", status: 401 };
  const rows = materializeSnapshot(
    key,
    decodedSnapshot(key, snapshotWithSessionField("failure", { ...failure, future_failure_key: futureValue })),
  ).sessions as Array<Record<string, unknown>>;
  expect(rows[0]?.failure).toEqual(failure);
  for (const kept of [
    { cause_kind: "crashed" },
    {
      title: "😀".repeat(80),
      cause_kind: "k".repeat(1024),
      provider: "p".repeat(1024),
      status: Number.MAX_SAFE_INTEGER,
    },
  ]) {
    expect(decodedSnapshot(key, snapshotWithSessionField("failure", kept)).snapshot.entities[0]?.value).toEqual({
      ...sessionValue("local:session"),
      failure: kept,
    });
  }
});

test.each([
  ["null", null],
  ["a string in place of the record", "Provider error"],
  ["nothing to say", {}],
  ["a status alone", { status: 500 }],
  ["an empty title", { title: "" }],
  ["an over-long title", { title: "t".repeat(81) }],
  ["an empty cause kind", { cause_kind: "" }],
  ["an over-long provider", { cause_kind: "provider", provider: "p".repeat(1025) }],
  ["a negative status", { cause_kind: "provider", status: -1 }],
  ["a string status", { cause_kind: "provider", status: "401" }],
] as const)("codec refuses a failure summary with %s", (_name, failure) => {
  expectContentFreeRejection(key, snapshotWithSessionField("failure", failure));
});

// A row carries the opening of its session's last agent message (S1d). The
// codec keeps one within the hub schema's bound and refuses anything else.
test("codec keeps a row's last message within its bound and refuses one past it", () => {
  const onTheBound = "😀".repeat(200);
  expect(
    decodedSnapshot(key, snapshotWithSessionField("last_message", onTheBound)).snapshot.entities[0]?.value,
  ).toEqual({ ...sessionValue("local:session"), last_message: onTheBound });
  for (const malformed of ["", "t".repeat(201), 7, ["Three layouts are ready."]]) {
    expectContentFreeRejection(key, snapshotWithSessionField("last_message", malformed));
  }
});

// A row names its session's model (S17). The codec keeps a name within the hub
// schema's label bound and refuses anything else.
test("codec keeps a row's model name within its bound and refuses one past it", () => {
  const onTheBound = "😀".repeat(512);
  expect(decodedSnapshot(key, snapshotWithSessionField("model_name", onTheBound)).snapshot.entities[0]?.value).toEqual({
    ...sessionValue("local:session"),
    model_name: onTheBound,
  });
  for (const malformed of ["", "m".repeat(513), 7, ["Gpt 5.6"]]) {
    expectContentFreeRejection(key, snapshotWithSessionField("model_name", malformed));
  }
});

// #2477: a value record's key table is mapped over its generated interface, so
// a key the interface drops, a key missing from a table, and a key whose
// required/optional mark does not match the interface's `?` all stop
// compiling. These deliberate drifts pin that; an unused directive means the
// mapped type stopped rejecting one.
type SampleKeyTable = ValueRecordKeyTable<{ required: string; optional?: number }>;
const sampleKeyTable: SampleKeyTable = { required: "required", optional: "optional" };
// @ts-expect-error a key the interface marks required must read "required"
const sampleFlippedToOptional: SampleKeyTable = { required: "optional", optional: "optional" };
// @ts-expect-error a key the interface marks optional must read "optional"
const sampleFlippedToRequired: SampleKeyTable = { required: "required", optional: "required" };
// @ts-expect-error a key the interface does not name must not appear
const sampleExtraKey: SampleKeyTable = { required: "required", optional: "optional", gone: "optional" };
// @ts-expect-error every key the interface names must appear
const sampleMissingKey: SampleKeyTable = { required: "required" };
// @ts-expect-error every key of the real generated summary must be listed
const missingSummaryKey: ValueRecordKeyTable<NavigationSessionSummary> = { ref: "required" };
void [
  sampleKeyTable,
  sampleFlippedToOptional,
  sampleFlippedToRequired,
  sampleExtraKey,
  sampleMissingKey,
  missingSummaryKey,
];

// cmd/evener-hub/navigation_value_records_test.go keeps this fixture naming
// every wire field of every navigation value record. Decoding it must keep all
// of them: a field the hub sends that the codec does not list would be dropped
// without a sound, and this is the test that hears it.
test("codec keeps every field the hub's value records carry", () => {
  for (const [kind, record] of [
    ["section", valueRecords.session],
    ["catalog", valueRecords.project],
    ["pin_catalog", valueRecords.pin_section],
  ] as const) {
    const { key: resource, snapshot } = schemaFixture(kind);
    const first = snapshot.entities[0];
    if (!first) throw new Error("missing entity");
    first.value = record;
    expect(decodedSnapshot(resource, snapshot).snapshot.entities[0]?.value).toEqual(record);
  }

  const manifest = schemaFixture("manifest");
  manifest.snapshot.metadata = valueRecords.manifest;
  expect(decodedSnapshot(manifest.key, manifest.snapshot).snapshot.metadata).toEqual(valueRecords.manifest);

  // A location's metadata names its resource, so the resource here is built
  // from the fixture's own ref.
  const location: ResourceKey = { kind: "location", ref: String(valueRecords.location.ref) };
  const locationDecoded = decodedSnapshot(location, {
    metadata: valueRecords.location,
    entities: [],
    containers: [
      {
        key: navigationRootContainerKey(location, "session"),
        owner: { kind: "resource_root", slot: "session" },
        children: [],
      },
    ],
  });
  expect(locationDecoded.snapshot.metadata).toEqual(valueRecords.location);
});

// A snapshot read used to run each session value's full validator five times:
// once in validateSnapshotForResource's entity loop, twice inside the
// validateGraphForResource it then called, and twice more when merge's
// reconcileSnapshot re-validated the graph decode had already validated
// (#2478). Decode now leaves the entity pass to validateGraphForResource, but
// reconcileSnapshot still validates the graph it installs because it is an
// exported entry a caller can hand a resource built outside decode. That
// leaves two passes. The rfc3339 check runs once per session value and never
// on the copies decode and merge take, so counting its regex counts the
// validator itself.
test("a snapshot read runs each session value's validator twice, not five times", () => {
  const snapshot = liveSnapshot();
  const first = snapshot.entities[0];
  if (!first) throw new Error("missing entity");
  first.value = { ...(first.value as object), updated_at: "2026-01-02T03:04:05Z" };

  const timestampChecks: number[] = [];
  const originalExec = RegExp.prototype.exec;
  const spy = vi.spyOn(RegExp.prototype, "exec").mockImplementation(function (this: RegExp, value: string) {
    if (this.source.startsWith("^(\\d{4})-")) timestampChecks.push(1);
    return originalExec.call(this, value);
  });

  try {
    const decoded = decodeNavigationResponse(key, undefined, snapshotResponse(key, snapshot));
    if (decoded.status !== "snapshot") throw new Error(`expected a snapshot, got ${decoded.status}`);
    reconcileSnapshot(null, snapshotResource(key, decoded));
  } finally {
    spy.mockRestore();
  }

  expect(timestampChecks).toHaveLength(2);
});

test("an archived list decodes rows with nested fork-original children", () => {
  const rows = [
    completeSession({
      ref: "local:root",
      updated_at: "2026-09-01T00:00:00Z",
      favorite: true,
      children: [{ ref: "local:original", kind: "fork" }],
    }),
    completeSession({ ref: "devbox:remote", host_id: "devbox", offline: true }),
  ];
  expect(decodeArchivedListSessions(rows)).toEqual(rows);
});

test("an archived list rejects a malformed row, a malformed child, and a non-array", () => {
  const missingRef = { ...completeSession({ ref: "local:root" }) };
  delete missingRef.ref;
  const badChild = {
    ...completeSession({ ref: "local:root" }),
    children: [{ ...completeSession({ ref: "local:child" }), session_id: undefined }],
  };
  for (const value of [[missingRef], [badChild], { sessions: [] }, null]) {
    expect(() => decodeArchivedListSessions(value)).toThrow("navigation protocol: invalid archived list");
  }
});

test("an archived list rejects children nested deeper than the navigation depth bound", () => {
  let row = completeSession({ ref: "local:leaf" });
  for (let depth = 0; depth < 40; depth++) {
    row = completeSession({ ref: `local:level-${depth}`, children: [row] });
  }
  expect(() => decodeArchivedListSessions([row])).toThrow("navigation protocol: invalid archived list");
});
