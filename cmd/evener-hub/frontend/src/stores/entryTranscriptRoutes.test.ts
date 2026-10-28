import "fake-indexeddb/auto";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, describe, expect, it } from "vitest";
import {
  advanceOlderPageGeneration,
  entryRouteRetirementCount,
  entryTranscriptRoute,
  existingEntryRoute,
  forgetEntryRoute,
  noteEntryRouteRetirement,
  olderPageGeneration,
  resetEntryTranscriptRoutesForTests,
  setProductionEntryCacheForTests,
} from "./entryTranscriptRoutes";
import { SessionEntryCache } from "./sessionEntryCache";

afterEach(() => resetEntryTranscriptRoutesForTests());

function useFreshCache(): SessionEntryCache {
  const cache = new SessionEntryCache({
    indexedDB: new IDBFactory(),
    keyRange: IDBKeyRange,
    databaseName: `routes-${crypto.randomUUID()}`,
  });
  setProductionEntryCacheForTests(cache);
  return cache;
}

describe("entry transcript routes", () => {
  it("creates a route on first lookup and finds it afterwards without creating another", () => {
    useFreshCache();
    expect(existingEntryRoute("r")).toBeUndefined();
    const route = entryTranscriptRoute("r");
    expect(existingEntryRoute("r")).toBe(route);
    expect(entryTranscriptRoute("r")).toBe(route);
  });

  it("forgets a route only while it is still the registered one", () => {
    useFreshCache();
    const route = entryTranscriptRoute("r");
    forgetEntryRoute({ ...route!, lifetime: route!.lifetime });
    expect(existingEntryRoute("r")).toBe(route);
    forgetEntryRoute(route!);
    expect(existingEntryRoute("r")).toBeUndefined();
  });

  it("forgets every route, retirement count and older-page generation on reset", () => {
    useFreshCache();
    entryTranscriptRoute("r");
    noteEntryRouteRetirement("r");
    advanceOlderPageGeneration("r", 0);
    expect(entryRouteRetirementCount("r")).toBe(1);
    expect(olderPageGeneration("r")).toBe(1);

    resetEntryTranscriptRoutesForTests();

    expect(existingEntryRoute("r")).toBeUndefined();
    expect(entryRouteRetirementCount("r")).toBe(0);
    expect(olderPageGeneration("r")).toBe(0);
  });
});
