// @vitest-environment node

import {
  type DocFileContent,
  type DocumentKey,
  displayedFingerprint,
  type PreparedAnnotation,
} from "@evener/appwire-client";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, expect, test, vi } from "vitest";
import type { ComposerPreparation, ComposerSourceState } from "../panes/session/composer/composerContract";
import { MemoryStorage } from "../storageTestUtils";
import {
  type BrowserDraftKey,
  type BrowserDraftRepository,
  browserDraftKey,
  createBrowserDraftRepository,
} from "./browserDraftRepository";
import {
  type BrowserDocumentAnnotationRepository,
  createDocumentAnnotationRepository,
} from "./documentAnnotationIndexedDB";
import { ANNOTATION_OPERATIONS_STORE, ANNOTATION_PAYLOADS_STORE } from "./localCompositionSchema";
import { holdNextTransactionEvent } from "./testing/stalledIndexedDB";

afterEach(() => {
  vi.restoreAllMocks();
});

interface FixtureOptions {
  prepareAnnotation?: () => Promise<ComposerPreparation | null>;
  // Runs after a changed read validates and before its expiry fence opens.
  beforeExpiryFence?: () => Promise<void>;
  // Runs when a handoff resolves its destination, immediately before the arm.
  onTarget?: () => void;
  // Runs once the append has issued all of its reads, before it awaits them.
  onAppendReadsIssued?: () => void;
  // Seeds the ordinary draft row a real append needs to validate its token.
  seedDraft?: boolean;
}

interface Fixture {
  factory: IDBFactory;
  repository: BrowserDocumentAnnotationRepository;
  drafts: BrowserDraftRepository;
  acknowledge: ReturnType<typeof vi.fn>;
  pendingExpiryRetained: ReturnType<typeof vi.fn>;
  listeners: Set<() => void>;
  subscribeCount: () => number;
  storageSuccess: ReturnType<typeof vi.fn>;
  document: DocumentKey;
  operation: PreparedAnnotation;
  preparation: ComposerPreparation | null;
}

async function openFixture(options: FixtureOptions = {}): Promise<Fixture> {
  const factory = new IDBFactory();
  const drafts = createBrowserDraftRepository({
    indexedDB: factory,
    databaseName: "annotation-close",
    storage: new MemoryStorage(),
  });
  const key: BrowserDraftKey = browserDraftKey("root", "https://close.example");
  // Seed before the repository subscribes to drafts, so setup publishes no
  // storage-success notification of its own.
  let preparation: ComposerPreparation | null = null;
  if (options.seedDraft) {
    const seeded = await drafts.seedDraft(key, { text: "", skillNames: [], commandNames: [] });
    if (seeded.state !== "committed") throw new Error(`draft seed was not committed: ${seeded.state}`);
    preparation = { key, token: seeded.draft.token, backing: { kind: "draft" }, localGeneration: 0 };
  }
  const listeners = new Set<() => void>();
  let subscribeCalls = 0;
  const acknowledge = vi.fn();
  const source = {
    ref: "root",
    sourceId: "source-1",
    alive: true,
    subscribeRecoveryPersisted(listener: () => void) {
      subscribeCalls += 1;
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    // A seeded fixture hands the real append the token it needs, so a handoff
    // that is not stopped early reaches the append's own guards.
    prepareAnnotation: options.prepareAnnotation ?? (preparation ? async () => preparation : async () => null),
    ownsPreparation: () => true,
    acknowledgeAnnotation: acknowledge,
  } as unknown as ComposerSourceState;
  const storageSuccess = vi.fn();
  const pendingExpiryRetained = vi.fn();
  const repository = createDocumentAnnotationRepository({
    indexedDB: factory,
    databaseName: "annotation-close",
    drafts,
    targetFor: () => {
      options.onTarget?.();
      return source;
    },
    onStorageSuccess: storageSuccess,
    beforeExpiryFence: options.beforeExpiryFence,
    onPendingExpiryRetained: pendingExpiryRetained,
    onAppendReadsIssued: () => options.onAppendReadsIssued?.(),
  });
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
  return {
    factory,
    repository,
    drafts,
    acknowledge,
    pendingExpiryRetained,
    listeners,
    subscribeCount: () => subscribeCalls,
    storageSuccess,
    document,
    operation,
    preparation,
  };
}

// The adapter arms one recovery-persisted subscription per composer source
// when a handoff resolves that source. close() must release it: a surviving
// composer source otherwise keeps the closed repository alive and still
// invokes its storage-success callback after closure.
test("close releases the handoff's recovery-persisted subscription", async () => {
  const fixture = await openFixture();

  // The handoff resolves the source and arms its recovery notification before
  // preparing the append; a null preparation ends the handoff without
  // touching the composer.
  expect(await fixture.repository.handoff(fixture.document, fixture.operation.operationId)).toEqual({
    state: "pending",
    reason: "owner-unavailable",
  });
  expect(fixture.listeners.size).toBe(1);

  fixture.storageSuccess.mockClear();
  for (const listener of [...fixture.listeners]) listener();
  expect(fixture.storageSuccess).toHaveBeenCalledTimes(1);

  fixture.repository.close();
  // A late notification from the surviving source cannot reach the closed
  // repository's storage-success callback.
  for (const listener of [...fixture.listeners]) listener();
  expect(fixture.storageSuccess).toHaveBeenCalledTimes(1);
  expect(fixture.listeners.size).toBe(0);
  fixture.drafts.close();
});

// close() must fence work, not only release listeners: a handoff awaiting its
// preparation read would otherwise resume after closure, append through the
// retained composer, and notify storage success on a torn-down repository.
test("close while a handoff's preparation read is outstanding stops the resumed handoff", async () => {
  let markStarted: () => void = () => undefined;
  const preparationStarted = new Promise<void>((resolve) => {
    markStarted = resolve;
  });
  let resolvePreparation: (value: ComposerPreparation | null) => void = () => undefined;
  const fixture = await openFixture({
    seedDraft: true,
    prepareAnnotation: () => {
      markStarted();
      return new Promise((resolve) => {
        resolvePreparation = resolve;
      });
    },
  });
  if (!fixture.preparation) throw new Error("fixture seeded no preparation");

  const handoff = fixture.repository.handoff(fixture.document, fixture.operation.operationId);
  await preparationStarted;
  expect(fixture.subscribeCount()).toBe(1);
  fixture.storageSuccess.mockClear();
  fixture.repository.close();
  resolvePreparation(fixture.preparation);

  expect(await handoff).toEqual({ state: "pending", reason: "owner-unavailable" });
  expect(fixture.acknowledge).not.toHaveBeenCalled();
  expect(fixture.subscribeCount()).toBe(1);
  expect(fixture.listeners.size).toBe(0);
  expect(fixture.storageSuccess).not.toHaveBeenCalled();
  // The append never ran: the retained draft keeps its pre-annotation text.
  expect((await fixture.drafts.load(fixture.preparation.key))?.text).toBe("");
  fixture.drafts.close();
});

// The arm site is after the destination resolves and before the preparation
// read; closure at that boundary must register no recovery listener.
test("close before a handoff arms its listener registers nothing", async () => {
  let fixture: Fixture | undefined;
  fixture = await openFixture({ onTarget: () => fixture?.repository.close() });
  expect(fixture.subscribeCount()).toBe(0);

  expect(await fixture.repository.handoff(fixture.document, fixture.operation.operationId)).toEqual({
    state: "pending",
    reason: "owner-unavailable",
  });
  expect(fixture.subscribeCount()).toBe(0);
  expect(fixture.listeners.size).toBe(0);
  fixture.drafts.close();
});

// The append's own reads are its last asynchronous boundary before the final
// guard; closure while they are outstanding must stop the commit.
test("close during the append's reads stops the final commit", async () => {
  let fixture: Fixture | undefined;
  fixture = await openFixture({
    seedDraft: true,
    onAppendReadsIssued: () => fixture?.repository.close(),
  });
  if (!fixture.preparation) throw new Error("fixture seeded no preparation");

  fixture.storageSuccess.mockClear();
  expect(await fixture.repository.handoff(fixture.document, fixture.operation.operationId)).toEqual({
    state: "pending",
    reason: "owner-unavailable",
  });
  expect(fixture.acknowledge).not.toHaveBeenCalled();
  expect(fixture.storageSuccess).not.toHaveBeenCalled();
  expect((await fixture.drafts.load(fixture.preparation.key))?.text).toBe("");
  fixture.drafts.close();
});

// A changed read validates in one transaction and fences in a second, with an
// observation boundary between them. Closure at that boundary must stop the
// fence before it opens a connection again: a resumed fence would rewrite the
// durable head after application teardown. The refusal is a stale ticket, not
// a retained expiry: this adapter holds no retry work past close().
test("close between a changed read's validation and its expiry fence stops the fence", async () => {
  let markFence: () => void = () => undefined;
  const fenceReached = new Promise<void>((resolve) => {
    markFence = resolve;
  });
  let releaseFence: () => void = () => undefined;
  const fenceGate = new Promise<void>((resolve) => {
    releaseFence = resolve;
  });
  const fixture = await openFixture({
    beforeExpiryFence: () => {
      markFence();
      return fenceGate;
    },
  });
  const changed: DocFileContent = {
    text: "Changed content\n",
    binary: false,
    mediaType: "text/markdown",
    truncated: false,
    sizeBytes: 17,
  };
  const ticket = await fixture.repository.beginRead(fixture.document);
  const acceptance = fixture.repository.acceptRead(ticket, changed);
  await fenceReached;
  fixture.storageSuccess.mockClear();
  fixture.repository.close();
  releaseFence();

  expect(await acceptance).toEqual({ state: "stale-ticket" });
  // No post-close notification and no retained work this closed adapter could
  // never retry.
  expect(fixture.pendingExpiryRetained).not.toHaveBeenCalled();
  expect(fixture.storageSuccess).not.toHaveBeenCalled();
  // No fence landed: a fresh adapter still reads the head the first acceptance
  // established, not the replacement epoch a resumed fence would have written.
  const probe = createDocumentAnnotationRepository({
    indexedDB: fixture.factory,
    databaseName: "annotation-close",
    drafts: fixture.drafts,
    targetFor: () => null,
  });
  const read = await probe.beginRead(fixture.document);
  expect(read.headRevision).toBe(fixture.operation.lease.headRevision);
  probe.close();
  fixture.drafts.close();
});

// The runtime's retry pass sweeps persisted work before the separate outcome
// scan opens its own connection. Closure landing while the pass is outstanding
// must reach that scan too: the scan's promise keeps the caller's retry-needed
// state alive, so it rejects as a storage refusal instead of reopening the
// closed adapter and reading through it.
test("close during an outstanding retry pass stops the outcome scan reopening", async () => {
  const fixture = await openFixture({ seedDraft: true });
  const open = vi.spyOn(fixture.factory, "open");
  // The pass's pending-operation read is its last transaction before the scan.
  const hold = holdNextTransactionEvent(
    [ANNOTATION_OPERATIONS_STORE, ANNOTATION_PAYLOADS_STORE],
    "complete",
    "readonly",
  );
  const retry = fixture.repository.retryPendingWithOutcome("storage-recovered");
  await hold.reached;
  fixture.repository.close();
  const opensAtClose = open.mock.calls.length;
  expect(opensAtClose).toBe(0);
  hold.release();

  await expect(retry).rejects.toThrow("Document annotation repository is closed");
  expect(open).toHaveBeenCalledTimes(opensAtClose);
  fixture.drafts.close();
});
