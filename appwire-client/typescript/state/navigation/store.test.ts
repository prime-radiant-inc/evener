import { expect, test } from "vitest";
import { FakeClient } from "../../testing/fakeClient";
import { capability, manifest, wireV2 } from "../../testing/navigation";
import type { NavigationReadParams, NavigationReadResponse } from "../../types.gen";
import {
  createNavigationStore,
  type NavigationPersistence,
  type NavigationStore,
  projectNodeExpansionKey,
} from "./store";

const flush = async () => {
  for (let i = 0; i < 64; i++) await Promise.resolve();
};

/** A host port that records what the store asks of it. `onWrite` runs inside
 * writeExpansion, so a test can see what the store had already published by
 * the time it handed the map over. */
function recordingPersistence(
  seed: Iterable<readonly [string, boolean]> = [],
  onWrite: (expansion: ReadonlyMap<string, boolean>) => void = () => undefined,
): NavigationPersistence & { reads: number; writes: Array<Record<string, boolean>> } {
  const port = {
    reads: 0,
    writes: [] as Array<Record<string, boolean>>,
    readExpansion(): Map<string, boolean> {
      port.reads++;
      return new Map(seed);
    },
    writeExpansion(expansion: ReadonlyMap<string, boolean>): void {
      port.writes.push(Object.fromEntries(expansion));
      onWrite(expansion);
    },
  };
  return port;
}

const catalogOfOne = (projectKey: string, defaultExpanded: boolean) => (params: NavigationReadParams) => {
  if (params.resource === "manifest")
    return wireV2(
      params,
      manifest({ catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } } }),
    );
  if (params.resource === "catalog" && params.catalog === "projects")
    return wireV2(params, { projects: [{ key: projectKey, default_expanded: defaultExpanded }], remaining: 0 });
  if (params.resource === "project")
    return wireV2(params, {
      key: projectKey,
      current: { sessions: [], remaining: 0 },
      recent: { sessions: [], remaining: 0 },
      archived: { sessions: [], remaining: 0 },
    });
  return wireV2(params, { sessions: [], remaining: 0 });
};

const booted = async (store: NavigationStore, script: (p: NavigationReadParams) => NavigationReadResponse) => {
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", script);
  store.init(client, capability());
  await flush();
  return client;
};

test("two stores keep their own expansion, their own port and their own connection", async () => {
  const firstPort = recordingPersistence([["a", true]]);
  const secondPort = recordingPersistence();
  const first = createNavigationStore({ persistence: firstPort });
  const second = createNavigationStore({ persistence: secondPort });

  expect([...first.getState().expanded]).toEqual([["a", true]]);
  expect(second.getState().expanded.size).toBe(0);

  first.getState().setExpanded("b", true);
  expect(second.getState().expanded.size).toBe(0);
  expect(secondPort.writes).toEqual([]);
  expect(firstPort.writes).toEqual([{ a: true, b: true }]);

  await booted(first, catalogOfOne("p", false));
  expect(first.getState().mode).toBe("v2");
  expect(second.getState().mode).toBe("unknown");
  expect(second.getState().manifest).toBeNull();
});

test("expansion is read from the port when the store is built and again on reset", () => {
  const port = recordingPersistence([["a", true]]);
  const store = createNavigationStore({ persistence: port });
  expect(port.reads).toBe(1);
  store.getState().setExpanded("b", true);
  store.reset();
  expect(port.reads).toBe(2);
  // The port, not the store's own last value, is what a rebuilt store holds.
  expect([...store.getState().expanded]).toEqual([["a", true]]);
});

test("both expansion actions publish before they hand the map to the port", () => {
  const published: Array<Array<[string, boolean]>> = [];
  let store: NavigationStore | undefined;
  const port = recordingPersistence([["a", true]], () => {
    published.push([...(store?.getState().expanded ?? [])]);
  });
  store = createNavigationStore({ persistence: port });

  store.getState().setExpanded("b", false);
  store.getState().toggleExpanded("a");

  expect(port.writes).toEqual([
    { a: true, b: false },
    { a: false, b: false },
  ]);
  // Persistence is best-effort: the store's own state is already the new one
  // by the time the host is asked to keep it.
  expect(published).toEqual([
    [
      ["a", true],
      ["b", false],
    ],
    [
      ["a", false],
      ["b", false],
    ],
  ]);
});

test("a persisted project-node key overrides the catalog's default_expanded at boot", async () => {
  const projectKey = "persisted/project";
  const store = createNavigationStore({
    persistence: recordingPersistence([[projectNodeExpansionKey(projectKey), true]]),
  });
  const reads: NavigationReadParams[] = [];
  await booted(store, (params) => {
    reads.push(params);
    return catalogOfOne(projectKey, false)(params);
  });
  await flush();
  expect(reads.filter((p) => p.resource === "project" && p.projectKey === projectKey)).toHaveLength(1);
});

test("a store reset mid-boot fences the fan-out its manifest would have started", async () => {
  const store = createNavigationStore({ persistence: recordingPersistence() });
  const client = new FakeClient("ready");
  let releaseManifest: (() => void) | undefined;
  client.on("evener/navigation/read", async (params: NavigationReadParams) => {
    if (params.resource === "manifest") {
      await new Promise<void>((resolve) => {
        releaseManifest = resolve;
      });
    }
    return catalogOfOne("p", true)(params);
  });
  store.init(client, capability());
  await flush();

  store.reset();
  releaseManifest?.();
  await flush();

  expect(store.getState().mode).toBe("unknown");
  expect(store.getState().manifest).toBeNull();
  expect(store.getState().resources.size).toBe(0);
});
