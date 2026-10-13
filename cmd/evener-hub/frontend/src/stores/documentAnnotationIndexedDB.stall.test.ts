// @vitest-environment node

import type { DocumentKey } from "@evener/appwire-client";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, expect, test, vi } from "vitest";
import { MemoryStorage } from "../storageTestUtils";
import { browserDraftKey, createBrowserDraftRepository } from "./browserDraftRepository";
import { createDocumentAnnotationRepository } from "./documentAnnotationIndexedDB";
import { ANNOTATION_HEADS_STORE, ANNOTATION_NAMESPACES_STORE } from "./localCompositionSchema";
import { holdNextWriteTransaction } from "./testing/stalledIndexedDB";

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

// Annotation transactions gained the same watchdog the drafts repository and
// the outbox already have: a stalled request or completion event stops
// waiting and REJECTS, because the open deadline alone never covered the
// transaction behind it - without one, beginRead would hang a reader forever
// and a save/retry pass would never settle. Annotation writes reconcile on
// retry through their operation rows (the duplicate-operation identity
// check) and reads re-read durable state, so the unobserved outcome is
// honestly reported as a fault for the caller's own retry.
test("a stalled annotation transaction stops waiting and the same adapter can reopen and read", async () => {
  const factory = new IDBFactory();
  const drafts = createBrowserDraftRepository({
    indexedDB: factory,
    databaseName: "annotation-stall",
    storage: new MemoryStorage(),
  });
  const repository = createDocumentAnnotationRepository({
    indexedDB: factory,
    databaseName: "annotation-stall",
    drafts,
    targetFor: () => null,
  });
  const key = browserDraftKey("root", "https://stall.example");
  const document: DocumentKey = { ...key, path: "NOTES.md" };
  // Prime the namespace and head rows, then hold the next readwrite
  // transaction over the read stores: fake-indexeddb commits it, but its
  // "complete" event - and beginRead waiting on that event - stays held.
  await repository.beginRead(document);
  const hold = holdNextWriteTransaction([ANNOTATION_NAMESPACES_STORE, ANNOTATION_HEADS_STORE]);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  let failure: unknown;
  repository.beginRead(document).catch((error: unknown) => {
    failure = error;
  });
  await hold.reached;
  await vi.advanceTimersByTimeAsync(10_001);
  expect(failure).toMatchObject({ message: "Document annotation repository transaction timed out" });
  hold.release();
  // The watchdog retired the stalled connection; a fresh read still works.
  expect((await repository.beginRead(document)).key).toEqual(document);
  repository.close();
  drafts.close();
});
