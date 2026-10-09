// Real-storage fixtures for the browser document-annotation adapter: one
// fake-indexeddb factory and database back the real BrowserDraftRepository,
// MutationOutboxIndexedDB, composer source and annotation adapter, and the
// recovery record is built through real enqueueIntent/transferToRecovery.
import {
  type AcceptedSourceLease,
  buildInput,
  type DestinationProvenance,
  type DocFileContent,
  type DocumentAnnotationRepository,
  type DocumentKey,
  displayedFingerprint,
  type PreparedAnnotation,
} from "@evener/appwire-client";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, vi } from "vitest";
import { browserDraftKey, createBrowserDraftRepository } from "../../../stores/browserDraftRepository";
import {
  annotationOperationKey,
  createDocumentAnnotationRepository,
  type DocumentAnnotationOptions,
} from "../../../stores/documentAnnotationIndexedDB";
import { requestResult, transactionCompletion } from "../../../stores/idbConnection";
import { LOCAL_COMPOSITION_DATABASE_VERSION } from "../../../stores/localCompositionSchema";
import { MutationOutboxIndexedDB } from "../../../stores/mutationOutboxIndexedDB";
import { createComposerSourceState } from "../../session/composer/sourceState";
import { FIRST, SOURCE } from "./annotationFixtures";

const fixtureDisposers: Array<() => void | Promise<void>> = [];

// The fixture recovery attachment's identifying metadata and a fresh read of a
// Blob's bytes (object equality alone does not compare Blob content). Shared by
// both annotation test files so neither duplicates them.
export const attachmentIdentity = {
  presentationId: "image-1",
  marker: 1,
  name: "one.png",
  mediaType: "image/png",
};

export async function blobBytes(blob: Blob): Promise<Uint8Array> {
  return new Uint8Array(await blob.arrayBuffer());
}

export function cleanupAfterTest(dispose: () => void | Promise<void>): void {
  fixtureDisposers.push(dispose);
}

// Run every registered disposer even after an earlier one throws.
afterEach(async () => {
  const failures: unknown[] = [];
  for (const dispose of fixtureDisposers.splice(0).reverse()) {
    try {
      await dispose();
    } catch (error) {
      failures.push(error);
    }
  }
  vi.restoreAllMocks();
  if (failures.length > 0) throw new AggregateError(failures, "annotation fixture cleanup failed");
});

export function docContent(text: string, revision?: string, truncated = false): DocFileContent {
  return {
    text,
    revision,
    truncated,
    binary: false,
    mediaType: "text/markdown",
    sizeBytes: new TextEncoder().encode(text).byteLength,
  };
}

// Read a store's rows through the public IndexedDB API, always closing the raw
// connection and observing both the request and the transaction completion.
export async function rowsForTests<T>(factory: IDBFactory, databaseName: string, store: string): Promise<T[]> {
  const database = await new Promise<IDBDatabase>((resolve, reject) => {
    const open = factory.open(databaseName, LOCAL_COMPOSITION_DATABASE_VERSION);
    open.onsuccess = () => resolve(open.result);
    open.onerror = () => reject(open.error);
  });
  try {
    const tx = database.transaction(store, "readonly");
    const completed = transactionCompletion(tx);
    const [rows] = await Promise.all([requestResult<T[]>(tx.objectStore(store).getAll()), completed]);
    return rows;
  } finally {
    database.close();
  }
}

// The one literal capture/operation builder both test helpers share: no
// production formatter or computed reference text is involved.
export function preparedAnnotationFromLease(
  lease: AcceptedSourceLease,
  operationId: string,
  note: string,
  destination: DestinationProvenance,
  revision = "rev-A",
  truncated = false,
): PreparedAnnotation {
  return {
    operationId,
    lease,
    note,
    destination,
    capture: {
      evidence: { wholeRevision: revision, fingerprint: displayedFingerprint(SOURCE, truncated) },
      sourceRange: FIRST,
      start: { line: 1, column: 19 },
      endInclusive: { line: 1, column: 22 },
      selectedRenderedText: "same",
      contextBefore: "First occurrence: ",
      contextAfter: "\n\nOther context for a second occurrence is nearby: same\n",
      mappingId: "map-A",
      displayedSnapshotId: lease.displayedSnapshotId,
    },
  };
}

// Build one genuine accepted operation from a real beginRead/acceptRead lease.
export async function acceptedOperation(
  repo: Pick<DocumentAnnotationRepository, "beginRead" | "acceptRead">,
  key: DocumentKey,
  operationId: string,
  note: string,
  destination: DestinationProvenance,
  revision = "rev-A",
): Promise<PreparedAnnotation> {
  const read = await repo.acceptRead(await repo.beginRead(key), docContent(SOURCE, revision));
  if (read.state !== "accepted") throw new Error("read not accepted");
  return preparedAnnotationFromLease(read.lease, operationId, note, destination, revision);
}

export async function fixture(
  databaseName: string,
  hooks: Partial<
    Pick<
      DocumentAnnotationOptions,
      | "beforeFinalGuard"
      | "beforeAbortableCompletion"
      | "afterCommit"
      | "beforeCommit"
      | "onAppendReadsIssued"
      | "onReadBoundary"
      | "beforeExpiryFence"
      | "onStorageSuccess"
    >
  > = {},
) {
  // Native structuredClone (fake IndexedDB) cannot preserve jsdom Blob bytes,
  // so the attachment is created and read with Node's real Blob. Restoration is
  // registered first and so runs last, after every read and after the other
  // disposers, even when setup fails partway.
  const { Blob: NodeBlob } = await vi.importActual<{ Blob: typeof Blob }>("node:buffer");
  vi.stubGlobal("Blob", NodeBlob);
  cleanupAfterTest(() => {
    vi.unstubAllGlobals();
  });
  const factory = new IDBFactory();
  const key = browserDraftKey("root", location.origin);
  const document: DocumentKey = { ...key, path: "NOTES.md" };
  const drafts = createBrowserDraftRepository({ indexedDB: factory, databaseName });
  cleanupAfterTest(() => drafts.close());
  await drafts.seedDraft(key, {
    text: "ordinary /same",
    skillNames: ["same"],
    mentions: [{ kind: "skill", name: "same", offset: 9 }],
  });
  const outbox = new MutationOutboxIndexedDB({
    indexedDB: factory,
    databaseName,
    createMutationId: () => "rec-1",
    createPresentationId: () => "image-1",
    now: () => 100,
  });
  cleanupAfterTest(() => outbox.close());
  const staged = [{ marker: 1, name: "one.png", mediaType: "image/png", data: "AQ==" }];
  const input = buildInput(
    "recovery /plan [image 1]",
    staged,
    [],
    ["plan"],
    [{ kind: "command", name: "plan", offset: 9 }],
  );
  const sent = await outbox.enqueueIntent({
    targetRef: "root",
    method: "turn/start",
    payload: { input },
    optimisticDisplay: { method: "turn/start", input },
    composerText: "recovery /plan [image 1]",
    composerMentions: [{ kind: "command", name: "plan", offset: 9 }],
    attachments: [
      {
        presentationId: "image-1",
        marker: 1,
        name: "one.png",
        mediaType: "image/png",
        blob: new Blob([new Uint8Array([1])]),
      },
    ],
  });
  await outbox.transferToRecovery(sent.clientMutationId, "rejected", "fixture rejection");
  const source = createComposerSourceState("root", { repository: drafts, key, outbox });
  // Join the source's queued work before disposing.
  cleanupAfterTest(async () => {
    await source.whenSettled();
    source.dispose();
  });
  await source.ready();
  const destination: DestinationProvenance = {
    kind: "browser-origin",
    paneId: "pane-A",
    compositionOwnerId: source.sourceId,
  };
  const repo = createDocumentAnnotationRepository({
    indexedDB: factory,
    databaseName,
    drafts,
    ...hooks,
    targetFor: (origin: DestinationProvenance, doc: DocumentKey) =>
      origin.kind === "browser-origin" &&
      origin.compositionOwnerId === source.sourceId &&
      origin.paneId === "pane-A" &&
      doc.sessionRef === key.sessionRef &&
      doc.namespace.kind === "browser" &&
      doc.namespace.origin === key.namespace.origin &&
      source.alive
        ? source
        : null,
  });
  cleanupAfterTest(() => repo.close());
  const operation = await acceptedOperation(repo, document, "op-1", "Keep this scope.", destination);
  await repo.saveAnnotation(operation);
  return {
    factory,
    databaseName,
    key,
    document,
    drafts,
    outbox,
    source,
    destination,
    repo,
    operation,
    operationKey: annotationOperationKey(document.namespace, "op-1"),
  };
}
