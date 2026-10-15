// @vitest-environment node

// The IndexedDB adapter's half of the package's MutationOutboxStorage
// port-conformance suite: the web factory hosts the shared contracts over
// fake-indexeddb, the same contracts the native expo-sqlite adapter runs
// (mobile-native/src/mutationOutboxStorage.conformance.test.ts). One set of
// contracts, two hosts.
import { describeMutationOutboxStorage } from "@evener/appwire-client/testing/mutationOutboxStorageConformance";
import { IDBFactory } from "fake-indexeddb";
import { setMutationClientIdentityForTests } from "./mutationClientIdentity";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";

// Each conformance instance's raw handles, so the legacy contracts can
// rewrite a record exactly as the pre-unresolvedAttempts adapter wrote it.
const legacyHandles = new WeakMap<MutationOutboxIndexedDB, { indexedDB: IDBFactory; databaseName: string }>();

describeMutationOutboxStorage({
  name: "IndexedDB",
  createStorage(options = {}) {
    // The web reads its identity from a module singleton rather than a
    // constructor option; reset it to the requested id (or to undefined) so
    // each conformance test starts from the same place.
    setMutationClientIdentityForTests(options.getOwnClientId?.());
    const indexedDB = new IDBFactory();
    const databaseName = `mutation-outbox-conformance-${crypto.randomUUID()}`;
    const storage = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: options.createMutationId,
      now: options.now,
    });
    legacyHandles.set(storage, { indexedDB, databaseName });
    return storage;
  },
  async stripAttemptCount(storage, clientMutationId) {
    const handle = legacyHandles.get(storage as MutationOutboxIndexedDB);
    if (!handle) throw new Error("conformance storage missing its raw handle");
    await new Promise<void>((resolve, reject) => {
      const open = handle.indexedDB.open(handle.databaseName);
      open.onsuccess = () => {
        const db = open.result;
        const transaction = db.transaction("outbox", "readwrite");
        const store = transaction.objectStore("outbox");
        const get = store.get(clientMutationId);
        get.onsuccess = () => {
          const next = { ...(get.result as Record<string, unknown>) };
          delete next.unresolvedAttempts;
          store.put(next);
        };
        transaction.oncomplete = () => {
          db.close();
          resolve();
        };
        transaction.onerror = () => reject(transaction.error);
      };
      open.onerror = () => reject(open.error);
    });
  },
  async stripCaptureAtEnqueue(storage, clientMutationId) {
    const handle = legacyHandles.get(storage as MutationOutboxIndexedDB);
    if (!handle) throw new Error("conformance storage missing its raw handle");
    await new Promise<void>((resolve, reject) => {
      const open = handle.indexedDB.open(handle.databaseName);
      open.onsuccess = () => {
        const db = open.result;
        const transaction = db.transaction("outbox", "readwrite");
        const store = transaction.objectStore("outbox");
        const get = store.get(clientMutationId);
        get.onsuccess = () => {
          const next = { ...(get.result as Record<string, unknown>) };
          delete next.stopEpochAtEnqueue;
          store.put(next);
        };
        transaction.oncomplete = () => {
          db.close();
          resolve();
        };
        transaction.onerror = () => reject(transaction.error);
      };
      open.onerror = () => reject(open.error);
    });
  },
});
