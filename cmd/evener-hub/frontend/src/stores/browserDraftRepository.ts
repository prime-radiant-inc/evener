import type {
  DraftAuthorityToken,
  DraftBackingIdentity,
  DraftSettlement,
  DraftSettlementGuard,
  SubmittedDraft,
} from "@evener/appwire-client/state/mutation";
import {
  composerDraftStorageKey,
  decodeSupportedComposerDraft,
  draftStorageKey,
} from "../panes/session/composer/draft";
import type { SkillEditorValue } from "../panes/session/composer/skillDocument";
import { IDBConnection, requestResult, transactionCompletion, tryAbortTransaction } from "./idbConnection";
import {
  ANNOTATION_NAMESPACES_STORE,
  COMPOSER_DRAFTS_STORE,
  DRAFT_CONFLICTS_STORE,
  DRAFT_TRANSFERS_STORE,
  LOCAL_COMPOSITION_DATABASE_NAME,
  LOCAL_COMPOSITION_DATABASE_VERSION,
  registerLocalCompositionSchema,
} from "./localCompositionSchema";
import { createSecureUUID } from "./secureUUID";

export interface BrowserDraftKey {
  namespace: { kind: "browser"; origin: string };
  sessionRef: string;
}

export interface BrowserDraftOwner {
  paneId: string;
  compositionOwnerId: string;
  lifetimeSerial: number;
}

export type DraftToken = DraftAuthorityToken;

export interface StoredDraftValue extends SkillEditorValue {
  key: BrowserDraftKey;
  token: DraftToken;
  backing: DraftBackingIdentity;
}

export interface OwnedComposerDraft extends StoredDraftValue {
  ownership: "owned";
  compositionOwnerId: string;
}

export type StoredComposerDraft =
  | OwnedComposerDraft
  | (StoredDraftValue & { ownership: "unclaimed"; compositionOwnerId?: never });

export type DraftCommit =
  | { state: "committed"; draft: StoredComposerDraft }
  | { state: "conflict"; current: StoredComposerDraft }
  | { state: "unavailable" };

export interface SupportedDraftConflict {
  conflictId: string;
  key: BrowserDraftKey;
  legacyKey: string;
  inspectedBytes: string;
  value: SkillEditorValue;
}

export interface BrowserDraftRepository {
  load(key: BrowserDraftKey): Promise<StoredComposerDraft | null>;
  seedUnclaimed(key: BrowserDraftKey, value: SkillEditorValue): Promise<DraftCommit>;
  claim(
    key: BrowserDraftKey,
    owner: BrowserDraftOwner,
    expected: DraftToken | null,
    stillOwnsOwner: () => boolean,
  ): Promise<DraftCommit>;
  adopt(
    key: BrowserDraftKey,
    owner: BrowserDraftOwner,
    expected: DraftToken,
    expectedOwnerId: string,
    stillOwnsOwner: () => boolean,
  ): Promise<DraftCommit>;
  transferSupportedDrafts(namespace: BrowserDraftKey["namespace"]): Promise<{ state: "committed" | "pending" }>;
  listConflicts(key: BrowserDraftKey): Promise<readonly SupportedDraftConflict[]>;
  restoreConflict(
    key: BrowserDraftKey,
    owner: BrowserDraftOwner,
    expected: DraftToken,
    conflictId: string,
    stillOwnsEdit: () => boolean,
  ): Promise<DraftCommit>;
  commitEdit(
    key: BrowserDraftKey,
    owner: BrowserDraftOwner,
    expected: DraftToken,
    value: SkillEditorValue,
    stillOwnsEdit: () => boolean,
  ): Promise<DraftCommit>;
  clearSubmitted(key: BrowserDraftKey, submitted: SubmittedDraft): Promise<DraftSettlement>;
  subscribe(listener: (key: BrowserDraftKey) => void): () => void;
}

type BrowserDraftOperation =
  | "seedUnclaimed"
  | "claim"
  | "adopt"
  | "transferSupportedDrafts"
  | "restoreConflict"
  | "commitEdit"
  | "clearSubmitted";

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

interface NamespaceRow {
  namespaceKey: string;
  incarnation: string;
}

type StoredDraftRow = StoredComposerDraft & { serializedKey: string };

interface DraftTransferRow {
  serializedKey: string;
  namespaceKey: string;
  legacyKey: string;
  inspectedBytes: string | null;
}

interface DraftConflictRow extends SupportedDraftConflict {
  draftKey: string;
}

interface InspectedDraft {
  key: BrowserDraftKey;
  draftKey: string;
  legacyKeyV1: string;
  legacyKeyV2: string;
  legacyV1: string | null;
  structuredV2: string | null;
  value: SkillEditorValue;
  conflictId: string;
}

const STORAGE_WAIT_MS = 10_000;
const DRAFT_STORES = [COMPOSER_DRAFTS_STORE, ANNOTATION_NAMESPACES_STORE] as const;
const TRANSFER_STORES = [
  COMPOSER_DRAFTS_STORE,
  DRAFT_TRANSFERS_STORE,
  DRAFT_CONFLICTS_STORE,
  ANNOTATION_NAMESPACES_STORE,
] as const;

function namespaceKey(namespace: BrowserDraftKey["namespace"]): string {
  return JSON.stringify([namespace.kind, namespace.origin]);
}

function serializedDraftKey(key: BrowserDraftKey): string {
  return JSON.stringify([key.namespace.kind, key.namespace.origin, key.sessionRef]);
}

function serializedTransferKey(namespace: BrowserDraftKey["namespace"], legacyKey: string): string {
  return JSON.stringify([namespace.kind, namespace.origin, legacyKey]);
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

function cloneBacking(backing: DraftBackingIdentity): DraftBackingIdentity {
  return backing.kind === "draft"
    ? { kind: "draft" }
    : {
        kind: "recovery",
        recoveryId: backing.recoveryId,
        writeVersion: backing.writeVersion,
        replacementEpoch: backing.replacementEpoch,
      };
}

function cloneToken(token: DraftToken): DraftToken {
  return {
    namespaceIncarnation: token.namespaceIncarnation,
    persistentRevision: token.persistentRevision,
    ownerRevision: token.ownerRevision,
  };
}

function publicDraft(row: StoredDraftRow): StoredComposerDraft {
  const { serializedKey: _serializedKey, ...draft } = row;
  return {
    ...draft,
    ...cloneValue(draft),
    key: cloneKey(draft.key),
    token: cloneToken(draft.token),
    backing: cloneBacking(draft.backing),
  } as StoredComposerDraft;
}

function storedDraft(draft: StoredComposerDraft): StoredDraftRow {
  return { ...draft, ...cloneValue(draft), key: cloneKey(draft.key), serializedKey: serializedDraftKey(draft.key) };
}

function sameToken(left: DraftToken, right: DraftToken): boolean {
  return (
    left.namespaceIncarnation === right.namespaceIncarnation &&
    left.persistentRevision === right.persistentRevision &&
    left.ownerRevision === right.ownerRevision
  );
}

function sameBacking(left: DraftBackingIdentity, right: DraftBackingIdentity): boolean {
  if (left.kind !== right.kind) return false;
  if (left.kind === "draft" || right.kind === "draft") return true;
  return (
    left.recoveryId === right.recoveryId &&
    left.writeVersion === right.writeVersion &&
    left.replacementEpoch === right.replacementEpoch
  );
}

function sameMentions(left: SkillEditorValue["mentions"], right: SkillEditorValue["mentions"]): boolean {
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

function sameStrings(left: readonly string[] | undefined, right: readonly string[] | undefined): boolean {
  const a = left ?? [];
  const b = right ?? [];
  return a.length === b.length && a.every((value, index) => value === b[index]);
}

function sameValue(left: SkillEditorValue, right: SkillEditorValue): boolean {
  return (
    left.text === right.text &&
    sameStrings(left.skillNames, right.skillNames) &&
    sameStrings(left.commandNames, right.commandNames) &&
    sameMentions(left.mentions, right.mentions)
  );
}

function replaceValue<T extends StoredComposerDraft>(draft: T, value: SkillEditorValue): T {
  const { text: _text, skillNames: _skillNames, commandNames: _commandNames, mentions: _mentions, ...identity } = draft;
  return { ...identity, ...cloneValue(value) } as T;
}

function isEmptyValue(value: SkillEditorValue): boolean {
  return (
    value.text === "" &&
    value.skillNames.length === 0 &&
    (value.commandNames?.length ?? 0) === 0 &&
    (value.mentions?.length ?? 0) === 0
  );
}

function newToken(incarnation: string): DraftToken {
  return { namespaceIncarnation: incarnation, persistentRevision: 0, ownerRevision: 0 };
}

function unclaimedDraft(key: BrowserDraftKey, value: SkillEditorValue, incarnation: string): StoredComposerDraft {
  return {
    ...cloneValue(value),
    key: cloneKey(key),
    token: newToken(incarnation),
    backing: { kind: "draft" },
    ownership: "unclaimed",
  };
}

function ownedDraft(
  key: BrowserDraftKey,
  value: SkillEditorValue,
  incarnation: string,
  ownerId: string,
): OwnedComposerDraft {
  return {
    ...cloneValue(value),
    key: cloneKey(key),
    token: newToken(incarnation),
    backing: { kind: "draft" },
    ownership: "owned",
    compositionOwnerId: ownerId,
  };
}

function committed(draft: StoredComposerDraft): DraftCommit {
  return { state: "committed", draft: publicDraft(storedDraft(draft)) };
}

function conflict(current: StoredDraftRow): DraftCommit {
  return { state: "conflict", current: publicDraft(current) };
}

export function captureDraftSettlementGuard(draft: OwnedComposerDraft): DraftSettlementGuard {
  const token = Object.freeze(cloneToken(draft.token));
  const backing = Object.freeze(cloneBacking(draft.backing));
  return Object.freeze({ token, compositionOwnerId: draft.compositionOwnerId, backing });
}

class BrowserDraftRepositoryImpl implements BrowserDraftRepository {
  readonly #connection: IDBConnection;
  readonly #databaseName: string;
  readonly #storage: Storage;
  readonly #beforeCommit: ((operation: BrowserDraftOperation) => void) | undefined;
  readonly #beforeLegacyCleanup: (() => void) | undefined;
  readonly #onListenerDiagnostic: (diagnostic: BrowserDraftListenerDiagnostic) => void;
  readonly #listeners = new Set<(key: BrowserDraftKey) => void>();

  constructor(options: BrowserDraftRepositoryOptions) {
    const indexedDB = options.indexedDB ?? globalThis.indexedDB;
    if (!indexedDB) throw new Error("IndexedDB is unavailable");
    const storage = options.storage ?? globalThis.localStorage;
    if (!storage) throw new Error("localStorage is unavailable");
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
        open: "Unable to open browser draft repository",
        superseded: "Browser draft repository connection was closed",
        timeout: () => new Error("Browser draft repository open timed out"),
        blocked: "Browser draft repository upgrade is blocked",
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

  async load(key: BrowserDraftKey): Promise<StoredComposerDraft | null> {
    const changed: BrowserDraftKey[] = [];
    const draft = await this.#write(DRAFT_STORES, undefined, async (transaction) => {
      const incarnation = await this.#currentIncarnation(transaction, key.namespace);
      const store = transaction.objectStore(COMPOSER_DRAFTS_STORE);
      const row = await requestResult<StoredDraftRow | undefined>(store.get(serializedDraftKey(key)));
      if (!row) return null;
      if (row.token.namespaceIncarnation === incarnation) return publicDraft(row);
      const refreshed: StoredComposerDraft = {
        ...publicDraft(row),
        token: { ...row.token, namespaceIncarnation: incarnation },
      };
      await requestResult(store.put(storedDraft(refreshed)));
      changed.push(key);
      return publicDraft(storedDraft(refreshed));
    });
    this.#publish(changed);
    return draft;
  }

  async seedUnclaimed(key: BrowserDraftKey, value: SkillEditorValue): Promise<DraftCommit> {
    try {
      const changed: BrowserDraftKey[] = [];
      const result = await this.#write(DRAFT_STORES, "seedUnclaimed", async (transaction) => {
        const incarnation = await this.#currentIncarnation(transaction, key.namespace);
        const store = transaction.objectStore(COMPOSER_DRAFTS_STORE);
        const current = await requestResult<StoredDraftRow | undefined>(store.get(serializedDraftKey(key)));
        if (current) return conflict(current);
        const draft = unclaimedDraft(key, value, incarnation);
        await requestResult(store.add(storedDraft(draft)));
        changed.push(key);
        return committed(draft);
      });
      this.#publish(changed);
      return result;
    } catch {
      return { state: "unavailable" };
    }
  }

  async claim(
    key: BrowserDraftKey,
    owner: BrowserDraftOwner,
    expected: DraftToken | null,
    stillOwnsOwner: () => boolean,
  ): Promise<DraftCommit> {
    try {
      const changed: BrowserDraftKey[] = [];
      const result = await this.#write(DRAFT_STORES, "claim", async (transaction) => {
        const incarnation = await this.#currentIncarnation(transaction, key.namespace);
        const store = transaction.objectStore(COMPOSER_DRAFTS_STORE);
        const current = await requestResult<StoredDraftRow | undefined>(store.get(serializedDraftKey(key)));
        if (!current) {
          if (expected !== null || !stillOwnsOwner()) return { state: "unavailable" } as const;
          const draft = ownedDraft(key, { text: "", skillNames: [] }, incarnation, owner.compositionOwnerId);
          draft.token = { ...draft.token, ownerRevision: 1 };
          await requestResult(store.add(storedDraft(draft)));
          changed.push(key);
          return committed(draft);
        }
        if (
          current.token.namespaceIncarnation !== incarnation ||
          expected === null ||
          !sameToken(current.token, expected)
        ) {
          return conflict(current);
        }
        if (!stillOwnsOwner()) return { state: "unavailable" } as const;
        if (current.ownership === "owned") {
          return current.compositionOwnerId === owner.compositionOwnerId ? committed(current) : conflict(current);
        }
        const next: OwnedComposerDraft = {
          ...publicDraft(current),
          ownership: "owned",
          compositionOwnerId: owner.compositionOwnerId,
          token: { ...current.token, ownerRevision: current.token.ownerRevision + 1 },
        };
        await requestResult(store.put(storedDraft(next)));
        changed.push(key);
        return committed(next);
      });
      this.#publish(changed);
      return result;
    } catch {
      return { state: "unavailable" };
    }
  }

  async adopt(
    key: BrowserDraftKey,
    owner: BrowserDraftOwner,
    expected: DraftToken,
    expectedOwnerId: string,
    stillOwnsOwner: () => boolean,
  ): Promise<DraftCommit> {
    try {
      const changed: BrowserDraftKey[] = [];
      const result = await this.#write(DRAFT_STORES, "adopt", async (transaction) => {
        const incarnation = await this.#currentIncarnation(transaction, key.namespace);
        const store = transaction.objectStore(COMPOSER_DRAFTS_STORE);
        const current = await requestResult<StoredDraftRow | undefined>(store.get(serializedDraftKey(key)));
        if (!current) return { state: "unavailable" } as const;
        if (
          current.token.namespaceIncarnation !== incarnation ||
          !sameToken(current.token, expected) ||
          current.ownership !== "owned" ||
          current.compositionOwnerId !== expectedOwnerId
        ) {
          return conflict(current);
        }
        if (!stillOwnsOwner()) return { state: "unavailable" } as const;
        if (current.compositionOwnerId === owner.compositionOwnerId) return committed(current);
        const next: OwnedComposerDraft = {
          ...publicDraft(current),
          ownership: "owned",
          compositionOwnerId: owner.compositionOwnerId,
          token: { ...current.token, ownerRevision: current.token.ownerRevision + 1 },
        };
        await requestResult(store.put(storedDraft(next)));
        changed.push(key);
        return committed(next);
      });
      this.#publish(changed);
      return result;
    } catch {
      return { state: "unavailable" };
    }
  }

  async commitEdit(
    key: BrowserDraftKey,
    owner: BrowserDraftOwner,
    expected: DraftToken,
    value: SkillEditorValue,
    stillOwnsEdit: () => boolean,
  ): Promise<DraftCommit> {
    try {
      const changed: BrowserDraftKey[] = [];
      const result = await this.#write(DRAFT_STORES, "commitEdit", async (transaction) => {
        const incarnation = await this.#currentIncarnation(transaction, key.namespace);
        const store = transaction.objectStore(COMPOSER_DRAFTS_STORE);
        const current = await requestResult<StoredDraftRow | undefined>(store.get(serializedDraftKey(key)));
        if (!current) return { state: "unavailable" } as const;
        if (
          current.token.namespaceIncarnation !== incarnation ||
          !sameToken(current.token, expected) ||
          current.ownership !== "owned" ||
          current.compositionOwnerId !== owner.compositionOwnerId
        ) {
          return conflict(current);
        }
        if (!stillOwnsEdit()) return { state: "unavailable" } as const;
        if (sameValue(current, value)) return committed(current);
        const next: OwnedComposerDraft = {
          ...replaceValue(publicDraft(current), value),
          ownership: "owned",
          compositionOwnerId: current.compositionOwnerId,
          token: { ...current.token, persistentRevision: current.token.persistentRevision + 1 },
        };
        await requestResult(store.put(storedDraft(next)));
        changed.push(key);
        return committed(next);
      });
      this.#publish(changed);
      return result;
    } catch {
      return { state: "unavailable" };
    }
  }

  async clearSubmitted(key: BrowserDraftKey, submitted: SubmittedDraft): Promise<DraftSettlement> {
    let draftUnchanged = false;
    try {
      const changed: BrowserDraftKey[] = [];
      const result = await this.#write(DRAFT_STORES, "clearSubmitted", async (transaction) => {
        const store = transaction.objectStore(COMPOSER_DRAFTS_STORE);
        const current = await requestResult<StoredDraftRow | undefined>(store.get(serializedDraftKey(key)));
        if (!current) return { cleared: false, draftUnchanged: false, persistence: "committed" } as const;
        const incarnation = await this.#currentIncarnation(transaction, key.namespace);
        const guard = submitted.settlementGuard;
        draftUnchanged =
          guard !== undefined &&
          current.ownership === "owned" &&
          current.token.namespaceIncarnation === incarnation &&
          current.compositionOwnerId === guard.compositionOwnerId &&
          sameToken(current.token, guard.token) &&
          sameBacking(current.backing, guard.backing) &&
          (submitted.persistentRevisionAtStart === undefined ||
            submitted.persistentRevisionAtStart === current.token.persistentRevision);
        if (!draftUnchanged || !submitted.stillOwnsEdit?.()) {
          return { cleared: false, draftUnchanged, persistence: "committed" } as const;
        }
        const submittedValue: SkillEditorValue = {
          text: submitted.text,
          skillNames: [...submitted.skillNames],
          ...(submitted.commandNames === undefined ? {} : { commandNames: [...submitted.commandNames] }),
        };
        if (
          current.text !== submittedValue.text ||
          !sameStrings(current.skillNames, submittedValue.skillNames) ||
          !sameStrings(current.commandNames, submittedValue.commandNames)
        ) {
          return { cleared: false, draftUnchanged, persistence: "committed" } as const;
        }
        if (current.ownership !== "owned") {
          return { cleared: false, draftUnchanged: false, persistence: "committed" } as const;
        }
        const cleared: OwnedComposerDraft = {
          ...replaceValue(publicDraft(current), { text: "", skillNames: [] }),
          ownership: "owned",
          compositionOwnerId: current.compositionOwnerId,
          token: { ...current.token, persistentRevision: current.token.persistentRevision + 1 },
        };
        await requestResult(store.put(storedDraft(cleared)));
        changed.push(key);
        return { cleared: true, draftUnchanged: true, persistence: "committed" } as const;
      });
      this.#publish(changed);
      return result;
    } catch {
      return { cleared: false, draftUnchanged, persistence: "pending" };
    }
  }

  async listConflicts(key: BrowserDraftKey): Promise<readonly SupportedDraftConflict[]> {
    return this.#read(DRAFT_CONFLICTS_STORE, async (transaction) => {
      const rows = await requestResult<DraftConflictRow[]>(
        transaction.objectStore(DRAFT_CONFLICTS_STORE).index("byDraftKey").getAll(serializedDraftKey(key)),
      );
      return rows.map(({ draftKey: _draftKey, ...row }) => ({
        ...row,
        key: cloneKey(row.key),
        value: cloneValue(row.value),
      }));
    });
  }

  async restoreConflict(
    key: BrowserDraftKey,
    owner: BrowserDraftOwner,
    expected: DraftToken,
    conflictId: string,
    stillOwnsEdit: () => boolean,
  ): Promise<DraftCommit> {
    try {
      const changed: BrowserDraftKey[] = [];
      const result = await this.#write(
        [COMPOSER_DRAFTS_STORE, DRAFT_CONFLICTS_STORE, ANNOTATION_NAMESPACES_STORE],
        "restoreConflict",
        async (transaction) => {
          const incarnation = await this.#currentIncarnation(transaction, key.namespace);
          const drafts = transaction.objectStore(COMPOSER_DRAFTS_STORE);
          const current = await requestResult<StoredDraftRow | undefined>(drafts.get(serializedDraftKey(key)));
          if (!current) return { state: "unavailable" } as const;
          if (
            current.token.namespaceIncarnation !== incarnation ||
            !sameToken(current.token, expected) ||
            current.ownership !== "owned" ||
            current.compositionOwnerId !== owner.compositionOwnerId ||
            !isEmptyValue(current)
          ) {
            return conflict(current);
          }
          const conflicts = transaction.objectStore(DRAFT_CONFLICTS_STORE);
          const incoming = await requestResult<DraftConflictRow | undefined>(conflicts.get(conflictId));
          if (!incoming || incoming.draftKey !== serializedDraftKey(key)) return conflict(current);
          if (!stillOwnsEdit()) return { state: "unavailable" } as const;
          const next: OwnedComposerDraft = {
            ...replaceValue(publicDraft(current), incoming.value),
            ownership: "owned",
            compositionOwnerId: current.compositionOwnerId,
            token: { ...current.token, persistentRevision: current.token.persistentRevision + 1 },
          };
          await requestResult(drafts.put(storedDraft(next)));
          await requestResult(conflicts.delete(conflictId));
          changed.push(key);
          return committed(next);
        },
      );
      this.#publish(changed);
      return result;
    } catch {
      return { state: "unavailable" };
    }
  }

  async transferSupportedDrafts(namespace: BrowserDraftKey["namespace"]): Promise<{ state: "committed" | "pending" }> {
    let inspected: InspectedDraft[];
    try {
      inspected = this.#inspectSupportedDrafts(namespace);
    } catch {
      return { state: "pending" };
    }
    if (inspected.length === 0) return { state: "committed" };
    const changed: BrowserDraftKey[] = [];
    try {
      await this.#write(TRANSFER_STORES, "transferSupportedDrafts", async (transaction) => {
        const incarnation = await this.#currentIncarnation(transaction, namespace);
        const drafts = transaction.objectStore(COMPOSER_DRAFTS_STORE);
        const transfers = transaction.objectStore(DRAFT_TRANSFERS_STORE);
        const conflicts = transaction.objectStore(DRAFT_CONFLICTS_STORE);
        for (const incoming of inspected) {
          const current = await requestResult<StoredDraftRow | undefined>(drafts.get(incoming.draftKey));
          const markerV1Key = serializedTransferKey(namespace, incoming.legacyKeyV1);
          const markerV2Key = serializedTransferKey(namespace, incoming.legacyKeyV2);
          const [markerV1, markerV2] = await Promise.all([
            requestResult<DraftTransferRow | undefined>(transfers.get(markerV1Key)),
            requestResult<DraftTransferRow | undefined>(transfers.get(markerV2Key)),
          ]);
          const changedV1 = markerV1 === undefined || markerV1.inspectedBytes !== incoming.legacyV1;
          const changedV2 = markerV2 === undefined || markerV2.inspectedBytes !== incoming.structuredV2;
          const hasPriorMarker = markerV1 !== undefined || markerV2 !== undefined;

          if (!current && !hasPriorMarker) {
            await requestResult(drafts.add(storedDraft(unclaimedDraft(incoming.key, incoming.value, incarnation))));
            changed.push(incoming.key);
          } else if ((changedV1 || changedV2) && (incoming.legacyV1 !== null || incoming.structuredV2 !== null)) {
            const selected = this.#conflictSource(incoming, markerV1, markerV2, changedV1, changedV2);
            if (selected !== null) {
              const existing = await requestResult<DraftConflictRow[]>(
                conflicts.index("byDraftKey").getAll(incoming.draftKey),
              );
              if (
                !existing.some(
                  (row) => row.legacyKey === selected.legacyKey && row.inspectedBytes === selected.inspectedBytes,
                )
              ) {
                const row: DraftConflictRow = {
                  conflictId: incoming.conflictId,
                  draftKey: incoming.draftKey,
                  key: cloneKey(incoming.key),
                  legacyKey: selected.legacyKey,
                  inspectedBytes: selected.inspectedBytes,
                  value: cloneValue(incoming.value),
                };
                await requestResult(conflicts.add(row));
                changed.push(incoming.key);
              }
            }
          }
          await requestResult(
            transfers.put({
              serializedKey: markerV1Key,
              namespaceKey: namespaceKey(namespace),
              legacyKey: incoming.legacyKeyV1,
              inspectedBytes: incoming.legacyV1,
            } satisfies DraftTransferRow),
          );
          await requestResult(
            transfers.put({
              serializedKey: markerV2Key,
              namespaceKey: namespaceKey(namespace),
              legacyKey: incoming.legacyKeyV2,
              inspectedBytes: incoming.structuredV2,
            } satisfies DraftTransferRow),
          );
        }
      });
    } catch {
      return { state: "pending" };
    }
    this.#publish(changed);
    this.#cleanupInspected(inspected);
    return { state: "committed" };
  }

  #inspectSupportedDrafts(namespace: BrowserDraftKey["namespace"]): InspectedDraft[] {
    const refs = new Set<string>();
    const v1Prefix = draftStorageKey("");
    const v2Prefix = composerDraftStorageKey("");
    const keys = Array.from({ length: this.#storage.length }, (_, index) => this.#storage.key(index));
    for (const storageKey of keys) {
      if (storageKey?.startsWith(v1Prefix)) refs.add(storageKey.slice(v1Prefix.length));
      if (storageKey?.startsWith(v2Prefix)) refs.add(storageKey.slice(v2Prefix.length));
    }
    return [...refs].sort().map((sessionRef) => {
      const legacyKeyV1 = draftStorageKey(sessionRef);
      const legacyKeyV2 = composerDraftStorageKey(sessionRef);
      const legacyV1 = this.#storage.getItem(legacyKeyV1);
      const structuredV2 = this.#storage.getItem(legacyKeyV2);
      const draftKey: BrowserDraftKey = { namespace: { ...namespace }, sessionRef };
      return {
        key: draftKey,
        draftKey: serializedDraftKey(draftKey),
        legacyKeyV1,
        legacyKeyV2,
        legacyV1,
        structuredV2,
        value: decodeSupportedComposerDraft(structuredV2, legacyV1),
        conflictId: createSecureUUID(),
      };
    });
  }

  #conflictSource(
    incoming: InspectedDraft,
    markerV1: DraftTransferRow | undefined,
    markerV2: DraftTransferRow | undefined,
    changedV1: boolean,
    changedV2: boolean,
  ): { legacyKey: string; inspectedBytes: string } | null {
    const candidates = [
      ...(changedV2 && incoming.structuredV2 !== null
        ? [{ legacyKey: incoming.legacyKeyV2, inspectedBytes: incoming.structuredV2, kind: "v2" as const }]
        : []),
      ...(changedV1 && incoming.legacyV1 !== null
        ? [{ legacyKey: incoming.legacyKeyV1, inspectedBytes: incoming.legacyV1, kind: "v1" as const }]
        : []),
    ];
    if (candidates.length === 0) return null;
    for (const candidate of candidates) {
      const reverted =
        candidate.kind === "v2"
          ? decodeSupportedComposerDraft(markerV2?.inspectedBytes ?? null, incoming.legacyV1)
          : decodeSupportedComposerDraft(incoming.structuredV2, markerV1?.inspectedBytes ?? null);
      if (!sameValue(reverted, incoming.value)) return candidate;
    }
    return candidates[0] ?? null;
  }

  #cleanupInspected(inspected: readonly InspectedDraft[]): void {
    try {
      this.#beforeLegacyCleanup?.();
      for (const item of inspected) {
        for (const [legacyKey, bytes] of [
          [item.legacyKeyV2, item.structuredV2],
          [item.legacyKeyV1, item.legacyV1],
        ] as const) {
          if (bytes !== null && this.#storage.getItem(legacyKey) === bytes) this.#storage.removeItem(legacyKey);
        }
      }
    } catch {
      // IDB already holds both the authoritative row and inspected-byte markers.
    }
  }

  async #currentIncarnation(transaction: IDBTransaction, namespace: BrowserDraftKey["namespace"]): Promise<string> {
    const store = transaction.objectStore(ANNOTATION_NAMESPACES_STORE);
    const key = namespaceKey(namespace);
    const current = await requestResult<NamespaceRow | undefined>(store.get(key));
    if (current) return current.incarnation;
    const row = { namespaceKey: key, incarnation: createSecureUUID() } satisfies NamespaceRow;
    await requestResult(store.add(row));
    return row.incarnation;
  }

  async #read<T>(stores: string | readonly string[], body: (transaction: IDBTransaction) => Promise<T>): Promise<T> {
    return this.#transaction(stores, "readonly", undefined, body);
  }

  async #write<T>(
    stores: string | readonly string[],
    operation: BrowserDraftOperation | undefined,
    body: (transaction: IDBTransaction) => Promise<T>,
  ): Promise<T> {
    return this.#transaction(stores, "readwrite", operation, body);
  }

  async #transaction<T>(
    stores: string | readonly string[],
    mode: "readonly" | "readwrite",
    operation: BrowserDraftOperation | undefined,
    body: (transaction: IDBTransaction) => Promise<T>,
  ): Promise<T> {
    const database = await this.#connection.open();
    const transaction = database.transaction([...([] as string[]).concat(stores as string | readonly string[])], mode);
    const completed = transactionCompletion(transaction);
    try {
      const result = await body(transaction);
      if (operation) this.#beforeCommit?.(operation);
      await completed;
      return result;
    } catch (error) {
      tryAbortTransaction(transaction);
      await completed.catch(() => undefined);
      throw error;
    }
  }

  #publish(keys: readonly BrowserDraftKey[]): void {
    const unique = new Map(keys.map((key) => [serializedDraftKey(key), key]));
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
