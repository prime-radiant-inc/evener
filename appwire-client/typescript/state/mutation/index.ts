// The mutation state layer, published as
// `@evener/appwire-client/state/mutation`: the durable record shapes both
// apps' outboxes store, the provenance rule their projections ask, the
// discovery half of the outbox itself over a storage port, the dispatcher
// that turns a stored record into one attempt at a time with no blind
// replay, the pure reconciliation that turns durable records plus a live
// model into the rows a composer's queue renders, and the pending-turns
// projection store built on that reconciliation. The storage adapter and
// every React binding stay in the apps — this subpath is what a record IS,
// how waiting work gets noticed, what may be done about it, how it
// reconciles, and what a host's own store of them tracks, not where any of
// it lives.

export type { MutationDispatcherOptions } from "./dispatcher";
export { MutationDispatcher, validConsumedClientMutationIds } from "./dispatcher";
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
  PendingTurnsDraftPort,
  PendingTurnsState,
  PendingTurnsStore,
  PendingTurnsStoreDeps,
  PendingTurnsThreadsPort,
  SubmittedDraft,
} from "./pendingTurns";
export { createPendingTurnsStore } from "./pendingTurns";
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
