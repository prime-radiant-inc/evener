import { canonicalJson, normalizeConfig } from "@evener/appwire-client";
import type {
	KeybindingDraftCheckpoint,
	KeybindingDraftStorage,
	TranscriptDisplayConfigV1,
	TranscriptDraftCheckpoint,
	TranscriptDraftStorage,
} from "@evener/appwire-client";

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** A stored record's bytes that decode (JSON.parse succeeds) to the JSON
 * value `null` - a PRESENT record, distinct from no record at all (the
 * DraftPort contract already treats a bare JS `null` as "no record" - see
 * draftCheckpointPort.ts). Tagged rather than returned as the bare string
 * "null": a stored JSON STRING "null" (the six bytes `"null"`) parses to
 * that identical primitive, so a bare-string sentinel here would make the
 * two indistinguishable - exactly the collision that let a present, invalid
 * transcript checkpoint (the string case) read as no draft at all (the null
 * case). Branded with a symbol rather than checked by string-tagged shape:
 * JSON.parse can never produce a symbol-keyed property, so no legitimately
 * stored (successfully parsed) value can ever satisfy this predicate by
 * coincidence. A shape/string tag does not have that guarantee: stored bytes
 * that happen to be the JSON text of a previously-seen marker would parse
 * right back into something indistinguishable from it. */
const STORED_NULL_BRAND: unique symbol = Symbol("evener.nativePreferenceDrafts.storedNull");
export interface StoredNullRecord {
	readonly [STORED_NULL_BRAND]: true;
}
export const STORED_NULL_RECORD: StoredNullRecord = { [STORED_NULL_BRAND]: true };
export function isStoredNullRecord(value: unknown): value is StoredNullRecord {
	return (
		typeof value === "object" &&
		value !== null &&
		(value as { [STORED_NULL_BRAND]?: unknown })[STORED_NULL_BRAND] === true
	);
}

/** A stored record's bytes that JSON.parse could not decode at all. Tagged
 * with the original bytes rather than returned bare: a bare string could
 * coincidentally equal a legitimately decoded value (a checkpoint field, or
 * STORED_NULL_RECORD's own would-be sentinel), which is the same class of
 * collision StoredNullRecord exists to close - see its own comment on the
 * symbol brand. */
const UNPARSEABLE_BRAND: unique symbol = Symbol("evener.nativePreferenceDrafts.unparseable");
export interface UnparseableDraftBytes {
	readonly [UNPARSEABLE_BRAND]: true;
	readonly raw: string;
}
export function isUnparseableDraftBytes(value: unknown): value is UnparseableDraftBytes {
	return (
		typeof value === "object" &&
		value !== null &&
		(value as { [UNPARSEABLE_BRAND]?: unknown })[UNPARSEABLE_BRAND] === true &&
		typeof (value as { raw?: unknown }).raw === "string"
	);
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
		return { [UNPARSEABLE_BRAND]: true, raw } satisfies UnparseableDraftBytes;
	}
	return parsed === null ? STORED_NULL_RECORD : parsed;
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

/** Whether two in-memory draft values name the same record - the shared
 * marker-aware identity a compare-and-swap must run: a marker
 * (StoredNullRecord, UnparseableDraftBytes) compares by its own tag rather
 * than the ordinary shape its unique-symbol brand leaves in canonicalJson
 * (an empty object, a raw-only object), and everything else compares
 * canonically, key order normalized. The Storage-backed backend runs this
 * same comparison through stored bytes (matchesStoredBytes re-derives the
 * identity key for both sides); an in-memory test double compares its stored
 * values through this predicate directly. */
export function sameDraftIdentity(stored: unknown, expected: unknown): boolean {
	return draftIdentityKey(stored) === draftIdentityKey(expected);
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
	/** Inserts `checkpoint` at `key` only if nothing is stored there; reports
	 * whether it did - the atomic twin of replaceIf for a record that does
	 * not exist yet (see DraftPort.insertIfAbsent). */
	insertIfAbsent(key: string, checkpoint: unknown): boolean;
	/** Replaces the record at `key` with `next` only if `expected` still names
	 * it; reports whether it did (see DraftPort.replaceIf). */
	replaceIf(key: string, expected: unknown, next: unknown): boolean;
}

/** The keybindings port's own narrower view of the shared backend: its
 * checkpoint shape is known, so its compare-and-swap signatures name it.
 * Both native ports drive the shared DraftPort contract now - the transcript
 * projection settles atomically through the same store machinery - so this
 * interface only narrows the base, it does not add a method the transcript
 * port lacks. */
export interface NativeKeybindingDraftBackend extends NativePreferenceDraftBackend {
	/** Inserts `checkpoint` at `key` only if nothing is stored there; reports
	 * whether it did. The atomic twin of replaceIf for a record that does not
	 * exist yet - see DraftPort.insertIfAbsent's own comment. */
	insertIfAbsent(key: string, checkpoint: KeybindingDraftCheckpoint): boolean;
	replaceIf(
		key: string,
		expected: unknown,
		next: KeybindingDraftCheckpoint,
	): boolean;
}

/** The raw string-keyed storage a backend's own get/set/deleteIf/replaceIf
 * run on - Storage.getItemSync et al. in production and a Map-backed fake in
 * tests. */
export interface RawStringStorage {
	getItemSync(key: string): string | null;
	setItemSync(key: string, value: string): void;
	removeItemSync(key: string): void;
}

/** One native keybinding backend over any raw string storage. Keeping parsing
 * and identity comparison here lets tests exercise the same code production
 * uses, including malformed bytes, stored JSON null, and key-order changes. */
export function rawStringDraftBackend(storage: RawStringStorage, createId: () => string): NativeKeybindingDraftBackend {
	function matches(key: string, value: unknown): boolean {
		return matchesStoredBytes(storage.getItemSync(key), value);
	}
	return {
		createId,
		get(key: string): unknown {
			const value = storage.getItemSync(key);
			return value === null ? null : parseDraftBytes(value);
		},
		set(key: string, value: unknown) {
			storage.setItemSync(key, JSON.stringify(value));
		},
		insertIfAbsent(key: string, value: unknown): boolean {
			if (storage.getItemSync(key) !== null) return false;
			storage.setItemSync(key, JSON.stringify(value));
			return true;
		},
		delete(key: string) {
			storage.removeItemSync(key);
		},
		deleteIf(key: string, value: unknown): boolean {
			if (!matches(key, value)) return false;
			storage.removeItemSync(key);
			return true;
		},
		replaceIf(key: string, expected: unknown, next: unknown): boolean {
			if (!matches(key, expected)) return false;
			storage.setItemSync(key, JSON.stringify(next));
			return true;
		},
	};
}

/** What a draft read named, once decoded: no record, a record this build can
 * read, or a record present but unreadable. Absent and unreadable used to be
 * told apart by a single boolean (readable or not), which cannot distinguish
 * "nothing is there" from "something unreadable replaced it" - the store-free
 * discard action needs that distinction (see draftUnreadableAfterDiscard). */
export type DraftReadOutcome = "absent" | "readable" | "unreadable";

export function classifyDraftRead(
	loaded: unknown,
	isReadable: (value: unknown) => boolean,
): DraftReadOutcome {
	if (loaded === null || loaded === undefined) return "absent";
	return isReadable(loaded) ? "readable" : "unreadable";
}

/** Reads a draft port ONCE and reports both its classification and the raw
 * value load() returned, so a caller that also needs to decode a readable
 * record never re-reads the port outside this same guard. Every DraftPort
 * method may throw a genuine storage failure; that says nothing about what
 * is stored, so `value` is undefined for that outcome. */
export function readDraftOutcomeWithValue(
	storage: Pick<KeybindingDraftStorage, "load">,
	isReadable: (value: unknown) => boolean,
): { outcome: DraftReadOutcome | "storageUnavailable"; value: unknown } {
	try {
		const value = storage.load();
		return { outcome: classifyDraftRead(value, isReadable), value };
	} catch {
		return { outcome: "storageUnavailable", value: undefined };
	}
}

/** Reads a draft port and classifies the outcome the way a cold-offline probe
 * or a store-free discard's re-read must. The value-returning helper owns the
 * try/catch so callers that need both pieces cannot accidentally read twice. */
export function readDraftOutcome(
	storage: Pick<KeybindingDraftStorage, "load">,
	isReadable: (value: unknown) => boolean,
): DraftReadOutcome | "storageUnavailable" {
	return readDraftOutcomeWithValue(storage, isReadable).outcome;
}

/** Whether the store-free discard action should still present the record as
 * unreadable, given the CURRENT record a follow-up read just found -
 * `outcome` (discardStoredKeybindingDraft's own result) plays no part: a
 * "removed" or "absent" outcome does not mean the record is gone NOW, since
 * a concurrent writer can insert a fresh unreadable record between
 * draftCheckpointPort's own re-read and this caller's - only the follow-up
 * read's own classification of what is there right now can say that. */
export function draftUnreadableAfterDiscard(current: DraftReadOutcome): boolean {
	return current === "unreadable";
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
		insertIfAbsent: (checkpoint) => backend.insertIfAbsent(key, checkpoint),
		removeIf: (checkpoint) => backend.deleteIf(key, checkpoint),
		replaceIf: (expected, next) => backend.replaceIf(key, expected, next),
	};
}

/** A transcript checkpoint the previous native implementation wrote - the
 * hub's mobile-only draft, stored without the shared store's `layout` field.
 * The shared decoder rejects a record without a layout as unreadable, so a
 * legacy record is adopted under its known mobile layout instead of stranding
 * a saved draft on upgrade. Returns the migrated checkpoint, or null when
 * `value` is not a readable legacy checkpoint (a record that already carries
 * a layout, an unreadable marker, or anything else the old shape did not
 * admit). */
function migrateLegacyTranscriptCheckpoint(
	value: unknown,
): TranscriptDraftCheckpoint | null {
	if (!isRecord(value) || "layout" in value) return null;
	if (typeof value.id !== "string" || value.id.length === 0) return null;
	if (
		!Number.isSafeInteger(value.baseRevision) ||
		(value.baseRevision as number) < 0 ||
		typeof value.writeUncertain !== "boolean"
	)
		return null;
	let config: TranscriptDisplayConfigV1;
	try {
		config = normalizeConfig(value.config as TranscriptDisplayConfigV1);
	} catch {
		return null;
	}
	return {
		id: value.id,
		layout: "mobile",
		baseRevision: value.baseRevision as number,
		config,
		writeUncertain: value.writeUncertain,
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
		// The shared store classifies whatever these bytes decode to: a valid
		// checkpoint is read as one, and a present-but-unreadable record (a
		// stored JSON null, unparseable bytes, or a checkpoint a build before
		// layouts did not write) is tagged by parseDraftBytes and surfaces as
		// draftUnreadable rather than silently reading as no draft - the same
		// contract nativeKeybindingDrafts runs, and the one discardDraft's
		// recovery needs.
		load: () => {
			const value = backend.get(key) ?? null;
			const migrated = migrateLegacyTranscriptCheckpoint(value);
			if (migrated === null) return value;
			// Adopt the record under its known layout by compare-and-swap, so the
			// bytes the shared store classifies are the bytes its later
			// removeIf/replaceIf compare against. A refusal means another writer
			// replaced it meanwhile: report what is actually there now instead of
			// the migration. The adoption is BEST-EFFORT: a write failure
			// (quota, denied storage) must not turn into a load() throw, which
			// the shared store would map to storageUnavailable with no draft -
			// hiding the readable legacy checkpoint this call just decoded.
			try {
				return backend.replaceIf(key, value, migrated)
					? migrated
					: (backend.get(key) ?? null);
			} catch {
				return migrated;
			}
		},
		save: (checkpoint) => backend.set(key, checkpoint),
		insertIfAbsent: (checkpoint) => backend.insertIfAbsent(key, checkpoint),
		removeIf: (checkpoint) => backend.deleteIf(key, checkpoint),
		replaceIf: (expected, next) => backend.replaceIf(key, expected, next),
	};
}
