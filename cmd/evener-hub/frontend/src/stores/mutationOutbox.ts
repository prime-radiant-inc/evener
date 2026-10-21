import type {
  MutationAttachmentRef,
  MutationCommit as PackageMutationCommit,
  MutationIntent as PackageMutationIntent,
  MutationOptimisticRecord as PackageMutationOptimisticRecord,
  MutationOutboxRecord as PackageMutationOutboxRecord,
  MutationRecord as PackageMutationRecord,
  MutationRecoveryRecord as PackageMutationRecoveryRecord,
} from "@evener/appwire-client/state/mutation";

export type { MutationOutboxState, MutationRecoveryKind } from "@evener/appwire-client/state/mutation";

// The web is the one host with attachment bytes to stage: the package's
// record shapes carry only the identifying metadata (marker pairs an
// attachment back to its "[image N]" composerText anchor), and this adds the
// Blob no other host has.
export interface MutationAttachment extends MutationAttachmentRef {
  blob: Blob;
}

export type MutationIntent = PackageMutationIntent<MutationAttachment>;
export type MutationRecord = PackageMutationRecord<MutationAttachment>;
export type MutationOutboxRecord = PackageMutationOutboxRecord<MutationAttachment>;
export type MutationOptimisticRecord = PackageMutationOptimisticRecord<MutationAttachment>;
export type MutationRecoveryRecord = PackageMutationRecoveryRecord<MutationAttachment>;
export type MutationCommit = PackageMutationCommit<MutationAttachment>;

// The caller's durable-enqueue admission refused the write. It is not a
// storage failure: the row was never added, so the transaction aborts and the
// caller decides whether the refusal is a stale activation (Retry) or a
// surface error (everyone else).
export class MutationAdmissionLostError extends Error {
  constructor() {
    super("The submission's source is no longer current");
    this.name = "MutationAdmissionLostError";
  }
}

// The class itself is the package's (state/mutation/outbox.ts): discovery over
// a storage port, with every host-shaped capability an option. This module is
// the web's side of that — the attachment type with bytes in it (above), and
// the re-export every importer here already names.
export type {
  MutationClientLookup,
  MutationEnqueueAdmission,
  MutationOutboxOptions,
  MutationStopBarrier,
} from "@evener/appwire-client/state/mutation";
export { acceptedRecord, carriesOptimisticInput, MutationOutbox } from "@evener/appwire-client/state/mutation";
