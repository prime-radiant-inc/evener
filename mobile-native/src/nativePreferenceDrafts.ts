import type {
	KeybindingDraftCheckpoint,
	KeybindingDraftStorage,
} from "@evener/appwire-client";
import type {
	TranscriptDraftCheckpoint,
	TranscriptDraftStorage,
} from "./preferenceDraftRepository";

export interface NativePreferenceDraftBackend {
	createId(): string;
	get(key: string): unknown;
	set(key: string, value: unknown): void;
	delete(key: string): void;
	deleteIf(
		key: string,
		checkpoint: TranscriptDraftCheckpoint | KeybindingDraftCheckpoint,
	): boolean;
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
		expected: KeybindingDraftCheckpoint,
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
			return value === undefined ? null : (value as TranscriptDraftCheckpoint);
		},
		save: (checkpoint) => backend.set(key, checkpoint),
		remove: () => backend.delete(key),
		removeIf: (checkpoint) => backend.deleteIf(key, checkpoint),
	};
}
