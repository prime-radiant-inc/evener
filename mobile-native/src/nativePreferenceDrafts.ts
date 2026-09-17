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

/** Either section's storage, by name, for a caller that only knows which
 * section it is discarding for and has no reason to pick between
 * nativeTranscriptDrafts/nativeKeybindingDrafts itself. */
export function nativePreferenceDrafts(
	section: "transcript" | "keybindings",
	hubId: string,
	backend: NativePreferenceDraftBackend,
): KeybindingDraftStorage & TranscriptDraftStorage {
	return section === "transcript"
		? nativeDraftStorage("transcript", hubId, backend)
		: nativeDraftStorage("keybinding", hubId, backend);
}

/** The shape either section's storage has, over its own checkpoint type. */
interface SectionDraftStorage<Checkpoint> {
	createId(): string;
	load(): unknown;
	save(checkpoint: Checkpoint): void;
	removeIf(checkpoint: Checkpoint): void;
}

export type RetainingDraftStorage<Checkpoint> = SectionDraftStorage<Checkpoint> & {
	/** Removes exactly the record the most recent load() call returned -
	 * never a fresh reload, which would name (and remove) whatever is stored
	 * NOW. A model's own repository (createDraftRepository) retains this same
	 * identity too, but only for as long as that model instance lives; this
	 * wrapper is what the provider hands the model AND keeps for itself, so
	 * the identity a model's load() classified survives that model's disposal
	 * (a client dropped, a hub reconnect) for the no-model discard path to
	 * use. A no-op before anything has ever been loaded through it. */
	discardLastLoaded(): void;
};

/** Wraps a section's storage so the identity of whatever load() most
 * recently returned outlives any one caller - in particular, a model
 * instance built over this same storage and later disposed. See
 * RetainingDraftStorage. */
export function retainingDraftStorage<Checkpoint>(
	storage: SectionDraftStorage<Checkpoint>,
): RetainingDraftStorage<Checkpoint> {
	let lastLoaded: unknown;
	let hasLastLoaded = false;
	return {
		createId: () => storage.createId(),
		load: () => {
			const value = storage.load();
			hasLastLoaded = value !== null && value !== undefined;
			lastLoaded = value;
			return value;
		},
		save: (checkpoint) => storage.save(checkpoint),
		removeIf: (checkpoint) => storage.removeIf(checkpoint),
		discardLastLoaded: () => {
			if (!hasLastLoaded) return;
			storage.removeIf(lastLoaded as Checkpoint);
		},
	};
}
