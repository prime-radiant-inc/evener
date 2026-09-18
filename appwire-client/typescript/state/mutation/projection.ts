// The pending-turns store's per-target durable-record replacement, and the
// port shape a durable read of them takes. A target's whole set of records
// is always replaced together, never merged field by field - matching a
// durable read's own unit of truth: a snapshot for a target is everything
// known about it, not a diff against what a store already held.

import type {
  MutationAttachmentRef,
  MutationOptimisticRecord,
  MutationOutboxRecord,
  MutationRecoveryRecord,
} from "./records";

// The one thing a durable read answers, independent of storage mechanism
// (IndexedDB on the web): every outbox/optimistic/recovery record known for
// `ref`, or for every ref when `ref` is omitted.
export interface MutationPersistenceSnapshot<A extends MutationAttachmentRef = MutationAttachmentRef> {
  outbox: MutationOutboxRecord<A>[];
  optimistic: MutationOptimisticRecord<A>[];
  recovery: MutationRecoveryRecord<A>[];
}

// What a durable read needs from the host's storage. The web's own
// `readMutationPersistence` already has this exact shape.
export interface MutationPersistencePort<A extends MutationAttachmentRef = MutationAttachmentRef> {
  read(ref?: string): Promise<MutationPersistenceSnapshot<A>>;
}

// Replaces every record `targets` names with `records`' own for that target,
// and drops every existing record of `current`'s whose target is in
// `targets` but has no replacement in `records` - a target's whole set of
// records is always replaced together, never merged field by field.
export function replaceTargetRecords<T extends { clientMutationId: string; targetRef: string }>(
  current: Map<string, T>,
  targets: ReadonlySet<string>,
  records: T[],
): Map<string, T> {
  const next = new Map(current);
  for (const [id, record] of next) {
    if (targets.has(record.targetRef)) next.delete(id);
  }
  for (const record of records) {
    if (targets.has(record.targetRef)) next.set(record.clientMutationId, record);
  }
  return next;
}
