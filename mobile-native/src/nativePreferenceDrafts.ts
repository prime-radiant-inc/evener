import type {
	KeybindingDraftCheckpoint,
	KeybindingDraftStorage,
	TranscriptDraftCheckpoint,
	TranscriptDraftStorage,
} from "@evener/appwire-client";

/** Either section's checkpoint, as the device backend stores them. */
export type NativePreferenceDraftCheckpoint =
	| TranscriptDraftCheckpoint
	| KeybindingDraftCheckpoint;

export interface NativePreferenceDraftBackend {
	createId(): string;
	get(key: string): unknown;
	set(key: string, value: unknown): void;
	delete(key: string): void;
	deleteIf(key: string, checkpoint: NativePreferenceDraftCheckpoint): void;
}

/** One hub's drafts for one preference section, under that section's key
 * prefix. The two sections' ports differ only in the checkpoint type they
 * declare, and the backend stores either, so one storage satisfies both. */
function nativeDraftStorage(
	prefix: string,
	hubId: string,
	backend: NativePreferenceDraftBackend,
): KeybindingDraftStorage & TranscriptDraftStorage {
	if (!hubId.trim())
		throw new Error("A hub id is required for preference drafts.");
	const key = `evener.native.${prefix}-draft.${hubId}`;
	return {
		createId: () => backend.createId(),
		load: () => backend.get(key) ?? null,
		save: (checkpoint) => backend.set(key, checkpoint),
		removeIf: (checkpoint) => backend.deleteIf(key, checkpoint),
	};
}

export function nativeKeybindingDrafts(
	hubId: string,
	backend: NativePreferenceDraftBackend,
): KeybindingDraftStorage {
	return nativeDraftStorage("keybinding", hubId, backend);
}

export function nativeTranscriptDrafts(
	hubId: string,
	backend: NativePreferenceDraftBackend,
): TranscriptDraftStorage {
	return nativeDraftStorage("transcript", hubId, backend);
}
