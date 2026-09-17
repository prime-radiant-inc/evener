// The mutation state layer, published as
// `@evener/appwire-client/state/mutation`: the durable record shapes both
// apps' outboxes store, the provenance rule their projections ask, and the
// discovery half of the outbox itself over a storage port, and the dispatcher
// that turns a stored record into one attempt at a time with no blind replay.
// The storage adapter and every React binding stay in the apps — this subpath
// is what a record IS, how waiting work gets noticed, and what may be done
// about it, not where it lives.

export type { MutationDispatcherOptions } from "./dispatcher";
export { MutationDispatcher } from "./dispatcher";
export type {
  MutationDiscoveryReason,
  MutationLifecycleTarget,
  MutationOutboxChannel,
  MutationOutboxOptions,
  MutationOutboxStorage,
  MutationVisibilityTarget,
} from "./outbox";
export { MutationOutbox } from "./outbox";
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
