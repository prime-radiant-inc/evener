import { decodeCurrentTranscriptState, decodeTranscriptItem, type TurnModel } from "@evener/appwire-client";
import {
  entryHistoryKey,
  type TranscriptItemRow,
  type TranscriptRequestedRef,
  type TranscriptResolvedIdentity,
  type TranscriptSessionMetadata,
} from "./sessionCacheSchema";
import type { EntryMigrationOutcome, EntryMigrationSource, SessionEntryCache } from "./sessionEntryCache";

const ENTRY_FORMAT_VERSION = 3;

export interface MigrateLegacySessionOptions {
  // The resolved host/session binding the legacy record maps onto. A legacy
  // record does not carry a resolved identity, so the caller supplies it.
  identity: TranscriptResolvedIdentity;
  requestedRefs: readonly TranscriptRequestedRef[];
  savedAt: number;
  accessedAt?: number;
  source: EntryMigrationSource;
  clearEpoch: number;
  // Durable generation the caller read for this identity. Defaults to 0.
  historyGeneration?: number;
}

function isObject(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

interface LegacyTurn {
  id: string;
  version?: number;
  items?: unknown[];
  [key: string]: unknown;
}

// Recorded turns of either real legacy shape: the whole-session `records` blob
// (a top-level `history`) or the `current` checkpoint (a `state.history`). The
// streaming overlay is never read.
function legacyRecordedTurns(legacy: unknown): LegacyTurn[] | undefined {
  if (!isObject(legacy)) return undefined;
  if (isObject(legacy.history) && Array.isArray(legacy.history.turns)) {
    return legacy.history.turns as LegacyTurn[];
  }
  if (legacy.state !== undefined) {
    const state = decodeCurrentTranscriptState(legacy.state);
    if (state && Array.isArray(state.history.turns)) {
      return state.history.turns as unknown as LegacyTurn[];
    }
  }
  return undefined;
}

/**
 * Pure conversion of one stored legacy record into keyed entry rows. It emits
 * stale recorded seeds only: items and item-free turn metadata, never overlay,
 * currentness or coverage. Items without a usable key, position or version are
 * dropped rather than guessed. Returning undefined marks the record
 * undecodable, which leaves its legacy source untouched.
 */
export function legacySessionToEntryRows(
  legacy: unknown,
  historyKey: string,
): { items: TranscriptItemRow<unknown>[]; turns: TurnModel[] } | undefined {
  const turns = legacyRecordedTurns(legacy);
  if (!turns) return undefined;
  const items: TranscriptItemRow<unknown>[] = [];
  const turnModels: TurnModel[] = [];
  const seen = new Set<string>();
  for (const turn of turns) {
    if (!isObject(turn) || typeof turn.id !== "string" || typeof turn.status !== "string") return undefined;
    const { id: _id, items: _items, ...metadata } = turn;
    turnModels.push({ ...metadata, id: turn.id, items: [] } as unknown as TurnModel);
    for (const raw of Array.isArray(turn.items) ? turn.items : []) {
      const decoded = decodeTranscriptItem(raw);
      if (!decoded?.position || decoded.version === undefined) continue;
      const transcriptKey = decoded.transcriptKey ?? decoded.id;
      if (seen.has(transcriptKey)) continue;
      seen.add(transcriptKey);
      items.push({
        historyKey,
        transcriptKey,
        position: decoded.position,
        version: decoded.version,
        item: decoded,
      });
    }
  }
  return { items, turns: turnModels };
}

/**
 * Convert one legacy record through the entry cache. The conversion and the
 * removal of its source commit in a single transaction, so an interrupted,
 * aborted or Clear-refused attempt leaves the legacy record intact for retry.
 * No automatic migration is invoked here; a caller owns scheduling.
 */
export function migrateLegacySession(
  cache: SessionEntryCache,
  options: MigrateLegacySessionOptions,
): Promise<EntryMigrationOutcome> {
  const historyKey = entryHistoryKey(options.identity);
  const sessionMetadata: TranscriptSessionMetadata = {
    historyKey,
    requestedRefs: options.requestedRefs,
    resolvedIdentity: options.identity,
    savedAt: options.savedAt,
    accessedAt: options.accessedAt ?? options.savedAt,
    formatVersion: ENTRY_FORMAT_VERSION,
  };
  return cache.commitMigration({
    historyKey,
    clearEpoch: options.clearEpoch,
    historyGeneration: options.historyGeneration,
    source: options.source,
    sessionMetadata,
    convert: (legacy) => legacySessionToEntryRows(legacy, historyKey),
  });
}
