import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { SessionEntryCache } from "../sessionEntryCache";

export function createEntryCacheFixture(name = `entry-cache-${crypto.randomUUID()}`) {
  const indexedDB = new IDBFactory();
  const cache = new SessionEntryCache({ indexedDB, keyRange: IDBKeyRange, databaseName: name });
  return {
    cache,
    dispose() {
      cache.close();
      indexedDB.deleteDatabase(name);
    },
  };
}
