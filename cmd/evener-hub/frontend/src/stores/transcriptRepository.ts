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
  mergeTranscriptItemVersioned,
  mergeTranscriptReaderWindow,
  mergeTranscriptTurnVersioned,
  pruneBackgroundHistoryBodies,
  type ThreadItem,
  type ThreadItemPosition,
  type ThreadModel,
  type ThreadReadResponse,
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
  getRetentionSnapshotForTesting(): { openingModelRetained: boolean; pendingPayloadCount: number };
  dispose(): void;
}

interface ReaderOpeningState {
  model?: ThreadModel;
  pendingRows: TranscriptItemRow<CachedSessionItem>[];
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
    if (
      !previousHistory ||
      !acceptedHistory ||
      previousHistory.bootGeneration !== acceptedHistory.bootGeneration ||
      previousHistory.epoch !== acceptedHistory.epoch ||
      previousHistory.incarnation !== acceptedHistory.incarnation ||
      previousHistory.invalidatedAtGeneration !== undefined ||
      previousHistory.failed !== undefined
    ) {
      this.#sourceRevision++;
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
    if (authorityBlocked) this.#sourceRevision++;
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

  attachReader(): TranscriptReader {
    let active = true;
    this.#readerCount++;
    const lifetime = this.#binding.lifetime;
    const captureOpeningState = (): ReaderOpeningState => ({
      model: this.#model,
      pendingRows: [...this.#pending.values()],
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
    const releaseOpeningState = (state: ReaderOpeningState) => {
      state.model = undefined;
      state.pendingRows.length = 0;
      if (openingState === state) openingState = undefined;
    };
    let latest = this.#model;
    const unlisten = this.subscribe(() => {
      latest = this.#model;
    });
    return {
      getSnapshot: () => latest,
      getRetentionSnapshotForTesting: () => ({
        openingModelRetained: openingState?.model !== undefined,
        pendingPayloadCount: openingState?.pendingRows.length ?? 0,
      }),
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
            window = await this.#cache.getLatestWindow(this.#binding.historyKey, limit);
          }
          if (!active || !this.#acceptsLifetime(lifetime) || !authorityCurrent()) {
            return { model: latest ?? this.#model, status: "recovery-needed", boundary: this.#boundary };
          }
          const current = cut.model ?? this.#model;
          if (!current) return { status: window.complete ? "stale" : "recovery-needed" };
          const rowsByKey = new Map<string, TranscriptItemRow<unknown>>();
          for (const row of [...window.rows, ...cut.pendingRows]) {
            const held = rowsByKey.get(row.transcriptKey);
            if (!held || held.version <= row.version) rowsByKey.set(row.transcriptKey, row);
          }
          const localRows = [...rowsByKey.values()].sort((left, right) =>
            comparePosition(left.position, right.position),
          );
          const boundedRows = limit === 0 ? [] : localRows.slice(-limit);
          const restored = await this.#restoreWindow(
            current,
            boundedRows,
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
        if (openingState) releaseOpeningState(openingState);
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

  async #restoreWindow(
    seed: ThreadModel,
    rows: readonly TranscriptItemRow<unknown>[],
    isCurrent: () => boolean,
  ): Promise<ThreadModel | undefined> {
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
      const cached = await this.#cache.getTurn(this.#binding.historyKey, turnId);
      if (!isCurrent()) return undefined;
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
