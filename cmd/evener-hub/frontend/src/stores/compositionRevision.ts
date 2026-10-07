// Transaction-local helpers for the shared composition stores: the serialized
// keys the `composerDrafts`/`annotationNamespaces` object stores are keyed by,
// the boundary that first initializes a session row's metadata, and the pure
// step every writer uses to advance it. Each helper takes an already-open
// idb transaction and never opens its own; the repository (and, later, the
// outbox writers) compose them inside one transaction.
import type { DraftSessionToken } from "@evener/appwire-client/state/mutation";
import type { SkillEditorValue } from "../panes/session/composer/skillDocument";
import type { BrowserDraftKey, StoredComposerDraft } from "./browserDraftRepository";
import { requestResult } from "./idbConnection";
import { ANNOTATION_NAMESPACES_STORE, COMPOSER_DRAFTS_STORE } from "./localCompositionSchema";
import { createSecureUUID } from "./secureUUID";

export interface NamespaceRow {
  namespaceKey: string;
  incarnation: string;
}

export type SessionRow = StoredComposerDraft & { serializedKey: string };

export function serializeNamespace(namespace: BrowserDraftKey["namespace"]): string {
  return JSON.stringify([namespace.kind, namespace.origin]);
}

// The existing JSON [kind, origin, sessionRef] session key.
export function serializeSessionKey(key: BrowserDraftKey): string {
  return JSON.stringify([key.namespace.kind, key.namespace.origin, key.sessionRef]);
}

export function sameSessionToken(left: DraftSessionToken, right: DraftSessionToken): boolean {
  return left.namespaceIncarnation === right.namespaceIncarnation && left.sessionRevision === right.sessionRevision;
}

async function readRow<T>(tx: IDBTransaction, store: string, key: string): Promise<T | undefined> {
  return requestResult<T | undefined>(tx.objectStore(store).get(key));
}

// Reads only: no row is created or refreshed.
export async function readNamespaceRow(
  tx: IDBTransaction,
  namespace: BrowserDraftKey["namespace"],
): Promise<NamespaceRow | undefined> {
  return readRow<NamespaceRow>(tx, ANNOTATION_NAMESPACES_STORE, serializeNamespace(namespace));
}

// Reads only: no row is created or refreshed.
export async function readSessionRow(tx: IDBTransaction, key: BrowserDraftKey): Promise<SessionRow | undefined> {
  return readRow<SessionRow>(tx, COMPOSER_DRAFTS_STORE, serializeSessionKey(key));
}

// Returns the existing namespace row or creates one, initializing the
// incarnation the first writer of this namespace will key its token to.
export async function ensureNamespaceRow(
  tx: IDBTransaction,
  namespace: BrowserDraftKey["namespace"],
): Promise<NamespaceRow> {
  const existing = await readNamespaceRow(tx, namespace);
  if (existing) return existing;
  const row: NamespaceRow = { namespaceKey: serializeNamespace(namespace), incarnation: createSecureUUID() };
  await requestResult(tx.objectStore(ANNOTATION_NAMESPACES_STORE).add(row));
  return row;
}

// Initializes absent session metadata at revision 1. Returns the existing row
// unchanged when one is already durable - an existing revision, including an
// explicit empty draft, wins over migration input.
export async function ensureSessionRow(tx: IDBTransaction, key: BrowserDraftKey): Promise<SessionRow> {
  const existing = await readSessionRow(tx, key);
  if (existing) return existing;
  const namespace = await ensureNamespaceRow(tx, key.namespace);
  const row: SessionRow = {
    serializedKey: serializeSessionKey(key),
    key: { namespace: { kind: "browser", origin: key.namespace.origin }, sessionRef: key.sessionRef },
    text: "",
    skillNames: [],
    token: { namespaceIncarnation: namespace.incarnation, sessionRevision: 1 },
  };
  await requestResult(putSessionRow(tx, row));
  return row;
}

// Pure: replaces the ordinary value only when one is supplied, otherwise
// preserves it, and always advances the revision by exactly one. The
// caller's `row` is never mutated.
export function nextSessionRow(row: SessionRow, value?: SkillEditorValue): SessionRow {
  const ordinary = value ?? row;
  return {
    serializedKey: row.serializedKey,
    key: row.key,
    text: ordinary.text,
    skillNames: [...ordinary.skillNames],
    ...(ordinary.commandNames === undefined ? {} : { commandNames: [...ordinary.commandNames] }),
    ...(ordinary.mentions === undefined ? {} : { mentions: ordinary.mentions.map((mention) => ({ ...mention })) }),
    token: { namespaceIncarnation: row.token.namespaceIncarnation, sessionRevision: row.token.sessionRevision + 1 },
  };
}

// Only issues the put; it performs no reads and opens no transaction of its own.
export function putSessionRow(tx: IDBTransaction, row: SessionRow): IDBRequest<IDBValidKey> {
  return tx.objectStore(COMPOSER_DRAFTS_STORE).put(row);
}
