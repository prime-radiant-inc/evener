import { backgroundTranscriptOptions } from "./backgroundTranscriptOptions";
import type { SessionEntryCache } from "./sessionEntryCache";
import { TranscriptPayloadBudget } from "./transcriptPayloadBudget";

/**
 * One retained body the entry write path holds in memory: a transcript item row,
 * a turn shell, or a preserved ended-round overlay body. Identity is the object
 * itself, so a body shared by two accounting sites is charged once, while two
 * independently decoded copies of the same transcript key are two distinct
 * objects and are charged separately. A record stores only the identity and a
 * byte scalar; it never holds a copy of the body.
 */
export interface EntryRetainedBody {
  readonly key: object;
  readonly bytes: number;
}

export type EntryWorkKind = "writing" | "retry" | "ended" | "background";

export interface EntryWriteAdmissionSnapshot {
  limit: number;
  /** Aggregate bytes charged to the one queue; excludes the oversize exception. */
  retainedBytes: number;
  /** Distinct identity count backing `retainedBytes`. */
  retainedIdentities: number;
  /** Separately accounted bytes of the one exclusive oversize batch, if any. */
  exemptBytes: number;
  /** Whether the one exclusive oversize batch is currently in flight. */
  exemptActive: boolean;
  /** Whether the single background drain slot is currently held. */
  backgroundInFlight: boolean;
  backgroundWaiters: number;
}

interface RetainedRecord {
  bytes: number;
  /** Per-owner acquisition count, so shared identity survives a partial release. */
  owners: Map<object, number>;
  kind: EntryWorkKind;
  /**
   * Whether this body's bytes sit outside the bounded aggregate because its
   * retention could not be refused (a preserved ended handoff body the model
   * still displays). Such bytes are still reported by `retainedBytes`.
   */
  overLimit: boolean;
}

interface BackgroundWaiter {
  owner: object;
  foreground: () => boolean;
  resume: () => void;
}

interface ExemptBatch {
  owner: object;
  bytes: number;
  keys: object[];
}

/**
 * The shared admission and drain for completed entry work. One instance covers
 * every entry repository that writes through the same cache, which is the
 * store's unit of aggregation: queued, in-flight, retry-retained and ended
 * handoff bodies all count against one pending budget
 * (`backgroundTranscriptOptions().pendingBytes`, 4 MiB).
 *
 * Two exclusive resources live here. The single background slot runs one
 * database batch at a time across those repositories for background work (the
 * retry drain and no-reader accepted writes); a repository with live reader
 * demand commits immediately, which is the accepted foreground exception and
 * keeps a stalled background batch from blocking healthy work. The oversize
 * exception is the *sole* immediate bounded batch: only one may be in flight at
 * a time, its bytes are accounted separately, and it is mutually exclusive with
 * the background slot so neither can overlap the other.
 */
export class EntryWriteAdmission {
  readonly #budget: TranscriptPayloadBudget;
  readonly #retained = new Map<object, RetainedRecord>();
  /** Bytes retained beyond the bound because their retention cannot be refused. */
  #overage = 0;
  #exempt: ExemptBatch | undefined;
  #backgroundOwner: object | undefined;
  readonly #waiters: BackgroundWaiter[] = [];
  #waking = false;

  constructor(limit: number) {
    this.#budget = new TranscriptPayloadBudget(limit);
  }

  get limit(): number {
    return this.#budget.limit;
  }

  get retainedBytes(): number {
    return this.#budget.used + this.#overage;
  }

  /**
   * Reserve the charge for a set of retained bodies under one owner. Distinct
   * keys already present are shared identity: they are not charged again, and
   * the owner acquires one count of each. Two entries for the same new key in
   * one call are also charged once. The reservation is all-or-nothing. With
   * `force`, a retention that cannot be refused (a preserved ended handoff body
   * the model keeps for display) is charged beyond the bound rather than
   * silently dropped, so the reported aggregate stays honest.
   *
   * A plain reservation also accounts for bytes already held beyond the bound:
   * forced retention reduces the room an ordinary batch may take, so a body
   * retained over the limit cannot hide an ordinary batch from the bound, and
   * releasing the forced bytes restores the room exactly.
   */
  retain(bodies: readonly EntryRetainedBody[], owner: object, kind: EntryWorkKind, force = false): boolean {
    const fresh = new Map<object, number>();
    for (const body of bodies) {
      if (!this.#retained.has(body.key) && !fresh.has(body.key)) fresh.set(body.key, body.bytes);
    }
    let needed = 0;
    for (const bytes of fresh.values()) needed += bytes;
    let overLimit = false;
    if (needed > 0) {
      if (this.#budget.used + this.#overage + needed <= this.#budget.limit) {
        this.#budget.reserve(needed);
      } else if (force) {
        this.#overage += needed;
        overLimit = true;
      } else {
        return false;
      }
    }
    const seen = new Set<object>();
    for (const body of bodies) {
      if (seen.has(body.key)) continue;
      seen.add(body.key);
      const record = this.#retained.get(body.key);
      if (record) {
        record.owners.set(owner, (record.owners.get(owner) ?? 0) + 1);
        // A later acquisition may tighten the kind for observability only.
        if (record.kind === "ended" && kind !== "ended") record.kind = kind;
      } else {
        this.#retained.set(body.key, {
          bytes: fresh.get(body.key) ?? body.bytes,
          owners: new Map([[owner, 1]]),
          kind,
          overLimit,
        });
      }
    }
    return true;
  }

  /** Release one acquisition per distinct body key from `owner`. */
  release(bodies: readonly EntryRetainedBody[], owner: object): void {
    const seen = new Set<object>();
    for (const body of bodies) {
      if (seen.has(body.key)) continue;
      seen.add(body.key);
      this.#releaseKeyForOwner(body.key, owner);
    }
  }

  releaseKey(key: object, owner: object): void {
    this.#releaseKeyForOwner(key, owner);
  }

  #releaseKeyForOwner(key: object, owner: object): void {
    const record = this.#retained.get(key);
    if (!record) return;
    const count = record.owners.get(owner) ?? 0;
    if (count <= 0) return;
    if (count > 1) {
      record.owners.set(owner, count - 1);
      return;
    }
    record.owners.delete(owner);
    if (record.owners.size === 0) {
      this.#retained.delete(key);
      if (record.overLimit) this.#overage -= record.bytes;
      else this.#budget.release(record.bytes);
    }
  }

  /** Release every acquisition held by one owner, for cancel or dispose. */
  releaseOwner(owner: object): void {
    for (const [key, record] of [...this.#retained]) {
      while (record.owners.has(owner)) this.#releaseKeyForOwner(key, owner);
    }
    this.cancelBackground(owner);
  }

  /**
   * Take the single over-capacity immediate batch. Returns false when another is
   * already in flight, so the caller must refuse the competing response for
   * caching rather than starting a second unbounded immediate write.
   */
  retainExempt(bodies: readonly EntryRetainedBody[], owner: object): boolean {
    if (this.#exempt !== undefined || this.#backgroundOwner !== undefined) return false;
    let bytes = 0;
    const keys: object[] = [];
    for (const body of bodies) {
      bytes += body.bytes;
      keys.push(body.key);
    }
    this.#exempt = { owner, bytes, keys };
    return true;
  }

  releaseExempt(owner: object): void {
    if (this.#exempt?.owner !== owner) return;
    this.#exempt = undefined;
    this.#wakeNext();
  }

  get exemptBytes(): number {
    return this.#exempt?.bytes ?? 0;
  }

  get exemptActive(): boolean {
    return this.#exempt !== undefined;
  }

  isRetained(key: object): boolean {
    return this.#retained.has(key) || (this.#exempt?.keys.includes(key) ?? false);
  }

  /** The retained identities owned by one repository, as real object references. */
  retainedKeysFor(owner: object): object[] {
    const keys: object[] = [];
    for (const [key, record] of this.#retained) {
      if (record.owners.has(owner)) keys.push(key);
    }
    return keys;
  }

  /**
   * Take the single background drain slot. Returns true when this owner may run
   * a background batch now, or false when it must wait for `resume` to be called
   * when the slot is handed over.
   */
  tryAcquireBackground(owner: object, foreground: () => boolean, resume: () => void): boolean {
    if (this.#backgroundOwner === owner) return true;
    if (this.#backgroundOwner === undefined && this.#exempt === undefined) {
      this.#backgroundOwner = owner;
      return true;
    }
    if (!this.#waiters.some((waiter) => waiter.owner === owner)) {
      this.#waiters.push({ owner, foreground, resume });
    }
    return false;
  }

  /**
   * Release the slot and hand it to the highest-priority waiter: a repository
   * with live reader demand first, otherwise the longest-waiting one. The slot
   * is cleared before the waiter runs, so a waiter that lost its demand and
   * declines does not strand it; the next waiter is then woken.
   */
  releaseBackground(owner: object): void {
    if (this.#backgroundOwner !== owner) return;
    this.#backgroundOwner = undefined;
    this.#wakeNext();
  }

  #wakeNext(): void {
    if (this.#waking) return;
    this.#waking = true;
    try {
      while (this.#backgroundOwner === undefined && this.#exempt === undefined && this.#waiters.length > 0) {
        let best = 0;
        for (let index = 1; index < this.#waiters.length; index += 1) {
          const candidate = this.#waiters[index];
          const incumbent = this.#waiters[best];
          if (candidate && incumbent && candidate.foreground() && !incumbent.foreground()) best = index;
        }
        const next = this.#waiters.splice(best, 1)[0];
        if (!next) return;
        next.resume();
      }
    } finally {
      this.#waking = false;
    }
  }

  /**
   * Remove `owner` from the waiter queue. It never releases a slot the owner is
   * actually holding: an issued background batch keeps its slot until its real
   * terminal outcome releases it.
   */
  cancelBackground(owner: object): void {
    const index = this.#waiters.findIndex((waiter) => waiter.owner === owner);
    if (index >= 0) this.#waiters.splice(index, 1);
  }

  snapshotForTesting(): EntryWriteAdmissionSnapshot {
    return {
      limit: this.#budget.limit,
      retainedBytes: this.retainedBytes,
      retainedIdentities: this.#retained.size,
      exemptBytes: this.exemptBytes,
      exemptActive: this.#exempt !== undefined,
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
