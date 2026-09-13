import type {
	KeybindingDraftCheckpoint,
	KeybindingDraftStorage,
} from "./keybindingDraftRepository";
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
	): void;
}

export function nativeKeybindingDrafts(
	hubId: string,
	backend: NativePreferenceDraftBackend,
): KeybindingDraftStorage {
	if (!hubId.trim())
		throw new Error("A hub id is required for preference drafts.");
	const key = `evener.native.keybinding-draft.${hubId}`;
	return {
		createId: () => backend.createId(),
		load: () => backend.get(key) ?? null,
		save: (checkpoint) => backend.set(key, checkpoint),
		remove: () => backend.delete(key),
		removeIf: (checkpoint) => backend.deleteIf(key, checkpoint),
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
