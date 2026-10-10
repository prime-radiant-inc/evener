import { backgroundTranscriptOptions } from "./backgroundTranscriptOptions";
import type { SessionEntryCache } from "./sessionEntryCache";
import { TranscriptPayloadBudget } from "./transcriptPayloadBudget";

/**
 * One retained body the entry write path holds in memory: a transcript item row
 * or a turn shell. Identity is the object itself, so a body shared by two
 * accounting sites (the repository's pending map and the batch it feeds, or a
 * batch and its retry entry) is charged once, while two independently decoded
 * copies of the same transcript key are two distinct objects and are charged
 * separately. The record stores only the identity and a byte scalar; it never
 * holds a copy of the body.
 */
export interface EntryRetainedBody {
  readonly key: object;
  readonly bytes: number;
}

export type EntryWorkKind = "writing" | "retry" | "exempt";

export interface EntryWriteAdmissionSnapshot {
  limit: number;
  /** Aggregate bytes charged to the one queue; excludes the oversize exception. */
  retainedBytes: number;
  /** Distinct identity count backing `retainedBytes`. */
  retainedIdentities: number;
  /** Distinct identity count of the separately-accounted oversize exception. */
  exemptIdentities: number;
  /** Whether the single background drain slot is currently held. */
  backgroundInFlight: boolean;
  backgroundWaiters: number;
}

interface RetainedRecord {
  bytes: number;
  owner: object;
  kind: EntryWorkKind;
}

interface BackgroundWaiter {
  owner: object;
  foreground: () => boolean;
  resume: () => void;
}

/**
 * The shared admission and drain for completed entry work. One instance covers
 * every entry repository that writes through the same cache, which is the
 * store's unit of aggregation: queued, in-flight and retry-retained bodies all
 * count against one pending budget (`backgroundTranscriptOptions().pendingBytes`,
 * 4 MiB), and the background retry drain runs at most one database batch at a
 * time across those repositories.
 *
 * Ordinary foreground writes do not take the background slot, so a stalled or
 * failed repository whose retry is in flight cannot block unrelated healthy
 * work; only bounded retried batches share the single slot, and among waiting
 * repositories one with live reader demand is served first.
 */
export class EntryWriteAdmission {
  readonly #budget: TranscriptPayloadBudget;
  readonly #retained = new Map<object, RetainedRecord>();
  readonly #exempt = new Map<object, object>();
  #backgroundOwner: object | undefined;
  readonly #waiters: BackgroundWaiter[] = [];

  constructor(limit: number) {
    this.#budget = new TranscriptPayloadBudget(limit);
  }

  get limit(): number {
    return this.#budget.limit;
  }

  get retainedBytes(): number {
    return this.#budget.used;
  }

  /** Reserve the charge for a set of retained bodies under one owner. */
  retain(bodies: readonly EntryRetainedBody[], owner: object, kind: EntryWorkKind): boolean {
    let needed = 0;
    for (const body of bodies) {
      if (!this.#retained.has(body.key)) needed += body.bytes;
    }
    if (needed > 0 && !this.#budget.reserve(needed)) return false;
    for (const body of bodies) {
      if (!this.#retained.has(body.key)) this.#retained.set(body.key, { bytes: body.bytes, owner, kind });
    }
    return true;
  }

  /**
   * Track the one oversize response the accepted contracts permit as its own
   * immediate bounded batch. It is accounted separately, so it never inflates
   * the aggregate and is never staged for retry.
   */
  retainExempt(bodies: readonly EntryRetainedBody[], owner: object): void {
    for (const body of bodies) this.#exempt.set(body.key, owner);
  }

  release(bodies: readonly EntryRetainedBody[]): void {
    for (const body of bodies) this.releaseKey(body.key);
  }

  releaseKey(key: object): void {
    this.#exempt.delete(key);
    const record = this.#retained.get(key);
    if (!record) return;
    this.#retained.delete(key);
    this.#budget.release(record.bytes);
  }

  /** Release every body retained under one owner, for cancel or dispose. */
  releaseOwner(owner: object): void {
    for (const [key, record] of [...this.#retained]) {
      if (record.owner === owner) this.releaseKey(key);
    }
    for (const [key, exemptOwner] of [...this.#exempt]) {
      if (exemptOwner === owner) this.#exempt.delete(key);
    }
    this.cancelBackground(owner);
  }

  /**
   * Take the single background drain slot. Returns true when this owner may run
   * a retried batch now, or false when it must wait for `resume` to be called.
   */
  tryAcquireBackground(owner: object, foreground: () => boolean, resume: () => void): boolean {
    if (this.#backgroundOwner === owner) return true;
    if (this.#backgroundOwner === undefined) {
      this.#backgroundOwner = owner;
      return true;
    }
    if (!this.#waiters.some((waiter) => waiter.owner === owner)) {
      this.#waiters.push({ owner, foreground, resume });
    }
    return false;
  }

  /**
   * Release the slot to the highest-priority waiter: a repository with live
   * reader demand first, otherwise the longest-waiting one (fair across
   * sessions).
   */
  releaseBackground(owner: object): void {
    if (this.#backgroundOwner !== owner) return;
    this.#backgroundOwner = undefined;
    if (this.#waiters.length === 0) return;
    let best = 0;
    for (let index = 1; index < this.#waiters.length; index += 1) {
      const candidate = this.#waiters[index];
      const incumbent = this.#waiters[best];
      if (candidate && incumbent && candidate.foreground() && !incumbent.foreground()) best = index;
    }
    const next = this.#waiters.splice(best, 1)[0];
    if (!next) return;
    this.#backgroundOwner = next.owner;
    next.resume();
  }

  cancelBackground(owner: object): void {
    const index = this.#waiters.findIndex((waiter) => waiter.owner === owner);
    if (index >= 0) this.#waiters.splice(index, 1);
    if (this.#backgroundOwner === owner) this.releaseBackground(owner);
  }

  isRetained(key: object): boolean {
    return this.#retained.has(key) || this.#exempt.has(key);
  }

  /** The retained identities owned by one repository, as real object references. */
  retainedKeysFor(owner: object): object[] {
    const keys: object[] = [];
    for (const [key, record] of this.#retained) {
      if (record.owner === owner) keys.push(key);
    }
    return keys;
  }

  snapshotForTesting(): EntryWriteAdmissionSnapshot {
    return {
      limit: this.#budget.limit,
      retainedBytes: this.#budget.used,
      retainedIdentities: this.#retained.size,
      exemptIdentities: this.#exempt.size,
      backgroundInFlight: this.#backgroundOwner !== undefined,
      backgroundWaiters: this.#waiters.length,
    };
  }
}

const admissionsByCache = new WeakMap<SessionEntryCache, EntryWriteAdmission>();

/**
 * The admission shared by every entry repository writing through the same
 * cache. It is created lazily so the pending limit is read from the options
 * installed when the store (or a test) builds its first repository.
 */
export function entryWriteAdmissionFor(cache: SessionEntryCache): EntryWriteAdmission {
  let admission = admissionsByCache.get(cache);
  if (!admission) {
    admission = new EntryWriteAdmission(backgroundTranscriptOptions().pendingBytes);
    admissionsByCache.set(cache, admission);
  }
  return admission;
}

/** @internal Forget a cache's shared admission, for test isolation. */
export function resetEntryWriteAdmissionForTests(cache: SessionEntryCache): void {
  admissionsByCache.delete(cache);
}
