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
const ROOT_A = summary({
  ref: "local:a",
  title: "A",
  state: "active",
  children: [CHILD_B, CHILD_C],
  more_subagents: 201,
});
const ROOT_D = summary({ ref: "local:d", title: "D", state: "idle" });

function stateWith(...entries: [ResourceKey, unknown][]): NavigationStoreState {
  return { ...emptyState(), resources: new Map(entries.map(([key, data]) => [keyID(key), resource(key, data)])) };
}

const LIVE_KEY: ResourceKey = { kind: "section", section: "live", offset: 0, limit: 50 };
const locationKey = (ref: string): ResourceKey => ({ kind: "location", ref });

function fullState(): NavigationStoreState {
  return stateWith(
    [LIVE_KEY, { sessions: [ROOT_A, ROOT_D] }],
    [locationKey("local:a"), { ref: "local:a", top_level_ref: "local:a", top_level: true, session: ROOT_A }],
    [locationKey("local:b"), { ref: "local:b", top_level_ref: "local:a", top_level: false, session: CHILD_B }],
  );
}

describe("scopeCounts", () => {
  test("counts active work only, never the completed fold", () => {
    const counts = scopeCounts(ROOT_A);
    expect(counts.activeSubagents).toBe(1); // B is current, C folds
    expect(counts.runningJobs).toBe(0);
    expect(counts.armedWatches).toBe(0);
    expect(counts.tasksTotal).toBe(0);
  });

  test("reads the leaf's own jobs, watches, and tasks", () => {
    const counts = scopeCounts(CHILD_B);
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
    expect(scopeCounts(s).armedWatches).toBe(2);
  });

  test("fork originals in children never count as agents", () => {
    // The wire's children carry fork originals (kind "fork") beside
    // subagents; the chip counts subagents only, or a live fork inflates it.
    const s = summary({
      ref: "local:x",
      title: "X",
      children: [
        summary({ ref: "local:fork", title: "Fork", state: "active", kind: "fork" }),
        summary({ ref: "local:sub", title: "Sub", state: "active", kind: "subagent" }),
      ],
    });
    expect(scopeCounts(s).activeSubagents).toBe(1);
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
  test("assembles leaf, path, and counts", () => {
    const scope = deriveScope(fullState(), "local:b");
    expect(scope?.leaf.ref).toBe("local:b");
    expect(scope?.path.map((c) => c.title)).toEqual(["A", "B"]);
    expect(scope?.counts.runningJobs).toBe(1);
    expect(scope?.counts.armedWatches).toBe(1);
  });

  test("returns null for an unknown ref", () => {
    expect(deriveScope(fullState(), "local:absent")).toBeNull();
  });
});
