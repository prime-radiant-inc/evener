// A shared in-memory NativeKeybindingDraftBackend fake for every test that
// needs one: a Map-backed store whose reads and writes clone the stored value
// (so a caller mutating what it read cannot corrupt the store) and whose
// deleteIf/replaceIf compare through the shared marker-aware identity helper
// (sameDraftIdentity) - the same comparison the real Storage-backed backend
// (NativePreferencesProvider.tsx) runs on stringified values via
// matchesStoredBytes: an unreadable-record marker (StoredNullRecord,
// UnparseableDraftBytes) matches by its own tag, never by the ordinary shape
// its unique-symbol brand leaves in canonicalJson.
// NativeKeybindingDraftBackend is a superset of NativePreferenceDraftBackend
// (the transcript draft port's narrower backend shape), so this fake serves
// both. In-repo test support, not shipped.

import {
	isStoredNullRecord,
	isUnparseableDraftBytes,
	parseDraftBytes,
	sameDraftIdentity,
} from "./nativePreferenceDrafts";
import type { NativeKeybindingDraftBackend } from "./nativePreferenceDrafts";

export interface FakeDraftBackend extends NativeKeybindingDraftBackend {
	store: Map<string, unknown>;
}

/** A clone that keeps the unreadable-record markers: their brands are unique
 * symbols, invisible to structuredClone (and to canonicalJson), so a plain
 * clone would reduce a stored-null marker to an ordinary empty object or an
 * unparseable marker to its raw-only shape - losing the marker identity the
 * real backend keeps by re-parsing the stored bytes on every get()
 * (parseDraftBytes re-derives the same marker from the same bytes). */
function markerAwareClone(value: unknown): unknown {
	if (isStoredNullRecord(value)) return parseDraftBytes("null");
	if (isUnparseableDraftBytes(value)) return parseDraftBytes(value.raw);
	return structuredClone(value);
}

export function fakeDraftBackend(): FakeDraftBackend {
	const store = new Map<string, unknown>();
	let id = 0;
	return {
		store,
		createId: () => `draft-${++id}`,
		get: (key) => {
			const value = store.get(key);
			return value === undefined ? null : markerAwareClone(value);
		},
		set: (key, value) => {
			store.set(key, markerAwareClone(value));
		},
		insertIfAbsent: (key, value) => {
			if (store.has(key)) return false;
			store.set(key, markerAwareClone(value));
			return true;
		},
		delete: (key) => {
			store.delete(key);
		},
		deleteIf: (key, value) => {
			// A missing key is never a match, whatever identity is named - the
			// identity helper's canonical encoding of `undefined` would
			// otherwise collide with an `undefined` identity's own encoding.
			if (!store.has(key) || !sameDraftIdentity(store.get(key), value)) return false;
			store.delete(key);
			return true;
		},
		replaceIf: (key, expected, next) => {
			if (!store.has(key) || !sameDraftIdentity(store.get(key), expected)) return false;
			store.set(key, markerAwareClone(next));
			return true;
		},
	};
}
