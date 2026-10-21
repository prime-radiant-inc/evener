// @vitest-environment node

import {
  type DestinationProvenance,
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
  APPEND_STORES,
  type BrowserDraftKey,
  type BrowserDraftRepository,
  browserDraftKey,
  createBrowserDraftRepository,
} from "./browserDraftRepository";
import {
  type BrowserDocumentAnnotationRepository,
  createDocumentAnnotationRepository,
} from "./documentAnnotationIndexedDB";
import { createDocumentAnnotationRuntime, type DocumentAnnotationRuntime } from "./documentAnnotationRuntime";
import {
  ANNOTATION_EVIDENCE_STORE,
  ANNOTATION_HEADS_STORE,
  ANNOTATION_NAMESPACES_STORE,
  ANNOTATION_OPERATIONS_STORE,
  ANNOTATION_PAYLOADS_STORE,
  ANNOTATION_SEQUENCES_STORE,
} from "./localCompositionSchema";
import { holdNextWriteTransaction, nextRealTask } from "./testing/stalledIndexedDB";

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

interface FixtureOptions {
  prepareAnnotation?: () => Promise<ComposerPreparation | null>;
  destination?: DestinationProvenance;
  // A real composer adopts the committed token a reconciliation hands it: that
  // adoption is what makes a later handoff's preparation current again.
  acknowledgeAnnotation?: (preparation: ComposerPreparation, token: ComposerPreparation["token"]) => void;
}

interface Fixture {
  factory: IDBFactory;
  repository: BrowserDocumentAnnotationRepository;
  drafts: BrowserDraftRepository;
  runtime: DocumentAnnotationRuntime;
  acknowledge: ReturnType<typeof vi.fn>;
  storageSuccess: ReturnType<typeof vi.fn>;
  key: BrowserDraftKey;
  document: DocumentKey;
  content: DocFileContent;
  operation: PreparedAnnotation;
  preparation: ComposerPreparation;
}

async function openFixture(options: FixtureOptions = {}): Promise<Fixture> {
  const factory = new IDBFactory();
  const drafts = createBrowserDraftRepository({
    indexedDB: factory,
    databaseName: "annotation-recovery",
    storage: new MemoryStorage(),
  });
  const key = browserDraftKey("root", "https://recovery.example");
  // Seed before the repository subscribes to drafts, so setup publishes no
  // storage-success notification of its own.
  const seeded = await drafts.seedDraft(key, { text: "", skillNames: [], commandNames: [] });
  if (seeded.state !== "committed") throw new Error(`draft seed was not committed: ${seeded.state}`);
  const preparation: ComposerPreparation = {
    key,
    token: seeded.draft.token,
    backing: { kind: "draft" },
    localGeneration: 0,
  };
  const acknowledge = vi.fn((preparation: ComposerPreparation, token: ComposerPreparation["token"]) => {
    options.acknowledgeAnnotation?.(preparation, token);
  });
  const source = {
    ref: "root",
    sourceId: "source-1",
    alive: true,
    subscribeRecoveryPersisted: () => () => undefined,
    prepareAnnotation: options.prepareAnnotation ?? (async () => preparation),
    ownsPreparation: () => true,
    acknowledgeAnnotation: acknowledge,
  } as unknown as ComposerSourceState;
  let runtime: DocumentAnnotationRuntime | undefined;
  // Production wiring: the repository's post-commit storage-success boundary is
  // the runtime's recovery trigger, so a notification the repository never
  // sends is one the runtime never sees.
  const storageSuccess = vi.fn(() => {
    runtime?.storageSucceeded();
  });
  const repository = createDocumentAnnotationRepository({
    indexedDB: factory,
    databaseName: "annotation-recovery",
    drafts,
    targetFor: () => source,
    onStorageSuccess: storageSuccess,
  });
  runtime = createDocumentAnnotationRuntime(repository);
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
    destination: options.destination ?? { kind: "browser-origin", paneId: "pane-1", compositionOwnerId: "source-1" },
  };
  await repository.saveAnnotation(operation);
  return {
    factory,
    repository,
    drafts,
    runtime,
    acknowledge,
    storageSuccess,
    key,
    document,
    content,
    operation,
    preparation,
  };
}

// A changed read installs its provisional suppression at entry and releases it
// only when its durable validation answers. A handoff overlapping that window
// refuses against the provisional restriction and returns pending, recording
// the runtime's retry need. A validation superseded by another adapter proves
// nothing about the change, so it releases the restriction - and that release
// is the only event that can spend the recorded need. Without a storage-success
// signal at that boundary no pass starts, and the eligible annotation waits for
// unrelated activity.
test("a stale validation that releases provisional suppression signals the pending handoff", async () => {
  let markPreparing: () => void = () => undefined;
  const preparing = new Promise<void>((resolve) => {
    markPreparing = resolve;
  });
  let releasePreparation: () => void = () => undefined;
  const preparationReleased = new Promise<void>((resolve) => {
    releasePreparation = resolve;
  });
  let preparations = 0;
  let fixture: Fixture | undefined;
  fixture = await openFixture({
    prepareAnnotation: async () => {
      preparations += 1;
      // The first handoff finishes preparation while the overlapping
      // acceptance's provisional suppression is active, so it refuses.
      if (preparations === 1) {
        markPreparing();
        await preparationReleased;
        return null;
      }
      return fixture?.preparation ?? null;
    },
  });

  // This adapter issues a newer read, so the acceptance below is the invocation
  // that owns the provisional suppression its local comparison installs.
  const ticket = await fixture.repository.beginRead(fixture.document);
  // Another adapter's read supersedes that ticket durably: the validation can
  // only answer stale, which releases the provisional entry.
  const sibling = createDocumentAnnotationRepository({
    indexedDB: fixture.factory,
    databaseName: "annotation-recovery",
    drafts: fixture.drafts,
    targetFor: () => null,
  });
  await sibling.beginRead(fixture.document);

  fixture.storageSuccess.mockClear();
  const handoff = fixture.runtime.handoff(fixture.document, fixture.operation.operationId);
  await preparing;
  // The handoff is mid-preparation. The overlapping acceptance installs its
  // provisional suppression synchronously at entry, before its first await.
  const changed: DocFileContent = {
    text: "Changed content\n",
    binary: false,
    mediaType: "text/markdown",
    truncated: false,
    sizeBytes: 17,
  };
  const acceptance = fixture.repository.acceptRead(ticket, changed);
  releasePreparation();

  expect(await handoff).toEqual({ state: "pending", reason: "owner-unavailable" });
  expect(await acceptance).toEqual({ state: "stale-ticket" });
  // No further external activity: the release of the provisional suppression
  // must be what starts the pass that completes the refused handoff.
  await fixture.runtime.whenIdle();

  const views = await fixture.repository.listAnnotations(fixture.document);
  expect(views.find((view) => view.operation.operationId === fixture.operation.operationId)?.handoff.state).toBe(
    "already-applied",
  );
  expect(fixture.storageSuccess).toHaveBeenCalled();

  sibling.close();
  fixture.repository.close();
  fixture.drafts.close();
});

// A save can commit while its completion stays unobserved: the watchdog
// rejects before saveAnnotation reaches the wake it publishes only after the
// transaction wrapper settles. The runtime retains the failed attempt, and its
// retry finds the operation already durable - newlySaved stays false - so the
// ordinary replay publishes no wake either. For an ownerless destination no
// later append supplies the missing notification, and subscribed readers stay
// stale despite the successful recovery. The durable row the replay confirms is
// the only cross-adapter evidence of that commit, so the replay must publish
// the invalidation the lost attempt could not.
test("a save replay whose first completion went unobserved wakes subscribed readers", async () => {
  const fixture = await openFixture({
    destination: { kind: "browser-readonly-session", sourcePaneId: "pane-1" },
  });
  const operation: PreparedAnnotation = {
    ...fixture.operation,
    operationId: "operation-2",
    destination: { kind: "browser-readonly-session", sourcePaneId: "pane-1" },
  };
  const listener = vi.fn();
  fixture.repository.subscribe(fixture.document, listener);

  // The next save transaction commits, but its completion event is held: the
  // write is durable while everything awaiting the event still waits.
  const hold = holdNextWriteTransaction([
    ANNOTATION_NAMESPACES_STORE,
    ANNOTATION_HEADS_STORE,
    ANNOTATION_EVIDENCE_STORE,
    ANNOTATION_PAYLOADS_STORE,
    ANNOTATION_OPERATIONS_STORE,
    ANNOTATION_SEQUENCES_STORE,
  ]);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  let reached = false;
  void hold.reached.then(() => {
    reached = true;
  });
  const firstSave = fixture.runtime.save(operation);
  for (let i = 0; i < 25 && !reached; i += 1) await nextRealTask();
  expect(reached).toBe(true);
  await vi.advanceTimersByTimeAsync(10_001);

  // The save reported failure and retained its operation; the commit it could
  // not observe never reached the wake publication.
  expect(await firstSave).toBeNull();
  expect(listener).not.toHaveBeenCalled();
  hold.release();
  vi.useRealTimers();

  // Recovery: the retained attempt replays against the durable row, and the
  // ownerless destination settles its handoff without an append of its own.
  await fixture.runtime.retryPending("storage-recovered");
  await fixture.runtime.whenIdle();

  const views = await fixture.repository.listAnnotations(fixture.document);
  expect(views.find((view) => view.operation.operationId === operation.operationId)?.handoff.state).toBe("pending");
  expect(listener).toHaveBeenCalled();

  fixture.repository.close();
  fixture.drafts.close();
});

// Recovery from an append whose completion went unobserved is a two-operation
// story. The committed append A advances the composer's durable token while the
// live composer still holds its pre-append one, so the persisted pending handoff
// B - swept FIRST - refuses revision-conflict against that stale token.
// Reconciliation then acknowledges A, and that acknowledgment is exactly the
// moment the composer becomes ready for B again. Reconciliation answers an
// applied receipt and publishes its wake to readers, not a storage-success
// signal to the retry runtime, so a sweep that reconciles after it already
// visited B stops with B pending and nothing left to trigger a revisit. The
// sweep must reconcile the uncertain appends it retained BEFORE it visits the
// persisted pending handoffs, so the composer those handoffs prepare against is
// the reconciled one.
test("an unobserved append reconciles before the pending sweep so a later handoff applies", async () => {
  const composer: { preparation: ComposerPreparation | null } = { preparation: null };
  const fixture = await openFixture({
    prepareAnnotation: async () => composer.preparation,
    acknowledgeAnnotation: (preparation, token) => {
      composer.preparation = { ...preparation, token };
    },
  });
  composer.preparation = fixture.preparation;
  const operationB: PreparedAnnotation = { ...fixture.operation, operationId: "operation-2" };
  await fixture.runtime.save(operationB);
  fixture.storageSuccess.mockClear();

  // A's next append transaction commits, but its completion event is held: the
  // applied receipt and the composer's new durable token exist while the
  // handoff that produced them waits on an event the watchdog outlives.
  const hold = holdNextWriteTransaction(APPEND_STORES);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  let reached = false;
  void hold.reached.then(() => {
    reached = true;
  });
  const appendA = fixture.runtime.handoff(fixture.document, fixture.operation.operationId);
  for (let i = 0; i < 50 && !reached; i += 1) await nextRealTask();
  expect(reached).toBe(true);
  await vi.advanceTimersByTimeAsync(10_001);

  // The append is durable but unobserved: the handoff retains its composer seam
  // and answers pending, exactly the state reconciliation exists to resolve.
  expect(await appendA).toEqual({ state: "pending", reason: "owner-unavailable" });
  hold.release();
  vi.useRealTimers();

  // ONE recovery pass, with no external trigger after it. B must come out of the
  // same pass applied: reconciliation makes its preparation current before the
  // sweep reaches it.
  await fixture.runtime.retryPending("storage-recovered");
  await fixture.runtime.whenIdle();

  // Reconciliation ran - the composer now carries the token A committed - yet B
  // is still pending: the sweep visited it before the acknowledgment landed.
  expect(fixture.acknowledge).toHaveBeenCalled();
  const views = await fixture.repository.listAnnotations(fixture.document);
  expect(views.find((view) => view.operation.operationId === operationB.operationId)?.handoff.state).toBe(
    "already-applied",
  );

  fixture.repository.close();
  fixture.drafts.close();
});
