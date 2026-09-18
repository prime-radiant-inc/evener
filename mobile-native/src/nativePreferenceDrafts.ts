import type {
	KeybindingDraftCheckpoint,
	KeybindingDraftStorage,
} from "@evener/appwire-client";
import type {
	TranscriptDraftCheckpoint,
	TranscriptDraftStorage,
} from "./preferenceDraftRepository";

/** Parses a draft record's stored bytes the way a backend's own get() must:
 * valid JSON decodes normally, and anything this build cannot use as a
 * record comes back as the RAW bytes instead of throwing, so the shared
 * store reads it as a present-but-unreadable record (draftUnreadable)
 * rather than a dead port. That includes a stored JSON `null` - the
 * DraftPort contract already treats a bare `null` as "no record" (see
 * draftCheckpointPort.ts), so handing one back unparsed keeps it a PRESENT
 * record distinct from that absent-record sentinel, and therefore
 * discardable through the unreadable-draft recovery path instead of
 * silently stuck. */
export function parseDraftBytes(raw: string): unknown {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return raw;
  }
  return parsed === null ? raw : parsed;
}

export interface NativePreferenceDraftBackend {
	createId(): string;
	get(key: string): unknown;
	set(key: string, value: unknown): void;
	delete(key: string): void;
	/** Removes the record at `key` only if it is still named by `identity`;
	 * reports whether it did. `identity` is usually a checkpoint this
	 * backend itself produced, but the unreadable-record recovery also hands
	 * it the RAW value get() returned - typed `unknown`, not either
	 * checkpoint shape, so a conforming backend never assumes it can decode
	 * what it is given. */
	deleteIf(key: string, identity: unknown): boolean;
}

/** The keybindings draft port settles atomically (a save that adopts a
 * checkpoint replaced under it): replaceIf is that port's own requirement,
 * not every NativePreferenceDraftBackend's - nativeTranscriptDrafts' backend
 * never calls it (the pre-migration transcript design never settles
 * atomically), so it stays on this narrower interface instead of widening
 * the shared one for a method only one consumer needs. */
export interface NativeKeybindingDraftBackend extends NativePreferenceDraftBackend {
	replaceIf(
		key: string,
		expected: unknown,
		next: KeybindingDraftCheckpoint,
	): boolean;
}

export function nativeKeybindingDrafts(
	hubId: string,
	backend: NativeKeybindingDraftBackend,
): KeybindingDraftStorage {
	if (!hubId.trim())
		throw new Error("A hub id is required for preference drafts.");
	const key = `evener.native.keybinding-draft.${hubId}`;
	return {
		createId: () => backend.createId(),
		load: () => backend.get(key) ?? null,
		save: (checkpoint) => backend.set(key, checkpoint),
		removeIf: (checkpoint) => backend.deleteIf(key, checkpoint),
		replaceIf: (expected, next) => backend.replaceIf(key, expected, next),
	};
}

export function nativeTranscriptDrafts(
	hubId: string,
	backend: NativePreferenceDraftBackend,
): TranscriptDraftStorage {
	if (!hubId.trim())
		throw new Error("A hub id is required for preference drafts.");
	const key = `evener.native.transcript-draft.${hubId}`;
	return {
		createId: () => backend.createId(),
		load: () => {
			const value = backend.get(key);
			// A stored JSON `null` comes back from the shared backend.get() as
			// the raw string "null" (parseDraftBytes preserves it so the
			// KEYBINDINGS port can classify it as a present-but-unreadable
			// record - see nativeKeybindingDrafts). The transcript port has no
			// unreadable-record recovery path, so it keeps treating a stored
			// null as no draft, the same as a plain JSON.parse always did.
			if (value === undefined || value === null || value === "null") return null;
			return value as TranscriptDraftCheckpoint;
		},
		save: (checkpoint) => backend.set(key, checkpoint),
		remove: () => backend.delete(key),
		removeIf: (checkpoint) => backend.deleteIf(key, checkpoint),
	};
}
