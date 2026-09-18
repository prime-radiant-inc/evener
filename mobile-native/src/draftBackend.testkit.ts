// A shared in-memory NativeKeybindingDraftBackend fake for every test that
// needs one: a Map-backed store compared canonically (key order normalized)
// on deleteIf/replaceIf, the same comparison the real Storage-backed backend
// (NativePreferencesProvider.tsx) runs on stringified values via
// matchesStoredBytes/canonicalJson.
// NativeKeybindingDraftBackend is a superset of NativePreferenceDraftBackend
// (the transcript draft port's narrower backend shape), so this fake serves
// both. In-repo test support, not shipped.

import { canonicalJson } from "@evener/appwire-client";
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
		get: (key) => {
			const value = store.get(key);
			return value === undefined ? null : structuredClone(value);
		},
		set: (key, value) => {
			store.set(key, structuredClone(value));
		},
		insertIfAbsent: (key, value) => {
			if (store.has(key)) return false;
			store.set(key, structuredClone(value));
			return true;
		},
		delete: (key) => {
			store.delete(key);
		},
		deleteIf: (key, value) => {
			// A missing key is never a match, whatever identity is named - a
			// canonical encoding of `undefined` would otherwise collide with an
			// `undefined` identity's own encoding.
			if (!store.has(key) || canonicalJson(store.get(key)) !== canonicalJson(value)) return false;
			store.delete(key);
			return true;
		},
		replaceIf: (key, expected, next) => {
			if (!store.has(key) || canonicalJson(store.get(key)) !== canonicalJson(expected)) return false;
			store.set(key, structuredClone(next));
			return true;
		},
	};
}
