import { expect, test } from "vitest";
import type { NavigationManifest, NavigationSessionSummary } from "../../types.gen";
import { normalizedGraphFromSnapshot } from "./codec";
import { selectDisplaySources, selectSources, subagentTallyToShow } from "./selectors";
import { isSettledGone, navigationViewScope, type ResourceKey, type ResourceState } from "./types";

test.each([
  [
    { kind: "project_page", projectKey: "项目/a|b", tier: "recent", offset: 2, limit: 7 },
    "nav3/project_page///6aG555uuL2F8Yg/cmVjZW50/2/7",
  ],
  [{ kind: "location", ref: "源/α:β|?" }, "nav3/location/5rqQL86xOs6yfD8////0/0"],
  [
    { kind: "pin_section", sectionId: "pins/研发|?", offset: 3, limit: 11 },
    "nav3/pin_section//cGlucy_noJTlj5F8Pw///3/11",
  ],
  [{ kind: "section", section: "live", offset: 4, limit: 0 }, "nav3/live/////4/50"],
  [
    { kind: "project_page", projectKey: "project", tier: "current", offset: 5, limit: 51 },
    "nav3/project_page///cHJvamVjdA/Y3VycmVudA/5/50",
  ],
  [{ kind: "pin_catalog", offset: 6, limit: 0 }, "nav3/pin_catalog/////6/100"],
  [{ kind: "catalog", catalog: "projects", offset: 7, limit: 101 }, "nav3/projects/////7/100"],
  [
    { kind: "project", projectKey: "no-project", catalog: "test_runs" },
    "nav3/project///bm8tcHJvamVjdA//0/0/catalog/test_runs",
  ],
  [
    {
      kind: "project_page",
      projectKey: "no-project",
      catalog: "archived_projects",
      tier: "current",
      offset: 1,
      limit: 2,
    },
    "nav3/project_page///bm8tcHJvamVjdA/Y3VycmVudA/1/2/catalog/archived_projects",
  ],
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

// D6: only a live root shows its subagent tally, and only when something in it
// is worth a glance. Done subagents are history, so a tally of all done shows
// nothing.
test("subagentTallyToShow shows a live root's running and failed counts and hides the rest", () => {
  const row = (overrides: Partial<NavigationSessionSummary>): NavigationSessionSummary => ({
    ref: "local:s",
    host_id: "local",
    session_id: "s",
    title: "t",
    project: "p",
    state: "active",
    kind: "session",
    live: true,
    children: [],
    ...overrides,
  });
  expect(subagentTallyToShow(row({ subagents: { running: 3, failed: 1, done: 5 } }))).toEqual({
    running: 3,
    failed: 1,
  });
  expect(subagentTallyToShow(row({ subagents: { running: 0, failed: 2, done: 0 } }))).toEqual({
    running: 0,
    failed: 2,
  });
  expect(subagentTallyToShow(row({ subagents: { running: 0, failed: 0, done: 4 } }))).toBeNull();
  expect(subagentTallyToShow(row({}))).toBeNull();
  expect(subagentTallyToShow(row({ live: false, subagents: { running: 3, failed: 0, done: 0 } }))).toBeNull();
});
