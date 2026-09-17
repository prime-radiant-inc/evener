import { expect, test } from "vitest";
import { memoryNavigationPersistence } from "../../testing/navigationPersistence";
import type { NavigationManifest, NavigationSessionSummary, NavigationWatchSummary } from "../../types.gen";
import { normalizedGraphFromSnapshot } from "./codec";
import {
  selectDisplaySources,
  selectSessionOmittedArmedWatches,
  selectSessionOmittedWatches,
  selectSources,
} from "./selectors";
import { createNavigationStore } from "./store";
import { isSettledGone, keyID, navigationViewScope, type ResourceKey, type ResourceState } from "./types";

const emptyState = () => createNavigationStore({ persistence: memoryNavigationPersistence() }).getState();

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

function watchesState(
  title: string,
  watches: NavigationWatchSummary[],
  omittedWatches = 0,
  omittedArmedWatches = 0,
): ReturnType<typeof emptyState> {
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
    omitted_watches: omittedWatches,
    omitted_armed_watches: omittedArmedWatches,
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
  return { ...emptyState(), resources: new Map([[keyID(sectionKey), resource]]) };
}

// A ref that drops out of the session list must not keep a cache entry for the
// life of the page. Deleting the entry also drops its old array, so the next
// read recomputes instead of serving a stale identity.
test("selectSessionOmittedWatches reads the count off the summary and is zero when absent", () => {
  const watchRow: NavigationWatchSummary = {
    id: "w",
    source: "self",
    deliveries: 1,
    created_at: "2026-09-12T10:00:00Z",
    active: true,
  };
  expect(selectSessionOmittedWatches("local:s", watchesState("t", [watchRow], 5))).toBe(5);
  expect(selectSessionOmittedWatches("local:s", watchesState("t", [watchRow]))).toBe(0);
  expect(selectSessionOmittedWatches("local:absent", watchesState("t", [watchRow], 5))).toBe(0);
});

// The armed subset of the omitted rows is what the rail and the activity panel
// add to the retained armed count; it must be readable and zero when absent.
test("selectSessionOmittedArmedWatches reads the armed subset and is zero when absent", () => {
  const watchRow: NavigationWatchSummary = {
    id: "w",
    source: "self",
    deliveries: 1,
    created_at: "2026-09-12T10:00:00Z",
    active: true,
  };
  expect(selectSessionOmittedArmedWatches("local:s", watchesState("t", [watchRow], 5, 3))).toBe(3);
  expect(selectSessionOmittedArmedWatches("local:s", watchesState("t", [watchRow], 5))).toBe(0);
  expect(selectSessionOmittedArmedWatches("local:absent", watchesState("t", [watchRow], 5, 3))).toBe(0);
});

// selectSources feeds the host picker and the spawn form's launch target, so a
// retained snapshot must not be read as a launchable host list: on an
// invalidation or reconnect the resource keeps its previous data while
// loading/stale - and that data can list a host the fresh manifest has since
// removed or taken offline. Only a settled manifest may expose sources.
const manifestData: NavigationManifest = {
  generation_id: "generation_test",
  revision: 1,
  sources: [
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ],
  attentionSummary: { needsYou: 0, error: 0, working: 0 },
  sections: { live: { count: 0 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
  catalogs: { projects: { count: 0 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
};
function manifestResource(
  overrides: Partial<ResourceState<NavigationManifest>> = {},
): ResourceState<NavigationManifest> {
  return {
    key: { kind: "manifest" },
    data: manifestData,
    loadedRevision: 1,
    targetRevision: null,
    forceToken: 0,
    etag: '"test"',
    loading: false,
    stale: false,
    error: null,
    generationID: "generation_test",
    ...overrides,
  };
}
const sourcesOf = (manifest: ResourceState<NavigationManifest> | null) =>
  selectSources({ manifest } as unknown as Parameters<typeof selectSources>[0]);

test("selectSources exposes a settled manifest's sources and withholds an unsettled one", () => {
  expect(sourcesOf(manifestResource()).map((source) => source.id)).toEqual(["local", "buildbox"]);
  // A retained snapshot with no settled authority exposes nothing: loading,
  // stale (invalidation/reconnect), and errored all read as no sources.
  for (const overrides of [{ loading: true }, { stale: true }, { error: new Error("read failed") }]) {
    expect(sourcesOf(manifestResource(overrides))).toEqual([]);
  }
  expect(sourcesOf(null)).toEqual([]);
});

test("selectSources returns the same empty snapshot whenever it withholds sources", () => {
  // useSyncExternalStore compares snapshots by identity, so every withheld
  // state must share one stable array rather than allocate a fresh one.
  const withheld = sourcesOf(manifestResource({ stale: true }));
  expect(sourcesOf(manifestResource({ loading: true }))).toBe(withheld);
  expect(sourcesOf(manifestResource({ error: new Error("read failed") }))).toBe(withheld);
  expect(sourcesOf(null)).toBe(withheld);
});

// ...and the DISPLAY view beside it, which answers the other question: what does
// the reader's screen already know about the hosts? Withholding the retained
// snapshot there hid the host picker and flipped every remote row's offline
// badge to ONLINE for the length of the refresh (round nine).
const displaySourcesOf = (manifest: ResourceState<NavigationManifest> | null) =>
  selectDisplaySources({ manifest } as unknown as Parameters<typeof selectDisplaySources>[0]);

test("selectDisplaySources keeps the last-known sources while the manifest is unsettled", () => {
  for (const overrides of [{ loading: true }, { stale: true }, { error: new Error("read failed") }]) {
    expect(displaySourcesOf(manifestResource(overrides)).map((source) => source.id)).toEqual(["local", "buildbox"]);
  }
  // Nothing has ever been read: there is no last-known reading to display.
  expect(displaySourcesOf(manifestResource({ data: null }))).toEqual([]);
  expect(displaySourcesOf(null)).toEqual([]);
});

test("selectDisplaySources hands back the store's own array, so snapshots stay identical", () => {
  // Same useSyncExternalStore contract as selectSources: a fresh array (or a
  // fresh copy) on every read would re-render the store's subscribers forever.
  expect(displaySourcesOf(manifestResource())).toBe(manifestData.sources);
  const retained = displaySourcesOf(manifestResource({ stale: true }));
  expect(displaySourcesOf(manifestResource({ loading: true }))).toBe(retained);
});

test("the two views differ exactly where settledness does", () => {
  // A settled manifest: launch decisions and display agree.
  const settled = manifestResource();
  expect(displaySourcesOf(settled)).toBe(sourcesOf(settled));
  // Unsettled: only the display read keeps describing the hosts.
  const stale = manifestResource({ stale: true });
  expect(sourcesOf(stale)).toEqual([]);
  expect(displaySourcesOf(stale).map((source) => source.id)).toEqual(["local", "buildbox"]);
});
