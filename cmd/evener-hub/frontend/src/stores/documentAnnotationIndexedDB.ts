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
import type { ComposerPreparation, ComposerSourceState } from "../panes/session/composer/sourceState";
import type { AppendAnnotationResult, BrowserDraftKey, BrowserDraftRepository } from "./browserDraftRepository";
import { type NamespaceRow, serializeSessionKey } from "./compositionRevision";
import { IDBConnection, requestResult, transactionCompletion, tryAbortTransaction } from "./idbConnection";
import {
  ANNOTATION_EVIDENCE_STORE,
  ANNOTATION_EXPIRY_STORE,
  ANNOTATION_HEADS_STORE,
  ANNOTATION_NAMESPACES_STORE,
  ANNOTATION_OPERATIONS_STORE,
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
}

export interface BrowserDocumentAnnotationRepository extends AnnotationReadRepository {
  saveAnnotation(operation: PreparedAnnotation): Promise<SaveReceipt>;
  listAnnotations(key: DocumentKey): Promise<AnnotationView[]>;
  finishExpiry(receipt: ExpiryReceipt): Promise<void>;
  retryPending(reason: AnnotationRetryReason): Promise<void>;
  retryPendingWithOutcome(reason: AnnotationRetryReason): Promise<BrowserAnnotationRetryOutcome>;
  resetOwnedNamespace(
    namespace: BrowserDraftKey["namespace"],
    expectedIncarnation: string,
  ): Promise<{ committed: boolean; incarnation: string }>;
  handoff(key: DocumentKey, operationId: string): Promise<HandoffResult>;
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

interface SuppressionEntry {
  requestId: string;
  confirmed: boolean;
}

interface ReadState {
  ticket: ReadTicket;
  accepted?: { lease: AcceptedSourceLease; content: DocFileContent };
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

function epochIdentity(lease: AcceptedSourceLease): string {
  return epochIdentityOf(annotationDocumentKey(lease.key), lease.namespaceIncarnation, lease.epoch);
}

function epochIdentityOf(documentKey: string, namespaceIncarnation: string, epoch: string): string {
  return JSON.stringify([documentKey, namespaceIncarnation, epoch]);
}

// Deterministic evidence key for an epoch's retained comparable baseline. It is
// deliberately distinct from the head's current-display evidence, so an
// inconclusive read can advance the display without erasing the evidence needed
// to detect a later definite change. The row is removed with its epoch.
function annotationBaselineEvidenceKey(epoch: string): string {
  return JSON.stringify(["annotation-baseline", epoch]);
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
  // Keyed registrations, woken only after a committed change to their exact
  // document or namespace.
  readonly #subscriptions = new Set<AnnotationSubscription>();
  // Best-effort peer wakeups, scoped to this adapter's database.
  readonly #channel: AnnotationWakeChannel;
  readonly #channelSourceId = makeSourceId();

  constructor(options: DocumentAnnotationOptions) {
    const indexedDB = options.indexedDB ?? globalThis.indexedDB;
    if (!indexedDB) throw new Error("IndexedDB is unavailable");
    this.#options = options;
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
        open: "Unable to open document annotation repository",
        superseded: "Document annotation repository connection was closed",
        timeout: () => new Error("Document annotation repository open timed out"),
        blocked: "Document annotation repository upgrade is blocked",
      },
      reportDiagnostic: () => undefined,
    });
  }

  close(): void {
    // Late work must not revive a closed registration; retire every listener and
    // the peer channel before the connection.
    this.#subscriptions.clear();
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
    this.#notifyDocument(documentKey);
    this.#channel.postMessage({ documentKey });
  }

  #publishNamespaceWake(namespaceKey: string): void {
    this.#notifyNamespace(namespaceKey);
    this.#channel.postMessage({ namespaceKey });
  }

  #onPeerWake(message: AnnotationWakeMessage): void {
    if ("documentKey" in message) this.#notifyDocument(message.documentKey);
    else this.#notifyNamespace(message.namespaceKey);
  }

  // Observational storage-success boundary: called only AFTER an authority
  // transaction completes, outside every transaction. An observer fault cannot
  // change the committed result.
  #notifyStorageSuccess(): void {
    try {
      this.#options.onStorageSuccess?.();
    } catch {
      // Observational only.
    }
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
    this.#issuedReads.set(ticket.requestId, { documentKey, readGeneration: issuedGeneration });
    // Remember the completed current ticket in the same synchronous continuation,
    // carrying the prior completed accepted source for the full document key.
    this.#readStates.set(documentKey, { ticket, accepted: this.#readStates.get(documentKey)?.accepted });
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
      const changed = compareDisplayedSources(
        {
          source: previous.content.text,
          evidence: {
            wholeRevision: previous.content.revision,
            fingerprint: displayedFingerprint(previous.content.text, previous.content.truncated),
          },
        },
        {
          source: content.text,
          evidence: {
            wholeRevision: content.revision,
            fingerprint: displayedFingerprint(content.text, content.truncated),
          },
        },
      ).changed;
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
    const finishValidation = (current: boolean): void => {
      if (!provisional) return;
      const entry = this.#suppressedEpochs.get(provisional);
      if (entry?.requestId !== ticket.requestId) return;
      if (current) entry.confirmed = true;
      else if (!entry.confirmed) this.#suppressedEpochs.delete(provisional);
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
        // Compare against the epoch's RETAINED comparable baseline, not the
        // head's current-display evidence: an inconclusive compatible read may
        // advance the display without erasing the evidence that detects a later
        // definite change. A head opened by beginRead has no baseline row yet.
        let baseline: AnnotationEvidenceRow | undefined;
        if (head) {
          baseline = await requestResult<AnnotationEvidenceRow | undefined>(
            transaction.objectStore(ANNOTATION_EVIDENCE_STORE).get(annotationBaselineEvidenceKey(head.epoch)),
          );
        }
        const contentChanged =
          baseline === undefined
            ? false
            : compareDisplayedSources(
                {
                  source: baseline.content.text,
                  evidence: {
                    wholeRevision: baseline.content.revision,
                    fingerprint: displayedFingerprint(baseline.content.text, baseline.content.truncated),
                  },
                },
                {
                  source: content.text,
                  evidence: {
                    wholeRevision: content.revision,
                    fingerprint: displayedFingerprint(content.text, content.truncated),
                  },
                },
              ).changed;
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
        if (baseline === undefined) {
          // Establish this epoch's retained comparable baseline under a
          // deterministic epoch key. It also covers a head opened by beginRead
          // that has no evidence row yet.
          await requestResult(
            transaction.objectStore(ANNOTATION_EVIDENCE_STORE).put({
              evidenceKey: annotationBaselineEvidenceKey(epoch),
              documentKey,
              epoch,
              content,
            } satisfies AnnotationEvidenceRow),
          );
        }
        await requestResult(
          transaction
            .objectStore(ANNOTATION_EVIDENCE_STORE)
            .put({ evidenceKey, documentKey, epoch, content } satisfies AnnotationEvidenceRow),
        );
        await requestResult(transaction.objectStore(ANNOTATION_HEADS_STORE).put(nextHead));
        this.#options.beforeCommit?.("acceptRead");
        return {
          kind: "accepted",
          lease: {
            key: ticket.key,
            namespaceIncarnation: namespaceRow.incarnation,
            epoch,
            headRevision: nextHead.headRevision,
            displayedSnapshotId: nextHead.displayedSnapshotId,
          } satisfies AcceptedSourceLease,
        } as const;
      });
      if (validation.kind === "stale") {
        finishValidation(false);
        return { state: "stale-ticket" };
      }
      if (validation.kind === "accepted") {
        // An unchanged acceptance is not a changed read: drop any provisional the
        // local comparison installed and record the completed acceptance.
        finishValidation(false);
        this.#readStates.set(documentKey, { ticket, accepted: { lease: validation.lease, content } });
        return { state: "accepted", lease: validation.lease };
      }
      // A current changed read: confirm suppression BEFORE scheduling its fence,
      // then run the outside-transaction observation boundary.
      finishValidation(true);
      await this.#options.beforeExpiryFence?.(ticket);
      try {
        const fenced = await this.#fence(ticket.key, validation.namespaceIncarnation, validation.head.epoch, content);
        this.#readStates.set(documentKey, { ticket, accepted: { lease: fenced.lease, content } });
        return { state: "expiry-pending", receipt: fenced.receipt };
      } catch (error) {
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
        return { state: "expiry-pending" };
      }
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
      // The replacement epoch also needs its own retained comparable baseline.
      await requestResult(
        transaction.objectStore(ANNOTATION_EVIDENCE_STORE).put({
          evidenceKey: annotationBaselineEvidenceKey(replacementEpoch),
          documentKey,
          epoch: replacementEpoch,
          content,
        } satisfies AnnotationEvidenceRow),
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

  async saveAnnotation(operation: PreparedAnnotation): Promise<SaveReceipt> {
    // Capture the caller's attempt synchronously, before any await, so later
    // caller mutations cannot change what is validated or persisted.
    const captured = structuredClone(operation);
    const namespace = captured.lease.key.namespace;
    if (namespace.kind !== "browser") throw new Error("browser annotations require a browser namespace");
    const operationKey = annotationOperationKey(namespace, captured.operationId);
    const documentKey = annotationDocumentKey(captured.lease.key);
    const sessionKey = serializeSessionKey({ namespace, sessionRef: captured.lease.key.sessionRef });
    let newlySaved = false;
    const receipt = await this.#transaction(SAVE_STORES, "readwrite", async (transaction) => {
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
      if (
        !head ||
        head.namespaceIncarnation !== namespaceRow.incarnation ||
        head.epoch !== captured.lease.epoch ||
        head.headRevision !== captured.lease.headRevision
      ) {
        throw new Error("accepted lease does not match the durable head");
      }
      const evidence = await requestResult<AnnotationEvidenceRow | undefined>(
        transaction.objectStore(ANNOTATION_EVIDENCE_STORE).get(head.evidenceKey),
      );
      if (!evidence || !evidenceMatches(evidence.content, captured.capture.evidence)) {
        throw new Error("accepted source evidence does not match the durable evidence");
      }
      const sequence = await requestResult<AnnotationSequenceRow | undefined>(
        transaction.objectStore(ANNOTATION_SEQUENCES_STORE).get(sessionKey),
      );
      const sessionSequence = (sequence?.lastSequence ?? 0) + 1;
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
    });
    this.#notifyStorageSuccess();
    // A duplicate save wrote nothing, so only a newly persisted operation wakes readers.
    if (newlySaved) this.#publishDocumentWake(documentKey);
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
          requestResult<AnnotationPayloadRow[]>(transaction.objectStore(ANNOTATION_PAYLOADS_STORE).getAll()),
          requestResult<AnnotationEvidenceRow[]>(transaction.objectStore(ANNOTATION_EVIDENCE_STORE).getAll()),
        ]);
        for (const payload of payloads) {
          if (payload.documentKey === documentKey && payload.epoch === receipt.expiredEpoch) {
            await requestResult(transaction.objectStore(ANNOTATION_PAYLOADS_STORE).delete(payload.payloadKey));
          }
        }
        for (const row of evidence) {
          if (row.documentKey === documentKey && row.epoch === receipt.expiredEpoch) {
            await requestResult(transaction.objectStore(ANNOTATION_EVIDENCE_STORE).delete(row.evidenceKey));
          }
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

  // Complete retained pending expiry fences first, then visit persisted pending
  // handoffs once in session save order. A pass that cannot complete a fence
  // rejects rather than reporting recovery. Other sessions and namespaces never
  // block one session's FIFO; terminal receipts and duplicate handling stay in
  // the real handoff path, so an applied operation is never recaptured here.
  async retryPending(reason: AnnotationRetryReason): Promise<void> {
    void reason;
    for (const [identity, record] of [...this.#pendingExpiries]) {
      try {
        await this.#fence(record.ticketKey, record.namespaceIncarnation, record.expiredEpoch, record.content);
        this.#pendingExpiries.delete(identity);
      } catch (error) {
        if (error instanceof SupersededFenceError) {
          // Another fence already replaced this epoch: the retained work is done.
          this.#pendingExpiries.delete(identity);
          continue;
        }
        throw error;
      }
    }
    await this.#retryPersistedPending();
  }

  // Read the persisted pending operations once, then hand each off using its
  // ORIGINAL full DocumentKey and operation ID retained in its payload. Sorted by
  // session then session sequence, so a session's earlier operation is retried
  // before its later one while a session whose owner is unavailable stays pending
  // without holding up another session.
  async #retryPersistedPending(): Promise<void> {
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
      await this.handoff(payload.operation.lease.key, payload.operation.operationId);
    }
  }

  // Browser-only outcome boundary: the underlying real pass runs first, then a
  // completed read-only snapshot of the persisted operation receipts reports
  // whether any handoff is still pending. The read-only observation never emits
  // onStorageSuccess; the underlying pass keeps its own producer boundaries.
  retryPendingWithOutcome(reason: AnnotationRetryReason): Promise<BrowserAnnotationRetryOutcome> {
    return retryBrowserAnnotationsWithOutcome(this, this.#connection, reason);
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
    for (const [documentKey, state] of this.#readStates) {
      if (documentKeyNamespace(documentKey) !== namespaceKey) continue;
      const accepted = state.accepted;
      if (accepted && accepted.lease.namespaceIncarnation === expectedIncarnation) {
        this.#suppressedEpochs.set(epochIdentity(accepted.lease), {
          requestId: state.ticket.requestId,
          confirmed: true,
        });
      }
    }
    const newIncarnation = createSecureUUID();
    const result = await this.#transaction(
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
        const heads = await requestResult<AnnotationHeadRow[]>(
          transaction.objectStore(ANNOTATION_HEADS_STORE).getAll(),
        );
        for (const row of heads) {
          if (documentKeyNamespace(row.documentKey) === namespaceKey) {
            await requestResult(transaction.objectStore(ANNOTATION_HEADS_STORE).delete(row.documentKey));
          }
        }
        const evidence = await requestResult<AnnotationEvidenceRow[]>(
          transaction.objectStore(ANNOTATION_EVIDENCE_STORE).getAll(),
        );
        for (const row of evidence) {
          if (documentKeyNamespace(row.documentKey) === namespaceKey) {
            await requestResult(transaction.objectStore(ANNOTATION_EVIDENCE_STORE).delete(row.evidenceKey));
          }
        }
        const payloads = await requestResult<AnnotationPayloadRow[]>(
          transaction.objectStore(ANNOTATION_PAYLOADS_STORE).getAll(),
        );
        for (const row of payloads) {
          if (documentKeyNamespace(row.documentKey) === namespaceKey) {
            await requestResult(transaction.objectStore(ANNOTATION_PAYLOADS_STORE).delete(row.payloadKey));
          }
        }
        const operations = await requestResult<AnnotationOperationRow[]>(
          transaction.objectStore(ANNOTATION_OPERATIONS_STORE).getAll(),
        );
        for (const row of operations) {
          if (row.namespaceKey === namespaceKey) {
            await requestResult(transaction.objectStore(ANNOTATION_OPERATIONS_STORE).delete(row.operationKey));
          }
        }
        const sequences = await requestResult<AnnotationSequenceRow[]>(
          transaction.objectStore(ANNOTATION_SEQUENCES_STORE).getAll(),
        );
        for (const row of sequences) {
          if (documentKeyNamespace(row.documentKey) === namespaceKey) {
            await requestResult(transaction.objectStore(ANNOTATION_SEQUENCES_STORE).delete(row.documentKey));
          }
        }
        const expiries = await requestResult<AnnotationExpiryRow[]>(
          transaction.objectStore(ANNOTATION_EXPIRY_STORE).getAll(),
        );
        for (const row of expiries) {
          if (annotationNamespaceKey(row.receipt.key.namespace) === namespaceKey) {
            await requestResult(transaction.objectStore(ANNOTATION_EXPIRY_STORE).delete(row.expiryKey));
          }
        }
        await requestResult(
          transaction
            .objectStore(ANNOTATION_NAMESPACES_STORE)
            .put({ namespaceKey, incarnation: newIncarnation } satisfies NamespaceRow),
        );
        this.#options.beforeCommit?.("reset");
        return { committed: true, incarnation: newIncarnation };
      },
    );
    if (result.committed) {
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
    }
    return result;
  }

  async handoff(key: DocumentKey, operationId: string): Promise<HandoffResult> {
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
      return { state: "pending", reason: "owner-unavailable" };
    }
    if (!receipt) return { state: "cancelled" };
    if (receipt.state === "applied") return { state: "already-applied", draftRevision: receipt.draftRevision };
    if (receipt.state === "cancelled") return { state: "cancelled" };
    let payload: AnnotationPayloadRow | undefined;
    try {
      payload = await this.#readPayload(receipt.payloadKey);
    } catch {
      return { state: "pending", reason: "owner-unavailable" };
    }
    if (!payload) return { state: "cancelled" };
    // The immutable ORIGINAL payload lease owns suppression eligibility for this
    // operation; any suppression of that old epoch (provisional or confirmed)
    // cancels it, and the capture is never refreshed.
    const originalLease = structuredClone(payload.operation.lease);
    const documentStillEligible = () => !this.#suppressedEpochs.has(epochIdentity(originalLease));
    if (!documentStillEligible()) return { state: "cancelled" };
    const source = this.#options.targetFor(payload.operation.destination, key);
    if (!source) return { state: "pending", reason: "owner-unavailable" };
    let preparation: ComposerPreparation | null;
    try {
      preparation = await source.prepareAnnotation();
    } catch {
      return { state: "pending", reason: "owner-unavailable" };
    }
    if (!preparation) return { state: "pending", reason: "owner-unavailable" };
    if (!documentStillEligible()) return { state: "cancelled" };
    let result: AppendAnnotationResult;
    try {
      result = await this.#options.drafts.appendAnnotation({
        operationKey,
        document: key,
        preparation,
        sourceId: source.sourceId,
        stillOwnsEdit: () => source.ownsPreparation(preparation),
        documentStillEligible,
        onAppendReadsIssued: this.#options.onAppendReadsIssued,
        beforeFinalGuard: this.#options.beforeFinalGuard,
        beforeAbortableCompletion: this.#options.beforeAbortableCompletion,
      });
    } catch {
      // The append runner has already settled rollback; a failed transaction is
      // the documented recoverable pending outcome, never a thrown error. The
      // committed path below keeps its own observer boundaries.
      return { state: "pending", reason: "owner-unavailable" };
    }
    if (result.state === "applied") {
      try {
        this.#options.afterCommit?.();
      } catch {
        // An observer cannot relabel a committed append.
      }
      try {
        source.acknowledgeAnnotation(preparation, result.token, result.value);
      } catch {
        // An acknowledgement fault cannot relabel a committed append.
      }
      try {
        this.#options.onStorageSuccess?.();
      } catch {
        // Observational only.
      }
      // The append committed; wake matching readers (and peers) after commit.
      this.#publishDocumentWake(annotationDocumentKey(key));
      return { state: "applied", draftRevision: result.draftRevision };
    }
    return result;
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
  ): Promise<T> {
    const database = await this.#connection.open();
    const transaction = database.transaction([...stores], mode);
    const completed = transactionCompletion(transaction);
    void completed.catch(() => undefined);
    try {
      const result = await body(transaction);
      await completed;
      return result;
    } catch (error) {
      tryAbortTransaction(transaction);
      await completed.catch(() => undefined);
      throw error;
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
export async function retryBrowserAnnotationsWithOutcome(
  repository: DocumentAnnotationRepository,
  connection: IDBConnection,
  reason: AnnotationRetryReason,
): Promise<BrowserAnnotationRetryOutcome> {
  await repository.retryPending(reason);
  const database = await connection.open();
  const transaction = database.transaction(ANNOTATION_OPERATIONS_STORE, "readonly");
  const completed = transactionCompletion(transaction);
  const [operations] = await Promise.all([
    requestResult<AnnotationOperationRow[]>(transaction.objectStore(ANNOTATION_OPERATIONS_STORE).getAll()),
    completed,
  ]);
  return { pending: operations.some((operation) => operation.state === "pending") };
}
