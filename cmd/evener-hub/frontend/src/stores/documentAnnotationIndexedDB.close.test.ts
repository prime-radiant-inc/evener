// @vitest-environment node

import { type DocFileContent, displayedFingerprint, type PreparedAnnotation } from "@evener/appwire-client";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, expect, test, vi } from "vitest";
import type { ComposerSourceState } from "../panes/session/composer/composerContract";
import { MemoryStorage } from "../storageTestUtils";
import { browserDraftKey, createBrowserDraftRepository } from "./browserDraftRepository";
import { createDocumentAnnotationRepository } from "./documentAnnotationIndexedDB";

afterEach(() => {
  vi.restoreAllMocks();
});

// The adapter arms one recovery-persisted subscription per composer source
// when a handoff resolves that source. close() must release it: a surviving
// composer source otherwise keeps the closed repository alive and still
// invokes its storage-success callback after closure.
test("close releases the handoff's recovery-persisted subscription", async () => {
  const factory = new IDBFactory();
  const drafts = createBrowserDraftRepository({
    indexedDB: factory,
    databaseName: "annotation-close",
    storage: new MemoryStorage(),
  });
  const listeners = new Set<() => void>();
  const source = {
    ref: "root",
    sourceId: "source-1",
    alive: true,
    subscribeRecoveryPersisted(listener: () => void) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    prepareAnnotation: async () => null,
  } as unknown as ComposerSourceState;
  const storageSuccess = vi.fn();
  const repository = createDocumentAnnotationRepository({
    indexedDB: factory,
    databaseName: "annotation-close",
    drafts,
    targetFor: () => source,
    onStorageSuccess: storageSuccess,
  });
  const key = browserDraftKey("root", "https://close.example");
  const document = { ...key, path: "NOTES.md" };
  const content: DocFileContent = {
    text: "A note with an [image 1] marker\n",
    binary: false,
    mediaType: "text/markdown",
    truncated: false,
    sizeBytes: 29,
  };
  const ticket = await repository.beginRead(document);
  const accepted = await repository.acceptRead(ticket, content);
  if (accepted.state !== "accepted") throw new Error(`read was not accepted: ${accepted.state}`);
  const operation: PreparedAnnotation = {
    operationId: "operation-1",
    lease: accepted.lease,
    capture: {
      evidence: {
        wholeRevision: content.revision,
        fingerprint: displayedFingerprint(content.text, content.truncated),
      },
      sourceRange: { start: 0, end: 1 },
      start: { line: 1, column: 1 },
      endInclusive: { line: 1, column: 1 },
      selectedRenderedText: "A",
      contextBefore: "",
      contextAfter: "",
      mappingId: "mapping-1",
      displayedSnapshotId: accepted.lease.displayedSnapshotId,
    },
    note: "note",
    destination: { kind: "browser-origin", paneId: "pane-1", compositionOwnerId: "source-1" },
  };
  await repository.saveAnnotation(operation);

  // The handoff resolves the source and arms its recovery notification before
  // preparing the append; a null preparation ends the handoff without
  // touching the composer.
  expect(await repository.handoff(document, operation.operationId)).toEqual({
    state: "pending",
    reason: "owner-unavailable",
  });
  expect(listeners.size).toBe(1);

  storageSuccess.mockClear();
  for (const listener of [...listeners]) listener();
  expect(storageSuccess).toHaveBeenCalledTimes(1);

  repository.close();
  // A late notification from the surviving source cannot reach the closed
  // repository's storage-success callback.
  for (const listener of [...listeners]) listener();
  expect(storageSuccess).toHaveBeenCalledTimes(1);
  expect(listeners.size).toBe(0);
  drafts.close();
});
