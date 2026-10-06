import { IDBFactory } from "fake-indexeddb";
import { expect, test } from "vitest";
import {
  ANNOTATION_EVIDENCE_STORE,
  ANNOTATION_EXPIRY_STORE,
  ANNOTATION_HEADS_STORE,
  ANNOTATION_NAMESPACES_STORE,
  ANNOTATION_OPERATIONS_STORE,
  ANNOTATION_PAYLOADS_STORE,
  ANNOTATION_SEQUENCES_STORE,
  COMPOSER_DRAFTS_STORE,
  DRAFT_CONFLICTS_STORE,
  DRAFT_TRANSFERS_STORE,
  LOCAL_COMPOSITION_DATABASE_VERSION,
} from "./localCompositionSchema";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";

function openVersion3(indexedDB: IDBFactory, databaseName: string): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(databaseName, 3);
    request.addEventListener("upgradeneeded", () => {
      const database = request.result;
      const outbox = database.createObjectStore("outbox", { keyPath: "clientMutationId" });
      outbox.createIndex("byTargetSequence", ["targetRef", "intentSequence"], { unique: true });
      const optimistic = database.createObjectStore("optimistic", { keyPath: "clientMutationId" });
      optimistic.createIndex("byTargetSequence", ["targetRef", "intentSequence"], { unique: true });
      const recovery = database.createObjectStore("recovery", { keyPath: "clientMutationId" });
      recovery.createIndex("byTargetSequence", ["targetRef", "intentSequence"]);
      database.createObjectStore("sequences", { keyPath: "targetRef" });
    });
    request.addEventListener("success", () => resolve(request.result), { once: true });
    request.addEventListener("error", () => reject(request.error ?? new Error("version-3 open failed")), {
      once: true,
    });
  });
}

function transactionCompletion(transaction: IDBTransaction): Promise<void> {
  return new Promise((resolve, reject) => {
    transaction.addEventListener("complete", () => resolve(), { once: true });
    transaction.addEventListener("abort", () => reject(transaction.error ?? new Error("transaction aborted")), {
      once: true,
    });
  });
}

function openCurrent(indexedDB: IDBFactory, databaseName: string): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(databaseName, LOCAL_COMPOSITION_DATABASE_VERSION);
    request.addEventListener("success", () => resolve(request.result), { once: true });
    request.addEventListener("error", () => reject(request.error ?? new Error("current open failed")), { once: true });
  });
}

test("opening the shared database upgrades populated version-3 mutation stores without deleting or duplicating rows", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "local-composition-upgrade";
  const old = await openVersion3(indexedDB, databaseName);
  const seed = old.transaction(["outbox", "optimistic", "recovery", "sequences"], "readwrite");
  seed
    .objectStore("outbox")
    .put({ clientMutationId: "out-1", targetRef: "ref-a", intentSequence: 1, marker: "outbox" });
  seed
    .objectStore("optimistic")
    .put({ clientMutationId: "opt-1", targetRef: "ref-a", intentSequence: 2, marker: "optimistic" });
  seed
    .objectStore("recovery")
    .put({ clientMutationId: "rec-1", targetRef: "ref-a", intentSequence: 3, marker: "recovery" });
  seed.objectStore("sequences").put({ targetRef: "ref-a", lastSequence: 3, stopEpoch: 2 });
  await transactionCompletion(seed);
  old.close();

  const adapter = new MutationOutboxIndexedDB({ indexedDB, databaseName });
  await adapter.listOutbox();
  const upgraded = await openCurrent(indexedDB, databaseName);

  expect(upgraded.version).toBe(4);
  expect([...upgraded.objectStoreNames]).toEqual([
    "annotationEvidence",
    "annotationExpiry",
    "annotationHeads",
    "annotationNamespaces",
    "annotationOperations",
    "annotationPayloads",
    "annotationSequences",
    "composerDrafts",
    "draftConflicts",
    "draftTransfers",
    "optimistic",
    "outbox",
    "recovery",
    "sequences",
  ]);
  const read = upgraded.transaction(["outbox", "optimistic", "recovery", "sequences"], "readonly");
  const values = await Promise.all(
    ["outbox", "optimistic", "recovery", "sequences"].map(
      (store) =>
        new Promise<unknown[]>((resolve, reject) => {
          const request = read.objectStore(store).getAll();
          request.addEventListener("success", () => resolve(request.result), { once: true });
          request.addEventListener("error", () => reject(request.error ?? new Error(`${store} read failed`)), {
            once: true,
          });
        }),
    ),
  );
  await transactionCompletion(read);
  expect(values).toEqual([
    [{ clientMutationId: "out-1", targetRef: "ref-a", intentSequence: 1, marker: "outbox" }],
    [{ clientMutationId: "opt-1", targetRef: "ref-a", intentSequence: 2, marker: "optimistic" }],
    [{ clientMutationId: "rec-1", targetRef: "ref-a", intentSequence: 3, marker: "recovery" }],
    [{ targetRef: "ref-a", lastSequence: 3, stopEpoch: 2 }],
  ]);
  upgraded.close();
  adapter.close();
});

test("the additive schema gives every authored row a serialized namespace, session, or document key", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "local-composition-keypaths";
  const adapter = new MutationOutboxIndexedDB({ indexedDB, databaseName });
  await adapter.listOutbox();
  const database = await openCurrent(indexedDB, databaseName);
  const transaction = database.transaction(
    [
      COMPOSER_DRAFTS_STORE,
      DRAFT_TRANSFERS_STORE,
      DRAFT_CONFLICTS_STORE,
      ANNOTATION_NAMESPACES_STORE,
      ANNOTATION_HEADS_STORE,
      ANNOTATION_EVIDENCE_STORE,
      ANNOTATION_PAYLOADS_STORE,
      ANNOTATION_OPERATIONS_STORE,
      ANNOTATION_SEQUENCES_STORE,
      ANNOTATION_EXPIRY_STORE,
    ],
    "readonly",
  );
  expect(
    [
      COMPOSER_DRAFTS_STORE,
      DRAFT_TRANSFERS_STORE,
      DRAFT_CONFLICTS_STORE,
      ANNOTATION_NAMESPACES_STORE,
      ANNOTATION_HEADS_STORE,
      ANNOTATION_EVIDENCE_STORE,
      ANNOTATION_PAYLOADS_STORE,
      ANNOTATION_OPERATIONS_STORE,
      ANNOTATION_SEQUENCES_STORE,
      ANNOTATION_EXPIRY_STORE,
    ].map((name) => [name, transaction.objectStore(name).keyPath]),
  ).toEqual([
    ["composerDrafts", "serializedKey"],
    ["draftTransfers", "serializedKey"],
    ["draftConflicts", "conflictId"],
    ["annotationNamespaces", "namespaceKey"],
    ["annotationHeads", "documentKey"],
    ["annotationEvidence", "evidenceKey"],
    ["annotationPayloads", "payloadKey"],
    ["annotationOperations", "operationKey"],
    ["annotationSequences", "documentKey"],
    ["annotationExpiry", "expiryKey"],
  ]);
  database.close();
  adapter.close();
});
