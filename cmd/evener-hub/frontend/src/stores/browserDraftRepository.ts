import type { ComposerMention, DocumentKey, HandoffResult } from "@evener/appwire-client";
import { buildInput, formatAnnotationReference } from "@evener/appwire-client";
import type {
  DraftBackingIdentity,
  DraftSessionToken,
  DraftSettlement,
  DraftSettlementGuard,
  SubmittedDraft,
} from "@evener/appwire-client/state/mutation";
import type { ComposerPreparation } from "../panes/session/composer/composerContract";
import {
  type ComposerDraft,
  composerDraftStorageKey,
  decodeStructuredComposerDraft,
  decodeSupportedComposerDraft,
  draftStorageKey,
  isBlankDraft,
} from "../panes/session/composer/draft";
import { recoveryComposerDraft } from "../panes/session/composer/recovery/recoveryDraft";
import { appendLiteralReference, type SkillEditorValue } from "../panes/session/composer/skillDocument";
import {
  type NamespaceRow,
  nextSessionRow,
  putSessionRow,
  readNamespaceRow,
  readSessionRow,
  type SessionRow,
  sameSessionToken,
  serializeNamespace,
  serializeSessionKey,
} from "./compositionRevision";
import { IDBConnection, IndexedDBTransactionTimeoutError, requestResult, runBoundedTransaction } from "./idbConnection";
import {
  ANNOTATION_EVIDENCE_STORE,
  ANNOTATION_EXPIRY_STORE,
  ANNOTATION_HEADS_STORE,
  ANNOTATION_NAMESPACES_STORE,
  ANNOTATION_OPERATIONS_STORE,
  ANNOTATION_PAYLOADS_STORE,
  ANNOTATION_SEQUENCES_STORE,
  type AnnotationExpiryRow,
  type AnnotationHeadRow,
  type AnnotationOperationRow,
  type AnnotationPayloadRow,
  annotationDocumentKey,
  annotationExpiryKey,
  COMPOSER_DRAFTS_STORE,
  DRAFT_TRANSFERS_STORE,
  LOCAL_COMPOSITION_DATABASE_NAME,
  LOCAL_COMPOSITION_DATABASE_VERSION,
  MUTATION_RECOVERY_STORE,
  registerLocalCompositionSchema,
  sameHandoffDestination,
  terminalOperationRow,
} from "./localCompositionSchema";
import type { MutationRecoveryRecord } from "./mutationOutbox";
import { applyRecoveryInput } from "./recoveryInput";
import { createSecureUUID } from "./secureUUID";

// Owner-free browser draft repository. A durable session row carries one
// monotonic `sessionRevision` and the namespace incarnation it was written
// under; there is no owner, claim, adopt or losing-draft archive. Explicit
// writes (seed, edit, clear) are compared against both the stored row and the
// caller's expected token before they are authorized.
export interface BrowserDraftKey {
  namespace: { kind: "browser"; origin: string };
  sessionRef: string;
}

export interface StoredComposerDraft extends SkillEditorValue {
  key: BrowserDraftKey;
  token: DraftSessionToken;
  // Wall-clock stamp of the last genuine draft write (seed, edit or carry), so
  // drafts under two different refs can be ordered when one is carried to the
  // other. Absent on rows an older build wrote.
  editedAt?: number;
}

export type DraftCommit =
  | { state: "committed"; draft: StoredComposerDraft }
  | { state: "conflict"; current: StoredComposerDraft }
  | { state: "unavailable" };

export interface BrowserDraftRepository {
  load(key: BrowserDraftKey): Promise<StoredComposerDraft | null>;
  seedDraft(key: BrowserDraftKey, value: SkillEditorValue): Promise<DraftCommit>;
  commitEdit(
    key: BrowserDraftKey,
    expected: DraftSessionToken,
    value: SkillEditorValue,
    stillAuthorized: () => boolean,
  ): Promise<DraftCommit>;
  clearSubmitted(key: BrowserDraftKey, submitted: SubmittedDraft): Promise<DraftSettlement>;
  appendAnnotation(input: AppendAnnotationInput): Promise<AppendAnnotationResult>;
  readAppendBacking(preparation: ComposerPreparation): Promise<CommittedAppendBacking | null>;
  // The row and its selected recovery record as ONE committed capture: reading
  // the record in a second transaction would let a peer recovery edit commit
  // between the reads, pairing its newer record content with the row's stale
  // token, which no later persistence can validate. Null when either half is
  // gone, or when the record was retargeted to another session - a retarget
  // preserves the id while moving the content, so the id alone does not prove
  // the pair belongs to this key. The row keeps load's lazy re-token contract.
  readRecoveryPair(key: BrowserDraftKey, recoveryId: string): Promise<RecoveryPairCapture | null>;
  // "committed": this transfer imported what it could and recorded its markers
  // (or had nothing to inspect). "pending": the durable transfer write itself
  // faulted; the caller retries. "unavailable": the legacy scan faulted - this
  // session can read no legacy draft, so no caller can order against the legacy
  // store, and proceeds exactly as with none.
  transferSupportedDrafts(
    namespace: BrowserDraftKey["namespace"],
  ): Promise<{ state: "committed" | "pending" | "unavailable" }>;
  carryDraft(from: BrowserDraftKey, to: BrowserDraftKey): Promise<void>;
  subscribe(listener: (key: BrowserDraftKey) => void): () => void;
  close(): void;
}

type BrowserDraftOperation = "seedDraft" | "commitEdit" | "clearSubmitted" | "transferSupportedDrafts" | "carryDraft";

export type BrowserDraftOpenDiagnosticPath =
  | "open-timeout"
  | "open-blocked"
  | "upgrade-abandoned"
  | "versionchange-retire";

export interface BrowserDraftOpenDiagnostic {
  database: string;
  version: number;
  path: BrowserDraftOpenDiagnosticPath;
  versionchangeTransaction: boolean;
}

export interface BrowserDraftListenerDiagnostic {
  path: "listener-failed";
  key: BrowserDraftKey;
  error: unknown;
}

export interface BrowserDraftRepositoryOptions {
  indexedDB?: IDBFactory;
  databaseName?: string;
  storage?: Storage;
  beforeCommit?: (operation: BrowserDraftOperation) => void;
  beforeLegacyCleanup?: () => void;
  onOpenDiagnostic?: (diagnostic: BrowserDraftOpenDiagnostic) => void;
  onListenerDiagnostic?: (diagnostic: BrowserDraftListenerDiagnostic) => void;
}

interface DraftTransferRow {
  serializedKey: string;
  namespaceKey: string;
  legacyKey: string;
  inspectedBytes: string | null;
}

interface InspectedDraft {
  key: BrowserDraftKey;
  draftKey: string;
  legacyKeyV1: string;
  legacyKeyV2: string;
  legacyV1: string | null;
  structuredV2: string | null;
  // Which legacy representation the decoded value came from. The import carries
  // exactly this one's content, so only its bytes are removable.
  carriedFromV2: boolean;
  value: ComposerDraft;
}

const STORAGE_WAIT_MS = 10_000;
const DRAFT_STORES = [COMPOSER_DRAFTS_STORE, ANNOTATION_NAMESPACES_STORE] as const;
const TRANSFER_STORES = [COMPOSER_DRAFTS_STORE, DRAFT_TRANSFERS_STORE, ANNOTATION_NAMESPACES_STORE] as const;
// The one atomic append scope: the ordinary session row, the recovery record,
// and every annotation store the append resolves or writes.
export const APPEND_STORES = [
  COMPOSER_DRAFTS_STORE,
  MUTATION_RECOVERY_STORE,
  ANNOTATION_OPERATIONS_STORE,
  ANNOTATION_HEADS_STORE,
  ANNOTATION_EVIDENCE_STORE,
  ANNOTATION_PAYLOADS_STORE,
  ANNOTATION_SEQUENCES_STORE,
  ANNOTATION_EXPIRY_STORE,
  ANNOTATION_NAMESPACES_STORE,
] as const;

export interface AppendAnnotationInput {
  operationKey: string;
  document: DocumentKey;
  preparation: ComposerPreparation;
  sourceId: string;
  stillOwnsEdit: () => boolean;
  documentStillEligible: () => boolean;
  // True when the candidate's epoch is confirmed-expired: the source change
  // was proven and the epoch suppressed, but the expiry's fence write has not
  // completed, so the candidate's durable row is still pending while it can
  // never append. Such a predecessor must not block a healthy successor's
  // FIFO. Absent when the caller holds no expiry knowledge.
  epochConfirmedExpired?: (candidate: { documentKey: string; namespaceIncarnation: string; epoch: string }) => boolean;
  onAppendReadsIssued?: () => void;
}

export type AppendAnnotationResult =
  | Exclude<HandoffResult, { state: "applied" }>
  | {
      state: "applied";
      draftRevision: number;
      token: DraftSessionToken;
      value: SkillEditorValue;
    };

// The committed backing an append landed on: the ordinary session row's
// current token with the backing's committed value (the row itself for a
// draft backing, the recovery record's composer draft for a recovery
// backing). Reconciling an append whose completion went unobserved reads
// this instead of guessing; the token and value carry exactly what an
// observed applied result would have acknowledged.
export interface CommittedAppendBacking {
  token: DraftSessionToken;
  value: SkillEditorValue;
}

export interface RecoveryPairCapture {
  row: StoredComposerDraft;
  record: MutationRecoveryRecord;
}

export function browserDraftKey(sessionRef: string, origin = location.origin): BrowserDraftKey {
  return { namespace: { kind: "browser", origin }, sessionRef };
}

export function captureDraftSettlementGuard(
  row: StoredComposerDraft,
  backing: DraftBackingIdentity,
  localGeneration: number,
): DraftSettlementGuard {
  return Object.freeze({
    token: Object.freeze({ ...row.token }),
    backing: Object.freeze({ ...backing }),
    localGeneration,
  });
}

function cloneKey(key: BrowserDraftKey): BrowserDraftKey {
  return { namespace: { kind: "browser", origin: key.namespace.origin }, sessionRef: key.sessionRef };
}

function cloneValue(value: SkillEditorValue): SkillEditorValue {
  return {
    text: value.text,
    skillNames: [...value.skillNames],
    ...(value.commandNames === undefined ? {} : { commandNames: [...value.commandNames] }),
    ...(value.mentions === undefined ? {} : { mentions: value.mentions.map((mention) => ({ ...mention })) }),
  };
}

function publicSessionRow(row: SessionRow): StoredComposerDraft {
  return {
    ...cloneValue(row),
    key: cloneKey(row.key),
    token: { ...row.token },
    ...(row.editedAt === undefined ? {} : { editedAt: row.editedAt }),
  };
}

function newSessionRow(key: BrowserDraftKey, value: SkillEditorValue, incarnation: string): SessionRow {
  return {
    serializedKey: serializeSessionKey(key),
    key: cloneKey(key),
    ...cloneValue(value),
    token: { namespaceIncarnation: incarnation, sessionRevision: 1 },
    editedAt: Date.now(),
  };
}

function sameStrings(left: readonly string[] | undefined, right: readonly string[] | undefined): boolean {
  const a = left ?? [];
  const b = right ?? [];
  return a.length === b.length && a.every((value, index) => value === b[index]);
}

// Absent and explicit-empty mentions are distinct editor values; a mismatch on
// either side refuses. Elements compare by kind/name/offset.
function sameMentions(
  left: readonly ComposerMention[] | undefined,
  right: readonly ComposerMention[] | undefined,
): boolean {
  if (left === undefined || right === undefined) return left === right;
  return (
    left.length === right.length &&
    left.every((mention, index) => {
      const other = right[index];
      return (
        other !== undefined &&
        mention.kind === other.kind &&
        mention.name === other.name &&
        mention.offset === other.offset
      );
    })
  );
}

// The full structured-value comparison two editor values share: text, skills,
// commands and mentions compared field-for-field, with absent and explicit-empty
// mentions kept distinct. Exported so the outbox can reuse it for guarded
// ordinary consumption instead of duplicating the field rules.
export function sameComposerValue(
  left: {
    text: string;
    skillNames: readonly string[];
    commandNames?: readonly string[];
    mentions?: readonly ComposerMention[];
  },
  right: {
    text: string;
    skillNames: readonly string[];
    commandNames?: readonly string[];
    mentions?: readonly ComposerMention[];
  },
): boolean {
  return (
    left.text === right.text &&
    sameStrings(left.skillNames, right.skillNames) &&
    sameStrings(left.commandNames, right.commandNames) &&
    sameMentions(left.mentions, right.mentions)
  );
}

function serializedTransferKey(namespace: BrowserDraftKey["namespace"], legacyKey: string): string {
  return JSON.stringify([namespace.kind, namespace.origin, legacyKey]);
}

class BrowserDraftRepositoryImpl implements BrowserDraftRepository {
  readonly #connection: IDBConnection;
  readonly #databaseName: string;
  readonly #storage: Storage | undefined;
  readonly #beforeCommit: ((operation: BrowserDraftOperation) => void) | undefined;
  readonly #beforeLegacyCleanup: (() => void) | undefined;
  readonly #onListenerDiagnostic: (diagnostic: BrowserDraftListenerDiagnostic) => void;
  readonly #listeners = new Set<(key: BrowserDraftKey) => void>();

  constructor(options: BrowserDraftRepositoryOptions) {
    const indexedDB = options.indexedDB ?? globalThis.indexedDB;
    if (!indexedDB) throw new Error("IndexedDB is unavailable");
    // Legacy localStorage is best-effort migration input, never a requirement:
    // a durable repository must stay fully usable when the legacy store is
    // absent or its accessor faults, and its migration scan then reports
    // unavailable. Acquire it guarded so a throwing getter cannot fail
    // construction before any durable operation.
    let storage = options.storage;
    if (storage === undefined) {
      try {
        storage = globalThis.localStorage;
      } catch {
        storage = undefined;
      }
    }
    this.#databaseName = options.databaseName ?? LOCAL_COMPOSITION_DATABASE_NAME;
    this.#storage = storage;
    this.#beforeCommit = options.beforeCommit;
    this.#beforeLegacyCleanup = options.beforeLegacyCleanup;
    this.#onListenerDiagnostic = options.onListenerDiagnostic ?? (() => undefined);
    const report = options.onOpenDiagnostic ?? (() => undefined);
    this.#connection = new IDBConnection({
      indexedDB,
      databaseName: this.#databaseName,
      databaseVersion: LOCAL_COMPOSITION_DATABASE_VERSION,
      waitMs: STORAGE_WAIT_MS,
      upgrade: registerLocalCompositionSchema,
      errors: {
        open: () => new Error("Unable to open browser draft repository"),
        superseded: () => new Error("Browser draft repository connection was closed"),
        timeout: () => new Error("Browser draft repository open timed out"),
        blocked: () => new Error("Browser draft repository upgrade is blocked"),
      },
      reportDiagnostic: (path, versionchangeTransaction) => {
        try {
          report({
            database: this.#databaseName,
            version: LOCAL_COMPOSITION_DATABASE_VERSION,
            path,
            versionchangeTransaction,
          });
        } catch {
          // Diagnostics are observational and cannot change storage outcomes.
        }
      },
    });
  }

  close(): void {
    this.#connection.close();
  }

  subscribe(listener: (key: BrowserDraftKey) => void): () => void {
    this.#listeners.add(listener);
    return () => this.#listeners.delete(listener);
  }

  // The row with load's lazy re-token contract: a row whose namespace rotated
  // incarnations adopts the current incarnation, preserving the session
  // revision and the complete ordinary value, and records the re-token in
  // `changed` for the caller to publish.
  async #readRowWithRetoken(
    transaction: IDBTransaction,
    key: BrowserDraftKey,
    changed: BrowserDraftKey[],
  ): Promise<StoredComposerDraft | null> {
    const row = await readSessionRow(transaction, key);
    if (!row) return null;
    const namespace = await readNamespaceRow(transaction, key.namespace);
    const incarnation = namespace?.incarnation;
    if (incarnation === undefined || row.token.namespaceIncarnation === incarnation) return publicSessionRow(row);
    const refreshed: SessionRow = { ...row, token: { ...row.token, namespaceIncarnation: incarnation } };
    const write = putSessionRow(transaction, refreshed);
    changed.push(key);
    await requestResult(write);
    return publicSessionRow(refreshed);
  }

  async load(key: BrowserDraftKey): Promise<StoredComposerDraft | null> {
    const changed: BrowserDraftKey[] = [];
    return this.#writeAndPublish(DRAFT_STORES, undefined, changed, (transaction) =>
      this.#readRowWithRetoken(transaction, key, changed),
    );
  }

  async readRecoveryPair(key: BrowserDraftKey, recoveryId: string): Promise<RecoveryPairCapture | null> {
    const changed: BrowserDraftKey[] = [];
    return this.#writeAndPublish(
      [COMPOSER_DRAFTS_STORE, ANNOTATION_NAMESPACES_STORE, MUTATION_RECOVERY_STORE],
      undefined,
      changed,
      async (transaction) => {
        const row = await this.#readRowWithRetoken(transaction, key, changed);
        if (!row) return null;
        const record = await requestResult<MutationRecoveryRecord | undefined>(
          transaction.objectStore(MUTATION_RECOVERY_STORE).get(recoveryId),
        );
        // A retarget preserves the recovery id while moving the row to another
        // session, so pairing by id alone could pair this key's token with a
        // foreign session's text and attachments.
        if (!record || record.targetRef !== key.sessionRef) return null;
        return { row, record };
      },
    );
  }

  async seedDraft(key: BrowserDraftKey, value: SkillEditorValue): Promise<DraftCommit> {
    try {
      const changed: BrowserDraftKey[] = [];
      return await this.#writeAndPublish(DRAFT_STORES, "seedDraft", changed, async (transaction) => {
        const existing = await readSessionRow(transaction, key);
        if (existing) return { state: "conflict", current: publicSessionRow(existing) } as const;
        const incarnation = await this.#currentIncarnation(transaction, key.namespace);
        const row = newSessionRow(key, value, incarnation);
        const write = transaction.objectStore(COMPOSER_DRAFTS_STORE).add(row);
        changed.push(key);
        await requestResult(write);
        return { state: "committed", draft: publicSessionRow(row) } as const;
      });
    } catch {
      return { state: "unavailable" };
    }
  }

  async commitEdit(
    key: BrowserDraftKey,
    expected: DraftSessionToken,
    value: SkillEditorValue,
    stillAuthorized: () => boolean,
  ): Promise<DraftCommit> {
    try {
      const changed: BrowserDraftKey[] = [];
      return await this.#writeAndPublish(DRAFT_STORES, "commitEdit", changed, async (transaction) => {
        const incarnation = await this.#currentIncarnation(transaction, key.namespace);
        const row = await readSessionRow(transaction, key);
        if (!row) return { state: "unavailable" } as const;
        // Both the stored row's incarnation and the caller's expected incarnation
        // must match the current namespace; neither predicate may mask the other.
        if (
          expected.namespaceIncarnation !== incarnation ||
          row.token.namespaceIncarnation !== incarnation ||
          expected.sessionRevision !== row.token.sessionRevision
        ) {
          return { state: "conflict", current: publicSessionRow(row) } as const;
        }
        if (!stillAuthorized()) return { state: "unavailable" } as const;
        // The one choke point every explicit edit goes through, so the stamp
        // is the draft's actual last-write time.
        const next = { ...nextSessionRow(row, value), editedAt: Date.now() };
        const write = putSessionRow(transaction, next);
        changed.push(key);
        await requestResult(write);
        return { state: "committed", draft: publicSessionRow(next) } as const;
      });
    } catch {
      return { state: "unavailable" };
    }
  }

  async clearSubmitted(key: BrowserDraftKey, submitted: SubmittedDraft): Promise<DraftSettlement> {
    let draftUnchanged = false;
    try {
      const changed: BrowserDraftKey[] = [];
      return await this.#writeAndPublish(DRAFT_STORES, "clearSubmitted", changed, async (transaction) => {
        const row = await readSessionRow(transaction, key);
        if (!row) return { cleared: false, draftUnchanged: false, persistence: "committed" } as const;
        const incarnation = await this.#currentIncarnation(transaction, key.namespace);
        const guard = submitted.settlementGuard;
        // The captured token and backing fence the durable row; live authority
        // comes only from the actual stillOwnsEdit callback, never from two
        // captured numeric fields.
        draftUnchanged =
          guard !== undefined &&
          guard.backing.kind === "draft" &&
          row.token.namespaceIncarnation === incarnation &&
          sameSessionToken(row.token, guard.token);
        if (!draftUnchanged || !submitted.stillOwnsEdit?.()) {
          return { cleared: false, draftUnchanged, persistence: "committed" } as const;
        }
        if (!sameComposerValue(row, submitted)) {
          return { cleared: false, draftUnchanged, persistence: "committed" } as const;
        }
        // A cleared row is blank, and a blank draft carries no edit time: the
        // resume-carry ordering stamp belongs to text that exists.
        const cleared = nextSessionRow(row, { text: "", skillNames: [] });
        delete cleared.editedAt;
        const write = putSessionRow(transaction, cleared);
        changed.push(key);
        await requestResult(write);
        return { cleared: true, draftUnchanged: true, persistence: "committed" } as const;
      });
    } catch {
      return { cleared: false, draftUnchanged, persistence: "pending" };
    }
  }

  // A store-driven resume can return a different identity, and the pane
  // follows it. The draft the old ref's composer committed lives under the
  // OLD key, while the incoming pane's composer starts on the NEW key's own
  // draft - so without a carry the old ref's draft would be dropped at the
  // identity change. The carry preserves whichever draft is genuinely NEWER
  // by its wall-clock write stamp: a blank or absent destination always takes
  // the source; otherwise the source wins only when strictly newer, so ties
  // and two unstamped rows (written before stamps existed) keep the
  // destination, conservatively. The source copy stays under the old key
  // either way; a storage fault leaves both rows exactly where they were.
  async carryDraft(from: BrowserDraftKey, to: BrowserDraftKey): Promise<void> {
    if (serializeSessionKey(from) === serializeSessionKey(to)) return;
    try {
      const changed: BrowserDraftKey[] = [];
      await this.#writeAndPublish(DRAFT_STORES, "carryDraft", changed, async (transaction) => {
        const source = await readSessionRow(transaction, from);
        // A blank or absent source never moves.
        if (!source || isBlankDraft(source)) return;
        const destination = await readSessionRow(transaction, to);
        if (destination && !isBlankDraft(destination) && (source.editedAt ?? 0) <= (destination.editedAt ?? 0)) {
          return;
        }
        const carried: SessionRow = destination
          ? { ...nextSessionRow(destination, source), editedAt: Date.now() }
          : newSessionRow(to, source, await this.#currentIncarnation(transaction, to.namespace));
        const write = putSessionRow(transaction, carried);
        changed.push(to);
        await requestResult(write);
      });
    } catch {
      // Best-effort like every draft write: the source copy stays put.
    }
  }

  async appendAnnotation(input: AppendAnnotationInput): Promise<AppendAnnotationResult> {
    const preparation = input.preparation;
    const changed: BrowserDraftKey[] = [];
    return this.#writeAndPublish(
      APPEND_STORES,
      undefined,
      changed,
      async (transaction): Promise<AppendAnnotationResult> => {
        const operations = transaction.objectStore(ANNOTATION_OPERATIONS_STORE);
        const operation = await requestResult<AnnotationOperationRow | undefined>(operations.get(input.operationKey));
        if (!operation) return { state: "cancelled" };
        if (operation.state === "applied") return { state: "already-applied", draftRevision: operation.draftRevision };
        if (operation.state === "cancelled") return { state: "cancelled" };
        // A handoff no composition owner can ever claim settles unclaimed at
        // the annotation store, which preserves the saved annotation and its
        // copyable reference. There is no append to replay here.
        if (operation.state === "unclaimed") return { state: "cancelled" };
        const documentKey = annotationDocumentKey(input.document);
        const reads = Promise.all([
          readNamespaceRow(transaction, preparation.key.namespace),
          readSessionRow(transaction, preparation.key),
          requestResult<AnnotationHeadRow | undefined>(
            transaction.objectStore(ANNOTATION_HEADS_STORE).get(documentKey),
          ),
          requestResult<AnnotationPayloadRow | undefined>(
            transaction.objectStore(ANNOTATION_PAYLOADS_STORE).get(operation.payloadKey),
          ),
          requestResult<AnnotationExpiryRow | undefined>(
            transaction.objectStore(ANNOTATION_EXPIRY_STORE).get(annotationExpiryKey(documentKey, operation.epoch)),
          ),
          requestResult<AnnotationOperationRow[]>(operations.getAll()),
          preparation.backing.kind === "recovery"
            ? requestResult<MutationRecoveryRecord | undefined>(
                transaction.objectStore(MUTATION_RECOVERY_STORE).get(preparation.backing.recoveryId),
              )
            : Promise.resolve(undefined),
        ]);
        // All IDB prerequisites are outstanding; the transaction is untouched by
        // this synchronous observation.
        input.onAppendReadsIssued?.();
        const [namespace, row, head, payload, expiry, allOperations, recovery] = await reads;
        if (
          !namespace ||
          !head ||
          !payload ||
          expiry ||
          operation.documentKey !== documentKey ||
          operation.sessionKey !== serializeSessionKey(preparation.key) ||
          operation.namespaceIncarnation !== namespace.incarnation ||
          head.namespaceIncarnation !== namespace.incarnation ||
          head.epoch !== operation.epoch ||
          payload.epoch !== operation.epoch ||
          input.document.namespace.kind !== "browser" ||
          input.document.namespace.origin !== preparation.key.namespace.origin ||
          input.document.sessionRef !== preparation.key.sessionRef ||
          operation.destination.kind !== "browser-origin" ||
          operation.destination.compositionOwnerId !== input.sourceId
        ) {
          return { state: "cancelled" };
        }
        // Strict FIFO scoped to the captured recipient: only an earlier PENDING
        // handoff to the SAME destination identity blocks this append, so a
        // pending handoff captured for a different composer - or for an
        // ownerless destination no composer can ever claim - never starves it.
        // A confirmed-expired predecessor is no blocker either: its own append
        // can never run, and its still-pending row is only the expiry fence's
        // unfinished write, so it must not hold a healthy successor hostage.
        if (
          allOperations.some(
            (candidate) =>
              candidate.sessionKey === operation.sessionKey &&
              candidate.namespaceIncarnation === namespace.incarnation &&
              candidate.state === "pending" &&
              candidate.sessionSequence < operation.sessionSequence &&
              sameHandoffDestination(candidate.destination, operation.destination) &&
              input.epochConfirmedExpired?.(candidate) !== true,
          )
        ) {
          return { state: "pending", reason: "dirty-draft" };
        }
        if (
          !row ||
          row.token.namespaceIncarnation !== namespace.incarnation ||
          !sameSessionToken(row.token, preparation.token)
        ) {
          return { state: "pending", reason: "revision-conflict" };
        }
        if (
          preparation.backing.kind === "recovery" &&
          (!recovery || recovery.targetRef !== preparation.key.sessionRef)
        ) {
          return { state: "pending", reason: "owner-unavailable" };
        }
        const reference = formatAnnotationReference(
          payload.operation.capture,
          input.document.path,
          payload.operation.note,
        );
        let value: SkillEditorValue;
        let nextRecovery: MutationRecoveryRecord | undefined;
        if (preparation.backing.kind === "recovery" && recovery) {
          const current = recoveryComposerDraft(recovery);
          const next = appendLiteralReference(current, reference);
          nextRecovery = applyRecoveryInput(
            recovery,
            buildInput(
              next.text,
              next.attachments.map((attachment) => ({
                marker: attachment.marker,
                mediaType: attachment.mediaType,
                data: attachment.data ?? "",
                name: attachment.name,
              })),
              next.skillNames,
              next.commandNames,
              next.mentions,
            ),
            recovery.attachments,
            next.text,
            next.mentions,
          );
          value = next;
        } else {
          value = appendLiteralReference(row, reference);
        }
        const appended = nextSessionRow(row, preparation.backing.kind === "draft" ? value : undefined);
        // An ordinary-draft append changes the draft's text, so it stamps its
        // actual edit time like every genuine draft write: the resume carry
        // orders drafts by this stamp, and keeping the pre-annotation stamp
        // would let an older-edited destination drop the new reference. A
        // recovery-backed append leaves the ordinary draft untouched and keeps
        // its stamp.
        const nextRow = preparation.backing.kind === "draft" ? { ...appended, editedAt: Date.now() } : appended;
        // The operation's OWN durable row is the cross-instance fence: a
        // dismissal flips it terminal in its own transaction, so re-read it at
        // guard time and refuse a row that is no longer pending. IndexedDB
        // serializes the write transactions, so a dismissal issued during this
        // append either waits behind it (the in-memory predicate below catches
        // that same-instance case) or was already committed when this read ran.
        const currentOperation = await requestResult<AnnotationOperationRow | undefined>(
          operations.get(input.operationKey),
        );
        if (currentOperation?.state !== "pending") return { state: "cancelled" };
        // Every asynchronous prerequisite and construction precedes this guard.
        if (!input.documentStillEligible()) return { state: "cancelled" };
        if (!input.stillOwnsEdit()) return { state: "pending", reason: "owner-unavailable" };
        const writes: Promise<unknown>[] = [requestResult(putSessionRow(transaction, nextRow))];
        if (nextRecovery) {
          writes.push(requestResult(transaction.objectStore(MUTATION_RECOVERY_STORE).put(nextRecovery)));
        }
        writes.push(
          requestResult(
            operations.put(
              terminalOperationRow(operation, "applied", { draftRevision: nextRow.token.sessionRevision }),
            ),
          ),
        );
        changed.push(preparation.key);
        await Promise.all(writes);
        return {
          state: "applied",
          draftRevision: nextRow.token.sessionRevision,
          token: nextRow.token,
          value,
        };
      },
    );
  }

  // The ordinary session row carries the committed token for BOTH backings: a
  // recovery append advances the row's revision while leaving its content
  // untouched, so the row plus the recovery record reconstruct exactly what
  // the append committed. The row and its backing's record are read in ONE
  // transaction - a peer recovery update committing between two separate
  // reads would leave the reconciliation adopting a token the row has already
  // moved past, and the next persistence under that stale token refuses while
  // the composer and record diverge. No lazy re-token happens here: a rotated
  // incarnation fails the caller's lineage guard either way, so the pair is
  // read, never written. Null when either half is gone or the recovery record
  // was retargeted to another session - the caller treats that append as
  // unreconcilable, never as empty text.
  async readAppendBacking(preparation: ComposerPreparation): Promise<CommittedAppendBacking | null> {
    const recoveryId = preparation.backing.kind === "recovery" ? preparation.backing.recoveryId : null;
    return this.#transaction(
      [COMPOSER_DRAFTS_STORE, MUTATION_RECOVERY_STORE],
      "readonly",
      undefined,
      async (transaction) => {
        const [row, record] = await Promise.all([
          readSessionRow(transaction, preparation.key),
          recoveryId !== null
            ? requestResult<MutationRecoveryRecord | undefined>(
                transaction.objectStore(MUTATION_RECOVERY_STORE).get(recoveryId),
              )
            : Promise.resolve(undefined),
        ]);
        if (!row) return null;
        if (recoveryId === null) return { token: row.token, value: publicSessionRow(row) };
        // A retarget preserves the recovery id while moving the row to another
        // session, so pairing by id alone could pair this key's token with a
        // foreign session's text and attachments.
        if (!record || record.targetRef !== preparation.key.sessionRef) return null;
        return { token: row.token, value: recoveryComposerDraft(record) };
      },
    );
  }

  async transferSupportedDrafts(
    namespace: BrowserDraftKey["namespace"],
  ): Promise<{ state: "committed" | "pending" | "unavailable" }> {
    let inspected: InspectedDraft[];
    try {
      inspected = this.#inspectSupportedDrafts(namespace);
    } catch {
      return { state: "unavailable" };
    }
    if (inspected.length === 0) return { state: "committed" };
    const changed: BrowserDraftKey[] = [];
    // A watchdog rejection can outlive this commit. The completion's own
    // arrival proves the import landed, and by then a later scan imports the
    // same refs no more, so its cleanup would never reach the legacy bytes
    // this scan consumed. Cleaning up here is idempotent: it removes only
    // entries whose bytes still match what this scan inspected.
    const cleanupOnLateCommit = () => this.#cleanupInspected(inspected, changed);
    try {
      await this.#writeAndPublish(
        TRANSFER_STORES,
        "transferSupportedDrafts",
        changed,
        async (transaction) => {
          const incarnation = await this.#currentIncarnation(transaction, namespace);
          const drafts = transaction.objectStore(COMPOSER_DRAFTS_STORE);
          const transfers = transaction.objectStore(DRAFT_TRANSFERS_STORE);
          for (const incoming of inspected) {
            const current = await requestResult<SessionRow | undefined>(drafts.get(incoming.draftKey));
            const markerV1Key = serializedTransferKey(namespace, incoming.legacyKeyV1);
            const markerV2Key = serializedTransferKey(namespace, incoming.legacyKeyV2);
            const [markerV1, markerV2] = await Promise.all([
              requestResult<DraftTransferRow | undefined>(transfers.get(markerV1Key)),
              requestResult<DraftTransferRow | undefined>(transfers.get(markerV2Key)),
            ]);
            // Initialization only: a durable row or a previously inspected outcome
            // is never re-imported and never overwritten.
            if (!current && markerV1 === undefined && markerV2 === undefined) {
              const imported = newSessionRow(incoming.key, incoming.value, incarnation);
              // The imported row keeps the legacy draft's own wall-clock stamp so
              // later cross-session carries order by genuine edit order, not by
              // this transfer's import order; an unstamped legacy draft stays
              // unstamped and the carry's conservative rule orders it.
              const legacyStamp = incoming.value.editedAt;
              if (legacyStamp === undefined) delete imported.editedAt;
              else imported.editedAt = legacyStamp;
              const write = drafts.add(imported);
              changed.push(incoming.key);
              await requestResult(write);
            }
            await requestResult(
              transfers.put({
                serializedKey: markerV1Key,
                namespaceKey: serializeNamespace(namespace),
                legacyKey: incoming.legacyKeyV1,
                inspectedBytes: incoming.legacyV1,
              } satisfies DraftTransferRow),
            );
            await requestResult(
              transfers.put({
                serializedKey: markerV2Key,
                namespaceKey: serializeNamespace(namespace),
                legacyKey: incoming.legacyKeyV2,
                inspectedBytes: incoming.structuredV2,
              } satisfies DraftTransferRow),
            );
          }
        },
        cleanupOnLateCommit,
      );
    } catch {
      return { state: "pending" };
    }
    this.#cleanupInspected(inspected, changed);
    return { state: "committed" };
  }

  #inspectSupportedDrafts(namespace: BrowserDraftKey["namespace"]): InspectedDraft[] {
    if (this.#storage === undefined) throw new Error("legacy storage is unavailable");
    const storage = this.#storage;
    const refs = new Set<string>();
    const v1Prefix = draftStorageKey("");
    const v2Prefix = composerDraftStorageKey("");
    const keys = Array.from({ length: storage.length }, (_, index) => storage.key(index));
    for (const storageKey of keys) {
      if (storageKey?.startsWith(v1Prefix)) refs.add(storageKey.slice(v1Prefix.length));
      if (storageKey?.startsWith(v2Prefix)) refs.add(storageKey.slice(v2Prefix.length));
    }
    return [...refs].sort().map((sessionRef) => {
      const legacyKeyV1 = draftStorageKey(sessionRef);
      const legacyKeyV2 = composerDraftStorageKey(sessionRef);
      const legacyV1 = storage.getItem(legacyKeyV1);
      const structuredV2 = storage.getItem(legacyKeyV2);
      const draftKey: BrowserDraftKey = { namespace: { ...namespace }, sessionRef };
      const value = decodeSupportedComposerDraft(structuredV2, legacyV1);
      // The structured record wins whenever it decodes to the value the import
      // carries; otherwise the v1 literal is what the row now holds. The
      // decoder already knows whether it decoded, so decode once and derive
      // the provenance from that result instead of parsing the record again.
      const structured = structuredV2 === null ? null : decodeStructuredComposerDraft(structuredV2);
      const carriedFromV2 = structured !== null && sameComposerValue(structured.value, value);
      return {
        key: draftKey,
        draftKey: serializeSessionKey(draftKey),
        legacyKeyV1,
        legacyKeyV2,
        legacyV1,
        structuredV2,
        carriedFromV2,
        value,
      };
    });
  }

  #cleanupInspected(inspected: readonly InspectedDraft[], imported: readonly BrowserDraftKey[]): void {
    try {
      this.#beforeLegacyCleanup?.();
      // Cleanup only runs after a successful inspection, which requires a
      // legacy store; an absent store has inspected nothing to clean.
      const storage = this.#storage;
      if (storage === undefined) return;
      // Deletion requires a THIS-SCAN import: a ref the transfer skipped (its
      // durable row or marker already existed) keeps its legacy bytes, so a
      // divergent later edit from an older tab always survives even when it is
      // unchanged across scans.
      const importedKeys = new Set(imported.map(serializeSessionKey));
      for (const item of inspected) {
        if (!importedKeys.has(item.draftKey)) continue;
        // The import carried ONE representation's content into the durable
        // row: v2 when it decoded as a structured record, v1 otherwise. Only a
        // legacy entry whose bytes the row now carries is removable. A
        // divergent entry from an older tab that the row does not preserve is
        // user work, so it survives alongside its marker's inspected bytes;
        // the next scan re-inspects it, imports nothing because the row and
        // both markers exist, and cleans nothing because that scan imported
        // nothing. The composer-side recovery path is what eventually
        // disposes of it, never this cleanup.
        const v2Carried = item.carriedFromV2;
        const v1Carried = item.legacyV1 !== null && item.legacyV1 === item.value.text;
        for (const [legacyKey, bytes, carried] of [
          [item.legacyKeyV2, item.structuredV2, v2Carried],
          [item.legacyKeyV1, item.legacyV1, v1Carried],
        ] as const) {
          if (carried && bytes !== null && storage.getItem(legacyKey) === bytes) storage.removeItem(legacyKey);
        }
      }
    } catch {
      // IDB already holds both the authoritative row and inspected-byte markers.
    }
  }

  async #currentIncarnation(transaction: IDBTransaction, namespace: BrowserDraftKey["namespace"]): Promise<string> {
    const current = await readNamespaceRow(transaction, namespace);
    if (current) return current.incarnation;
    const row = { namespaceKey: serializeNamespace(namespace), incarnation: createSecureUUID() } satisfies NamespaceRow;
    await requestResult(transaction.objectStore(ANNOTATION_NAMESPACES_STORE).add(row));
    return row.incarnation;
  }

  // One owner for every drafts write. The caller allocates `changed` and pushes
  // each key the moment its write is issued; this helper publishes those keys
  // exactly once when the transaction commits. A watchdog rejection can
  // outlive a commit, so the runner's late-completion seam publishes the same
  // keys when that completion lands: a committed write is never silent. A
  // caller whose post-commit consequences the rejection skipped passes them as
  // onLateCommit, which runs only when that late completion proves the commit.
  async #writeAndPublish<T>(
    stores: readonly string[],
    operation: BrowserDraftOperation | undefined,
    changed: BrowserDraftKey[],
    body: (transaction: IDBTransaction) => Promise<T>,
    onLateCommit?: () => void,
  ): Promise<T> {
    let published = false;
    const publish = () => {
      if (published) return;
      published = true;
      this.#publish(changed);
    };
    const result = await this.#transaction(stores, "readwrite", operation, body, () => {
      publish();
      onLateCommit?.();
    });
    publish();
    return result;
  }

  async #transaction<T>(
    stores: readonly string[],
    mode: "readonly" | "readwrite",
    operation: BrowserDraftOperation | undefined,
    body: (transaction: IDBTransaction) => Promise<T>,
    onLateCompletion?: () => void,
  ): Promise<T> {
    const database = await this.#connection.open();
    const transaction = database.transaction([...stores], mode);
    // Unlike the outbox (whose delivery state machines must not observe a
    // fabricated mutation outcome), every drafts operation re-reads durable
    // state on retry: seedDraft reports conflict, commitEdit unavailable,
    // appendAnnotation already-applied. An unobserved transaction is therefore
    // honestly reported as a storage fault — the send path's
    // tolerateStorageFault and each operation's own state handling take over —
    // instead of leaving the caller pending forever. A commit that lands after
    // that fault is still published through onLateCompletion.
    return runBoundedTransaction({
      transaction,
      retire: () => this.#connection.retire(database),
      waitMs: STORAGE_WAIT_MS,
      timeout: () => new IndexedDBTransactionTimeoutError("Browser draft repository transaction timed out"),
      onLateCompletion,
      run: async () => {
        const result = await body(transaction);
        if (operation) this.#beforeCommit?.(operation);
        return result;
      },
    });
  }

  #publish(keys: readonly BrowserDraftKey[]): void {
    const unique = new Map(keys.map((key) => [serializeSessionKey(key), key]));
    for (const key of unique.values()) {
      for (const listener of this.#listeners) {
        try {
          listener(cloneKey(key));
        } catch (error) {
          try {
            this.#onListenerDiagnostic({ path: "listener-failed", key: cloneKey(key), error });
          } catch {
            // Diagnostics are observational and cannot change storage outcomes.
          }
        }
      }
    }
  }
}

export function createBrowserDraftRepository(options: BrowserDraftRepositoryOptions = {}): BrowserDraftRepository {
  return new BrowserDraftRepositoryImpl(options);
}

// The one lazy module singleton every production browser writer shares, so the
// source defaults and the annotation ports use the SAME repository instance.
let browserDrafts: BrowserDraftRepository | undefined;
export function getBrowserDraftRepository(): BrowserDraftRepository {
  if (browserDrafts === undefined) browserDrafts = createBrowserDraftRepository();
  return browserDrafts;
}

// Deliberate reset of the module singleton for tests that REPLACE the storage
// universe (a new IndexedDB factory): the captured factory makes the old
// singleton stale, so closing it alone is not a reset. Production never calls
// this; it drops the singleton so the next accessor captures the new factory.
export function resetBrowserDraftRepositoryForTests(): void {
  browserDrafts?.close();
  browserDrafts = undefined;
}

export function createBrowserDraftTransferRuntime(input: {
  repository: BrowserDraftRepository;
  namespace: BrowserDraftKey["namespace"];
  storage: Storage;
  window: Window;
  document: Document;
}): {
  composerOpened(key: BrowserDraftKey): void;
  dispose(): void;
} {
  let running = false;
  let followUp = false;
  let disposed = false;
  const scan = () => {
    if (disposed) return;
    if (running) {
      followUp = true;
      return;
    }
    running = true;
    void input.repository
      .transferSupportedDrafts(input.namespace)
      .catch(() => ({ state: "pending" as const }))
      .finally(() => {
        running = false;
        if (followUp) {
          followUp = false;
          scan();
        }
      });
  };
  const visibilityChanged = () => {
    if (input.document.visibilityState === "visible") scan();
  };
  const storageChanged = (event: StorageEvent) => {
    if (event.storageArea !== null && event.storageArea !== input.storage) return;
    if (event.key?.startsWith(draftStorageKey("")) || event.key?.startsWith(composerDraftStorageKey(""))) scan();
  };
  input.document.addEventListener("visibilitychange", visibilityChanged);
  input.window.addEventListener("storage", storageChanged);
  scan();
  return {
    composerOpened(key) {
      if (key.namespace.kind === input.namespace.kind && key.namespace.origin === input.namespace.origin) scan();
    },
    dispose() {
      if (disposed) return;
      disposed = true;
      input.document.removeEventListener("visibilitychange", visibilityChanged);
      input.window.removeEventListener("storage", storageChanged);
    },
  };
}
