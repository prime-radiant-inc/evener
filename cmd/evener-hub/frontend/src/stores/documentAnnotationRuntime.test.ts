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
import { createDocumentAnnotationRuntime } from "./documentAnnotationRuntime";

afterEach(() => {
  vi.restoreAllMocks();
});

interface Fixture {
  repository: BrowserDocumentAnnotationRepository;
  drafts: BrowserDraftRepository;
  acknowledge: ReturnType<typeof vi.fn>;
  storageSuccess: ReturnType<typeof vi.fn>;
  key: BrowserDraftKey;
  document: DocumentKey;
  operation: PreparedAnnotation;
  preparation: ComposerPreparation;
}

async function openFixture(
  prepareAnnotation: () => Promise<ComposerPreparation | null>,
  onAppendReadsIssued?: () => void,
): Promise<Fixture> {
  const factory = new IDBFactory();
  const drafts = createBrowserDraftRepository({
    indexedDB: factory,
    databaseName: "annotation-runtime",
    storage: new MemoryStorage(),
  });
  const key = browserDraftKey("root", "https://runtime.example");
  const seeded = await drafts.seedDraft(key, { text: "", skillNames: [], commandNames: [] });
  if (seeded.state !== "committed") throw new Error(`draft seed was not committed: ${seeded.state}`);
  const preparation: ComposerPreparation = {
    key,
    token: seeded.draft.token,
    backing: { kind: "draft" },
    localGeneration: 0,
  };
  const acknowledge = vi.fn();
  const source = {
    ref: "root",
    sourceId: "source-1",
    alive: true,
    subscribeRecoveryPersisted: () => () => undefined,
    prepareAnnotation,
    ownsPreparation: () => true,
    acknowledgeAnnotation: acknowledge,
  } as unknown as ComposerSourceState;
  const storageSuccess = vi.fn();
  const repository = createDocumentAnnotationRepository({
    indexedDB: factory,
    databaseName: "annotation-runtime",
    drafts,
    targetFor: () => source,
    onStorageSuccess: storageSuccess,
    onAppendReadsIssued: () => onAppendReadsIssued?.(),
  });
  const document: DocumentKey = { ...key, path: "NOTES.md" };
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
  return { repository, drafts, acknowledge, storageSuccess, key, document, operation, preparation };
}

// Disposal is the runtime's only terminal state. The runtime cancels its own
// loop between handoffs, but a handoff already awaiting the composer's
// preparation must observe that cancellation too: otherwise the resolved
// preparation appends to the retained draft and acknowledges a composer that
// application teardown already discarded. The repository's handoff fences
// closure; the runtime must propagate its disposal the same way.
test("dispose while a handoff's preparation is outstanding stops the resumed handoff", async () => {
  let markStarted: () => void = () => undefined;
  const preparationStarted = new Promise<void>((resolve) => {
    markStarted = resolve;
  });
  let resolvePreparation: (value: ComposerPreparation | null) => void = () => undefined;
  const fixture = await openFixture(() => {
    markStarted();
    return new Promise((resolve) => {
      resolvePreparation = resolve;
    });
  });
  const runtime = createDocumentAnnotationRuntime(fixture.repository);

  const handoff = runtime.handoff(fixture.document, fixture.operation.operationId);
  await preparationStarted;
  fixture.storageSuccess.mockClear();
  runtime.dispose();
  resolvePreparation(fixture.preparation);

  expect(await handoff).toEqual({ state: "pending", reason: "owner-unavailable" });
  expect(fixture.acknowledge).not.toHaveBeenCalled();
  expect(fixture.storageSuccess).not.toHaveBeenCalled();
  // The append never ran: the retained draft keeps its pre-annotation text.
  expect((await fixture.drafts.load(fixture.key))?.text).toBe("");
  fixture.repository.close();
  fixture.drafts.close();
});

// The append's own reads are its last asynchronous boundary before the final
// eligibility guard. Disposal there must reach the guard through the same
// handoff cancellation, or the resumed append commits and acknowledges the
// discarded composer.
test("dispose during the append's reads stops the final commit", async () => {
  let fixture: Fixture | undefined;
  let runtime: ReturnType<typeof createDocumentAnnotationRuntime> | undefined;
  fixture = await openFixture(
    async () => fixture?.preparation ?? null,
    () => runtime?.dispose(),
  );
  runtime = createDocumentAnnotationRuntime(fixture.repository);
  fixture.storageSuccess.mockClear();

  expect(await runtime.handoff(fixture.document, fixture.operation.operationId)).toEqual({
    state: "pending",
    reason: "owner-unavailable",
  });
  expect(fixture.acknowledge).not.toHaveBeenCalled();
  expect(fixture.storageSuccess).not.toHaveBeenCalled();
  expect((await fixture.drafts.load(fixture.key))?.text).toBe("");
  fixture.repository.close();
  fixture.drafts.close();
});

// A recovery trigger that arrives while a pass is running names state that pass
// may already have visited: the composer its handoff skipped can have become
// claimable after the sweep moved on. The trigger must be retained and run one
// coalesced follow-up, or the annotation stays pending until unrelated activity
// starts a later pass.
test("a storage success during an active pass runs one follow-up after the sweep skipped the operation", async () => {
  let markSecondHandoffStarted: () => void = () => undefined;
  const secondHandoffStarted = new Promise<void>((resolve) => {
    markSecondHandoffStarted = resolve;
  });
  let releaseSecondHandoff: () => void = () => undefined;
  const secondHandoffReleased = new Promise<void>((resolve) => {
    releaseSecondHandoff = resolve;
  });
  let preparations = 0;
  let fixture: Fixture | undefined;
  fixture = await openFixture(async () => {
    preparations += 1;
    // The first operation's handoff is skipped: no composer claims it.
    if (preparations === 1) return null;
    // Block the second handoff so the pass is still active after the sweep
    // visited the first operation.
    if (preparations === 2) {
      markSecondHandoffStarted();
      await secondHandoffReleased;
      return null;
    }
    // Every later pass sees the composer, now that it became available.
    return fixture?.preparation ?? null;
  });
  const secondOperation: PreparedAnnotation = { ...fixture.operation, operationId: "operation-2" };
  await fixture.repository.saveAnnotation(secondOperation);
  const runtime = createDocumentAnnotationRuntime(fixture.repository);

  const pass = runtime.retryPending("composer-open");
  await secondHandoffStarted;
  // The sweep already visited operation-1 and is blocked in operation-2's
  // handoff. The composer's storage commits land during that window, and both
  // coalesce into one follow-up pass.
  runtime.storageSucceeded();
  runtime.storageSucceeded();
  releaseSecondHandoff();
  await pass;
  await runtime.whenIdle();

  const views = await fixture.repository.listAnnotations(fixture.document);
  // A persisted applied row lists as already-applied: the follow-up's append
  // committed, so the annotation is no longer pending.
  expect(views.find((view) => view.operation.operationId === fixture.operation.operationId)?.handoff.state).toBe(
    "already-applied",
  );
  // Two operations in the skipped pass plus two in the one follow-up; a second
  // follow-up would prepare the still-pending operation again.
  expect(preparations).toBe(4);

  fixture.repository.close();
  fixture.drafts.close();
});

// A direct handoff queued during an active pass is work that pass's follow-up
// must run behind, never work the pass itself waits for. A storage commit while
// the handoff waits asks the pass for a follow-up; if the handoff also waited
// for the follow-up to settle, each would wait for the other and neither the
// handoff, nor any later retry, nor whenIdle() would ever settle.
test("a direct handoff queued during an active pass settles when the pass runs a follow-up", async () => {
  let markSweepBlocked: () => void = () => undefined;
  const sweepBlocked = new Promise<void>((resolve) => {
    markSweepBlocked = resolve;
  });
  let releaseSweep: () => void = () => undefined;
  const sweepReleased = new Promise<void>((resolve) => {
    releaseSweep = resolve;
  });
  let preparations = 0;
  let fixture: Fixture | undefined;
  fixture = await openFixture(async () => {
    preparations += 1;
    // The sweep's handoff is skipped: no composer claims it. Holding it open
    // keeps the pass active while the reader saves and storage commits.
    if (preparations === 1) {
      markSweepBlocked();
      await sweepReleased;
      return null;
    }
    return fixture?.preparation ?? null;
  });
  const runtime = createDocumentAnnotationRuntime(fixture.repository);

  void runtime.retryPending("composer-open");
  await sweepBlocked;
  // A save during the active pass reaches the direct handoff path, which joins
  // the handoff chain behind the running pass. The storage commit then asks
  // that pass for one follow-up.
  const handoff = runtime.handoff(fixture.document, fixture.operation.operationId);
  runtime.storageSucceeded();
  const settled = Promise.all([handoff, runtime.whenIdle()]).then(() => "settled");
  releaseSweep();

  // A stalled handoff must read as a stall here, not hold the suite until its
  // timeout.
  await expect(
    Promise.race([settled, new Promise<string>((resolve) => setTimeout(() => resolve("stalled"), 1000))]),
  ).resolves.toBe("settled");
  await expect(handoff).resolves.toMatchObject({ state: "applied" });
  const views = await fixture.repository.listAnnotations(fixture.document);
  expect(views.find((view) => view.operation.operationId === fixture.operation.operationId)?.handoff.state).toBe(
    "already-applied",
  );

  fixture.repository.close();
  fixture.drafts.close();
});
