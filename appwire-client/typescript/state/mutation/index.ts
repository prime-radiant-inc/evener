// The mutation state layer, published as
// `@evener/appwire-client/state/mutation`: the durable record shapes both
// apps' outboxes store and the provenance rule their projections ask. The
// storage, the dispatcher's scheduling and every React binding stay in the
// apps — this subpath is what a record IS, not where it lives.
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
