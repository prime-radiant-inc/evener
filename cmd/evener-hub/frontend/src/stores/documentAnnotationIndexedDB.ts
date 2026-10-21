// Browser document-annotation adapter.
//
// Implemented here: durable ticket issuance and supersession, the accepted-read
// path, save, the same-session ordinary/recovery append path through the
// EXISTING BrowserDraftRepository.appendAnnotation seam, the changed-read durable
// expiry fence (split into its own transaction after validation), immediate
// in-memory suppression of a changed current read, retained-expiry retry,
// durable listing and expiry deletion.
//
// Durable listing, namespace reset, expiry deletion and keyed subscriptions
// complete this adapter's surface: committed saves, applied handoffs, fences,
// expiry deletions and namespace resets wake matching readers locally and,
// best-effort, across adapters on the same database. The persisted-handoff/
// runtime retry coordinator and editor/UI integration live outside this module.
import {
  type AcceptedReadResult,
  type AcceptedSourceLease,
  type AnnotationRetryReason,
  type AnnotationView,
  compareDisplayedSources,
  type DocFileContent,
  type DocumentAnnotationRepository,
  type DocumentKey,
  displayedFingerprint,
  type ExpiryReceipt,
  type HandoffResult,
  type PreparedAnnotation,
  type ReadTicket,
  type SaveReceipt,
} from "@evener/appwire-client";
import type { ComposerPreparation, ComposerSourceState } from "../panes/session/composer/composerContract";
import type {
  AppendAnnotationResult,
  BrowserDraftKey,
  BrowserDraftRepository,
  CommittedAppendBacking,
} from "./browserDraftRepository";
import { type NamespaceRow, serializeSessionKey } from "./compositionRevision";
import { IDBConnection, requestResult, transactionCompletion, tryAbortTransaction } from "./idbConnection";
import {
  ANNOTATION_EVIDENCE_DOCUMENT_EPOCH_INDEX,
  ANNOTATION_EVIDENCE_STORE,
  ANNOTATION_EXPIRY_STORE,
  ANNOTATION_HEADS_STORE,
  ANNOTATION_NAMESPACES_STORE,
  ANNOTATION_OPERATIONS_STORE,
  ANNOTATION_PAYLOADS_DOCUMENT_EPOCH_INDEX,
  ANNOTATION_PAYLOADS_STORE,
  ANNOTATION_SEQUENCES_STORE,
  type AnnotationEvidenceRow,
  type AnnotationExpiryRow,
  type AnnotationHeadRow,
  type AnnotationOperationRow,
  type AnnotationPayloadRow,
  type AnnotationSequenceRow,
  annotationDocumentKey,
  annotationExpiryKey,
  annotationNamespaceKey,
  annotationOperationKey,
  LOCAL_COMPOSITION_DATABASE_NAME,
  LOCAL_COMPOSITION_DATABASE_VERSION,
  registerLocalCompositionSchema,
} from "./localCompositionSchema";
import { createSecureUUID } from "./secureUUID";
import { createVersionedChannel, makeSourceId, type VersionedChannelMessage } from "./versionedChannel";

export { annotationDocumentKey, annotationExpiryKey, annotationOperationKey } from "./localCompositionSchema";

// The two read methods the accepted-read path needs, kept as a Pick so callers
// that only read do not depend on the rest of the repository surface.
export type AnnotationReadRepository = Pick<DocumentAnnotationRepository, "beginRead" | "acceptRead">;

export type AnnotationReadBoundary =
  | { phase: "begin-read-issued"; key: DocumentKey }
  | { phase: "ticket-remembered"; ticket: ReadTicket };

export interface DocumentAnnotationOptions {
  indexedDB?: IDBFactory;
  databaseName?: string;
  // Key-range constructor for the compound document+epoch index queries.
  // Browsers always provide IDBKeyRange; jsdom does not, so tests wiring the
  // production ports without the annotation fixture leave it undefined and the
  // adapter's queries fall back to a full scan.
  keyRange?: typeof IDBKeyRange;
  drafts: BrowserDraftRepository;
  targetFor(destination: PreparedAnnotation["destination"], key: DocumentKey): ComposerSourceState | null;
  beforeCommit?: (operation: "acceptRead" | "save" | "expiry" | "deleteExpiry" | "reset") => void;
  beforeFinalGuard?: () => void;
  beforeAbortableCompletion?: () => void;
  afterCommit?: () => void;
  onAppendReadsIssued?: () => void;
  onReadBoundary?: (event: AnnotationReadBoundary) => void;
  beforeExpiryFence?: (ticket: ReadTicket) => Promise<void>;
  onStorageSuccess?: () => void;
  // Observational, outside every transaction: a failed expiry fence retained
  // retryable repository work. The retry runtime registers the need so a later
  // storage-success notification can start the pass that completes the fence.
  onPendingExpiryRetained?: () => void;
}

// Operation-scoped cancellation for ONE in-flight save. The runtime owns the
// handle and its lifetime; the repository only observes it while its save
// transaction is alive, and MUST release its listener when the transaction
// settles. Kept local to this adapter: no shared package contract or schema
// change.
export interface AnnotationSaveCancellation {
  readonly cancelled: boolean;
  // Register a listener invoked once if the save is cancelled before it commits.
  // If cancellation already happened, the listener runs immediately. The
  // returned release removes the listener; call it at settlement.
  onCancel(listener: () => void): () => void;
}

// Retry-pass-scoped cancellation, owned by the runtime that started the pass.
// The persisted sweep hands off directly to the repository, so disposal during
// a pass must reach it here: a handoff it has not started yet is left pending
// for whichever runtime retries after teardown, never run against a retained
// composer.
export interface AnnotationRetryCancellation {
  readonly cancelled: boolean;
}

export interface BrowserDocumentAnnotationRepository extends AnnotationReadRepository {
  saveAnnotation(operation: PreparedAnnotation, cancellation?: AnnotationSaveCancellation): Promise<SaveReceipt>;
  listAnnotations(key: DocumentKey): Promise<AnnotationView[]>;
  finishExpiry(receipt: ExpiryReceipt): Promise<void>;
  // Cancel exactly ONE durable pending operation outside any acceptance: the
  // completion a proven source change needs when a faulted beginRead left no
  // ticket to fence with. The row's operation identity is the caller's own
  // retained attempt.
  discardPendingOperation(key: DocumentKey, operationId: string): Promise<void>;
  // Suppress one epoch's listing and pending handoffs from a source change the
  // reader PROVED outside any acceptance: the fault-branch completion a SAVED
  // annotation needs, whose durable row must survive for the expiry machinery
  // to retire while its handoff must not append behind the changed source.
  suppressEpochForExpiry(lease: AcceptedSourceLease): void;
  retryPending(reason: AnnotationRetryReason, cancellation?: AnnotationRetryCancellation): Promise<void>;
  retryPendingWithOutcome(
    reason: AnnotationRetryReason,
    cancellation?: AnnotationRetryCancellation,
  ): Promise<BrowserAnnotationRetryOutcome>;
  resetOwnedNamespace(
    namespace: BrowserDraftKey["namespace"],
    expectedIncarnation: string,
  ): Promise<{ committed: boolean; incarnation: string }>;
  // A runtime that drives this handoff owns its own disposal: pass the same
  // retry-scoped cancellation the persisted sweep uses so a handoff already
  // awaiting a preparation or its append reads refuses at the append's final
  // guard instead of appending after application teardown.
  handoff(key: DocumentKey, operationId: string, cancellation?: AnnotationRetryCancellation): Promise<HandoffResult>;
  subscribe(key: DocumentKey, listener: () => void): () => void;
  close(): void;
}

export interface BrowserAnnotationRetryOutcome {
  pending: boolean;
}

// A keyed annotation subscription, registered by the exact full document identity
// (namespace kind/origin, session, path) and the namespace it belongs to.
interface AnnotationSubscription {
  documentKey: string;
  namespaceKey: string;
  listener: () => void;
}

// Best-effort peer wake envelope: a committed invalidation identity only, never
// annotation content.
type AnnotationWakeMessage =
  | (VersionedChannelMessage & { documentKey: string })
  | (VersionedChannelMessage & { namespaceKey: string });

interface AnnotationWakeChannel {
  connect(): void;
  close(): void;
  postMessage(payload: { documentKey: string } | { namespaceKey: string }): void;
}

// Isolate one observer's fault from every other observer and from the committed
// result that triggered the wakeup.
function invokeObserver(listener: () => void): void {
  try {
    listener();
  } catch {
    // Observer faults cannot relabel a committed result.
  }
}

// One deletion pattern for every store swept by a namespace reset: a row
// leaves when its namespace identity matches, deleted by the store's own key,
// so a store added to the reset cannot grow a divergent loop.
async function deleteRowsWhere<Row>(
  transaction: IDBTransaction,
  storeName: string,
  rowBelongs: (row: Row) => boolean,
  keyOf: (row: Row) => IDBValidKey,
): Promise<void> {
  const rows = await requestResult<Row[]>(transaction.objectStore(storeName).getAll());
  for (const row of rows) {
    if (rowBelongs(row)) {
      await requestResult(transaction.objectStore(storeName).delete(keyOf(row)));
    }
  }
}

const STORAGE_WAIT_MS = 10_000;
const READ_STORES = [ANNOTATION_NAMESPACES_STORE, ANNOTATION_HEADS_STORE] as const;
const ACCEPT_STORES = [
  ANNOTATION_NAMESPACES_STORE,
  ANNOTATION_HEADS_STORE,
  ANNOTATION_EVIDENCE_STORE,
  ANNOTATION_OPERATIONS_STORE,
  ANNOTATION_EXPIRY_STORE,
] as const;
const SAVE_STORES = [
  ANNOTATION_NAMESPACES_STORE,
  ANNOTATION_HEADS_STORE,
  ANNOTATION_EVIDENCE_STORE,
  ANNOTATION_PAYLOADS_STORE,
  ANNOTATION_OPERATIONS_STORE,
  ANNOTATION_SEQUENCES_STORE,
] as const;

function evidenceMatches(content: DocFileContent, evidence: PreparedAnnotation["capture"]["evidence"]): boolean {
  if (
    evidence.wholeRevision !== undefined &&
    content.revision !== undefined &&
    evidence.wholeRevision !== content.revision
  ) {
    return false;
  }
  return JSON.stringify(evidence.fingerprint) === JSON.stringify(displayedFingerprint(content.text, content.truncated));
}

// Exact identity between two accepted observations. Unlike evidenceMatches, which
// is deliberately tolerant of missing metadata for compatibility, every
// distinguishing field must match here: the displayed text, the revision value
// (including both-absent) and the truncation coverage. Absence or any inconclusive
// comparison is NOT identity.
function contentIdentical(left: DocFileContent, right: DocFileContent): boolean {
  return left.text === right.text && left.revision === right.revision && left.truncated === right.truncated;
}

// The two-element namespace key carried inside a serialized document or session
// key. Reset scopes by this exact identity, never by an origin substring, so one
// namespace (or a sibling whose origin shares a prefix) is never caught.
function documentKeyNamespace(documentKey: string): string | undefined {
  try {
    const parsed: unknown = JSON.parse(documentKey);
    if (Array.isArray(parsed) && typeof parsed[0] === "string" && typeof parsed[1] === "string") {
      return JSON.stringify([parsed[0], parsed[1]]);
    }
  } catch {
    // A non-JSON key cannot carry a namespace.
  }
  return undefined;
}

// Raised when the durable identity a fence intended to replace has already moved,
// so an obsolete validation must not rewrite newer state.
class SupersededFenceError extends Error {}

// Raised when an operation-scoped cancellation is already known before its save
// transaction opens. The runtime owns the cancellation handle and treats this as
// a handled, non-retryable dismissal.
class SaveCancelledError extends Error {}

// close() is terminal. A transaction or scan that sees closure refuses before
// it opens a connection, so no later asynchronous boundary can revive the
// adapter. Callers classify it like any other storage refusal: the retry
// runtime keeps its retry-needed state rather than treating work as done.
class RepositoryClosedError extends Error {
  constructor() {
    super("Document annotation repository is closed");
  }
}

interface SuppressionEntry {
  // The ticket whose read owns a provisional entry, or null when no ticket
  // owns it (a suppression installed from a proven change outside any
  // acceptance): ticket-owned entries are confirmed or released by that
  // ticket's validation, and a null-owner entry is never released by a ticket.
  requestId: string | null;
  confirmed: boolean;
}

interface ReadState {
  ticket: ReadTicket;
  accepted?: { lease: AcceptedSourceLease; content: DocFileContent };
  // Every comparable observation accepted for the current accepted epoch is kept
  // so an entry-time comparison sees richer prior evidence, not only the latest.
  observations: DocFileContent[];
}

// A confirmed suppressed epoch whose durable fence did not commit. Retained in
// this adapter instance only (the spec's before-persistence limit) until a retry
// completes the real fence.
interface PendingExpiry {
  namespace: Extract<DocumentKey["namespace"], { kind: "browser" }>;
  documentKey: string;
  namespaceIncarnation: string;
  expiredEpoch: string;
  content: DocFileContent;
  ticketKey: DocumentKey;
}

// One append attempt whose completion the caller never observed, with the
// composer seam captured before the append ran: the key and operation
// identity replay the handoff, and the source and preparation reconcile
// the live composer once the receipt is known applied.
interface UncertainAppendRecord {
  key: DocumentKey;
  operationId: string;
  source: ComposerSourceState;
  preparation: ComposerPreparation;
}

function epochIdentity(lease: AcceptedSourceLease): string {
  return epochIdentityOf(annotationDocumentKey(lease.key), lease.namespaceIncarnation, lease.epoch);
}

function epochIdentityOf(documentKey: string, namespaceIncarnation: string, epoch: string): string {
  return JSON.stringify([documentKey, namespaceIncarnation, epoch]);
}

// True when the current content is a definite change against ANY retained
// comparable observation. A change is definite only relative to something that
// was actually observed, never to a synthesized text/revision pair; missing or
// inconclusive evidence alone is not a change. Shared by the local entry guard
// and the durable validation so both keep one comparison policy.
function contentChangedAgainst(observations: DocFileContent[], content: DocFileContent): boolean {
  return observations.some(
    (observation) =>
      compareDisplayedSources(
        {
          source: observation.text,
          evidence: {
            wholeRevision: observation.revision,
            fingerprint: displayedFingerprint(observation.text, observation.truncated),
          },
        },
        {
          source: content.text,
          evidence: {
            wholeRevision: content.revision,
            fingerprint: displayedFingerprint(content.text, content.truncated),
          },
        },
      ).changed,
  );
}

// A destination no composition owner can ever claim. Only a browser-origin
// destination names a recipient - its pane and composition owner - that
// targetFor can resolve, and a closed pane can reopen with its retained
// source. The ownerless kinds (a read-only view, an originless restored
// opening, a native recipient) carry no owner identity at all, so a handoff
// captured for one of them can never resolve; its handoff settles terminal
// instead of answering the retryable pending outcome forever.
function destinationNeverResolves(destination: PreparedAnnotation["destination"]): boolean {
  return destination.kind !== "browser-origin";
}

// Read exactly one document+epoch's rows. With a key-range constructor (every
// browser) the compound index bounds the scan; without one (jsdom with no fake
// class installed) the read falls back to the pre-index full scan with the
// same filter, so an environment without IDBKeyRange keeps working instead of
// crashing.
async function evidenceRowsForDocumentEpoch(
  evidenceStore: IDBObjectStore,
  keyRange: typeof IDBKeyRange | undefined,
  documentKey: string,
  epoch: string,
): Promise<AnnotationEvidenceRow[]> {
  if (keyRange) {
    return requestResult<AnnotationEvidenceRow[]>(
      evidenceStore.index(ANNOTATION_EVIDENCE_DOCUMENT_EPOCH_INDEX).getAll(keyRange.only([documentKey, epoch])),
    );
  }
  return (await requestResult<AnnotationEvidenceRow[]>(evidenceStore.getAll())).filter(
    (row) => row.documentKey === documentKey && row.epoch === epoch,
  );
}

async function payloadRowsForDocumentEpoch(
  payloadStore: IDBObjectStore,
  keyRange: typeof IDBKeyRange | undefined,
  documentKey: string,
  epoch: string,
): Promise<AnnotationPayloadRow[]> {
  if (keyRange) {
    return requestResult<AnnotationPayloadRow[]>(
      payloadStore.index(ANNOTATION_PAYLOADS_DOCUMENT_EPOCH_INDEX).getAll(keyRange.only([documentKey, epoch])),
    );
  }
  return (await requestResult<AnnotationPayloadRow[]>(payloadStore.getAll())).filter(
    (row) => row.documentKey === documentKey && row.epoch === epoch,
  );
}

class BrowserDocumentAnnotationRepositoryImpl implements BrowserDocumentAnnotationRepository {
  readonly #connection: IDBConnection;
  readonly #options: DocumentAnnotationOptions;
  // Adapter-local binding from an issued ticket to the durable read generation
  // it was issued at. Acceptance still requires the durable head to match, so a
  // later beginRead in ANOTHER adapter retires this ticket.
  readonly #issuedReads = new Map<string, { documentKey: string; readGeneration: number }>();
  // The last completed current ticket and accepted source per document key, used
  // for immediate in-memory suppression (never durable, never in ComposerSourceState).
  readonly #readStates = new Map<string, ReadState>();
  // Suppression ownership per original accepted epoch; a confirmed entry is
  // irreversible for that old lease.
  readonly #suppressedEpochs = new Map<string, SuppressionEntry>();
  // Retained pending expiry fences awaiting real completion.
  readonly #pendingExpiries = new Map<string, PendingExpiry>();
  // One append attempt per operation whose outcome the caller could not
  // observe: the transaction may have committed (receipt applied, reference
  // durable) while the live composer still holds its pre-append text. The
  // retained source and preparation let the next pass that finds the receipt
  // applied reconcile that composer from the committed backing. The record
  // lives only until the operation's next observed outcome: an applied
  // receipt reconciles or drops it, any other receipt state proves the
  // attempt rolled back, and the applied receipt itself prevents a duplicate
  // append.
  readonly #uncertainAppends = new Map<string, UncertainAppendRecord>();
  // Keyed registrations, woken only after a committed change to their exact
  // document or namespace.
  readonly #subscriptions = new Set<AnnotationSubscription>();
  // One recovery-commit notification armed per resolved composer source. A
  // handoff refused while the composer was typing waits on that edit's
  // persistence; recovery edits commit through the mutation outbox, which the
  // drafts-repository subscription cannot observe, so the source's own
  // recovery-persisted notification is routed through the same storage-success
  // boundary. The WeakMap dies with the source and dedupes per source without
  // retaining one; the release set below holds only the unsubscribe closures so
  // close() can reach them, since a WeakMap is not iterable.
  readonly #recoveryNotifications = new WeakMap<ComposerSourceState, () => void>();
  readonly #recoveryNotificationReleases = new Set<() => void>();
  // close() is terminal. A handoff awaiting a receipt, payload or preparation
  // read resumes outside the close call, so every asynchronous boundary checks
  // this before it reopens the connection, arms a listener, or appends through
  // the retained composer.
  #closed = false;
  // Best-effort peer wakeups, scoped to this adapter's database.
  readonly #channel: AnnotationWakeChannel;
  readonly #channelSourceId = makeSourceId();
  readonly #keyRange: typeof IDBKeyRange | undefined;
  readonly #unsubscribeDrafts: () => void;

  constructor(options: DocumentAnnotationOptions) {
    const indexedDB = options.indexedDB ?? globalThis.indexedDB;
    if (!indexedDB) throw new Error("IndexedDB is unavailable");
    this.#options = options;
    this.#keyRange = options.keyRange ?? globalThis.IDBKeyRange;
    this.#channel = createVersionedChannel<AnnotationWakeMessage>({
      name: `evener-annotation:${options.databaseName ?? LOCAL_COMPOSITION_DATABASE_NAME}`,
      getSourceId: () => this.#channelSourceId,
      // A peer message must carry a non-empty invalidation identity; it wakes
      // matching readers only and is never treated as annotation content.
      isMessage: (value): value is AnnotationWakeMessage => {
        const candidate = value as unknown as { documentKey?: unknown; namespaceKey?: unknown };
        return (
          (typeof candidate.documentKey === "string" && candidate.documentKey !== "") ||
          (typeof candidate.namespaceKey === "string" && candidate.namespaceKey !== "")
        );
      },
      onMessage: (message) => this.#onPeerWake(message),
    });
    this.#channel.connect();
    this.#connection = new IDBConnection({
      indexedDB,
      databaseName: options.databaseName ?? LOCAL_COMPOSITION_DATABASE_NAME,
      databaseVersion: LOCAL_COMPOSITION_DATABASE_VERSION,
      waitMs: STORAGE_WAIT_MS,
      upgrade: registerLocalCompositionSchema,
      errors: {
        open: () => new Error("Unable to open document annotation repository"),
        superseded: () => new Error("Document annotation repository connection was closed"),
        timeout: () => new Error("Document annotation repository open timed out"),
        blocked: () => new Error("Document annotation repository upgrade is blocked"),
      },
      reportDiagnostic: () => undefined,
    });
    // A refused handoff waits on the composer's own persistence: the draft or
    // recovery write that dirtied the preparation must, once committed, wake
    // the pending-handoff retry. Every draft and recovery commit lands through
    // this same drafts repository, so its commit notification - not only this
    // adapter's own transactions - is storage progress the runtime retries
    // against, coalesced by the runtime's single-pass design.
    this.#unsubscribeDrafts = options.drafts.subscribe(() => this.#notifyStorageSuccess());
  }

  close(): void {
    // Late work must not revive a closed registration; retire every listener and
    // the peer channel before the connection. A surviving composer source must
    // not keep this repository reachable through its recovery notification, so
    // every armed subscription is released too. Retained uncertain appends hold
    // composer seams that only a later handoff could reconcile - a closed
    // repository runs none, so the records retire with it.
    this.#closed = true;
    for (const release of this.#recoveryNotificationReleases) release();
    this.#recoveryNotificationReleases.clear();
    this.#subscriptions.clear();
    this.#uncertainAppends.clear();
    this.#unsubscribeDrafts();
    this.#channel.close();
    this.#connection.close();
  }

  // Register a listener for the exact full document identity. The returned
  // unsubscribe retires only this registration.
  subscribe(key: DocumentKey, listener: () => void): () => void {
    const subscription: AnnotationSubscription = {
      documentKey: annotationDocumentKey(key),
      namespaceKey: annotationNamespaceKey(key.namespace),
      listener,
    };
    this.#subscriptions.add(subscription);
    return () => {
      this.#subscriptions.delete(subscription);
    };
  }

  // Wake matching local registrations, each isolated from the others' faults.
  #notifyDocument(documentKey: string): void {
    for (const subscription of [...this.#subscriptions]) {
      if (subscription.documentKey === documentKey) invokeObserver(subscription.listener);
    }
  }

  #notifyNamespace(namespaceKey: string): void {
    for (const subscription of [...this.#subscriptions]) {
      if (subscription.namespaceKey === namespaceKey) invokeObserver(subscription.listener);
    }
  }

  // Local wakeup plus best-effort peer wakeup, called only after a real commit.
  #publishDocumentWake(documentKey: string): void {
    if (this.#closed) return;
    this.#notifyDocument(documentKey);
    this.#channel.postMessage({ documentKey });
  }

  #publishNamespaceWake(namespaceKey: string): void {
    if (this.#closed) return;
    this.#notifyNamespace(namespaceKey);
    this.#channel.postMessage({ namespaceKey });
  }

  #onPeerWake(message: AnnotationWakeMessage): void {
    if ("documentKey" in message) this.#notifyDocument(message.documentKey);
    else this.#notifyNamespace(message.namespaceKey);
  }

  // Observational storage-success boundary: called only AFTER an authority
  // transaction completes, outside every transaction, and after every draft or
  // recovery commit the composer persists through the shared drafts
  // repository. An observer fault cannot change the committed result.
  #notifyStorageSuccess(): void {
    if (this.#closed) return;
    invokeObserver(() => this.#options.onStorageSuccess?.());
  }

  // Arm the source's recovery-persisted notification once per source: every
  // handoff (direct or replayed) passes here, so a recovery-typing refusal
  // leaves the notification armed before the runtime ever needs it.
  #armRecoveryCommitNotification(source: ComposerSourceState): void {
    if (this.#recoveryNotifications.has(source)) return;
    const unsubscribe = source.subscribeRecoveryPersisted(() => this.#notifyStorageSuccess());
    this.#recoveryNotifications.set(source, unsubscribe);
    this.#recoveryNotificationReleases.add(unsubscribe);
  }

  // Test-only inspection of the adapter-local issued-ticket map: acceptance
  // validates against the durable head, so a retained superseded ticket and
  // a pruned one are observably identical to every production caller. The
  // bound tests read this to prove the map cannot accumulate one ticket per
  // abandoned read for the adapter's whole lifetime.
  issuedReadTicketsForTests(): ReadonlyMap<string, { documentKey: string; readGeneration: number }> {
    return this.#issuedReads;
  }

  async beginRead(key: DocumentKey): Promise<ReadTicket> {
    if (key.namespace.kind !== "browser") throw new Error("browser annotations require a browser namespace");
    const namespace = key.namespace;
    const documentKey = annotationDocumentKey(key);
    let issuedGeneration = 0;
    const ticket = await this.#transaction(READ_STORES, "readwrite", async (transaction) => {
      const namespaceRequest = requestResult<NamespaceRow | undefined>(
        transaction.objectStore(ANNOTATION_NAMESPACES_STORE).get(annotationNamespaceKey(namespace)),
      );
      const headRequest = requestResult<AnnotationHeadRow | undefined>(
        transaction.objectStore(ANNOTATION_HEADS_STORE).get(documentKey),
      );
      this.#options.onReadBoundary?.({ phase: "begin-read-issued", key });
      const [namespaceRow, head] = await Promise.all([namespaceRequest, headRequest]);
      const incarnation = namespaceRow?.incarnation ?? (await this.#createNamespace(transaction, namespace));
      // Durably record issuance: a later beginRead in ANY adapter advances
      // readGeneration, so this ticket validates stale once it is superseded.
      issuedGeneration = (head?.readGeneration ?? 0) + 1;
      await requestResult(
        transaction.objectStore(ANNOTATION_HEADS_STORE).put({
          documentKey,
          namespaceIncarnation: incarnation,
          epoch: head?.epoch ?? createSecureUUID(),
          headRevision: head?.headRevision ?? 0,
          readGeneration: issuedGeneration,
          displayedSnapshotId: head?.displayedSnapshotId ?? createSecureUUID(),
          evidenceKey: head?.evidenceKey ?? createSecureUUID(),
        } satisfies AnnotationHeadRow),
      );
      return {
        key,
        namespaceIncarnation: incarnation,
        headRevision: head?.headRevision ?? 0,
        requestId: createSecureUUID(),
      } satisfies ReadTicket;
    });
    // Issuing a newer read supersedes every earlier unaccepted ticket for this
    // document in this adapter: the issuance just advanced the durable read
    // generation, so those tickets can only ever validate stale now. Dropping
    // them here bounds the map to one live ticket per document instead of
    // accumulating one per failed, cancelled or abandoned read for the whole
    // application lifetime; tickets for other documents keep their own slots.
    for (const [requestId, issued] of this.#issuedReads) {
      if (issued.documentKey === documentKey) this.#issuedReads.delete(requestId);
    }
    this.#issuedReads.set(ticket.requestId, { documentKey, readGeneration: issuedGeneration });
    // Remember the completed current ticket in the same synchronous continuation,
    // carrying the prior completed accepted source for the full document key.
    const prior = this.#readStates.get(documentKey);
    this.#readStates.set(documentKey, { ticket, accepted: prior?.accepted, observations: prior?.observations ?? [] });
    this.#options.onReadBoundary?.({ phase: "ticket-remembered", ticket });
    this.#notifyStorageSuccess();
    return ticket;
  }

  async acceptRead(ticket: ReadTicket, content: DocFileContent): Promise<AcceptedReadResult> {
    if (ticket.key.namespace.kind !== "browser") return { state: "stale-ticket" };
    const namespace = ticket.key.namespace;
    const documentKey = annotationDocumentKey(ticket.key);
    const issued = this.#issuedReads.get(ticket.requestId);
    if (!issued || issued.documentKey !== documentKey) return { state: "stale-ticket" };
    // Entry, before the first await: a locally current read of changed content
    // installs or transfers unconfirmed suppression for the original accepted
    // epoch, so an overlapping append sees it at its own final guard. A confirmed
    // entry never transfers.
    const remembered = this.#readStates.get(documentKey);
    const previous = remembered?.accepted;
    let provisional: string | undefined;
    if (remembered?.ticket.requestId === ticket.requestId && previous) {
      const changed = contentChangedAgainst(remembered.observations, content);
      if (changed) {
        const identity = epochIdentity(previous.lease);
        const entry = this.#suppressedEpochs.get(identity);
        if (!entry?.confirmed) {
          this.#suppressedEpochs.set(identity, { requestId: ticket.requestId, confirmed: false });
          provisional = identity;
        }
      }
    }
    // Confirm or remove ONLY this invocation's still-owned provisional entry, from
    // the real durable validation result, never a caller-supplied boolean.
    // Returns true when it actually released an unconfirmed entry: that release
    // is an observation boundary the retry runtime must hear about.
    const finishValidation = (current: boolean): boolean => {
      if (!provisional) return false;
      const entry = this.#suppressedEpochs.get(provisional);
      if (entry?.requestId !== ticket.requestId) return false;
      if (current) {
        entry.confirmed = true;
        return false;
      }
      if (entry.confirmed) return false;
      this.#suppressedEpochs.delete(provisional);
      return true;
    };
    // A stale or unchanged validation releases the provisional restriction a
    // concurrent handoff may have refused against. That refusal only records
    // the runtime's retry need; the release is the sole event proving the
    // restriction lifted, so signal the storage-success boundary the runtime
    // already subscribes to. Without it the annotation stays pending until
    // unrelated activity starts a pass. Called after the validation transaction
    // settled, never inside it, so the pass the runtime may start is serviceable.
    const releaseProvisional = (): void => {
      if (finishValidation(false)) this.#notifyStorageSuccess();
    };
    try {
      // Validation commits before the fence: the durable identity of this ticket
      // is settled here, and the fence below is a separate transaction so a fence
      // abort cannot roll back validation or reject the read.
      const validation = await this.#transaction(ACCEPT_STORES, "readwrite", async (transaction) => {
        const namespaceRow = await requestResult<NamespaceRow | undefined>(
          transaction.objectStore(ANNOTATION_NAMESPACES_STORE).get(annotationNamespaceKey(namespace)),
        );
        if (!namespaceRow || namespaceRow.incarnation !== ticket.namespaceIncarnation)
          return { kind: "stale" } as const;
        const head = await requestResult<AnnotationHeadRow | undefined>(
          transaction.objectStore(ANNOTATION_HEADS_STORE).get(documentKey),
        );
        // A later beginRead advanced the durable read generation, so this ticket
        // was superseded before its acceptance could validate.
        if ((head?.readGeneration ?? 0) !== issued.readGeneration) return { kind: "stale" } as const;
        if (
          (head?.headRevision ?? 0) !== ticket.headRevision ||
          (head !== undefined && head.namespaceIncarnation !== namespaceRow.incarnation)
        ) {
          return { kind: "stale" } as const;
        }
        // Compare against EVERY retained comparable observation of the current
        // epoch, not just the first. An accepted prefix or revision-less read is
        // legitimately incomplete, so a change is definite only when it is
        // definite against some observation that was actually accepted; a later
        // richer observation must therefore be consulted too. Missing or
        // inconclusive evidence alone never expires notes.
        const currentEpoch = head?.epoch;
        const retained = currentEpoch
          ? (
              await evidenceRowsForDocumentEpoch(
                transaction.objectStore(ANNOTATION_EVIDENCE_STORE),
                this.#keyRange,
                documentKey,
                currentEpoch,
              )
            ).map((row) => row.content)
          : [];
        const contentChanged = contentChangedAgainst(retained, content);
        // A locally confirmed suppression keeps the current durable epoch dead even
        // when content reverts, so an unchanged reversion still fences instead of
        // resurrecting the old note.
        const epochSuppressed =
          head !== undefined &&
          head.namespaceIncarnation === namespaceRow.incarnation &&
          this.#suppressedEpochs.get(epochIdentityOf(documentKey, namespaceRow.incarnation, head.epoch))?.confirmed ===
            true;
        if (head && (contentChanged || epochSuppressed)) {
          return { kind: "fence", head, namespaceIncarnation: namespaceRow.incarnation } as const;
        }
        // An exactly identical re-acceptance must not manufacture a new lease
        // identity: when the incoming content is byte-identical to the latest
        // durable accepted observation the current head points at, preserve the
        // accepted head revision and displayed snapshot identity so an immutable
        // operation captured from the previous lease still matches the durable
        // head. Identity is exact and against that ONE observation only: a first
        // read with no accepted evidence row, missing/inconclusive metadata, or an
        // identical older observation elsewhere in retained history is not identity.
        const latest = head
          ? await requestResult<AnnotationEvidenceRow | undefined>(
              transaction.objectStore(ANNOTATION_EVIDENCE_STORE).get(head.evidenceKey),
            )
          : undefined;
        if (head && latest && contentIdentical(latest.content, content)) {
          // readGeneration still advances so a later issuance supersedes this
          // ticket and the accepted ticket stays single-use.
          const preservedHead: AnnotationHeadRow = {
            documentKey,
            namespaceIncarnation: namespaceRow.incarnation,
            epoch: head.epoch,
            headRevision: head.headRevision,
            readGeneration: head.readGeneration + 1,
            displayedSnapshotId: head.displayedSnapshotId,
            evidenceKey: head.evidenceKey,
          };
          await requestResult(transaction.objectStore(ANNOTATION_HEADS_STORE).put(preservedHead));
          this.#options.beforeCommit?.("acceptRead");
          const observations: DocFileContent[] = [...retained, content];
          return {
            kind: "accepted",
            lease: {
              key: ticket.key,
              namespaceIncarnation: namespaceRow.incarnation,
              epoch: head.epoch,
              headRevision: preservedHead.headRevision,
              displayedSnapshotId: preservedHead.displayedSnapshotId,
            } satisfies AcceptedSourceLease,
            observations,
          } as const;
        }
        const epoch = head?.epoch ?? createSecureUUID();
        const evidenceKey = createSecureUUID();
        const nextHead: AnnotationHeadRow = {
          documentKey,
          namespaceIncarnation: namespaceRow.incarnation,
          epoch,
          headRevision: (head?.headRevision ?? 0) + 1,
          readGeneration: (head?.readGeneration ?? 0) + 1,
          displayedSnapshotId: createSecureUUID(),
          evidenceKey,
        };
        await requestResult(
          transaction
            .objectStore(ANNOTATION_EVIDENCE_STORE)
            .put({ evidenceKey, documentKey, epoch, content } satisfies AnnotationEvidenceRow),
        );
        await requestResult(transaction.objectStore(ANNOTATION_HEADS_STORE).put(nextHead));
        this.#options.beforeCommit?.("acceptRead");
        const observations: DocFileContent[] = [...retained, content];
        return {
          kind: "accepted",
          lease: {
            key: ticket.key,
            namespaceIncarnation: namespaceRow.incarnation,
            epoch,
            headRevision: nextHead.headRevision,
            displayedSnapshotId: nextHead.displayedSnapshotId,
          } satisfies AcceptedSourceLease,
          // The already-validated comparable observations of this epoch plus the
          // content just accepted. The continuation publishes them into local
          // memory, so a recreated adapter that validated against durable history
          // carries that history into its entry guard, not only the latest read.
          observations,
        } as const;
      });
      if (validation.kind === "stale") {
        releaseProvisional();
        return { state: "stale-ticket" };
      }
      if (validation.kind === "accepted") {
        // An unchanged acceptance is not a changed read: drop any provisional the
        // local comparison installed and record the completed acceptance.
        releaseProvisional();
        this.#readStates.set(documentKey, {
          ticket,
          accepted: { lease: validation.lease, content },
          observations: validation.observations,
        });
        return { state: "accepted", lease: validation.lease };
      }
      // A current changed read: confirm suppression BEFORE scheduling its fence,
      // then run the outside-transaction observation boundary. A recreated
      // adapter validated this change against durable history alone — it holds
      // no in-memory acceptance, so no provisional exists to confirm. The
      // expired epoch must be suppressed from the validated durable head
      // itself, or a failed fence leaves the dead epoch visible to listing
      // and handoff-eligible despite the confirmed source change.
      this.#suppressedEpochs.set(epochIdentityOf(documentKey, validation.namespaceIncarnation, validation.head.epoch), {
        requestId: ticket.requestId,
        confirmed: true,
      });
      finishValidation(true);
      await this.#options.beforeExpiryFence?.(ticket);
      try {
        const fenced = await this.#fence(ticket.key, validation.namespaceIncarnation, validation.head.epoch, content);
        // The replacement epoch starts with its own evidence, never inheriting the
        // dead epoch's observations.
        this.#readStates.set(documentKey, {
          ticket,
          accepted: { lease: fenced.lease, content },
          observations: [content],
        });
        return { state: "expiry-pending", receipt: fenced.receipt };
      } catch (error) {
        // Closure refused the fence before it opened a connection. This adapter
        // can never retry, so the read is stale with no retained work and no
        // post-teardown notification.
        if (this.#closed) return { state: "stale-ticket" };
        if (error instanceof SupersededFenceError) return { state: "stale-ticket" };
        // A failed fence keeps the confirmed suppression and its pending expiry for
        // retry; the read is honest expiry-pending with no committed receipt.
        this.#pendingExpiries.set(
          epochIdentityOf(documentKey, validation.namespaceIncarnation, validation.head.epoch),
          {
            namespace,
            documentKey,
            namespaceIncarnation: validation.namespaceIncarnation,
            expiredEpoch: validation.head.epoch,
            content,
            ticketKey: ticket.key,
          },
        );
        // Observational, after the retention is recorded: the runtime's retry
        // health learns about this work here, because no failed save or pending
        // handoff will ever mark it and storage-success recovery alone must be
        // able to complete the fence. An observer fault cannot change the
        // retained work or the honest expiry-pending outcome.
        invokeObserver(() => this.#options.onPendingExpiryRetained?.());
        return { state: "expiry-pending" };
      }
    } catch (error) {
      // A fault after this invocation installed its provisional suppression — a
      // storage fault inside validation, or a fault in the outside-transaction
      // observation boundary — must release that still-owned UNCONFIRMED entry
      // on the way out: the finally below only retires the ticket, and a leaked
      // unconfirmed entry would hide an unchanged epoch's annotations and
      // cancel their handoffs forever. Confirmed suppression (an expiry this or
      // an earlier read proved) is never released here. That release RESTORES
      // the epoch's eligibility - a handoff that refused pending against this
      // suppression can append again - and the release is the only observation
      // of the restored state, so it signals the storage-success boundary the
      // runtime retries on. This catch runs after the awaited validation
      // transaction settled (with its fault), never inside it. Without the
      // signal the runtime keeps its recorded need with nothing to spend it and
      // the handoff waits for unrelated activity.
      releaseProvisional();
      throw error;
    } finally {
      this.#issuedReads.delete(ticket.requestId);
    }
  }

  // The durable fence is its own transaction so a failed fence cannot roll back
  // validation and so the observation boundary can run between them. It rechecks
  // the durable identity it is about to replace: an obsolete validation must not
  // rewrite a newer epoch.
  async #fence(
    ticketKey: DocumentKey,
    namespaceIncarnation: string,
    expectedExpiredEpoch: string,
    content: DocFileContent,
  ): Promise<{ receipt: ExpiryReceipt; lease: AcceptedSourceLease }> {
    const documentKey = annotationDocumentKey(ticketKey);
    const fenced = await this.#transaction(ACCEPT_STORES, "readwrite", async (transaction) => {
      const namespaceRow = await requestResult<NamespaceRow | undefined>(
        transaction.objectStore(ANNOTATION_NAMESPACES_STORE).get(annotationNamespaceKey(ticketKey.namespace)),
      );
      if (!namespaceRow || namespaceRow.incarnation !== namespaceIncarnation) throw new SupersededFenceError();
      const currentHead = await requestResult<AnnotationHeadRow | undefined>(
        transaction.objectStore(ANNOTATION_HEADS_STORE).get(documentKey),
      );
      if (
        !currentHead ||
        currentHead.epoch !== expectedExpiredEpoch ||
        currentHead.namespaceIncarnation !== namespaceRow.incarnation
      ) {
        throw new SupersededFenceError();
      }
      // Confirmed source change: one transaction commits the replacement head,
      // cancels EVERY pending operation of the expired epoch, and writes the
      // genuine fenced ExpiryReceipt before any deletion.
      const expiredEpoch = currentHead.epoch;
      const replacementEpoch = createSecureUUID();
      const fencedEvidenceKey = createSecureUUID();
      const displayedSnapshotId = createSecureUUID();
      await requestResult(
        transaction
          .objectStore(ANNOTATION_EVIDENCE_STORE)
          .put({ evidenceKey: fencedEvidenceKey, documentKey, epoch: replacementEpoch, content }),
      );
      await requestResult(
        transaction.objectStore(ANNOTATION_HEADS_STORE).put({
          documentKey,
          namespaceIncarnation: namespaceRow.incarnation,
          epoch: replacementEpoch,
          headRevision: currentHead.headRevision + 1,
          readGeneration: currentHead.readGeneration + 1,
          displayedSnapshotId,
          evidenceKey: fencedEvidenceKey,
        } satisfies AnnotationHeadRow),
      );
      const fencedOperations = await requestResult<AnnotationOperationRow[]>(
        transaction.objectStore(ANNOTATION_OPERATIONS_STORE).getAll(),
      );
      for (const operation of fencedOperations) {
        if (
          operation.documentKey !== documentKey ||
          operation.epoch !== expiredEpoch ||
          operation.state !== "pending"
        ) {
          continue;
        }
        const { destination: _destination, payloadKey: _payloadKey, ...terminal } = operation;
        await requestResult(
          transaction.objectStore(ANNOTATION_OPERATIONS_STORE).put({ ...terminal, state: "cancelled" }),
        );
      }
      const expiryReceipt: ExpiryReceipt = {
        key: ticketKey,
        namespaceIncarnation: namespaceRow.incarnation,
        expiredEpoch,
        replacementEpoch,
        phase: "fenced",
      };
      await requestResult(
        transaction.objectStore(ANNOTATION_EXPIRY_STORE).put({
          expiryKey: annotationExpiryKey(documentKey, expiredEpoch),
          receipt: expiryReceipt,
        } satisfies AnnotationExpiryRow),
      );
      this.#options.beforeCommit?.("expiry");
      return {
        receipt: expiryReceipt,
        lease: {
          key: ticketKey,
          namespaceIncarnation: namespaceRow.incarnation,
          epoch: replacementEpoch,
          headRevision: currentHead.headRevision + 1,
          displayedSnapshotId,
        } satisfies AcceptedSourceLease,
      };
    });
    // Both the changed-read fence and the retained-expiry retry land here after
    // the real transaction commits.
    this.#publishDocumentWake(documentKey);
    return fenced;
  }

  async saveAnnotation(operation: PreparedAnnotation, cancellation?: AnnotationSaveCancellation): Promise<SaveReceipt> {
    // Capture the caller's attempt synchronously, before any await, so later
    // caller mutations cannot change what is validated or persisted.
    const captured = structuredClone(operation);
    const namespace = captured.lease.key.namespace;
    if (namespace.kind !== "browser") throw new Error("browser annotations require a browser namespace");
    const operationKey = annotationOperationKey(namespace, captured.operationId);
    const documentKey = annotationDocumentKey(captured.lease.key);
    const sessionKey = serializeSessionKey({ namespace, sessionRef: captured.lease.key.sessionRef });
    let newlySaved = false;
    let replayedSaved = false;
    const receipt = await this.#transaction(
      SAVE_STORES,
      "readwrite",
      async (transaction) => {
        const operations = transaction.objectStore(ANNOTATION_OPERATIONS_STORE);
        const existing = await requestResult<AnnotationOperationRow | undefined>(operations.get(operationKey));
        if (existing) {
          // Every receipt state compares against the payload that still exists;
          // an expired/reset payload is absent, so a terminal replay is returned
          // unchanged rather than recreated.
          // payloadKey equals the serialized operationKey, so an applied receipt
          // (which retains no payloadKey) still finds its unexpired payload.
          const priorPayload = await requestResult<AnnotationPayloadRow | undefined>(
            transaction.objectStore(ANNOTATION_PAYLOADS_STORE).get(operationKey),
          );
          if (priorPayload && JSON.stringify(priorPayload.operation) !== JSON.stringify(captured)) {
            throw new Error("duplicate operation id with a different payload");
          }
          // A durable row answers the replay even when its first attempt never
          // observed the commit: a watchdog rejected after the transaction
          // committed, so the wake below its rejection skipped was never
          // published. The row is the only cross-adapter evidence of that
          // commit, so the replay publishes the invalidation the lost attempt
          // could not. A genuinely duplicate save publishes one redundant wake;
          // readers re-list durable state, which costs a read and changes
          // nothing else.
          replayedSaved = true;
          return {
            operationId: captured.operationId,
            epoch: existing.epoch,
            sessionSequence: existing.sessionSequence,
          } satisfies SaveReceipt;
        }
        const namespaceRow = await requestResult<NamespaceRow | undefined>(
          transaction.objectStore(ANNOTATION_NAMESPACES_STORE).get(annotationNamespaceKey(namespace)),
        );
        if (!namespaceRow || namespaceRow.incarnation !== captured.lease.namespaceIncarnation) {
          throw new Error("accepted lease incarnation is not current");
        }
        const head = await requestResult<AnnotationHeadRow | undefined>(
          transaction.objectStore(ANNOTATION_HEADS_STORE).get(documentKey),
        );
        // The retained attempt validates against the current, unexpired
        // epoch and its durable evidence, never against the exact head
        // revision: a metadata-less reread advances the head revision within
        // the epoch without establishing any source change, and holding a
        // failed first save to the old revision would strand the comment
        // permanently. A genuine source change fences into a new epoch, which
        // the epoch equality here already rejects.
        if (!head || head.namespaceIncarnation !== namespaceRow.incarnation || head.epoch !== captured.lease.epoch) {
          throw new Error("accepted lease does not match the durable head");
        }
        // The capture validates against ANY observation the current, unexpired
        // epoch retains — its own accepted observation included, not just the
        // head's latest. A compatible reread with different prefix coverage
        // becomes the head's latest observation without establishing a change,
        // and holding the retry to that latest row alone would strand the
        // comment despite its original evidence still being retained. Within an
        // epoch every retained observation is validated-comparable; a genuine
        // change fences into a new epoch, which the epoch check above rejects.
        const epochEvidence = await evidenceRowsForDocumentEpoch(
          transaction.objectStore(ANNOTATION_EVIDENCE_STORE),
          this.#keyRange,
          documentKey,
          head.epoch,
        );
        if (!epochEvidence.some((row) => evidenceMatches(row.content, captured.capture.evidence))) {
          throw new Error("accepted source evidence does not match the durable evidence");
        }
        const sequence = await requestResult<AnnotationSequenceRow | undefined>(
          transaction.objectStore(ANNOTATION_SEQUENCES_STORE).get(sessionKey),
        );
        const sessionSequence = (sequence?.lastSequence ?? 0) + 1;
        // The durable head and its evidence still validate an epoch whose
        // expiry a changed read already confirmed but whose fence transaction
        // failed: the in-memory suppression is the only record the epoch is
        // dead. A save committed here would return a receipt the caller
        // treats as success - clearing its note - while the pending expiry
        // deletes the payload with the epoch. Reject before any write; the
        // note survives for reselection.
        if (this.#suppressedEpochs.get(epochIdentity(captured.lease))?.confirmed === true) {
          throw new Error("accepted lease epoch is confirmed expired");
        }
        await requestResult(
          transaction.objectStore(ANNOTATION_PAYLOADS_STORE).put({
            payloadKey: operationKey,
            documentKey,
            epoch: head.epoch,
            operation: captured,
          } satisfies AnnotationPayloadRow),
        );
        await requestResult(
          operations.put({
            operationKey,
            namespaceKey: annotationNamespaceKey(namespace),
            namespaceIncarnation: namespaceRow.incarnation,
            documentKey,
            sessionKey,
            sessionSequence,
            operationId: captured.operationId,
            epoch: head.epoch,
            state: "pending",
            destination: captured.destination,
            payloadKey: operationKey,
          } satisfies AnnotationOperationRow),
        );
        await requestResult(
          transaction
            .objectStore(ANNOTATION_SEQUENCES_STORE)
            .put({ documentKey: sessionKey, lastSequence: sessionSequence } satisfies AnnotationSequenceRow),
        );
        this.#options.beforeCommit?.("save");
        newlySaved = true;
        return { operationId: captured.operationId, epoch: head.epoch, sessionSequence } satisfies SaveReceipt;
      },
      cancellation,
    );
    this.#notifyStorageSuccess();
    // A newly persisted operation and a replay that confirms an existing one
    // both name a committed state readers may not have observed.
    if (newlySaved || replayedSaved) this.#publishDocumentWake(documentKey);
    return receipt;
  }

  // Listing is durable-read only: no composer lookup, transport or mutation. It
  // returns unexpired pending AND applied operations whose retained payload still
  // exists (payloadKey equals the operation key), excluding cancelled rows and
  // any epoch carrying a durable expiry row.
  async listAnnotations(key: DocumentKey): Promise<AnnotationView[]> {
    if (key.namespace.kind !== "browser") return [];
    const documentKey = annotationDocumentKey(key);
    return this.#transaction(
      [ANNOTATION_OPERATIONS_STORE, ANNOTATION_PAYLOADS_STORE, ANNOTATION_EXPIRY_STORE],
      "readonly",
      async (transaction) => {
        const [operations, payloads, expiries] = await Promise.all([
          requestResult<AnnotationOperationRow[]>(transaction.objectStore(ANNOTATION_OPERATIONS_STORE).getAll()),
          requestResult<AnnotationPayloadRow[]>(transaction.objectStore(ANNOTATION_PAYLOADS_STORE).getAll()),
          requestResult<AnnotationExpiryRow[]>(transaction.objectStore(ANNOTATION_EXPIRY_STORE).getAll()),
        ]);
        const expiredKeys = new Set(expiries.map((row) => row.expiryKey));
        const payloadByKey = new Map(payloads.map((row) => [row.payloadKey, row]));
        const views: AnnotationView[] = [];
        for (const row of operations) {
          if (row.documentKey !== documentKey || row.state === "cancelled") continue;
          if (expiredKeys.has(annotationExpiryKey(documentKey, row.epoch))) continue;
          if (this.#suppressedEpochs.has(epochIdentityOf(row.documentKey, row.namespaceIncarnation, row.epoch))) {
            continue;
          }
          const payload = payloadByKey.get(row.operationKey);
          if (!payload) continue;
          const receipt: SaveReceipt = {
            operationId: row.operationId,
            epoch: row.epoch,
            sessionSequence: row.sessionSequence,
          };
          const handoff: HandoffResult =
            row.state === "applied"
              ? { state: "already-applied", draftRevision: row.draftRevision }
              : { state: "pending", reason: "owner-unavailable" };
          views.push({ operation: payload.operation, receipt, handoff });
        }
        views.sort((left, right) => left.receipt.sessionSequence - right.receipt.sessionSequence);
        return views;
      },
    );
  }

  // Deletion is guarded by the current namespace incarnation and the durable
  // expiry row for (documentKey, expiredEpoch). It removes only that epoch's
  // payload/evidence, flips the receipt to "deleted", and retains terminal
  // operation receipts. A replayed receipt is a no-op, so it cannot remove newer
  // work; an injected deleteExpiry fault rejects the whole transaction.
  async finishExpiry(receipt: ExpiryReceipt): Promise<void> {
    if (receipt.key.namespace.kind !== "browser") return;
    const namespace = receipt.key.namespace;
    const documentKey = annotationDocumentKey(receipt.key);
    const expiryKey = annotationExpiryKey(documentKey, receipt.expiredEpoch);
    let removed = false;
    await this.#transaction(
      [
        ANNOTATION_NAMESPACES_STORE,
        ANNOTATION_HEADS_STORE,
        ANNOTATION_EVIDENCE_STORE,
        ANNOTATION_PAYLOADS_STORE,
        ANNOTATION_OPERATIONS_STORE,
        ANNOTATION_EXPIRY_STORE,
      ],
      "readwrite",
      async (transaction) => {
        const namespaceRow = await requestResult<NamespaceRow | undefined>(
          transaction.objectStore(ANNOTATION_NAMESPACES_STORE).get(annotationNamespaceKey(namespace)),
        );
        if (!namespaceRow || namespaceRow.incarnation !== receipt.namespaceIncarnation) return;
        const expiryRow = await requestResult<AnnotationExpiryRow | undefined>(
          transaction.objectStore(ANNOTATION_EXPIRY_STORE).get(expiryKey),
        );
        if (!expiryRow || expiryRow.receipt.expiredEpoch !== receipt.expiredEpoch) return;
        if (expiryRow.receipt.phase === "deleted") return;
        const [payloads, evidence] = await Promise.all([
          payloadRowsForDocumentEpoch(
            transaction.objectStore(ANNOTATION_PAYLOADS_STORE),
            this.#keyRange,
            documentKey,
            receipt.expiredEpoch,
          ),
          evidenceRowsForDocumentEpoch(
            transaction.objectStore(ANNOTATION_EVIDENCE_STORE),
            this.#keyRange,
            documentKey,
            receipt.expiredEpoch,
          ),
        ]);
        for (const payload of payloads) {
          await requestResult(transaction.objectStore(ANNOTATION_PAYLOADS_STORE).delete(payload.payloadKey));
        }
        for (const row of evidence) {
          await requestResult(transaction.objectStore(ANNOTATION_EVIDENCE_STORE).delete(row.evidenceKey));
        }
        await requestResult(
          transaction.objectStore(ANNOTATION_EXPIRY_STORE).put({
            expiryKey,
            receipt: { ...expiryRow.receipt, phase: "deleted" },
          } satisfies AnnotationExpiryRow),
        );
        this.#options.beforeCommit?.("deleteExpiry");
        removed = true;
      },
    );
    if (removed) this.#publishDocumentWake(documentKey);
  }

  // Cancel exactly ONE durable pending operation outside any acceptance. The
  // beginRead-fault reader calls this with its retained attempt's identity once
  // the raw fetch proves a definite source change: without it, the runtime's
  // next retry pass re-applies the obsolete operation with its old identity (the
  // fence that would have cancelled the epoch's pending rows requires a ticket
  // the fault never issued). Only a row still pending for the exact
  // namespace-qualified operation identity is flipped to the fence's terminal
  // cancelled receipt; an absent row (the save never committed), a terminal
  // receipt (applied, cancelled, unclaimed) and every other operation stay
  // untouched, so a replay is an idempotent no-op. No wake is published: the
  // discard's only producer is the reader's own fault branch, which publishes
  // its own state in the same flow, and the acceptance that follows on healed
  // storage fences the epoch and wakes peers - the same shape settleUnclaimed
  // keeps.
  async discardPendingOperation(key: DocumentKey, operationId: string): Promise<void> {
    if (key.namespace.kind !== "browser") return;
    const operationKey = annotationOperationKey(key.namespace, operationId);
    await this.#transaction([ANNOTATION_OPERATIONS_STORE], "readwrite", async (transaction) => {
      const row = await requestResult<AnnotationOperationRow | undefined>(
        transaction.objectStore(ANNOTATION_OPERATIONS_STORE).get(operationKey),
      );
      if (row?.state !== "pending") return;
      const { destination: _destination, payloadKey: _payloadKey, ...terminal } = row;
      await requestResult(
        transaction.objectStore(ANNOTATION_OPERATIONS_STORE).put({ ...terminal, state: "cancelled" }),
      );
    });
  }

  // The suppression half of the same fault-branch completion: a source change
  // the READER proved against retained evidence (a faulted beginRead's raw
  // fetch, an acceptance that threw before its own changed-read suppression
  // could run) must still cancel the affected epoch's pending handoffs and
  // hide it from listing. The entry is confirmed - the change was proven, not
  // a local guess - and owns no ticket, so no validation can release it. It
  // lives like the changed-read suppression: until the expiry's fence and
  // deletion complete, and inert once the epoch's rows are gone.
  suppressEpochForExpiry(lease: AcceptedSourceLease): void {
    this.#suppressedEpochs.set(epochIdentity(lease), { requestId: null, confirmed: true });
  }

  // Complete retained pending expiry fences first, then visit persisted pending
  // handoffs once in session save order. A document-specific fence failure
  // (say, insufficient quota for a large replacement snapshot) must not hold
  // every other document's pending annotations hostage: each failed fence is
  // retained and the pass continues, and the first retained fault is re-thrown
  // AFTER the pass so the runtime keeps exactly the retry-needed signal a
  // throwing pass always carried. Other sessions and namespaces never block one
  // session's FIFO; terminal receipts and duplicate handling stay in the real
  // handoff path, so an applied operation is never recaptured here.
  async retryPending(reason: AnnotationRetryReason, cancellation?: AnnotationRetryCancellation): Promise<void> {
    void reason;
    const retainedFaults: unknown[] = [];
    for (const [identity, record] of [...this.#pendingExpiries]) {
      try {
        await this.#fence(record.ticketKey, record.namespaceIncarnation, record.expiredEpoch, record.content);
        this.#pendingExpiries.delete(identity);
      } catch (error) {
        if (error instanceof SupersededFenceError) {
          // Another fence already replaced this epoch: the retained work is done.
          this.#pendingExpiries.delete(identity);
        } else {
          retainedFaults.push(error);
        }
      }
    }
    retainedFaults.push(...(await this.#sweepUnfinishedExpiries()));
    await this.#retryPersistedPending(cancellation);
    if (retainedFaults.length > 0) throw retainedFaults[0];
  }

  // Complete persisted unfinished expiry receipts: a fence's deletion can
  // outlive the reader that fenced it (the pane closed before its reread
  // adopted the replacement epoch, or the deletion transaction itself
  // faulted), so every retry pass sweeps the receipt rows that are not yet
  // terminal. finishExpiry is idempotent and incarnation-guarded, so a
  // receipt from before a namespace reset no-ops here. A faulting deletion is
  // the same isolation case as a failed retained fence: the non-terminal
  // receipt is swept again on the next trigger while the pass continues to the
  // persisted handoffs.
  async #sweepUnfinishedExpiries(): Promise<unknown[]> {
    const rows = await this.#transaction([ANNOTATION_EXPIRY_STORE], "readonly", async (transaction) =>
      requestResult<AnnotationExpiryRow[]>(transaction.objectStore(ANNOTATION_EXPIRY_STORE).getAll()),
    );
    const faults: unknown[] = [];
    for (const row of rows) {
      if (row.receipt.phase === "deleted") continue;
      try {
        await this.finishExpiry(row.receipt);
      } catch (error) {
        faults.push(error);
      }
    }
    return faults;
  }

  // Read the persisted pending operations once, then hand each off using its
  // ORIGINAL full DocumentKey and operation ID retained in its payload. Sorted by
  // session then session sequence, so a session's earlier operation is retried
  // before its later one while a session whose owner is unavailable stays pending
  // without holding up another session.
  //
  // Retained uncertain appends are reconciled FIRST, before the pending scan
  // below is even read. An append that committed without an observed completion
  // advances the composer's durable token while the live composer still holds
  // the pre-append one, so a pending handoff swept first refuses
  // revision-conflict against that stale token. Reconciliation acknowledges the
  // committed backing, and that acknowledgment is exactly what makes the
  // composer's next preparation current. It answers an already-applied receipt,
  // so it publishes a reader wake but no storage-success signal the runtime
  // could retry on: a sweep that reconciles only after visiting the pending
  // handoffs leaves them stranded with nothing left to trigger a revisit. The
  // uncertain pass therefore runs first, and the pending sweep below skips the
  // operations it already visited - a retry that re-faulted re-retains its own
  // record under the same identity, which this pass must not append twice.
  async #retryPersistedPending(cancellation?: AnnotationRetryCancellation): Promise<void> {
    const handled = new Set<string>();
    // A committed append whose completion went unobserved answers applied on
    // replay, so a pending scan would skip it - recovery must not skip the
    // now-applied operation, and must reconcile it before a later handoff
    // prepares against the composer it is about to update. Its receipt either
    // proves the rollback (the record retires inside handoff) or answers
    // applied, where the retained composer seam reconciles with the committed
    // backing.
    for (const record of [...this.#uncertainAppends.values()]) {
      if (cancellation?.cancelled) return;
      handled.add(record.operationId);
      await this.handoff(record.key, record.operationId, cancellation);
    }
    const pending = await this.#transaction(
      [ANNOTATION_OPERATIONS_STORE, ANNOTATION_PAYLOADS_STORE],
      "readonly",
      async (transaction) => {
        const [operations, payloads] = await Promise.all([
          requestResult<AnnotationOperationRow[]>(transaction.objectStore(ANNOTATION_OPERATIONS_STORE).getAll()),
          requestResult<AnnotationPayloadRow[]>(transaction.objectStore(ANNOTATION_PAYLOADS_STORE).getAll()),
        ]);
        const payloadByKey = new Map(payloads.map((row) => [row.payloadKey, row]));
        return operations
          .flatMap((row) => {
            if (row.state !== "pending") return [];
            const payload = payloadByKey.get(row.payloadKey);
            if (!payload) return [];
            return [{ row, payload }];
          })
          .sort(
            (left, right) =>
              left.row.sessionKey.localeCompare(right.row.sessionKey) ||
              left.row.sessionSequence - right.row.sessionSequence,
          );
      },
    );
    for (const { payload } of pending) {
      // A sweep outlives the runtime that started it only until disposal: a
      // later handoff must not start against a retained composer. The
      // operations it never reached keep their pending receipts.
      if (cancellation?.cancelled) return;
      // Reconciliation above already handed this operation off in this pass; a
      // second attempt would only repeat the outcome against the composer it
      // just touched.
      if (handled.has(payload.operation.operationId)) continue;
      await this.handoff(payload.operation.lease.key, payload.operation.operationId, cancellation);
    }
  }

  // Browser-only outcome boundary: the underlying real pass runs first, then a
  // completed read-only snapshot of the persisted operation receipts reports
  // whether any handoff is still pending. The read-only observation never emits
  // onStorageSuccess; the underlying pass keeps its own producer boundaries.
  retryPendingWithOutcome(
    reason: AnnotationRetryReason,
    cancellation?: AnnotationRetryCancellation,
  ): Promise<BrowserAnnotationRetryOutcome> {
    return retryBrowserAnnotationsWithOutcome(
      this,
      this.#connection,
      reason,
      () => this.#uncertainAppends.size > 0,
      cancellation,
      () => this.#closed,
    );
  }

  // Reset rotates ONE browser namespace's annotation incarnation and removes only
  // that namespace's annotation rows in a single transaction. A stale expected
  // incarnation neither rotates nor deletes newer state. This adapter's captured
  // old accepted leases are suppressed before the first await so an in-flight
  // handoff cancels instead of appending; composition, recovery and other
  // namespaces are untouched. The new incarnation is generated from the existing
  // secure UUID helper before the transaction opens and written inside it;
  // retained pending expiry work is released only after the reset actually
  // commits, so an aborted reset keeps it recoverable.
  async resetOwnedNamespace(
    namespace: BrowserDraftKey["namespace"],
    expectedIncarnation: string,
  ): Promise<{ committed: boolean; incarnation: string }> {
    const namespaceKey = annotationNamespaceKey(namespace);
    // Reset's suppression is provisional until its transaction commits: the
    // entries still cancel in-flight handoffs and hide listings while the
    // reset runs, but an ABORTED reset must not leave confirmed suppression
    // behind - surviving entries would vanish live annotations from listings
    // and let an unchanged reread treat the epoch as expired, deleting
    // payloads the reset never removed. An independently confirmed expiry (a
    // changed read's proof) keeps its own entry untouched.
    const provisioned: string[] = [];
    for (const [documentKey, state] of this.#readStates) {
      if (documentKeyNamespace(documentKey) !== namespaceKey) continue;
      const accepted = state.accepted;
      if (accepted && accepted.lease.namespaceIncarnation === expectedIncarnation) {
        const identity = epochIdentity(accepted.lease);
        if (this.#suppressedEpochs.has(identity)) continue;
        this.#suppressedEpochs.set(identity, { requestId: state.ticket.requestId, confirmed: false });
        provisioned.push(identity);
      }
    }
    const confirmProvisioned = (): void => {
      for (const identity of provisioned) {
        const entry = this.#suppressedEpochs.get(identity);
        if (entry && !entry.confirmed) entry.confirmed = true;
      }
    };
    // An aborted or non-rotating reset releases what it provisioned. The release
    // RESTORES the old epoch's eligibility: a handoff that refused pending while
    // the reset ran can append again, and this release is the only observation
    // of that restored state. Both callers below run after the reset
    // transaction settled, so the storage-success signal they fire cannot start
    // a pass inside a transaction. Returns true when it actually released an
    // unconfirmed entry.
    const releaseProvisioned = (): boolean => {
      let released = false;
      for (const identity of provisioned) {
        const entry = this.#suppressedEpochs.get(identity);
        if (entry && !entry.confirmed) {
          this.#suppressedEpochs.delete(identity);
          released = true;
        }
      }
      return released;
    };
    const newIncarnation = createSecureUUID();
    let result: { committed: boolean; incarnation: string };
    try {
      result = await this.#transaction(
        [
          ANNOTATION_NAMESPACES_STORE,
          ANNOTATION_HEADS_STORE,
          ANNOTATION_EVIDENCE_STORE,
          ANNOTATION_PAYLOADS_STORE,
          ANNOTATION_OPERATIONS_STORE,
          ANNOTATION_SEQUENCES_STORE,
          ANNOTATION_EXPIRY_STORE,
        ],
        "readwrite",
        async (transaction) => {
          const namespaceRow = await requestResult<NamespaceRow | undefined>(
            transaction.objectStore(ANNOTATION_NAMESPACES_STORE).get(namespaceKey),
          );
          if (!namespaceRow || namespaceRow.incarnation !== expectedIncarnation) {
            return { committed: false, incarnation: namespaceRow?.incarnation ?? "" };
          }
          await deleteRowsWhere<AnnotationHeadRow>(
            transaction,
            ANNOTATION_HEADS_STORE,
            (row) => documentKeyNamespace(row.documentKey) === namespaceKey,
            (row) => row.documentKey,
          );
          await deleteRowsWhere<AnnotationEvidenceRow>(
            transaction,
            ANNOTATION_EVIDENCE_STORE,
            (row) => documentKeyNamespace(row.documentKey) === namespaceKey,
            (row) => row.evidenceKey,
          );
          await deleteRowsWhere<AnnotationPayloadRow>(
            transaction,
            ANNOTATION_PAYLOADS_STORE,
            (row) => documentKeyNamespace(row.documentKey) === namespaceKey,
            (row) => row.payloadKey,
          );
          await deleteRowsWhere<AnnotationOperationRow>(
            transaction,
            ANNOTATION_OPERATIONS_STORE,
            (row) => row.namespaceKey === namespaceKey,
            (row) => row.operationKey,
          );
          await deleteRowsWhere<AnnotationSequenceRow>(
            transaction,
            ANNOTATION_SEQUENCES_STORE,
            (row) => documentKeyNamespace(row.documentKey) === namespaceKey,
            (row) => row.documentKey,
          );
          await deleteRowsWhere<AnnotationExpiryRow>(
            transaction,
            ANNOTATION_EXPIRY_STORE,
            (row) => annotationNamespaceKey(row.receipt.key.namespace) === namespaceKey,
            (row) => row.expiryKey,
          );
          await requestResult(
            transaction
              .objectStore(ANNOTATION_NAMESPACES_STORE)
              .put({ namespaceKey, incarnation: newIncarnation } satisfies NamespaceRow),
          );
          this.#options.beforeCommit?.("reset");
          return { committed: true, incarnation: newIncarnation };
        },
      );
    } catch (error) {
      // The transaction settled by rolling back; the release is an observation
      // boundary outside it.
      if (releaseProvisioned()) this.#notifyStorageSuccess();
      throw error;
    }
    if (result.committed) {
      confirmProvisioned();
      // Reset wakes every registration in its exact namespace, and no other.
      this.#publishNamespaceWake(namespaceKey);
      for (const [identity, record] of [...this.#pendingExpiries]) {
        if (
          record.namespaceIncarnation === expectedIncarnation &&
          annotationNamespaceKey(record.namespace) === namespaceKey
        ) {
          this.#pendingExpiries.delete(identity);
        }
      }
    } else {
      // A stale expected incarnation rotated nothing: the transaction settled
      // without the reset these entries were provisioned for, so their release
      // restores eligibility exactly as a rollback does.
      if (releaseProvisioned()) this.#notifyStorageSuccess();
    }
    return result;
  }

  async handoff(
    key: DocumentKey,
    operationId: string,
    cancellation?: AnnotationRetryCancellation,
  ): Promise<HandoffResult> {
    // Closure is terminal for this adapter; a runtime's cancellation is
    // terminal for the work it started. Both mean the same thing to a handoff
    // at every asynchronous boundary below: do not reopen the connection, arm a
    // listener, or append through a retained composer.
    const refused = (): boolean => this.#closed || cancellation?.cancelled === true;
    if (refused()) return { state: "pending", reason: "owner-unavailable" };
    const namespace = key.namespace;
    if (namespace.kind !== "browser") return { state: "cancelled" };
    const operationKey = annotationOperationKey(namespace, operationId);
    // The whole fallible pre-commit path returns the documented fulfilled
    // pending/owner-unavailable result on a storage or preparation fault: an
    // unreadable receipt is not cancelled and must stay recoverable through the
    // runtime's storage-recovery retry. A known committed receipt remains
    // authoritative, and post-commit observer faults still cannot relabel it.
    let receipt: AnnotationOperationRow | undefined;
    try {
      receipt = await this.#readOperation(operationKey);
    } catch {
      // The receipt is unobservable: any retained uncertain record for this
      // operation survives for the next pass.
      return { state: "pending", reason: "owner-unavailable" };
    }
    if (refused()) return { state: "pending", reason: "owner-unavailable" };
    if (!receipt) {
      this.#uncertainAppends.delete(operationKey);
      return { state: "cancelled" };
    }
    if (receipt.state === "applied") {
      // A committed append whose completion an earlier attempt never observed
      // answers applied here with its composer unreconciled: the durable
      // draft carries the reference while the live editor still holds its
      // pre-append text. Retained reconciliation work - the source and
      // preparation captured when the append faulted - updates the unchanged
      // composer from the committed backing; the source's own ownership
      // guard preserves newer edits, and the applied receipt prevents any
      // duplicate append.
      const reconciliation = await this.#reconcileUncertainAppend(operationKey, receipt.draftRevision, cancellation);
      if (reconciliation !== "faulted") this.#uncertainAppends.delete(operationKey);
      if (refused()) return { state: "pending", reason: "owner-unavailable" };
      return { state: "already-applied", draftRevision: receipt.draftRevision };
    }
    // The receipt is observably NOT applied: a previously uncertain attempt
    // for this operation is proven rolled back, so its retained
    // reconciliation work retires here. The append below re-observes its own
    // outcome and re-retains only if this attempt in turn faults.
    this.#uncertainAppends.delete(operationKey);
    if (receipt.state === "cancelled") return { state: "cancelled" };
    // An already-settled unclaimed handoff reports the permanent
    // owner-unavailable outcome; the saved annotation stays listed with its
    // copyable reference.
    if (receipt.state === "unclaimed") return { state: "pending", reason: "owner-unavailable" };
    let payload: AnnotationPayloadRow | undefined;
    try {
      payload = await this.#readPayload(receipt.payloadKey);
    } catch {
      return { state: "pending", reason: "owner-unavailable" };
    }
    if (refused()) return { state: "pending", reason: "owner-unavailable" };
    if (!payload) return { state: "cancelled" };
    // The immutable ORIGINAL payload lease owns suppression eligibility for this
    // operation; any suppression of that old epoch (provisional or confirmed)
    // cancels it, and the capture is never refreshed.
    const originalLease = structuredClone(payload.operation.lease);
    const suppression = () => this.#suppressedEpochs.get(epochIdentity(originalLease));
    // An unconfirmed suppression is a LOCAL comparison awaiting durable
    // validation: it can still release as a stale read, which leaves this
    // operation's pending receipt eligible again. Only a confirmed suppression -
    // a source change the reader proved - is the terminal cancelled outcome.
    // Reporting the provisional state as cancelled dropped the runtime's retry
    // intent, so a later storage success could never complete the still-pending
    // handoff once the validation released the suppression.
    const suppressedOutcome = (): HandoffResult | null => {
      const entry = suppression();
      if (!entry) return null;
      return entry.confirmed ? { state: "cancelled" } : { state: "pending", reason: "owner-unavailable" };
    };
    let provisionalSuppressionAtGuard = false;
    const documentStillEligible = () => {
      if (refused()) return false;
      // The append's last guard asks this after its reads. Record a provisional
      // answer so the cancellation it causes stays retryable: the validation the
      // entry awaits may still prove its read stale and release the suppression.
      const entry = suppression();
      if (!entry) return true;
      if (!entry.confirmed) provisionalSuppressionAtGuard = true;
      return false;
    };
    const suppressed = suppressedOutcome();
    if (suppressed) return suppressed;
    // A permanently unresolvable recipient settles its receipt terminal here
    // instead of answering the retryable pending outcome forever: the saved
    // annotation and its copyable reference stay listed exactly as before,
    // while the settled receipt stops blocking later handoffs and stops
    // feeding the retry outcome. The pending answer remains this handoff's
    // honest permanent outcome - no owner is or ever will be available. A
    // settle that cannot commit leaves the receipt pending for the next pass.
    if (destinationNeverResolves(payload.operation.destination)) {
      try {
        await this.#settleUnclaimed(operationKey);
      } catch {
        // Durable settle work stays recoverable through the ordinary retry path.
      }
      return { state: "pending", reason: "owner-unavailable" };
    }
    const source = this.#options.targetFor(payload.operation.destination, key);
    if (!source) return { state: "pending", reason: "owner-unavailable" };
    if (refused()) return { state: "pending", reason: "owner-unavailable" };
    this.#armRecoveryCommitNotification(source);
    let preparation: ComposerPreparation | null;
    try {
      preparation = await source.prepareAnnotation();
    } catch {
      return { state: "pending", reason: "owner-unavailable" };
    }
    if (refused()) return { state: "pending", reason: "owner-unavailable" };
    if (!preparation) return { state: "pending", reason: "owner-unavailable" };
    const suppressedAfterPreparation = suppressedOutcome();
    if (suppressedAfterPreparation) return suppressedAfterPreparation;
    let result: AppendAnnotationResult;
    try {
      result = await this.#options.drafts.appendAnnotation({
        operationKey,
        document: key,
        preparation,
        sourceId: source.sourceId,
        stillOwnsEdit: () => source.ownsPreparation(preparation),
        documentStillEligible,
        epochConfirmedExpired: (candidate) =>
          this.#suppressedEpochs.get(
            epochIdentityOf(candidate.documentKey, candidate.namespaceIncarnation, candidate.epoch),
          )?.confirmed === true,
        onAppendReadsIssued: this.#options.onAppendReadsIssued,
        beforeFinalGuard: this.#options.beforeFinalGuard,
        beforeAbortableCompletion: this.#options.beforeAbortableCompletion,
      });
    } catch {
      // A thrown append is an outcome this call could not observe: the
      // transaction may have committed - an unobserved completion leaves the
      // receipt applied and the reference durable - or rolled back. Retain
      // this attempt's composer seam so the next pass that finds the receipt
      // applied reconciles the live composer from the committed backing
      // instead of leaving the editor on its pre-append text while the
      // durable draft moved on. The recoverable pending outcome and the
      // caller's own retry semantics are unchanged.
      if (refused()) return { state: "pending", reason: "owner-unavailable" };
      this.#uncertainAppends.set(operationKey, { key, operationId, source, preparation });
      return { state: "pending", reason: "owner-unavailable" };
    }
    if (refused()) return { state: "pending", reason: "owner-unavailable" };
    if (result.state === "applied") {
      invokeObserver(() => this.#options.afterCommit?.());
      invokeObserver(() => source.acknowledgeAnnotation(preparation, result.token, result.value));
      this.#notifyStorageSuccess();
      // The append committed; wake matching readers (and peers) after commit.
      this.#publishDocumentWake(annotationDocumentKey(key));
      return { state: "applied", draftRevision: result.draftRevision };
    }
    // The append's own last guard can be the first place an overlapping read's
    // provisional suppression is observed. The durable operation was not
    // cancelled, so answer the retryable pending outcome and let a later
    // storage success finish the handoff once the suppression resolves.
    if (result.state === "cancelled" && provisionalSuppressionAtGuard) {
      return { state: "pending", reason: "owner-unavailable" };
    }
    return result;
  }

  // Reconcile the live composer an uncertain append left behind: the receipt
  // answers applied, so the retained source and preparation update that
  // composer from the committed backing. The source's own ownership guard
  // decides what publishing means - an unchanged composer adopts the
  // committed value, a newer edit stays untouched, a disposed source answers
  // nothing - so the reconciliation can never stomp live work. The wake the
  // original commit never published settles readers now that the receipt is
  // confirmed applied, whatever the composer does.
  async #reconcileUncertainAppend(
    operationKey: string,
    draftRevision: number,
    cancellation?: AnnotationRetryCancellation,
  ): Promise<"reconciled" | "unreconcilable" | "faulted" | "none"> {
    const retained = this.#uncertainAppends.get(operationKey);
    if (!retained) return "none";
    let backing: CommittedAppendBacking | null;
    try {
      backing = await this.#options.drafts.readAppendBacking(retained.preparation);
    } catch {
      // A transient read fault keeps the record: the receipt is applied, so
      // only this reconciliation is still outstanding.
      return "faulted";
    }
    if (this.#closed || cancellation?.cancelled === true) return "none";
    this.#publishDocumentWake(annotationDocumentKey(retained.key));
    if (
      !backing ||
      backing.token.namespaceIncarnation !== retained.preparation.token.namespaceIncarnation ||
      backing.token.sessionRevision < draftRevision
    ) {
      // The committed lineage is gone or superseded: the annotation's
      // copyable listing survives, but no composer state can be reconciled.
      return "unreconcilable";
    }
    const { token, value } = backing;
    invokeObserver(() => retained.source.acknowledgeAnnotation(retained.preparation, token, value));
    return "reconciled";
  }

  async #readOperation(operationKey: string): Promise<AnnotationOperationRow | undefined> {
    return this.#transaction([ANNOTATION_OPERATIONS_STORE], "readonly", (transaction) =>
      requestResult<AnnotationOperationRow | undefined>(
        transaction.objectStore(ANNOTATION_OPERATIONS_STORE).get(operationKey),
      ),
    );
  }

  async #readPayload(payloadKey: string): Promise<AnnotationPayloadRow | undefined> {
    return this.#transaction([ANNOTATION_PAYLOADS_STORE], "readonly", (transaction) =>
      requestResult<AnnotationPayloadRow | undefined>(
        transaction.objectStore(ANNOTATION_PAYLOADS_STORE).get(payloadKey),
      ),
    );
  }

  // Flip one never-resolvable pending handoff to its terminal unclaimed
  // receipt. The payload row is untouched (the saved annotation and its
  // copyable reference stay listed); a receipt that already left pending wins
  // and this settle no-ops. No wake is published: the settled row lists exactly
  // as it did while pending, so no reader observation changes.
  async #settleUnclaimed(operationKey: string): Promise<void> {
    await this.#transaction([ANNOTATION_OPERATIONS_STORE], "readwrite", async (transaction) => {
      const row = await requestResult<AnnotationOperationRow | undefined>(
        transaction.objectStore(ANNOTATION_OPERATIONS_STORE).get(operationKey),
      );
      if (row?.state !== "pending") return;
      const { destination: _destination, payloadKey: _payloadKey, ...terminal } = row;
      await requestResult(
        transaction.objectStore(ANNOTATION_OPERATIONS_STORE).put({ ...terminal, state: "unclaimed" }),
      );
    });
  }

  async #createNamespace(
    transaction: IDBTransaction,
    namespace: Extract<DocumentKey["namespace"], { kind: "browser" }>,
  ): Promise<string> {
    const incarnation = createSecureUUID();
    await requestResult(
      transaction
        .objectStore(ANNOTATION_NAMESPACES_STORE)
        .put({ namespaceKey: annotationNamespaceKey(namespace), incarnation } satisfies NamespaceRow),
    );
    return incarnation;
  }

  async #transaction<T>(
    stores: readonly string[],
    mode: "readonly" | "readwrite",
    body: (transaction: IDBTransaction) => Promise<T>,
    cancellation?: AnnotationSaveCancellation,
  ): Promise<T> {
    // close() is terminal for every flow, not only the handoff path: a changed
    // read resuming between its validation and its fence, a sweep row, or a
    // settle must not reopen the connection and write after teardown.
    if (this.#closed) throw new RepositoryClosedError();
    if (cancellation?.cancelled) throw new SaveCancelledError();
    const database = await this.#connection.open();
    // close() can land while the open is outstanding. The connection would
    // otherwise install a fresh database after teardown; refuse and release it.
    if (this.#closed) {
      this.#connection.close();
      throw new RepositoryClosedError();
    }
    const transaction = database.transaction([...stores], mode);
    // Observe operation-scoped cancellation for the life of THIS transaction. The
    // listener aborts before commit, and registering it synchronously here also
    // covers a cancellation that lands between the open and the body. It is
    // released at settlement so no listener/handle outlives the save.
    const release = cancellation?.onCancel(() => {
      tryAbortTransaction(transaction);
    });
    const completed = transactionCompletion(transaction);
    void completed.catch(() => undefined);
    let timer: ReturnType<typeof setTimeout> | undefined;
    let timedOut = false;
    const deadline = new Promise<never>((_resolve, reject) => {
      timer = setTimeout(() => {
        // Best effort while the transaction can still abort: prove the
        // rollback. Retiring first would close the database, and a finished
        // transaction refuses abort().
        tryAbortTransaction(transaction);
        this.#connection.retire(database);
        // Annotation writes reconcile on retry through their operation rows
        // (the duplicate-operation identity check) and reads re-read durable
        // state, so an unobserved outcome is honestly reported as a fault for
        // the caller's own retry instead of leaving it pending forever - the
        // same settle-the-caller call the drafts repository and session cache
        // watchdogs make.
        timedOut = true;
        reject(new Error("Document annotation repository transaction timed out"));
      }, STORAGE_WAIT_MS);
    });
    try {
      // Promise.all rejects on either failure without waiting for the other
      // input. A terminal abort therefore releases even a stalled body, while
      // success requires both the result and the durable commit event.
      const [result] = await Promise.race([Promise.all([body(transaction), completed]), deadline]);
      return result;
    } catch (error) {
      if (!timedOut) {
        tryAbortTransaction(transaction);
        // The rollback wait shares the same deadline the happy path honors:
        // a stalled abort event must not keep the call pending past the
        // watchdog.
        await Promise.race([completed.catch(() => undefined), deadline]).catch(() => undefined);
      }
      throw error;
    } finally {
      clearTimeout(timer);
      release?.();
    }
  }
}

export function createDocumentAnnotationRepository(
  options: DocumentAnnotationOptions,
): BrowserDocumentAnnotationRepository {
  return new BrowserDocumentAnnotationRepositoryImpl(options);
}

// Read one repository adapter's live issued-ticket map. Production acceptance
// never distinguishes a pruned superseded ticket from a generation-missed one,
// so only the bound tests use this.
export function issuedReadTicketsForTests(
  repository: BrowserDocumentAnnotationRepository,
): ReadonlyMap<string, { documentKey: string; readGeneration: number }> {
  return (repository as BrowserDocumentAnnotationRepositoryImpl).issuedReadTicketsForTests();
}

// Wrap one real retry pass with a completed, read-only receipt scan. The pass
// runs to completion first; only IDB request/completion promises are awaited in
// the outcome transaction, and a fulfilled pass is never itself treated as
// recovery. Applied/cancelled receipts are not pending; open/read/completion
// failures reject and keep the caller's retry-needed state.
export async function retryBrowserAnnotationsWithOutcome(
  repository: BrowserDocumentAnnotationRepository,
  connection: IDBConnection,
  reason: AnnotationRetryReason,
  // Outstanding reconciliation work for appends whose completion went
  // unobserved: a receipt that answers applied never appears in the pending
  // scan below, but a faulted reconciliation read leaves the live composer
  // stale and must keep the runtime's shared retry intent alive.
  hasRetainedReconciliation: () => boolean = () => false,
  cancellation?: AnnotationRetryCancellation,
  isClosed?: () => boolean,
): Promise<BrowserAnnotationRetryOutcome> {
  // close() is terminal: a pass that was already in flight must not reopen the
  // adapter for the scan. Refuse like any other storage fault so the caller
  // keeps its retry-needed state instead of reading through teardown.
  if (isClosed?.() === true) throw new RepositoryClosedError();
  await repository.retryPending(reason, cancellation);
  if (isClosed?.() === true) throw new RepositoryClosedError();
  const database = await connection.open();
  if (isClosed?.() === true) {
    connection.close();
    throw new RepositoryClosedError();
  }
  const transaction = database.transaction(ANNOTATION_OPERATIONS_STORE, "readonly");
  const completed = transactionCompletion(transaction);
  void completed.catch(() => undefined);
  // This scan ends the runtime's shared retry pass, so a stalled request
  // would leave every later retry awaiting the same never-settling promise.
  // The same deadline the repository's transactions carry applies: abort
  // while the transaction can still abort, retire the connection, settle
  // the caller so its retry-needed state survives for the next pass.
  let timer: ReturnType<typeof setTimeout> | undefined;
  let timedOut = false;
  const deadline = new Promise<never>((_resolve, reject) => {
    timer = setTimeout(() => {
      tryAbortTransaction(transaction);
      connection.retire(database);
      timedOut = true;
      reject(new Error("Document annotation outcome scan timed out"));
    }, STORAGE_WAIT_MS);
  });
  try {
    const [operations] = await Promise.race([
      Promise.all([
        requestResult<AnnotationOperationRow[]>(transaction.objectStore(ANNOTATION_OPERATIONS_STORE).getAll()),
        completed,
      ]),
      deadline,
    ]);
    return {
      pending: operations.some((operation) => operation.state === "pending") || hasRetainedReconciliation(),
    };
  } catch (error) {
    if (!timedOut) {
      tryAbortTransaction(transaction);
      // The rollback wait shares the same deadline the happy path honors:
      // a stalled abort event must not keep the shared retry pass pending
      // past the watchdog.
      await Promise.race([completed.catch(() => undefined), deadline]).catch(() => undefined);
    }
    throw error;
  } finally {
    clearTimeout(timer);
  }
}
