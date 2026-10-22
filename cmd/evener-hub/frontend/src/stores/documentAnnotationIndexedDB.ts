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
import { type NamespaceRow, readNamespaceRow, serializeSessionKey } from "./compositionRevision";
import {
  IDBConnection,
  IndexedDBTransactionTimeoutError,
  requestResult,
  runBoundedTransaction,
  tryAbortTransaction,
} from "./idbConnection";
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
  terminalOperationRow,
} from "./localCompositionSchema";
import { createSecureUUID } from "./secureUUID";
import { createVersionedChannel, makeSourceId, type VersionedChannelMessage } from "./versionedChannel";

export { annotationDocumentKey, annotationExpiryKey, annotationOperationKey } from "./localCompositionSchema";

// The two read methods the accepted-read path needs, kept as a Pick so callers
// that only read do not depend on the rest of the repository surface.
export type AnnotationReadRepository = Pick<DocumentAnnotationRepository, "beginRead" | "acceptRead">;

export interface DocumentAnnotationOptions {
  indexedDB?: IDBFactory;
  databaseName?: string;
  drafts: BrowserDraftRepository;
  targetFor(destination: PreparedAnnotation["destination"], key: DocumentKey): ComposerSourceState | null;
  beforeCommit?: (operation: "acceptRead" | "save" | "expiry" | "deleteExpiry" | "reset" | "discard") => void;
  onAppendReadsIssued?: () => void;
  beforeExpiryFence?: (ticket: ReadTicket) => Promise<void>;
  onStorageSuccess?: () => void;
  // Observational, outside every transaction: a failed expiry fence retained
  // retryable repository work. The retry runtime registers the need so a later
  // storage-success notification can start the pass that completes the fence.
  onPendingExpiryRetained?: () => void;
  // Observational, outside every transaction: a cancelled save's durable
  // discard faulted. The identity stays fenced and the discard is retained
  // retryable repository work, so the retry runtime registers the need that a
  // later storage-success notification spends.
  onCancelledDiscardRetained?: () => void;
  // Observational, outside every transaction: an append's committed backing is
  // retained for a delivery that faulted. The receipt may already be APPLIED,
  // so no handoff answer shows the outstanding work; the retry runtime
  // registers the need its own settlement spends. Emission-free by contract:
  // the fault path fires no signal or wake, because a signal inside a pass
  // would latch and chain sibling passes for as long as the fault persists.
  onUncertainAppendRetained?: () => void;
}

// Operation-scoped cancellation for ONE in-flight save. The runtime owns the
// handle and its lifetime; the repository only observes it while its save
// transaction is alive, and MUST release its listener when the transaction
// settles. Kept local to this adapter: no shared package contract or schema
// change.
export interface AnnotationSaveCancellation {
  readonly cancelled: boolean;
  // Why the save was cancelled. A user DISMISSAL fences the identity and flips
  // its durable row terminal; application teardown only aborts a transaction
  // it can still abort and leaves a committed row pending for whichever
  // runtime retries after it. Null until cancellation lands.
  readonly reason: "dismissal" | "teardown" | null;
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
  // Dismiss ONE saved operation: fence its identity synchronously and flip its
  // pending row to the terminal cancelled receipt. Idempotent, and safe against
  // an in-flight save because IndexedDB serializes the write transactions: the
  // flip orders after the save's commit and sees the committed row, or before
  // it and no-ops (the save's settlement re-runs it). The terminal row is the
  // cross-instance fence.
  dismissOperation(key: DocumentKey, operationId: string): Promise<void>;
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

// An acknowledgment is a DELIVERY, not an observation: a fault leaves the
// composer's authority un-advanced while the append is already committed. It
// must be attempted, not swallowed, so the caller can retain the work a later
// pass re-drives. True means the composer received the committed backing.
function attemptDelivery(delivery: () => void): boolean {
  try {
    delivery();
    return true;
  } catch {
    return false;
  }
}

// One deletion pattern for every store swept by a namespace reset: a row
// leaves when its namespace identity matches, deleted by the store's own key,
// so a store added to the reset cannot grow a divergent loop. Every request is
// issued in row order; the one wait after the loop lets them run as a batch.
async function deleteRowsWhere<Row>(
  transaction: IDBTransaction,
  storeName: string,
  rowBelongs: (row: Row) => boolean,
  keyOf: (row: Row) => IDBValidKey,
): Promise<void> {
  const rows = await requestResult<Row[]>(transaction.objectStore(storeName).getAll());
  const deletes: IDBRequest<undefined>[] = [];
  for (const row of rows) {
    if (rowBelongs(row)) {
      deletes.push(transaction.objectStore(storeName).delete(keyOf(row)));
    }
  }
  await Promise.all(deletes.map((request) => requestResult(request)));
}

// Flip one pending operation row to its terminal receipt. The destination and
// payload reference describe work the row no longer owns, so both leave with
// the terminal state; the caller keeps its own row selection and state.
function putTerminalOperation(
  transaction: IDBTransaction,
  row: Extract<AnnotationOperationRow, { state: "pending" }>,
  state: "cancelled" | "unclaimed",
): IDBRequest<IDBValidKey> {
  return transaction.objectStore(ANNOTATION_OPERATIONS_STORE).put(terminalOperationRow(row, state));
}

const STORAGE_WAIT_MS = 10_000;
const READ_STORES = [ANNOTATION_NAMESPACES_STORE, ANNOTATION_HEADS_STORE] as const;
export const ACCEPT_STORES = [
  ANNOTATION_NAMESPACES_STORE,
  ANNOTATION_HEADS_STORE,
  ANNOTATION_EVIDENCE_STORE,
  ANNOTATION_OPERATIONS_STORE,
  ANNOTATION_EXPIRY_STORE,
] as const;
export const SAVE_STORES = [
  ANNOTATION_NAMESPACES_STORE,
  ANNOTATION_HEADS_STORE,
  ANNOTATION_EVIDENCE_STORE,
  ANNOTATION_PAYLOADS_STORE,
  ANNOTATION_OPERATIONS_STORE,
  ANNOTATION_SEQUENCES_STORE,
] as const;

// Reset sweeps every annotation store of one namespace: the save set plus the
// expiry receipts it retires with them.
export const RESET_STORES = [...SAVE_STORES, ANNOTATION_EXPIRY_STORE] as const;

function evidenceMatches(
  content: DocFileContent,
  evidence: PreparedAnnotation["capture"]["evidence"],
  // The capture is loop-invariant during a validation, so its serialized
  // fingerprint is computed once by the caller.
  serializedFingerprint: string,
): boolean {
  if (
    evidence.wholeRevision !== undefined &&
    content.revision !== undefined &&
    evidence.wholeRevision !== content.revision
  ) {
    return false;
  }
  return serializedFingerprint === JSON.stringify(displayedFingerprint(content.text, content.truncated));
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
  // The document and incarnation this entry names, carried as fields so a
  // retirement compares structured state instead of re-decoding the identity.
  documentKey: string;
  namespaceIncarnation: string;
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

interface IssuedRead {
  requestId: string;
  readGeneration: number;
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
  // The confirmed entry this retention installed, so the superseded arm can
  // release exactly that object and nothing a peer installed since.
  suppression: SuppressionEntry;
}

// A dismissed save whose durable flip has not committed. The operation identity
// is fenced from handoff (the row may still be pending) and the discard is
// retryable work this adapter instance retains until storage accepts it.
// Per-instance state that mirrors, but never replaces, the terminal row.
interface PendingCancelledDiscard {
  key: DocumentKey;
  operationId: string;
  // The dismissal-time flip issued for this record, once one has been issued.
  // The save's post-settlement second arm awaits it: a flip that already
  // classified the row is not repeated inline, while a flip that ran before the
  // save committed (row absent) is repeated now that the row exists.
  dismissal?: Promise<DiscardOutcome>;
  // The applied observation's one document wake. The fence stays up and every
  // pass re-observes the applied row, but this adapter's listing changed only
  // when the fence first hid it, so the wake is published once.
  wakePublished?: boolean;
}

// What a durable discard found, so the in-memory fence is dropped only when the
// row is genuinely terminal or gone. "applied" is committed protection work the
// no-op discard did not remove: the fence stays up.
type DiscardOutcome = "flipped" | "terminal" | "absent" | "applied" | "faulted";

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

export function epochIdentityOf(documentKey: string, namespaceIncarnation: string, epoch: string): string {
  return JSON.stringify([documentKey, namespaceIncarnation, epoch]);
}

// True when the current content is a definite change against ANY retained
// comparable observation. A change is definite only relative to something that
// was actually observed, never to a synthesized text/revision pair; missing or
// inconclusive evidence alone is not a change. Shared by the local entry guard
// and the durable validation so both keep one comparison policy.
function contentChangedAgainst(observations: DocFileContent[], content: DocFileContent): boolean {
  const incomingEvidence = {
    wholeRevision: content.revision,
    fingerprint: displayedFingerprint(content.text, content.truncated),
  };
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
          evidence: incomingEvidence,
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

// Read exactly one document+epoch's rows from the index that carries both.
// With a key-range constructor (every browser) the compound index bounds the
// scan; without one (jsdom with no fake class installed) the read falls back
// to the pre-index full scan with the same filter, so an environment without
// IDBKeyRange keeps working instead of crashing.
async function rowsForDocumentEpoch<Row extends { documentKey: string; epoch: string }>(
  store: IDBObjectStore,
  indexName: string,
  keyRange: typeof IDBKeyRange | undefined,
  documentKey: string,
  epoch: string,
): Promise<Row[]> {
  if (keyRange) {
    return requestResult<Row[]>(store.index(indexName).getAll(keyRange.only([documentKey, epoch])));
  }
  return (await requestResult<Row[]>(store.getAll())).filter(
    (row) => row.documentKey === documentKey && row.epoch === epoch,
  );
}

class BrowserDocumentAnnotationRepositoryImpl implements BrowserDocumentAnnotationRepository {
  readonly #connection: IDBConnection;
  readonly #options: DocumentAnnotationOptions;
  // Adapter-local registrations keyed by document and then namespace
  // incarnation. Keying by incarnation makes cross-incarnation displacement
  // impossible: a continuation of a retired incarnation writes only its own
  // inert slot (its acceptance fails the durable incarnation gate), while the
  // current incarnation's slot is untouched. Within one incarnation the newest
  // read generation wins, so a held earlier issuance settling after a later one
  // cannot displace the newer registration. Incarnations are random identities
  // with no order: a slot is replaced only on identity, never compared across
  // incarnations. Acceptance still requires the durable head to match, so a
  // later beginRead in ANOTHER adapter retires this ticket too.
  readonly #issuedReads = new Map<string, Map<string, IssuedRead>>();
  // Acceptances currently executing in this adapter. A newer beginRead
  // replaces an older ticket's REGISTRATION while that older acceptance is
  // still in flight, so registration ownership alone cannot answer whether a
  // displaced provisional's owner can still act; this set can.
  readonly #liveAcceptances = new Set<string>();
  // The last completed ticket and accepted source per (document, incarnation)
  // slot, used for immediate in-memory suppression (never durable, never in
  // ComposerSourceState).
  readonly #readStates = new Map<string, Map<string, ReadState>>();
  // Suppression ownership per original accepted epoch; a confirmed entry is
  // irreversible for that old lease.
  readonly #suppressedEpochs = new Map<string, SuppressionEntry>();
  // Incarnations this adapter itself has retired, one per namespace. Guards
  // asynchronous no-await writes that would otherwise reinstate state naming a
  // retired incarnation. A reset prunes the namespace's older record before it
  // adds the incarnation it just retired, so the set stays bounded.
  readonly #retiredIncarnations = new Set<string>();
  // Retained pending expiry fences awaiting real completion.
  readonly #pendingExpiries = new Map<string, PendingExpiry>();
  // Operation identities whose save was dismissed but whose durable discard has
  // not committed: fenced from handoff, and retried by the next pass.
  readonly #pendingCancelledDiscards = new Map<string, PendingCancelledDiscard>();
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
  // boundary. The map holds each source's unsubscribe so close() can reach it,
  // and arming prunes the entries of sources that retired since.
  readonly #recoveryNotificationReleases = new Map<ComposerSourceState, () => void>();
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
    // Browsers always provide IDBKeyRange; jsdom may not, so every compound
    // index query falls back to a full scan when it is absent.
    this.#keyRange = globalThis.IDBKeyRange;
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
    // repository runs none, so the records retire with it. Their rows may be
    // applied with no publication yet, so that evidence is published while
    // listeners and the channel still exist, before closure forbids it.
    for (const operationKey of [...this.#uncertainAppends.keys()]) this.#forgetUncertainAppend(operationKey);
    this.#closed = true;
    for (const release of this.#recoveryNotificationReleases.values()) release();
    this.#recoveryNotificationReleases.clear();
    this.#subscriptions.clear();
    this.#pendingCancelledDiscards.clear();
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

  // The ONE emission rule every committed-but-unobserved transition settles
  // through, called only AFTER its transaction has settled and outside every
  // transaction. A durable transition either re-observes itself and publishes
  // its invalidation here, or it announces itself here; no arm may publish one
  // half of the signal/wake pair and skip the other. A wake names a reader's
  // invalidation, the signal names storage progress the retry runtime spends.
  #settleTransition(options: { documentKey?: string; namespaceKey?: string; signal?: boolean }): void {
    if (this.#closed) return;
    if (options.signal === true) this.#notifyStorageSuccess();
    if (options.documentKey !== undefined) this.#publishDocumentWake(options.documentKey);
    if (options.namespaceKey !== undefined) this.#publishNamespaceWake(options.namespaceKey);
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
    if (this.#recoveryNotificationReleases.has(source)) return;
    // A source that retired since it armed holds a release nobody else would
    // drop: prune those before arming the next one.
    for (const [armed, release] of this.#recoveryNotificationReleases) {
      if (armed.alive) continue;
      release();
      this.#recoveryNotificationReleases.delete(armed);
    }
    const unsubscribe = source.subscribeRecoveryPersisted(() => this.#notifyStorageSuccess());
    this.#recoveryNotificationReleases.set(source, unsubscribe);
  }

  // An uncertain-append record exists only because an append's completion was
  // never observed: the durable row may be APPLIED even though nothing
  // announced it. Dropping the record without publishing would leave that
  // committed evidence silent, and a successor waiting on a trigger that never
  // comes. A redundant re-list is the accepted cost.
  #forgetUncertainAppend(operationKey: string): void {
    const record = this.#uncertainAppends.get(operationKey);
    if (record === undefined) return;
    this.#uncertainAppends.delete(operationKey);
    this.#settleTransition({ signal: true, documentKey: annotationDocumentKey(record.key) });
  }

  // Release the unconfirmed suppression entries a caller still owns and signal
  // the storage-success boundary exactly once iff anything was released. A
  // release RESTORES an epoch's eligibility that an overlapping handoff may
  // have refused against, and it is the only observation of that restored
  // state, so the release itself - not each caller - owns the emission.
  // Callers run after their transaction settled: the signal can start a retry
  // pass and must never fire inside one. Confirmed entries are never released
  // here: a proven change (or a committed reset) keeps its suppression until
  // its expiry's deletion completes.
  #releaseUnconfirmed(owned: readonly { identity: string; requestId?: string | null }[]): void {
    let released = false;
    for (const candidate of owned) {
      const entry = this.#suppressedEpochs.get(candidate.identity);
      if (!entry || entry.confirmed) continue;
      if (candidate.requestId !== undefined && entry.requestId !== candidate.requestId) continue;
      this.#suppressedEpochs.delete(candidate.identity);
      released = true;
    }
    if (released) this.#settleTransition({ signal: true });
  }

  async beginRead(key: DocumentKey): Promise<ReadTicket> {
    if (key.namespace.kind !== "browser") throw new Error("browser annotations require a browser namespace");
    const namespace = key.namespace;
    const documentKey = annotationDocumentKey(key);
    let issuedGeneration = 0;
    const ticket = await this.#transaction(READ_STORES, "readwrite", async (transaction) => {
      const namespaceRequest = readNamespaceRow(transaction, namespace);
      const headRequest = requestResult<AnnotationHeadRow | undefined>(
        transaction.objectStore(ANNOTATION_HEADS_STORE).get(documentKey),
      );
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
    // The issuance transaction may have committed in an incarnation a reset has
    // since retired while this continuation was held. The namespace wake is
    // best-effort, and this continuation cannot know it is dead from its own
    // data, so registration consults the CURRENTLY durable incarnation. A
    // retired-incarnation ticket registers nothing and writes no read state:
    // its caller still receives the correct stale answer from the durable
    // incarnation gate in acceptRead, and nothing here may displace a current
    // registration.
    if (await this.#durableIncarnationIs(namespace, ticket.namespaceIncarnation)) {
      const issuedForDocument = this.#issuedReads.get(documentKey) ?? new Map<string, IssuedRead>();
      this.#issuedReads.set(documentKey, issuedForDocument);
      // A registration of THIS incarnation supersedes the document's other
      // incarnation slots: they name retired incarnations and at most one can
      // remain per document. The gate read above is check-then-act, so a held
      // completion can resume with an answer a reset has since retired: prune a
      // foreign slot only when a fresh read proves this incarnation is current.
      const hasForeignIncarnation = [...issuedForDocument.keys()].some(
        (incarnation) => incarnation !== ticket.namespaceIncarnation,
      );
      const current =
        !hasForeignIncarnation || (await this.#durableIncarnationIs(namespace, ticket.namespaceIncarnation));
      if (current) {
        for (const incarnation of [...issuedForDocument.keys()]) {
          if (incarnation !== ticket.namespaceIncarnation) issuedForDocument.delete(incarnation);
        }
      }
      // Within one incarnation the generation compare keeps a held earlier
      // issuance from replacing a newer one.
      const live = issuedForDocument.get(ticket.namespaceIncarnation);
      if (live === undefined || live.readGeneration < issuedGeneration) {
        issuedForDocument.set(ticket.namespaceIncarnation, {
          requestId: ticket.requestId,
          readGeneration: issuedGeneration,
        });
        const statesForDocument = this.#readStates.get(documentKey) ?? new Map<string, ReadState>();
        this.#readStates.set(documentKey, statesForDocument);
        if (current) {
          for (const incarnation of [...statesForDocument.keys()]) {
            if (incarnation !== ticket.namespaceIncarnation) statesForDocument.delete(incarnation);
          }
        }
        // Carry only this same incarnation's prior state: another incarnation's
        // lease was retired by the reset.
        const carried = statesForDocument.get(ticket.namespaceIncarnation);
        statesForDocument.set(ticket.namespaceIncarnation, {
          ticket,
          accepted: carried?.accepted,
          observations: carried?.observations ?? [],
        });
      }
    }
    this.#settleTransition({ signal: true });
    return ticket;
  }

  async acceptRead(ticket: ReadTicket, content: DocFileContent): Promise<AcceptedReadResult> {
    if (ticket.key.namespace.kind !== "browser") return { state: "stale-ticket" };
    const namespace = ticket.key.namespace;
    const documentKey = annotationDocumentKey(ticket.key);
    const issued = this.#issuedReads.get(documentKey)?.get(ticket.namespaceIncarnation);
    if (issued === undefined || !this.#ownsRegistration(documentKey, ticket.namespaceIncarnation, ticket.requestId)) {
      return { state: "stale-ticket" };
    }
    this.#liveAcceptances.add(ticket.requestId);
    // Entry, before the first await: a locally current read of changed content
    // installs or transfers unconfirmed suppression for the original accepted
    // epoch, so an overlapping append sees it at its own final guard. A confirmed
    // entry never transfers.
    const remembered = this.#readStates.get(documentKey)?.get(ticket.namespaceIncarnation);
    const previous = remembered?.accepted;
    let provisional: string | undefined;
    // The entry this invocation displaced, restored when its validation says
    // not-current so another owner's provisional survives this release.
    let displacedEntry: SuppressionEntry | undefined;
    if (remembered?.ticket.requestId === ticket.requestId && previous) {
      const changed = contentChangedAgainst(remembered.observations, content);
      if (changed) {
        const identity = epochIdentity(previous.lease);
        const entry = this.#suppressedEpochs.get(identity);
        if (!entry?.confirmed) {
          displacedEntry = entry;
          this.#suppressedEpochs.set(identity, {
            documentKey,
            namespaceIncarnation: previous.lease.namespaceIncarnation,
            requestId: ticket.requestId,
            confirmed: false,
          });
          provisional = identity;
        }
      }
    }
    // Confirm or release ONLY this invocation's still-owned provisional entry,
    // from the real durable validation result, never a caller-supplied
    // boolean. The release helper signals the boundary.
    // Restore the entry this invocation displaced only while its owner is
    // still live: the displaced entry belongs to another ticket, and that
    // ticket's registration is what will confirm or release it. An owner that
    // retired can never act again, so restoring its entry would leak an
    // unconfirmed suppression forever. Release this invocation's own install
    // instead, identity-checked, so the map ends empty. An unconfirmed entry
    // always carries a requestId: suppressEpochForExpiry installs confirmed
    // entries, and the reset's provisioning names its read's ticket.
    const restoreDisplacedOrRelease = (identity: string): void => {
      if (
        displacedEntry !== undefined &&
        displacedEntry.requestId !== null &&
        (this.#ownsRegistration(documentKey, ticket.namespaceIncarnation, displacedEntry.requestId) ||
          this.#liveAcceptances.has(displacedEntry.requestId)) &&
        !this.#isRetiredIncarnation(annotationNamespaceKey(namespace), displacedEntry.namespaceIncarnation)
      ) {
        this.#suppressedEpochs.set(identity, displacedEntry);
        return;
      }
      this.#releaseUnconfirmed([{ identity, requestId: ticket.requestId }]);
    };
    const finishValidation = (current: boolean, fencedIdentity?: string): void => {
      if (!provisional) return;
      const entry = this.#suppressedEpochs.get(provisional);
      if (entry?.requestId !== ticket.requestId) return;
      // Confirm only when the changed path fenced exactly the epoch this
      // provisional names: a peer may have advanced the head in between, and a
      // confirmation for the dead epoch would never be released.
      if (current && (fencedIdentity === undefined || provisional === fencedIdentity)) {
        entry.confirmed = true;
        return;
      }
      restoreDisplacedOrRelease(provisional);
    };
    // A stale or unchanged validation releases the provisional restriction a
    // concurrent handoff may have refused against. That refusal only records
    // the runtime's retry need; the release is the sole event proving the
    // restriction lifted, so signal the storage-success boundary the runtime
    // already subscribes to. Without it the annotation stays pending until
    // unrelated activity starts a pass. Called after the validation transaction
    // settled, never inside it, so the pass the runtime may start is serviceable.
    const releaseProvisional = (): void => finishValidation(false);
    try {
      // Validation commits before the fence: the durable identity of this ticket
      // is settled here, and the fence below is a separate transaction so a fence
      // abort cannot roll back validation or reject the read.
      const validation = await this.#transaction(ACCEPT_STORES, "readwrite", async (transaction) => {
        const namespaceRow = await readNamespaceRow(transaction, namespace);
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
              await rowsForDocumentEpoch<AnnotationEvidenceRow>(
                transaction.objectStore(ANNOTATION_EVIDENCE_STORE),
                ANNOTATION_EVIDENCE_DOCUMENT_EPOCH_INDEX,
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
        // local comparison installed and record the completed acceptance. A reset
        // can land between the validation's commit and this continuation, so the
        // durable incarnation decides whether the state may be installed at all:
        // no in-memory read state names an incarnation the durable row retired.
        releaseProvisional();
        if (await this.#durableIncarnationIs(namespace, validation.lease.namespaceIncarnation)) {
          this.#installReadState(ticket, { lease: validation.lease, content }, validation.observations);
        }
        return { state: "accepted", lease: validation.lease };
      }
      // A reset can retire this read's incarnation between the validation's
      // commit and this continuation. No confirmed suppression and no fence may
      // be installed for an incarnation the durable row has retired: the fence
      // would be refused by its own re-check, and a confirmation landing after
      // the reset's sweep would re-create the entry that sweep exists to retire.
      // The caller gets the same stale answer the fence's re-check gives, and a
      // fresh retry registers cleanly under the new incarnation.
      if (!(await this.#durableIncarnationIs(namespace, validation.namespaceIncarnation))) {
        releaseProvisional();
        return { state: "stale-ticket" };
      }
      // A current changed read: confirm suppression BEFORE scheduling its fence,
      // then run the outside-transaction observation boundary. A recreated
      // adapter validated this change against durable history alone — it holds
      // no in-memory acceptance, so no provisional exists to confirm. The
      // expired epoch must be suppressed from the validated durable head
      // itself, or a failed fence leaves the dead epoch visible to listing
      // and handoff-eligible despite the confirmed source change.
      const suppressionIdentity = epochIdentityOf(documentKey, validation.namespaceIncarnation, validation.head.epoch);
      // The exact entry object this invocation installs, so the superseded path
      // releases only its own confirmation and restores whatever an overlapping
      // invocation owned here before.
      const priorSuppression = this.#suppressedEpochs.get(suppressionIdentity);
      const installedSuppression: SuppressionEntry = {
        documentKey,
        namespaceIncarnation: validation.namespaceIncarnation,
        requestId: ticket.requestId,
        confirmed: true,
      };
      this.#suppressedEpochs.set(suppressionIdentity, installedSuppression);
      finishValidation(true, suppressionIdentity);
      // The observed boundary runs before the fence. A rejection here proves no
      // fence committed, so the epoch is still live: release exactly this
      // invocation's own confirmed install (and restore what it displaced) or
      // the epoch stays hidden from listing and refused by saves forever.
      const restoreInstalledSuppression = (): void => {
        if (this.#suppressedEpochs.get(suppressionIdentity) !== installedSuppression) return;
        if (
          priorSuppression === undefined ||
          priorSuppression.requestId === ticket.requestId ||
          this.#isRetiredIncarnation(annotationNamespaceKey(namespace), priorSuppression.namespaceIncarnation)
        ) {
          this.#suppressedEpochs.delete(suppressionIdentity);
        } else {
          this.#suppressedEpochs.set(suppressionIdentity, priorSuppression);
        }
      };
      try {
        await this.#options.beforeExpiryFence?.(ticket);
      } catch (error) {
        restoreInstalledSuppression();
        throw error;
      }
      try {
        const fenced = await this.#fence(
          ticket.key,
          namespace,
          validation.namespaceIncarnation,
          validation.head.epoch,
          content,
        );
        // The replacement epoch starts with its own evidence, never inheriting the
        // dead epoch's observations. The fence can commit and then lose its
        // completion across a reset, so installation re-checks the durable
        // incarnation the fence wrote under.
        if (await this.#durableIncarnationIs(namespace, fenced.lease.namespaceIncarnation)) {
          this.#installReadState(ticket, { lease: fenced.lease, content }, [content]);
        }
        return { state: "expiry-pending", receipt: fenced.receipt };
      } catch (error) {
        // Closure refused the fence before it opened a connection. This adapter
        // can never retry, so the read is stale with no retained work and no
        // post-teardown notification.
        if (this.#closed) return { state: "stale-ticket" };
        if (error instanceof SupersededFenceError) {
          // Another fence already replaced this epoch: a peer's fence, or a
          // reset's rotation whose sweep may already have passed. This
          // invocation's confirmation now names an epoch that is no longer the
          // durable head. Whether its entry may go is DURABLE evidence:
          //   - The incarnation is still current: a fence replaced the epoch
          //     and wrote its expiry row, so the deletion path will settle the
          //     transition. Leave the installed entry (and the prior it
          //     displaced) for that deletion, whose commit publishes the wake.
          //     Removing the entry here would sever the only map state the
          //     deletion path could still be holding.
          //   - The incarnation is no longer current: a peer reset superseded
          //     it, deleted the rows, and published the namespace wake. Nothing
          //     here will ever sweep. Restore the pre-invocation state instead
          //     of leaving an inert entry behind.
          if (await this.#durableIncarnationIs(namespace, validation.namespaceIncarnation)) {
            return { state: "stale-ticket" };
          }
          restoreInstalledSuppression();
          return { state: "stale-ticket" };
        }
        // A failed fence keeps the confirmed suppression and its pending expiry for
        // retry; the read is honest expiry-pending with no committed receipt.
        this.#pendingExpiries.set(suppressionIdentity, {
          namespace,
          documentKey,
          namespaceIncarnation: validation.namespaceIncarnation,
          expiredEpoch: validation.head.epoch,
          content,
          ticketKey: ticket.key,
          suppression: installedSuppression,
        });
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
      this.#liveAcceptances.delete(ticket.requestId);
      // Retire only this invocation's own registration: a newer read that
      // registered while this acceptance was settling keeps its ticket.
      if (this.#ownsRegistration(documentKey, ticket.namespaceIncarnation, ticket.requestId)) {
        const issuedForDocument = this.#issuedReads.get(documentKey);
        issuedForDocument?.delete(ticket.namespaceIncarnation);
        if (issuedForDocument?.size === 0) this.#issuedReads.delete(documentKey);
      }
    }
  }

  // True while this document's live registration in `namespaceIncarnation` is
  // the one `requestId` owns. Entry and finally both refuse or retire only the
  // registration they own.
  #ownsRegistration(documentKey: string, namespaceIncarnation: string, requestId: string): boolean {
    return this.#issuedReads.get(documentKey)?.get(namespaceIncarnation)?.requestId === requestId;
  }

  // Whether a prior reset in this adapter retired the incarnation. Guards the
  // synchronous restores and confirmed writes that cannot re-check durability.
  #isRetiredIncarnation(namespaceKey: string, incarnation: string): boolean {
    return this.#retiredIncarnations.has(`${namespaceKey}\u0000${incarnation}`);
  }

  // Record a completed acceptance (accepted or fenced) in the ticket's own
  // incarnation slot. Within one incarnation validations commit in strict
  // ticket-generation order, so the durable read generation already orders
  // them; two held completions can still RESUME in the opposite order. A
  // newer registration in the slot keeps its identity, and its slot accepts
  // the delayed completion's durable evidence unless that evidence is OLDER
  // than what the slot holds: an acceptance whose lease headRevision is lower
  // is older evidence and must never overwrite it. Equal headRevision is the
  // same epoch and identical evidence (a preserved acceptance copies epoch,
  // headRevision and evidence key), so the overwrite is harmless. Incarnations
  // and epochs are unordered identities and are never compared.
  #installReadState(
    ticket: ReadTicket,
    accepted: { lease: AcceptedSourceLease; content: DocFileContent },
    observations: DocFileContent[],
  ): void {
    const documentKey = annotationDocumentKey(ticket.key);
    const namespaceIncarnation = ticket.namespaceIncarnation;
    const statesForDocument = this.#readStates.get(documentKey) ?? new Map<string, ReadState>();
    this.#readStates.set(documentKey, statesForDocument);
    const existing = statesForDocument.get(namespaceIncarnation);
    if (
      existing !== undefined &&
      existing.ticket.requestId !== ticket.requestId &&
      existing.accepted !== undefined &&
      accepted.lease.headRevision < existing.accepted.lease.headRevision
    ) {
      return;
    }
    statesForDocument.set(
      namespaceIncarnation,
      existing === undefined || existing.ticket.requestId === ticket.requestId
        ? { ticket, accepted, observations }
        : { ticket: existing.ticket, accepted, observations },
    );
  }

  // The durable fence is its own transaction so a failed fence cannot roll back
  // validation and so the observation boundary can run between them. It rechecks
  // the durable identity it is about to replace: an obsolete validation must not
  // rewrite a newer epoch.
  async #fence(
    ticketKey: DocumentKey,
    namespace: Extract<DocumentKey["namespace"], { kind: "browser" }>,
    namespaceIncarnation: string,
    expectedExpiredEpoch: string,
    content: DocFileContent,
  ): Promise<{ receipt: ExpiryReceipt; lease: AcceptedSourceLease }> {
    const documentKey = annotationDocumentKey(ticketKey);
    const fenced = await this.#transaction(ACCEPT_STORES, "readwrite", async (transaction) => {
      const namespaceRow = await readNamespaceRow(transaction, namespace);
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
        await requestResult(putTerminalOperation(transaction, operation, "cancelled"));
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
    // the real transaction commits. The commit settles BOTH halves: it replaces
    // the displayed head, so readers and peers must wake, and it cancels every
    // pending operation of the expired epoch, which is the eligibility-restoring
    // event for a successor that refused behind one of those rows in the session
    // FIFO. That refusal can only land while the changed read's VALIDATION is
    // durably committed but its completion event has not reached acceptRead,
    // because the confirmed-expiry exemption is installed in the continuation,
    // after that completion; releasing the completion leaves the fence's commit
    // as the only observation of the restored eligibility. The runtime's
    // recorded retry need therefore has nothing to spend without this signal,
    // and the healthy successor waits for unrelated external activity.
    this.#settleTransition({ signal: true, documentKey });
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
    // The dismissal's fence must be up at the moment the cancellation lands,
    // not when the transaction settles: the committed row is visible to a retry
    // pass while the held completion still keeps this save unsettled, and that
    // pass must refuse the identity. The token notifies synchronously with the
    // cancellation, which is the earliest observation of it.
    const releaseCancellationFence = cancellation?.onCancel(() => {
      // Only a dismissal fences. Teardown aborts the transaction below and
      // must leave a committed row pending, never flip it terminal.
      if (cancellation.reason === "dismissal") {
        this.#fenceCancelledDiscard(captured.lease.key, captured.operationId);
      }
    });
    let receipt: SaveReceipt;
    try {
      receipt = await this.#transaction(
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
            return {
              operationId: captured.operationId,
              epoch: existing.epoch,
              sessionSequence: existing.sessionSequence,
            } satisfies SaveReceipt;
          }
          const namespaceRow = await readNamespaceRow(transaction, namespace);
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
          const epochEvidence = await rowsForDocumentEpoch<AnnotationEvidenceRow>(
            transaction.objectStore(ANNOTATION_EVIDENCE_STORE),
            ANNOTATION_EVIDENCE_DOCUMENT_EPOCH_INDEX,
            this.#keyRange,
            documentKey,
            head.epoch,
          );
          const capturedFingerprint = JSON.stringify(captured.capture.evidence.fingerprint);
          if (
            !epochEvidence.some((row) => evidenceMatches(row.content, captured.capture.evidence, capturedFingerprint))
          ) {
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
          return { operationId: captured.operationId, epoch: head.epoch, sessionSequence } satisfies SaveReceipt;
        },
        cancellation,
      );
    } catch (error) {
      // The transaction may have committed before the rejection: a watchdog
      // outlives a held completion event. A dismissed attempt still leaves no
      // durable work for a later pass.
      if (cancellation?.cancelled) {
        releaseCancellationFence?.();
        if (cancellation.reason === "dismissal") await this.#discardCancelledSave(captured);
        else {
          // Teardown reached the save past its abort boundary, so the
          // transaction may have committed a row no completion will announce.
          // An ownerless destination has no later handoff that could publish
          // it (its unclaimed settle changes no listing and is deliberately
          // silent), so the catch publishes the commit it may have left.
          this.#settleTransition({ signal: true, documentKey });
        }
        throw new SaveCancelledError();
      }
      releaseCancellationFence?.();
      throw error;
    }
    releaseCancellationFence?.();
    // Cancellation reaches the transaction only while it can still abort. One
    // that passed that boundary commits anyway, so the success path must
    // observe the token too: a dismissed row must retire, while a teardown
    // leaves the committed row pending for a later runtime.
    if (cancellation?.cancelled) {
      if (cancellation.reason === "dismissal") {
        await this.#discardCancelledSave(captured);
      } else {
        // Teardown reached the save past its abort boundary. The committed row
        // is real pending work for whichever runtime retries after teardown,
        // never a dismissal: publish the commit and leave the row pending.
        this.#settleTransition({ signal: true, documentKey });
      }
      throw new SaveCancelledError();
    }
    // A newly persisted operation and a replay that confirms an existing one
    // both name a committed state readers may not have observed.
    this.#settleTransition({ signal: true, documentKey });
    return receipt;
  }

  // A save the runtime dismissed can still commit: its cancellation reaches the
  // transaction only while it can abort, and one past that boundary leaves its
  // pending row durable. This is the SECOND arm: the dismissal-time flip
  // (dismissOperation) already ran, and this re-runs it only when that flip
  // found no row yet. The runtime's cancellation stays terminal for this
  // attempt either way.
  async #discardCancelledSave(operation: PreparedAnnotation): Promise<void> {
    const record = this.#fenceCancelledDiscard(operation.lease.key, operation.operationId);
    if (!record) return;
    // A dismissal-time flip that already classified the row answers this arm:
    // a flipped/terminal/applied row needs no retry, and a faulted one is
    // retained for the next trigger. Only a flip that ran before the save
    // committed (row absent) must be repeated now that the row exists.
    if (record.dismissal !== undefined) {
      const outcome = await record.dismissal;
      if (outcome !== "absent") return;
    }
    await this.#completeDismissal(record, true);
  }

  // Dismiss ONE operation. The fence is installed synchronously, before this
  // method's first await, so a handoff that reaches the repository while the
  // flip is still in flight refuses; the flip itself becomes the durable
  // cross-instance fence. Idempotent: IndexedDB serializes this write
  // transaction against the save's own, so the flip sees either the committed
  // row or no row yet.
  async dismissOperation(key: DocumentKey, operationId: string): Promise<void> {
    const record = this.#fenceCancelledDiscard(key, operationId);
    if (!record) return;
    const dismissal = this.#completeDismissal(record, false);
    record.dismissal = dismissal;
    await dismissal;
  }

  // ONE runner owns both dismissal arms' fault handling. Closure is terminal:
  // this adapter retains nothing it could never retry, and a handoff refuses
  // anyway. Any other fault keeps the fence and the retained record, and the
  // runtime registers the need a later storage-success trigger spends.
  async #completeDismissal(record: PendingCancelledDiscard, dropAbsent: boolean): Promise<DiscardOutcome> {
    try {
      return await this.#completeCancelledDiscard(record, dropAbsent);
    } catch {
      if (!this.#closed) invokeObserver(() => this.#options.onCancelledDiscardRetained?.());
      return "faulted";
    }
  }

  // Fence ONE operation identity from handoff until its durable discard
  // commits. Installed at dismissal time (synchronously) and by the save's own
  // cancellation observation; a closed adapter retains nothing it could never
  // retry, and handoffs refuse anyway.
  #fenceCancelledDiscard(key: DocumentKey, operationId: string): PendingCancelledDiscard | null {
    if (this.#closed) return null;
    const namespace = key.namespace;
    if (namespace.kind !== "browser") return null;
    const operationKey = annotationOperationKey(namespace, operationId);
    const existing = this.#pendingCancelledDiscards.get(operationKey);
    if (existing) return existing;
    const record: PendingCancelledDiscard = { key, operationId };
    this.#pendingCancelledDiscards.set(operationKey, record);
    return record;
  }

  // The durable row's terminal state is the only thing that drops the fence: a
  // no-op discard over an APPLIED row leaves committed protection work this
  // fence still owns, so it is retained rather than silently released. An
  // absent row may still precede a commit in the save's in-flight window, so
  // only a settlement/retry caller (dropAbsent) may release that case. A fault
  // propagates and leaves the record for the next pass to retry.
  async #completeCancelledDiscard(record: PendingCancelledDiscard, dropAbsent: boolean): Promise<DiscardOutcome> {
    const outcome = await this.#discardPendingOperationState(record.key, record.operationId);
    if (outcome === "applied") return outcome;
    if (outcome === "absent" && !dropAbsent) return outcome;
    // Every record is browser-kind by construction: #fenceCancelledDiscard
    // refuses any other namespace.
    const operationKey = annotationOperationKey(record.key.namespace, record.operationId);
    if (this.#pendingCancelledDiscards.get(operationKey) === record) {
      this.#pendingCancelledDiscards.delete(operationKey);
    }
    return outcome;
  }

  // Listing is durable-read only: no composer lookup, transport or mutation. It
  // returns unexpired pending AND applied operations whose retained payload still
  // exists (payloadKey equals the operation key), excluding cancelled rows, any
  // epoch carrying a durable expiry row, and identities a dismissal still
  // fences. The flip at dismissal time collapses the window to its commit; the
  // fence filter hides the note even inside that window.
  async listAnnotations(key: DocumentKey): Promise<AnnotationView[]> {
    if (key.namespace.kind !== "browser") return [];
    const documentKey = annotationDocumentKey(key);
    return this.#transaction(
      [ANNOTATION_OPERATIONS_STORE, ANNOTATION_PAYLOADS_STORE, ANNOTATION_EXPIRY_STORE],
      "readonly",
      async (transaction) => {
        const [operations, payloads, expiries] = await Promise.all([
          requestResult<AnnotationOperationRow[]>(transaction.objectStore(ANNOTATION_OPERATIONS_STORE).getAll()),
          // The map is consulted only with this document's operation keys, so
          // the compound index bounds the payload read to the document; arrays
          // sort after strings in IDB key order, so the upper bound covers
          // every epoch.
          this.#keyRange
            ? requestResult<AnnotationPayloadRow[]>(
                transaction
                  .objectStore(ANNOTATION_PAYLOADS_STORE)
                  .index(ANNOTATION_PAYLOADS_DOCUMENT_EPOCH_INDEX)
                  .getAll(this.#keyRange.bound([documentKey], [documentKey, []])),
              )
            : requestResult<AnnotationPayloadRow[]>(transaction.objectStore(ANNOTATION_PAYLOADS_STORE).getAll()),
          requestResult<AnnotationExpiryRow[]>(transaction.objectStore(ANNOTATION_EXPIRY_STORE).getAll()),
        ]);
        const expiredKeys = new Set(expiries.map((row) => row.expiryKey));
        const payloadByKey = new Map(payloads.map((row) => [row.payloadKey, row]));
        const views: AnnotationView[] = [];
        for (const row of operations) {
          if (row.documentKey !== documentKey || row.state === "cancelled") continue;
          if (this.#pendingCancelledDiscards.has(row.operationKey)) continue;
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
    try {
      await this.#transaction([...ACCEPT_STORES, ANNOTATION_PAYLOADS_STORE], "readwrite", async (transaction) => {
        const namespaceRow = await readNamespaceRow(transaction, namespace);
        if (!namespaceRow || namespaceRow.incarnation !== receipt.namespaceIncarnation) return;
        const expiryRow = await requestResult<AnnotationExpiryRow | undefined>(
          transaction.objectStore(ANNOTATION_EXPIRY_STORE).get(expiryKey),
        );
        if (!expiryRow || expiryRow.receipt.expiredEpoch !== receipt.expiredEpoch) return;
        if (expiryRow.receipt.phase === "deleted") return;
        const [payloads, evidence] = await Promise.all([
          rowsForDocumentEpoch<AnnotationPayloadRow>(
            transaction.objectStore(ANNOTATION_PAYLOADS_STORE),
            ANNOTATION_PAYLOADS_DOCUMENT_EPOCH_INDEX,
            this.#keyRange,
            documentKey,
            receipt.expiredEpoch,
          ),
          rowsForDocumentEpoch<AnnotationEvidenceRow>(
            transaction.objectStore(ANNOTATION_EVIDENCE_STORE),
            ANNOTATION_EVIDENCE_DOCUMENT_EPOCH_INDEX,
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
      });
    } catch (error) {
      // The deletion may have committed before the rejection: a watchdog can
      // outlive a held completion. The durable expiry row is the evidence of
      // that commit, so a "deleted" row settles the same release and wake the
      // resolved path would have published.
      await this.#settleDeletedExpiry(receipt, expiryKey);
      throw error;
    }
    if (removed) {
      // The deletion's own commit is the evidence: this transaction performed
      // the fenced-to-deleted phase transition, so readers must re-list even
      // when a retry arm already removed the suppression entry. The release
      // below is memory hygiene only.
      this.#releaseExpiredEpoch(documentKey, receipt.namespaceIncarnation, receipt.expiredEpoch);
      this.#settleTransition({ documentKey });
    }
  }

  // The expired epoch's payloads and evidence are gone, its replacement minted
  // a fresh UUID epoch, and the fence that wrote this receipt already flipped
  // the epoch's pending operations terminal. Every consult path keys on this
  // exact document+incarnation+epoch identity - listing, save validation,
  // handoff eligibility, durable validation and the revert-resurrection fence -
  // so a released entry can never legitimately match a live epoch again: the
  // identity names a deleted UUID, never a replacement. Releasing it is pure
  // memory hygiene: the deletion's own commit owns the wake, so this helper
  // never publishes one.
  #releaseExpiredEpoch(documentKey: string, namespaceIncarnation: string, expiredEpoch: string): void {
    this.#suppressedEpochs.delete(epochIdentityOf(documentKey, namespaceIncarnation, expiredEpoch));
  }

  // Act on the durable "deleted" phase a rejected finishExpiry left behind.
  // True when the row proves the commit; the release and wake have then been
  // published through the one emission helper.
  async #settleDeletedExpiry(receipt: ExpiryReceipt, expiryKey: string): Promise<boolean> {
    if (this.#closed) return false;
    let row: AnnotationExpiryRow | undefined;
    try {
      row = await this.#readRow<AnnotationExpiryRow>(ANNOTATION_EXPIRY_STORE, expiryKey);
    } catch {
      // The evidence is unreadable now; the original rejection stays
      // authoritative and a later sweep re-observes the row.
      return false;
    }
    if (row?.receipt.phase !== "deleted") return false;
    this.#releaseExpiredEpoch(annotationDocumentKey(receipt.key), receipt.namespaceIncarnation, receipt.expiredEpoch);
    // The row proves the deletion committed even though its caller never
    // observed the completion, so this arm publishes the phase transition.
    this.#settleTransition({ documentKey: annotationDocumentKey(receipt.key) });
    return true;
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
  // untouched, so a replay is an idempotent no-op. A committed flip emits its
  // own observation: the cancelled row leaves every listing, and a successor
  // that refused behind it in the session FIFO becomes eligible again. A no-op
  // changes neither and stays silent.
  async discardPendingOperation(key: DocumentKey, operationId: string): Promise<void> {
    await this.#discardPendingOperationState(key, operationId);
  }

  // The state-reporting discard behind both the public operation and the
  // dismissal fence: "applied" is the one non-pending outcome a dismissal must
  // not treat as terminal, because the append it protects already committed.
  async #discardPendingOperationState(key: DocumentKey, operationId: string): Promise<DiscardOutcome> {
    if (key.namespace.kind !== "browser") return "absent";
    const operationKey = annotationOperationKey(key.namespace, operationId);
    let outcome: DiscardOutcome = "absent";
    let flippedDocumentKey: string | undefined;
    let terminalDocumentKey: string | undefined;
    let appliedDocumentKey: string | undefined;
    await this.#transaction([ANNOTATION_OPERATIONS_STORE], "readwrite", async (transaction) => {
      const row = await requestResult<AnnotationOperationRow | undefined>(
        transaction.objectStore(ANNOTATION_OPERATIONS_STORE).get(operationKey),
      );
      if (!row) return;
      // A row that moved past pending is terminal for this dismissal unless it
      // is the applied receipt the append wrote: that is live work the fence
      // still protects.
      if (row.state !== "pending") {
        outcome = row.state === "applied" ? "applied" : "terminal";
        // A terminal row is the committed evidence of a flip whose completion
        // an earlier arm never observed. Publish it before the record retires,
        // or the dismissal's last trace leaves with the record.
        if (row.state !== "applied") terminalDocumentKey = row.documentKey;
        else appliedDocumentKey = row.documentKey;
        return;
      }
      this.#options.beforeCommit?.("discard");
      await requestResult(putTerminalOperation(transaction, row, "cancelled"));
      outcome = "flipped";
      flippedDocumentKey = row.documentKey;
    });
    // A committed pending-to-cancelled flip is the eligibility-restoring event
    // for a successor that refused behind this row in the session FIFO, and it
    // removes the row from every reader's listing. An arm that instead OBSERVED
    // an already-terminal row owns the same publication: the flip committed
    // earlier, but the watchdog outlived the completion that would have
    // announced it. Emit both observations after the transaction settles,
    // outside it, exactly as every other committed transition does.
    if (terminalDocumentKey !== undefined) this.#settleTransition({ signal: true, documentKey: terminalDocumentKey });
    if (flippedDocumentKey !== undefined) this.#settleTransition({ signal: true, documentKey: flippedDocumentKey });
    // An applied row stays committed but the permanent dismissal fence hides
    // it from this adapter's listing, so same-adapter subscribers must re-list.
    // Later passes observe the same applied row; only the first publishes.
    if (appliedDocumentKey !== undefined) {
      const record = this.#pendingCancelledDiscards.get(operationKey);
      if (record !== undefined && record.wakePublished !== true) {
        record.wakePublished = true;
        this.#settleTransition({ documentKey: appliedDocumentKey });
      }
    }
    return outcome;
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
    if (this.#isRetiredIncarnation(annotationNamespaceKey(lease.key.namespace), lease.namespaceIncarnation)) return;
    this.#suppressedEpochs.set(epochIdentity(lease), {
      documentKey: annotationDocumentKey(lease.key),
      namespaceIncarnation: lease.namespaceIncarnation,
      requestId: null,
      confirmed: true,
    });
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
    // Complete retained dismissals FIRST: their identities stay fenced from the
    // pending sweep below until the discard commits, and a committed discard
    // leaves the sweep no row to visit. A fault retains the record for the next
    // trigger and surfaces through the same retained-fault rule as the fences.
    for (const record of [...this.#pendingCancelledDiscards.values()]) {
      try {
        await this.#completeCancelledDiscard(record, true);
      } catch (error) {
        retainedFaults.push(error);
      }
    }
    for (const [identity, record] of [...this.#pendingExpiries]) {
      try {
        await this.#fence(
          record.ticketKey,
          record.namespace,
          record.namespaceIncarnation,
          record.expiredEpoch,
          record.content,
        );
        this.#pendingExpiries.delete(identity);
      } catch (error) {
        if (error instanceof SupersededFenceError) {
          // Another fence already replaced this epoch: the retained work is
          // done. Whether the entry may go is durable evidence: when the
          // incarnation is still current, a fence replaced the epoch and its
          // expiry row survives, so the sweep below commits the deletion and
          // that commit publishes the wake. Deleting the entry here would make
          // the deletion silent. Only a peer reset - an incarnation that is no
          // longer durable, whose rows and expiry the peer deleted and whose
          // namespace wake is the publisher - leaves the entry inert here.
          if (
            this.#suppressedEpochs.get(identity) === record.suppression &&
            !(await this.#durableIncarnationIs(record.namespace, record.namespaceIncarnation))
          ) {
            this.#suppressedEpochs.delete(identity);
          }
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
      if (row.receipt.phase === "deleted") {
        // A deleted row this adapter still suppresses is a finishExpiry whose
        // commit its caller never observed (or whose release it never
        // reached). The row is re-observed every pass, so the suppression
        // entry is the once-only guard: the pass that finds it publishes the
        // transition and drops it, and a later pass with no entry stays
        // silent. The row itself cannot be the guard; it persists.
        const documentKey = annotationDocumentKey(row.receipt.key);
        const identity = epochIdentityOf(documentKey, row.receipt.namespaceIncarnation, row.receipt.expiredEpoch);
        if (this.#suppressedEpochs.delete(identity)) this.#settleTransition({ documentKey });
        continue;
      }
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
    // Keyed like every durable identity: two namespaces may mint the same bare
    // operation ID, and the pending row's own operationKey is the identity
    // that tells them apart.
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
      handled.add(annotationOperationKey(record.key.namespace, record.operationId));
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
    for (const { row, payload } of pending) {
      // A sweep outlives the runtime that started it only until disposal: a
      // later handoff must not start against a retained composer. The
      // operations it never reached keep their pending receipts.
      if (cancellation?.cancelled) return;
      // Reconciliation above already handed this operation off in this pass; a
      // second attempt would only repeat the outcome against the composer it
      // just touched.
      if (handled.has(row.operationKey)) continue;
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
      () => this.#closed,
      cancellation,
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
    const provisioned: { identity: string; requestId: string }[] = [];
    for (const [documentKey, states] of this.#readStates) {
      if (documentKeyNamespace(documentKey) !== namespaceKey) continue;
      const state = states.get(expectedIncarnation);
      if (!state) continue;
      const accepted = state.accepted;
      if (accepted && accepted.lease.namespaceIncarnation === expectedIncarnation) {
        const identity = epochIdentity(accepted.lease);
        if (this.#suppressedEpochs.has(identity)) continue;
        this.#suppressedEpochs.set(identity, {
          documentKey,
          namespaceIncarnation: expectedIncarnation,
          requestId: state.ticket.requestId,
          confirmed: false,
        });
        provisioned.push({ identity, requestId: state.ticket.requestId });
      }
    }
    const confirmProvisioned = (): void => {
      for (const { identity, requestId } of provisioned) {
        const entry = this.#suppressedEpochs.get(identity);
        if (entry && !entry.confirmed && entry.requestId === requestId) entry.confirmed = true;
      }
    };
    // An aborted or non-rotating reset releases what it provisioned. The release
    // RESTORES the old epoch's eligibility: a handoff that refused pending while
    // the reset ran can append again, and this release is the only observation
    // of that restored state. Both callers below run after the reset
    // transaction settled, so the storage-success signal the release helper
    // fires cannot start a pass inside a transaction.
    const releaseProvisioned = (): void => {
      this.#releaseUnconfirmed(provisioned);
    };
    const newIncarnation = createSecureUUID();
    let result: { committed: boolean; incarnation: string };
    try {
      result = await this.#transaction(RESET_STORES, "readwrite", async (transaction) => {
        const namespaceRow = await readNamespaceRow(transaction, namespace);
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
      });
    } catch (error) {
      // The rejection is not proof of a rollback: a watchdog can outlive a
      // completion, so the reset may have committed. The durable namespace
      // row's NEW incarnation is the commit's evidence; when it landed, this
      // arm owns the publication the lost completion skipped. Otherwise the
      // transaction did roll back and its release is an observation boundary
      // outside it.
      if (await this.#durableIncarnationIs(namespace, newIncarnation)) {
        result = { committed: true, incarnation: newIncarnation };
      } else {
        releaseProvisioned();
        throw error;
      }
    }
    if (result.committed) {
      confirmProvisioned();
      // Remember what this adapter retired. One record per namespace: an older
      // tombstone is pruned before the incarnation just retired is added.
      const retiredPrefix = `${namespaceKey}\u0000`;
      for (const key of [...this.#retiredIncarnations]) {
        if (key.startsWith(retiredPrefix)) this.#retiredIncarnations.delete(key);
      }
      this.#retiredIncarnations.add(`${retiredPrefix}${expectedIncarnation}`);
      // Reset wakes every registration in its exact namespace, and no other.
      this.#settleTransition({ namespaceKey });
      // A live ticket names its own issuance incarnation. The reset just retired
      // exactly one incarnation, so only registrations naming it are dead: a
      // read that registered in the NEW incarnation before this continuation
      // ran is current and must survive. Never a blanket clear.
      for (const [documentKey, issued] of [...this.#issuedReads]) {
        if (documentKeyNamespace(documentKey) !== namespaceKey) continue;
        issued.delete(expectedIncarnation);
        if (issued.size === 0) this.#issuedReads.delete(documentKey);
      }
      // The reset deleted every row these entries named, so no consult path
      // can match them again. Retire every suppression entry of the retired
      // incarnation, provisioned or CONFIRMED: the reset also deleted the
      // expiry rows that a confirmed entry's later finishExpiry would wait for,
      // so nothing else will ever release it. Otherwise a long-lived adapter
      // retains one inert entry per pre-reset changed read.
      for (const [identity, entry] of [...this.#suppressedEpochs]) {
        if (
          entry.namespaceIncarnation === expectedIncarnation &&
          documentKeyNamespace(entry.documentKey) === namespaceKey
        ) {
          this.#suppressedEpochs.delete(identity);
        }
      }
      // Scope the read-state retirement exactly like the registrations above:
      // a replacement-incarnation acceptance installed while this reset's own
      // continuation was held is current and must survive.
      for (const [documentKey, states] of [...this.#readStates]) {
        if (documentKeyNamespace(documentKey) !== namespaceKey) continue;
        states.delete(expectedIncarnation);
        if (states.size === 0) this.#readStates.delete(documentKey);
      }
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
      releaseProvisioned();
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
    // A dismissed save's identity is fenced until its durable discard commits.
    // Its row may still be pending, but a dismissed note must never reach the
    // composer; the terminal cancelled outcome also drops the runtime's retry
    // intent so no pass keeps revisiting it. The retained composer seam retires
    // with the identity: reconciling it later would deliver the dismissed note
    // from the committed backing.
    if (this.#pendingCancelledDiscards.has(operationKey)) {
      this.#forgetUncertainAppend(operationKey);
      return { state: "cancelled" };
    }
    // The whole fallible pre-commit path returns the documented fulfilled
    // pending/owner-unavailable result on a storage or preparation fault: an
    // unreadable receipt is not cancelled and must stay recoverable through the
    // runtime's storage-recovery retry. A known committed receipt remains
    // authoritative, and post-commit observer faults still cannot relabel it.
    let receipt: AnnotationOperationRow | undefined;
    try {
      receipt = await this.#readRow<AnnotationOperationRow>(ANNOTATION_OPERATIONS_STORE, operationKey);
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
      if (reconciliation !== "faulted") this.#forgetUncertainAppend(operationKey);
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
      payload = await this.#readRow<AnnotationPayloadRow>(ANNOTATION_PAYLOADS_STORE, receipt.payloadKey);
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
      // A dismissal joins the same predicate at the append's final guard: its
      // synchronous fence is set before its durable flip can be observed here.
      if (this.#pendingCancelledDiscards.has(operationKey)) return false;
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
    // The entry guard ran before the preparation; a dismissal that landed while
    // this handoff waited owns the identity now. Answer terminal and retire the
    // retained seam rather than appending the dismissed note.
    if (this.#pendingCancelledDiscards.has(operationKey)) {
      this.#forgetUncertainAppend(operationKey);
      return { state: "cancelled" };
    }
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
      // A dismissal that lost the commit race still owns the composer delivery.
      // The flip found the applied row and kept the fence, by design - committed
      // work cannot be aborted - so the durable state is honestly applied and
      // the commit below is still reported. Only the delivery is ours to
      // refuse: acknowledging here would insert the dismissed note into the
      // very editor the user dismissed it from.
      if (!this.#pendingCancelledDiscards.has(operationKey)) {
        const delivered = attemptDelivery(() => source.acknowledgeAnnotation(preparation, result.token, result.value));
        // A fault leaves the composer's token un-advanced while the handoff
        // reports applied. Retain the existing uncertain-append record so a
        // later pass re-drives the delivery from the committed backing.
        if (!delivered) {
          this.#uncertainAppends.set(operationKey, { key, operationId, source, preparation });
          // The receipt answers applied, so this handoff's own settlement sees
          // no pending work and would drop the trigger the transition below
          // latches. Register the retained delivery BEFORE that signal, so the
          // signal latches on outstanding work and the settlement spends it on
          // the pass that re-drives the delivery.
          invokeObserver(() => this.#options.onUncertainAppendRetained?.());
        }
      }
      // The append committed; wake matching readers (and peers) after commit.
      this.#settleTransition({ signal: true, documentKey: annotationDocumentKey(key) });
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
    // A dismissed identity is never reconciled into the composer: the
    // acknowledgment below is the one path that can still deliver the note
    // after the fence is gone. Returning "none" makes the caller drop the
    // record.
    if (this.#pendingCancelledDiscards.has(operationKey)) return "none";
    let backing: CommittedAppendBacking | null;
    try {
      backing = await this.#options.drafts.readAppendBacking(retained.preparation);
    } catch {
      // A transient read fault keeps the record: the receipt is applied, so
      // only this reconciliation is still outstanding. The handoff answers
      // already-applied and stays signal-free, so the retained work registers
      // the need its own settlement spends.
      invokeObserver(() => this.#options.onUncertainAppendRetained?.());
      return "faulted";
    }
    if (this.#closed || cancellation?.cancelled === true) return "none";
    // The fence can land while the backing read is outstanding.
    if (this.#pendingCancelledDiscards.has(operationKey)) return "none";
    this.#settleTransition({ documentKey: annotationDocumentKey(retained.key) });
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
    if (!attemptDelivery(() => retained.source.acknowledgeAnnotation(retained.preparation, token, value))) {
      // The delivery faulted: the record stays and the next pass attempts it
      // again. The applied row is already published, so no signal is owed -
      // and no signal may be fired here: the retained delivery registers the
      // need its handoff's settlement spends instead.
      invokeObserver(() => this.#options.onUncertainAppendRetained?.());
      return "faulted";
    }
    // The acknowledgment is the moment the committed backing makes the
    // composer current again: a handoff that refused against the pre-append
    // token can now append. Signal storage success here, outside every
    // transaction, so the runtime can start the pass that revisits it - safe
    // in-pass because the runtime retains a trigger arriving while its work is
    // outstanding. "none" and "unreconcilable" restore no eligibility (the
    // annotation is durably applied and listed) and stay silent.
    this.#settleTransition({ signal: true });
    return "reconciled";
  }

  async #readRow<Row>(storeName: string, key: string): Promise<Row | undefined> {
    return this.#transaction([storeName], "readonly", (transaction) =>
      requestResult<Row | undefined>(transaction.objectStore(storeName).get(key)),
    );
  }

  // The namespace's current durable incarnation (see #issuedReads); an
  // unreadable identity proves nothing current.
  async #durableIncarnationIs(
    namespace: Extract<DocumentKey["namespace"], { kind: "browser" }>,
    incarnation: string,
  ): Promise<boolean> {
    try {
      const row = await this.#readRow<NamespaceRow>(ANNOTATION_NAMESPACES_STORE, annotationNamespaceKey(namespace));
      return row?.incarnation === incarnation;
    } catch {
      return false;
    }
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
      await requestResult(putTerminalOperation(transaction, row, "unclaimed"));
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
    try {
      // Annotation writes reconcile on retry through their operation rows (the
      // duplicate-operation identity check) and reads re-read durable state,
      // so an unobserved outcome is honestly reported as a fault for the
      // caller's own retry instead of leaving it pending forever - the same
      // settle-the-caller call the drafts repository and session cache
      // watchdogs make.
      return await runBoundedTransaction({
        transaction,
        retire: () => this.#connection.retire(database),
        waitMs: STORAGE_WAIT_MS,
        timeout: () => new IndexedDBTransactionTimeoutError("Document annotation repository transaction timed out"),
        run: () => body(transaction),
      });
    } finally {
      release?.();
    }
  }
}

export function createDocumentAnnotationRepository(
  options: DocumentAnnotationOptions,
): BrowserDocumentAnnotationRepository {
  return new BrowserDocumentAnnotationRepositoryImpl(options);
}

// Wrap one real retry pass with a completed, read-only receipt scan. The pass
// runs to completion first; only IDB request/completion promises are awaited in
// the outcome transaction, and a fulfilled pass is never itself treated as
// recovery. Applied/cancelled receipts are not pending; open/read/completion
// failures reject and keep the caller's retry-needed state.
async function retryBrowserAnnotationsWithOutcome(
  repository: BrowserDocumentAnnotationRepository,
  connection: IDBConnection,
  reason: AnnotationRetryReason,
  // Outstanding reconciliation work for appends whose completion went
  // unobserved: a receipt that answers applied never appears in the pending
  // scan below, but a faulted reconciliation read leaves the live composer
  // stale and must keep the runtime's shared retry intent alive.
  hasRetainedReconciliation: () => boolean,
  isClosed: () => boolean,
  cancellation?: AnnotationRetryCancellation,
): Promise<BrowserAnnotationRetryOutcome> {
  // close() is terminal: a pass that was already in flight must not reopen the
  // adapter for the scan. Refuse like any other storage fault so the caller
  // keeps its retry-needed state instead of reading through teardown.
  if (isClosed()) throw new RepositoryClosedError();
  await repository.retryPending(reason, cancellation);
  if (isClosed()) throw new RepositoryClosedError();
  const database = await connection.open();
  if (isClosed()) {
    connection.close();
    throw new RepositoryClosedError();
  }
  const transaction = database.transaction(ANNOTATION_OPERATIONS_STORE, "readonly");
  // This scan ends the runtime's shared retry pass, so a stalled request
  // would leave every later retry awaiting the same never-settling promise.
  // The same deadline the repository's transactions carry applies: abort
  // while the transaction can still abort, retire the connection, settle
  // the caller so its retry-needed state survives for the next pass.
  const operations = await runBoundedTransaction({
    transaction,
    retire: () => connection.retire(database),
    waitMs: STORAGE_WAIT_MS,
    timeout: () => new IndexedDBTransactionTimeoutError("Document annotation outcome scan timed out"),
    run: () => requestResult<AnnotationOperationRow[]>(transaction.objectStore(ANNOTATION_OPERATIONS_STORE).getAll()),
  });
  return {
    pending: operations.some((operation) => operation.state === "pending") || hasRetainedReconciliation(),
  };
}
