import type {
  DraftBackingIdentity,
  DraftSessionToken,
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
import { IDBConnection, requestResult, transactionCompletion, tryAbortTransaction } from "./idbConnection";
import {
  ANNOTATION_NAMESPACES_STORE,
  COMPOSER_DRAFTS_STORE,
  DRAFT_TRANSFERS_STORE,
  LOCAL_COMPOSITION_DATABASE_NAME,
  LOCAL_COMPOSITION_DATABASE_VERSION,
  registerLocalCompositionSchema,
} from "./localCompositionSchema";
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
  transferSupportedDrafts(namespace: BrowserDraftKey["namespace"]): Promise<{ state: "committed" | "pending" }>;
  subscribe(listener: (key: BrowserDraftKey) => void): () => void;
  close(): void;
}

type BrowserDraftOperation = "seedDraft" | "commitEdit" | "clearSubmitted" | "transferSupportedDrafts";

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
  value: SkillEditorValue;
}

const STORAGE_WAIT_MS = 10_000;
const DRAFT_STORES = [COMPOSER_DRAFTS_STORE, ANNOTATION_NAMESPACES_STORE] as const;
const TRANSFER_STORES = [COMPOSER_DRAFTS_STORE, DRAFT_TRANSFERS_STORE, ANNOTATION_NAMESPACES_STORE] as const;

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
  return { ...cloneValue(row), key: cloneKey(row.key), token: { ...row.token } };
}

function newSessionRow(key: BrowserDraftKey, value: SkillEditorValue, incarnation: string): SessionRow {
  return {
    serializedKey: serializeSessionKey(key),
    key: cloneKey(key),
    ...cloneValue(value),
    token: { namespaceIncarnation: incarnation, sessionRevision: 1 },
  };
}

function sameStrings(left: readonly string[] | undefined, right: readonly string[] | undefined): boolean {
  const a = left ?? [];
  const b = right ?? [];
  return a.length === b.length && a.every((value, index) => value === b[index]);
}

// Absent and explicit-empty mentions are distinct editor values; a mismatch on
// either side refuses. Elements compare by kind/name/offset.
function sameMentions(left: SkillEditorValue["mentions"], right: SubmittedDraft["mentions"]): boolean {
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

function sameSubmittedValue(row: SessionRow, submitted: SubmittedDraft): boolean {
  return (
    row.text === submitted.text &&
    sameStrings(row.skillNames, submitted.skillNames) &&
    sameStrings(row.commandNames, submitted.commandNames) &&
    sameMentions(row.mentions, submitted.mentions)
  );
}

function serializedTransferKey(namespace: BrowserDraftKey["namespace"], legacyKey: string): string {
  return JSON.stringify([namespace.kind, namespace.origin, legacyKey]);
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
      const row = await readSessionRow(transaction, key);
      if (!row) return null;
      const namespace = await readNamespaceRow(transaction, key.namespace);
      const incarnation = namespace?.incarnation;
      if (incarnation === undefined || row.token.namespaceIncarnation === incarnation) return publicSessionRow(row);
      // Lazy re-token: adopt the current incarnation while preserving the session
      // revision and the complete ordinary value.
      const refreshed: SessionRow = { ...row, token: { ...row.token, namespaceIncarnation: incarnation } };
      await requestResult(putSessionRow(transaction, refreshed));
      changed.push(key);
      return publicSessionRow(refreshed);
    });
    this.#publish(changed);
    return draft;
  }

  async seedDraft(key: BrowserDraftKey, value: SkillEditorValue): Promise<DraftCommit> {
    try {
      const changed: BrowserDraftKey[] = [];
      const result = await this.#write(DRAFT_STORES, "seedDraft", async (transaction) => {
        const existing = await readSessionRow(transaction, key);
        if (existing) return { state: "conflict", current: publicSessionRow(existing) } as const;
        const incarnation = await this.#currentIncarnation(transaction, key.namespace);
        const row = newSessionRow(key, value, incarnation);
        await requestResult(transaction.objectStore(COMPOSER_DRAFTS_STORE).add(row));
        changed.push(key);
        return { state: "committed", draft: publicSessionRow(row) } as const;
      });
      this.#publish(changed);
      return result;
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
      const result = await this.#write(DRAFT_STORES, "commitEdit", async (transaction) => {
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
        const next = nextSessionRow(row, value);
        await requestResult(putSessionRow(transaction, next));
        changed.push(key);
        return { state: "committed", draft: publicSessionRow(next) } as const;
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
        if (!sameSubmittedValue(row, submitted)) {
          return { cleared: false, draftUnchanged, persistence: "committed" } as const;
        }
        const cleared = nextSessionRow(row, { text: "", skillNames: [] });
        await requestResult(putSessionRow(transaction, cleared));
        changed.push(key);
        return { cleared: true, draftUnchanged: true, persistence: "committed" } as const;
      });
      this.#publish(changed);
      return result;
    } catch {
      return { cleared: false, draftUnchanged, persistence: "pending" };
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
            await requestResult(drafts.add(newSessionRow(incoming.key, incoming.value, incarnation)));
            changed.push(incoming.key);
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
        draftKey: serializeSessionKey(draftKey),
        legacyKeyV1,
        legacyKeyV2,
        legacyV1,
        structuredV2,
        value: decodeSupportedComposerDraft(structuredV2, legacyV1),
      };
    });
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
    const current = await readNamespaceRow(transaction, namespace);
    if (current) return current.incarnation;
    const row = { namespaceKey: serializeNamespace(namespace), incarnation: createSecureUUID() } satisfies NamespaceRow;
    await requestResult(transaction.objectStore(ANNOTATION_NAMESPACES_STORE).add(row));
    return row.incarnation;
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
    // Observe the completion rejection immediately so a request error that aborts
    // the transaction before `await body` returns is never an unhandled rejection.
    // The real failure still propagates through the later `await completed`, and
    // atomic rollback and `tryAbortTransaction` are unchanged.
    void completed.catch(() => undefined);
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
