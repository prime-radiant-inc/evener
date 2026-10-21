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

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

interface FixtureOptions {
  prepareAnnotation?: () => Promise<ComposerPreparation | null>;
  destination?: DestinationProvenance;
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
  const acknowledge = vi.fn();
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
