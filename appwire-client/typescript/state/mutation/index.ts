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

export type { MutationCommit, MutationCommitFeed } from "./commitFeed";
export { wireMutationCommitFeed } from "./commitFeed";
export type { MutationDispatchClientLookup, MutationDispatcherOptions } from "./dispatcher";
export { MutationDispatcher, validConsumedClientMutationIds } from "./dispatcher";
export type {
  MutationClientLookup,
  MutationDiscoveryReason,
  MutationLifecycleTarget,
  MutationOutboxChannel,
  MutationOutboxOptions,
  MutationOutboxStorage,
  MutationStopBarrier,
  MutationVisibilityTarget,
} from "./outbox";
export { isClientReady, MutationOutbox } from "./outbox";
export type { PendingMethod, PendingTurnEntry, PendingTurnState } from "./pendingEntries";
export {
  imagePlaceholder,
  normalizeText,
  pendingEntryPreview,
  queueEntryPreviewText,
  reconcilePendingEntries,
  reflectedMutationIds,
  skillMarkers,
  truncateForDisplay,
} from "./pendingEntries";
export type {
  PendingTurnsDraftPort,
  PendingTurnsState,
  PendingTurnsStore,
  PendingTurnsStoreDeps,
  PendingTurnsThreadsPort,
  SubmittedDraft,
} from "./pendingTurns";
export {
  awaitingFirstFrameSend,
  blockedEntries,
  createPendingTurnsStore,
  outboxEntriesByState,
  recoveryEntries,
} from "./pendingTurns";
export type {
  MutationPersistencePort,
  MutationPersistenceSnapshot,
  MutationProjectionFence,
  MutationProjectionRefresh,
} from "./projection";
export { createMutationProjectionFence, replaceTargetRecords } from "./projection";
export type { MutationProjectionWorkPorts, MutationProjectionWorkTracker } from "./projectionWork";
export { createMutationProjectionWorkTracker } from "./projectionWork";
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
export type { MutationSubmissionCommitted, MutationSubmissionOptions } from "./submission";
export { createSubmissionRunner, submitWithPendingTracking } from "./submission";
