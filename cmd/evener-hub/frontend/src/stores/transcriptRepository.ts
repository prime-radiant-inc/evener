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
import type { TranscriptItemRow, TranscriptResolvedIdentity, TranscriptSessionMetadata } from "./sessionCacheSchema";
import { entryHistoryKey } from "./sessionCacheSchema";
import type { SessionEntryCache } from "./sessionEntryCache";

function comparePosition(
  left: { entry: number; item: number; sub?: number },
  right: { entry: number; item: number; sub?: number },
): number {
  return left.entry - right.entry || left.item - right.item || (left.sub ?? 0) - (right.sub ?? 0);
}

export interface TranscriptRepositoryBinding {
  ref: string;
  threadId: string;
  historyKey: string;
  identity: TranscriptResolvedIdentity;
  clearEpoch: number;
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

  constructor(cache: SessionEntryCache, binding: TranscriptRepositoryBinding) {
    if (entryHistoryKey(binding.identity) !== binding.historyKey) throw new Error("binding history identity mismatch");
    const { seedModel, ...scalarBinding } = binding;
    this.#cache = cache;
    this.#binding = scalarBinding;
    this.#baselineIdentity = this.#mintBaseline();
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
      this.#recoveryNeeded = true;
      this.#publish();
      return true;
    }
    this.#model = next;
    this.#boundary++;
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

  async settled(): Promise<void> {
    await Promise.all([...this.#writes]);
  }

  invalidate(): void {
    this.#invalidated = true;
    this.#pending.clear();
  }

  dispose(): void {
    if (this.#disposed) return;
    this.#disposed = true;
    this.#listeners.clear();
    this.#pending.clear();
    this.#model = undefined;
  }

  #acceptsLifetime(lifetime: number): boolean {
    return !this.#disposed && !this.#invalidated && lifetime === this.#binding.lifetime;
  }

  #mintBaseline(): string {
    return JSON.stringify([this.#binding.lifetime, this.#sourceRevision]);
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

  #queueWrite(batch: Parameters<SessionEntryCache["commitBatch"]>[0]): void {
    const write = this.#cache.commitBatch(batch).then((outcome) => {
      if (outcome.outcome === "committed") {
        if (batch.coverage) {
          this.#committedCoverageBoundary = batch.coverage.boundary;
          this.#committedCoverageSnapshot = batch.coverage.representedSnapshot;
          this.#committedCoverageReadId = batch.coverage.readId;
          this.#committedCoverageContinuityId =
            batch.coverage.kind === "suffix" ? batch.coverage.continuityId : undefined;
        }
        for (const row of batch.items) {
          if (this.#pending.get(row.transcriptKey) === row) this.#pending.delete(row.transcriptKey);
        }
      } else {
        this.#cacheContinuityLost = true;
        for (const row of batch.items) {
          if (this.#pending.get(row.transcriptKey) === row) this.#pending.delete(row.transcriptKey);
        }
      }
    });
    this.#writes.add(write);
    void write.finally(() => {
      this.#writes.delete(write);
      this.#reclaimBackgroundModel();
    });
  }

  #now(): number {
    return this.#binding.now?.() ?? Date.now();
  }

  #reclaimBackgroundModel(): void {
    if (this.#readerCount > 0 || this.#pending.size > 0 || this.#writes.size > 0 || !this.#model) return;
    this.#model = pruneBackgroundHistoryBodies(this.#model);
  }

  #publish(): void {
    for (const listener of this.#listeners) listener();
  }
}
