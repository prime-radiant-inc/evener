import {
  type AnyNotification,
  applyNotification,
  applyReadResponse,
  type CachedSessionItem,
  compareBootGeneration,
  comparePositions,
  decodeTranscriptItem,
  encodeTranscriptItem,
  hydrateThread,
  type ItemModel,
  mergeTranscriptItemVersioned,
  mergeTranscriptReaderWindow,
  mergeTranscriptTurnVersioned,
  pruneBackgroundHistoryBodies,
  type ThreadItem,
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
  now?: () => number;
}

export type TranscriptReaderStatus = "current" | "stale" | "recovery-needed";

export interface TranscriptReaderResult {
  model?: ThreadModel;
  status: TranscriptReaderStatus;
  boundary?: number;
  olderCursor?: string;
}

export interface TranscriptReader {
  openLocal(limit: number): Promise<TranscriptReaderResult>;
  getSnapshot(): ThreadModel | undefined;
  dispose(): void;
}

/** Owns real wire acceptance, publication, local writes and attached readers for one binding. */
export class TranscriptRepository {
  readonly #cache: SessionEntryCache;
  readonly #binding: TranscriptRepositoryBinding;
  readonly #listeners = new Set<() => void>();
  readonly #writes = new Set<Promise<void>>();
  readonly #pending = new Map<string, TranscriptItemRow<CachedSessionItem>>();
  #model: ThreadModel | undefined;
  #boundary = 0;
  #readerCount = 0;
  #coverageBoundary: string | undefined;
  #coverageReadId: string | undefined;
  #continuityId: string | undefined;
  #coverageStart: { entry: number; item: number; sub?: number } | null = null;
  #coverageEnd: { entry: number; item: number; sub?: number } | null = null;
  #representedSnapshot = 0;
  #bootGeneration: string | undefined;
  #epoch: number | undefined;
  #recoveryNeeded = false;
  #disposed = false;
  #invalidated = false;
  #cacheContinuityLost = false;

  constructor(cache: SessionEntryCache, binding: TranscriptRepositoryBinding) {
    if (entryHistoryKey(binding.identity) !== binding.historyKey) throw new Error("binding history identity mismatch");
    this.#cache = cache;
    this.#binding = binding;
    this.#model = binding.seedModel;
  }

  getSnapshot = (): ThreadModel | undefined => this.#model;

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
    this.#boundary = Math.max(this.#boundary + 1, response.snapshot?.length ?? 0);
    this.#coverageBoundary = request.boundary;
    this.#coverageReadId = request.readId;
    this.#continuityId ??= `${this.#binding.lifetime}:${request.readId}`;
    this.#bootGeneration = response.bootGeneration;
    this.#epoch = response.epoch;
    this.#recoveryNeeded = candidate.history?.invalidatedAtGeneration !== undefined;
    const rows = this.#rowsFromRead(response);
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
        start: positions[0] ?? null,
        end: positions.at(-1) ?? null,
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
    if (notification.method === "history/updated") {
      const history = notification.params as {
        bootGeneration: string;
        epoch: number;
        snapshot: { incarnation: string };
      };
      const generation =
        this.#bootGeneration === undefined
          ? "replace"
          : compareBootGeneration(this.#bootGeneration, history.bootGeneration);
      if (
        history.snapshot.incarnation !== this.#binding.identity.incarnation ||
        generation === "replace" ||
        (this.#epoch !== undefined && history.epoch > this.#epoch)
      ) {
        this.#recoveryNeeded = true;
        this.#publish();
        return false;
      }
      if (generation === "ignore" || (this.#epoch !== undefined && history.epoch < this.#epoch)) return false;
    }
    const next = applyNotification(this.#model, notification, this.#now());
    if (next === this.#model) {
      if (notification.method !== "overlay/end") return false;
      this.#recoveryNeeded = true;
      this.#publish();
      return true;
    }
    this.#model = next;
    this.#boundary++;
    const rows = this.#notificationRows(notification);
    const history =
      notification.method === "history/updated" ? (notification.params as { snapshot: { length: number } }) : undefined;
    const turns = this.#notificationTurns(notification);
    const hasRecordedInput = rows.length > 0 || turns.length > 0;
    const coverage =
      history && hasRecordedInput ? this.#contiguousSuffix(rows, turns, history.snapshot.length) : undefined;
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
    let acceptedBoundary = this.#boundary;
    let latest = this.#model;
    const unlisten = this.subscribe(() => {
      latest = this.#model;
      acceptedBoundary = this.#boundary;
    });
    return {
      getSnapshot: () => latest,
      openLocal: async (limit) => {
        const openingBoundary = acceptedBoundary;
        if (!active || !this.#acceptsLifetime(lifetime)) return { status: "recovery-needed" };
        try {
          const coverageBoundary = this.#coverageBoundary;
          const window = await this.#cache.getLatestWindow(this.#binding.historyKey, limit, coverageBoundary);
          if (!active || !this.#acceptsLifetime(lifetime)) return { status: "recovery-needed" };
          if (!coverageBoundary && window.boundary) this.#coverageBoundary = window.boundary;
          const current = this.#model;
          if (!current) return { status: window.complete ? "stale" : "recovery-needed" };
          const restored = await this.#restoreWindow(current, window.rows);
          if (!active || !this.#acceptsLifetime(lifetime)) return { status: "recovery-needed" };
          // A racing accepted model wins by item version; the stale query only fills absent slots.
          latest = this.#boundary > openingBoundary ? this.#mergeReaderModels(latest ?? current, restored) : restored;
          this.#model = latest;
          this.#publish();
          const status =
            window.complete && !window.stale && !this.#cacheContinuityLost && !this.#recoveryNeeded
              ? "current"
              : "recovery-needed";
          return {
            model: latest ?? current,
            status,
            boundary: this.#boundary,
            olderCursor: current.olderCursor,
          };
        } catch {
          return { model: latest, status: "recovery-needed", boundary: this.#boundary };
        }
      },
      dispose: () => {
        if (!active) return;
        active = false;
        this.#readerCount--;
        unlisten();
        latest = undefined;
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

  #rowsFromRead(response: ThreadReadResponse): TranscriptItemRow<CachedSessionItem>[] {
    const model = this.#model;
    return this.#readItems(response).flatMap((wire) => {
      const item = model?.turns
        .flatMap((turn) => turn.items)
        .find((candidate) => candidate.transcriptKey === wire.transcriptKey || candidate.id === wire.id);
      return item
        ? [this.#row(item)].filter((row): row is TranscriptItemRow<CachedSessionItem> => row !== undefined)
        : [];
    });
  }

  #rowsFromWindow(response: ThreadReadResponse): TranscriptItemRow<CachedSessionItem>[] {
    const model = this.#model;
    const items = (response.thread.turns ?? []).flatMap((turn) =>
      (turn.items ?? []).map((item) => ({ ...item, turnId: item.turnId ?? turn.id })),
    );
    return items.flatMap((wire) => {
      const item = model?.turns
        .flatMap((turn) => turn.items)
        .find((candidate) => candidate.transcriptKey === wire.transcriptKey || candidate.id === wire.id);
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
      const item = this.#model?.turns
        .flatMap((turn) => turn.items)
        .find((candidate) => candidate.transcriptKey === key || candidate.id === input.id);
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
      representedSnapshot !== this.#representedSnapshot + 1 ||
      (rows.length === 0 && turns.length === 0)
    ) {
      return undefined;
    }
    const positions = rows.map((row) => row.position).sort(comparePosition);
    const previousEnd = this.#coverageEnd;
    if (previousEnd && positions.some((position) => comparePosition(position, previousEnd) <= 0)) {
      return undefined;
    }
    return {
      kind: "suffix",
      readId: this.#coverageReadId,
      continuityId: this.#continuityId,
      previousBoundary: this.#coverageBoundary,
      boundary: `${this.#coverageBoundary}:suffix:${representedSnapshot}`,
      start: this.#coverageStart,
      end: positions.at(-1) ?? this.#coverageEnd,
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

  async #restoreWindow(seed: ThreadModel, rows: readonly TranscriptItemRow<unknown>[]): Promise<ThreadModel> {
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

  #queueWrite(batch: Parameters<SessionEntryCache["commitBatch"]>[0]): void {
    const write = this.#cache.commitBatch(batch).then((outcome) => {
      if (outcome.outcome === "committed") {
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
