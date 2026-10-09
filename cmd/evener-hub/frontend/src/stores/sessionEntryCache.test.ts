import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, expect, it } from "vitest";
import {
  CURRENT_META_STORE,
  CURRENT_STORE,
  ENTRY_COVERAGE_STORE,
  ENTRY_ITEM_STORE,
  ENTRY_META_STORE,
  ENTRY_TURN_STORE,
  LEGACY_META_STORE,
  LEGACY_RECORDS_STORE,
} from "./sessionCacheSchema";
import { SessionEntryCache } from "./sessionEntryCache";

const caches: SessionEntryCache[] = [];
const databases: Array<{ factory: IDBFactory; name: string }> = [];
afterEach(() => {
  for (const cache of caches.splice(0)) cache.close();
  for (const { factory, name } of databases.splice(0)) factory.deleteDatabase(name);
});

it("writes and reads one entry by history identity and transcript key", async () => {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  caches.push(cache);
  const row = {
    historyKey: "host/session/incarnation",
    transcriptKey: "entry-1/item-0",
    position: { entry: 1, item: 0 },
    version: 1,
    item: { id: "item-0", turnId: "turn-1", type: "message", text: "hello" },
  };

  await cache.putItems([row]);
  await expect(cache.getLatest(row.historyKey, 40)).resolves.toEqual([row]);
  const request = factory.open(name);
  const database = await new Promise<IDBDatabase>((resolve, reject) => {
    request.addEventListener("success", () => resolve(request.result), { once: true });
    request.addEventListener("error", () => reject(request.error), { once: true });
  });
  expect(Array.from(database.objectStoreNames)).toEqual(
    [
      CURRENT_META_STORE,
      CURRENT_STORE,
      ENTRY_COVERAGE_STORE,
      ENTRY_ITEM_STORE,
      ENTRY_META_STORE,
      ENTRY_TURN_STORE,
      LEGACY_META_STORE,
      LEGACY_RECORDS_STORE,
    ].sort(),
  );
  database.close();
});

it("opens only the latest bounded rows for the requested history identity", async () => {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  caches.push(cache);
  const rows = Array.from({ length: 100 }, (_, entry) => ({
    historyKey: "host/session/incarnation",
    transcriptKey: `entry-${entry}`,
    position: { entry, item: 0 },
    version: 1,
    item: { id: `item-${entry}`, turnId: "turn-1", type: "assistant", text: `item-${entry}` },
  }));
  await cache.putItems(rows);
  await cache.putItems([{ ...rows[99]!, historyKey: "other-host/session/incarnation" }]);

  await expect(cache.getLatest("host/session/incarnation", 3)).resolves.toEqual(rows.slice(97));
});

it("does not let an older item version replace a newer committed entry", async () => {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  caches.push(cache);
  const historyKey = "host/session/incarnation";
  const row = {
    historyKey,
    transcriptKey: "entry-1/item-0",
    position: { entry: 1, item: 0 },
    version: 2,
    item: { id: "item-0", turnId: "turn-1", type: "message", text: "newer" },
  };
  await cache.putItems([row]);
  await cache.putItems([{ ...row, version: 1, item: { ...row.item, text: "older" } }]);
  await expect(cache.getLatest(historyKey, 40)).resolves.toEqual([row]);
});
