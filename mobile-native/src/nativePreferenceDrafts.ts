import { canonicalJson, isPlainObject, normalizeConfig } from "@evener/appwire-client";
import type {
	KeybindingsRule,
	TranscriptDisplayConfigV1,
	TranscriptDraftCheckpoint,
	TranscriptDraftStorage,
} from "@evener/appwire-client";
import type { NavigationActionBackend } from "./navigationActionRepository";
import type { SyncStringStorage } from "./syncStringStorage";

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

export interface NativePreferenceDraftBackend extends NavigationActionBackend {
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

/** One native draft backend over any raw string storage. Keeping parsing
 * and identity comparison here lets tests exercise the same code production
 * uses, including malformed bytes, stored JSON null, and key-order changes. */
export function rawStringDraftBackend(
	storage: SyncStringStorage,
	createId: () => string,
): NativePreferenceDraftBackend {
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

/** The checkpoint a build before the one-immediate-write ruling left under
 * `evener.native.keybinding-draft.<hubId>`. It is no longer written - the
 * immediate PATCH is the only new-write path - but every persisted format
 * stays readable (docs/product/README.md), so an upgrade surfaces the saved
 * proposal for the user to apply or discard. Applying never happens on its
 * own: a record that was never confirmed, or was composed against an older
 * hub revision, must not silently replace the hub's current shortcuts. */
export interface LegacyKeybindingDraftCheckpoint {
	id: string;
	baseRevision: number;
	rules: KeybindingsRule[];
	writeUncertain: boolean;
}

export type LegacyKeybindingDraft =
	| { outcome: "absent" }
	| { outcome: "readable"; checkpoint: LegacyKeybindingDraftCheckpoint; raw: unknown }
	| { outcome: "unreadable"; raw: unknown }
	| { outcome: "storageUnavailable" };

export type LegacyKeybindingDiscardResult = "removed" | "absent" | "refused" | "storageUnavailable";

export interface LegacyKeybindingDraftStorage {
	/** Reads once and classifies the record; never throws. */
	read(): LegacyKeybindingDraft;
	/** Removes the exact record this store last read, refusing a record a
	 * concurrent writer has replaced. Never throws. */
	discard(): LegacyKeybindingDiscardResult;
}

function legacyKeybindingRules(value: unknown): KeybindingsRule[] | null {
	if (!Array.isArray(value)) return null;
	const rules: KeybindingsRule[] = [];
	for (const item of value) {
		if (!isPlainObject(item)) return null;
		if (typeof item.action !== "string" || item.action.length === 0) return null;
		if (!(item.chord === null || (typeof item.chord === "string" && item.chord.length > 0))) return null;
		rules.push({ action: item.action, chord: item.chord });
	}
	return rules;
}

function decodeLegacyKeybindingDraft(value: unknown): LegacyKeybindingDraftCheckpoint | null {
	if (!isPlainObject(value)) return null;
	if (typeof value.id !== "string" || value.id.length === 0) return null;
	if (!Number.isSafeInteger(value.baseRevision) || (value.baseRevision as number) < 0) return null;
	if (typeof value.writeUncertain !== "boolean") return null;
	const rules = legacyKeybindingRules(value.rules);
	if (rules === null) return null;
	return {
		id: value.id,
		baseRevision: value.baseRevision as number,
		rules,
		writeUncertain: value.writeUncertain,
	};
}

export function nativeLegacyKeybindingDraft(
	hubId: string,
	backend: NativePreferenceDraftBackend,
): LegacyKeybindingDraftStorage {
	if (!hubId.trim()) throw new Error("A hub id is required for preference drafts.");
	const key = `evener.native.keybinding-draft.${hubId}`;
	/** The record the caller last saw. discard() removes THIS identity, so a
	 * record a concurrent writer replaced after the read is refused rather
	 * than deleted unseen. */
	let lastRead: LegacyKeybindingDraft | null = null;
	function read(): LegacyKeybindingDraft {
		try {
			const raw = backend.get(key) ?? null;
			if (raw === null) {
				lastRead = { outcome: "absent" };
				return lastRead;
			}
			const checkpoint = decodeLegacyKeybindingDraft(raw);
			lastRead = checkpoint === null ? { outcome: "unreadable", raw } : { outcome: "readable", checkpoint, raw };
			return lastRead;
		} catch {
			lastRead = { outcome: "storageUnavailable" };
			return lastRead;
		}
	}
	return {
		read,
		discard(): LegacyKeybindingDiscardResult {
			const current = lastRead ?? read();
			if (current.outcome === "absent") return "absent";
			if (current.outcome === "storageUnavailable") return "storageUnavailable";
			try {
				if (backend.deleteIf(key, current.raw)) return "removed";
			} catch {
				return "storageUnavailable";
			}
			const after = read();
			if (after.outcome === "absent") return "absent";
			if (after.outcome === "storageUnavailable") return "storageUnavailable";
			return "refused";
		},
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
function migrateLegacyTranscriptCheckpoint(value: unknown): TranscriptDraftCheckpoint | null {
	if (!isPlainObject(value) || "layout" in value) return null;
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

/** The phone's local transcript override for one hub: the presentation
 * preference a hub that predates thinkingOpenByDefault cannot store (the
 * compatibility table's "the client works with an older hub" direction). The
 * model retains it when a save is acknowledged with the older hub's
 * four-field reading and clears it once a hub acknowledges the request
 * exactly; refresh and reconnect re-read it. */
export interface NativeTranscriptLocalStorage {
	load(): TranscriptDisplayConfigV1 | null;
	save(config: TranscriptDisplayConfigV1 | null): void;
}

export function nativeTranscriptLocal(
	hubId: string,
	backend: NativePreferenceDraftBackend,
): NativeTranscriptLocalStorage {
	if (!hubId.trim()) throw new Error("A hub id is required for transcript overrides.");
	const key = `evener.native.transcript-local.${hubId}`;
	return {
		// Best effort: the override is a local convenience, so an unavailable
		// or unreadable backend reads as "no override" rather than failing the
		// model that owns the hub defaults.
		load: () => {
			try {
				const value = backend.get(key);
				if (value === null || value === undefined) return null;
				return normalizeConfig(value as TranscriptDisplayConfigV1);
			} catch {
				return null;
			}
		},
		save: (config) => {
			try {
				if (config === null) backend.delete(key);
				else backend.set(key, config);
			} catch {
				// The next save retries; the in-memory override still applies.
			}
		},
	};
}

export function nativeTranscriptDrafts(hubId: string, backend: NativePreferenceDraftBackend): TranscriptDraftStorage {
	if (!hubId.trim()) throw new Error("A hub id is required for preference drafts.");
	const key = `evener.native.transcript-draft.${hubId}`;
	/** The bytes a legacy checkpoint's migration could NOT rewrite, keyed by the
	 * migrated checkpoint the read returned. The shared repository classifies a
	 * read's value as the identity its later compare-and-swap names, so without
	 * this the migrated-in-memory checkpoint would be compared against bytes
	 * that still carry the legacy shape and every discard/save would refuse -
	 * stranding the draft the best-effort migration kept readable. */
	const legacyBytes = new WeakMap<object, unknown>();
	const storedIdentity = (identity: unknown): unknown =>
		typeof identity === "object" && identity !== null && legacyBytes.has(identity)
			? legacyBytes.get(identity)
			: identity;
	return {
		createId: () => backend.createId(),
		// The shared store classifies whatever these bytes decode to: a valid
		// checkpoint is read as one, and a present-but-unreadable record (a
		// stored JSON null, unparseable bytes, or a checkpoint a build before
		// layouts did not write) is tagged by parseDraftBytes and surfaces as
		// draftUnreadable rather than silently reading as no draft.
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
				return backend.replaceIf(key, value, migrated) ? migrated : (backend.get(key) ?? null);
			} catch {
				legacyBytes.set(migrated, value);
				return migrated;
			}
		},
		save: (checkpoint) => backend.set(key, checkpoint),
		insertIfAbsent: (checkpoint) => backend.insertIfAbsent(key, checkpoint),
		removeIf: (checkpoint) => backend.deleteIf(key, storedIdentity(checkpoint)),
		replaceIf: (expected, next) => backend.replaceIf(key, storedIdentity(expected), next),
	};
}
