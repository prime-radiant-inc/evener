// The mutation state layer, published as
// `@evener/appwire-client/state/mutation`: the durable record shapes both
// apps' outboxes store, the provenance rule their projections ask, and the
// discovery half of the outbox itself over a storage port. The storage
// adapter, the dispatcher's scheduling and every React binding stay in the
// apps — this subpath is what a record IS and how waiting work gets noticed,
// not where it lives.

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
export { isOwnMutationRecord, ownClientId, setMutationClientIdentityForTests } from "./records";
