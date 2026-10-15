import type { IDBFactory } from "fake-indexeddb";
import type { MutationOutboxRecord } from "../mutationOutbox";

// The outbox fields a build can predate: `attempted` has been written on
// every row since #936, `unresolvedAttempts` since #4342 round 1, and
// `stopEpochAtEnqueue` since round 5. A row written by a build from before
// any of those landed persists untouched through every later schema version,
// because the IndexedDB upgrade handler creates missing stores but never
// rewrites records - so the only way a test can hold one is a raw
// IndexedDB write against the same database, at the real boundary, the way
// those rows actually persist. Production code never strips fields; this is
// the test twin of that persistence.
export async function rewriteOutboxRowWithoutFields(
  indexedDB: IDBFactory,
  databaseName: string,
  clientMutationId: string,
  fields: Array<"attempted" | "unresolvedAttempts" | "stopEpochAtEnqueue">,
): Promise<void> {
  const openRequest = indexedDB.open(databaseName);
  const database = await new Promise<IDBDatabase>((resolve, reject) => {
    openRequest.addEventListener("success", () => resolve(openRequest.result), { once: true });
    openRequest.addEventListener("error", () => reject(openRequest.error), { once: true });
  });
  try {
    const read = database.transaction("outbox", "readonly").objectStore("outbox").get(clientMutationId);
    const row = await new Promise<MutationOutboxRecord>((resolve, reject) => {
      read.addEventListener("success", () => resolve(read.result), { once: true });
      read.addEventListener("error", () => reject(read.error), { once: true });
    });
    const legacyRow: MutationOutboxRecord = { ...row };
    for (const field of fields) {
      delete legacyRow[field];
    }
    const write = database.transaction("outbox", "readwrite").objectStore("outbox").put(legacyRow);
    await new Promise<void>((resolve, reject) => {
      write.addEventListener("success", () => resolve(), { once: true });
      write.addEventListener("error", () => reject(write.error), { once: true });
    });
  } finally {
    database.close();
  }
}
