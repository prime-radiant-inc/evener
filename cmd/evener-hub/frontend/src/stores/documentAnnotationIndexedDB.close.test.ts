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

afterEach(() => {
  vi.restoreAllMocks();
});

interface FixtureOptions {
  prepareAnnotation?: () => Promise<ComposerPreparation | null>;
  // Runs when a handoff resolves its destination, immediately before the arm.
  onTarget?: () => void;
  // Runs once the append has issued all of its reads, before it awaits them.
  onAppendReadsIssued?: () => void;
  // Seeds the ordinary draft row a real append needs to validate its token.
  seedDraft?: boolean;
}

interface Fixture {
  repository: BrowserDocumentAnnotationRepository;
  drafts: BrowserDraftRepository;
  acknowledge: ReturnType<typeof vi.fn>;
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
  const repository = createDocumentAnnotationRepository({
    indexedDB: factory,
    databaseName: "annotation-close",
    drafts,
    targetFor: () => {
      options.onTarget?.();
      return source;
    },
    onStorageSuccess: storageSuccess,
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
    repository,
    drafts,
    acknowledge,
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
