// Regression tests for the navigation codec's session-entity schema: a live
// hub projects each session's armed watches onto its summary (`watches` on the
// entity value, see navigation_projection.go's navigationWatches). The codec's
// session allowlist/validator must accept that field, or every frame carrying a
// watch fails validation and the store silently keeps the pre-watch snapshot -
// the rail count and the Activity panel's Watches group then render nothing.
//
// The watch fixture is copied from a real AppWire frame (`appwire-2.jsonl`)
// captured during a live run: keys in wire order and RFC3339 instants included.
import { expect, test } from "vitest";
import { buildWatchRows } from "../../protocol/activityRows";
import type { NavigationSnapshot, NavigationWatchSummary } from "../../protocol/types.gen";
import { armedWatchCount } from "../../shell/rail/railNodes";
import { decodeNavigationResponse, materializeNavigationResource, normalizedGraphFromSnapshot } from "./codec";
import { applyDelta } from "./merge";
import { selectRailModel } from "./selectors";
import {
  navigationOwnedContainerKey,
  navigationRootContainerKey,
  navigationViewScope,
  type ResourceKey,
} from "./types";

const key = { kind: "section", section: "live", offset: 0, limit: 50 } as const;
const base = { generationId: "g", revision: 1, etag: "tag-1" };

const entityKey = (resource: ResourceKey, digit: string) =>
  `${navigationViewScope(resource)}/entity/${digit.repeat(64)}`;

// The two watches the live run armed: one progress watch on a job, one hourly
// self-watch. Exactly the wire shape the hub sent (deliveries 0, active true).
const LIVE_WATCHES: NavigationWatchSummary[] = [
  {
    id: "watch_034NnUweXeqLfzEzRxbrMK",
    source: "job_034NnSnRLFjNWPnAO6mLLk_ous2I5jk4JD2",
    target: "job_034NnSnRLFjNWPnAO6mLLk_ous2I5jk4JD2",
    note: "Poll the queue depth",
    cadence: [{ kind: "progress", seconds: 1 }],
    deliveries: 0,
    created_at: "2026-09-12T23:37:19.356907592Z",
    active: true,
  },
  {
    id: "watch_034NnUwj1O1QNVww6cRmOY",
    source: "self",
    target: "caller",
    note: "Hourly sweep",
    cadence: [{ kind: "every", seconds: 60 }],
    deliveries: 0,
    created_at: "2026-09-12T23:37:19.402124579Z",
    active: true,
  },
];

function sessionValue(ref: string, watches?: NavigationWatchSummary[]) {
  return {
    ref,
    host_id: "local",
    session_id: ref.slice(ref.indexOf(":") + 1),
    title: "Session",
    project: "demo",
    state: "active",
    kind: "session",
    live: true,
    children: [],
    ...(watches ? { watches } : {}),
  };
}

function liveSnapshot(resource: ResourceKey = key, watches?: NavigationWatchSummary[]): NavigationSnapshot {
  const session = entityKey(resource, "1");
  return {
    metadata: { generation_id: "g", revision: 1, offset: 0, limit: 50, remaining: 0, truncated: false },
    entities: [{ key: session, kind: "session", value: sessionValue("local:session", watches) }],
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

const snapshotResponse = (_resource: ResourceKey, data: NavigationSnapshot) => ({
  status: "ok",
  representation: "snapshot",
  ...base,
  data,
});

test("a session entity carrying watches decodes and materializes the watch list", () => {
  const decoded = decodeNavigationResponse(key, undefined, snapshotResponse(key, liveSnapshot(key, LIVE_WATCHES)));
  expect(decoded.status).toBe("snapshot");
  if (decoded.status !== "snapshot") throw new Error("fixture is incomplete");

  const normalized = {
    key,
    graph: normalizedGraphFromSnapshot(decoded.snapshot),
    version: decoded.version,
    presence: "present" as const,
  };
  const materialized = materializeNavigationResource(normalized) as {
    sessions: Array<{ watches?: NavigationWatchSummary[] }>;
  };
  expect(materialized.sessions[0]?.watches).toEqual(LIVE_WATCHES);

  // The rail consumes the graph through selectRailModel, whose RailSession must
  // carry the same list; the panel reads the same field off the session object.
  const railSession = [...selectRailModel(normalized).sessions.values()][0];
  expect(railSession?.watches).toEqual(LIVE_WATCHES);
  expect(armedWatchCount(railSession?.watches)).toBe(2);
  const rows = buildWatchRows(materialized.sessions[0]?.watches);
  expect(rows.map((row) => row.watch.note)).toEqual(["Poll the queue depth", "Hourly sweep"]);
});

test("an events cadence carrying its throttle and filter decodes and survives", () => {
  const eventsWatch: NavigationWatchSummary = {
    id: "watch_events",
    source: "self",
    target: "caller",
    note: "Error watch",
    cadence: [{ kind: "events", every: 3, filter: "tool_name=Bash, status=error" }],
    deliveries: 0,
    created_at: "2026-09-12T23:37:19.402124579Z",
    active: true,
  };
  const decoded = decodeNavigationResponse(key, undefined, snapshotResponse(key, liveSnapshot(key, [eventsWatch])));
  expect(decoded.status).toBe("snapshot");
  if (decoded.status !== "snapshot") throw new Error("fixture is incomplete");

  const normalized = {
    key,
    graph: normalizedGraphFromSnapshot(decoded.snapshot),
    version: decoded.version,
    presence: "present" as const,
  };
  const materialized = materializeNavigationResource(normalized) as {
    sessions: Array<{ watches?: NavigationWatchSummary[] }>;
  };
  expect(materialized.sessions[0]?.watches?.[0]?.cadence).toEqual([
    { kind: "events", every: 3, filter: "tool_name=Bash, status=error" },
  ]);
});

test("a delta upserting a session with watches keeps the field", () => {
  const previous = {
    key,
    graph: normalizedGraphFromSnapshot(liveSnapshot()),
    version: base,
    presence: "present" as const,
  };
  const session = entityKey(key, "1");
  const nextBase = { generationId: "g", revision: 2, etag: "tag-2" };
  const delta = {
    metadata: { generation_id: "g", revision: 2, offset: 0, limit: 50, remaining: 0, truncated: false },
    upsertedEntities: [{ key: session, kind: "session", value: sessionValue("local:session", LIVE_WATCHES) }],
    removedEntityKeys: [],
    upsertedContainers: [],
    removedContainerKeys: [],
  };
  const next = applyDelta(previous, delta, nextBase);
  const materialized = materializeNavigationResource(next) as {
    sessions: Array<{ watches?: NavigationWatchSummary[] }>;
  };
  expect(materialized.sessions[0]?.watches).toEqual(LIVE_WATCHES);
});

test("malformed watch rows are still rejected", () => {
  const missingID = structuredClone(liveSnapshot(key, LIVE_WATCHES)) as unknown as {
    entities: Array<{ value: Record<string, unknown> }>;
  };
  (missingID.entities[0]!.value.watches as Array<Record<string, unknown>>)[0]!.id = 7;
  expect(() =>
    decodeNavigationResponse(key, undefined, snapshotResponse(key, missingID as unknown as NavigationSnapshot)),
  ).toThrow();

  const noActive = structuredClone(liveSnapshot(key, LIVE_WATCHES)) as unknown as {
    entities: Array<{ value: Record<string, unknown> }>;
  };
  delete (noActive.entities[0]!.value.watches as Array<Record<string, unknown>>)[0]!.active;
  expect(() =>
    decodeNavigationResponse(key, undefined, snapshotResponse(key, noActive as unknown as NavigationSnapshot)),
  ).toThrow();

  const badSeconds = structuredClone(liveSnapshot(key, LIVE_WATCHES)) as unknown as {
    entities: Array<{ value: Record<string, unknown> }>;
  };
  (
    (badSeconds.entities[0]!.value.watches as Array<Record<string, unknown>>)[0]!.cadence as Array<
      Record<string, unknown>
    >
  )[0]!.seconds = "1";
  expect(() =>
    decodeNavigationResponse(key, undefined, snapshotResponse(key, badSeconds as unknown as NavigationSnapshot)),
  ).toThrow();
});
