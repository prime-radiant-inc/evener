import { WireError } from "@evener/appwire-client";
import { MutationStorageError, MutationStorageTimeoutError } from "./mutationOutboxIndexedDB";

export class ConflictError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "ConflictError";
  }
}

/** A user's Stop landed while a resume was pending, so the resume and the
 * send it carried were canceled rather than delivered. */
export class StopCanceledError extends Error {
  constructor() {
    super("Stop canceled this pending action; send again when ready.");
    this.name = "StopCanceledError";
  }
}

// mapConflict recognizes the WireError shape the daemon uses for a lost turn
// CAS (turn/start, turn/steer, turn/queue, turn/interrupt) or a stale/raced
// escalation resolve: code -32013 with
// data.evenerErrorInfo === "conflict" (appwire.Conflict(), appwire/errors.go).
// The discriminator is the evenerErrorInfo string, not the code alone — code
// -32013 is also used by appwire.QueuedDrainPartial with a different
// evenerErrorInfo, which must NOT map to ConflictError. Any other rejection
// (a different WireError, RequestTimeoutError, ConnectionClosedError, ...)
// passes through unchanged.
export function mapConflict(err: unknown): Error {
  if (err instanceof WireError && err.evenerErrorInfo === "conflict") {
    return new ConflictError(err.message);
  }
  return err instanceof Error ? err : new Error(String(err));
}

// Whether the IndexedDB watchdog gave up on an open or a transaction: storage
// that may only be slow, so a durable write is worth a second attempt.
export function isStorageTimeout(error: unknown): boolean {
  return error instanceof MutationStorageTimeoutError;
}

// The DOMException names IndexedDB uses when storage itself refuses: a full
// origin, a browser that denies this origin storage, a schema version another
// tab moved, a connection closed under the transaction, a transaction the
// browser aborted, an internal failure. Names that judge the record written
// (DataError, DataCloneError, ConstraintError: its key, its payload, a
// duplicate id) or that only this code's own bugs raise (NotFoundError for a
// missing store, TransactionInactiveError for a request on a finished
// transaction) are absent on purpose. An AbortError cannot stand in for a
// record judgment: the outbox adapter races a transaction's body against its
// completion, so a body that throws rejects with its own error before the
// abort it causes is reported, and IDBConnection rejects an open whose schema
// upgrade threw with IDBUpgradeError, not the abort.
const STORAGE_REFUSAL_NAMES: ReadonlySet<string> = new Set([
  "QuotaExceededError",
  "SecurityError",
  "VersionError",
  "InvalidStateError",
  "UnknownError",
  "AbortError",
]);

// Whether an outbox operation failed because storage did, not because of what
// it was asked to write: the adapter's own storage errors (a timeout, a
// refused, blocked or superseded open) or a storage refusal IndexedDB raised.
// Only these earn the send fallback and the storage-blocked reconcile record;
// the outbox's own validation errors keep their ordinary meaning. IndexedDB's
// are matched by name because DOMExceptions from another realm fail
// instanceof.
export function isStorageFailure(error: unknown): boolean {
  if (error instanceof MutationStorageError) return true;
  const name = typeof error === "object" && error !== null ? (error as { name?: unknown }).name : undefined;
  return typeof name === "string" && STORAGE_REFUSAL_NAMES.has(name);
}

// FRAME_TIMES_WINDOW_MS matches widgets/cadence's own WINDOW_MS exactly
// (the trace it renders) so the ring never evicts a sample Cadence would
// still want to show; FRAME_TIMES_MAX_ENTRIES is an independent cap purely
// against runaway growth during a high-frequency notification burst within
// that same 60s window (a long-lived, mostly-idle thread's ring stays far
// under 64 on the window alone).
export const FRAME_TIMES_WINDOW_MS = 60_000;
export const FRAME_TIMES_MAX_ENTRIES = 64;

// appendFrameTime is a pure ring-buffer step: append `now`, evict anything
// older than the trace window (mirroring Cadence's own ticksFor exclusion,
// `age > WINDOW_MS`, so the two boundaries agree exactly), then cap at
// FRAME_TIMES_MAX_ENTRIES, keeping the most recent. `times` need not be
// sorted (Cadence's own frameTimes prop doc says the same) - this never
// re-sorts, only filters and slices.
export function appendFrameTime(times: number[], now: number): number[] {
  const kept = times.filter((t) => now - t <= FRAME_TIMES_WINDOW_MS);
  const next = [...kept, now];
  return next.length > FRAME_TIMES_MAX_ENTRIES ? next.slice(next.length - FRAME_TIMES_MAX_ENTRIES) : next;
}
