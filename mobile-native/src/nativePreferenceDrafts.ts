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
	/** Removes the stored record only if `checkpoint` still names it; reports
	 * whether it did. */
	deleteIf(key: string, checkpoint: NativePreferenceDraftCheckpoint): boolean;
	/** Replaces the stored record with `next` only if `expected` still names
	 * it; reports whether it did. The atomic twin of deleteIf. */
	replaceIf(
		key: string,
		expected: NativePreferenceDraftCheckpoint,
		next: NativePreferenceDraftCheckpoint,
	): boolean;
}

/** The shape either section's storage has, over its own checkpoint type. */
interface SectionDraftStorage<Checkpoint> {
	createId(): string;
	load(): unknown;
	save(checkpoint: Checkpoint): void;
	/** Removes the stored record only if `checkpoint` still names it; reports
	 * whether it did, so a refusal (the record it named is gone, replaced by
	 * something else) is never mistaken for success. */
	removeIf(checkpoint: Checkpoint): boolean;
	/** Replaces the stored record with `next` only if `expected` still names
	 * it; reports whether it did. */
	replaceIf(expected: Checkpoint, next: Checkpoint): boolean;
}

/** One hub's drafts for one preference section, under that section's key
 * prefix. The two sections' ports differ only in the checkpoint type they
 * declare, and the backend stores either, so one storage satisfies both. */
function nativeDraftStorage(
	prefix: string,
	hubId: string,
	backend: NativePreferenceDraftBackend,
): SectionDraftStorage<KeybindingDraftCheckpoint> & SectionDraftStorage<TranscriptDraftCheckpoint> {
	if (!hubId.trim())
		throw new Error("A hub id is required for preference drafts.");
	const key = `evener.native.${prefix}-draft.${hubId}`;
	return {
		createId: () => backend.createId(),
		load: () => backend.get(key) ?? null,
		save: (checkpoint) => backend.set(key, checkpoint),
		removeIf: (checkpoint) => backend.deleteIf(key, checkpoint),
		replaceIf: (expected, next) => backend.replaceIf(key, expected, next),
	};
}

export function nativeKeybindingDrafts(
	hubId: string,
	backend: NativePreferenceDraftBackend,
): SectionDraftStorage<KeybindingDraftCheckpoint> & KeybindingDraftStorage {
	return nativeDraftStorage("keybinding", hubId, backend);
}

export function nativeTranscriptDrafts(
	hubId: string,
	backend: NativePreferenceDraftBackend,
): SectionDraftStorage<TranscriptDraftCheckpoint> & TranscriptDraftStorage {
	return nativeDraftStorage("transcript", hubId, backend);
}

/** Either section's storage, by name, for a caller that only knows which
 * section it is discarding for and has no reason to pick between
 * nativeTranscriptDrafts/nativeKeybindingDrafts itself. */
export function nativePreferenceDrafts(
	section: "transcript" | "keybindings",
	hubId: string,
	backend: NativePreferenceDraftBackend,
): SectionDraftStorage<KeybindingDraftCheckpoint> &
	SectionDraftStorage<TranscriptDraftCheckpoint> &
	KeybindingDraftStorage &
	TranscriptDraftStorage {
	return section === "transcript"
		? nativeDraftStorage("transcript", hubId, backend)
		: nativeDraftStorage("keybinding", hubId, backend);
}

export type RetainingDraftStorage<Checkpoint> = SectionDraftStorage<Checkpoint> & {
	/** Removes exactly the record the most recent load() call returned -
	 * never a fresh reload, which would name (and remove) whatever is stored
	 * NOW. A model's own repository (createDraftRepository) retains this same
	 * identity too, but only for as long as that model instance lives; this
	 * wrapper is what the provider hands the model AND keeps for itself, so
	 * the identity a model's load() classified survives that model's disposal
	 * (a client dropped, a hub reconnect) for the no-model discard path to
	 * use. A no-op (reporting false) before anything has ever been loaded
	 * through it. Reports whether the removal actually happened: a refusal
	 * (the record it named is gone, replaced by something else) re-classifies
	 * against whatever is stored now - the same thing a live model's
	 * discardUnreadable+restoreDraft does - so a caller never projects
	 * "discarded" on a record that is still there. */
	discardLastLoaded(): boolean;
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
	function trackLoad(value: unknown): unknown {
		hasLastLoaded = value !== null && value !== undefined;
		lastLoaded = value;
		return value;
	}
	return {
		createId: () => storage.createId(),
		load: () => trackLoad(storage.load()),
		save: (checkpoint) => {
			storage.save(checkpoint);
			// What was just written IS now the classified record: a discard
			// right after an edit must name it, not the pre-edit bytes this
			// write replaced.
			trackLoad(checkpoint);
		},
		removeIf: (checkpoint) => storage.removeIf(checkpoint),
		replaceIf: (expected, next) => {
			const replaced = storage.replaceIf(expected, next);
			// What was just written IS now the classified record - the same
			// reason save() tracks its own writes (round 19/20): a no-model
			// discard right after a settle must name it, not the pre-settle
			// bytes it replaced.
			if (replaced) trackLoad(next);
			return replaced;
		},
		discardLastLoaded: () => {
			if (!hasLastLoaded) return false;
			const removed = storage.removeIf(lastLoaded as Checkpoint);
			// The record this wrapper named is gone, replaced by something else:
			// re-classify against whatever is actually there now, so a follow-up
			// discard targets it instead of repeatedly naming a record that no
			// longer exists.
			if (!removed) trackLoad(storage.load());
			return removed;
		},
	};
}
