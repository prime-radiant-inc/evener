// The mutation state layer, published as
// `@evener/appwire-client/state/mutation`: the durable record shapes both
// apps' outboxes store, the provenance rule their projections ask, and the
// pure reconciliation that turns durable records plus a live model into the
// rows a composer's queue renders. The storage, the dispatcher's scheduling
// and every React binding stay in the apps — this subpath is what a record
// IS and how it reconciles, not where it lives.

export type { PendingMethod, PendingTurnEntry, PendingTurnState } from "./pendingEntries";
export { reconcilePendingEntries } from "./pendingEntries";
export type {
  ClientIdentity,
  ClientIdentityStorage,
  MutationAttachmentRef,
  MutationIntent,
  MutationOptimisticRecord,
  MutationOutboxRecord,
  MutationOutboxState,
  MutationRecord,
  MutationRecoveryKind,
  MutationRecoveryRecord,
} from "./records";
export { createClientIdentity } from "./records";
export type { SecureRandomSource } from "./secureUUID";
export { createSecureUUID } from "./secureUUID";
