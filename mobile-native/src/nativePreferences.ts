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
	type TranscriptDisplayClient,
	type TranscriptDisplayConfigV1,
	type TranscriptDisplayStoreState,
	transcriptDisplaySupport,
	type TranscriptDraftStorage,
} from "@evener/appwire-client";

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
	/** The port answered but what it held could not be read - see the shared
	 * store's field of the same name. Both domains surface the real
	 * classification now that transcriptMobile is the same store's
	 * projection: an unreadable record is distinct from an absent one, and
	 * discarding it is what clears the notice. */
	draftUnreadable: boolean;
}

/** The confirmed payload as the shared store holds it: its rule list is the
 * store's own (identity-stable until the next applied payload), read-only. */
export type ConfirmedKeybindings = Omit<KeybindingsOverrides, "rules"> & {
	rules: readonly KeybindingsRule[];
};

export type KeybindingsPreferenceState = PreferenceState<ConfirmedKeybindings> & {
	draftError: string | null;
	hubError: string | null;
	loadError: string | null;
};

export type TranscriptMobilePreferenceState = PreferenceState<{
	revision: number;
	config: TranscriptDisplayConfigV1;
}>;

export interface NativePreferencesSnapshot {
	keybindings: KeybindingsPreferenceState;
	transcriptMobile: TranscriptMobilePreferenceState;
}

const KEYBINDINGS_LOAD_ERROR_MESSAGE =
	"The hub could not load its saved shortcuts. Repair the hub settings file before editing.";

const HUB_UNCONFIRMED_MESSAGE = "The hub request could not be confirmed.";

export function keybindingsErrorMessage(
	draftError: string | null,
	hubError: string | null,
	loadError: string | null,
): string | null {
	if (draftError !== null) return draftError;
	if (loadError !== null) return KEYBINDINGS_LOAD_ERROR_MESSAGE;
	return hubError === null ? null : HUB_UNCONFIRMED_MESSAGE;
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
): KeybindingsPreferenceState {
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
		// state.draft carries its own `generation` staleness stamp (see
		// readyGenerationFence.ts), which PreferenceState<ConfirmedKeybindings>
		// has no field for - stripped here rather than forwarded structurally.
		draft:
			state.draft === null
				? null
				: { version: state.draft.version, revision: state.draft.revision, rules: state.draft.rules },
		error: keybindingsErrorMessage(
			state.draftError,
			state.hubError,
			state.loadError,
		),
		conflict: state.draftConflict,
		writeUncertain: state.writeUncertain,
		storageUnavailable: state.storageUnavailable,
		draftUnreadable: state.draftUnreadable,
		draftError: state.draftError,
		hubError: state.hubError,
		loadError: state.loadError,
	};
}

// The transcript domain is the same projection over the shared transcript
// display store's mobile layout. That store owns the hub get/patch, the draft
// checkpoint and the ready-generation fence; this half only maps its fields
// onto the screen's own snapshot. The default's `generation` stamp is
// stripped for the same reason the keybindings draft's is (the screen's type
// has no field for it), and a hub-sourced failure is fixed copy. An
// unconfirmed write is `writeUncertain`; its message is that fact, so the
// screen's error slot and its write-uncertain notice never disagree.
function transcriptErrorMessage(
	state: TranscriptDisplayStoreState,
): string | null {
	if (state.draftError !== null) return state.draftError;
	return state.hubError !== null || state.writeUncertain
		? HUB_UNCONFIRMED_MESSAGE
		: null;
}

function transcriptDomain(
	state: TranscriptDisplayStoreState,
): TranscriptMobilePreferenceState {
	return {
		support: state.hubSupport,
		loading: state.hubLoading,
		saving: state.saving,
		confirmed: state.hub.mobile ?? null,
		draft:
			state.draft === null || state.draft.layout !== "mobile"
				? null
				: { revision: state.draft.revision, config: state.draft.config },
		error: transcriptErrorMessage(state),
		conflict: state.draftConflict,
		writeUncertain: state.writeUncertain,
		storageUnavailable: state.storageUnavailable,
		draftUnreadable: state.draftUnreadable,
	};
}

export class NativePreferences {
	private state: NativePreferencesSnapshot;
	private readonly listeners = new Set<() => void>();
	private readonly keybindings: KeybindingsStore;
	private readonly transcripts: ReturnType<typeof createTranscriptDisplayStore>;
	private readonly unsubscribeKeybindings: () => void;
	private readonly unsubscribeTranscripts: () => void;
	private disposed = false;

	constructor(
		client: TranscriptDisplayClient,
		features: NativeFeatures,
		transcriptStorage?: TranscriptDraftStorage,
		keybindingStorage?: KeybindingDraftStorage,
	) {
		// One store per model, like one model per ready connection: the
		// features are the handshake's, so support is final and the one ready
		// generation lasts until dispose. The keybindings half is the
		// template; transcriptMobile is the same projection, over
		// createTranscriptDisplayStore rather than a hand-rolled state
		// machine - the shared store owns the hub reads/writes, the draft
		// checkpoint and the generation fence.
		this.keybindings = createKeybindingsStore({
			client,
			drafts: keybindingStorage,
		});
		this.keybindings.setSupport(keybindingsSupport(features));
		this.keybindings.beginReadyGeneration();
		this.transcripts = createTranscriptDisplayStore({
			client,
			drafts: transcriptStorage,
		});
		this.transcripts.setSupport(transcriptDisplaySupport(features));
		this.transcripts.beginReadyGeneration();
		this.state = {
			keybindings: keybindingsDomain(this.keybindings.getState()),
			transcriptMobile: transcriptDomain(this.transcripts.getState()),
		};
		this.unsubscribeKeybindings = this.keybindings.subscribe((state) =>
			this.publish({ keybindings: keybindingsDomain(state) }),
		);
		this.unsubscribeTranscripts = this.transcripts.subscribe((state) =>
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
		// Both stores fence their own reads by the ready generation and the
		// support they were told; each refreshHubDefaults/refreshOverrides
		// gates on storageUnavailable itself.
		await Promise.all([
			this.keybindings.getState().refreshOverrides(),
			this.transcripts.getState().refreshHubDefaults(),
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
	): Promise<{ revision: number; config: TranscriptDisplayConfigV1 }> {
		return this.transcripts.getState().saveDraft("mobile", config);
	}

	async editTranscript(config: TranscriptDisplayConfigV1): Promise<void> {
		this.transcripts.getState().editDraft("mobile", config);
	}

	async discardTranscriptDraft(): Promise<void> {
		this.transcripts.getState().discardDraft();
	}

	async rebaseTranscriptDraft(reviewedRevision: number): Promise<void> {
		this.transcripts.getState().rebaseDraft(reviewedRevision);
	}

	dispose(): void {
		if (this.disposed) return;
		this.disposed = true;
		this.unsubscribeKeybindings();
		this.unsubscribeTranscripts();
		this.keybindings.dispose();
		this.transcripts.dispose();
		this.listeners.clear();
	}
}
