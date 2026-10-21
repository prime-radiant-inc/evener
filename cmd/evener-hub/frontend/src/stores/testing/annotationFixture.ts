// Shared setup for the document-annotation suites.
//
// Every suite opens the same platform - one fake factory, one drafts
// repository, one annotation repository and the document key they share - then
// wires the composer seams its own scenario needs. The options here are the
// real drifts between those suites: whether a draft is seeded, whether delivery
// listeners are tracked, and whether the repository's storage-success boundary
// reaches a runtime. Everything else is fixed, so a suite can compare its
// assertions against the same accepted lease, operation and content.
import {
  type AcceptedSourceLease,
  type DestinationProvenance,
  type DocFileContent,
  type DocumentKey,
  displayedFingerprint,
  type PreparedAnnotation,
} from "@evener/appwire-client";
import { IDBFactory } from "fake-indexeddb";
import { vi } from "vitest";
import type { ComposerPreparation, ComposerSourceState } from "../../panes/session/composer/composerContract";
import { MemoryStorage } from "../../storageTestUtils";
import {
  type BrowserDraftKey,
  type BrowserDraftRepository,
  browserDraftKey,
  createBrowserDraftRepository,
} from "../browserDraftRepository";
import {
  type BrowserDocumentAnnotationRepository,
  createDocumentAnnotationRepository,
  type DocumentAnnotationOptions,
} from "../documentAnnotationIndexedDB";
import { createDocumentAnnotationRuntime, type DocumentAnnotationRuntime } from "../documentAnnotationRuntime";
import { nextRealTask } from "./stalledIndexedDB";

// A one-shot gate: the test waits on `promise` and releases or answers it from
// any later boundary.
export interface Deferred<T> {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (error: unknown) => void;
}

export function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

// Yield to the real task queue until `condition` holds, bounded, so work parked
// behind fake-indexeddb's delivery can progress without its fake watchdog
// firing first. The bound is a tripwire, not the mechanism: a fixed hop count
// passes () => false.
export async function pollUntil(condition: () => boolean, maxHops: number): Promise<void> {
  for (let hop = 0; hop < maxHops && !condition(); hop += 1) await nextRealTask();
}

export function sampleAnnotationContent(): DocFileContent {
  return {
    text: "A note with an [image 1] marker\n",
    binary: false,
    mediaType: "text/markdown",
    truncated: false,
    sizeBytes: 29,
  };
}

export function buildAnnotationOperation(
  lease: AcceptedSourceLease,
  content: DocFileContent,
  destination: PreparedAnnotation["destination"] = {
    kind: "browser-origin",
    paneId: "pane-1",
    compositionOwnerId: "source-1",
  },
): PreparedAnnotation {
  return {
    operationId: "operation-1",
    lease,
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
      displayedSnapshotId: lease.displayedSnapshotId,
    },
    note: "note",
    destination,
  };
}

export interface AnnotationStoreOptions {
  databaseName: string;
  origin: string;
  targetFor?: DocumentAnnotationOptions["targetFor"];
  // A fault injected at a commit boundary: a throw aborts that transaction.
  beforeCommit?: DocumentAnnotationOptions["beforeCommit"];
  // Runs after a changed read validates and before its expiry fence opens.
  beforeExpiryFence?: DocumentAnnotationOptions["beforeExpiryFence"];
  // Runs once the append has issued all of its reads, before it awaits them.
  onAppendReadsIssued?: DocumentAnnotationOptions["onAppendReadsIssued"];
  onStorageSuccess?: () => void;
  onPendingExpiryRetained?: () => void;
  onCancelledDiscardRetained?: () => void;
  // Runs after the factory, drafts repository and key exist and BEFORE the
  // annotation repository subscribes to drafts, so setup work publishes no
  // storage-success notification of its own. Returns the preparation the seeded
  // draft yields.
  seed?: (parts: { drafts: BrowserDraftRepository; key: BrowserDraftKey }) => Promise<ComposerPreparation | null>;
}

export interface AnnotationStore {
  factory: IDBFactory;
  drafts: BrowserDraftRepository;
  repository: BrowserDocumentAnnotationRepository;
  key: BrowserDraftKey;
  document: DocumentKey;
  preparation: ComposerPreparation | null;
}

// The shared platform: one fake factory, one drafts repository, one document
// key, and the annotation repository over all three. Suites that need no seeded
// annotation (the stall cases) open exactly this and prime the rows themselves.
export async function openAnnotationStore(options: AnnotationStoreOptions): Promise<AnnotationStore> {
  const factory = new IDBFactory();
  const drafts = createBrowserDraftRepository({
    indexedDB: factory,
    databaseName: options.databaseName,
    storage: new MemoryStorage(),
  });
  const key = browserDraftKey("root", options.origin);
  const preparation = options.seed ? await options.seed({ drafts, key }) : null;
  const document: DocumentKey = { ...key, path: "NOTES.md" };
  const repository = createDocumentAnnotationRepository({
    indexedDB: factory,
    databaseName: options.databaseName,
    drafts,
    targetFor: options.targetFor ?? (() => null),
    beforeCommit: options.beforeCommit,
    beforeExpiryFence: options.beforeExpiryFence,
    onAppendReadsIssued: options.onAppendReadsIssued,
    onStorageSuccess: options.onStorageSuccess,
    onPendingExpiryRetained: options.onPendingExpiryRetained,
    onCancelledDiscardRetained: options.onCancelledDiscardRetained,
  });
  return { factory, drafts, repository, key, document, preparation };
}

export interface AnnotationFixtureOptions
  extends Omit<
    AnnotationStoreOptions,
    "seed" | "targetFor" | "onStorageSuccess" | "onPendingExpiryRetained" | "onCancelledDiscardRetained"
  > {
  // Whether the drafts repository holds the ordinary row a real append needs.
  seedDraft?: boolean;
  prepareAnnotation?: () => Promise<ComposerPreparation | null>;
  acknowledgeAnnotation?: (preparation: ComposerPreparation, token: ComposerPreparation["token"]) => void;
  destination?: DestinationProvenance;
  // Resolve the destination's composer source differently from the fixture's
  // single built source: a suite serving two browser namespaces wires one
  // source per namespace here. When absent, every destination resolves to the
  // built source.
  targetFor?: DocumentAnnotationOptions["targetFor"];
  // The close suite observes what the adapter arms on its source, so it tracks
  // every registration instead of discarding them.
  trackRecoveryListeners?: boolean;
  // Production wiring routes the repository's storage-success boundary into
  // the runtime's recovery trigger.
  routeStorageSuccessIntoRuntime?: boolean;
  // Production wiring routes a faulted cancelled-save discard into the
  // runtime's retry need, so a later trigger spends the pass that completes it.
  routeCancelledDiscardIntoRuntime?: boolean;
  // The recovery suite drives the runtime the fixture builds; the other suites
  // build their own or use none.
  runtime?: boolean;
  // Runs when a handoff resolves its destination, immediately before the arm.
  onTarget?: () => void;
}

export interface AnnotationFixture extends AnnotationStore {
  runtime: DocumentAnnotationRuntime | undefined;
  source: ComposerSourceState;
  acknowledge: ReturnType<typeof vi.fn>;
  storageSuccess: ReturnType<typeof vi.fn>;
  pendingExpiryRetained: ReturnType<typeof vi.fn>;
  cancelledDiscardRetained: ReturnType<typeof vi.fn>;
  listeners: Set<() => void>;
  subscribeCount: () => number;
  // The namespace incarnation the fixture's first read was issued under.
  incarnation: string;
  content: DocFileContent;
  operation: PreparedAnnotation;
}

export interface AnnotationFixtureWithRuntime extends AnnotationFixture {
  runtime: DocumentAnnotationRuntime;
}

export function openAnnotationFixture(
  options: AnnotationFixtureOptions & { runtime: true },
): Promise<AnnotationFixtureWithRuntime>;
export function openAnnotationFixture(options: AnnotationFixtureOptions): Promise<AnnotationFixture>;
export async function openAnnotationFixture(options: AnnotationFixtureOptions): Promise<AnnotationFixture> {
  const listeners = new Set<() => void>();
  let subscribeCalls = 0;
  const acknowledge = vi.fn((preparation: ComposerPreparation, token: ComposerPreparation["token"]) => {
    options.acknowledgeAnnotation?.(preparation, token);
  });
  // The composer source does not exist until its seeded draft token does; the
  // repository resolves it only during a handoff, after setup.
  let source: ComposerSourceState | undefined;
  let runtime: DocumentAnnotationRuntime | undefined;
  const storageSuccess = vi.fn(() => {
    if (options.routeStorageSuccessIntoRuntime === true) runtime?.storageSucceeded();
  });
  const pendingExpiryRetained = vi.fn();
  const cancelledDiscardRetained = vi.fn(() => {
    if (options.routeCancelledDiscardIntoRuntime === true) runtime?.registerRetryNeed();
  });
  const store = await openAnnotationStore({
    databaseName: options.databaseName,
    origin: options.origin,
    targetFor: (destination, key) => {
      options.onTarget?.();
      return options.targetFor ? options.targetFor(destination, key) : (source ?? null);
    },
    beforeCommit: options.beforeCommit,
    beforeExpiryFence: options.beforeExpiryFence,
    onAppendReadsIssued: options.onAppendReadsIssued,
    onStorageSuccess: storageSuccess,
    onPendingExpiryRetained: pendingExpiryRetained,
    onCancelledDiscardRetained: cancelledDiscardRetained,
    seed:
      options.seedDraft === false
        ? undefined
        : async ({ drafts, key }) => {
            const seeded = await drafts.seedDraft(key, { text: "", skillNames: [], commandNames: [] });
            if (seeded.state !== "committed") throw new Error(`draft seed was not committed: ${seeded.state}`);
            return { key, token: seeded.draft.token, backing: { kind: "draft" }, localGeneration: 0 };
          },
  });
  const builtSource = {
    ref: "root",
    sourceId: "source-1",
    alive: true,
    subscribeRecoveryPersisted:
      options.trackRecoveryListeners === true
        ? (listener: () => void) => {
            subscribeCalls += 1;
            listeners.add(listener);
            return () => listeners.delete(listener);
          }
        : () => () => undefined,
    // A seeded fixture hands the real append the token it needs; an unseeded one
    // answers no preparation.
    prepareAnnotation: options.prepareAnnotation ?? (async () => store.preparation),
    ownsPreparation: () => true,
    acknowledgeAnnotation: acknowledge,
  } as unknown as ComposerSourceState;
  source = builtSource;
  if (options.runtime === true) runtime = createDocumentAnnotationRuntime(store.repository);
  const content = sampleAnnotationContent();
  const ticket = await store.repository.beginRead(store.document);
  const accepted = await store.repository.acceptRead(ticket, content);
  if (accepted.state !== "accepted") throw new Error(`read was not accepted: ${accepted.state}`);
  const operation = buildAnnotationOperation(accepted.lease, content, options.destination);
  await store.repository.saveAnnotation(operation);
  return {
    ...store,
    runtime,
    source: builtSource,
    acknowledge,
    storageSuccess,
    pendingExpiryRetained,
    cancelledDiscardRetained,
    listeners,
    subscribeCount: () => subscribeCalls,
    incarnation: ticket.namespaceIncarnation,
    content,
    operation,
  };
}
