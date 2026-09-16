import { afterEach, expect, test } from "vitest";
import { EXPANSION_STORAGE_KEY } from "../../shell/rail/railExpansion";
import { type NavigationPersistence, railExpansionPersistence } from "./persistence";
import { navigationStore, resetNavigationStoreForTests } from "./store";

/** A host port that records what the store asks of it, so a test can see
 * which side of the seam each read and write lands on. */
function recordingPersistence(seed: Iterable<readonly [string, boolean]> = []): NavigationPersistence & {
  reads: number;
  writes: Array<Record<string, boolean>>;
} {
  const port = {
    reads: 0,
    writes: [] as Array<Record<string, boolean>>,
    readExpansion(): Map<string, boolean> {
      port.reads++;
      return new Map(seed);
    },
    writeExpansion(expansion: ReadonlyMap<string, boolean>): void {
      port.writes.push(Object.fromEntries(expansion));
    },
  };
  return port;
}

afterEach(() => {
  localStorage.removeItem(EXPANSION_STORAGE_KEY);
  resetNavigationStoreForTests();
});

test("the store loads expansion from the host port when it is built", () => {
  const port = recordingPersistence([["projectnode:p", true]]);
  resetNavigationStoreForTests(port);
  expect(port.reads).toBe(1);
  expect([...navigationStore.getState().expanded]).toEqual([["projectnode:p", true]]);
});

test("a host port holding nothing leaves the store with no expansion, whatever the browser stored", () => {
  localStorage.setItem(EXPANSION_STORAGE_KEY, JSON.stringify({ "projectnode:p": true }));
  resetNavigationStoreForTests(recordingPersistence());
  expect(navigationStore.getState().expanded.size).toBe(0);
});

test("setExpanded writes the whole expansion map through the host port", () => {
  const port = recordingPersistence([["a", true]]);
  resetNavigationStoreForTests(port);
  navigationStore.getState().setExpanded("b", false);
  expect(port.writes).toEqual([{ a: true, b: false }]);
  expect(localStorage.getItem(EXPANSION_STORAGE_KEY)).toBeNull();
});

test("toggleExpanded writes the flipped map through the host port", () => {
  const port = recordingPersistence([["a", true]]);
  resetNavigationStoreForTests(port);
  navigationStore.getState().toggleExpanded("a");
  expect(navigationStore.getState().expanded.get("a")).toBe(false);
  expect(port.writes).toEqual([{ a: false }]);
  expect(localStorage.getItem(EXPANSION_STORAGE_KEY)).toBeNull();
});

test("the web port is the rail's one localStorage blob", () => {
  railExpansionPersistence.writeExpansion(new Map([["projectnode:p", true]]));
  expect(JSON.parse(localStorage.getItem(EXPANSION_STORAGE_KEY) ?? "null")).toEqual({ "projectnode:p": true });
  expect([...railExpansionPersistence.readExpansion()]).toEqual([["projectnode:p", true]]);
});
