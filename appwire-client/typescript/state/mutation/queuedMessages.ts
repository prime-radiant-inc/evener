import type { InputItem } from "../../types.gen";
import { COMPOSER_METHODS, PENDING_TURN_METHODS } from "./composerMethods";
import type { MutationStopBarrier, MutationStopCapture } from "./outbox";
import type { MutationAttachmentRef, MutationIntent, MutationOutboxRecord, MutationRecoveryRecord } from "./records";

// Optional composition ownership actions, separate from the dispatcher's port.
// Lost claims resolve absent/false. Storage failures reject, never enqueue a fallback.
export interface QueuedMessageStorage<A extends MutationAttachmentRef = MutationAttachmentRef> {
  getOutboxWithStopEpoch(id: string): Promise<{ record: MutationOutboxRecord<A> | undefined; stopEpoch: number }>;
  editQueuedMessage(
    expected: MutationOutboxRecord<A>,
    barrier: MutationStopBarrier,
  ): Promise<MutationRecoveryRecord<A> | undefined>;
  removeQueuedMessage(expected: MutationOutboxRecord<A>, barrier: MutationStopBarrier): Promise<boolean>;
  resendRecovery(
    sourceIds: string | readonly string[],
    intent: MutationIntent<A>,
    barrier: MutationStopBarrier | undefined,
    expectedRecoveryRevisions?: Readonly<Record<string, number>>,
  ): Promise<MutationOutboxRecord<A> | undefined>;
}

// The web recovery fence's storage surface. The base contract above stays the
// whole shared surface - hosts without displayed-ref rows implement it alone
// and are never forced to grow a web-only fence. The web runtime's retargeted
// resends cross a still-displayed ref whose Stop epoch the click must capture
// alongside the destination's, and its blockedUnknown rows release only
// through the explicit-Retry reauthorization, so the web store declares this
// extension and the conformance suite's web describe pins it.
export interface WebQueuedMessageStorage<A extends MutationAttachmentRef = MutationAttachmentRef>
  extends QueuedMessageStorage<A> {
  getOutboxWithStopEpoch(
    id: string,
  ): Promise<{ record: MutationOutboxRecord<A> | undefined; stopEpoch: number; displayedStopEpoch: number }>;
  reauthorizeBlockedMutation(id: string, barrier: MutationStopCapture): Promise<boolean>;
}

// Called only on a durable read inside the host's write transaction. The
// policy is checked separately against that same row, never the rendered copy.
export function matchesQueuedMessageSnapshot(record: MutationOutboxRecord, expected: MutationOutboxRecord): boolean {
  return (
    record.clientMutationId === expected.clientMutationId &&
    record.targetRef === expected.targetRef &&
    record.instanceId === expected.instanceId &&
    record.threadId === expected.threadId &&
    record.method === expected.method &&
    record.state === expected.state &&
    record.intentSequence === expected.intentSequence
  );
}

function hasContradictoryAttemptEvidence(record: MutationOutboxRecord): boolean {
  return record.attempted === true || (record.unresolvedAttempts !== undefined && record.unresolvedAttempts > 0);
}

function isSafelyWaiting(record: MutationOutboxRecord): boolean {
  if (hasContradictoryAttemptEvidence(record)) return false;
  if (record.state === "canceled") return true;
  if (record.state !== "submitting") return false;
  return record.attempted === false || record.unresolvedAttempts === 0;
}

function recordInput(record: MutationOutboxRecord): InputItem[] {
  return Array.isArray(record.payload.input) ? (record.payload.input as InputItem[]) : [];
}

function hasComposerInput(record: MutationOutboxRecord): boolean {
  return recordInput(record).some((item) => {
    if (item.type === "image") return true;
    if (item.type === "text") return typeof item.text === "string" && item.text.trim() !== "";
    if (item.type === "skill" || item.type === "command")
      return typeof item.name === "string" && item.name.trim() !== "";
    return false;
  });
}

export function canEditQueuedMessage(record: MutationOutboxRecord): boolean {
  if (!isSafelyWaiting(record)) return false;
  if (
    record.method === COMPOSER_METHODS.send ||
    record.method === COMPOSER_METHODS.queue ||
    record.method === COMPOSER_METHODS.steer
  )
    return true;
  return record.method === COMPOSER_METHODS.drain && hasComposerInput(record);
}

export function canRemoveQueuedMessage(record: MutationOutboxRecord): boolean {
  return isSafelyWaiting(record) && PENDING_TURN_METHODS.includes(record.method);
}
