// The detail level a client remembers for each individual session: an LRU of
// [sessionRef, level] choices kept under one storage key. A choice replaces
// what a session shows and keeps how it is annotated (configAtLevel), so the
// hub's advanced settings still own timings, costs and system events.

import {
  CONTENT_LEVELS,
  type ContentLevel,
  makeTranscriptDisplayConfig,
  type TranscriptDisplayConfigV1,
} from "./transcriptDisplayConfig";

/** The synchronous string storage a client keeps its choices in. */
export interface DetailLevelStorage {
  getItemSync(key: string): string | null;
  setItemSync(key: string, value: string): void;
}

const LIMIT = 500;

function read(storage: DetailLevelStorage, storageKey: string): [string, ContentLevel][] {
  try {
    const raw = storage.getItemSync(storageKey);
    const value: unknown = raw ? JSON.parse(raw) : [];
    if (!Array.isArray(value)) return [];
    return value.filter(
      (entry): entry is [string, ContentLevel] =>
        Array.isArray(entry) &&
        entry.length === 2 &&
        typeof entry[0] === "string" &&
        typeof entry[1] === "string" &&
        (CONTENT_LEVELS as readonly string[]).includes(entry[1]),
    );
  } catch {
    // Nothing readable: nothing chosen yet.
    return [];
  }
}

/** The detail level chosen for individual sessions, newest first, under one storage key. */
export class SessionDetailLevels {
  private entries: [string, ContentLevel][];
  private revision = 0;
  private listeners = new Set<() => void>();

  constructor(
    private readonly storage: DetailLevelStorage,
    private readonly storageKey: string,
  ) {
    this.entries = read(storage, storageKey);
  }

  get(ref: string): ContentLevel | null {
    return this.entries.find(([entry]) => entry === ref)?.[1] ?? null;
  }

  set(ref: string, level: ContentLevel): void {
    const next: [string, ContentLevel][] = [[ref, level], ...this.entries.filter(([entry]) => entry !== ref)];
    this.entries = next.slice(0, LIMIT);
    this.commit();
  }

  clear(ref: string): void {
    if (!this.entries.some(([entry]) => entry === ref)) return;
    this.entries = this.entries.filter(([entry]) => entry !== ref);
    this.commit();
  }

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  getRevision = (): number => this.revision;

  private commit(): void {
    try {
      this.storage.setItemSync(this.storageKey, JSON.stringify(this.entries));
    } catch {
      // The in-memory copy still serves this launch.
    }
    this.revision += 1;
    for (const listener of [...this.listeners]) listener();
  }
}

/** A config showing `level`'s content with `base`'s advanced settings: a session's level replaces what is shown and keeps how it is annotated. */
export function configAtLevel(level: ContentLevel, base: TranscriptDisplayConfigV1): TranscriptDisplayConfigV1 {
  return makeTranscriptDisplayConfig({ kind: "preset", level }, base.advanced);
}
