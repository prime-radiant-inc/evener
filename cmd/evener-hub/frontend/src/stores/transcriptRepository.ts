import {
  type AnyNotification,
  applyHistoryReadFailure,
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
  itemTextPresence,
  mergeOlderItemPage,
  mergeTranscriptItemVersioned,
  mergeTranscriptReaderWindow,
  mergeTranscriptTurnVersioned,
  pruneBackgroundHistoryBodies,
  readDisposition,
  type SnapshotIdentity,
  type ThreadItem,
  type ThreadItemPosition,
  type ThreadModel,
  type ThreadReadResponse,
  type ThreadTurnsListResponse,
  type TurnModel,
} from "@evener/appwire-client";
import nacl from "tweetnacl";
import { type EntryRetainedBody, type EntryWriteAdmission, entryWriteAdmissionFor } from "./entryWriteAdmission";
import {
  entryHistoryKey,
  SESSION_CACHE_LOOKUP_DEADLINE_MS,
  type TranscriptItemRow,
  type TranscriptResolvedIdentity,
  type TranscriptSessionMetadata,
} from "./sessionCacheSchema";
import { type EntryCoverageInput, mayMatchCapacityIdentity, type SessionEntryCache } from "./sessionEntryCache";
import { encodedPayloadBytes } from "./transcriptPayloadBudget";

// A changed offer is eligible for an actual write, not proof that its merged
// persisted plan differs. Hash synchronously: no lookup may retain these bodies
// outside admission. Keep only the fixed 128-character SHA-512 digest.
function offeredContentFingerprint(batch: Parameters<SessionEntryCache["commitBatch"]>[0]): string {
  const content = JSON.stringify([
    batch.items.map((row) => ({ ...row, item: encodeTranscriptItem(row.item as ItemModel) })),
    (batch.turns ?? []).map(({ items: _items, ...turn }) => turn),
  ]);
  return Array.from(nacl.hash(new TextEncoder().encode(content)), (byte) => byte.toString(16).padStart(2, "0")).join(
    "",
  );
}

// Local bodies can fill omissions at the same version, but cannot overwrite
// explicit values (including empty ones). Keep normal version/identity rules;
// only when the held item wins at equal version, use it as the authoritative
// incoming payload over the cached fallback.
function mergeLocalItem(held: ItemModel | undefined, incoming: ItemModel): ItemModel {
  const merged = mergeTranscriptItemVersioned(held, held?.version, incoming, incoming.version ?? 0);
  if (held && merged === held && held.version === incoming.version) {
    const enriched = mergeTranscriptItemVersioned(incoming, undefined, held, held.version ?? 0);
    // An omission-tolerant merge keeps the source's omission marker even when
    // it carries fallback text. That body is still usable by a local reader.
    return itemTextPresence(held) === "omitted" && held.text === "" && incoming.text !== ""
      ? { ...enriched, text: incoming.text }
      : enriched;
  }
  return merged;
}

function restoreReaderWindow(seed: ThreadModel, turns: TurnModel[]): ThreadModel {
  if (!seed.history) return mergeTranscriptReaderWindow(seed, turns);
  const local = new Map(turns.flatMap((turn) => turn.items).map((item) => [item.transcriptKey ?? item.id, item]));
  const history = {
    ...seed.history,
    turns: seed.history.turns.map((turn) => ({
      ...turn,
      items: turn.items.map((held) => {
        const incoming = local.get(held.transcriptKey ?? held.id);
        return incoming ? mergeLocalItem(held, incoming) : held;
      }),
    })),
  };
  return mergeTranscriptReaderWindow({ ...seed, history }, turns);
}

// Retained history identities prove eviction and local reachability, but are
// not display content. Removing their display-only turns also puts the virtual
// list's near-top origin at the oldest hydrated content. Explicit-empty bodies
// and tools with omitted text but real payloads are not identity-only.
function hydratedDisplay(model: ThreadModel): ThreadModel {
  let changed = false;
  const turns = model.turns.flatMap((turn) => {
    const items = turn.items.filter((item) => !encodeTranscriptItem(item).identityOnly);
    if (items.length === turn.items.length) return [turn];
    changed = true;
    return items.length === 0 ? [] : [{ ...turn, items }];
  });
  return changed ? { ...model, turns } : model;
}

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

let sourceReadSequence = 0;

/** Unique across routes, repository replacements, tabs and browser reloads. */
export function nextTranscriptReadIdentity(): { readId: string; boundary: string } {
  const identity = `${PAGE_LIFETIME_NONCE}:${++sourceReadSequence}`;
  return { readId: `read:${identity}`, boundary: `boundary:${identity}` };
}

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
  /**
   * Whether this route's publication and persistence claims are currently
   * suppressed by an in-flight Clear. A suppressed route accepts nothing (no
   * publish, no write) but is not disposed, so an aborted Clear can re-enable
   * the still-held route without a release/reacquire.
   */
  suppressed?: () => boolean;
  /** Persistence-only lifetime permission. Live input and display stay usable. */
  mayPersist?: () => boolean;
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
  /** The owner's canonical notification fold, before synchronous publication. */
  foldNotification?: (model: ThreadModel, notification: AnyNotification, now: number) => ThreadModel;
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
  /** Read-only, deadline-owned transport ports for end disposition and final retirement. */
  readHistory?: (held?: SnapshotIdentity, signal?: AbortSignal) => Promise<ThreadReadResponse>;
  readHistoryPage?: (request: {
    snapshot: SnapshotIdentity;
    cursor: string;
    before: ThreadItemPosition;
    signal?: AbortSignal;
  }) => Promise<ThreadTurnsListResponse>;
}

export type TranscriptReaderStatus = "current" | "stale" | "recovery-needed";
export type TranscriptRetirementOutcome = { kind: "saved" | "stale" | "cancelled" };

export interface TranscriptReaderResult {
  model?: ThreadModel;
  status: TranscriptReaderStatus;
  /**
   * Undefined: no retained foreground range was checked (including first
   * promotion or a cancelled opening). True: every required keyed body and
   * version was restored. False: usable partial content may be published, but
   * the store must recover rather than accept its latest-window fallback.
   * This is body completeness, not coverage or source authority.
   */
  retainedRangeComplete?: boolean;
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
  latestWindowBoundary?: string;
  acceptedCoverageBoundary?: string;
  readId?: string;
  continuityId?: string;
  committedSnapshot?: number;
  sourceRevision: number;
  foregroundStart?: ThreadItemPosition;
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
  readonly #preflightWrites = new Map<
    Parameters<SessionEntryCache["commitBatch"]>[0],
    { revision: number; write: Promise<void> }
  >();
  readonly #pending = new Map<string, TranscriptItemRow<CachedSessionItem>>();
  #model: ThreadModel | undefined;
  #boundary = 0;
  #sourceRevision = 0;
  // Identity-only range ownership survives foreground reader replacement, not
  // source reconciliation. Background payload reclamation remains unchanged.
  #foregroundStart: ThreadItemPosition | undefined;
  #localOlder: { before: ThreadItemPosition; floor: ThreadItemPosition; sourceRevision: number } | undefined;
  #replacementRevision = 0;
  #uncommittedReplacementFrom: ThreadItemPosition | undefined;
  #baselineIdentity = "";
  #readerCount = 0;
  #coverageBoundary: string | undefined;
  // The boundary this suffix chain descends from (the read certificate's own
  // boundary). Suffix boundary identity is derived from this bounded base plus
  // the monotonic event counter, so it never accumulates the previous suffix
  // identifier. `#coverageBoundary` still tracks the latest boundary for the
  // cache's previousBoundary equality chain.
  #suffixBoundaryBase: string | undefined;
  #coverageReadId: string | undefined;
  #localReadContinuation: string | undefined;
  #continuityId: string | undefined;
  #coverageStart: { entry: number; item: number; sub?: number } | null = null;
  #coverageEnd: { entry: number; item: number; sub?: number } | null = null;
  #representedSnapshot = 0;
  #committedCoverageBoundary: string | undefined;
  #committedCoverageSnapshot: number | undefined;
  #committedCoverageReadId: string | undefined;
  #committedCoverageContinuityId: string | undefined;
  #committedCoverage: { coverage: EntryCoverageInput; revision: number } | undefined;
  #retirementWrite: { boundary: string; persisted: boolean } | undefined;
  #seedSnapshot: number | undefined;
  #seedAuthorityInvalid = false;
  #recoveryNeeded = false;
  #disposed = false;
  #invalidated = false;
  #persistenceCleared = false;
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
  // The last intrinsically oversized recorded window in this repository's
  // cache lifetime. Unrelated writes/evictions cannot make these bytes fit.
  #capacityRefusal: string | undefined;
  #capacityRefusedOffer: string | undefined;
  // Ordinary capacity refusals settle without a failed-retry disposition, but
  // their missing extent still prevents a narrower commit restoring continuity.
  #capacityRefusedReach: AbandonedCoverage | undefined;
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
  // The bodies of commits or memo preflights issued and not yet settled. They stay charged
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
  readonly #endClaims = new Set<string>();
  #endWork: Promise<void> | undefined;
  #endRetryCancel: (() => void) | undefined;
  #endRetryAttempt = 0;
  #lastDisposedRound: string | undefined;
  #sealed = false;
  #retirement: Promise<TranscriptRetirementOutcome> | undefined;
  #cancelRetirement: (() => void) | undefined;

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
      this.#model = hydratedDisplay(this.#seedAuthorityInvalid ? invalidateHistory(seedModel) : seedModel);
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
   * @internal The recovery flags that decide whether a local open may report
   * "current". `openLocal`'s status is `current` only while the opening cut is
   * authoritative, which requires `!recoveryNeeded` (a refused durable write sets
   * it). Read-only; used to measure why an opening declined to serve locally.
   */
  getRecoveryStateForTesting(): {
    recoveryNeeded: boolean;
    seedAuthorityInvalid: boolean;
    cacheContinuityLost: boolean;
    invalidated: boolean;
    disposed: boolean;
  } {
    return {
      recoveryNeeded: this.#recoveryNeeded,
      seedAuthorityInvalid: this.#seedAuthorityInvalid,
      cacheContinuityLost: this.#cacheContinuityLost,
      invalidated: this.#invalidated,
      disposed: this.#disposed,
    };
  }

  /**
   * True while the held view is still the one a completed local open
   * established: no invalidation (recovery need, seed authority, cache
   * continuity, invalidation marker, disposal) has overturned it since. A
   * remount over a still-held view can serve it without a disk
   * recertification, whose lookup-deadline expiry would discard the loaded
   * range and fall back to a source read.
   */
  holdsEstablishedView(): boolean {
    return (
      !this.#recoveryNeeded &&
      !this.#seedAuthorityInvalid &&
      !this.#cacheContinuityLost &&
      !this.#invalidated &&
      !this.#disposed
    );
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

  /** The owner calls this after lifting reversible persistence suppression. */
  resumePersistence(): void {
    this.#continueBackground();
  }

  /** A committed Clear cancels queued persistence, not display or issued writes. */
  cancelPersistenceAfterClear(): void {
    this.#persistenceCleared = true;
    this.#cancelRetry();
    this.#abandonBackgroundAccepts("cancelled");
    this.#pending.clear();
  }

  /** Adopt a source failure under the same captured lifetime as a read. */
  acceptReadFailure(message: string, generation: number, context: { lifetime: number }): void {
    if (!this.#acceptsLifetime(context.lifetime) || !this.#model) return;
    const failed = applyHistoryReadFailure(this.#model, message, generation);
    if (failed === this.#model) return;
    this.#model = failed;
    this.#advanceSourceRevision();
    this.#recoveryNeeded = true;
    this.#publish();
    this.#reclaimBackgroundModel();
  }

  /** Accept a complete wire read under its captured reader lifetime. */
  acceptRead(
    response: ThreadReadResponse,
    request: { lifetime: number; readId: string; boundary: string; preserveEnded?: boolean },
  ): boolean {
    if (this.#sealed || !this.#acceptsLifetime(request.lifetime) || !this.#matchesRead(response)) return false;
    const before = this.#model;
    let candidate = before
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
    if (request.preserveEnded && before?.overlay) {
      for (const item of Object.values(before.overlay)) {
        if (item.kind !== "notice" && (!item.roundId || !this.#endClaims.has(item.roundId))) continue;
        candidate = applyNotification(
          candidate,
          { method: "overlay/upserted", params: { ref: this.#binding.ref, threadId: this.#binding.threadId, item } },
          this.#now(),
        );
      }
      for (const roundId of this.#endClaims) {
        candidate = applyOverlayEndForTranscriptHandoff(
          candidate,
          { method: "overlay/end", params: { ref: this.#binding.ref, threadId: this.#binding.threadId, roundId } },
          this.#now(),
        );
      }
    }
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
    this.#suffixBoundaryBase = request.boundary;
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
    let rows = this.#rowsFromRead(response, candidate);
    let turns = this.#readTurns(response, candidate);
    const coverageRows = this.#rowsFromWindow(response);
    const replacesHistory = before?.history && readDisposition(before.history, response) === "replace";
    const replacement =
      replacesHistory || response.authoritative
        ? {
            from: replacesHistory
              ? { entry: 0, item: 0 }
              : ((response.thread.turns ?? [])
                  .flatMap((turn) => (turn.items ?? []).flatMap((item) => (item.position ? [item.position] : [])))
                  .sort(comparePosition)[0] ?? { entry: 0, item: 0 }),
          }
        : undefined;
    if (replacement) {
      const outstanding = this.#uncommittedReplacementFrom;
      if (outstanding && comparePosition(outstanding, replacement.from) < 0) {
        // Retiring an earlier queued replacement must not lose its deletions or
        // the accepted rows below this new window. Carry that still-uncommitted
        // interval forward from the reducer's current model, not the old batch.
        const inherited =
          candidate.history?.turns.filter((turn) =>
            turn.items.some(
              (item) =>
                item.position &&
                comparePosition(item.position, outstanding) >= 0 &&
                comparePosition(item.position, replacement.from) < 0,
            ),
          ) ?? [];
        const currentKeys = new Set(rows.map((row) => row.transcriptKey));
        rows = [
          ...inherited.flatMap((turn) =>
            turn.items.flatMap((item) => {
              if (
                !item.position ||
                comparePosition(item.position, outstanding) < 0 ||
                comparePosition(item.position, replacement.from) >= 0 ||
                (item.transcriptKey && currentKeys.has(item.transcriptKey))
              )
                return [];
              const row = this.#row(item);
              return row ? [row] : [];
            }),
          ),
          ...rows,
        ];
        turns = [...inherited.filter((turn) => !turns.some((current) => current.id === turn.id)), ...turns];
        replacement.from = outstanding;
      }
      this.#uncommittedReplacementFrom = replacement.from;
      this.#replacementRevision = this.#sourceRevision;
      // Pending opening bodies must obey the same removal as durable rows.
      for (const [key, row] of this.#pending) {
        if (comparePosition(row.position, replacement.from) >= 0) this.#pending.delete(key);
      }
    }
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
      turns: turns.map((turn) => ({ ...turn, items: [] })),
      replacement,
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

  /**
   * Versioned pre-cut input uses the shared recorded merge or queue revision.
   * An unversioned overlay may enrich a slot absent from the adopted read, but
   * cannot replace or append to a response-owned slot. Capture that authority
   * once for the whole cut, so a missing upsert and its deltas stay together.
   * The shared reducer prunes any overlay the versioned recorded history covers.
   */
  acceptBeforeReadCut(notifications: readonly AnyNotification[], context: { lifetime: number }): void {
    const responseOverlayKeys = new Set(Object.keys(this.#model?.overlay ?? {}));
    for (const notification of notifications) {
      if (notification.method === "history/updated") {
        this.accept(notification, context);
      } else if (
        notification.method === "thread/queueChanged" &&
        notification.params.queue.revision > (this.#model?.queue?.revision ?? -1)
      ) {
        this.accept(notification, context);
      } else if (notification.method === "overlay/upserted" && !responseOverlayKeys.has(notification.params.item.key)) {
        this.accept(notification, context);
      } else if (
        notification.method === "overlay/delta" &&
        !responseOverlayKeys.has(notification.params.key) &&
        this.#model?.overlay?.[notification.params.key]
      ) {
        this.accept(notification, context);
      }
    }
  }

  /** Apply response metadata after the caller's generation and intent fences. */
  acceptResponseFields(fields: Partial<Pick<ThreadModel, "goal" | "humanNote">>): void {
    if (this.#disposed || this.#sealed || !this.#model) return;
    this.#model = { ...this.#model, ...fields };
    this.#publish();
  }

  /** Accept a notification only when the captured authority still matches this repository. */
  accept(notification: AnyNotification, context: { lifetime: number }): boolean {
    if (this.#sealed || !this.#acceptsLifetime(context.lifetime)) return false;
    const params = notification.params as { ref?: unknown; threadId?: unknown; item?: unknown };
    if (params.ref !== this.#binding.ref || params.threadId !== this.#binding.threadId) return false;
    if (!this.#model) return false;
    if (notification.method === "overlay/end" && notification.params.roundId === this.#lastDisposedRound) return false;
    if (notification.method === "overlay/end") {
      if (notification.params.roundId !== this.#lastDisposedRound) {
        this.#endClaims.add(notification.params.roundId);
        this.#startEndDisposition();
      }
    }
    const previousModel = this.#model;
    const now = this.#now();
    const next =
      notification.method === "overlay/end"
        ? applyOverlayEndForTranscriptHandoff(this.#model, notification, now)
        : (this.#binding.foldNotification ?? applyNotification)(this.#model, notification, now);
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
    if (this.#sealed || !this.#acceptsLifetime(request.lifetime)) return false;
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
    const revisionAtStart = this.#sourceRevision;
    // Restore omitted bodies only inside the interval the opening already
    // represented. Read/suffix coverage is not page-baseline coverage, so these
    // raw rows merge compatibly without certifying a range or changing cursors.
    const local = this.localOlderPosition() ? this.#localOlder : undefined;
    if (local) {
      // As with certified paging below, only race payload work. The authority
      // guard keeps its own deadline and must still reconcile uncertainty.
      let lookupActive = true;
      const lookup = async () => {
        const stored = await this.#cache.getBefore(this.#binding.historyKey, local.before, limit);
        if (!lookupActive || !this.#acceptsLifetime(lifetime) || this.#sourceRevision !== revisionAtStart) {
          return undefined;
        }
        const rows = stored.filter((row) => comparePosition(row.position, local.floor) >= 0);
        const turns = await this.#readTurnMetadata(this.#turnIDsForRows(rows, []));
        return { rows, turns };
      };
      let timer: ReturnType<typeof setTimeout> | undefined;
      let payload: Awaited<ReturnType<typeof lookup>> | undefined;
      try {
        payload = await Promise.race([
          lookup(),
          new Promise<undefined>((resolve) => {
            timer = setTimeout(() => resolve(undefined), SESSION_CACHE_LOOKUP_DEADLINE_MS);
          }),
        ]);
      } catch {
        // This known local continuation may have no server cursor. Ordinary
        // server paging would then strand its omitted bodies.
        return { kind: "reconcile" };
      } finally {
        lookupActive = false;
        if (timer !== undefined) clearTimeout(timer);
      }
      if (!this.#acceptsLifetime(lifetime)) return { kind: "server" };
      if (this.#sourceRevision !== revisionAtStart) return { kind: "reconcile" };
      if (!payload) return { kind: "reconcile" };
      const { rows, turns } = payload;
      if (!(await this.#durableAuthoritiesUnchanged())) return { kind: "reconcile" };
      if (!this.#acceptsLifetime(lifetime)) return { kind: "server" };
      if (this.#sourceRevision !== revisionAtStart) return { kind: "reconcile" };
      const oldest = rows[0]?.position;
      const nextBefore =
        oldest && rows.length === limit && comparePosition(oldest, local.floor) > 0 ? oldest : undefined;
      const restoredRows = new Map(rows.map((row) => [row.transcriptKey, row]));
      // Eviction does not change Clear/deletion generations. A short raw page
      // therefore proves exhaustion only if it restores every held identity
      // in the consumed interval, not merely every row still present on disk.
      for (const turn of (this.#model ?? current).history?.turns ?? []) {
        for (const item of turn.items) {
          if (
            !item.position ||
            comparePosition(item.position, nextBefore ?? local.floor) < 0 ||
            comparePosition(item.position, local.before) >= 0
          )
            continue;
          const row = item.transcriptKey ? restoredRows.get(item.transcriptKey) : undefined;
          if (!row || row.version < (item.version ?? 0) || comparePosition(row.position, item.position) !== 0) {
            return { kind: "reconcile" };
          }
        }
      }
      const restored = this.#restoreWindow(this.#model ?? current, rows, turns, () => this.#acceptsLifetime(lifetime));
      if (!restored) return { kind: "server" };
      // Item rows can outlive their separately read turn metadata. A turn
      // removed from the hydrated display cannot restore those rows, so they
      // must remain reachable through the existing local continuation.
      const restoredTurns = new Set(restored.turns.map((turn) => turn.id));
      if (this.#turnIDsForRows(rows, []).some((turnId) => !restoredTurns.has(turnId))) {
        return { kind: "reconcile" };
      }
      if (beforePublish && !beforePublish()) return { kind: "superseded" };
      this.#localOlder = nextBefore ? { ...local, before: nextBefore } : undefined;
      this.#model = restored;
      this.#publish();
      onPublished?.();
      return { kind: "local", model: restored };
    }
    const before = this.#oldestPosition(current);
    if (!before) return { kind: "server" };
    // Bound both payload reads with one cache budget. Keep the separately
    // bounded durable-authority guard outside: uncertainty there must reconcile.
    // The raced work only returns data, so late results can never publish.
    let lookupActive = true;
    const lookup = async () => {
      let window = await this.#cache.getOlderWindow(this.#binding.historyKey, before, limit, this.#baselineIdentity);
      if (!lookupActive || !this.#acceptsLifetime(lifetime) || this.#sourceRevision !== revisionAtStart) {
        return undefined;
      }
      if (
        window.coverage.length === 0 &&
        this.#localReadContinuation !== undefined &&
        this.#localReadContinuation === this.#coverageReadId
      ) {
        // Read/suffix certificates predate page authority. Only this live
        // baseline's exact read can supply their local continuation. The cache
        // rejects competing authorities, and the normal revision/deadline
        // guards below still fence both lookups and the eventual publication.
        const readWindow = await this.#cache.getOlderWindow(this.#binding.historyKey, before, limit);
        if (!lookupActive || !this.#acceptsLifetime(lifetime) || this.#sourceRevision !== revisionAtStart) {
          return undefined;
        }
        if (
          readWindow.coverage.length > 0 &&
          readWindow.coverage.every(
            (coverage) => coverage.readId === this.#coverageReadId && coverage.authority === undefined,
          )
        )
          window = readWindow;
      }
      const turnIds = window.stale ? [] : this.#turnIDsForRows(window.rows, []);
      const turnMetadata = await this.#readTurnMetadata(turnIds);
      return { window, turnMetadata };
    };
    let timer: ReturnType<typeof setTimeout> | undefined;
    let payload: Awaited<ReturnType<typeof lookup>> | undefined;
    try {
      payload = await Promise.race([
        lookup(),
        new Promise<undefined>((resolve) => {
          timer = setTimeout(() => resolve(undefined), SESSION_CACHE_LOOKUP_DEADLINE_MS);
        }),
      ]);
    } catch {
      return { kind: "server" };
    } finally {
      lookupActive = false;
      if (timer !== undefined) clearTimeout(timer);
    }
    if (!this.#acceptsLifetime(lifetime)) return { kind: "server" };
    // An authoritative reconciliation during the lookup retires this window's
    // authority; never publish it into the new baseline.
    if (this.#sourceRevision !== revisionAtStart) return { kind: "reconcile" };
    if (!payload) return { kind: "server" };
    const { window, turnMetadata } = payload;
    // A committed Clear during the lookup retires this window's authority even
    // when the client and source identity are unchanged: never publish rows a
    // cleared epoch no longer owns.
    if (!(await this.#durableAuthoritiesUnchanged())) return { kind: "reconcile" };
    if (!this.#acceptsLifetime(lifetime)) return { kind: "server" };
    if (this.#sourceRevision !== revisionAtStart) return { kind: "reconcile" };
    // Only a connected-but-unverified chain is genuine uncertainty. Empty
    // coverage below the oldest row is an ordinary network continuation.
    if (window.stale) {
      if (window.coverage.length > 0) return { kind: "reconcile" };
      const newest = this.#model ?? current;
      return newest.olderCursor ? { kind: "server" } : { kind: "exhausted", model: newest };
    }
    if (window.rows.length === 0) {
      // Certified local coverage below the oldest row ended. Only a frontier
      // with no server cursor is real history end; a cursor means more history
      // remains on the network.
      if (window.complete && window.serverCursor === undefined) {
        this.#localReadContinuation = undefined;
        return { kind: "exhausted", model: { ...(this.#model ?? current), olderCursor: undefined } };
      }
      return { kind: "server" };
    }
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

  /** Local content continuation, independent of the opaque server cursor. */
  localOlderPosition(): ThreadItemPosition | undefined {
    if (this.#disposed || this.#invalidated) return undefined;
    return this.#localOlder?.sourceRevision === this.#sourceRevision ? this.#localOlder.before : undefined;
  }

  /**
   * The oldest position still missing a real body below a restored window.
   * Bodies the published model already holds are not debt: pages that loaded
   * while a restore was in flight provided theirs, so the continuation must
   * not re-advertise them.
   */
  #missingBodyFloor(model: ThreadModel | undefined, windowStart: ThreadItemPosition): ThreadItemPosition | undefined {
    let floor: ThreadItemPosition | undefined;
    for (const item of (model?.history?.turns ?? []).flatMap((turn) => turn.items)) {
      if (!item.position || comparePosition(item.position, windowStart) >= 0) continue;
      if (!encodeTranscriptItem(item).identityOnly) continue;
      if (!floor || comparePosition(item.position, floor) < 0) floor = item.position;
    }
    return floor;
  }

  /**
   * True when every item the retained foreground interval requires is
   * resident with a real body. The interval runs from the oldest bodied
   * position this revision published — the floor only ever moves deeper, so
   * identities above it that lost their bodies (a release reclaimed them,
   * later updates repopulated only the tail) stay inside the requirement,
   * while identities below it are older demand, not debt. A settled stall
   * accepts only on this standard: the latest window alone cannot vouch for
   * a deeper range.
   */
  holdsResidentForegroundRange(): boolean {
    if (this.#disposed || this.#invalidated) return false;
    const model = this.#model;
    const start = this.#foregroundStart;
    if (!model || !start) return false;
    const required = (model.history?.turns ?? model.turns)
      .flatMap((turn) => turn.items)
      .filter((item) => item.position && comparePosition(item.position, start) >= 0);
    if (required.length === 0) return false;
    const displayed = new Map(
      model.turns.flatMap((turn) => turn.items.map((item) => [item.transcriptKey ?? item.id, item] as const)),
    );
    return required.every((item) => {
      const body = displayed.get(item.transcriptKey ?? item.id);
      return (
        body !== undefined && (body.version ?? 0) >= (item.version ?? 0) && !encodeTranscriptItem(body).identityOnly
      );
    });
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
      // A page certifies older content, never a latest window. Asking the cache
      // for it scans bodies that the fallback would immediately discard. Capture
      // this choice with the same boundary/revision, not mutable state after I/O.
      latestWindowBoundary:
        this.#committedCoverage?.revision === this.#sourceRevision &&
        this.#committedCoverage.coverage.boundary === this.#committedCoverageBoundary &&
        this.#committedCoverage.coverage.kind === "page"
          ? undefined
          : this.#committedCoverageBoundary,
      acceptedCoverageBoundary: this.#coverageBoundary,
      readId: this.#committedCoverageReadId,
      continuityId: this.#committedCoverageContinuityId,
      committedSnapshot: this.#committedCoverageSnapshot,
      sourceRevision: this.#sourceRevision,
      foregroundStart: this.#foregroundStart,
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
        const foregroundStart = cut.foregroundStart;
        const requestedForeground = foregroundStart
          ? (cut.model?.history?.turns ?? cut.model?.turns ?? [])
              .flatMap((turn) => turn.items)
              .filter((item) => item.position && comparePosition(item.position, foregroundStart) >= 0)
              .map((item) => ({ key: item.transcriptKey ?? item.id, version: item.version ?? 0 }))
          : [];
        const sourceRevisionAtOpen = cut.sourceRevision;
        const authorityCurrent = () => sourceRevisionAtOpen === this.#sourceRevision;
        const openingCurrent = () => active && this.#acceptsLifetime(lifetime) && authorityCurrent();
        const checkRetainedRange = (model: ThreadModel | undefined): boolean | undefined => {
          // An exception may follow cancellation or source replacement. Those
          // results cannot judge the obsolete opening's retained requirements.
          if (limit <= 0 || requestedForeground.length === 0 || !openingCurrent()) return undefined;
          const displayed = new Map(
            model?.turns.flatMap((turn) => turn.items.map((item) => [item.transcriptKey ?? item.id, item] as const)),
          );
          return requestedForeground.every((item) => {
            const body = displayed.get(item.key);
            return (
              body !== undefined && (body.version ?? 0) >= item.version && !encodeTranscriptItem(body).identityOnly
            );
          });
        };
        try {
          // The limit bounds first promotion. Reopening a still-owned range
          // must not silently turn it back into that initial latest window.
          if (limit > 0) limit = Math.max(limit, requestedForeground.length);
          const openingCutBoundary = cut.boundary;
          const coverageBoundary = cut.committedBoundary;
          const acceptedBoundaryAtOpen = cut.acceptedCoverageBoundary;
          // Suffix certificates are mutable per readId. Resolve the latest real
          // certificate and prove that it covers this reader's fixed cut.
          let window = await this.#cache.getLatestWindow(this.#binding.historyKey, limit, cut.latestWindowBoundary);
          if (!openingCurrent()) {
            return { model: latest ?? this.#model, status: "recovery-needed", boundary: this.#boundary };
          }
          if (this.#seedAuthorityInvalid) {
            // A disk-only opening may display its bounded rows, including prior
            // older pages, but old coverage cannot certify this browser's source.
            window = {
              rows: await this.#cache.getLatest(this.#binding.historyKey, limit),
              coverage: [],
              complete: false,
              stale: true,
            };
          }
          if (!window.complete && cut.latestWindowBoundary !== undefined) {
            window = { ...window, rows: [] };
            window = await this.#cache.getLatestWindow(this.#binding.historyKey, limit);
          }
          // A committed page boundary is not a latest-window certificate. The
          // fallback above selects only the latest read, so join certified
          // older rows from this baseline to restore the window already loaded
          // at the opening cut, without treating arbitrary disk rows as current.
          const loadedStart = cut.model && this.#oldestPosition(cut.model);
          const latestStart = window.rows[0]?.position;
          if (
            window.complete &&
            loadedStart &&
            latestStart &&
            comparePosition(loadedStart, latestStart) < 0 &&
            window.rows.length < limit
          ) {
            // Durable rows below the certified window that the opening cut had
            // loaded. The certified older query is scoped to connected
            // certificates and deliberately refuses numeric adjacency, so its
            // returned set can be gapped or stop short of the opening cut's
            // oldest row; no probe of its depth can prove the loaded interval is
            // filled. Take the whole interval from the bounded durable rows and
            // merge in any certified rows the query did return, letting the
            // keyed, version-ordered dedupe below decide which row wins. The
            // certified rows keep their job of adding certification; the merge
            // itself certifies no interval and hands out no cursor, so a
            // reopened pane is never silently truncated to the latest window
            // (global-contracts: isolated stored seeds merge without certifying
            // an interval).
            let olderRows: readonly TranscriptItemRow<unknown>[] = [];
            if (cut.authorityValid) {
              const older = await this.#cache.getOlderWindow(
                this.#binding.historyKey,
                latestStart,
                limit - window.rows.length,
                this.#baselineIdentity,
              );
              if (!older.stale) olderRows = older.rows;
            }
            const durableOlder = await this.#cache.getLatest(this.#binding.historyKey, limit);
            const olderByKey = new Map<string, TranscriptItemRow<unknown>>();
            for (const row of [...olderRows, ...durableOlder]) {
              const held = olderByKey.get(row.transcriptKey);
              if (!held || held.version <= row.version) olderByKey.set(row.transcriptKey, row);
            }
            const boundedOlder = [...olderByKey.values()].filter(
              (row) =>
                comparePosition(row.position, latestStart) < 0 && comparePosition(row.position, loadedStart) >= 0,
            );
            if (boundedOlder.length > 0) {
              boundedOlder.sort((left, right) => comparePosition(left.position, right.position));
              window = { ...window, rows: [...boundedOlder, ...window.rows] };
            }
          }
          if (!openingCurrent()) {
            return { model: latest ?? this.#model, status: "recovery-needed", boundary: this.#boundary };
          }
          cut.cacheRows = [...window.rows];
          window = { ...window, rows: [] };
          const turnIds = this.#turnIDsForRows(cut.cacheRows, cut.pendingRows);
          const turnMetadata = await this.#readTurnMetadata(turnIds);
          if (!openingCurrent()) {
            return { model: latest ?? this.#model, status: "recovery-needed", boundary: this.#boundary };
          }
          // A committed Clear during the reads retires the snapshot's authority
          // even when client and source identity are unchanged: never publish a
          // locally opened window the cleared epoch no longer owns. An
          // UNESTABLISHED lookup (a slow or unreadable storage read) is not a
          // change: restore the durable bodies but never claim currentness, so a
          // transient lookup delay cannot publish a body-less transcript.
          const durableAuthorities = await this.#durableAuthoritiesState();
          if (durableAuthorities === "changed") {
            return { model: latest ?? this.#model, status: "recovery-needed", boundary: this.#boundary };
          }
          const current = cut.model ?? this.#model;
          if (!current) return { status: window.complete ? "stale" : "recovery-needed" };
          const rowsByKey = new Map<string, TranscriptItemRow<unknown>>();
          for (const row of [...cut.cacheRows, ...cut.pendingRows]) {
            const held = rowsByKey.get(row.transcriptKey);
            const incomingItem = decodeTranscriptItem(row.item as CachedSessionItem);
            const heldItem = held && decodeTranscriptItem(held.item as CachedSessionItem);
            if (!incomingItem) continue;
            const item = mergeLocalItem(heldItem, incomingItem);
            const winner = held && held.version >= row.version ? held : row;
            rowsByKey.set(row.transcriptKey, { ...winner, item: encodeTranscriptItem(item) });
          }
          const localRows = [...rowsByKey.values()].sort((left, right) =>
            comparePosition(left.position, right.position),
          );
          const boundedRows = limit === 0 ? [] : localRows.slice(-limit);
          const restored = this.#restoreWindow(current, boundedRows, turnMetadata, openingCurrent);
          if (!restored || !openingCurrent()) {
            return { model: latest ?? this.#model, status: "recovery-needed", boundary: this.#boundary };
          }
          // A racing accepted model wins by item version; the stale query only
          // fills absent slots. Local older pages and accepted notifications
          // also replace the model without advancing the read boundary, so
          // merge whenever the repository's model moved past the opening's
          // captured cut — a late restore must never discard them.
          latest =
            this.#boundary > openingCutBoundary || this.#model !== current
              ? this.#mergeReaderModels(latest ?? current, restored)
              : restored;
          const retainedRangeComplete = checkRetainedRange(latest);
          const oldestReturned = boundedRows[0]?.position;
          const certifiedStart = window.coverage[0]?.start;
          const localBefore =
            oldestReturned && certifiedStart && comparePosition(certifiedStart, oldestReturned) < 0
              ? oldestReturned
              : undefined;
          // Keep the unrestored identities: raw paging uses them to detect
          // eviction rather than mistaking a short page for exhaustion. The
          // continuation must describe bodies still missing in the published
          // model, not the opening's captured cut: local pages that loaded
          // while this restore was in flight already provided theirs, and
          // re-advertising loaded history only risks a redundant lookup that
          // can reconcile the retained range away.
          this.#model = latest;
          const missingFloor = oldestReturned ? this.#missingBodyFloor(latest, oldestReturned) : undefined;
          this.#localOlder =
            missingFloor && oldestReturned
              ? { before: oldestReturned, floor: missingFloor, sourceRevision: sourceRevisionAtOpen }
              : undefined;
          this.#publish();
          const certifiedSnapshot = window.coverage.at(-1)?.representedSnapshot;
          const seedCovered =
            this.#seedSnapshot === undefined ||
            (certifiedSnapshot !== undefined && certifiedSnapshot >= this.#seedSnapshot);
          const pendingCutCovered =
            !cut.hadPendingWrites ||
            (acceptedBoundaryAtOpen !== undefined && acceptedBoundaryAtOpen !== coverageBoundary);
          // A healthy source read can prove there are no recorded bodies to
          // restore before its empty certificate commits. This is not vacuous
          // coverage: an accepted read named an empty window, the full retained
          // history is empty, and no admission loss or recovery is outstanding.
          // Keep durable currentness separate, so this case is only "stale".
          const provenEmptySourceWindow =
            this.#coverageReadId !== undefined &&
            this.#coverageStart === null &&
            this.#coverageEnd === null &&
            latest?.history?.turns.every((turn) => turn.items.length === 0) === true &&
            cut.authorityValid &&
            durableAuthorities === "unchanged" &&
            !this.#seedAuthorityInvalid &&
            !this.#cacheContinuityLost &&
            !this.#recoveryNeeded;
          const status =
            window.complete &&
            !window.stale &&
            ((cut.readId === undefined && this.#seedSnapshot !== undefined) ||
              this.#coversOpeningCut(window.coverage, cut.readId, cut.continuityId, cut.committedSnapshot)) &&
            seedCovered &&
            pendingCutCovered &&
            cut.authorityValid &&
            durableAuthorities === "unchanged" &&
            !this.#cacheContinuityLost &&
            !this.#recoveryNeeded
              ? "current"
              : provenEmptySourceWindow
                ? "stale"
                : "recovery-needed";
          if (localBefore) this.#localReadContinuation = this.#coverageReadId;
          const before = (missingFloor ? oldestReturned : undefined) ?? localBefore;
          return {
            model: latest ?? current,
            status: retainedRangeComplete === false ? "recovery-needed" : status,
            ...(retainedRangeComplete === undefined ? {} : { retainedRangeComplete }),
            boundary: this.#boundary,
            ...(before ? { before } : {}),
            olderCursor: window.serverCursor ?? current.olderCursor,
          };
        } catch {
          const retainedRangeComplete = checkRetainedRange(latest);
          return {
            model: latest,
            status: "recovery-needed",
            ...(retainedRangeComplete === undefined ? {} : { retainedRangeComplete }),
            boundary: this.#boundary,
          };
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
  async settled(includeEnd = true): Promise<void> {
    if (includeEnd) await this.#endWork;
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
    this.#capacityRefusal = undefined;
    this.#capacityRefusedOffer = undefined;
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
    this.#capacityRefusal = undefined;
    this.#capacityRefusedOffer = undefined;
    this.#cancelRetirement?.();
    this.#endRetryCancel?.();
    this.#endRetryCancel = undefined;
    this.#unregisterPreferred?.();
    this.#unregisterPreferred = undefined;
    this.#cancelRetry();
    this.#abandonBackgroundAccepts("cancelled");
    this.#releaseHandoffOverlay();
    this.#listeners.clear();
    this.#pending.clear();
    this.#model = undefined;
  }

  /** Reacquisition cancels the final read without retiring the live page authority. */
  cancelRetirement(): void {
    if (!this.#retirement) return;
    this.#cancelRetirement?.();
    this.#cancelRetirement = undefined;
    this.#retirement = undefined;
    this.#retirementWrite = undefined;
    this.#sealed = false;
  }

  /** Fresh response seals inputs, then joins only the writes accepted at that cut. */
  retire(): Promise<TranscriptRetirementOutcome> {
    if (this.#retirement) return this.#retirement;
    this.#endRetryCancel?.();
    this.#endRetryCancel = undefined;
    let active = true;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    const current = () => active && this.#acceptsLifetime(this.#binding.lifetime);
    const cancelled = new Promise<TranscriptRetirementOutcome>((resolve) => {
      this.#cancelRetirement = () => {
        active = false;
        resolve({ kind: "cancelled" });
      };
    });
    const work = (async (): Promise<TranscriptRetirementOutcome> => {
      const read = this.#binding.readHistory;
      if (!read || !current()) return { kind: "stale" };
      const response = await read(undefined, controller.signal);
      if (!current()) return { kind: "cancelled" };
      if (!response.snapshot || !this.#matchesRead(response)) throw new Error("final read has no authority");
      const boundary = `final:${this.#boundary}:${response.snapshot.length}`;
      const finalWrite = { boundary, persisted: false };
      this.#retirementWrite = finalWrite;
      if (
        !this.acceptRead(response, {
          lifetime: this.#binding.lifetime,
          readId: boundary,
          boundary,
          preserveEnded: true,
        })
      )
        throw new Error("final read was discarded");
      this.#sealed = true;
      await this.settled(false);
      if (!current()) return { kind: "cancelled" };
      const unresolvedEnd = Object.values(this.#model?.overlay ?? {}).some(
        (item) => item.kind !== "notice" && item.roundId && this.#endClaims.has(item.roundId),
      );
      // Settlement can also mean a policy refusal dropped the work. Track this
      // read's commit, not the last-completing (possibly earlier) suffix.
      const persisted = !this.#cacheContinuityLost && finalWrite.persisted;
      return { kind: this.#recoveryNeeded || unresolvedEnd || !persisted ? "stale" : "saved" };
    })();
    const retirement: Promise<TranscriptRetirementOutcome> = Promise.race([
      work,
      cancelled,
      new Promise<TranscriptRetirementOutcome>((resolve) => {
        timer = setTimeout(() => resolve({ kind: "stale" }), 30_000);
      }),
    ])
      .catch((): TranscriptRetirementOutcome => ({ kind: current() ? "stale" : "cancelled" }))
      .then((outcome) => {
        if (this.#retirement !== retirement) return { kind: "cancelled" } as const;
        this.#sealed = true;
        if (outcome.kind === "stale" && current()) {
          this.#recoveryNeeded = true;
          if (this.#model) this.#model = invalidateHistory(this.#model);
          this.#publish();
        }
        return outcome;
      })
      .finally(() => {
        active = false;
        controller.abort();
        if (this.#retirement === retirement) this.#cancelRetirement = undefined;
        if (timer !== undefined) clearTimeout(timer);
      });
    this.#retirement = retirement;
    return retirement;
  }

  #startEndDisposition(): void {
    if (
      this.#retirement ||
      !this.#binding.readHistory ||
      this.#endWork ||
      this.#endRetryCancel ||
      !this.#acceptsLifetime(this.#binding.lifetime)
    )
      return;
    // Defer once so a recorded publication in the same delivery batch can retire
    // its covered slots before the authoritative request is issued.
    this.#endWork = Promise.resolve()
      .then(async () => {
        const rounds = new Set(this.#endClaims);
        if (rounds.size === 0 || !this.#acceptsLifetime(this.#binding.lifetime)) return;
        let active = true;
        const controller = new AbortController();
        let timer: ReturnType<typeof setTimeout> | undefined;
        try {
          // Same 30-second deadline as AppwireClient's ordinary thread/read. One
          // attempt owns that deadline, not one new interval per returned page.
          await Promise.race([
            this.#resolveEnd(
              rounds,
              () => active && !this.#retirement && this.#acceptsLifetime(this.#binding.lifetime),
              controller.signal,
            ),
            new Promise<never>((_resolve, reject) => {
              timer = setTimeout(() => reject(new Error("end disposition timed out")), 30_000);
            }),
          ]);
          if (this.#retirement || !this.#acceptsLifetime(this.#binding.lifetime)) return;
          for (const round of rounds) {
            this.#endClaims.delete(round);
            this.#lastDisposedRound = round;
          }
          this.#endRetryAttempt = 0;
        } catch {
          if (this.#retirement || !this.#acceptsLifetime(this.#binding.lifetime)) return;
          this.#recoveryNeeded = true;
          if (this.#readerCount === 0) this.#dropEnded(rounds);
          if (this.#model) this.#model = invalidateHistory(this.#model);
          this.#publish();
          this.#paceEndRetry();
        } finally {
          active = false;
          controller.abort();
          if (timer !== undefined) clearTimeout(timer);
        }
      })
      .finally(() => {
        this.#endWork = undefined;
        if (this.#endClaims.size > 0 && !this.#endRetryCancel) this.#startEndDisposition();
      });
  }

  #paceEndRetry(): void {
    if (this.#endRetryCancel || !this.#binding.readHistory) return;
    const attempt = ++this.#endRetryAttempt;
    const fire = () => {
      this.#endRetryCancel = undefined;
      this.#startEndDisposition();
    };
    if (this.#binding.retryScheduler) this.#endRetryCancel = this.#binding.retryScheduler(attempt, fire);
    else {
      const timer = setTimeout(fire, Math.min(2_000, 250 * 2 ** Math.min(attempt - 1, 3)));
      this.#endRetryCancel = () => clearTimeout(timer);
    }
  }

  #dropEnded(rounds: ReadonlySet<string>): void {
    if (!this.#model) return;
    for (const roundId of rounds)
      this.#model = applyNotification(
        this.#model,
        {
          method: "overlay/end",
          params: { ref: this.#binding.ref, threadId: this.#binding.threadId, roundId },
        },
        this.#now(),
      );
  }

  async #resolveEnd(rounds: ReadonlySet<string>, current: () => boolean, signal: AbortSignal): Promise<void> {
    const model = this.#model;
    const read = this.#binding.readHistory;
    if (!model || !read) throw new Error("end disposition has no source");
    // Only identities and positions are captured. Bodies remain in the real
    // model, charged by the existing handoff owner, until disposition.
    const anchors = Object.values(model.overlay ?? {})
      .flatMap((item) =>
        item.kind !== "notice" && item.roundId && rounds.has(item.roundId) && item.anchor ? [item.anchor] : [],
      )
      .sort(comparePosition);
    let unresolved = Object.values(model.overlay ?? {})
      .filter((item) => item.kind !== "notice" && item.roundId && rounds.has(item.roundId) && !item.anchor)
      .map(({ kind, roundId, callId, historyKey }) => ({ kind, roundId, callId, historyKey }));
    // Match pruneCoveredOverlay's identities, but only against this attempt's
    // accepted fixed-snapshot content, not later traffic in the live model.
    const cover = (items: ThreadItem[]) => {
      unresolved = unresolved.filter(
        (slot) =>
          !items.some((item) => {
            switch (slot.kind) {
              case "stream":
                return !!slot.roundId && item.roundId === slot.roundId;
              case "preview":
                return !!slot.callId && item.type === "agentMessage" && item.callId === slot.callId;
              case "tool":
                return !!slot.historyKey && item.transcriptKey === slot.historyKey && !!item.completedAtEntry;
              default:
                return false;
            }
          }),
      );
    };
    const held = model.history?.incarnation
      ? { incarnation: model.history.incarnation, length: model.history.length }
      : undefined;
    const response = await read(held, signal);
    if (!current()) return;
    if (!response.snapshot || !this.#matchesRead(response)) throw new Error("end disposition has no history authority");
    const fixed = response.snapshot;
    const boundary = `end:${this.#boundary}:${fixed.length}`;
    if (
      !this.acceptRead(response, { lifetime: this.#binding.lifetime, readId: boundary, boundary, preserveEnded: true })
    )
      throw new Error("end disposition read was discarded");
    cover(this.#readItems(response));
    const minimum = anchors[0];
    let cursor = response.olderCursor;
    let before = this.#rowsFromWindow(response)
      .map((row) => row.position)
      .sort(comparePosition)[0];
    const seen = new Set<string>();
    const needsCoverage = () => unresolved.length > 0 || (minimum && before && comparePosition(minimum, before) < 0);
    while (needsCoverage() && cursor) {
      if (!before || !this.#binding.readHistoryPage || seen.has(cursor))
        throw new Error("end disposition coverage is incomplete");
      seen.add(cursor);
      const page = await this.#binding.readHistoryPage({ snapshot: fixed, cursor, before, signal });
      if (!current()) return;
      // Later traffic cannot move this attempt's history boundary.
      if (
        page.snapshot?.incarnation !== fixed.incarnation ||
        page.snapshot.length !== fixed.length ||
        page.bootGeneration !== response.bootGeneration ||
        page.epoch !== response.epoch
      )
        throw new Error("end disposition page crossed its boundary");
      if (
        !this.acceptPage(page, {
          lifetime: this.#binding.lifetime,
          readId: boundary,
          boundary: `${boundary}:page:${seen.size}`,
          authority: this.#baselineIdentity,
          requestCursor: cursor,
          before,
        })
      )
        throw new Error("end disposition page was discarded");
      cover(page.data.flatMap((turn) => turn.items ?? []));
      const positions = page.data
        .flatMap((turn) => (turn.items ?? []).flatMap((item) => (item.position ? [item.position] : [])))
        .sort(comparePosition);
      before = positions[0] ?? before;
      cursor = page.nextCursor;
    }
    // Exhausting the authoritative interval proves discard for unmatched slots.
    // A missing anchor or an unreceived continuation cannot supply that proof.
    if (needsCoverage() && cursor !== undefined) throw new Error("end disposition coverage is incomplete");
    this.#dropEnded(rounds);
    this.#recoveryNeeded = this.#model?.history?.invalidatedAtGeneration !== undefined;
    this.#publish();
  }

  #acceptsLifetime(lifetime: number): boolean {
    return (
      !this.#disposed &&
      !this.#invalidated &&
      !(this.#binding.suppressed?.() ?? false) &&
      lifetime === this.#binding.lifetime
    );
  }

  #mintBaseline(): string {
    return JSON.stringify([
      this.#binding.sessionNonce ?? PAGE_LIFETIME_NONCE,
      this.#binding.lifetime,
      this.#sourceRevision,
    ]);
  }

  /**
   * Whether the durable Clear epoch this binding captured is still current. An
   * asynchronous lookup that began before a committed Clear or history deletion must not publish the
   * rows it read under the old authorities, even when the client and source identity
   * are unchanged. Bounded by the lookup deadline and fail-closed: an epoch that
   * cannot be established (timeout or read failure) is NOT treated as unchanged,
   * so an unverified result never publishes as current.
   */
  async #durableAuthoritiesUnchanged(): Promise<boolean> {
    return (await this.#durableAuthoritiesState()) === "unchanged";
  }

  /**
   * The durable Clear epoch/history-generation relation, three ways, so a local
   * opening can tell a CONFIRMED change (a committed Clear or deletion: do not
   * publish) apart from an UNESTABLISHED lookup (a slow or unreadable storage
   * read: fail closed for `current` but still restore the bodies the disk holds).
   * Treating a slow read as a change discarded the whole restore and left the
   * oldest recorded rows body-less — a user-visible blank transcript.
   */
  async #durableAuthoritiesState(): Promise<"unchanged" | "changed" | "unestablished"> {
    let timer: ReturnType<typeof setTimeout> | undefined;
    try {
      const authorities = await Promise.race([
        Promise.all([this.#cache.getClearEpoch(), this.#cache.getHistoryGeneration(this.#binding.historyKey)]),
        new Promise<undefined>((resolve) => {
          timer = setTimeout(() => resolve(undefined), SESSION_CACHE_LOOKUP_DEADLINE_MS);
        }),
      ]);
      if (authorities === undefined) return "unestablished";
      return authorities[0] === this.#binding.clearEpoch && authorities[1] === (this.#binding.historyGeneration ?? 0)
        ? "unchanged"
        : "changed";
    } catch {
      return "unestablished";
    } finally {
      if (timer !== undefined) clearTimeout(timer);
    }
  }

  #advanceSourceRevision(): void {
    this.#sourceRevision++;
    this.#foregroundStart = undefined;
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
      // Bounded identity: the chain's fixed base plus the monotonic event
      // counter. Embedding the previous boundary here would retain every prior
      // suffix identifier in one metadata value.
      boundary: `${this.#suffixBoundaryBase ?? this.#coverageBoundary}:suffix:${this.#boundary}`,
      start: this.#coverageStart,
      end: end ?? null,
      representedSnapshot,
    };
  }

  #row(item: ItemModel): TranscriptItemRow<CachedSessionItem> | undefined {
    if (!item.transcriptKey || !item.position) return undefined;
    return {
      historyKey: this.#binding.historyKey,
      transcriptKey: item.transcriptKey,
      position: item.position,
      // Immutable recorded headers have version zero, omitted by the wire
      // encoder. Their key and position still identify a durable source item.
      version: item.version ?? 0,
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
        mergedItems.set(key, mergeLocalItem(mergeBase, item));
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
    return restoreReaderWindow(restored, loadedTurns);
  }

  #mergeReaderModels(held: ThreadModel, incoming: ThreadModel): ThreadModel {
    if (incoming.history) return restoreReaderWindow(held, incoming.history.turns);
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

  async #matchesCapacityRefusal(batch: Parameters<SessionEntryCache["commitBatch"]>[0]): Promise<boolean> {
    try {
      // Reuse the cache's actual merge/encoding plan, including held
      // versions. Fresh provenance is irrelevant, changed richness is not.
      const plan = await this.#cache.planCapacity(batch);
      return plan.identity === this.#capacityRefusal;
    } catch {
      // A failed lookup cannot establish refusal. Let the ordinary write
      // report its storage failure and use the existing recovery path.
      return false;
    }
  }

  /**
   * `retryRevision`, when given, is the source revision the batch was issued
   * under. A retried batch whose revision no longer matches is superseded by a
   * newer boundary, so its completion must not write the old baseline's
   * committed-coverage bookkeeping back over the new baseline's.
   */
  #queueWrite(batch: Parameters<SessionEntryCache["commitBatch"]>[0], retryRevision?: number): Promise<void> {
    if (this.#binding.mayPersist?.() === false) return this.#declinePersistence(batch);
    if (retryRevision === undefined && batch.coverage?.kind === "read" && !batch.replacement) {
      // An ordinary read supersedes certificates, not the accepted payloads
      // omitted by its response. Carry compatible preflight bodies and turns
      // into its write. Newer rows already include the reducer's version/richness
      // merge, so retain one object per key for admission and pending cleanup.
      const inherited = [...this.#preflightWrites]
        .filter(
          ([prior, entry]) =>
            entry.revision === this.#sourceRevision &&
            (prior.coverage?.kind === "read" || prior.coverage?.kind === "suffix"),
        )
        .map(([prior]) => prior);
      if (inherited.length > 0) {
        // Replacement deletion is part of the payload operation, not its old
        // coverage certificate. Union suffix scopes at their earliest boundary.
        const replacementFrom = inherited
          .flatMap((prior) => (prior.replacement ? [prior.replacement.from] : []))
          .sort(comparePosition)[0];
        batch = {
          ...batch,
          replacement: replacementFrom ? { from: replacementFrom } : undefined,
          items: [
            ...new Map(
              [...inherited.flatMap((prior) => prior.items), ...batch.items].map((row) => [row.transcriptKey, row]),
            ).values(),
          ],
          turns: [
            ...new Map(
              [...inherited.flatMap((prior) => prior.turns ?? []), ...(batch.turns ?? [])].map((turn) => [
                turn.id,
                turn,
              ]),
            ).values(),
          ],
        };
      }
    }
    const model = this.#model;
    const history = model?.history;
    if (retryRevision === undefined && model && history?.incarnation !== undefined && !this.#seedAuthorityInvalid) {
      // Do not construct a whole cached record to get these fields: that would
      // encode every body. Pages update the cursor in this scalar header too.
      batch = {
        ...batch,
        sourceAuthority: {
          bootGeneration: history.bootGeneration,
          epoch: history.epoch,
          incarnation: history.incarnation,
          // Recorded appends leave the reducer's read watermark unchanged.
          // Their durable authority is the snapshot this suffix certifies.
          representedSnapshot: batch.coverage?.kind === "suffix" ? batch.coverage.representedSnapshot : history.length,
        },
        sessionMetadata: {
          historyKey: this.#binding.historyKey,
          requestedRefs: [{ clientId: this.#binding.identity.clientId, ref: this.#binding.ref }],
          resolvedIdentity: this.#binding.identity,
          savedAt: this.#now(),
          accessedAt: this.#now(),
          formatVersion: 3,
          cachedHeader: {
            ref: model.ref,
            threadId: model.threadId,
            name: model.name,
            modelProvider: model.modelProvider,
            model: model.model,
            imageSessionId: model.imageSessionId,
            olderCursor: model.olderCursor,
            savedAt: this.#now(),
            history: {
              bootGeneration: history.bootGeneration,
              epoch: history.epoch,
              incarnation: history.incarnation,
              length: history.length,
              appliedGeneration: history.appliedGeneration,
              issuedGeneration: history.issuedGeneration,
            },
          },
        },
      };
    }
    const issuedRevision = retryRevision ?? this.#sourceRevision;
    // Capture only predecessors already accepted for this suffix's chain.
    // Later preflights must not become dependencies of earlier ones.
    const predecessors =
      batch.coverage?.kind === "suffix"
        ? [...this.#preflightWrites]
            .filter(([prior]) => prior.coverage?.readId === batch.coverage?.readId)
            .map(([, entry]) => entry.write)
        : [];
    if (retryRevision === undefined && this.#capacityRefusal !== undefined) {
      // A lookup retains its candidate just like a write. Charge it to ordinary
      // admission, never the sole oversize write exemption. If it cannot fit,
      // decline caching without starting a lookup or requesting reconciliation:
      // admission pressure cannot establish that the refused content changed.
      const bodies = retainedBodies(batch);
      if (!this.#admission.retain(bodies, this, "writing")) {
        // No lookup may retain this candidate. A structurally impossible memo
        // match needs no lookup, and still has ordinary write eligibility.
        if (!mayMatchCapacityIdentity(batch, this.#capacityRefusal)) {
          return this.#admitWrite(batch, issuedRevision, false, predecessors);
        }
        // Structural equality is not a confirmed memo hit. A changed offer
        // deserves a real write even when it cannot fit a preflight lookup.
        // The cache's actual merge still decides whether it fits disk.
        if (offeredContentFingerprint(batch) !== this.#capacityRefusedOffer) {
          return this.#admitWrite(batch, issuedRevision, false, predecessors);
        }
        // Suppressing reconciliation does not erase the dropped extent. A later
        // narrower commit must not claim it repaired this unpersisted coverage.
        this.#recordAbandonment("failed", [batch.coverage]);
        return this.#declinePersistence(batch);
      }
      this.#inFlight.set(batch, bodies);
      const check = async () => {
        let memoized = false;
        try {
          memoized = await this.#matchesCapacityRefusal(batch);
          await Promise.all(predecessors);
        } finally {
          this.#inFlight.delete(batch);
          this.#admission.release(bodies, this);
        }
        // Ordinary reads share a source revision but replace the read window.
        // A delayed plan must not commit the window an intervening read replaced.
        // Pages have independent request IDs and belong to the page authority,
        // not to the baseline read ID.
        if (
          issuedRevision !== this.#sourceRevision ||
          ((batch.coverage?.kind === "read" || batch.coverage?.kind === "suffix") &&
            batch.coverage.readId !== this.#coverageReadId) ||
          (batch.coverage?.kind === "page" && batch.coverage.authority !== this.#baselineIdentity)
        ) {
          for (const row of batch.items) {
            if (this.#pending.get(row.transcriptKey) === row) this.#pending.delete(row.transcriptKey);
          }
          return;
        }
        if (memoized || this.#disposed || this.#invalidated) return this.#declinePersistence(batch);
        return this.#admitWrite(batch, issuedRevision, false);
      };
      const checked = check();
      this.#preflightWrites.set(batch, { revision: issuedRevision, write: checked });
      this.#writes.add(checked);
      void checked.finally(() => {
        this.#preflightWrites.delete(batch);
        this.#writes.delete(checked);
        this.#reclaimBackgroundModel();
      });
      return checked;
    }
    return this.#admitWrite(batch, issuedRevision, retryRevision !== undefined, predecessors);
  }

  #admitWrite(
    batch: Parameters<SessionEntryCache["commitBatch"]>[0],
    issuedRevision: number,
    isRetry: boolean,
    predecessors: Promise<void>[] = [],
  ): Promise<void> {
    if (this.#binding.mayPersist?.() === false) return this.#declinePersistence(batch);
    const bodies = retainedBodies(batch);
    if (predecessors.length > 0) {
      // Even candidates that bypass a capacity lookup must not overtake one.
      // Waiting is ordinary retained work, never an unbounded exempt write.
      if (!this.#admission.retain(bodies, this, "background")) return this.#dropForOverflow(batch);
      const waiting = Promise.all(predecessors).then(() => {
        this.#admission.release(bodies, this);
        if (this.#disposed || this.#invalidated || issuedRevision !== this.#sourceRevision) {
          return this.#declinePersistence(batch);
        }
        return this.#admitWrite(batch, issuedRevision, isRetry);
      });
      this.#writes.add(waiting);
      void waiting.finally(() => this.#writes.delete(waiting));
      return waiting;
    }
    const bodyBytes = bodies.reduce((sum, body) => sum + body.bytes, 0);
    if (isRetry) {
      // The background drain already holds the slot and retained these bodies.
      return this.#issueCommit(batch, bodies, issuedRevision, true, false);
    }
    const readId = batch.coverage?.kind === "suffix" ? batch.coverage.readId : undefined;
    const waitsForPredecessor =
      readId !== undefined &&
      (this.#retryQueue.some((entry) => entry.batch.coverage?.readId === readId) ||
        (this.#retryInFlight && [...this.#inFlight.keys()].some((entry) => entry.coverage?.readId === readId)) ||
        this.#backgroundAccepts.some((entry) => entry.batch.coverage?.readId === readId));
    if (waitsForPredecessor) {
      // Publication already happened. Persistence must not overtake a failed
      // predecessor, including while its retry or queued successors are draining.
      if (!this.#admission.retain(bodies, this, "background")) return this.#dropForOverflow(batch);
      this.#backgroundAccepts.push({ batch, revision: issuedRevision, bodies });
      this.#continueBackground();
      return Promise.resolve();
    }
    const oversize = bodyBytes > this.#admission.limit;
    if (oversize) {
      // The sole immediate bounded batch. Only one may be in flight; a competing
      // response is refused for caching rather than starting a second unbounded
      // immediate write.
      if (!this.#admission.retainExempt(bodies, this)) {
        if (this.#capacityRefusal !== undefined) {
          this.#recordAbandonment("failed", [batch.coverage]);
          return this.#declinePersistence(batch);
        }
        return this.#dropForOverflow(batch);
      }
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
    if (issuedRevision < this.#replacementRevision) {
      this.#admission.release(bodies, this);
      if (isExempt) this.#admission.releaseExempt(this);
      for (const row of batch.items) {
        if (this.#pending.get(row.transcriptKey) === row) this.#pending.delete(row.transcriptKey);
      }
      return Promise.resolve();
    }
    if (this.#binding.mayPersist?.() === false) {
      this.#admission.release(bodies, this);
      if (isExempt) this.#admission.releaseExempt(this);
      return this.#declinePersistence(batch);
    }
    this.#inFlight.set(batch, bodies);
    let memoized = false;
    const commit = async (): Promise<Awaited<ReturnType<SessionEntryCache["commitBatch"]>> | undefined> => {
      const matches = isRetry && this.#capacityRefusal !== undefined && (await this.#matchesCapacityRefusal(batch));
      // The capacity lookup yields to replacements and source changes. Retire
      // stale work before issuing a transaction, not only in its bookkeeping.
      if (
        this.#disposed ||
        this.#invalidated ||
        issuedRevision < this.#replacementRevision ||
        (isRetry && issuedRevision !== this.#sourceRevision)
      ) {
        return undefined;
      }
      if (matches) {
        memoized = true;
        return { outcome: "refused", reason: "capacity" };
      }
      return this.#cache.commitBatch(batch);
    };
    const write = commit().then((outcome) => {
      const superseded =
        issuedRevision < this.#replacementRevision || (isRetry && issuedRevision !== this.#sourceRevision);
      if (outcome && batch.coverage?.boundary === this.#retirementWrite?.boundary && this.#retirementWrite) {
        this.#retirementWrite.persisted = outcome.outcome === "committed" && outcome.coverage !== "not-extended";
      }
      if (!outcome) {
        this.#admission.release(bodies, this);
        for (const row of batch.items) {
          if (this.#pending.get(row.transcriptKey) === row) this.#pending.delete(row.transcriptKey);
        }
      } else if (outcome.outcome === "committed") {
        if (batch.replacement && !superseded) this.#uncommittedReplacementFrom = undefined;
        this.#retryAttempt = 0;
        // Restoration is conditioned on the outcome's coverage, not the offered
        // input: only an `extended` certificate actually repairs the chain (an
        // `unchanged` batch adds none; `not-extended` means the row did not
        // survive policy or a suffix could not extend, and the cache refuses to
        // certify it). A superseded retry likewise contributes nothing.
        if (!superseded && batch.coverage && outcome.coverage === "extended") {
          // Clearing continuity loss is a claim that this commit's coverage
          // carries any outstanding abandoned work forward, not merely that the
          // repository is live. A later, narrower window (or a different window
          // at the same snapshot) extends its own certificate while the
          // abandoned extent stays missing, so it must not certify a partial
          // local view as current. When no abandonment is outstanding (an
          // ordinary transient failure whose own retry re-covers it, or a
          // lifted persistence suppression) continuity is restored.
          const abandoned = this.#recoveryAbandoned;
          const refused = this.#capacityRefusedReach;
          const snapshot = batch.coverage.representedSnapshot;
          // A committed authoritative replacement deletes every omitted row
          // from its floor onward. It proves that obsolete refused positions
          // no longer exist, even when its new certificate ends before them.
          // A refused prefix below that floor, or a newer abandoned snapshot,
          // remains an obligation. Position alone does not identify a version.
          const replacesReach = (reach: AbandonedCoverage) =>
            batch.replacement &&
            reach.start &&
            snapshot >= reach.snapshot &&
            comparePosition(batch.replacement.from, reach.start) <= 0;
          if (refused && (replacesReach(refused) || commitCarriesCoverage(batch.coverage, refused))) {
            this.#capacityRefusedReach = undefined;
          }
          const repairsAbandoned =
            abandoned !== undefined &&
            (replacesReach(abandoned.reach) || commitCarriesCoverage(batch.coverage, abandoned.reach));
          if (this.#capacityRefusedReach === undefined && (abandoned === undefined || repairsAbandoned)) {
            this.#cacheContinuityLost = false;
          }
          // Clearing the disposition itself additionally requires a live owner:
          // a dead repository takes no further work, so its own late completion
          // can never repair a different cancelled batch.
          if (!this.#invalidated && !this.#disposed && abandoned !== undefined && repairsAbandoned) {
            this.#recoveryAbandoned = undefined;
            this.#reconciledReach = undefined;
          }
        }
        if (!superseded && batch.coverage && outcome.coverage !== "not-extended") {
          this.#committedCoverage = { coverage: batch.coverage, revision: issuedRevision };
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
        if (!superseded && batch.coverage && outcome.coverage === "not-extended") {
          // A committed row is not a durable certificate. In particular, an
          // overlapping suffix can block its baseline until an authoritative
          // read replaces it. Reuse the bounded, reach-deduplicated repair path.
          // A retry may finish after that replacement. Its old blocked chain
          // cannot invalidate a current certificate that already covers its
          // complete range and snapshot.
          const committed = this.#committedCoverage;
          const alreadyCovered =
            !this.#cacheContinuityLost &&
            committed?.revision === this.#sourceRevision &&
            committed.coverage.readId === this.#coverageReadId &&
            committed.coverage.readId !== batch.coverage.readId &&
            commitCarriesCoverage(committed.coverage, coverageReach(batch.coverage));
          if (!alreadyCovered) {
            this.#cacheContinuityLost = true;
            this.#recoveryNeeded = true;
            this.#recordAbandonment("failed", [batch.coverage]);
            this.#scheduleReconcile();
          }
        }
      } else {
        if (!superseded) this.#cacheContinuityLost = true;
        if (!superseded && outcome.outcome === "refused" && outcome.reason === "capacity") {
          const reach = coverageReach(batch.coverage);
          this.#capacityRefusedReach = this.#capacityRefusedReach
            ? widerReach(this.#capacityRefusedReach, reach)
            : reach;
        }
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
        // Only the cache can prove intrinsic oversize using its disk encoding
        // and limit. Queue exemption says nothing about whether eviction or a
        // smaller certificate could cure this refusal.
        const plan = this.#cache.refusedCapacityPlan(batch);
        if (
          !memoized &&
          !superseded &&
          outcome.outcome === "refused" &&
          outcome.reason === "capacity" &&
          !this.#disposed &&
          !this.#invalidated &&
          this.#cache.isIntrinsicCapacityRefusal(plan)
        ) {
          this.#capacityRefusal = plan?.identity;
          this.#capacityRefusedOffer = offeredContentFingerprint(batch);
        }
        if (isExempt && !memoized && !superseded) this.#recordAbandonment("failed", [batch.coverage]);
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

  #declinePersistence(batch: Parameters<SessionEntryCache["commitBatch"]>[0]): Promise<void> {
    for (const row of batch.items) {
      if (this.#pending.get(row.transcriptKey) === row) this.#pending.delete(row.transcriptKey);
    }
    // No local certificate can claim this deliberately unpersisted input.
    // Unlike an overflow, the owner's suppression must not request repairs.
    this.#cacheContinuityLost = true;
    this.#reclaimBackgroundModel();
    return Promise.resolve();
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
    if (this.#readerCount === 0 && this.#model?.history) {
      // Refused rows have no admission charge. Remove only those versions,
      // independently of unrelated admitted work whose display must survive.
      // Reconciliation restores their content; active overlay stays owned here.
      const refused = new Map(batch.items.map((row) => [row.transcriptKey, row.version]));
      const retain = (item: ItemModel) =>
        item.transcriptKey === undefined ||
        !refused.has(item.transcriptKey) ||
        refused.get(item.transcriptKey) !== (item.version ?? 0);
      this.#model = {
        ...this.#model,
        history: {
          ...this.#model.history,
          turns: this.#model.history.turns.map((turn) => ({ ...turn, items: turn.items.filter(retain) })),
        },
        turns: this.#model.turns.map((turn) => ({ ...turn, items: turn.items.filter(retain) })),
      };
    }
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
  // without demand, or after cancel, the failure is an explicit disposition.
  // Demand is not the same as an attached reader: a desired Live background
  // session holds demand with no reader.
  #hasDemand(): boolean {
    return this.#readerCount > 0 || this.#binding.isPreferred?.() === true;
  }

  #enqueueRetry(
    batch: Parameters<SessionEntryCache["commitBatch"]>[0],
    revision: number,
    atFront: boolean,
    bodies: EntryRetainedBody[],
  ): void {
    if (this.#disposed || this.#invalidated || this.#persistenceCleared) {
      this.#recordAbandonment("cancelled", [batch.coverage]);
      this.#admission.release(bodies, this);
      return;
    }
    if (!this.#hasDemand()) {
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
    if (this.#binding.suppressed?.()) return;
    if (!this.#hasDemand()) return;
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
   * accepted write because its causal coverage is required. Foreground writes
   * wait here only for their own repository's predecessors, not other sessions.
   * When the slot is busy this repository waits and is resumed by
   * `releaseBackground`, which prioritises live reader demand.
   */
  #drainBackground(): void {
    if (this.#disposed || this.#invalidated) {
      this.#abandonBackgroundAccepts("cancelled");
      if (this.#retryQueue.length > 0) this.#abandonQueue("cancelled");
      this.#retryQueue.length = 0;
      return;
    }
    // Keep bounded queued work for abort recovery, but do not acquire the slot
    // or issue a new transaction while the owner's Clear is unresolved. An
    // already-issued batch keeps its accounting until its real outcome.
    if (this.#binding.suppressed?.()) return;
    // One repository cannot overlap itself: a second drain call while this
    // repository's own batch is in flight must not issue a concurrent second
    // batch (the shared slot admits a re-entrant same-owner acquisition).
    if (this.#backgroundInFlight) return;
    if (!this.#hasDemand() && this.#retryQueue.length > 0) {
      // An issued retry needs live demand; without it the queued retries are an
      // explicit cancelled disposition.
      this.#abandonQueue("cancelled");
      this.#retryQueue.length = 0;
    }
    if (this.#retryQueue.length > 0 && this.#retryScheduled) return;
    if (this.#retryQueue.length === 0 && this.#backgroundAccepts.length === 0) return;
    if (
      !this.#admission.tryAcquireBackground(
        this,
        () => this.#hasDemand(),
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
  // paced backoff while demand holds; accepted background work (which needs no
  // reader) continues immediately.
  #continueBackground(): void {
    if (this.#retryQueue.length > 0 && this.#hasDemand()) {
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
    this.#model = hydratedDisplay(pruneBackgroundHistoryBodies(this.#model));
  }

  #publish(): void {
    this.#reconcileHandoffOverlay();
    if (this.#model) this.#model = hydratedDisplay(this.#model);
    if (this.#readerCount > 0 && this.#model) {
      for (const turn of this.#model.turns) {
        for (const item of turn.items) {
          if (
            item.transcriptKey &&
            item.position &&
            (!this.#foregroundStart || comparePosition(item.position, this.#foregroundStart) < 0) &&
            !encodeTranscriptItem(item).identityOnly
          ) {
            this.#foregroundStart = item.position;
          }
        }
      }
    }
    for (const listener of this.#listeners) listener();
  }
}
