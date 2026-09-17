// The mutation state layer, published as
// `@evener/appwire-client/state/mutation`: the durable record shapes both
// apps' outboxes store, the provenance rule their projections ask, the
// discovery half of the outbox itself over a storage port, and the pure
// reconciliation that turns durable records plus a live model into the rows a
// composer's queue renders. The storage adapter, the dispatcher's scheduling and
// every React binding stay in the apps — this subpath is what a record IS, how
// waiting work gets noticed, and how it reconciles, not where it lives.

export type {
  MutationDiscoveryReason,
  MutationLifecycleTarget,
  MutationOutboxChannel,
  MutationOutboxOptions,
  MutationOutboxStorage,
  MutationVisibilityTarget,
} from "./outbox";
export { MutationOutbox } from "./outbox";
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
