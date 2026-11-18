import "fake-indexeddb/auto";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, describe, expect, it } from "vitest";
import {
  advanceOlderPageGeneration,
  entryRouteRetirementCount,
  entryTranscriptRoute,
  existingEntryRoute,
  forgetEntryRoute,
  isEntryTranscriptBackgroundOnly,
  noteEntryRouteRetirement,
  olderPageGeneration,
  resetEntryTranscriptRoutesForTests,
  setProductionEntryCacheForTests,
} from "./entryTranscriptRoutes";
import { SessionEntryCache } from "./sessionEntryCache";
import {
  holdBackground,
  holdBodyOwnership,
  holdForeground,
  releaseBodyOwnership,
  releaseForegroundHold,
  resetThreadOwnersForTests,
} from "./threadOwners";

afterEach(() => {
  resetEntryTranscriptRoutesForTests();
  resetThreadOwnersForTests();
});

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

  // The background-only answer is about who DISPLAYS the bodies, and a routing
  // lease displays nothing. Counting it as a body owner meant a resync during a
  // resend could put a finished transcript back in the store after the last
  // pane had released its reader - bodies retained for nobody.
  it("treats a routing lease as no body owner, so completed bodies are not retained for it", () => {
    holdBackground("routing");
    expect(isEntryTranscriptBackgroundOnly("routing")).toBe(true);

    // A routing lease keeps the model and the subscription but displays
    // nothing, so it is still no body owner: the answer stays "nobody is
    // showing these bodies". Counting it here was the defect - a resync during
    // a resend could then put a finished transcript back in the store after the
    // last pane had released its reader.
    const lease = holdForeground("routing", true);
    expect(isEntryTranscriptBackgroundOnly("routing")).toBe(true);

    // A pane's body ownership is the answer that retains: the bodies it shows
    // must stay.
    holdBodyOwnership("routing");
    expect(isEntryTranscriptBackgroundOnly("routing")).toBe(false);

    // ...and once that body owner releases, the routing lease is the only
    // holder left, so the answer returns to "nobody displays these".
    releaseBodyOwnership("routing");
    releaseForegroundHold(lease);
    expect(isEntryTranscriptBackgroundOnly("routing")).toBe(true);
  });
});
