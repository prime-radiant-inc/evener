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

describeMutationOutboxStorage({
  name: "IndexedDB",
  createStorage(options = {}) {
    // The web reads its identity from a module singleton rather than a
    // constructor option; reset it to the requested id (or to undefined) so
    // each conformance test starts from the same place.
    setMutationClientIdentityForTests(options.getOwnClientId?.());
    return new MutationOutboxIndexedDB({
      indexedDB: new IDBFactory(),
      databaseName: `mutation-outbox-conformance-${crypto.randomUUID()}`,
      createMutationId: options.createMutationId,
      now: options.now,
    });
  },
});
