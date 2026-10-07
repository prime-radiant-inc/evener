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

export interface BrowserDraftKey {
  namespace: { kind: "browser"; origin: string };
  sessionRef: string;
}

// One session's latest committed explicit draft. The token's sessionRevision is
// the ordering authority; there is no persistent pane owner or backing counter
// on an ordinary row.
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

export function browserDraftKey(sessionRef: string, origin: string = location.origin): BrowserDraftKey {
  return { namespace: { kind: "browser", origin }, sessionRef };
}

// Captures the durable session token plus the caller's backing identity and
// local edit generation. Freezing the copy means a later mutation of the source
// row cannot retroactively authorize a stale cleanup.
export function captureDraftSettlementGuard(
  row: StoredComposerDraft,
  backing: DraftBackingIdentity,
  localGeneration: number,
): DraftSettlementGuard {
  return Object.freeze({
    token: Object.freeze(cloneToken(row.token)),
    backing: Object.freeze({ ...backing }),
    localGeneration,
  });
}

// The writers that still exist after owner/claim/adopt/conflict removal.
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
  legacyKeyV1: string;
  legacyKeyV2: string;
  legacyV1: string | null;
  structuredV2: string | null;
  value: SkillEditorValue;
}

const STORAGE_WAIT_MS = 10_000;
const DRAFT_STORES = [COMPOSER_DRAFTS_STORE, ANNOTATION_NAMESPACES_STORE] as const;
const TRANSFER_STORES = [COMPOSER_DRAFTS_STORE, DRAFT_TRANSFERS_STORE, ANNOTATION_NAMESPACES_STORE] as const;

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

function cloneToken(token: DraftSessionToken): DraftSessionToken {
  return { namespaceIncarnation: token.namespaceIncarnation, sessionRevision: token.sessionRevision };
}

function publicDraft(row: SessionRow): StoredComposerDraft {
  const { serializedKey: _serializedKey, ...draft } = row;
  return { ...draft, ...cloneValue(draft), key: cloneKey(draft.key), token: cloneToken(draft.token) };
}

function sameStrings(left: readonly string[] | undefined, right: readonly string[] | undefined): boolean {
  const a = left ?? [];
  const b = right ?? [];
  return a.length === b.length && a.every((value, index) => value === b[index]);
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

function sameValue(left: SkillEditorValue, right: SkillEditorValue): boolean {
  return (
    left.text === right.text &&
    sameStrings(left.skillNames, right.skillNames) &&
    sameStrings(left.commandNames, right.commandNames) &&
    sameMentions(left.mentions, right.mentions)
  );
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
      const incarnation = await this.#currentIncarnation(transaction, key.namespace);
      if (row.token.namespaceIncarnation === incarnation) return publicDraft(row);
      // Lazy re-token: only the incarnation is refreshed, so a namespace reset
      // invalidates captured tokens without moving the durable revision or the
      // value a captured stale cleanup would otherwise still match.
      const refreshed: SessionRow = { ...row, token: { ...row.token, namespaceIncarnation: incarnation } };
      await requestResult(putSessionRow(transaction, refreshed));
      changed.push(key);
      return publicDraft(refreshed);
    });
    this.#publish(changed);
    return draft;
  }

  async seedDraft(key: BrowserDraftKey, value: SkillEditorValue): Promise<DraftCommit> {
    try {
      const changed: BrowserDraftKey[] = [];
      const result = await this.#write(DRAFT_STORES, "seedDraft", async (transaction) => {
        const existing = await readSessionRow(transaction, key);
        if (existing) return { state: "conflict", current: publicDraft(existing) } as const;
        const incarnation = await this.#currentIncarnation(transaction, key.namespace);
        const row: SessionRow = {
          serializedKey: serializeSessionKey(key),
          key: cloneKey(key),
          ...cloneValue(value),
          token: { namespaceIncarnation: incarnation, sessionRevision: 1 },
        };
        await requestResult(putSessionRow(transaction, row));
        changed.push(key);
        return { state: "committed", draft: publicDraft(row) } as const;
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
        const row = await readSessionRow(transaction, key);
        const namespace = await readNamespaceRow(transaction, key.namespace);
        if (!row || !namespace) return { state: "unavailable" } as const;
        if (
          expected.namespaceIncarnation !== namespace.incarnation ||
          row.token.namespaceIncarnation !== namespace.incarnation ||
          expected.sessionRevision !== row.token.sessionRevision
        ) {
          return { state: "conflict", current: publicDraft(row) } as const;
        }
        if (!stillAuthorized()) return { state: "unavailable" } as const;
        // Explicit identical values still advance: the revision is the ordering
        // authority, not a value-change counter.
        const next = nextSessionRow(row, value);
        await requestResult(putSessionRow(transaction, next));
        changed.push(key);
        return { state: "committed", draft: publicDraft(next) } as const;
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
        const namespace = await readNamespaceRow(transaction, key.namespace);
        const guard = submitted.settlementGuard;
        draftUnchanged =
          guard !== undefined &&
          namespace !== undefined &&
          row.token.namespaceIncarnation === namespace.incarnation &&
          sameSessionToken(row.token, guard.token);
        if (!draftUnchanged || !submitted.stillOwnsEdit?.()) {
          return { cleared: false, draftUnchanged, persistence: "committed" } as const;
        }
        const submittedValue: SkillEditorValue = {
          text: submitted.text,
          skillNames: [...submitted.skillNames],
          ...(submitted.commandNames === undefined ? {} : { commandNames: [...submitted.commandNames] }),
          ...(submitted.mentions === undefined
            ? {}
            : { mentions: submitted.mentions.map((mention) => ({ ...mention })) }),
        };
        if (!sameValue(row, submittedValue)) {
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
        const transfers = transaction.objectStore(DRAFT_TRANSFERS_STORE);
        for (const incoming of inspected) {
          // Initialization only: any durable row, including an explicit empty
          // draft, wins over migration input, and no losing copy is archived.
          const existing = await readSessionRow(transaction, incoming.key);
          if (!existing) {
            const row: SessionRow = {
              serializedKey: serializeSessionKey(incoming.key),
              key: cloneKey(incoming.key),
              ...cloneValue(incoming.value),
              token: { namespaceIncarnation: incarnation, sessionRevision: 1 },
            };
            await requestResult(putSessionRow(transaction, row));
            changed.push(incoming.key);
          }
          await requestResult(
            transfers.put({
              serializedKey: serializedTransferKey(namespace, incoming.legacyKeyV1),
              namespaceKey: serializeNamespace(namespace),
              legacyKey: incoming.legacyKeyV1,
              inspectedBytes: incoming.legacyV1,
            } satisfies DraftTransferRow),
          );
          await requestResult(
            transfers.put({
              serializedKey: serializedTransferKey(namespace, incoming.legacyKeyV2),
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
    // Markers are durable now; cleanup only removes bytes still identical to
    // what this pass inspected, so a value changed under us is re-inspected.
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
      return {
        key: { namespace: { ...namespace }, sessionRef },
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
      // IndexedDB already holds the authoritative row and the inspected-byte markers.
    }
  }

  async #currentIncarnation(transaction: IDBTransaction, namespace: BrowserDraftKey["namespace"]): Promise<string> {
    const current = await readNamespaceRow(transaction, namespace);
    if (current) return current.incarnation;
    const row = { namespaceKey: serializeNamespace(namespace), incarnation: createSecureUUID() } satisfies NamespaceRow;
    await requestResult(transaction.objectStore(ANNOTATION_NAMESPACES_STORE).put(row));
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
