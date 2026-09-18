import type {
	DiscardStoredDraftResult,
	KeybindingDraftCheckpoint,
	KeybindingDraftStorage,
} from "@evener/appwire-client";
import type {
	TranscriptDraftCheckpoint,
	TranscriptDraftStorage,
} from "./preferenceDraftRepository";

/** A stored record's bytes that decode (JSON.parse succeeds) to the JSON
 * value `null` - a PRESENT record, distinct from no record at all (the
 * DraftPort contract already treats a bare JS `null` as "no record" - see
 * draftCheckpointPort.ts). Tagged rather than returned as the bare string
 * "null": a stored JSON STRING "null" (the six bytes `"null"`) parses to
 * that identical primitive, so a bare-string sentinel here would make the
 * two indistinguishable - exactly the collision that let a present, invalid
 * transcript checkpoint (the string case) read as no draft at all (the null
 * case). */
export interface StoredNullRecord {
	readonly kind: "storedNull";
}
export const STORED_NULL_RECORD: StoredNullRecord = { kind: "storedNull" };
export function isStoredNullRecord(value: unknown): value is StoredNullRecord {
	return typeof value === "object" && value !== null && (value as { kind?: unknown }).kind === "storedNull";
}

/** A stored record's bytes that JSON.parse could not decode at all. Tagged
 * with the original bytes rather than returned bare: a bare string could
 * coincidentally equal a legitimately decoded value (a checkpoint field, or
 * STORED_NULL_RECORD's own would-be sentinel), which is the same class of
 * collision StoredNullRecord exists to close. */
export interface UnparseableDraftBytes {
	readonly kind: "unparseable";
	readonly raw: string;
}
export function isUnparseableDraftBytes(value: unknown): value is UnparseableDraftBytes {
	return typeof value === "object" && value !== null && (value as { kind?: unknown }).kind === "unparseable";
}

/** Parses a draft record's stored bytes the way a backend's own get() must:
 * valid JSON decodes normally, and anything this build cannot use as a
 * record comes back as a tagged marker instead of throwing, so the shared
 * store reads it as a present-but-unreadable record (draftUnreadable)
 * rather than a dead port. */
export function parseDraftBytes(raw: string): unknown {
	let parsed: unknown;
	try {
		parsed = JSON.parse(raw);
	} catch {
		return { kind: "unparseable", raw } satisfies UnparseableDraftBytes;
	}
	return parsed === null ? STORED_NULL_RECORD : parsed;
}

/** A canonical string encoding of `value` with every object's keys sorted
 * recursively, so two structurally equal values compare equal regardless of
 * key order - a byte-for-byte JSON.stringify compare only tolerates
 * whitespace, not key order, which is not what "canonicalize" promises. */
function canonicalJson(value: unknown): string {
	if (Array.isArray(value)) return `[${value.map(canonicalJson).join(",")}]`;
	if (value !== null && typeof value === "object") {
		const record = value as Record<string, unknown>;
		return `{${Object.keys(record)
			.sort()
			.map((key) => `${JSON.stringify(key)}:${canonicalJson(record[key])}`)
			.join(",")}}`;
	}
	return JSON.stringify(value);
}

/** One comparison key for a draft identity, whether it is a real checkpoint,
 * a StoredNullRecord, or UnparseableDraftBytes - matchesStoredBytes derives
 * this for both the current stored bytes and the identity it is checking, so
 * the two marker types compare by their own tag instead of falling through
 * to a JSON compare that was never meant to describe them. */
function draftIdentityKey(value: unknown): string {
	if (isUnparseableDraftBytes(value)) return `raw:${value.raw}`;
	if (isStoredNullRecord(value)) return "storedNull";
	return `json:${canonicalJson(value)}`;
}

/** Whether `stored` (the exact bytes a backend's get() read, or null for no
 * key) names `value` - the compare deleteIf/replaceIf run before touching
 * storage. `value` is usually a checkpoint this build produced, but
 * discardStoredDraft() also hands it the RAW value get() returned for a
 * record no build can decode (a StoredNullRecord or UnparseableDraftBytes -
 * see parseDraftBytes). Re-deriving what get() would return for `stored`
 * right now and comparing identity keys handles a real checkpoint (key-order
 * and whitespace both normalized), either marker, and a mismatch between the
 * two kinds, all through the one path. */
export function matchesStoredBytes(stored: string | null, value: unknown): boolean {
	if (stored === null) return false;
	return draftIdentityKey(parseDraftBytes(stored)) === draftIdentityKey(value);
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

/** Whether the store-free discard action should still present the record as
 * unreadable once discardStoredKeybindingDraft's outcome is known.
 * "refused" carries two different situations behind one outcome: the record
 * now present decodes as valid (the reason "refused" exists at all - see
 * DiscardStoredDraftResult), or a concurrent writer replaced the unreadable
 * record with a DIFFERENT one that is still unreadable (draftCheckpointPort's
 * own re-read after a failed removeIf). Only the second keeps the recovery
 * notice up; the first, like "removed" and "absent", clears it. */
export function draftUnreadableAfterDiscard(
	outcome: DiscardStoredDraftResult,
	currentIsReadable: boolean,
): boolean {
	return outcome === "refused" && !currentIsReadable;
}

/** Whether a locally stored draft record is present but unreadable - what
 * the store-free discard action needs to know about BEFORE any live model
 * has connected. A cold offline start never reaches bindNativePreferences'
 * `ready` callback (no connection, so no model), so the live domain's own
 * draftUnreadable is never computed and the "Discard unreadable draft"
 * action would otherwise be unreachable until the hub answers. */
export function localDraftIsUnreadable(
	loaded: unknown,
	isReadable: (value: unknown) => boolean,
): boolean {
	return loaded !== null && loaded !== undefined && !isReadable(loaded);
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
			// STORED_NULL_RECORD (parseDraftBytes tags it so the KEYBINDINGS
			// port can classify it as a present-but-unreadable record - see
			// nativeKeybindingDrafts). The transcript port has no
			// unreadable-record recovery path, so it keeps treating a stored
			// null as no draft, the same as a plain JSON.parse always did - but
			// ONLY that exact case: a stored JSON STRING "null" (a present,
			// invalid checkpoint) is a different value and falls through to be
			// rejected below, not silently read as no draft.
			if (value === undefined || value === null || isStoredNullRecord(value)) return null;
			return value as TranscriptDraftCheckpoint;
		},
		save: (checkpoint) => backend.set(key, checkpoint),
		remove: () => backend.delete(key),
		removeIf: (checkpoint) => backend.deleteIf(key, checkpoint),
	};
}
