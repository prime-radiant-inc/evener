// A shared in-memory NativeKeybindingDraftBackend fake for every test that
// needs one: a Map-backed store compared by JSON bytes on deleteIf/replaceIf,
// the same comparison the real Storage-backed backend
// (NativePreferencesProvider.tsx) runs on stringified values.
// NativeKeybindingDraftBackend is a superset of NativePreferenceDraftBackend
// (the transcript draft port's narrower backend shape), so this fake serves
// both. In-repo test support, not shipped.

import type { NativeKeybindingDraftBackend } from "./nativePreferenceDrafts";

export interface FakeDraftBackend extends NativeKeybindingDraftBackend {
	store: Map<string, unknown>;
}

export function fakeDraftBackend(): FakeDraftBackend {
	const store = new Map<string, unknown>();
	let id = 0;
	return {
		store,
		createId: () => `draft-${++id}`,
		get: (key) => store.get(key) ?? null,
		set: (key, value) => {
			store.set(key, structuredClone(value));
		},
		delete: (key) => {
			store.delete(key);
		},
		deleteIf: (key, value) => {
			if (JSON.stringify(store.get(key)) !== JSON.stringify(value)) return false;
			store.delete(key);
			return true;
		},
		replaceIf: (key, expected, next) => {
			if (JSON.stringify(store.get(key)) !== JSON.stringify(expected)) return false;
			store.set(key, structuredClone(next));
			return true;
		},
	};
}
