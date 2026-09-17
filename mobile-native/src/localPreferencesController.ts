import {
	type KeybindingDraftCheckpoint,
	type TranscriptDraftCheckpoint,
	validateKeybindingDraftCheckpoint,
	validateTranscriptDraftCheckpoint,
} from "@evener/appwire-client";
import {
	nativeKeybindingDrafts,
	nativeTranscriptDrafts,
	type NativePreferenceDraftBackend,
	retainingDraftStorage,
} from "./nativePreferenceDrafts";
import {
	type ConfirmedKeybindings,
	type ConfirmedTranscript,
	initialDomain,
	type NativePreferencesSnapshot,
	type PreferenceState,
} from "./nativePreferences";

/** One hub's persisted preference drafts, owned here whether or not a client
 * is bound: the ONLY thing that can classify and discard a stored draft
 * before a connected model exists (cold start) or once one has gone away
 * (backgrounded, reconnecting) - round 14's `NativePreferencesProvider`
 * finding was that `bound` stays null in exactly those states, so nothing
 * was exposed at all. This class never touches a client and never needs one:
 * every method reads or writes the same device ports the connected store
 * restores through.
 *
 * Storage is wrapped with retainingDraftStorage (round 17 Medium 1, eighth
 * raise of the discard-identity asymmetry): getSnapshot() is what classifies
 * a record, and discardTranscript/discardKeybindings must remove EXACTLY
 * that record, not a fresh reload - the render that showed "discard
 * unreadable draft" and the tap that presses it are two different moments,
 * and nothing re-classifies in between while this controller is the only
 * thing reading (no model, so no refresh attempt either). A fresh reload at
 * discard time would name whatever is stored NOW, which another store or app
 * version may have already replaced with a valid newer checkpoint.
 *
 * A connected model's own domain is the richer superset once one exists
 * (confirmed hub values, conflict, an authoritative writeUncertain) - the
 * provider reads that instead once bound, and reads this only for the gap a
 * model cannot cover. */
export class LocalPreferencesController {
	private readonly transcriptStorage;
	private readonly keybindingStorage;

	constructor(hubId: string, backend: NativePreferenceDraftBackend) {
		this.transcriptStorage = retainingDraftStorage<TranscriptDraftCheckpoint>(nativeTranscriptDrafts(hubId, backend));
		this.keybindingStorage = retainingDraftStorage<KeybindingDraftCheckpoint>(nativeKeybindingDrafts(hubId, backend));
	}

	/** Re-reads both sections' persisted drafts. Cheap (one JSON parse per
	 * section) and always current, so callers re-read after a discard rather
	 * than this class tracking listeners of its own. Each read is also what
	 * discardTranscript/discardKeybindings will later remove, whenever they
	 * are called next - see retainingDraftStorage. */
	getSnapshot(): NativePreferencesSnapshot {
		return {
			transcriptMobile: this.readTranscript(),
			keybindings: this.readKeybindings(),
		};
	}

	private readTranscript(): PreferenceState<ConfirmedTranscript> {
		const value = this.transcriptStorage.load();
		if (value === null || value === undefined) return initialDomain();
		try {
			const checkpoint = validateTranscriptDraftCheckpoint(value);
			const draft =
				checkpoint.layout === "mobile"
					? { revision: checkpoint.baseRevision, config: checkpoint.config }
					: null;
			return {
				...initialDomain<ConfirmedTranscript>(),
				draft,
				writeUncertain: checkpoint.writeUncertain,
			};
		} catch {
			return { ...initialDomain<ConfirmedTranscript>(), storageUnavailable: true, draftUnreadable: true };
		}
	}

	private readKeybindings(): PreferenceState<ConfirmedKeybindings> {
		const value = this.keybindingStorage.load();
		if (value === null || value === undefined) return initialDomain();
		try {
			const checkpoint = validateKeybindingDraftCheckpoint(value);
			return {
				...initialDomain<ConfirmedKeybindings>(),
				draft: { version: 1, revision: checkpoint.baseRevision, rules: checkpoint.rules },
				writeUncertain: checkpoint.writeUncertain,
			};
		} catch {
			return { ...initialDomain<ConfirmedKeybindings>(), storageUnavailable: true, draftUnreadable: true };
		}
	}

	/** Removes the record most recently classified by getSnapshot() - never a
	 * fresh reload, which could name (and remove) a record another store or
	 * app version has since replaced. Rejects (rather than throwing) on a
	 * port failure so a caller awaiting this inside an async handler gets the
	 * rejection its error handler already knows how to show. */
	async discardTranscript(): Promise<void> {
		this.transcriptStorage.discardLastLoaded();
	}

	async discardKeybindings(): Promise<void> {
		this.keybindingStorage.discardLastLoaded();
	}
}
