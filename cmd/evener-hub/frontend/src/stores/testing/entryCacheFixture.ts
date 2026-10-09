import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { SessionEntryCache } from "../sessionEntryCache";

export function createEntryCacheFixture(name = `entry-cache-${crypto.randomUUID()}`) {
  const indexedDB = new IDBFactory();
  const cache = new SessionEntryCache({ indexedDB, keyRange: IDBKeyRange, databaseName: name });
  return {
    cache,
    indexedDB,
    name,
    async dispose() {
      cache.close();
      await new Promise<void>((resolve, reject) => {
        const request = indexedDB.deleteDatabase(name);
        request.addEventListener("success", () => resolve(), { once: true });
        request.addEventListener("error", () => reject(request.error), { once: true });
        request.addEventListener("blocked", () => reject(new Error(`${name} database deletion blocked`)), {
          once: true,
        });
      });
    },
  };
}
