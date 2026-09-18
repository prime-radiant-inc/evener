// The pending-turns store's per-target durable-record replacement, the port
// shape a durable read of them takes, and the durable-read fence: which of a
// refresh's targets its snapshot may still replace once that read resolves.
//
// A refresh can name one target or every currently-tracked one ("all
// targets"), and refreshes overlap: a slow all-targets read can still be in
// flight when a specific target's own faster read - or a live commit that
// never reads at all - lands a newer generation for that one target. The
// rule is symmetric and generation-only, never "all beats specific" or the
// reverse: whichever generation is highest for a target wins, and an
// all-targets refresh's generation counts against every target's own floor
// exactly as a specific refresh's does. `createMutationProjectionFence`
// tracks nothing but that ordering; the durable read itself is the injected
// `MutationPersistencePort`.

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

// The targets an accepted refresh may replace, alongside the snapshot it
// read them from.
export interface MutationProjectionRefresh<A extends MutationAttachmentRef = MutationAttachmentRef> {
  targets: ReadonlySet<string>;
  snapshot: MutationPersistenceSnapshot<A>;
}

export interface MutationProjectionFence<A extends MutationAttachmentRef = MutationAttachmentRef> {
  // This fence's current epoch: bumped by `reset`, and readable so a caller
  // with its own longer-lived operation (a submission) can compare its own
  // epoch snapshot against it later without the fence naming that operation.
  epoch(): number;
  // Runs one refresh through `port`: assigns it a new generation, reads,
  // then decides which of its targets that generation may still replace.
  // Returns false when this fence has been `reset` since the refresh
  // started - the read's result must be discarded entirely, not partially
  // applied - or when the port's read itself rejected.
  refresh(port: MutationPersistencePort<A>, ref?: string): Promise<MutationProjectionRefresh<A> | false>;
  // Registers `ref`'s next generation as already consumed with no durable
  // read at all - a live commit that writes the record directly and must
  // still out-rank a slower in-flight refresh of the same or an older
  // generation.
  advance(ref: string): void;
  // Invalidates every refresh in flight and forgets every tracked
  // generation (a client swap, or a test's own reset).
  reset(): void;
}

export function createMutationProjectionFence<
  A extends MutationAttachmentRef = MutationAttachmentRef,
>(): MutationProjectionFence<A> {
  let refreshGeneration = 0;
  let allTargetsRefreshGeneration = 0;
  const refreshGenerations = new Map<string, number>();
  let refreshEpoch = 0;

  return {
    epoch: () => refreshEpoch,

    async refresh(port, ref) {
      const epoch = refreshEpoch;
      const generation = ++refreshGeneration;
      if (ref === undefined) allTargetsRefreshGeneration = generation;
      else refreshGenerations.set(ref, generation);
      try {
        const snapshot = await port.read(ref);
        if (refreshEpoch !== epoch) return false;
        // Reads of all targets and reads of one target share the same
        // ordering. An old all-target snapshot must not erase a newer
        // local commit.
        const targets = new Set(
          ref === undefined
            ? [
                ...refreshGenerations.keys(),
                ...snapshot.outbox.map((record) => record.targetRef),
                ...snapshot.optimistic.map((record) => record.targetRef),
                ...snapshot.recovery.map((record) => record.targetRef),
              ]
            : [ref],
        );
        for (const target of targets) {
          if (generation < Math.max(allTargetsRefreshGeneration, refreshGenerations.get(target) ?? 0)) {
            targets.delete(target);
          } else {
            refreshGenerations.set(target, generation);
          }
        }
        return { targets, snapshot };
      } catch {
        // A read failure cannot discard the last durable projection.
        // Lifecycle discovery or the next explicit action retries the same
        // read.
        return false;
      }
    },

    advance(ref) {
      refreshGenerations.set(ref, ++refreshGeneration);
    },

    reset() {
      refreshEpoch += 1;
      refreshGeneration = 0;
      allTargetsRefreshGeneration = 0;
      refreshGenerations.clear();
    },
  };
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
