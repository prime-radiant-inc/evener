// statusScope derives the activity surfaces' scope - the path of sessions from
// the root to the leaf you're reading, and the four activity counts - from the
// navigation store. No new activity state anywhere: the store is the truth.

import type { NavigationSessionSummary, NavigationWatchSummary } from "@evener/appwire-client";
import {
  createNavigationStore,
  keyID,
  type NavigationStoreState,
  type ResourceKey,
  type ResourceState,
} from "@evener/appwire-client/state/navigation";
import { memoryNavigationPersistence } from "@evener/appwire-client/testing/navigationPersistence";
import { describe, expect, test } from "vitest";
import { deriveScope, scopeCounts, scopePath } from "./statusScope";

function emptyState(): NavigationStoreState {
  return createNavigationStore({ persistence: memoryNavigationPersistence() }).getState();
}

function resource(key: ResourceKey, data: unknown): ResourceState {
  return {
    key,
    data,
    loadedRevision: 1,
    targetRevision: 1,
    forceToken: 0,
    etag: "tag",
    loading: false,
    stale: false,
    error: null,
    generationID: "generation_test",
  } as ResourceState;
}

function summary(
  partial: Partial<NavigationSessionSummary> & { ref: string; title: string },
): NavigationSessionSummary {
  return {
    host_id: "local",
    session_id: partial.ref,
    project: "evener",
    state: "idle",
    kind: "session",
    live: true,
    children: [],
    ...partial,
  } as NavigationSessionSummary;
}

const WATCH: NavigationWatchSummary = {
  id: "w1",
  source: "self",
  deliveries: 0,
  created_at: "2026-09-29T10:00:00Z",
  active: true,
};

// The fixture tree: root A with children B (active: 1 running job, 1 armed
// watch, 2/5 tasks) and C (idle), and 201 more subagents the wire did not
// carry. Root D is unrelated.
const CHILD_B = summary({
  ref: "local:b",
  title: "B",
  state: "active",
  kind: "subagent",
  running_jobs: [{ job_id: "j1", job_type: "shell", status: "running", command: "go test ./..." }],
  watches: [WATCH],
  tasks: { total: 5, done: 2, current: "doing the thing" },
});
const CHILD_C = summary({ ref: "local:c", title: "C", state: "idle", kind: "subagent" });
// The wire's real shapes: list rows and locations are FLAT (no children), and
// the session's child tree arrives through the paged subagents resource -
// page 0 here, with 201 direct children beyond it accounted in `remaining`.
const ROOT_A = summary({
  ref: "local:a",
  title: "A",
  state: "active",
});
const ROOT_D = summary({ ref: "local:d", title: "D", state: "idle" });

function stateWith(...entries: [ResourceKey, unknown][]): NavigationStoreState {
  return { ...emptyState(), resources: new Map(entries.map(([key, data]) => [keyID(key), resource(key, data)])) };
}

const LIVE_KEY: ResourceKey = { kind: "section", section: "live", offset: 0, limit: 50 };
const locationKey = (ref: string): ResourceKey => ({ kind: "location", ref });
const subagentsKey = (ref: string, offset = 0): ResourceKey => ({ kind: "subagents", ref, offset, limit: 50 });

function fullState(): NavigationStoreState {
  return stateWith(
    [LIVE_KEY, { sessions: [ROOT_A, ROOT_D] }],
    [locationKey("local:a"), { ref: "local:a", top_level_ref: "local:a", top_level: true, session: ROOT_A }],
    [locationKey("local:b"), { ref: "local:b", top_level_ref: "local:a", top_level: false, session: CHILD_B }],
    [subagentsKey("local:a"), { sessions: [CHILD_B, CHILD_C], remaining: 201, truncated: false }],
  );
}

describe("scopeCounts", () => {
  test("counts active work only, never the completed fold", () => {
    // Agents come from the subagents resource's rows, never the (flat) leaf.
    const counts = scopeCounts(ROOT_A, { rows: [CHILD_B, CHILD_C], remaining: 201 });
    expect(counts.activeSubagents).toBe(1); // B is current, C folds
    expect(counts.runningJobs).toBe(0);
    expect(counts.armedWatches).toBe(0);
    expect(counts.tasksTotal).toBe(0);
  });

  test("an unloaded subagents resource counts zero agents, never children off the leaf", () => {
    const counts = scopeCounts(ROOT_A, null);
    expect(counts.activeSubagents).toBe(0);
  });

  test("reads the leaf's own jobs, watches, and tasks", () => {
    const counts = scopeCounts(CHILD_B, null);
    expect(counts.runningJobs).toBe(1);
    expect(counts.armedWatches).toBe(1);
    expect(counts.tasksDone).toBe(2);
    expect(counts.tasksTotal).toBe(5);
  });

  test("armed watches include the hub-omitted armed rows", () => {
    const s = summary({
      ref: "local:x",
      title: "X",
      watches: [WATCH],
      omitted_watches: 2,
      omitted_armed_watches: 1,
    });
    expect(scopeCounts(s, null).armedWatches).toBe(2);
  });

  test("fork originals in the page rows never count as agents", () => {
    // The page's rows carry fork originals (kind "fork") beside subagents;
    // the chip counts subagents only, or a live fork inflates it.
    const counts = scopeCounts(ROOT_A, {
      rows: [
        summary({ ref: "local:fork", title: "Fork", state: "active", kind: "fork" }),
        summary({ ref: "local:sub", title: "Sub", state: "active", kind: "subagent" }),
      ],
      remaining: 0,
    });
    expect(counts.activeSubagents).toBe(1);
  });
});

describe("scopePath", () => {
  test("walks from the root to a nested leaf", () => {
    expect(scopePath(fullState(), "local:b")).toEqual([
      { ref: "local:a", title: "A" },
      { ref: "local:b", title: "B" },
    ]);
  });

  test("a top-level session's path is itself", () => {
    expect(scopePath(fullState(), "local:a")).toEqual([{ ref: "local:a", title: "A" }]);
  });

  test("degrades to the leaf alone when the root's tree does not contain it", () => {
    // Location exists for B but the live section's root A carries no children
    // (a partially loaded tree).
    const rootlessA = summary({ ref: "local:a", title: "A", state: "active" });
    const state = stateWith(
      [LIVE_KEY, { sessions: [rootlessA] }],
      [locationKey("local:b"), { ref: "local:b", top_level_ref: "local:a", top_level: false, session: CHILD_B }],
    );
    expect(scopePath(state, "local:b")).toEqual([{ ref: "local:b", title: "B" }]);
  });

  test("returns null for a ref the store has never heard of", () => {
    expect(scopePath(fullState(), "local:absent")).toBeNull();
  });
});

describe("deriveScope", () => {
  test("assembles leaf, path, counts, and the subagents page", () => {
    const scope = deriveScope(fullState(), "local:b");
    expect(scope?.leaf.ref).toBe("local:b");
    expect(scope?.path.map((c) => c.title)).toEqual(["A", "B"]);
    expect(scope?.counts.runningJobs).toBe(1);
    expect(scope?.counts.armedWatches).toBe(1);
  });

  test("carries the leaf's subagents rows and the wire's remainder", () => {
    const scope = deriveScope(fullState(), "local:a");
    expect(scope?.subagents?.rows.map((row) => row.ref)).toEqual(["local:b", "local:c"]);
    expect(scope?.subagents?.remaining).toBe(201);
    expect(scope?.counts.activeSubagents).toBe(1);
  });

  test("a scope whose subagents resource has not loaded reads null, with zero agents counted", () => {
    const state = stateWith(
      [LIVE_KEY, { sessions: [ROOT_D] }],
      [locationKey("local:d"), { ref: "local:d", top_level_ref: "local:d", top_level: true, session: ROOT_D }],
    );
    const scope = deriveScope(state, "local:d");
    expect(scope?.subagents).toBeNull();
    expect(scope?.counts.activeSubagents).toBe(0);
  });

  test("concatenates every loaded page in offset order", () => {
    const extra = summary({ ref: "local:e", title: "E", state: "idle", kind: "subagent" });
    const state = stateWith(
      [LIVE_KEY, { sessions: [ROOT_A] }],
      [locationKey("local:a"), { ref: "local:a", top_level_ref: "local:a", top_level: true, session: ROOT_A }],
      [subagentsKey("local:a", 0), { sessions: [CHILD_B], remaining: 1, truncated: false }],
      [subagentsKey("local:a", 50), { sessions: [extra], remaining: 0, truncated: false }],
    );
    const scope = deriveScope(state, "local:a");
    expect(scope?.subagents?.rows.map((row) => row.ref)).toEqual(["local:b", "local:e"]);
    expect(scope?.subagents?.remaining).toBe(0);
  });

  test("returns null for an unknown ref", () => {
    expect(deriveScope(fullState(), "local:absent")).toBeNull();
  });
});
