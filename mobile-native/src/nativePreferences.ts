import {
	createKeybindingsStore,
	createTranscriptDisplayStore,
	type FeatureSet,
	type KeybindingDraftStorage,
	type KeybindingsOverrides,
	type KeybindingsRule,
	type KeybindingsStore,
	type KeybindingsStoreState,
	type KeybindingsSupport,
	keybindingsSupport,
	type TranscriptDisplayConfigV1,
	type TranscriptDisplayStore,
	type TranscriptDisplayStoreState,
	transcriptDisplaySupport,
	type TranscriptDraftStorage,
} from "@evener/appwire-client";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";

type NativeFeatures = Pick<
	FeatureSet,
	"keybindingsSettings" | "transcriptDisplaySettings"
>;
export interface PreferenceState<T> {
	support: KeybindingsSupport;
	loading: boolean;
	saving: boolean;
	confirmed: T | null;
	draft: T | null;
	error: string | null;
	conflict: boolean;
	writeUncertain: boolean;
	storageUnavailable: boolean;
	/** The stored draft could not be read. Nothing is wrong with the hub or the
	 * port: the screen must offer an ENABLED discard, or the section is locked
	 * with no way out. */
	draftUnreadable: boolean;
}

/** The confirmed payload as the shared store holds it: its rule list is the
 * store's own (identity-stable until the next applied payload), read-only. */
export type ConfirmedKeybindings = Omit<KeybindingsOverrides, "rules"> & {
	rules: readonly KeybindingsRule[];
};

export interface NativePreferencesSnapshot {
	keybindings: PreferenceState<ConfirmedKeybindings>;
	transcriptMobile: PreferenceState<ConfirmedTranscript>;
}

const initialDomain = <T>(): PreferenceState<T> => ({
	support: "unknown",
	loading: false,
	saving: false,
	confirmed: null,
	draft: null,
	error: null,
	conflict: false,
	writeUncertain: false,
	storageUnavailable: false,
	draftUnreadable: false,
});

const HUB_UNCONFIRMED_MESSAGE = "The hub request could not be confirmed.";

const KEYBINDINGS_LOAD_ERROR_MESSAGE =
	"The hub could not load its saved shortcuts. Repair the hub settings file before editing.";

function hubErrorMessage(state: KeybindingsStoreState): string | null {
	if (state.loadError !== null) return KEYBINDINGS_LOAD_ERROR_MESSAGE;
	return state.hubError === null ? null : HUB_UNCONFIRMED_MESSAGE;
}

// The keybinding domain is a projection of the shared store's state: the
// confirmed payload is the store's revision and raw rules (the rule array is
// the store's own, a fresh one per applied payload and identity-stable
// otherwise, which the shortcut screen's preview memo keys on), and a
// hub-sourced failure surfaces as fixed copy (a raw client error may carry a
// token or a path) while the draft port's own messages pass through. An
// unconfirmed write is the `writeUncertain` fact, rendered as its own notice.
function keybindingsDomain(
	state: KeybindingsStoreState,
): PreferenceState<ConfirmedKeybindings> {
	return {
		support: state.hubSupport,
		loading: state.hubLoading,
		saving: state.saving,
		confirmed: state.loaded
			? {
					version: 1,
					revision: state.revision,
					rules: state.rawOverrides,
					...(state.loadError === null ? {} : { loadError: state.loadError }),
				}
			: null,
		draft: state.draft,
		error: state.draftError ?? hubErrorMessage(state),
		conflict: state.draftConflict,
		writeUncertain: state.writeUncertain,
		storageUnavailable: state.storageUnavailable,
		draftUnreadable: state.draftUnreadable,
	};
}

/** One layer's confirmed default as the screen shows it. */
export interface ConfirmedTranscript {
	revision: number;
	config: TranscriptDisplayConfigV1;
}

// The transcript display domain is a projection of the shared store's mobile
// layer: the confirmed value is the store's, present only while it is
// confirmed for the current connection; the draft is the store's checkpointed
// proposal; a hub-sourced failure surfaces as fixed copy while the draft
// port's own messages pass through; an unconfirmed write is the
// `writeUncertain` fact, rendered as its own notice.
function transcriptDomain(
	state: TranscriptDisplayStoreState,
): PreferenceState<ConfirmedTranscript> {
	const confirmed = state.loaded ? state.hub.mobile : undefined;
	const draft = state.draft?.layout === "mobile" ? state.draft : null;
	return {
		support: state.hubSupport,
		loading: state.hubLoading,
		saving: state.saving,
		confirmed: confirmed
			? { revision: confirmed.revision, config: confirmed.config }
			: null,
		draft: draft ? { revision: draft.revision, config: draft.config } : null,
		error:
			state.draftError ??
			(state.hubError === null ? null : HUB_UNCONFIRMED_MESSAGE),
		conflict: state.draftConflict,
		writeUncertain: state.writeUncertain,
		storageUnavailable: state.storageUnavailable,
		draftUnreadable: state.draftUnreadable,
	};
}

/** The snapshot after a LOCAL discard cleared one section's unreadable record.
 * Pure, so the provider's reachability table can be pinned without rendering a
 * screen. Only the section discarded changes: one discard clears one record. */
export function snapshotAfterLocalDiscard(
	snapshot: NativePreferencesSnapshot,
	section: "transcript" | "keybindings",
): NativePreferencesSnapshot {
	const key = section === "transcript" ? "transcriptMobile" : "keybindings";
	const domain = snapshot[key];
	if (!domain.draftUnreadable) return snapshot;
	return {
		...snapshot,
		[key]: {
			...domain,
			draft: null,
			draftUnreadable: false,
			// The record was the only reason the port read as unavailable, and the
			// restore failure was the only reason this section reported an error.
			storageUnavailable: false,
			conflict: false,
			error: null,
		},
	};
}

export class NativePreferences {
	private state: NativePreferencesSnapshot;
	private readonly listeners = new Set<() => void>();
	private readonly keybindings: KeybindingsStore;
	private readonly unsubscribeKeybindings: () => void;
	private readonly transcript: TranscriptDisplayStore;
	private readonly unsubscribeTranscript: () => void;
	private disposed = false;

	constructor(
		client: ConversationClientLike,
		features: NativeFeatures,
		transcriptStorage?: TranscriptDraftStorage,
		keybindingStorage?: KeybindingDraftStorage,
	) {
		// One store per model per domain, like one model per ready connection:
		// the features are the handshake's, so support is final and the one
		// ready generation lasts until dispose.
		this.keybindings = createKeybindingsStore({
			client,
			drafts: keybindingStorage,
		});
		this.keybindings.setSupport(keybindingsSupport(features));
		this.keybindings.beginReadyGeneration();
		this.transcript = createTranscriptDisplayStore({
			client,
			drafts: transcriptStorage,
		});
		this.transcript.setSupport(transcriptDisplaySupport(features));
		this.transcript.beginReadyGeneration();
		this.state = {
			keybindings: keybindingsDomain(this.keybindings.getState()),
			transcriptMobile: transcriptDomain(this.transcript.getState()),
		};
		this.unsubscribeKeybindings = this.keybindings.subscribe((state) =>
			this.publish({ keybindings: keybindingsDomain(state) }),
		);
		this.unsubscribeTranscript = this.transcript.subscribe((state) =>
			this.publish({ transcriptMobile: transcriptDomain(state) }),
		);
	}
	getSnapshot = (): NativePreferencesSnapshot => this.state;

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => this.listeners.delete(listener);
	};

	private publish(change: Partial<NativePreferencesSnapshot>): void {
		if (this.disposed) return;
		this.state = { ...this.state, ...change };
		for (const listener of this.listeners) listener();
	}

	async refresh(): Promise<void> {
		if (this.disposed) return;
		await Promise.all([
			this.keybindings.getState().refreshOverrides(),
			this.transcript.getState().refreshHubDefaults(),
		]);
	}

	// The draft editor is the shared store's; these stay async so a refused
	// edit rejects the way the screen awaits it.
	async editKeybindings(rules: readonly KeybindingsRule[]): Promise<void> {
		this.keybindings.getState().editDraft(rules);
	}

	async saveKeybindings(
		rules?: readonly KeybindingsRule[],
	): Promise<KeybindingsOverrides> {
		return this.keybindings.getState().saveDraft(rules);
	}

	async discardKeybindingsDraft(): Promise<void> {
		this.keybindings.getState().discardDraft();
	}

	async rebaseKeybindingsDraft(reviewedRevision: number): Promise<void> {
		this.keybindings.getState().rebaseDraft(reviewedRevision);
	}

	async saveTranscript(
		config?: TranscriptDisplayConfigV1,
	): Promise<ConfirmedTranscript> {
		return this.transcript.getState().saveDraft("mobile", config);
	}

	async editTranscript(config: TranscriptDisplayConfigV1): Promise<void> {
		this.transcript.getState().editDraft("mobile", config);
	}

	async discardTranscriptDraft(): Promise<void> {
		this.transcript.getState().discardDraft();
	}

	async rebaseTranscriptDraft(reviewedRevision: number): Promise<void> {
		this.transcript.getState().rebaseDraft(reviewedRevision);
	}

	dispose(): void {
		if (this.disposed) return;
		this.disposed = true;
		this.unsubscribeKeybindings();
		this.unsubscribeTranscript();
		this.keybindings.dispose();
		this.transcript.dispose();
		this.listeners.clear();
	}
}
