import {
  type CachedSessionItem,
  decodeTranscriptItem,
  encodeTranscriptItem,
  type ItemModel,
} from "@evener/appwire-client";
import type { TranscriptItemRow } from "./sessionCacheSchema";
import { SessionEntryCache } from "./sessionEntryCache";

export interface TranscriptOverlayValue {
  key: string;
  value: unknown;
}

export interface TranscriptReader {
  openLocal(limit: number): Promise<{ items: TranscriptItemRow[]; overlay: readonly TranscriptOverlayValue[] }>;
  dispose(): void;
}

/** Session-local publication and storage owner for recorded transcript rows. */
export class TranscriptRepository {
  readonly #cache: SessionEntryCache;
  readonly #historyKey: string;
  readonly #listeners = new Set<() => void>();
  readonly #overlay = new Map<string, TranscriptOverlayValue>();
  readonly #pending = new Map<string, TranscriptItemRow>();
  readonly #writes = new Set<Promise<void>>();
  #disposed = false;

  constructor(cache: SessionEntryCache, historyKey: string) {
    this.#cache = cache;
    this.#historyKey = historyKey;
  }

  subscribe(listener: () => void): () => void {
    this.#listeners.add(listener);
    return () => this.#listeners.delete(listener);
  }

  setOverlay(value: TranscriptOverlayValue): void {
    if (this.#disposed) return;
    this.#overlay.set(value.key, value);
    this.#publish();
  }

  acceptRecorded(row: TranscriptItemRow<ItemModel>): void {
    if (this.#disposed || row.historyKey !== this.#historyKey) return;
    const encoded: TranscriptItemRow<CachedSessionItem> = { ...row, item: encodeTranscriptItem(row.item) };
    const existing = this.#pending.get(row.transcriptKey);
    if (existing && existing.version > row.version) return;
    this.#pending.set(row.transcriptKey, encoded);
    const overlayKey = row.item.overlayKey;
    if (typeof overlayKey === "string") this.#overlay.delete(overlayKey);
    this.#publish();
    const write = this.#cache.putItems([encoded]);
    this.#writes.add(write);
    void write
      .then(
        () => {
          if (this.#pending.get(row.transcriptKey) === encoded) this.#pending.delete(row.transcriptKey);
        },
        () => {
          if (this.#pending.get(row.transcriptKey) === encoded) this.#pending.delete(row.transcriptKey);
        },
      )
      .finally(() => this.#writes.delete(write));
  }

  async settled(): Promise<void> {
    await Promise.all([...this.#writes]);
  }

  attachReader(): TranscriptReader {
    let active = true;
    const captured = new Map(this.#pending);
    const unlisten = this.subscribe(() => {
      for (const [key, row] of this.#pending) captured.set(key, row);
    });
    return {
      openLocal: async (limit) => {
        const diskRows = await this.#cache.getLatest(this.#historyKey, limit);
        if (!active) return { items: [], overlay: [] };
        const items = new Map(diskRows.map((row) => [row.transcriptKey, row]));
        for (const row of captured.values()) {
          const current = items.get(row.transcriptKey);
          if (!current || current.version <= row.version) items.set(row.transcriptKey, row);
        }
        return {
          items: [...items.values()]
            .flatMap((row) => {
              const item = decodeTranscriptItem(row.item);
              return item ? [{ ...row, item }] : [];
            })
            .sort((a, b) => a.position.entry - b.position.entry || a.position.item - b.position.item),
          overlay: [...this.#overlay.values()],
        };
      },
      dispose: () => {
        if (!active) return;
        active = false;
        unlisten();
        captured.clear();
      },
    };
  }

  dispose(): void {
    if (this.#disposed) return;
    this.#disposed = true;
    this.#listeners.clear();
    this.#pending.clear();
    this.#overlay.clear();
  }

  #publish(): void {
    for (const listener of this.#listeners) listener();
  }
}
