import {
  type AnyNotification,
  applyNotification,
  applyOverlayEndForTranscriptHandoff,
  applyReadResponse,
  type CachedSessionItem,
  comparePositions,
  decodeTranscriptItem,
  encodeTranscriptItem,
  hydrateThread,
  type ItemModel,
  invalidateHistory,
  mergeOlderItemPage,
  mergeTranscriptItemVersioned,
  mergeTranscriptReaderWindow,
  mergeTranscriptTurnVersioned,
  pruneBackgroundHistoryBodies,
  type ThreadItem,
  type ThreadItemPosition,
  type ThreadModel,
  type ThreadReadResponse,
  type ThreadTurnsListResponse,
  type TurnModel,
} from "@evener/appwire-client";
import { type EntryRetainedBody, type EntryWriteAdmission, entryWriteAdmissionFor } from "./entryWriteAdmission";
import type { TranscriptItemRow, TranscriptResolvedIdentity, TranscriptSessionMetadata } from "./sessionCacheSchema";
import { entryHistoryKey } from "./sessionCacheSchema";
import type { EntryCoverageInput, SessionEntryCache } from "./sessionEntryCache";
import { encodedPayloadBytes } from "./transcriptPayloadBudget";

/**
 * A nonce unique to this browser lifetime (this page's module evaluation). The
 * baseline identity is minted from the tab-local connection lifetime and source
 * revision, but the lifetime counter resets with the page: without a
 * per-browser-lifetime nonce a reload mints the same token as the previous page
 * and could continue its durable page-chaining certificates. The store may
 * supply its own nonce through the binding so a replacement is explicit.
 */
const PAGE_LIFETIME_NONCE = (() => {
  try {
    const crypto = globalThis.crypto;
    if (typeof crypto?.randomUUID === "function") return crypto.randomUUID();
    if (typeof crypto?.getRandomValues === "function") {
      const bytes = new Uint8Array(16);
      crypto.getRandomValues(bytes);
      return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
    }
  } catch {
    // A locked-down page has no random source; a per-load time+math fallback
    // still separates reloads for the lifetime of one page.
  }
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
})();

function comparePosition(
  left: { entry: number; item: number; sub?: number },
  right: { entry: number; item: number; sub?: number },
): number {
  return left.entry - right.entry || left.item - right.item || (left.sub ?? 0) - (right.sub ?? 0);
}

/**
 * The certified extent an abandoned write would have contributed. A snapshot
 * length alone cannot identify covered work: a read is keyed by its own range
 * and a page certificate by its own range, and either can return `extended` at
 * the same snapshot count. The `start`/`end` positions identify the work within
 * this history, so a later certificate carrying the same extent proves repair.
 */
interface AbandonedCoverage {
  start: ThreadItemPosition | null;
  end: ThreadItemPosition | null;
  snapshot: number;
}

function coverageReach(coverage: EntryCoverageInput | undefined): AbandonedCoverage {
  return {
    start: coverage?.start ?? null,
    end: coverage?.end ?? null,
    snapshot: coverage?.representedSnapshot ?? 0,
  };
}

/** The tightest extent that contains both reaches, so a wider one is never lost. */
function widerReach(left: AbandonedCoverage, right: AbandonedCoverage): AbandonedCoverage {
  const start =
    left.start === null
      ? right.start
      : right.start === null
        ? left.start
        : comparePosition(right.start, left.start) < 0
          ? right.start
          : left.start;
  const end =
    left.end === null
      ? right.end
      : right.end === null
        ? left.end
        : comparePosition(right.end, left.end) > 0
          ? right.end
          : left.end;
  return { start, end, snapshot: Math.max(left.snapshot, right.snapshot) };
}

/**
 * Whether a commit's own certificate covers the abandoned reach. With a
 * positional extent, the abandoned range must lie within the commit's covered
 * range AND the commit must reach at least the abandoned snapshot: a position
 * identifies a slot, not a version, so an older version committed at the same
 * position does not carry a newer abandoned version forward. A certificate with
 * no positional extent can only be compared by snapshot length.
 */
function commitCarriesCoverage(coverage: EntryCoverageInput, reach: AbandonedCoverage): boolean {
  if (reach.start !== null && reach.end !== null) {
    if (coverage.start === null || coverage.end === null) return false;
    return (
      comparePosition(coverage.start, reach.start) <= 0 &&
      comparePosition(coverage.end, reach.end) >= 0 &&
      coverage.representedSnapshot >= reach.snapshot
    );
  }
  return coverage.representedSnapshot >= reach.snapshot;
}

/**
 * Whether `inner` lies within `outer`, so a reconciliation for `outer` answers
 * it. As with `commitCarriesCoverage`, the positions must contain `inner` AND the
 * snapshot must reach it: a position identifies a slot, not a version, so a newer
 * obligation at the same positions is not already answered.
 */
function reachWithin(outer: AbandonedCoverage, inner: AbandonedCoverage): boolean {
  if (inner.start !== null && inner.end !== null) {
    if (outer.start === null || outer.end === null) return false;
    return (
      comparePosition(outer.start, inner.start) <= 0 &&
      comparePosition(outer.end, inner.end) >= 0 &&
      outer.snapshot >= inner.snapshot
    );
  }
  return outer.snapshot >= inner.snapshot;
}

/**
 * The real body identities a batch retains in memory, keyed by the row/turn
 * object so a body shared between the pending map and its batch, or between a
 * batch and its retry entry, is charged once. Only scalars are computed here;
 * no copy of a body is kept.
 */
function retainedBodies(batch: Parameters<SessionEntryCache["commitBatch"]>[0]): EntryRetainedBody[] {
  const bodies: EntryRetainedBody[] = [];
  for (const row of batch.items) bodies.push({ key: row, bytes: encodedPayloadBytes(row) });
  for (const turn of batch.turns ?? []) bodies.push({ key: turn, bytes: encodedPayloadBytes(turn) });
  return bodies;
}

export interface TranscriptRepositoryBinding {
  ref: string;
  threadId: string;
  historyKey: string;
  identity: TranscriptResolvedIdentity;
  clearEpoch: number;
  /**
   * The durable per-history deletion generation this binding captured when it
   * began owning the identity. A batch captured before a `deleteHistory` of the
   * same identity carries the old value and is refused by the cache's
   * history fence; a genuinely post-delete acquisition reads the new value
   * first and may cache. Absent means 0.
   */
  historyGeneration?: number;
  /**
   * A browser-lifetime nonce. The baseline identity is minted from the
   * tab-local connection lifetime and source revision, but that lifetime
   * counter resets with the page: without a per-browser-lifetime nonce a
   * reload could mint the same baseline token as the previous page and
   * continue its durable page-chaining certificates. The store supplies the
   * nonce it minted for this page; absent, the repository mints one at module
   * load, which still separates reloads in production.
   */
  sessionNonce?: string;
  /**
   * Whether this binding's history is preferred for disk retention. Supplied
   * from real Live/demand state; the cache consults it when choosing an
   * eviction victim while it fits.
   */
  isPreferred?: () => boolean;
  lifetime: number;
  seedModel?: ThreadModel;
  seedAuthority?: {
    trustedCurrentSource: true;
    bootGeneration: string;
    epoch: number;
    incarnation: string;
    snapshotLength: number;
  };
  now?: () => number;
  /**
   * The paced-retry scheduler for this write path. When absent, a transient
   * write failure is retried on a bounded exponential timer. This route is
   * fixture-gated and the store does not currently pass a scheduler, so the
   * default timer is what runs today; the seam exists so tests can pace and
   * hold callbacks. Returns a cancel.
   */
  retryScheduler?: (attempt: number, retry: () => void) => () => void;
  /**
   * The shared aggregate admission and background drain. When absent, the
   * repository joins the admission shared by every entry repository writing
   * through the same cache, so queued and retry-retained completed work counts
   * against one 4 MiB queue. The seam lets a test drive one repository's view
   * in isolation.
   */
  admission?: EntryWriteAdmission;
  /**
   * A paced reconciliation request. When an aggregate overflow drops accepted
   * cache work, the repository cannot itself issue an authoritative read, so it
   * asks its owner (the store) to reconcile. Absent, an overflow still breaks
   * readiness honestly and the breaker waits for changed eligible content.
   */
  reconcile?: () => void;
}

export type TranscriptReaderStatus = "current" | "stale" | "recovery-needed";

export interface TranscriptReaderResult {
  model?: ThreadModel;
  status: TranscriptReaderStatus;
  boundary?: number;
  before?: ThreadItemPosition;
  olderCursor?: string;
}

export interface TranscriptReader {
  openLocal(limit: number): Promise<TranscriptReaderResult>;
  getSnapshot(): ThreadModel | undefined;
  /** @internal Reports retained body ownership without exposing payloads. */
  getRetentionSnapshotForTesting(): {
    openingModelRetained: boolean;
    pendingPayloadCount: number;
    cachedPayloadCount: number;
  };
  dispose(): void;
}

interface ReaderOpeningState {
  model?: ThreadModel;
  pendingRows: TranscriptItemRow<CachedSessionItem>[];
  cacheRows: TranscriptItemRow<unknown>[];
  boundary: number;
  committedBoundary?: string;
  acceptedCoverageBoundary?: string;
  readId?: string;
  continuityId?: string;
  committedSnapshot?: number;
  sourceRevision: number;
  hadPendingWrites: boolean;
  authorityValid: boolean;
}

/** Owns real wire acceptance, publication, local writes and attached readers for one binding. */
export class TranscriptRepository {
  readonly #cache: SessionEntryCache;
  readonly #admission: EntryWriteAdmission;
  readonly #binding: Omit<TranscriptRepositoryBinding, "seedModel">;
  readonly #listeners = new Set<() => void>();
  readonly #writes = new Set<Promise<void>>();
  readonly #pending = new Map<string, TranscriptItemRow<CachedSessionItem>>();
  #model: ThreadModel | undefined;
  #boundary = 0;
  #sourceRevision = 0;
  #baselineIdentity = "";
  #readerCount = 0;
  #coverageBoundary: string | undefined;
  #coverageReadId: string | undefined;
  #continuityId: string | undefined;
  #coverageStart: { entry: number; item: number; sub?: number } | null = null;
  #coverageEnd: { entry: number; item: number; sub?: number } | null = null;
  #representedSnapshot = 0;
  #committedCoverageBoundary: string | undefined;
  #committedCoverageSnapshot: number | undefined;
  #committedCoverageReadId: string | undefined;
  #committedCoverageContinuityId: string | undefined;
  #seedSnapshot: number | undefined;
  #seedAuthorityInvalid = false;
  #recoveryNeeded = false;
  #disposed = false;
  #invalidated = false;
  #cacheContinuityLost = false;
  // Transiently failed batches are retained in a queue and retried in order on
  // bounded backoff while a reader holds demand. A second failed batch is
  // deferred, not discarded; each entry remembers the source revision it was
  // issued under, so a boundary (reconnect/reconciliation) revokes an obsolete
  // retry instead of letting it overwrite the current committed coverage.
  #retryQueue: Array<{
    batch: Parameters<SessionEntryCache["commitBatch"]>[0];
    revision: number;
    bodies: EntryRetainedBody[];
  }> = [];
  #retryScheduled = false;
  #retryInFlight = false;
  // One repository runs at most one background database batch at a time, even
  // though the shared slot would admit a re-entrant same-owner acquisition.
  #backgroundInFlight = false;
  #retryCancel: (() => void) | undefined;
  #retryAttempt = 0;
  // One paced reconciliation callback after an aggregate overflow. It is armed
  // once and re-arms only on a later overflow, so repeated opens or unchanged
  // live deltas cannot start a repair loop; changed eligible content is
  // reconsidered through the ordinary accept path.
  #reconcileArmed = false;
  #reconcileCancel: (() => void) | undefined;
  // The abandoned reach a reconciliation was last requested for. Re-feeding the
  // same rejected content does not request another: only a wider obligation (or
  // one re-broken after a covering commit cleared it) arms afresh, so repeated
  // callback/read feedback is not a repair loop.
  #reconciledReach: AbandonedCoverage | undefined;
  // Accepted writes from a repository with no reader demand wait for the single
  // shared background slot instead of issuing immediately. Each carries the
  // bodies it retains and a settlement promise so `settled()` joins it.
  #backgroundAccepts: Array<{
    batch: Parameters<SessionEntryCache["commitBatch"]>[0];
    revision: number;
    bodies: EntryRetainedBody[];
  }> = [];
  // The bodies of commits actually issued and not yet settled. They stay charged
  // until their real terminal outcome, even across invalidate/dispose, so
  // retiring publication authority cannot free capacity a worker still holds.
  readonly #inFlight = new Map<Parameters<SessionEntryCache["commitBatch"]>[0], EntryRetainedBody[]>();
  // Completed ended-round overlay bodies preserved for handoff (uncovered by
  // recorded history). They are display-only but retained in memory, so they
  // count against the aggregate until pruned, invalidated or disposed.
  readonly #handoffOverlay = new Map<string, object>();
  // Rounds that have ended for the entry handoff. The preserved ended bodies
  // are the model overlay items whose round is in this set, so the inventory can
  // traverse the real overlay rather than only the charged registrations.
  readonly #endedRounds = new Set<string>();
  // Explicit disposition of recovery work that will never run: a cancelled
  // (invalidate/dispose/last-reader) or unadmitted (no demand) failed batch.
  // Settlement must report it rather than resolving as void success. It records
  // the abandoned work's *identity and extent* (its certified start/end range,
  // not a length), because an accepted certificate is window-specific and a
  // different window at the same snapshot count is not a repair. This is a
  // single fixed-shape hull: recording a later abandonment can only widen it,
  // never lower it, so no smaller or later abandonment can erase an earlier,
  // larger one, and the state cannot grow without limit (it holds no item
  // bodies or models, only a kind and two positions).
  #recoveryAbandoned: { kind: "cancelled" | "failed"; reach: AbandonedCoverage } | undefined;
  // The unregister for this binding's real-demand preference registration with
  // the shared cache, released on disposal so a replacement owner is the one
  // whose demand the eviction policy consults.
  #unregisterPreferred: (() => void) | undefined;

  constructor(cache: SessionEntryCache, binding: TranscriptRepositoryBinding) {
    if (entryHistoryKey(binding.identity) !== binding.historyKey) throw new Error("binding history identity mismatch");
    const { seedModel, ...scalarBinding } = binding;
    this.#cache = cache;
    this.#admission = binding.admission ?? entryWriteAdmissionFor(cache);
    this.#binding = scalarBinding;
    this.#baselineIdentity = this.#mintBaseline();
    if (binding.isPreferred) {
      this.#unregisterPreferred = cache.registerPreferredHistory(binding.historyKey, binding.isPreferred);
    }
    if (seedModel) {
      const history = seedModel.history;
      const authority = binding.seedAuthority;
      this.#seedAuthorityInvalid =
        authority?.trustedCurrentSource !== true ||
        seedModel.ref !== binding.ref ||
        seedModel.threadId !== binding.threadId ||
        !history ||
        authority.incarnation !== binding.identity.incarnation ||
        authority.incarnation !== history.incarnation ||
        authority.bootGeneration !== history.bootGeneration ||
        authority.epoch !== history.epoch ||
        authority.snapshotLength !== history.length ||
        history?.invalidatedAtGeneration !== undefined ||
        history?.failed !== undefined;
      if (!this.#seedAuthorityInvalid && authority) this.#seedSnapshot = authority.snapshotLength;
      this.#recoveryNeeded = this.#seedAuthorityInvalid;
      this.#model = this.#seedAuthorityInvalid ? invalidateHistory(seedModel) : seedModel;
    } else {
      this.#model = undefined;
    }
  }

  getSnapshot = (): ThreadModel | undefined => this.#model;

  /**
   * Opaque baseline identity for the accepted cache contract's `authority`.
   * It binds the tab-local connection/read lifetime to the repository's source
   * revision: reads and notifications from one uninterrupted connection share
   * one token, and an authoritative reconciliation or an identity change mints
   * a new one. Callers must treat it as opaque and never decode it.
   */
  getBaselineIdentity(): string {
    return this.#baselineIdentity;
  }

  /**
   * Marks a real connection/replacement/reconciliation boundary. The store
   * calls this when it invalidates held histories for a reconnect, because the
   * daemon can report the same boot generation, epoch and incarnation across a
   * gap. Outgoing page certificates then belong to the old baseline and cannot
   * certify the new interval. Same-connection reader close/reopen does not call
   * it, so that keeps the token.
   */
  beginFreshBaseline(): void {
    this.#advanceSourceRevision();
    // A real connection/reconciliation boundary retires the held history too.
    // A reconnect can report the same boot generation, epoch and incarnation
    // while entries were missed over the gap; merging the fresh read into
    // pre-gap rows would keep their old cursor and let renewed paging continue
    // from the pre-gap boundary. Marking the model invalid makes the next
    // accepted read replace it, so the fresh latest boundary and its returned
    // cursor win.
    if (this.#model) {
      this.#model = invalidateHistory(this.#model);
      this.#recoveryNeeded = true;
    }
  }

  /** The oldest recorded position this repository holds, or undefined. */
  oldestPosition(): ThreadItemPosition | undefined {
    const model = this.#model;
    return model ? this.#oldestPosition(model) : undefined;
  }

  /** @internal Reports ownership scalars without exposing or copying transcript bodies. */
  getRetentionSnapshotForTesting(): { bindingHasSeedModel: boolean } {
    const binding = this.#binding as TranscriptRepositoryBinding;
    return { bindingHasSeedModel: binding.seedModel !== undefined };
  }

  /**
   * @internal The aggregate admission scalars this repository sees in the shared
   * queue, paired with `retainedInventoryForTesting` so a test can observe the
   * bound against real retained references, not only a counter.
   */
  getAdmissionSnapshotForTesting(): {
    retainedBytes: number;
    retainedIdentities: number;
    exemptBytes: number;
    exemptActive: boolean;
    backgroundInFlight: boolean;
  } {
    const snapshot = this.#admission.snapshotForTesting();
    return {
      retainedBytes: snapshot.retainedBytes,
      retainedIdentities: snapshot.retainedIdentities,
      exemptBytes: snapshot.exemptBytes,
      exemptActive: snapshot.exemptActive,
      backgroundInFlight: snapshot.backgroundInFlight,
    };
  }

  /**
   * @internal The real retained body references the repository holds across its
   * own stores (pending rows, in-flight commits, queued retries, queued
   * background accepts and ended handoff overlays), independent of the
   * admission's accounting map and of the descriptor lists given to it: in-flight,
   * retry and background-accept bodies are read from the real batch objects, and
   * ended bodies from the real model overlay of rounds that ended. A test can
   * therefore inventory what is actually retained and cross-check the counters.
   */
  retainedInventoryForTesting(): {
    pendingRows: readonly object[];
    inFlightBodies: readonly object[];
    retryBodies: readonly object[];
    backgroundAcceptBodies: readonly object[];
    handoffOverlayBodies: readonly object[];
  } {
    const bodiesOf = (batch: Parameters<SessionEntryCache["commitBatch"]>[0]) => [
      ...batch.items,
      ...(batch.turns ?? []),
    ];
    const overlay = this.#model?.overlay ?? {};
    const handoffOverlayBodies: object[] = [];
    for (const item of Object.values(overlay)) {
      if (item.roundId !== undefined && this.#endedRounds.has(item.roundId)) handoffOverlayBodies.push(item);
    }
    return {
      pendingRows: [...this.#pending.values()],
      inFlightBodies: [...this.#inFlight.keys()].flatMap(bodiesOf),
      retryBodies: this.#retryQueue.flatMap((entry) => bodiesOf(entry.batch)),
      backgroundAcceptBodies: this.#backgroundAccepts.flatMap((entry) => bodiesOf(entry.batch)),
      handoffOverlayBodies,
    };
  }

  subscribe(listener: () => void): () => void {
    this.#listeners.add(listener);
    return () => this.#listeners.delete(listener);
  }

  /** Accept a complete wire read under its captured reader lifetime. */
  acceptRead(response: ThreadReadResponse, request: { lifetime: number; readId: string; boundary: string }): boolean {
    if (!this.#acceptsLifetime(request.lifetime) || !this.#matchesRead(response)) return false;
    const before = this.#model;
    const candidate = before
      ? applyReadResponse(before, response, this.#now())
      : (() => {
          const hydrated = hydrateThread(response, this.#binding.ref, this.#now());
          return response.changes
            ? applyNotification(
                hydrated,
                {
                  method: "history/updated",
                  params: {
                    ref: this.#binding.ref,
                    threadId: response.thread.id,
                    bootGeneration: response.bootGeneration ?? "",
                    epoch: response.epoch ?? 0,
                    snapshot: response.snapshot ?? { incarnation: this.#binding.identity.incarnation, length: 0 },
                    turns: response.changes.turns ?? [],
                    items: response.changes.items ?? [],
                  },
                },
                this.#now(),
              )
            : hydrated;
        })();
    if (candidate === before) return false;
    this.#model = candidate;
    const previousHistory = before?.history;
    const acceptedHistory = candidate.history;
    // An authoritative window may remove held items without changing source
    // identity, so an opening captured before it must not merge those items back.
    if (
      !previousHistory ||
      !acceptedHistory ||
      previousHistory.bootGeneration !== acceptedHistory.bootGeneration ||
      previousHistory.epoch !== acceptedHistory.epoch ||
      previousHistory.incarnation !== acceptedHistory.incarnation ||
      previousHistory.invalidatedAtGeneration !== undefined ||
      previousHistory.failed !== undefined ||
      response.authoritative === true
    ) {
      this.#advanceSourceRevision();
    }
    this.#boundary = Math.max(this.#boundary + 1, response.snapshot?.length ?? 0);
    this.#coverageBoundary = request.boundary;
    this.#coverageReadId = request.readId;
    // This token identifies the source authority and baseline that can support
    // uninterrupted suffixes; Snapshot.length is a separate byte boundary.
    this.#continuityId = JSON.stringify([
      this.#binding.lifetime,
      this.#binding.ref,
      this.#binding.threadId,
      this.#binding.historyKey,
      response.bootGeneration ?? "",
      response.epoch ?? 0,
      response.snapshot?.incarnation ?? this.#binding.identity.incarnation,
      request.readId,
    ]);
    this.#recoveryNeeded = candidate.history?.invalidatedAtGeneration !== undefined;
    this.#seedAuthorityInvalid = false;
    this.#seedSnapshot = undefined;
    const rows = this.#rowsFromRead(response, candidate);
    const coverageRows = this.#rowsFromWindow(response);
    const positions = coverageRows.map((row) => row.position);
    const orderedPositions = [...positions].sort(comparePosition);
    this.#coverageStart = orderedPositions[0] ?? null;
    this.#coverageEnd = orderedPositions.at(-1) ?? null;
    this.#representedSnapshot = response.snapshot?.length ?? 0;
    const metadata: TranscriptSessionMetadata = {
      historyKey: this.#binding.historyKey,
      requestedRefs: [{ clientId: this.#binding.identity.clientId, ref: this.#binding.ref }],
      resolvedIdentity: this.#binding.identity,
      savedAt: this.#now(),
      accessedAt: this.#now(),
      formatVersion: 3,
    };
    this.#publish();
    for (const row of rows) this.#pending.set(row.transcriptKey, row);
    this.#queueWrite({
      historyKey: this.#binding.historyKey,
      clearEpoch: this.#binding.clearEpoch,
      historyGeneration: this.#binding.historyGeneration,
      items: rows,
      turns: this.#readTurns(response, candidate).map((turn) => ({ ...turn, items: [] })),
      coverage: {
        kind: "read",
        readId: request.readId,
        start: orderedPositions[0] ?? null,
        end: orderedPositions.at(-1) ?? null,
        representedSnapshot: response.snapshot?.length ?? 0,
        serverCursor: response.olderCursor,
        boundary: request.boundary,
      },
      sessionMetadata: metadata,
    });
    return true;
  }

  /** Accept a notification only when the captured authority still matches this repository. */
  accept(notification: AnyNotification, context: { lifetime: number }): boolean {
    if (!this.#acceptsLifetime(context.lifetime)) return false;
    const params = notification.params as { ref?: unknown; threadId?: unknown; item?: unknown };
    if (params.ref !== this.#binding.ref || params.threadId !== this.#binding.threadId) return false;
    if (!this.#model) return false;
    const previousModel = this.#model;
    const now = this.#now();
    const next =
      notification.method === "overlay/end"
        ? applyOverlayEndForTranscriptHandoff(this.#model, notification, now)
        : applyNotification(this.#model, notification, now);
    if (next === this.#model) {
      if (notification.method !== "overlay/end") return false;
      this.#admitHandoffRound(next, (notification.params as { roundId?: string }).roundId ?? "");
      this.#recoveryNeeded = true;
      this.#publish();
      return true;
    }
    this.#model = next;
    this.#boundary++;
    if (notification.method === "overlay/end") {
      this.#admitHandoffRound(next, (notification.params as { roundId?: string }).roundId ?? "");
    }
    const authorityBlocked = next.history?.invalidatedAtGeneration !== undefined || next.history?.failed !== undefined;
    if (authorityBlocked) this.#advanceSourceRevision();
    const rows = authorityBlocked ? [] : this.#notificationRows(notification);
    const history =
      notification.method === "history/updated" ? (notification.params as { snapshot: { length: number } }) : undefined;
    const turns = authorityBlocked ? [] : this.#notificationTurns(notification);
    const hasRecordedInput = rows.length > 0 || turns.length > 0;
    const coverage =
      history && hasRecordedInput ? this.#contiguousSuffix(rows, turns, history.snapshot.length) : undefined;
    if (next.history?.invalidatedAtGeneration !== undefined || next.history?.failed !== undefined) {
      this.#recoveryNeeded = true;
    }
    const historyNotApplied =
      notification.method === "history/updated" &&
      (authorityBlocked || next.history?.turns === previousModel.history?.turns);
    if (historyNotApplied) {
      this.#publish();
      this.#reclaimBackgroundModel();
      return false;
    }
    if (history && hasRecordedInput && !coverage) this.#recoveryNeeded = true;
    if (notification.method === "overlay/end") this.#recoveryNeeded = true;
    this.#publish();
    if (hasRecordedInput) {
      for (const row of rows) this.#pending.set(row.transcriptKey, row);
      this.#queueWrite({
        historyKey: this.#binding.historyKey,
        clearEpoch: this.#binding.clearEpoch,
        historyGeneration: this.#binding.historyGeneration,
        items: rows,
        turns,
        ...(coverage ? { coverage } : {}),
      });
      if (coverage) {
        this.#coverageBoundary = coverage.boundary;
        this.#coverageEnd = coverage.end;
        this.#representedSnapshot = coverage.representedSnapshot;
      }
    }
    if (!hasRecordedInput) this.#reclaimBackgroundModel();
    return true;
  }

  /**
   * Accept a complete older page below the current window under its captured
   * reader lifetime. The page merges by the shared version rules, publishes to
   * attached readers and queues its recorded rows. `request.requestCursor` is
   * the opaque cursor this page answered; it is kept as the cache page
   * certificate's successor edge and is never decoded. The certificate carries
   * this repository's opaque baseline identity as `authority`.
   */
  acceptPage(
    response: ThreadTurnsListResponse,
    request: {
      lifetime: number;
      readId: string;
      boundary: string;
      authority: string;
      before?: ThreadItemPosition;
      requestCursor?: string;
    },
  ): boolean {
    if (!this.#acceptsLifetime(request.lifetime)) return false;
    if (request.authority !== this.#baselineIdentity) return false;
    if (response.snapshot && response.snapshot.incarnation !== this.#binding.identity.incarnation) return false;
    const current = this.#model;
    if (!current) return false;
    const before = request.before ?? this.#oldestPosition(current);
    const candidate = mergeOlderItemPage(current, response);
    // A discarded page leaves the model in place.
    if (candidate === current) return false;
    // An invalidating/deferring page establishes no certified returned range,
    // so it may not carry coverage. But the shared reducer's invalidation is a
    // source-authority transition that belongs to this acceptance owner: adopt
    // it here rather than discarding it. Otherwise the repository would keep a
    // stale valid model and unchanged source revision while only the store's
    // fallback invalidates, and a later repository publication could push the
    // old valid authority back over that store-only invalidation.
    if (
      candidate.history?.invalidatedAtGeneration !== undefined ||
      (candidate.history?.deferredPages?.length ?? 0) > (current.history?.deferredPages?.length ?? 0)
    ) {
      this.#model = candidate;
      this.#boundary++;
      if (candidate.history?.invalidatedAtGeneration !== undefined) {
        this.#advanceSourceRevision();
        this.#recoveryNeeded = true;
      }
      this.#publish();
      return false;
    }
    this.#model = candidate;
    this.#boundary++;
    const rows = this.#pageItemRows(response, candidate);
    const turns = this.#pageTurnRows(response, candidate);
    for (const row of rows) this.#pending.set(row.transcriptKey, row);
    const positions = rows.map((row) => row.position).sort(comparePosition);
    if (candidate !== current) this.#publish();
    this.#queueWrite({
      historyKey: this.#binding.historyKey,
      clearEpoch: this.#binding.clearEpoch,
      historyGeneration: this.#binding.historyGeneration,
      items: rows,
      turns,
      coverage: {
        kind: "page",
        readId: request.readId,
        before: before ?? null,
        start: positions[0] ?? null,
        end: positions.at(-1) ?? null,
        representedSnapshot: response.snapshot?.length ?? candidate.history?.length ?? 0,
        ...(response.nextCursor === undefined ? {} : { serverCursor: response.nextCursor }),
        ...(request.requestCursor === undefined ? {} : { requestCursor: request.requestCursor }),
        authority: this.#baselineIdentity,
        boundary: request.boundary,
      },
    });
    return true;
  }

  /**
   * Serve an older page from the certified local cache where this baseline's
   * coverage permits. `reconcile` means the cache holds a connected chain for
   * this baseline whose frontier is unverified (ambiguous), so the caller must
   * run the bounded authoritative reconciliation, never an ordinary cursor
   * request. `server` means the baseline simply has no local certificate below
   * the current oldest row: the ordinary server continuation is correct.
   * `exhausted` means the certified chain ended with no older rows. A lookup
   * that crosses a baseline change or loses its lifetime publishes nothing.
   * `superseded` means the caller's `beforePublish` guard rejected the page
   * after the lookup: nothing was merged or published.
   * `onPublished`, when given, runs synchronously in the same step as the
   * guarded publication (after `#publish`, before this method resolves), so a
   * caller's generation accounting and store handoff cannot be split from the
   * guard by the promise continuation.
   */
  async loadOlderPage(
    limit: number,
    lifetime: number,
    beforePublish?: () => boolean,
    onPublished?: () => void,
  ): Promise<
    | { kind: "local"; model: ThreadModel }
    | { kind: "exhausted"; model: ThreadModel }
    | { kind: "server" }
    | { kind: "reconcile" }
    | { kind: "superseded" }
  > {
    if (!this.#acceptsLifetime(lifetime)) return { kind: "server" };
    const current = this.#model;
    if (!current) return { kind: "server" };
    const before = this.#oldestPosition(current);
    if (!before) return { kind: "server" };
    const revisionAtStart = this.#sourceRevision;
    const window = await this.#cache.getOlderWindow(this.#binding.historyKey, before, limit, this.#baselineIdentity);
    if (!this.#acceptsLifetime(lifetime)) return { kind: "server" };
    // An authoritative reconciliation during the lookup retires this window's
    // authority; never publish it into the new baseline.
    if (this.#sourceRevision !== revisionAtStart) return { kind: "reconcile" };
    // Only a connected-but-unverified chain is genuine uncertainty. Empty
    // coverage below the oldest row is an ordinary network continuation.
    if (window.stale) return window.coverage.length > 0 ? { kind: "reconcile" } : { kind: "server" };
    if (window.rows.length === 0) {
      // Certified local coverage below the oldest row ended. Only a frontier
      // with no server cursor is real history end; a cursor means more history
      // remains on the network.
      if (window.complete && window.serverCursor === undefined) {
        return { kind: "exhausted", model: { ...(this.#model ?? current), olderCursor: undefined } };
      }
      return { kind: "server" };
    }
    const turnIds = this.#turnIDsForRows(window.rows, []);
    const turnMetadata = await this.#readTurnMetadata(turnIds);
    if (!this.#acceptsLifetime(lifetime)) return { kind: "server" };
    if (this.#sourceRevision !== revisionAtStart) return { kind: "reconcile" };
    // Merge into the newest model, not the one captured before the awaits, so a
    // recorded update delivered mid-lookup is never overwritten.
    const newest = this.#model ?? current;
    const restored = this.#restoreWindow(newest, window.rows, turnMetadata, () => this.#acceptsLifetime(lifetime));
    if (!restored) return { kind: "server" };
    const model: ThreadModel = window.complete ? { ...restored, olderCursor: window.serverCursor } : restored;
    // Evaluate the caller's ownership guard immediately before publication,
    // with no await between: the attached-reader subscription publishes this
    // model synchronously, so a guard that ran only after this method returned
    // would see the frontier cursor it just changed and reject its own page.
    if (beforePublish && !beforePublish()) return { kind: "superseded" };
    this.#model = model;
    this.#publish();
    onPublished?.();
    return { kind: "local", model };
  }

  attachReader(): TranscriptReader {
    let active = true;
    this.#readerCount++;
    const lifetime = this.#binding.lifetime;
    const captureOpeningState = (): ReaderOpeningState => ({
      model: this.#model,
      pendingRows: [...this.#pending.values()],
      cacheRows: [],
      boundary: this.#boundary,
      committedBoundary: this.#committedCoverageBoundary,
      acceptedCoverageBoundary: this.#coverageBoundary,
      readId: this.#committedCoverageReadId,
      continuityId: this.#committedCoverageContinuityId,
      committedSnapshot: this.#committedCoverageSnapshot,
      sourceRevision: this.#sourceRevision,
      hadPendingWrites: this.#writes.size > 0,
      authorityValid: !this.#recoveryNeeded && !this.#seedAuthorityInvalid,
    });
    let openingState: ReaderOpeningState | undefined = captureOpeningState();
    let disposedOpeningState: ReaderOpeningState | undefined;
    const releaseOpeningState = (state: ReaderOpeningState, preserveSnapshot = false) => {
      state.model = undefined;
      state.pendingRows.length = 0;
      state.cacheRows.length = 0;
      if (openingState === state) openingState = undefined;
      if (preserveSnapshot) disposedOpeningState = state;
      else if (disposedOpeningState === state) disposedOpeningState = undefined;
    };
    let latest = this.#model;
    const unlisten = this.subscribe(() => {
      latest = this.#model;
    });
    return {
      getSnapshot: () => latest,
      getRetentionSnapshotForTesting: () => {
        const cut = openingState ?? disposedOpeningState;
        return {
          openingModelRetained: cut?.model !== undefined,
          pendingPayloadCount: cut?.pendingRows.length ?? 0,
          cachedPayloadCount: cut?.cacheRows.length ?? 0,
        };
      },
      openLocal: async (limit) => {
        if (!active || !this.#acceptsLifetime(lifetime)) return { status: "recovery-needed" };
        let cut = openingState;
        if (!cut) {
          cut = captureOpeningState();
          openingState = cut;
        }
        try {
          const openingCutBoundary = cut.boundary;
          const coverageBoundary = cut.committedBoundary;
          const acceptedBoundaryAtOpen = cut.acceptedCoverageBoundary;
          const sourceRevisionAtOpen = cut.sourceRevision;
          // Suffix certificates are mutable per readId. Resolve the latest real
          // certificate and prove that it covers this reader's fixed cut.
          let window = await this.#cache.getLatestWindow(this.#binding.historyKey, limit, coverageBoundary);
          const authorityCurrent = () => sourceRevisionAtOpen === this.#sourceRevision;
          if (!active || !this.#acceptsLifetime(lifetime) || !authorityCurrent()) {
            return { model: latest ?? this.#model, status: "recovery-needed", boundary: this.#boundary };
          }
          if (!window.complete && coverageBoundary !== undefined) {
            window = { ...window, rows: [] };
            window = await this.#cache.getLatestWindow(this.#binding.historyKey, limit);
          }
          if (!active || !this.#acceptsLifetime(lifetime) || !authorityCurrent()) {
            return { model: latest ?? this.#model, status: "recovery-needed", boundary: this.#boundary };
          }
          cut.cacheRows = [...window.rows];
          window = { ...window, rows: [] };
          const turnIds = this.#turnIDsForRows(cut.cacheRows, cut.pendingRows);
          const turnMetadata = await this.#readTurnMetadata(turnIds);
          if (!active || !this.#acceptsLifetime(lifetime) || !authorityCurrent()) {
            return { model: latest ?? this.#model, status: "recovery-needed", boundary: this.#boundary };
          }
          const current = cut.model ?? this.#model;
          if (!current) return { status: window.complete ? "stale" : "recovery-needed" };
          const rowsByKey = new Map<string, TranscriptItemRow<unknown>>();
          for (const row of [...cut.cacheRows, ...cut.pendingRows]) {
            const held = rowsByKey.get(row.transcriptKey);
            if (!held || held.version <= row.version) rowsByKey.set(row.transcriptKey, row);
          }
          const localRows = [...rowsByKey.values()].sort((left, right) =>
            comparePosition(left.position, right.position),
          );
          const boundedRows = limit === 0 ? [] : localRows.slice(-limit);
          const restored = this.#restoreWindow(
            current,
            boundedRows,
            turnMetadata,
            () => active && this.#acceptsLifetime(lifetime) && authorityCurrent(),
          );
          if (!restored || !active || !this.#acceptsLifetime(lifetime) || !authorityCurrent()) {
            return { model: latest ?? this.#model, status: "recovery-needed", boundary: this.#boundary };
          }
          // A racing accepted model wins by item version; the stale query only fills absent slots.
          latest =
            this.#boundary > openingCutBoundary ? this.#mergeReaderModels(latest ?? current, restored) : restored;
          this.#model = latest;
          this.#publish();
          const certifiedSnapshot = window.coverage.at(-1)?.representedSnapshot;
          const seedCovered =
            this.#seedSnapshot === undefined ||
            (certifiedSnapshot !== undefined && certifiedSnapshot >= this.#seedSnapshot);
          const pendingCutCovered =
            !cut.hadPendingWrites ||
            (acceptedBoundaryAtOpen !== undefined && acceptedBoundaryAtOpen !== coverageBoundary);
          const status =
            window.complete &&
            !window.stale &&
            ((cut.readId === undefined && this.#seedSnapshot !== undefined) ||
              this.#coversOpeningCut(window.coverage, cut.readId, cut.continuityId, cut.committedSnapshot)) &&
            seedCovered &&
            pendingCutCovered &&
            cut.authorityValid &&
            !this.#cacheContinuityLost &&
            !this.#recoveryNeeded
              ? "current"
              : "recovery-needed";
          const oldestReturned = boundedRows[0]?.position;
          const certifiedStart = window.coverage[0]?.start;
          const localBefore =
            oldestReturned && certifiedStart && comparePosition(certifiedStart, oldestReturned) < 0
              ? oldestReturned
              : undefined;
          return {
            model: latest ?? current,
            status,
            boundary: this.#boundary,
            ...(localBefore ? { before: localBefore } : {}),
            olderCursor: window.serverCursor ?? current.olderCursor,
          };
        } catch {
          return { model: latest, status: "recovery-needed", boundary: this.#boundary };
        } finally {
          releaseOpeningState(cut);
        }
      },
      dispose: () => {
        if (!active) return;
        active = false;
        this.#readerCount--;
        unlisten();
        latest = undefined;
        if (openingState) releaseOpeningState(openingState, true);
        this.#reclaimBackgroundModel();
      },
    };
  }

  /**
   * Await issued writes. A failed write resolves its own promise, so an armed
   * or queued retry is recovery work that is still outstanding: a truthful
   * settlement must not report success while it remains. It does not wait for
   * a future timer (which demand can keep rearming), so it throws when recovery
   * is outstanding rather than hanging or silently succeeding.
   */
  async settled(): Promise<void> {
    // Accepted background work drains immediately once the batch ahead of it
    // settles, so await that continuation instead of reporting a batch that is
    // queued only because this repository is still running its predecessor. A
    // paced retry is not driven here: a callback that has not fired is genuinely
    // outstanding, not a hang.
    for (;;) {
      await Promise.all([...this.#writes]);
      if (this.#disposed || this.#invalidated) break;
      if (this.#writes.size > 0) continue;
      if (this.#backgroundAccepts.length === 0) break;
      if (this.#retryQueue.length > 0) break;
      const before = this.#writes.size;
      this.#drainBackground();
      if (this.#writes.size === before) break;
    }
    if (
      !this.#disposed &&
      (this.#retryQueue.length > 0 || this.#backgroundAccepts.length > 0 || this.#retryScheduled || this.#retryInFlight)
    ) {
      throw new Error("transcript recovery is outstanding");
    }
    // Recovery work that was cancelled or never admitted is an explicit
    // disposition, not silent success.
    if (this.#recoveryAbandoned?.kind === "cancelled") throw new Error("transcript recovery was cancelled");
    if (this.#recoveryAbandoned?.kind === "failed") throw new Error("transcript recovery failed");
  }

  invalidate(): void {
    this.#invalidated = true;
    this.#pending.clear();
    this.#cancelRetry();
    this.#abandonBackgroundAccepts("cancelled");
    // The invalidated repository keeps its model, and the model's display turns
    // still carry the ended handoff payload, so the charge (and the overlay
    // registration) stays with it until disposal. Releasing here would leave a
    // retained body with no charge or inventory entry.
  }

  dispose(): void {
    if (this.#disposed) return;
    this.#disposed = true;
    this.#unregisterPreferred?.();
    this.#unregisterPreferred = undefined;
    this.#cancelRetry();
    this.#abandonBackgroundAccepts("cancelled");
    this.#releaseHandoffOverlay();
    this.#listeners.clear();
    this.#pending.clear();
    this.#model = undefined;
  }

  #acceptsLifetime(lifetime: number): boolean {
    return !this.#disposed && !this.#invalidated && lifetime === this.#binding.lifetime;
  }

  #mintBaseline(): string {
    return JSON.stringify([
      this.#binding.sessionNonce ?? PAGE_LIFETIME_NONCE,
      this.#binding.lifetime,
      this.#sourceRevision,
    ]);
  }

  #advanceSourceRevision(): void {
    this.#sourceRevision++;
    this.#baselineIdentity = this.#mintBaseline();
  }

  #matchesRead(response: ThreadReadResponse): boolean {
    return (
      response.thread.id === this.#binding.threadId &&
      response.thread.sessionId === this.#binding.identity.sessionId &&
      response.thread.evener.ref === this.#binding.ref &&
      (!response.snapshot || response.snapshot.incarnation === this.#binding.identity.incarnation)
    );
  }

  #rowsFromRead(response: ThreadReadResponse, model: ThreadModel): TranscriptItemRow<CachedSessionItem>[] {
    const recordedItems = model.history?.turns.flatMap((turn) => turn.items) ?? [];
    return this.#readItems(response).flatMap((wire) => {
      const key = wire.transcriptKey ?? wire.id;
      const item = recordedItems.find((candidate) => (candidate.transcriptKey ?? candidate.id) === key);
      return item
        ? [this.#row(item)].filter((row): row is TranscriptItemRow<CachedSessionItem> => row !== undefined)
        : [];
    });
  }

  #rowsFromWindow(response: ThreadReadResponse): TranscriptItemRow<CachedSessionItem>[] {
    const recordedItems = this.#model?.history?.turns.flatMap((turn) => turn.items) ?? [];
    const items = (response.thread.turns ?? []).flatMap((turn) =>
      (turn.items ?? []).map((item) => ({ ...item, turnId: item.turnId ?? turn.id })),
    );
    return items.flatMap((wire) => {
      const key = wire.transcriptKey ?? wire.id;
      const item = recordedItems.find((candidate) => (candidate.transcriptKey ?? candidate.id) === key);
      return item
        ? [this.#row(item)].filter((row): row is TranscriptItemRow<CachedSessionItem> => row !== undefined)
        : [];
    });
  }

  #readItems(response: ThreadReadResponse): ThreadItem[] {
    return [
      ...(response.thread.turns ?? []).flatMap((turn) =>
        (turn.items ?? []).map((item) => ({ ...item, turnId: item.turnId ?? turn.id })),
      ),
      ...(response.changes?.items ?? []),
      ...(response.changes?.turns ?? []).flatMap((turn) =>
        (turn.items ?? []).map((item) => ({ ...item, turnId: item.turnId ?? turn.id })),
      ),
    ];
  }

  #pageItemRows(response: ThreadTurnsListResponse, model: ThreadModel): TranscriptItemRow<CachedSessionItem>[] {
    const recordedItems = model.history?.turns.flatMap((turn) => turn.items) ?? [];
    const inputs = (response.data ?? []).flatMap((turn) =>
      (turn.items ?? []).map((item) => ({ ...item, turnId: item.turnId ?? turn.id })),
    );
    return inputs.flatMap((wire) => {
      const key = wire.transcriptKey ?? wire.id;
      const item = recordedItems.find((candidate) => (candidate.transcriptKey ?? candidate.id) === key);
      const row = item ? this.#row(item) : undefined;
      return row ? [row] : [];
    });
  }

  #pageTurnRows(response: ThreadTurnsListResponse, model: ThreadModel): TurnModel[] {
    const ids = new Set([
      ...(response.data ?? []).map((turn) => turn.id),
      ...(response.data ?? []).flatMap((turn) => (turn.items ?? []).map((item) => item.turnId ?? turn.id)),
    ]);
    return model.turns.filter((turn) => ids.has(turn.id)).map((turn) => ({ ...turn, items: [] }));
  }

  #oldestPosition(model: ThreadModel): ThreadItemPosition | undefined {
    let oldest: ThreadItemPosition | undefined;
    for (const turn of model.history?.turns ?? []) {
      for (const item of turn.items) {
        if (!item.position) continue;
        if (!oldest || comparePosition(item.position, oldest) < 0) oldest = item.position;
      }
    }
    return oldest;
  }

  #readTurns(response: ThreadReadResponse, model: ThreadModel): TurnModel[] {
    const ids = new Set([
      ...(response.thread.turns ?? []).map((turn) => turn.id),
      ...(response.changes?.turns ?? []).map((turn) => turn.id),
      ...this.#readItems(response)
        .map((item) => item.turnId)
        .filter((id): id is string => id !== undefined),
    ]);
    return model.turns.filter((turn) => ids.has(turn.id));
  }

  #notificationRows(notification: AnyNotification): TranscriptItemRow<CachedSessionItem>[] {
    if (notification.method !== "history/updated") return [];
    const params = notification.params as { turns?: Array<{ id: string; items?: ThreadItem[] }>; items?: ThreadItem[] };
    const inputs = [
      ...(params.items ?? []),
      ...(params.turns ?? []).flatMap((turn) =>
        (turn.items ?? []).map((item) => ({ ...item, turnId: item.turnId ?? turn.id })),
      ),
    ];
    const byKey = new Set<string>();
    const rows: TranscriptItemRow<CachedSessionItem>[] = [];
    for (const input of inputs) {
      // Flat history records require their own turnId; only nested items inherit it above.
      if (!input.turnId) continue;
      const key = input.transcriptKey ?? input.id;
      if (byKey.has(key)) continue;
      byKey.add(key);
      const item = this.#model?.history?.turns
        .flatMap((turn) => turn.items)
        .find((candidate) => (candidate.transcriptKey ?? candidate.id) === key);
      const row = item && this.#row(item);
      if (row) rows.push(row);
    }
    return rows;
  }

  #notificationTurns(notification: AnyNotification): TurnModel[] {
    if (notification.method !== "history/updated") return [];
    const params = notification.params as { turns?: Array<{ id: string; items?: ThreadItem[] }>; items?: ThreadItem[] };
    const ids = new Set([
      ...(params.turns ?? []).map((turn) => turn.id),
      ...(params.turns ?? []).flatMap((turn) => (turn.items ?? []).map((item) => item.turnId ?? turn.id)),
      ...(params.items ?? []).map((item) => item.turnId).filter((id): id is string => id !== undefined),
    ]);
    return (this.#model?.turns ?? []).filter((turn) => ids.has(turn.id)).map((turn) => ({ ...turn, items: [] }));
  }

  #contiguousSuffix(
    rows: readonly TranscriptItemRow<CachedSessionItem>[],
    turns: readonly TurnModel[],
    representedSnapshot: number,
  ): import("./sessionEntryCache").EntryBatch["coverage"] {
    if (
      this.#recoveryNeeded ||
      !this.#coverageReadId ||
      !this.#coverageBoundary ||
      !this.#continuityId ||
      representedSnapshot < this.#representedSnapshot ||
      (rows.length === 0 && turns.length === 0)
    ) {
      return undefined;
    }
    // The producer's ChangedSince(previous byte boundary) is the continuity
    // proof. Item positions select the covered window; their numeric gaps do
    // not establish or disprove whether the input stream skipped records.
    const positions = rows.map((row) => row.position).sort(comparePosition);
    const previousEnd = this.#coverageEnd;
    const lastPosition = positions.at(-1);
    const end =
      previousEnd && (!lastPosition || comparePosition(lastPosition, previousEnd) <= 0) ? previousEnd : lastPosition;
    return {
      kind: "suffix",
      readId: this.#coverageReadId,
      continuityId: this.#continuityId,
      previousBoundary: this.#coverageBoundary,
      boundary: `${this.#coverageBoundary}:suffix:${this.#boundary}`,
      start: this.#coverageStart,
      end: end ?? null,
      representedSnapshot,
    };
  }

  #row(item: ItemModel): TranscriptItemRow<CachedSessionItem> | undefined {
    if (!item.transcriptKey || !item.position || item.version === undefined) return undefined;
    return {
      historyKey: this.#binding.historyKey,
      transcriptKey: item.transcriptKey,
      position: item.position,
      version: item.version,
      item: encodeTranscriptItem(item),
    };
  }

  #turnIDsForRows(
    cacheRows: readonly TranscriptItemRow<unknown>[],
    pendingRows: readonly TranscriptItemRow<CachedSessionItem>[],
  ): string[] {
    const turnIds = new Set<string>();
    const addRows = (rows: readonly TranscriptItemRow<unknown>[]) => {
      for (const row of rows) {
        const turnId = (row.item as { turnId?: unknown }).turnId;
        if (typeof turnId === "string") turnIds.add(turnId);
      }
    };
    addRows(cacheRows);
    addRows(pendingRows);
    return [...turnIds];
  }

  async #readTurnMetadata(
    turnIds: readonly string[],
  ): Promise<Map<string, Awaited<ReturnType<SessionEntryCache["getTurn"]>>>> {
    const rows = await Promise.all(
      turnIds.map(async (turnId) => [turnId, await this.#cache.getTurn(this.#binding.historyKey, turnId)] as const),
    );
    return new Map(rows);
  }

  #restoreWindow(
    seed: ThreadModel,
    rows: readonly TranscriptItemRow<unknown>[],
    turnMetadata: ReadonlyMap<string, Awaited<ReturnType<SessionEntryCache["getTurn"]>>>,
    isCurrent: () => boolean,
  ): ThreadModel | undefined {
    const byTurn = new Map<string, ItemModel[]>();
    for (const row of rows) {
      const item = decodeTranscriptItem(row.item as CachedSessionItem);
      if (!item?.turnId) continue;
      const items = byTurn.get(item.turnId) ?? [];
      const held = items.find((candidate) => candidate.transcriptKey === item.transcriptKey);
      const merged = mergeTranscriptItemVersioned(held, held?.version, item, row.version);
      if (!held) items.push(merged);
      byTurn.set(item.turnId, items);
    }
    const turns = new Map(seed.turns.map((turn) => [turn.id, turn]));
    for (const [turnId, items] of byTurn) {
      if (!isCurrent()) return undefined;
      const cached = turnMetadata.get(turnId);
      const held = turns.get(turnId);
      if (!cached && !held) continue;
      const incoming: TurnModel = cached
        ? { id: turnId, ...cached.metadata, items: [] }
        : { ...(held as TurnModel), items: [] };
      let mergedTurn = mergeTranscriptTurnVersioned(held, incoming);
      const mergedItems = new Map(mergedTurn.items.map((item) => [item.transcriptKey ?? item.id, item]));
      for (const item of items) {
        const key = item.transcriptKey ?? item.id;
        const existing = mergedItems.get(key);
        const compact = seed.history?.turns
          .flatMap((turn) => turn.items)
          .find((candidate) => (candidate.transcriptKey ?? candidate.id) === key);
        const mergeBase = compact?.version === existing?.version ? compact : existing;
        mergedItems.set(key, mergeTranscriptItemVersioned(mergeBase, mergeBase?.version, item, item.version ?? 0));
      }
      mergedTurn = {
        ...mergedTurn,
        items: [...mergedItems.values()].sort((left, right) => comparePositions(left.position, right.position)),
      };
      turns.set(turnId, mergedTurn);
    }
    const restored = { ...seed, turns: [...turns.values()] };
    const loadedTurns = [...byTurn].flatMap(([turnId, items]) => {
      const turn = turns.get(turnId);
      return turn ? [{ ...turn, items }] : [];
    });
    return mergeTranscriptReaderWindow(restored, loadedTurns);
  }

  #mergeReaderModels(held: ThreadModel, incoming: ThreadModel): ThreadModel {
    if (incoming.history) return mergeTranscriptReaderWindow(held, incoming.history.turns);
    const turns = new Map(held.turns.map((turn) => [turn.id, turn]));
    for (const incomingTurn of incoming.turns) {
      const heldTurn = turns.get(incomingTurn.id);
      const mergedTurn = mergeTranscriptTurnVersioned(heldTurn, incomingTurn);
      const items = new Map((heldTurn?.items ?? []).map((item) => [item.transcriptKey ?? item.id, item]));
      for (const item of incomingTurn.items) {
        const key = item.transcriptKey ?? item.id;
        const existing = items.get(key);
        const compact = held.history?.turns
          .flatMap((turn) => turn.items)
          .find((candidate) => (candidate.transcriptKey ?? candidate.id) === key);
        const mergeBase = compact?.version === existing?.version ? compact : existing;
        items.set(key, mergeTranscriptItemVersioned(mergeBase, mergeBase?.version, item, item.version ?? 0));
      }
      turns.set(incomingTurn.id, {
        ...mergedTurn,
        items: [...items.values()].sort((left, right) => comparePositions(left.position, right.position)),
      });
    }
    return { ...held, turns: [...turns.values()] };
  }

  #coversOpeningCut(
    coverage: readonly import("./sessionCacheSchema").TranscriptCoverageRow[],
    readId: string | undefined,
    continuityId: string | undefined,
    representedSnapshot: number | undefined,
  ): boolean {
    if (!readId || representedSnapshot === undefined) return false;
    const baseline = coverage.find((row) => row.kind === "read" && row.readId === readId);
    if (!baseline) return false;
    if (baseline.representedSnapshot >= representedSnapshot) return true;
    if (!continuityId) return false;
    return coverage.some(
      (row) =>
        row.kind === "suffix" &&
        row.readId === readId &&
        row.continuityId === continuityId &&
        row.representedSnapshot >= representedSnapshot,
    );
  }

  /**
   * `retryRevision`, when given, is the source revision the batch was issued
   * under. A retried batch whose revision no longer matches is superseded by a
   * newer boundary, so its completion must not write the old baseline's
   * committed-coverage bookkeeping back over the new baseline's.
   */
  #queueWrite(batch: Parameters<SessionEntryCache["commitBatch"]>[0], retryRevision?: number): Promise<void> {
    const issuedRevision = retryRevision ?? this.#sourceRevision;
    const bodies = retainedBodies(batch);
    const bodyBytes = bodies.reduce((sum, body) => sum + body.bytes, 0);
    const isRetry = retryRevision !== undefined;
    if (isRetry) {
      // The background drain already holds the slot and retained these bodies.
      return this.#issueCommit(batch, bodies, issuedRevision, true, false);
    }
    const oversize = bodyBytes > this.#admission.limit;
    if (oversize) {
      // The sole immediate bounded batch. Only one may be in flight; a competing
      // response is refused for caching rather than starting a second unbounded
      // immediate write.
      if (!this.#admission.retainExempt(bodies, this)) return this.#dropForOverflow(batch);
      return this.#issueCommit(batch, bodies, issuedRevision, false, true);
    }
    if (this.#readerCount > 0) {
      // Foreground demand: the accepted immediate batch.
      if (!this.#admission.retain(bodies, this, "writing")) return this.#dropForOverflow(batch);
      return this.#issueCommit(batch, bodies, issuedRevision, false, false);
    }
    // Background accepted work waits for the single shared background slot.
    if (!this.#admission.retain(bodies, this, "background")) return this.#dropForOverflow(batch);
    this.#backgroundAccepts.push({ batch, revision: issuedRevision, bodies });
    this.#drainBackground();
    return Promise.resolve();
  }

  /**
   * Issue one database batch and settle its accounting at the real terminal
   * outcome. The bodies stay charged (and inventoried as in-flight) until then,
   * even if the repository is invalidated or disposed meanwhile.
   */
  #issueCommit(
    batch: Parameters<SessionEntryCache["commitBatch"]>[0],
    bodies: EntryRetainedBody[],
    issuedRevision: number,
    isRetry: boolean,
    isExempt: boolean,
  ): Promise<void> {
    this.#inFlight.set(batch, bodies);
    const write = this.#cache.commitBatch(batch).then((outcome) => {
      const superseded = isRetry && issuedRevision !== this.#sourceRevision;
      if (outcome.outcome === "committed") {
        this.#retryAttempt = 0;
        // Restoration is conditioned on the outcome's coverage, not the offered
        // input: only an `extended` certificate actually repairs the chain (an
        // `unchanged` batch adds none; `not-extended` means the row did not
        // survive policy or a suffix could not extend, and the cache refuses to
        // certify it). A superseded retry likewise contributes nothing.
        if (!superseded && batch.coverage && outcome.coverage === "extended") {
          this.#cacheContinuityLost = false;
          // Clearing is a claim that this commit's coverage carries the
          // abandoned work forward, not merely that the repository is live. A
          // dead repository takes no further work, so its own late completion
          // can never repair a different cancelled batch. Live, the commit must
          // cover the abandoned extent; a predecessor finishing ahead of an
          // abandoned suffix, or a different window at the same snapshot,
          // leaves the disposition for settled() to report. Continuity is a
          // local durability fact about this commit's chain and is always
          // restored.
          const abandoned = this.#recoveryAbandoned;
          if (
            !this.#invalidated &&
            !this.#disposed &&
            abandoned !== undefined &&
            commitCarriesCoverage(batch.coverage, abandoned.reach)
          ) {
            this.#recoveryAbandoned = undefined;
            this.#reconciledReach = undefined;
          }
        }
        if (!superseded && batch.coverage && outcome.coverage !== "not-extended") {
          this.#committedCoverageBoundary = batch.coverage.boundary;
          this.#committedCoverageSnapshot = batch.coverage.representedSnapshot;
          this.#committedCoverageReadId = batch.coverage.readId;
          this.#committedCoverageContinuityId =
            batch.coverage.kind === "suffix" ? batch.coverage.continuityId : undefined;
        }
        this.#admission.release(bodies, this);
        for (const row of batch.items) {
          if (this.#pending.get(row.transcriptKey) === row) this.#pending.delete(row.transcriptKey);
        }
      } else {
        this.#cacheContinuityLost = true;
        for (const row of batch.items) {
          if (this.#pending.get(row.transcriptKey) === row) this.#pending.delete(row.transcriptKey);
        }
        // A failed transaction is transient until the storage proves otherwise.
        // Retry it on bounded backoff while a reader still holds demand, so a
        // momentary abort does not strand the route in recovery forever. A
        // refusal (`clear-epoch`, `history-fence`, `capacity`) is not transient:
        // retrying it would be the repair loop the policy forbids. A superseded
        // retry is dropped rather than re-armed. An oversize response is never
        // staged: it would breach the aggregate it was exempted from.
        // A retried batch that fails again keeps its place at the front so a
        // dependent suffix never overtakes its predecessor; a first failure
        // queues behind the failures already waiting.
        if (outcome.outcome === "failed" && !superseded && !isExempt) {
          this.#enqueueRetry(batch, issuedRevision, isRetry, bodies);
        } else {
          this.#admission.release(bodies, this);
        }
        if (isExempt) this.#recordAbandonment("failed", [batch.coverage]);
      }
      this.#inFlight.delete(batch);
      if (isExempt) this.#admission.releaseExempt(this);
      this.#scheduleRetry();
    });
    this.#writes.add(write);
    void write.finally(() => {
      this.#writes.delete(write);
      this.#reclaimBackgroundModel();
    });
    return write;
  }

  /**
   * Aggregate overflow: the affected cache work is dropped (its bodies were not
   * retained), durable readiness is honestly broken, one paced reconciliation is
   * requested, and the already-published reader stays usable.
   */
  #dropForOverflow(batch: Parameters<SessionEntryCache["commitBatch"]>[0]): Promise<void> {
    for (const row of batch.items) {
      if (this.#pending.get(row.transcriptKey) === row) this.#pending.delete(row.transcriptKey);
    }
    this.#cacheContinuityLost = true;
    this.#recoveryNeeded = true;
    this.#recordAbandonment("failed", [batch.coverage]);
    this.#scheduleReconcile();
    this.#reclaimBackgroundModel();
    return Promise.resolve();
  }

  // Arm one paced reconciliation after an overflow. It does not arm again for an
  // abandoned reach it already requested (re-feeding the same rejected content),
  // so repeated callback/read feedback is not a repair loop; a wider obligation
  // arms afresh, and changed eligible content is reconsidered through the
  // ordinary accept path.
  #scheduleReconcile(): void {
    if (this.#reconcileArmed || this.#disposed || this.#invalidated) return;
    const reach = this.#recoveryAbandoned?.reach;
    if (reach && this.#reconciledReach && reachWithin(this.#reconciledReach, reach)) return;
    this.#reconcileArmed = true;
    const fire = () => {
      this.#reconcileArmed = false;
      this.#reconcileCancel = undefined;
      this.#reconciledReach = this.#recoveryAbandoned?.reach;
      // A real request: the repository asks its owner for an authoritative read.
      this.#binding.reconcile?.();
      this.#scheduleRetry();
    };
    const scheduler = this.#binding.retryScheduler;
    if (scheduler) {
      this.#reconcileCancel = scheduler(1, fire);
      return;
    }
    const timer = setTimeout(fire, 250);
    this.#reconcileCancel = () => clearTimeout(timer);
  }

  /**
   * Admit the completed ended-round overlay bodies preserved for handoff by
   * `applyOverlayEndForTranscriptHandoff`. They are display-only, so they are not
   * persisted, but they are retained in memory and count against the aggregate
   * until a later history update prunes them.
   */
  #admitHandoffRound(model: ThreadModel, roundId: string): void {
    if (!roundId) return;
    this.#endedRounds.add(roundId);
    const overlay = model.overlay ?? {};
    const candidates: Array<[string, object, number]> = [];
    for (const [key, item] of Object.entries(overlay)) {
      if (item.roundId !== roundId) continue;
      if (this.#handoffOverlay.get(key) === item) continue;
      candidates.push([key, item, encodedPayloadBytes(item)]);
    }
    if (candidates.length === 0) return;
    const bodies = candidates.map(([, item, bytes]) => ({ key: item, bytes }));
    // The model keeps these display-only bodies, so their retention cannot be
    // refused: register and charge them even beyond the aggregate, so the
    // reported bound and the inventory stay honest. An over-limit retention is
    // still a real pressure, so it breaks readiness honestly and asks its owner
    // for one paced reconciliation, exactly as an ordinary overflow does.
    this.#admission.retain(bodies, this, "ended", true);
    for (const [key, item] of candidates) this.#handoffOverlay.set(key, item);
    if (this.#admission.retainedBytes > this.#admission.limit) {
      this.#recoveryNeeded = true;
      this.#scheduleReconcile();
    }
  }

  // Release ended handoff bodies a later model update no longer preserves.
  #reconcileHandoffOverlay(): void {
    if (this.#handoffOverlay.size === 0 && this.#endedRounds.size === 0) return;
    const overlay = this.#model?.overlay ?? {};
    for (const [key, item] of [...this.#handoffOverlay]) {
      if (overlay[key] !== item) {
        this.#handoffOverlay.delete(key);
        this.#admission.releaseKey(item, this);
      }
    }
    for (const roundId of [...this.#endedRounds]) {
      if (!Object.values(overlay).some((item) => item.roundId === roundId)) this.#endedRounds.delete(roundId);
    }
  }

  // Release the ended handoff charge and keep the model consistent with it: the
  // bodies whose charge is freed leave the overlay too, so the model never keeps
  // a body with no charge or inventory entry.
  #releaseHandoffOverlay(): void {
    for (const item of this.#handoffOverlay.values()) this.#admission.releaseKey(item, this);
    if (this.#model?.overlay && this.#handoffOverlay.size > 0) {
      const overlay = { ...this.#model.overlay };
      let dropped = false;
      for (const key of this.#handoffOverlay.keys()) {
        if (key in overlay) {
          delete overlay[key];
          dropped = true;
        }
      }
      if (dropped) this.#model = { ...this.#model, overlay };
    }
    this.#handoffOverlay.clear();
    this.#endedRounds.clear();
  }

  #now(): number {
    return this.#binding.now?.() ?? Date.now();
  }

  // Retain a failed batch in arrival order. A second distinct failure is
  // deferred behind the first; a re-failed retry keeps the front (atFront) so a
  // dependent suffix never overtakes it. Nothing is discarded. Demand-gated:
  // with no reader, or after cancel, the failure is an explicit disposition.
  #enqueueRetry(
    batch: Parameters<SessionEntryCache["commitBatch"]>[0],
    revision: number,
    atFront: boolean,
    bodies: EntryRetainedBody[],
  ): void {
    if (this.#disposed || this.#invalidated) {
      this.#recordAbandonment("cancelled", [batch.coverage]);
      this.#admission.release(bodies, this);
      return;
    }
    if (this.#readerCount <= 0) {
      this.#recordAbandonment("failed", [batch.coverage]);
      this.#admission.release(bodies, this);
      return;
    }
    const entry = { batch, revision, bodies };
    if (atFront) this.#retryQueue.unshift(entry);
    else this.#retryQueue.push(entry);
  }

  // Record an abandonment that drops the queued batches, remembering the
  // coverage identity and extent they would have contributed so a later commit
  // can be checked against it. The record is a hull: it is widened by each
  // abandonment and never lowered, so an earlier larger obligation survives a
  // later smaller one.
  #abandonQueue(kind: "cancelled" | "failed"): void {
    this.#recordAbandonment(
      kind,
      this.#retryQueue.map((entry) => entry.batch.coverage),
    );
    for (const entry of this.#retryQueue) this.#admission.release(entry.bodies, this);
  }

  #recordAbandonment(kind: "cancelled" | "failed", coverages: readonly (EntryCoverageInput | undefined)[]): void {
    let reach = this.#recoveryAbandoned?.reach;
    for (const coverage of coverages) {
      const next = coverageReach(coverage);
      reach = reach ? widerReach(reach, next) : next;
    }
    this.#recoveryAbandoned = {
      kind: this.#recoveryAbandoned?.kind ?? kind,
      reach: reach ?? { start: null, end: null, snapshot: 0 },
    };
  }

  // One paced timer arms the retry queue. The queue keeps its causal order; the
  // shared background slot decides which repository runs next.
  #scheduleRetry(): void {
    if (this.#retryScheduled || this.#retryInFlight || this.#disposed || this.#invalidated) return;
    if (this.#readerCount <= 0) return;
    if (this.#retryQueue.length === 0) return;
    this.#retryScheduled = true;
    this.#retryAttempt += 1;
    const attempt = this.#retryAttempt;
    const fire = () => {
      this.#retryScheduled = false;
      this.#retryCancel = undefined;
      this.#drainBackground();
    };
    const scheduler = this.#binding.retryScheduler;
    if (scheduler) {
      this.#retryCancel = scheduler(attempt, fire);
      return;
    }
    const delay = Math.min(2_000, 250 * 2 ** Math.min(attempt - 1, 3));
    const timer = setTimeout(fire, delay);
    this.#retryCancel = () => clearTimeout(timer);
  }

  /**
   * Run at most one background database batch across every entry repository
   * sharing this admission. A queued retry takes precedence over a queued
   * no-reader accepted write because its causal coverage is required; foreground
   * accepted writes never enter here, so a stalled background batch cannot block
   * them. When the slot is busy this repository waits and is resumed by
   * `releaseBackground`, which prioritises live reader demand.
   */
  #drainBackground(): void {
    if (this.#disposed || this.#invalidated) {
      this.#abandonBackgroundAccepts("cancelled");
      if (this.#retryQueue.length > 0) this.#abandonQueue("cancelled");
      this.#retryQueue.length = 0;
      return;
    }
    // One repository cannot overlap itself: a second drain call while this
    // repository's own batch is in flight must not issue a concurrent second
    // batch (the shared slot admits a re-entrant same-owner acquisition).
    if (this.#backgroundInFlight) return;
    if (this.#readerCount <= 0 && this.#retryQueue.length > 0) {
      // An issued retry needs live reader demand; without it the queued retries
      // are an explicit cancelled disposition.
      this.#abandonQueue("cancelled");
      this.#retryQueue.length = 0;
    }
    if (this.#retryQueue.length === 0 && this.#backgroundAccepts.length === 0) return;
    if (
      !this.#admission.tryAcquireBackground(
        this,
        () => this.#readerCount > 0,
        () => this.#drainBackground(),
      )
    ) {
      return;
    }
    const retry = this.#retryQueue.shift();
    if (retry) {
      // A boundary since this batch was issued retires its persistence claim:
      // re-committing it could only write the old baseline's bookkeeping over
      // the new one. Drop it and continue with the rest of the queue.
      if (retry.revision !== this.#sourceRevision) {
        this.#admission.release(retry.bodies, this);
        this.#admission.releaseBackground(this);
        this.#drainBackground();
        return;
      }
      this.#startBackgroundBatch(retry.batch, retry.bodies, retry.revision, true);
      return;
    }
    const accept = this.#backgroundAccepts.shift();
    if (!accept) {
      this.#admission.releaseBackground(this);
      return;
    }
    this.#startBackgroundBatch(accept.batch, accept.bodies, accept.revision, false);
  }

  // Issue one background batch and, on its real terminal outcome, release the
  // shared slot and keep the fair drain going so queued accepted work without
  // fresh input is never stranded.
  #startBackgroundBatch(
    batch: Parameters<SessionEntryCache["commitBatch"]>[0],
    bodies: EntryRetainedBody[],
    revision: number,
    isRetry: boolean,
  ): void {
    this.#backgroundInFlight = true;
    this.#retryInFlight = true;
    const write = this.#issueCommit(batch, bodies, revision, isRetry, false);
    void write.finally(() => {
      this.#backgroundInFlight = false;
      this.#retryInFlight = false;
      this.#admission.releaseBackground(this);
      this.#continueBackground();
    });
  }

  // After a background batch settles, drain whatever remains. Retries keep their
  // paced backoff while reader demand holds; accepted background work (which
  // needs no reader) continues immediately.
  #continueBackground(): void {
    if (this.#retryQueue.length > 0 && this.#readerCount > 0) {
      this.#scheduleRetry();
      return;
    }
    this.#drainBackground();
  }

  #abandonBackgroundAccepts(kind: "cancelled" | "failed"): void {
    for (const entry of this.#backgroundAccepts) {
      this.#recordAbandonment(kind, [entry.batch.coverage]);
      this.#admission.release(entry.bodies, this);
    }
    this.#backgroundAccepts.length = 0;
  }

  #cancelRetry(): void {
    if (this.#retryQueue.length > 0 || this.#retryScheduled) this.#abandonQueue("cancelled");
    this.#retryQueue.length = 0;
    this.#retryScheduled = false;
    const cancel = this.#retryCancel;
    this.#retryCancel = undefined;
    cancel?.();
    this.#reconcileArmed = false;
    const reconcileCancel = this.#reconcileCancel;
    this.#reconcileCancel = undefined;
    reconcileCancel?.();
    // Only drop the waiter claim. An issued background batch keeps its slot (and
    // its charge) until its real terminal outcome releases it.
    this.#admission.cancelBackground(this);
  }

  #reclaimBackgroundModel(): void {
    if (this.#readerCount > 0 || this.#pending.size > 0 || this.#writes.size > 0 || !this.#model) return;
    this.#model = pruneBackgroundHistoryBodies(this.#model);
  }

  #publish(): void {
    this.#reconcileHandoffOverlay();
    for (const listener of this.#listeners) listener();
  }
}
