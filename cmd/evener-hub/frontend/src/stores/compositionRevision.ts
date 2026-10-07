// Transaction-local helpers for the composition stores: the pure serialized
// keys, the durable session-token comparison, and the read / initialize / put
// primitives the inactive browser draft repository shares with the outbox and
// append writers. Every function that touches IndexedDB runs inside a caller's
// IDBTransaction and issues no transaction of its own; only the key
// serializers and nextSessionRow are pure.
//
// A session row carries the durable `sessionRevision` that orders a session's
// explicit writes. Transfer is initialization, not an edit, so an absent row
// is created at revision 1; every ordinary write goes through nextSessionRow,
// which advances the revision by exactly one whether or not the value changed.
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

export function serializeSessionKey(key: BrowserDraftKey): string {
  return JSON.stringify([key.namespace.kind, key.namespace.origin, key.sessionRef]);
}

export function serializeNamespace(namespace: BrowserDraftKey["namespace"]): string {
  return JSON.stringify([namespace.kind, namespace.origin]);
}

export function readNamespaceRow(
  transaction: IDBTransaction,
  namespace: BrowserDraftKey["namespace"],
): Promise<NamespaceRow | undefined> {
  return requestResult<NamespaceRow | undefined>(
    transaction.objectStore(ANNOTATION_NAMESPACES_STORE).get(serializeNamespace(namespace)),
  );
}

export function readSessionRow(transaction: IDBTransaction, key: BrowserDraftKey): Promise<SessionRow | undefined> {
  return requestResult<SessionRow | undefined>(
    transaction.objectStore(COMPOSER_DRAFTS_STORE).get(serializeSessionKey(key)),
  );
}

// PURE: returns a fresh row whose ordinary value is `value` when supplied and
// the row's own value otherwise, with the session revision advanced by one.
// Explicit-undefined metadata stays absent; present metadata is cloned so the
// result never aliases the caller's arrays.
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

export function sameSessionToken(left: DraftSessionToken, right: DraftSessionToken): boolean {
  return left.namespaceIncarnation === right.namespaceIncarnation && left.sessionRevision === right.sessionRevision;
}

// Issues only the put; the caller owns the transaction and observes completion.
export function putSessionRow(transaction: IDBTransaction, row: SessionRow): IDBRequest<IDBValidKey> {
  return transaction.objectStore(COMPOSER_DRAFTS_STORE).put(row);
}

// Guarantees a durable session row for `key`, initializing an absent one at
// revision 1 with an empty ordinary value after transfer. The namespace
// incarnation is created on demand exactly as every other writer does, so a
// freshly initialized row already carries the current incarnation.
export async function ensureSessionRow(transaction: IDBTransaction, key: BrowserDraftKey): Promise<SessionRow> {
  const existing = await readSessionRow(transaction, key);
  if (existing) return existing;
  const namespaceKey = serializeNamespace(key.namespace);
  const namespaceStore = transaction.objectStore(ANNOTATION_NAMESPACES_STORE);
  const current = await requestResult<NamespaceRow | undefined>(namespaceStore.get(namespaceKey));
  const incarnation = current?.incarnation ?? createSecureUUID();
  if (!current) {
    await requestResult(namespaceStore.put({ namespaceKey, incarnation } satisfies NamespaceRow));
  }
  const row: SessionRow = {
    serializedKey: serializeSessionKey(key),
    key: { namespace: { kind: "browser", origin: key.namespace.origin }, sessionRef: key.sessionRef },
    text: "",
    skillNames: [],
    token: { namespaceIncarnation: incarnation, sessionRevision: 1 },
  };
  await requestResult(putSessionRow(transaction, row));
  return row;
}
